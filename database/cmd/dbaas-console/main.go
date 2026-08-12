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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"

	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestops"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

const (
	backupctlPath  = "/usr/lib/dbaas/dbaas-backupctl"
	sudoPath       = "/usr/bin/sudo"
	sessionTimeout = 60 * time.Second

	exitOK      = 0
	exitFailure = 1
)

// backupctlRunner sends one request to the privileged backup helper.
type backupctlRunner func(ctx context.Context, requestFrame []byte) (responseFrame []byte, err error)

// terminalConfigurator prepares the terminal and returns a function that restores it.
type terminalConfigurator func() (restore func() error, err error)

func main() {
	configureTerminal := func() (func() error, error) { return configureProtocolTerminal(os.Stdin) }
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.ReadFile, runBackupctl, configureTerminal))
}

func run(
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	readFile func(string) ([]byte, error), //Reads the configured DBInstance UID.
	runner backupctlRunner,
	configureTerminal terminalConfigurator,
) int {
	if len(args) == 1 && args[0] == "--version" {
		_, _ = fmt.Fprintln(stdout, guestops.Version)
		return exitOK
	}
	if len(args) != 0 {
		_, _ = fmt.Fprintln(stderr, "dbaas-console: command-line arguments are not accepted")
		return exitFailure
	}

	restoreTerminal, err := configureTerminal()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "dbaas-console: terminal setup failed")
		return exitFailure
	}
	if restoreTerminal != nil {
		defer func() { _ = restoreTerminal() }()
	}
	// The marker means the wrapper is ready to drain a frame larger than the
	// kernel's TTY receive buffer.
	if _, err := fmt.Fprintln(stdout, guestprotocol.ReadyMarker); err != nil {
		return exitFailure
	}

	frame, err := guestprotocol.ReadFrame(stdin)
	if err != nil {
		return writeConsoleFailure(stdout, "", guestprotocol.CodeInvalidRequest, "request rejected")
	}
	request, err := guestprotocol.DecodeRequest(frame)
	if err != nil {
		return writeConsoleFailure(stdout, "", guestprotocol.CodeInvalidRequest, "request rejected")
	}
	if err := guestprotocol.ValidateRequest(request, guestprotocol.OperationProbe); err != nil {
		return writeConsoleFailure(stdout, request.RequestID, guestprotocol.CodeInvalidRequest, "request rejected")
	}
	instanceUID, err := guestops.ReadInstanceUID(readFile)
	if err != nil {
		return writeConsoleFailure(stdout, request.RequestID, guestprotocol.CodeOperationFailed, "guest identity is unavailable")
	}
	if request.InstanceUID != instanceUID {
		return writeConsoleFailure(stdout, request.RequestID, guestprotocol.CodeInstanceMismatch, "instance identity does not match")
	}

	canonicalFrame, err := guestprotocol.EncodeFrame(request)
	if err != nil {
		return writeConsoleFailure(stdout, request.RequestID, guestprotocol.CodeInvalidRequest, "request rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()
	output, err := runner(ctx, canonicalFrame)
	if err != nil {
		return writeConsoleFailure(stdout, request.RequestID, guestprotocol.CodeOperationFailed, "guest operation failed")
	}
	responseFrame, err := guestprotocol.ReadFrame(bytes.NewReader(output))
	if err != nil {
		return writeConsoleFailure(stdout, request.RequestID, guestprotocol.CodeProtocolViolation, "invalid guest operation response")
	}
	response, err := guestprotocol.DecodeResponse(responseFrame)
	if err != nil || guestprotocol.ValidateResponse(response, request.RequestID) != nil {
		return writeConsoleFailure(stdout, request.RequestID, guestprotocol.CodeProtocolViolation, "invalid guest operation response")
	}
	return writeConsoleResponse(stdout, response)
}

func runBackupctl(ctx context.Context, requestFrame []byte) ([]byte, error) {
	command := exec.CommandContext(ctx, sudoPath, backupctlPath)
	command.Stdin = bytes.NewReader(requestFrame)
	command.Stderr = io.Discard
	var output boundedBuffer
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return nil, errors.New("backup operation process failed")
	}
	return output.Bytes(), nil
}

type boundedBuffer struct {
	bytes.Buffer
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	remaining := guestprotocol.MaxFrameBytes + 2 - buffer.Len()
	if remaining <= 0 {
		return 0, errors.New("protocol response limit exceeded")
	}
	if len(data) > remaining {
		_, _ = buffer.Buffer.Write(data[:remaining])
		return remaining, errors.New("protocol response limit exceeded")
	}
	return buffer.Buffer.Write(data)
}

func configureProtocolTerminal(file *os.File) (func() error, error) {
	state, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	if errors.Is(err, unix.ENOTTY) {
		return func() error { return nil }, nil
	}
	if err != nil {
		return nil, err
	}
	configured := protocolTermios(*state)
	if err := unix.IoctlSetTermios(int(file.Fd()), unix.TCSETSF, &configured); err != nil {
		return nil, err
	}
	return func() error {
		return unix.IoctlSetTermios(int(file.Fd()), unix.TCSETS, state)
	}, nil
}

func protocolTermios(state unix.Termios) unix.Termios {
	state.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON
	state.Cc[unix.VMIN] = 1
	state.Cc[unix.VTIME] = 0
	return state
}

func writeConsoleFailure(stdout io.Writer, requestID string, code guestprotocol.ErrorCode, message string) int {
	return writeConsoleResponse(stdout, guestprotocol.Failure(requestID, guestops.Version, code, message))
}

func writeConsoleResponse(stdout io.Writer, response guestprotocol.Response) int {
	frame, err := guestprotocol.EncodeFrame(response)
	if err != nil {
		return exitFailure
	}
	if _, err := stdout.Write(frame); err != nil {
		return exitFailure
	}
	return exitOK
}
