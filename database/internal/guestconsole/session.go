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

// Package guestconsole runs the restricted DBaaS guest protocol through a
// KubeVirt serial console. It never sends shell commands or includes console
// transcripts and credentials in errors.
package guestconsole

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"time"

	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/guestprotocol"
)

var (
	ErrLogin    = errors.New("guest login failed")
	ErrProtocol = errors.New("guest protocol failed")
	ErrLogout   = errors.New("guest logout failed")
)

const maxConsoleBuffer = 1 << 20

var (
	loginPromptRE = regexp.MustCompile(`(?im)(?:^|\n)[^\n]*login:\s*$`)
	passwordRE    = regexp.MustCompile(`(?im)(?:^|\n)[^\n]*password:\s*$`)
	shellPromptRE = regexp.MustCompile(`(?m)(?:^|\n)[^\n]*[#$]\s*$`)
	readyRE       = regexp.MustCompile(regexp.QuoteMeta(guestprotocol.ReadyMarker))
	initialRE     = regexp.MustCompile(`(?im)(?:^|\n)[^\n]*login:\s*$|(?:^|\n)[^\n]*[#$]\s*$|` + regexp.QuoteMeta(guestprotocol.ReadyMarker))
	loginResultRE = regexp.MustCompile(`(?im)` + regexp.QuoteMeta(guestprotocol.ReadyMarker) + `|login incorrect\s*$|authentication failure\s*$|(?:^|\n)[^\n]*login:\s*$`)
	frameRE       = regexp.MustCompile(`(?m)^[A-Za-z0-9_-]+\n`)
)

// SessionTimeouts bounds each interactive phase. The caller should also put
// one overall deadline around the complete command.
type SessionTimeouts struct {
	Login    time.Duration
	Request  time.Duration
	Response time.Duration
	Logout   time.Duration
}

func DefaultSessionTimeouts() SessionTimeouts {
	return SessionTimeouts{
		Login:    15 * time.Second,
		Request:  5 * time.Second,
		Response: 30 * time.Second,
		Logout:   5 * time.Second,
	}
}

// RunSession authenticates to the restricted login shell, sends one framed
// request, validates its response, and confirms that getty owns the console
// again after the wrapper exits.
func RunSession(
	ctx context.Context,
	conn net.Conn,
	username string,
	password []byte,
	request guestprotocol.Request,
	timeouts SessionTimeouts,
) (response guestprotocol.Response, err error) {
	if conn == nil || username == "" || len(password) == 0 {
		return response, fmt.Errorf("%w: session input is incomplete", ErrLogin)
	}
	if err := guestprotocol.ValidateRequest(request, guestprotocol.OperationProbe); err != nil {
		return response, fmt.Errorf("%w: request validation failed", ErrProtocol)
	}
	requestFrame, err := guestprotocol.EncodeFrame(request)
	if err != nil {
		return response, fmt.Errorf("%w: request encoding failed", ErrProtocol)
	}
	if timeouts.Login <= 0 || timeouts.Request <= 0 || timeouts.Response <= 0 || timeouts.Logout <= 0 {
		return response, fmt.Errorf("%w: session timeouts are invalid", ErrProtocol)
	}

	terminal := newTerminalBuffer(conn, maxConsoleBuffer)
	wrapperRunning := false
	loggedOut := false
	defer func() {
		if !wrapperRunning || loggedOut {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), timeouts.Logout)
		defer cancel()
		// Ctrl-C terminates the restricted wrapper. It cannot expose a shell.
		_ = terminal.write(cleanupCtx, []byte{3})
		_, _ = terminal.expect(cleanupCtx, loginPromptRE)
	}()

	loginCtx, cancelLogin := context.WithTimeout(ctx, timeouts.Login)
	defer cancelLogin()
	if err := terminal.write(loginCtx, []byte("\n")); err != nil {
		return response, sessionError(ErrLogin, "wake console", err)
	}
	initial, err := terminal.expect(loginCtx, initialRE)
	if err != nil {
		return response, sessionError(ErrLogin, "login prompt not reached", err)
	}

	// A serial terminal survives a disconnected WebSocket. Close a wrapper or
	// shell left behind by an earlier interrupted session before logging in.
	switch {
	case readyRE.Match(initial):
		if err := terminal.write(loginCtx, []byte{3}); err != nil {
			return response, sessionError(ErrLogin, "close stale guest session", err)
		}
		if _, err := terminal.expect(loginCtx, loginPromptRE); err != nil {
			return response, sessionError(ErrLogin, "stale guest session remained active", err)
		}
	case shellPromptRE.Match(initial) && !loginPromptRE.Match(initial):
		if err := terminal.write(loginCtx, []byte("exit\n")); err != nil {
			return response, sessionError(ErrLogin, "close stale console session", err)
		}
		if _, err := terminal.expect(loginCtx, loginPromptRE); err != nil {
			return response, sessionError(ErrLogin, "stale console session remained active", err)
		}
	}

	if err := terminal.write(loginCtx, append([]byte(username), '\n')); err != nil {
		return response, sessionError(ErrLogin, "send username", err)
	}
	if _, err := terminal.expect(loginCtx, passwordRE); err != nil {
		return response, sessionError(ErrLogin, "password prompt not reached", err)
	}
	if err := terminal.writeSecretLine(loginCtx, password); err != nil {
		return response, sessionError(ErrLogin, "send password", err)
	}
	loginResult, err := terminal.expect(loginCtx, loginResultRE)
	if err != nil || !readyRE.Match(loginResult) {
		if err == nil {
			err = errors.New("credentials rejected")
		}
		return response, sessionError(ErrLogin, "restricted wrapper not reached", err)
	}
	wrapperRunning = true
	cancelLogin()

	requestCtx, cancelRequest := context.WithTimeout(ctx, timeouts.Request)
	if err := terminal.write(requestCtx, requestFrame); err != nil {
		cancelRequest()
		return response, sessionError(ErrProtocol, "send request", err)
	}
	cancelRequest()

	responseCtx, cancelResponse := context.WithTimeout(ctx, timeouts.Response)
	defer cancelResponse()
	encodedResponse, err := terminal.expect(responseCtx, frameRE)
	if err != nil {
		return response, sessionError(ErrProtocol, "response not received", err)
	}
	encodedResponse = encodedResponse[:len(encodedResponse)-1]
	if len(encodedResponse) > guestprotocol.MaxFrameBytes {
		return response, fmt.Errorf("%w: response exceeds the frame limit", ErrProtocol)
	}
	response, err = guestprotocol.DecodeResponse(encodedResponse)
	if err != nil || guestprotocol.ValidateResponse(response, request.RequestID) != nil {
		return guestprotocol.Response{}, fmt.Errorf("%w: response validation failed", ErrProtocol)
	}
	cancelResponse()

	logoutCtx, cancelLogout := context.WithTimeout(ctx, timeouts.Logout)
	defer cancelLogout()
	if _, err := terminal.expect(logoutCtx, loginPromptRE); err != nil {
		return guestprotocol.Response{}, sessionError(ErrLogout, "login prompt not restored", err)
	}
	loggedOut = true
	return response, nil
}

