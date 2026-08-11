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

	"golang.org/x/sys/unix"

	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestops"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

func TestConsoleRunProbe(t *testing.T) {
	request := consoleRequest()
	requestFrame, err := guestprotocol.EncodeFrame(request)
	if err != nil {
		t.Fatal(err)
	}
	terminalConfigured := false
	terminalRestored := false
	runner := func(_ context.Context, frame []byte) ([]byte, error) {
		if !terminalConfigured {
			t.Fatal("privileged helper ran before terminal configuration")
		}
		forwardedFrame, err := guestprotocol.ReadFrame(bytes.NewReader(frame))
		if err != nil {
			return nil, err
		}
		forwarded, err := guestprotocol.DecodeRequest(forwardedFrame)
		if err != nil {
			return nil, err
		}
		response := guestprotocol.Success(forwarded.RequestID, guestops.Version, map[string]string{"configuredInstanceUID": forwarded.InstanceUID})
		return guestprotocol.EncodeFrame(response)
	}
	var stdout, stderr bytes.Buffer
	exitCode := run(nil, bytes.NewReader(requestFrame), &stdout, &stderr, instanceReader("instance-123"), runner, func() (func() error, error) {
		if stdout.Len() != 0 {
			t.Fatal("ready marker was written before terminal configuration")
		}
		terminalConfigured = true
		return func() error {
			terminalRestored = true
			return nil
		}, nil
	})
	if exitCode != exitOK || stderr.Len() != 0 || !terminalConfigured || !terminalRestored {
		t.Fatalf("exit=%d stderr=%q configured=%t restored=%t", exitCode, stderr.String(), terminalConfigured, terminalRestored)
	}
	response := decodeConsoleResponse(t, stdout.String())
	if err := guestprotocol.ValidateResponse(response, request.RequestID); err != nil {
		t.Fatal(err)
	}
	if response.State != guestprotocol.StateSucceeded {
		t.Fatalf("response = %+v", response)
	}
}

func TestConsoleRejectsInvalidRequestsBeforePrivilegeBoundary(t *testing.T) {
	tests := []struct {
		name   string
		input  func() []byte
		reader func(string) ([]byte, error)
		code   guestprotocol.ErrorCode
	}{
		{"malformed", func() []byte { return []byte("not-base64!\n") }, instanceReader("instance-123"), guestprotocol.CodeInvalidRequest},
		{"arbitrary operation", func() []byte {
			request := consoleRequest()
			request.Operation = "shell"
			frame, _ := guestprotocol.EncodeFrame(request)
			return frame
		}, instanceReader("instance-123"), guestprotocol.CodeInvalidRequest},
		{"instance mismatch", func() []byte {
			frame, _ := guestprotocol.EncodeFrame(consoleRequest())
			return frame
		}, instanceReader("different-instance"), guestprotocol.CodeInstanceMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runnerCalled := false
			var stdout, stderr bytes.Buffer
			exitCode := run(nil, bytes.NewReader(test.input()), &stdout, &stderr, test.reader, func(context.Context, []byte) ([]byte, error) {
				runnerCalled = true
				return nil, nil
			}, noopTerminal)
			if exitCode != exitOK || runnerCalled {
				t.Fatalf("exit=%d runnerCalled=%t", exitCode, runnerCalled)
			}
			response := decodeConsoleResponse(t, stdout.String())
			if response.Error == nil || response.Error.Code != test.code {
				t.Fatalf("response = %+v", response)
			}
		})
	}
}

func TestConsoleRejectsMismatchedOrMalformedPrivilegedResponse(t *testing.T) {
	tests := []struct {
		name   string
		runner backupctlRunner
	}{
		{"wrong request ID", func(context.Context, []byte) ([]byte, error) {
			return guestprotocol.EncodeFrame(guestprotocol.Success("wrong", guestops.Version, struct{}{}))
		}},
		{"malformed response", func(context.Context, []byte) ([]byte, error) { return []byte("bad\n"), nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestFrame, _ := guestprotocol.EncodeFrame(consoleRequest())
			var stdout, stderr bytes.Buffer
			if got := run(nil, bytes.NewReader(requestFrame), &stdout, &stderr, instanceReader("instance-123"), test.runner, noopTerminal); got != exitOK {
				t.Fatalf("exit = %d", got)
			}
			response := decodeConsoleResponse(t, stdout.String())
			if response.Error == nil || response.Error.Code != guestprotocol.CodeProtocolViolation || response.RequestID != "request-123" {
				t.Fatalf("response = %+v", response)
			}
		})
	}
}

