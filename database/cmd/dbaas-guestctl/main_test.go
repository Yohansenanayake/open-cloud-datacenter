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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestconsole"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

func validArgs() []string {
	return []string{"probe", "--namespace", "tenant-a", "--dbinstance", "orders"}
}

func TestRunPrintsStructuredProbeResult(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run(validArgs(), &stdout, &stderr, func(context.Context, options) (guestprotocol.Response, error) {
		return guestprotocol.Success("request-1", "0.1.0", map[string]string{"configuredInstanceUID": "orders-uid"}), nil
	})
	if exitCode != exitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
	var response guestprotocol.Response
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("stdout is not structured JSON: %v", err)
	}
	if response.State != guestprotocol.StateSucceeded {
		t.Fatalf("response = %+v", response)
	}
}

func TestRunMapsBusyAndDoesNotPrintRawError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	secret := "never-print-this"
	exitCode := run(validArgs(), &stdout, &stderr, func(context.Context, options) (guestprotocol.Response, error) {
		return guestprotocol.Response{}, errors.Join(guestconsole.ErrBusy, errors.New(secret))
	})
	if exitCode != exitBusy || stdout.Len() != 0 {
		t.Fatalf("exit = %d, stdout = %q", exitCode, stdout.String())
	}
	if strings.Contains(stderr.String(), secret) {
		t.Fatalf("stderr leaked raw error: %q", stderr.String())
	}
}

func TestRunMapsProtocolFailureWithoutPrintingRawError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	secret := "never-print-this"
	exitCode := run(validArgs(), &stdout, &stderr, func(context.Context, options) (guestprotocol.Response, error) {
		return guestprotocol.Response{}, errors.Join(guestconsole.ErrProtocol, errors.New(secret))
	})
	if exitCode != exitGuestFailed || stdout.Len() != 0 {
		t.Fatalf("exit = %d, stdout = %q", exitCode, stdout.String())
	}
	if got := stderr.String(); strings.Contains(got, secret) || !strings.Contains(got, "guest operation failed") {
		t.Fatalf("stderr = %q", got)
	}
}

func TestParseOptionsRejectsUnsupportedInput(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"shell"},
		{"probe", "--namespace", "tenant-a"},
		{"probe", "--namespace", "tenant-a", "--dbinstance", "orders", "extra"},
		{"probe", "--namespace", "tenant-a", "--dbinstance", "orders", "--timeout", "61s"},
		{"probe", "--namespace", "tenant-a", "--dbinstance", "orders", "--vmi", "pg-orders"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("parseOptions(%v) succeeded", args)
		}
	}
}

func TestTargetVMINameComesFromDBInstanceStatus(t *testing.T) {
	instance := &dbaasv1.DBInstance{}
	instance.Status.Resources.VMName = "pg-orders"
	name, err := targetVMIName(instance)
	if err != nil || name != "pg-orders" {
		t.Fatalf("targetVMIName = %q, %v", name, err)
	}

	instance.Status.Resources.VMName = ""
	if _, err := targetVMIName(instance); err == nil {
		t.Fatal("accepted a DBInstance without a provisioned VM")
	}
}

func TestGuestCredentialRequiresExactRestrictedUser(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		credentials.GuestAccessUsernameKey: []byte(credentials.GuestOpsUsername),
		credentials.GuestAccessPasswordKey: []byte("guest-password"),
	}}
	username, password, err := guestCredential(secret)
	if err != nil || username != credentials.GuestOpsUsername || string(password) != "guest-password" {
		t.Fatalf("credential = %q/%q, %v", username, password, err)
	}
	secret.Data[credentials.GuestAccessUsernameKey] = []byte("root")
	if _, _, err := guestCredential(secret); err == nil {
		t.Fatal("accepted unexpected guest username")
	}
}