func sessionError(kind error, action string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s: %w", kind, action, cause)
	}
	return fmt.Errorf("%w: %s", kind, action)
}

// terminalBuffer keeps bytes read after a match for the next state and stores
// only normalized terminal text, never a raw transcript.
type terminalBuffer struct {
	conn       net.Conn
	buffer     []byte
	limit      int
	normalizer terminalNormalizer
}

func newTerminalBuffer(conn net.Conn, limit int) *terminalBuffer {
	return &terminalBuffer{conn: conn, limit: limit}
}

func (b *terminalBuffer) expect(ctx context.Context, expression *regexp.Regexp) ([]byte, error) {
	for {
		if location := expression.FindIndex(b.buffer); location != nil {
			matched := append([]byte(nil), b.buffer[location[0]:location[1]]...)
			b.buffer = append(b.buffer[:0], b.buffer[location[1]:]...)
			return matched, nil
		}
		if len(b.buffer) >= b.limit {
			return nil, errors.New("console buffer limit exceeded")
		}
		if err := setReadDeadline(b.conn, ctx); err != nil {
			return nil, err
		}
		stopInterrupt := context.AfterFunc(ctx, func() {
			_ = b.conn.SetReadDeadline(time.Now())
		})
		chunk := make([]byte, 4096)
		n, readErr := b.conn.Read(chunk)
		stopInterrupt()
		if n > 0 {
			b.buffer = b.normalizer.appendNormalized(b.buffer, chunk[:n])
			if len(b.buffer) > b.limit {
				return nil, errors.New("console buffer limit exceeded")
			}
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(readErr, io.EOF) {
				return nil, io.ErrUnexpectedEOF
			}
			var networkError net.Error
			if errors.As(readErr, &networkError) && networkError.Timeout() {
				return nil, context.DeadlineExceeded
			}
			return nil, readErr
		}
	}
}

func (b *terminalBuffer) write(ctx context.Context, data []byte) error {
	if err := setWriteDeadline(b.conn, ctx); err != nil {
		return err
	}
	stopInterrupt := context.AfterFunc(ctx, func() {
		_ = b.conn.SetWriteDeadline(time.Now())
	})
	defer stopInterrupt()
	for len(data) > 0 {
		n, err := b.conn.Write(data)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		data = data[n:]
	}
	return nil
}

func (b *terminalBuffer) writeSecretLine(ctx context.Context, secret []byte) error {
	if err := b.write(ctx, secret); err != nil {
		return err
	}
	return b.write(ctx, []byte{'\n'})
}

func setReadDeadline(conn net.Conn, ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return conn.SetReadDeadline(time.Time{})
	}
	return conn.SetReadDeadline(deadline)
}

func setWriteDeadline(conn net.Conn, ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return conn.SetWriteDeadline(time.Time{})
	}
	return conn.SetWriteDeadline(deadline)
}

// terminalNormalizer removes ANSI control sequences and normalizes CRLF/CR
// to LF as bytes arrive, including when a sequence is split across reads.
type terminalNormalizer struct {
	state     byte
	pendingCR bool
}

func (n *terminalNormalizer) appendNormalized(dst, src []byte) []byte {
	for _, current := range src {
		if n.pendingCR {
			dst = append(dst, '\n')
			n.pendingCR = false
			if current == '\n' {
				continue
			}
		}
		switch n.state {
		case 0:
			switch current {
			case '\r':
				n.pendingCR = true
			case 0x1b:
				n.state = 1
			default:
				dst = append(dst, current)
			}
		case 1: // ESC
			switch current {
			case '[':
				n.state = 2
			case ']':
				n.state = 3
			default:
				n.state = 0
			}
		case 2: // CSI, terminated by a byte in 0x40-0x7e.
			if current >= 0x40 && current <= 0x7e {
				n.state = 0
			}
		case 3: // OSC, terminated by BEL or ESC followed by backslash.
			if current == 0x07 {
				n.state = 0
			} else if current == 0x1b {
				n.state = 4
			}
		case 4:
			if current == '\\' {
				n.state = 0
			} else {
				n.state = 3
			}
		}
	}
	return dst
}
