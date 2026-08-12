/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package guestconsole

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kvclientset "kubevirt.io/client-go/kubevirt"
	kvcorev1 "kubevirt.io/client-go/kubevirt/typed/core/v1"

	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

var (
	ErrConnect              = errors.New("guest console connection failed")
	ErrSerialDisabled       = errors.New("VMI serial console is disabled")
	ErrSerialLoggingEnabled = errors.New("VMI serial console logging is enabled")
	ErrLeaseRelease         = errors.New("guest console lease release failed")
)

const consoleConnectRetryInterval = time.Second

type ConsoleOpener func(ctx context.Context, namespace, vmiName string) (net.Conn, error)

type Client struct {
	VMIs              kvclientset.Interface
	Leases            *LeaseManager
	OpenConsole       ConsoleOpener
	NewHolderIdentity func() (string, error)
	ConnectTimeout    time.Duration
	SessionDeadline   time.Duration
	SessionTimeouts   SessionTimeouts
}

type Target struct {
	Namespace   string
	VMIName     string
	InstanceUID string
	Username    string
	Password    []byte
}

func NewClient(config *rest.Config) (*Client, error) {
	vmiClient, err := kvclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Client{
		VMIs:              vmiClient,
		Leases:            NewLeaseManager(kubeClient.CoordinationV1()),
		OpenConsole:       kubeVirtConsoleOpener(config),
		NewHolderIdentity: randomHolderIdentity,
		ConnectTimeout:    10 * time.Second,
		SessionDeadline:   60 * time.Second,
		SessionTimeouts:   DefaultSessionTimeouts(),
	}, nil
}

// Execute opens one exclusive console session and returns one validated guest
// response. The request and console transcript are never included in errors.
func (c *Client) Execute(ctx context.Context, target Target, request guestprotocol.Request) (response guestprotocol.Response, err error) {
	if c == nil || c.VMIs == nil || c.Leases == nil || c.OpenConsole == nil || c.NewHolderIdentity == nil {
		return response, errors.New("guest console client is incomplete")
	}
	if target.Namespace == "" || target.VMIName == "" || target.InstanceUID == "" || target.Username == "" || len(target.Password) == 0 {
		return response, errors.New("guest console target is incomplete")
	}
	if request.InstanceUID != target.InstanceUID || guestprotocol.ValidateRequest(request, guestprotocol.OperationProbe) != nil {
		return response, ErrProtocol
	}
	if c.ConnectTimeout <= 0 || c.SessionDeadline <= 0 {
		return response, errors.New("guest console client timeouts are invalid")
	}

	sessionCtx, cancelSession := context.WithTimeout(ctx, c.SessionDeadline)
	defer cancelSession()
	connectCtx, cancelConnect := context.WithTimeout(sessionCtx, c.ConnectTimeout)
	vmi, getErr := c.VMIs.KubevirtV1().VirtualMachineInstances(target.Namespace).Get(connectCtx, target.VMIName, metav1.GetOptions{})
	if getErr != nil {
		cancelConnect()
		return response, ErrConnect
	}
	if vmi.Status.Phase != kubevirtv1.Running {
		cancelConnect()
		return response, ErrConnect
	}
	devices := vmi.Spec.Domain.Devices
	if devices.AutoattachSerialConsole == nil || !*devices.AutoattachSerialConsole {
		cancelConnect()
		return response, ErrSerialDisabled
	}
	if devices.LogSerialConsole == nil || *devices.LogSerialConsole {
		cancelConnect()
		return response, ErrSerialLoggingEnabled
	}

	holder, holderErr := c.NewHolderIdentity()
	if holderErr != nil {
		cancelConnect()
		return response, ErrConnect
	}
	guard, acquireErr := c.Leases.Acquire(connectCtx, target.Namespace, target.InstanceUID, holder)
	if acquireErr != nil {
		cancelConnect()
		if errors.Is(acquireErr, ErrBusy) {
			return response, ErrBusy
		}
		return response, ErrConnect
	}

	renewCtx, cancelRenew := context.WithCancel(sessionCtx)
	leaseErrors := guard.RenewLoop(renewCtx)
	operationCtx, cancelOperation := context.WithCancelCause(sessionCtx)
	go func() {
		if leaseErr, ok := <-leaseErrors; ok && leaseErr != nil {
			cancelOperation(leaseErr)
		}
	}()
	defer func() {
		cancelOperation(nil)
		cancelRenew()
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		if releaseErr := guard.Release(releaseCtx); releaseErr != nil && err == nil {
			response = guestprotocol.Response{}
			err = ErrLeaseRelease
		}
	}()

	conn, openErr := c.OpenConsole(connectCtx, target.Namespace, target.VMIName)
	cancelConnect()
	if openErr != nil {
		return response, ErrConnect
	}
	defer conn.Close()

	response, err = RunSession(operationCtx, conn, target.Username, target.Password, request, c.SessionTimeouts)
	if errors.Is(context.Cause(operationCtx), ErrLeaseLost) {
		return guestprotocol.Response{}, ErrLeaseLost
	}
	return response, err
}

