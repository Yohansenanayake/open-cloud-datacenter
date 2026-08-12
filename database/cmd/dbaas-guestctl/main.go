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

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestconsole"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

const (
	exitOK          = 0
	exitFailure     = 1
	exitInput       = 2
	exitConnect     = 3
	exitBusy        = 4
	exitGuestFailed = 5
)

type options struct {
	kubeconfig        string
	contextName       string
	namespace         string
	dbInstanceName    string
	operatorNamespace string
	timeout           time.Duration
}

type probeRunner func(ctx context.Context, options options) (guestprotocol.Response, error)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, runProbe))
}

func run(args []string, stdout, stderr io.Writer, probe probeRunner) int {
	parsed, err := parseOptions(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid input:", err)
		return exitInput
	}
	ctx, cancel := context.WithTimeout(context.Background(), parsed.timeout)
	defer cancel()
	response, err := probe(ctx, parsed)
	if err != nil {
		switch {
		case errors.Is(err, guestconsole.ErrBusy):
			_, _ = fmt.Fprintln(stderr, "probe failed: guest console is busy")
			return exitBusy
		case errors.Is(err, guestconsole.ErrLogin):
			_, _ = fmt.Fprintln(stderr, "probe failed: guest login failed")
			return exitGuestFailed
		case errors.Is(err, guestconsole.ErrProtocol),
			errors.Is(err, guestconsole.ErrLogout),
			errors.Is(err, guestconsole.ErrLeaseLost):
			_, _ = fmt.Fprintln(stderr, "probe failed: guest operation failed")
			return exitGuestFailed
		default:
			_, _ = fmt.Fprintln(stderr, "probe failed: guest console connection failed")
			return exitConnect
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(response); err != nil {
		_, _ = fmt.Fprintln(stderr, "unable to print probe result")
		return exitFailure
	}
	if response.State == guestprotocol.StateFailed {
		return exitGuestFailed
	}
	return exitOK
}

func parseOptions(args []string) (options, error) {
	var parsed options
	if len(args) == 0 || args[0] != "probe" {
		return parsed, errors.New("the only supported command is probe")
	}
	flags := flag.NewFlagSet("dbaas-guestctl probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&parsed.kubeconfig, "kubeconfig", "", "path to kubeconfig")
	flags.StringVar(&parsed.contextName, "context", "", "kubeconfig context")
	flags.StringVar(&parsed.namespace, "namespace", "", "DBInstance namespace")
	flags.StringVar(&parsed.dbInstanceName, "dbinstance", "", "DBInstance name")
	flags.StringVar(&parsed.operatorNamespace, "operator-namespace", "dbaas-system", "operator Secret namespace")
	flags.DurationVar(&parsed.timeout, "timeout", 60*time.Second, "overall command timeout")
	if err := flags.Parse(args[1:]); err != nil {
		return parsed, err
	}
	if flags.NArg() != 0 {
		return parsed, errors.New("positional arguments are not accepted")
	}
	for name, value := range map[string]string{
		"namespace":          parsed.namespace,
		"dbinstance":         parsed.dbInstanceName,
		"operator-namespace": parsed.operatorNamespace,
	} {
		if strings.TrimSpace(value) == "" {
			return parsed, fmt.Errorf("--%s is required", name)
		}
	}
	if parsed.timeout <= 0 || parsed.timeout > 60*time.Second {
		return parsed, errors.New("--timeout must be greater than zero and no more than 60s")
	}
	return parsed, nil
}

func runProbe(ctx context.Context, parsed options) (guestprotocol.Response, error) {
	config, err := buildRESTConfig(parsed)
	if err != nil {
		return guestprotocol.Response{}, guestconsole.ErrConnect
	}
	clusterClient, err := newClusterClient(config)
	if err != nil {
		return guestprotocol.Response{}, guestconsole.ErrConnect
	}
	var instance dbaasv1.DBInstance
	if err := clusterClient.Get(ctx, types.NamespacedName{Namespace: parsed.namespace, Name: parsed.dbInstanceName}, &instance); err != nil {
		return guestprotocol.Response{}, guestconsole.ErrConnect
	}
	if instance.UID == "" {
		return guestprotocol.Response{}, guestconsole.ErrConnect
	}
	vmiName, err := targetVMIName(&instance)
	if err != nil {
		return guestprotocol.Response{}, guestconsole.ErrConnect
	}
	var secret corev1.Secret
	secretKey := types.NamespacedName{
		Namespace: parsed.operatorNamespace,
		Name:      credentials.GuestAccessSecretName(&instance),
	}
	if err := clusterClient.Get(ctx, secretKey, &secret); err != nil {
		return guestprotocol.Response{}, guestconsole.ErrConnect
	}
	username, password, err := guestCredential(&secret)
	if err != nil {
		return guestprotocol.Response{}, guestconsole.ErrConnect
	}
	defer zero(password)

	requestID, err := newRequestID()
	if err != nil {
		return guestprotocol.Response{}, guestconsole.ErrConnect
	}
	consoleClient, err := guestconsole.NewClient(config)
	if err != nil {
		return guestprotocol.Response{}, guestconsole.ErrConnect
	}
	consoleClient.SessionDeadline = parsed.timeout
	return consoleClient.Execute(ctx, guestconsole.Target{
		Namespace:   parsed.namespace,
		VMIName:     vmiName,
		InstanceUID: string(instance.UID),
		Username:    username,
		Password:    password,
	}, guestprotocol.Request{
		ProtocolVersion: guestprotocol.Version,
		RequestID:       requestID,
		InstanceUID:     string(instance.UID),
		Operation:       guestprotocol.OperationProbe,
		Payload:         json.RawMessage(`{}`),
	})
}

func targetVMIName(instance *dbaasv1.DBInstance) (string, error) {
	if instance == nil || strings.TrimSpace(instance.Status.Resources.VMName) == "" {
		return "", errors.New("DBInstance has no provisioned VM")
	}
	return instance.Status.Resources.VMName, nil
}

func buildRESTConfig(parsed options) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if parsed.kubeconfig != "" {
		rules.ExplicitPath = parsed.kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: parsed.contextName}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

func newClusterClient(config *rest.Config) (ctrlclient.Client, error) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := dbaasv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return ctrlclient.New(config, ctrlclient.Options{Scheme: scheme})
}

func guestCredential(secret *corev1.Secret) (string, []byte, error) {
	username := string(secret.Data[credentials.GuestAccessUsernameKey])
	password := append([]byte(nil), secret.Data[credentials.GuestAccessPasswordKey]...)
	if username != credentials.GuestOpsUsername || len(password) == 0 {
		zero(password)
		return "", nil, errors.New("guest credential is invalid")
	}
	return username, password, nil
}

func newRequestID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "guestctl-" + hex.EncodeToString(random), nil
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
