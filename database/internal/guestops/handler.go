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

// Package guestops implements the privileged, fixed guest operation allowlist.
package guestops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

const (
	// Cloud-init writes this VM's DBInstance UID (dbi UID) here for request validation.
	InstanceUIDPath            = "/etc/dbaas/instance-uid"
	OSReleasePath              = "/etc/os-release"
	maxProbeCommandOutputBytes = 4 * 1024
)

// Version is overridden with -ldflags when building a release package.
var Version = "0.1.0"

type Dependencies struct {
	ReadFile   func(string) ([]byte, error)
	RunCommand func(context.Context, string, ...string) ([]byte, error)
	GOOS       string
	GOARCH     string
}

type OSInfo struct {
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	ID         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	VersionID  string `json:"versionID,omitempty"`
	PrettyName string `json:"prettyName,omitempty"`
}

type ProbeResult struct {
	ConfiguredInstanceUID string `json:"configuredInstanceUID"`
	OS                    OSInfo `json:"os"`
	PostgreSQLVersion     string `json:"postgresqlVersion,omitempty"`
	PGBackRestVersion     string `json:"pgBackRestVersion,omitempty"`
}

func DefaultDependencies() Dependencies {
	return Dependencies{
		ReadFile:   os.ReadFile,
		RunCommand: runCommand,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
	}
}

// Handle revalidates every request inside the privileged process. All error
// responses are intentionally generic so local paths and command output never
// cross the guest protocol boundary.
func Handle(ctx context.Context, request guestprotocol.Request, dependencies Dependencies) guestprotocol.Response {
	if err := guestprotocol.ValidateRequest(request, guestprotocol.OperationProbe); err != nil {
		return guestprotocol.Failure(request.RequestID, Version, guestprotocol.CodeInvalidRequest, "request rejected")
	}

	instanceUID, err := ReadInstanceUID(dependencies.ReadFile)
	if err != nil {
		return guestprotocol.Failure(request.RequestID, Version, guestprotocol.CodeOperationFailed, "guest identity is unavailable")
	}
	if request.InstanceUID != instanceUID {
		return guestprotocol.Failure(request.RequestID, Version, guestprotocol.CodeInstanceMismatch, "instance identity does not match")
	}

	switch request.Operation {
	case guestprotocol.OperationProbe:
		if !isEmptyObject(request.Payload) {
			return guestprotocol.Failure(request.RequestID, Version, guestprotocol.CodeInvalidRequest, "probe payload must be empty")
		}
		result := probe(ctx, instanceUID, dependencies)
		return guestprotocol.Success(request.RequestID, Version, result)
	default:
		return guestprotocol.Failure(request.RequestID, Version, guestprotocol.CodeInvalidRequest, "request rejected")
	}
}

// ReadInstanceUID reads the cloud-init-provisioned, root-owned guest identity.
func ReadInstanceUID(readFile func(string) ([]byte, error)) (string, error) {
	if readFile == nil {
		return "", errors.New("file reader is not configured")
	}
	contents, err := readFile(InstanceUIDPath)
	if err != nil {
		return "", err
	}
	uid := strings.TrimSpace(string(contents))
	if uid == "" || len(uid) > guestprotocol.MaxInstanceUIDBytes {
		return "", errors.New("configured instance UID is invalid")
	}
	return uid, nil
}

func probe(ctx context.Context, instanceUID string, dependencies Dependencies) ProbeResult {
	result := ProbeResult{
		ConfiguredInstanceUID: instanceUID,
		OS: OSInfo{
			GOOS:   dependencies.GOOS,
			GOARCH: dependencies.GOARCH,
		},
	}
	if dependencies.ReadFile != nil {
		if contents, err := dependencies.ReadFile(OSReleasePath); err == nil {
			applyOSRelease(&result.OS, contents)
		}
	}
	if dependencies.RunCommand != nil {
		result.PostgreSQLVersion = firstVersion(ctx, dependencies.RunCommand,
			command{name: "postgres", args: []string{"--version"}},
			command{name: "psql", args: []string{"--version"}},
			command{name: "pg_config", args: []string{"--version"}},
		)
		result.PGBackRestVersion = firstVersion(ctx, dependencies.RunCommand,
			command{name: "pgbackrest", args: []string{"version"}},
		)
	}
	return result
}

func applyOSRelease(info *OSInfo, contents []byte) {
	values := make(map[string]string)
	for _, line := range strings.Split(string(contents), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(key), "#") {
			continue
		}
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		values[strings.TrimSpace(key)] = value
	}
	info.ID = values["ID"]
	info.Name = values["NAME"]
	info.VersionID = values["VERSION_ID"]
	info.PrettyName = values["PRETTY_NAME"]
}

type command struct {
	name string
	args []string
}

func firstVersion(ctx context.Context, runner func(context.Context, string, ...string) ([]byte, error), commands ...command) string {
	for _, candidate := range commands {
		output, err := runner(ctx, candidate.name, candidate.args...)
		if err == nil {
			return strings.TrimSpace(string(output))
		}
	}
	return ""
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	var output limitedBuffer
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	remaining := maxProbeCommandOutputBytes - buffer.Len()
	if remaining <= 0 {
		return 0, errors.New("command output limit exceeded")
	}
	if len(data) > remaining {
		_, _ = buffer.Buffer.Write(data[:remaining])
		return remaining, errors.New("command output limit exceeded")
	}
	return buffer.Buffer.Write(data)
}

func isEmptyObject(raw json.RawMessage) bool {
	var payload map[string]json.RawMessage
	return json.Unmarshal(raw, &payload) == nil && len(payload) == 0
}
