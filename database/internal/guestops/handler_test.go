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

package guestops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

func TestHandleProbe(t *testing.T) {
	request := validProbeRequest()
	commands := make([]string, 0)
	dependencies := Dependencies{
		ReadFile: func(path string) ([]byte, error) {
			switch path {
			case InstanceUIDPath:
				return []byte("instance-123\n"), nil
			case OSReleasePath:
				return []byte("ID=ubuntu\nNAME=Ubuntu\nVERSION_ID=\"24.04\"\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\n"), nil
			default:
				return nil, errors.New("unexpected path")
			}
		},
		RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			commands = append(commands, name+" "+args[0])
			switch name {
			case "postgres":
				return nil, errors.New("not installed in PATH")
			case "psql":
				return []byte("psql (PostgreSQL) 16.3\n"), nil
			case "pgbackrest":
				return []byte("pgBackRest 2.53.1\n"), nil
			default:
				return nil, errors.New("unexpected command")
			}
		},
		GOOS:   "linux",
		GOARCH: "amd64",
	}

	response := Handle(context.Background(), request, dependencies)
	if err := guestprotocol.ValidateResponse(response, request.RequestID); err != nil {
		t.Fatal(err)
	}
	if response.State != guestprotocol.StateSucceeded {
		t.Fatalf("response = %+v", response)
	}
	var result ProbeResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	want := ProbeResult{
		ConfiguredInstanceUID: "instance-123",
		OS: OSInfo{
			GOOS:       "linux",
			GOARCH:     "amd64",
			ID:         "ubuntu",
			Name:       "Ubuntu",
			VersionID:  "24.04",
			PrettyName: "Ubuntu 24.04 LTS",
		},
		PostgreSQLVersion: "psql (PostgreSQL) 16.3",
		PGBackRestVersion: "pgBackRest 2.53.1",
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	wantCommands := []string{"postgres --version", "psql --version", "pgbackrest version"}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
}

func TestHandleRejectsBeforeExecutingCommands(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*guestprotocol.Request)
		wantCode guestprotocol.ErrorCode
	}{
		{"instance mismatch", func(request *guestprotocol.Request) { request.InstanceUID = "another-instance" }, guestprotocol.CodeInstanceMismatch},
		{"arbitrary operation", func(request *guestprotocol.Request) { request.Operation = "shell" }, guestprotocol.CodeInvalidRequest},
		{"nonempty probe payload", func(request *guestprotocol.Request) { request.Payload = json.RawMessage(`{"command":"id"}`) }, guestprotocol.CodeInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validProbeRequest()
			test.mutate(&request)
			commandCalled := false
			response := Handle(context.Background(), request, Dependencies{
				ReadFile: func(path string) ([]byte, error) {
					if path == InstanceUIDPath {
						return []byte("instance-123"), nil
					}
					return nil, errors.New("not found")
				},
				RunCommand: func(context.Context, string, ...string) ([]byte, error) {
					commandCalled = true
					return nil, nil
				},
			})
			if response.State != guestprotocol.StateFailed || response.Error == nil || response.Error.Code != test.wantCode {
				t.Fatalf("response = %+v", response)
			}
			if commandCalled {
				t.Fatal("request executed a command")
			}
		})
	}
}

func TestHandleDoesNotExposeLocalErrors(t *testing.T) {
	request := validProbeRequest()
	secret := "sensitive-local-detail"
	response := Handle(context.Background(), request, Dependencies{
		ReadFile: func(string) ([]byte, error) { return nil, errors.New(secret) },
	})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || contains(string(encoded), secret) {
		t.Fatalf("response leaked local error: %s", encoded)
	}
}

func validProbeRequest() guestprotocol.Request {
	return guestprotocol.Request{
		ProtocolVersion: guestprotocol.Version,
		RequestID:       "request-123",
		InstanceUID:     "instance-123",
		Operation:       guestprotocol.OperationProbe,
		Payload:         json.RawMessage(`{}`),
	}
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
