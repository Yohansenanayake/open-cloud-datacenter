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
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kvfake "kubevirt.io/client-go/kubevirt/fake"
	kvcorev1 "kubevirt.io/client-go/kubevirt/typed/core/v1"
)

func testTransportClient(t *testing.T, vmi *kubevirtv1.VirtualMachineInstance, opener ConsoleOpener) *Client {
	t.Helper()
	kubeClient := fake.NewSimpleClientset()
	return testTransportClientWithKubeClient(vmi, opener, kubeClient)
}

func testTransportClientWithKubeClient(vmi *kubevirtv1.VirtualMachineInstance, opener ConsoleOpener, kubeClient *fake.Clientset) *Client {
	return &Client{
		VMIs:              kvfake.NewSimpleClientset(vmi),
		Leases:            NewLeaseManager(kubeClient.CoordinationV1()),
		OpenConsole:       opener,
		NewHolderIdentity: func() (string, error) { return "test-holder", nil },
		ConnectTimeout:    time.Second,
		SessionDeadline:   3 * time.Second,
		SessionTimeouts:   testTimeouts(),
	}
}

func TestClientWaitsForRenewalBeforeReleasingLease(t *testing.T) {
	renewalStarted := make(chan struct{})
	allowRenewal := make(chan struct{})
	var renewalObserved atomic.Bool
	var renewalFinished atomic.Bool
	var releaseOvertookRenewal atomic.Bool

	kubeClient := fake.NewSimpleClientset()
	kubeClient.Fake.PrependReactor("update", "leases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		lease := action.(k8stesting.UpdateAction).GetObject().(*coordinationv1.Lease)
		if lease.Spec.HolderIdentity != nil {
			if renewalObserved.CompareAndSwap(false, true) {
				close(renewalStarted)
				<-allowRenewal
			}
			renewalFinished.Store(true)
		} else if !renewalFinished.Load() {
			releaseOvertookRenewal.Store(true)
		}
		return false, nil, nil
	})

	opener := func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go fakeProtocolGuest(server, fakeGuestOptions{
			singleWriteAfterLogin: true,
			beforeResponse:        renewalStarted,
		})
		return client, nil
	}
	client := testTransportClientWithKubeClient(testRunningVMI(), opener, kubeClient)
	client.Leases.RenewInterval = time.Millisecond
	client.Leases.Duration = time.Second

	go func() {
		<-renewalStarted
		time.Sleep(50 * time.Millisecond)
		close(allowRenewal)
	}()

	_, err := client.Execute(context.Background(), Target{
		Namespace: "tenant-a", VMIName: "pg-orders", InstanceUID: "orders-uid",
		Username: "dbaas-ops", Password: []byte("guest-password"),
	}, testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if releaseOvertookRenewal.Load() {
		t.Fatal("Lease was released before its in-flight renewal finished")
	}
}

func testRunningVMI() *kubevirtv1.VirtualMachineInstance {
	serialEnabled := true
	loggingEnabled := false
	return &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
		Spec: kubevirtv1.VirtualMachineInstanceSpec{Domain: kubevirtv1.DomainSpec{Devices: kubevirtv1.Devices{
			AutoattachSerialConsole: &serialEnabled,
			LogSerialConsole:        &loggingEnabled,
		}}},
		Status: kubevirtv1.VirtualMachineInstanceStatus{Phase: kubevirtv1.Running},
	}
}

func TestClientExecutesThroughExclusiveConsole(t *testing.T) {
	opener := func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go fakeProtocolGuest(server, fakeGuestOptions{singleWriteAfterLogin: true})
		return client, nil
	}
	client := testTransportClient(t, testRunningVMI(), opener)
	response, err := client.Execute(context.Background(), Target{
		Namespace: "tenant-a", VMIName: "pg-orders", InstanceUID: "orders-uid",
		Username: "dbaas-ops", Password: []byte("guest-password"),
	}, testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.RequestID != "request-1" {
		t.Fatalf("response = %+v", response)
	}
}

func TestClientRejectsUnsafeSerialSettings(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*kubevirtv1.VirtualMachineInstance)
		want   error
	}{
		{"serial disabled", func(vmi *kubevirtv1.VirtualMachineInstance) {
			disabled := false
			vmi.Spec.Domain.Devices.AutoattachSerialConsole = &disabled
		}, ErrSerialDisabled},
		{"serial logging enabled", func(vmi *kubevirtv1.VirtualMachineInstance) {
			enabled := true
			vmi.Spec.Domain.Devices.LogSerialConsole = &enabled
		}, ErrSerialLoggingEnabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			vmi := testRunningVMI()
			test.mutate(vmi)
			client := testTransportClient(t, vmi, func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("unsafe VMI must be rejected before opening the console")
				return nil, nil
			})
			_, err := client.Execute(context.Background(), Target{
				Namespace: "tenant-a", VMIName: "pg-orders", InstanceUID: "orders-uid",
				Username: "dbaas-ops", Password: []byte("guest-password"),
			}, testRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestClientReturnsBusyBeforeOpeningConsole(t *testing.T) {
	client := testTransportClient(t, testRunningVMI(), func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("busy console must not be opened")
		return nil, nil
	})
	ctx := context.Background()
	if _, err := client.Leases.Acquire(ctx, "tenant-a", "orders-uid", "other-holder"); err != nil {
		t.Fatal(err)
	}
	_, err := client.Execute(ctx, Target{
		Namespace: "tenant-a", VMIName: "pg-orders", InstanceUID: "orders-uid",
		Username: "dbaas-ops", Password: []byte("guest-password"),
	}, testRequest())
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("error = %v, want ErrBusy", err)
	}
}

func TestConsoleOpenRetriesTransientKubeVirtBusyResponse(t *testing.T) {
	attempts := 0
	want := &fakeStream{}
	stream, err := openConsoleStreamWithRetry(context.Background(), time.Millisecond, func() (kvcorev1.StreamInterface, error) {
		attempts++
		if attempts < 3 {
			return nil, &kvcorev1.AsyncSubresourceError{StatusCode: http.StatusBadRequest}
		}
		return want, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || stream != want {
		t.Fatalf("attempts = %d, stream = %T; want 3 attempts and the successful stream", attempts, stream)
	}
}

func TestConsoleOpenDoesNotRetryPermanentError(t *testing.T) {
	attempts := 0
	want := errors.New("forbidden")
	_, err := openConsoleStreamWithRetry(context.Background(), time.Millisecond, func() (kvcorev1.StreamInterface, error) {
		attempts++
		return nil, want
	})
	if !errors.Is(err, want) || attempts != 1 {
		t.Fatalf("error = %v, attempts = %d; want permanent error after one attempt", err, attempts)
	}
}

func TestConsoleOpenRetryStopsAtContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := openConsoleStreamWithRetry(ctx, time.Hour, func() (kvcorev1.StreamInterface, error) {
		return nil, &kvcorev1.AsyncSubresourceError{StatusCode: http.StatusBadRequest}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
}

type fakeStream struct{}

func (*fakeStream) Stream(kvcorev1.StreamOptions) error { return nil }
func (*fakeStream) AsConn() net.Conn                    { return nil }
