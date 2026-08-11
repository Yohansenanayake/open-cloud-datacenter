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
	"fmt"
	"io"
	"os"
	"time"

	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestops"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

const (
	exitOK           = 0
	exitFailure      = 1
	operationTimeout = 60 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Geteuid, guestops.DefaultDependencies()))
}

func run(
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	effectiveUID func() int,
	dependencies guestops.Dependencies,
) int {
	if len(args) == 1 && args[0] == "--version" {
		_, _ = fmt.Fprintln(stdout, guestops.Version)
		return exitOK
	}
	if len(args) != 0 {
		_, _ = fmt.Fprintln(stderr, "dbaas-backupctl: command-line arguments are not accepted")
		return exitFailure
	}

	frame, err := guestprotocol.ReadFrame(stdin)
	if err != nil {
		return writeFailure(stdout, "", guestprotocol.CodeInvalidRequest, "request rejected")
	}
	request, err := guestprotocol.DecodeRequest(frame)
	if err != nil {
		return writeFailure(stdout, "", guestprotocol.CodeInvalidRequest, "request rejected")
	}
	if effectiveUID == nil || effectiveUID() != 0 {
		return writeFailure(stdout, request.RequestID, guestprotocol.CodeOperationFailed, "privileged execution is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	response := guestops.Handle(ctx, request, dependencies)
	return writeResponse(stdout, response)
}

func writeFailure(stdout io.Writer, requestID string, code guestprotocol.ErrorCode, message string) int {
	return writeResponse(stdout, guestprotocol.Failure(requestID, guestops.Version, code, message))
}

func writeResponse(stdout io.Writer, response guestprotocol.Response) int {
	frame, err := guestprotocol.EncodeFrame(response)
	if err != nil {
		return exitFailure
	}
	if _, err := stdout.Write(frame); err != nil {
		return exitFailure
	}
	return exitOK
}