func randomHolderIdentity() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "dbaas-guest-" + hex.EncodeToString(random), nil
}

func kubeVirtConsoleOpener(config *rest.Config) ConsoleOpener {
	return func(ctx context.Context, namespace, vmiName string) (net.Conn, error) {
		stream, err := openConsoleStreamWithRetry(ctx, consoleConnectRetryInterval, func() (kvcorev1.StreamInterface, error) {
			return kvcorev1.AsyncSubresourceHelper(config, "virtualmachineinstances", namespace, vmiName, "console", nil)
		})
		if err != nil {
			return nil, err
		}
		return bridgeStream(stream), nil
	}
}

// KubeVirt returns HTTP 400 while a previous serial-console WebSocket is
// still being released. Retry only that known transient response and keep all
// attempts inside the connect timeout.
func openConsoleStreamWithRetry(
	ctx context.Context,
	retryInterval time.Duration,
	open func() (kvcorev1.StreamInterface, error),
) (kvcorev1.StreamInterface, error) {
	for {
		stream, err := openConsoleStreamAttempt(ctx, open)
		if err == nil {
			return stream, nil
		}
		var subresourceError *kvcorev1.AsyncSubresourceError
		if !errors.As(err, &subresourceError) || subresourceError.GetStatusCode() != http.StatusBadRequest {
			return nil, err
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func openConsoleStreamAttempt(
	ctx context.Context,
	open func() (kvcorev1.StreamInterface, error),
) (kvcorev1.StreamInterface, error) {
	type result struct {
		stream kvcorev1.StreamInterface
		err    error
	}
	opened := make(chan result, 1)
	go func() {
		stream, err := open()
		opened <- result{stream: stream, err: err}
	}()

	select {
	case <-ctx.Done():
		// If the KubeVirt helper finishes after our deadline, start and close
		// its stream so its internal HTTP round tripper can exit cleanly.
		go func() {
			late := <-opened
			if late.err == nil {
				closeUnusedStream(late.stream)
			}
		}()
		return nil, ctx.Err()
	case result := <-opened:
		return result.stream, result.err
	}
}

// bridgeStream gives the state machine a net.Conn while still using Stream,
// whose cleanup closes KubeVirt's internal completion channel. AsConn alone
// does not perform that cleanup in KubeVirt v1.6.0.
func bridgeStream(stream kvcorev1.StreamInterface) net.Conn {
	local, remote := net.Pipe()
	go func() {
		defer remote.Close()
		_ = stream.Stream(kvcorev1.StreamOptions{In: remote, Out: remote})
	}()
	return local
}

func closeUnusedStream(stream kvcorev1.StreamInterface) {
	conn := bridgeStream(stream)
	_ = conn.Close()
}
