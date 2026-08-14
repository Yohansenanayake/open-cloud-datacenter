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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

func testRequest() guestprotocol.Request {
	return guestprotocol.Request{
		ProtocolVersion: guestprotocol.Version,
		RequestID:       "request-1",
		InstanceUID:     "orders-uid",
		Operation:       guestprotocol.OperationProbe,
		Payload:         json.RawMessage(`{}`),
	}
}

func testTimeouts() SessionTimeouts {
	return SessionTimeouts{Login: time.Second, Request: time.Second, Response: time.Second, Logout: time.Second}
}

func TestRunSessionProbeAndRetainsUnreadBytes(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go fakeProtocolGuest(server, fakeGuestOptions{singleWriteAfterLogin: true})

	response, err := RunSession(context.Background(), client, "dbaas-ops", []byte("guest-password"), testRequest(), testTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	if response.State != guestprotocol.StateSucceeded || response.RequestID != "request-1" {
		t.Fatalf("response = %+v", response)
	}
}

func TestRunSessionHandlesSplitANSIAndCRLF(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go fakeProtocolGuest(server, fakeGuestOptions{splitWrites: true, ansiPrompts: true})

	if _, err := RunSession(context.Background(), client, "dbaas-ops", []byte("guest-password"), testRequest(), testTimeouts()); err != nil {
		t.Fatal(err)
	}
}

func TestRunSessionRecoversStaleWrapper(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go fakeProtocolGuest(server, fakeGuestOptions{staleWrapper: true})

	if _, err := RunSession(context.Background(), client, "dbaas-ops", []byte("guest-password"), testRequest(), testTimeouts()); err != nil {
		t.Fatal(err)
	}
}

func TestRunSessionRecoversStaleShell(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go fakeProtocolGuest(server, fakeGuestOptions{staleShell: true})

	if _, err := RunSession(context.Background(), client, "dbaas-ops", []byte("guest-password"), testRequest(), testTimeouts()); err != nil {
		t.Fatal(err)
	}
}

func TestRunSessionRejectsMismatchedAndOversizedResponses(t *testing.T) {
	for _, test := range []struct {
		name    string
		options fakeGuestOptions
	}{
		{"mismatched request ID", fakeGuestOptions{responseRequestID: "another-request"}},
		{"oversized frame", fakeGuestOptions{rawResponse: strings.Repeat("a", guestprotocol.MaxFrameBytes+1) + "\r\n"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			go fakeProtocolGuest(server, test.options)
			if _, err := RunSession(context.Background(), client, "dbaas-ops", []byte("guest-password"), testRequest(), testTimeouts()); !errors.Is(err, ErrProtocol) {
				t.Fatalf("error = %v, want ErrProtocol", err)
			}
		})
	}
}

func TestRunSessionRejectsCredentialsWithoutLeakingThem(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go fakeProtocolGuest(server, fakeGuestOptions{rejectLogin: true})

	secret := "never-print-this"
	_, err := RunSession(context.Background(), client, "dbaas-ops", []byte(secret), testRequest(), testTimeouts())
	if !errors.Is(err, ErrLogin) {
		t.Fatalf("error = %v, want ErrLogin", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked password: %v", err)
	}
}

func TestRunSessionCancellationInterruptsRead(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		_, _ = bufio.NewReader(server).ReadString('\n')
		_, _ = io.Copy(io.Discard, server)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	started := time.Now()
	_, err := RunSession(ctx, client, "dbaas-ops", []byte("guest-password"), testRequest(), SessionTimeouts{
		Login: time.Hour, Request: time.Hour, Response: time.Hour, Logout: time.Second,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled read took %s", elapsed)
	}
}

func TestRunSessionCancellationInterruptsRestrictedWrapper(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	interrupted := make(chan bool, 1)
	go func() {
		defer server.Close()
		reader := bufio.NewReader(server)
		_, _ = reader.ReadString('\n')
		_, _ = io.WriteString(server, "Ubuntu login: ")
		_, _ = reader.ReadString('\n')
		_, _ = io.WriteString(server, "Password: ")
		_, _ = reader.ReadString('\n')
		_, _ = io.WriteString(server, guestprotocol.ReadyMarker+"\r\n")
		_, _ = reader.ReadString('\n')
		controlByte, err := reader.ReadByte()
		interrupted <- err == nil && controlByte == 3
		if err == nil {
			_, _ = io.WriteString(server, "Ubuntu login: ")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := RunSession(ctx, client, "dbaas-ops", []byte("guest-password"), testRequest(), SessionTimeouts{
		Login: time.Second, Request: time.Second, Response: time.Second, Logout: time.Second,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	select {
	case got := <-interrupted:
		if !got {
			t.Fatal("restricted wrapper did not receive Ctrl-C cleanup")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for wrapper cleanup")
	}
}

func TestTerminalBufferBoundsNoise(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		_, _ = server.Write([]byte(strings.Repeat("x", 65)))
	}()

	buffer := newTerminalBuffer(client, 64)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := buffer.expect(ctx, loginPromptRE); err == nil || strings.Contains(err.Error(), strings.Repeat("x", 10)) {
		t.Fatalf("bounded read error = %v", err)
	}
}

type fakeGuestOptions struct {
	rejectLogin           bool
	staleWrapper          bool
	staleShell            bool
	splitWrites           bool
	ansiPrompts           bool
	singleWriteAfterLogin bool
	beforeResponse        <-chan struct{}
	responseRequestID     string
	rawResponse           string
}

func fakeProtocolGuest(conn net.Conn, options fakeGuestOptions) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	readLine := func() []byte {
		line, _ := reader.ReadBytes('\n')
		return line
	}
	write := func(value string) {
		if !options.splitWrites {
			_, _ = io.WriteString(conn, value)
			return
		}
		for _, part := range []string{value[:len(value)/2], value[len(value)/2:]} {
			_, _ = io.WriteString(conn, part)
		}
	}

	_ = readLine()
	if options.staleWrapper {
		write(guestprotocol.ReadyMarker + "\r\n")
		interrupt := make([]byte, 1)
		_, _ = io.ReadFull(reader, interrupt)
	}
	if options.staleShell {
		write("$ ")
		_ = readLine()
	}
	login := "Ubuntu login: "
	passwordPrompt := "Password: "
	if options.ansiPrompts {
		login = "\x1b[?2004hUbuntu \x1b[32mlogin:\x1b[0m "
		passwordPrompt = "Pass\x1b[31mword:\x1b[0m "
	}
	write(login)
	_ = readLine()
	write(passwordPrompt)
	_ = readLine()
	if options.rejectLogin {
		write("Login incorrect\r\nUbuntu login: ")
		_, _ = io.Copy(io.Discard, conn)
		return
	}

	ready := guestprotocol.ReadyMarker + "\r\n"
	if options.singleWriteAfterLogin {
		write(ready)
	} else {
		write(ready)
	}
	requestLine := readLine()
	frame := strings.TrimSpace(string(requestLine))
	request, err := guestprotocol.DecodeRequest([]byte(frame))
	if err != nil {
		return
	}
	if options.beforeResponse != nil {
		<-options.beforeResponse
	}
	responseRequestID := request.RequestID
	if options.responseRequestID != "" {
		responseRequestID = options.responseRequestID
	}
	responseFrame, _ := guestprotocol.EncodeFrame(guestprotocol.Success(responseRequestID, "0.1.0", struct{}{}))
	responseOutput := strings.ReplaceAll(string(responseFrame), "\n", "\r\n")
	if options.rawResponse != "" {
		responseOutput = options.rawResponse
	}
	responseOutput += "Ubuntu login: "
	if options.singleWriteAfterLogin {
		write(responseOutput)
	} else {
		write(responseOutput)
	}
}