func TestConsoleDoesNotLeakRunnerError(t *testing.T) {
	request := consoleRequest()
	request.Payload = json.RawMessage(`{}`)
	requestFrame, _ := guestprotocol.EncodeFrame(request)
	secret := "sensitive-child-process-detail"
	var stdout, stderr bytes.Buffer
	if got := run(nil, bytes.NewReader(requestFrame), &stdout, &stderr, instanceReader("instance-123"), func(context.Context, []byte) ([]byte, error) {
		return nil, errors.New(secret)
	}, noopTerminal); got != exitOK {
		t.Fatalf("exit = %d", got)
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("output leaked child error: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestConsoleRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"id"}, bytes.NewReader(nil), &stdout, &stderr, instanceReader("instance-123"), nil, noopTerminal); got != exitFailure {
		t.Fatalf("exit = %d", got)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "not accepted") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestConsoleDoesNotAnnounceReadyWhenTerminalSetupFails(t *testing.T) {
	requestFrame, err := guestprotocol.EncodeFrame(consoleRequest())
	if err != nil {
		t.Fatal(err)
	}
	secret := "sensitive-terminal-error"
	runnerCalled := false
	var stdout, stderr bytes.Buffer
	exitCode := run(nil, bytes.NewReader(requestFrame), &stdout, &stderr, instanceReader("instance-123"), func(context.Context, []byte) ([]byte, error) {
		runnerCalled = true
		return nil, nil
	}, func() (func() error, error) {
		return nil, errors.New(secret)
	})
	if exitCode != exitFailure || runnerCalled {
		t.Fatalf("exit=%d runnerCalled=%t", exitCode, runnerCalled)
	}
	if stdout.Len() != 0 {
		t.Fatalf("terminal failure announced readiness: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "terminal setup failed") || strings.Contains(stderr.String(), secret) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestProtocolTermios(t *testing.T) {
	initial := unix.Termios{Lflag: unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG}
	initial.Cc[unix.VMIN] = 9
	initial.Cc[unix.VTIME] = 7

	configured := protocolTermios(initial)
	if configured.Lflag&(unix.ECHO|unix.ECHONL|unix.ICANON) != 0 {
		t.Fatalf("protocol terminal flags were not cleared: %#x", configured.Lflag)
	}
	if configured.Lflag&unix.ISIG == 0 {
		t.Fatal("unrelated terminal flags were changed")
	}
	if configured.Cc[unix.VMIN] != 1 || configured.Cc[unix.VTIME] != 0 {
		t.Fatalf("VMIN=%d VTIME=%d", configured.Cc[unix.VMIN], configured.Cc[unix.VTIME])
	}
	if initial.Lflag&unix.ICANON == 0 || initial.Cc[unix.VMIN] != 9 || initial.Cc[unix.VTIME] != 7 {
		t.Fatal("input terminal state was modified")
	}
}

func decodeConsoleResponse(t *testing.T, output string) guestprotocol.Response {
	t.Helper()
	ready, remainder, ok := strings.Cut(output, "\n")
	if !ok || ready != guestprotocol.ReadyMarker {
		t.Fatalf("missing ready marker in %q", output)
	}
	frame, err := guestprotocol.ReadFrame(strings.NewReader(remainder))
	if err != nil {
		t.Fatal(err)
	}
	response, err := guestprotocol.DecodeResponse(frame)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func consoleRequest() guestprotocol.Request {
	return guestprotocol.Request{
		ProtocolVersion: guestprotocol.Version,
		RequestID:       "request-123",
		InstanceUID:     "instance-123",
		Operation:       guestprotocol.OperationProbe,
		Payload:         json.RawMessage(`{}`),
	}
}

func instanceReader(uid string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if path != guestops.InstanceUIDPath {
			return nil, errors.New("unexpected path")
		}
		return []byte(uid), nil
	}
}

func noopTerminal() (func() error, error) {
	return func() error { return nil }, nil
}
