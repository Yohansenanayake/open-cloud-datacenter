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

	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestops"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

func TestRunProbe(t *testing.T) {
	request := backupctlRequest()
	requestFrame, err := guestprotocol.EncodeFrame(request)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := run(nil, bytes.NewReader(requestFrame), &stdout, &stderr, func() int { return 0 }, guestops.Dependencies{
		ReadFile: func(path string) ([]byte, error) {
			if path == guestops.InstanceUIDPath {
				return []byte("instance-123"), nil
			}
			return nil, errors.New("not found")
		},
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("not installed")
		},
		GOOS:   "linux",
		GOARCH: "amd64",
	})
	if exitCode != exitOK || stderr.Len() != 0 {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
	response := decodeBackupctlResponse(t, stdout.Bytes())
	if err := guestprotocol.ValidateResponse(response, request.RequestID); err != nil {
		t.Fatal(err)
	}
	if response.State != guestprotocol.StateSucceeded {
		t.Fatalf("response = %+v", response)
	}
}

func TestRunRequiresRootAndRejectsArguments(t *testing.T) {
	requestFrame, err := guestprotocol.EncodeFrame(backupctlRequest())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if got := run(nil, bytes.NewReader(requestFrame), &stdout, &stderr, func() int { return 1000 }, guestops.Dependencies{}); got != exitOK {
		t.Fatalf("non-root protocol failure exit = %d", got)
	}
	response := decodeBackupctlResponse(t, stdout.Bytes())
	if response.Error == nil || response.Error.Code != guestprotocol.CodeOperationFailed {
		t.Fatalf("response = %+v", response)
	}

	stdout.Reset()
	if got := run([]string{"shell"}, bytes.NewReader(nil), &stdout, &stderr, func() int { return 0 }, guestops.Dependencies{}); got != exitFailure {
		t.Fatalf("argument exit = %d", got)
	}
}

func TestRunMalformedRequestDoesNotLeakInput(t *testing.T) {
	secret := "do-not-return-this-secret"
	var stdout, stderr bytes.Buffer
	if got := run(nil, strings.NewReader(secret+"\n"), &stdout, &stderr, func() int { return 0 }, guestops.Dependencies{}); got != exitOK {
		t.Fatalf("exit = %d", got)
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("output leaked input: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	response := decodeBackupctlResponse(t, stdout.Bytes())
	if response.Error == nil || response.Error.Code != guestprotocol.CodeInvalidRequest {
		t.Fatalf("response = %+v", response)
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"--version"}, bytes.NewReader(nil), &stdout, &stderr, func() int { return 1000 }, guestops.Dependencies{}); got != exitOK {
		t.Fatalf("exit = %d", got)
	}
	if strings.TrimSpace(stdout.String()) != guestops.Version || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func decodeBackupctlResponse(t *testing.T, output []byte) guestprotocol.Response {
	t.Helper()
	frame, err := guestprotocol.ReadFrame(bytes.NewReader(output))
	if err != nil {
		t.Fatal(err)
	}
	response, err := guestprotocol.DecodeResponse(frame)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func backupctlRequest() guestprotocol.Request {
	return guestprotocol.Request{
		ProtocolVersion: guestprotocol.Version,
		RequestID:       "request-123",
		InstanceUID:     "instance-123",
		Operation:       guestprotocol.OperationProbe,
		Payload:         json.RawMessage(`{}`),
	}
}
