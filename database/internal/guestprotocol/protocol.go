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

// Package guestprotocol defines the bounded, TTY-safe protocol shared by the
// DBaaS controller and the restricted guest tools.
package guestprotocol

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const (
	Version = 1

	// MaxMessageBytes is the 64 KiB decoded JSON limit (64 * 1024 bytes).
	MaxMessageBytes = 64 * 1024
	// Base64 uses 8 input bits per byte and stores 6 bits per character. Adding
	// 5 before division rounds up. The line terminator is not included.
	MaxFrameBytes = (MaxMessageBytes*8 + 5) / 6

	// 128 bytes fits 36-character Kubernetes UIDs and longer generated request
	// IDs while keeping both fields bounded.
	MaxRequestIDBytes   = 128
	MaxInstanceUIDBytes = 128

	ReadyMarker = "DBAAS-CONSOLE-READY-V1"
)

type Operation string

const (
	OperationProbe Operation = "probe"
)

type State string

const (
	StateAccepted  State = "Accepted"
	StateRunning   State = "Running"
	StateSucceeded State = "Succeeded"
	StateFailed    State = "Failed"
)

type ErrorCode string

const (
	CodeInvalidRequest     ErrorCode = "InvalidRequest"
	CodeInstanceMismatch   ErrorCode = "InstanceMismatch"
	CodeOperationFailed    ErrorCode = "OperationFailed"
	CodeProtocolViolation  ErrorCode = "ProtocolViolation"
	CodeGuestToolsInternal ErrorCode = "GuestToolsInternal"
)

// Request is one controller-to-guest operation. Payload remains raw so each
// operation can decode its own schema without exposing credentials in generic
// errors or logs.
type Request struct {
	ProtocolVersion int             `json:"protocolVersion"`
	RequestID       string          `json:"requestID"`
	InstanceUID     string          `json:"instanceUID"`
	Operation       Operation       `json:"operation"`
	Payload         json.RawMessage `json:"payload"`
}

// ResponseError contains only a stable, sanitized error code and message.
type ResponseError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message,omitempty"`
}

// Response is one guest-to-controller operation result.
type Response struct {
	ProtocolVersion           int             `json:"protocolVersion"`
	RequestID                 string          `json:"requestID"`
	State                     State           `json:"state"`
	GuestToolsVersion         string          `json:"guestToolsVersion"`
	SupportedProtocolVersions []int           `json:"supportedProtocolVersions"`
	Result                    json.RawMessage `json:"result"`
	Error                     *ResponseError  `json:"error,omitempty"`
}

func Success(requestID, guestToolsVersion string, result any) Response {
	return NewResponse(requestID, guestToolsVersion, StateSucceeded, result, nil)
}

func Failure(requestID, guestToolsVersion string, code ErrorCode, message string) Response {
	return NewResponse(requestID, guestToolsVersion, StateFailed, struct{}{}, &ResponseError{
		Code:    code,
		Message: message,
	})
}

func NewResponse(requestID, guestToolsVersion string, state State, result any, responseErr *ResponseError) Response {
	encodedResult, err := json.Marshal(result)
	if err != nil {
		encodedResult = json.RawMessage(`{}`)
		state = StateFailed
		responseErr = &ResponseError{Code: CodeGuestToolsInternal, Message: "guest operation failed"}
	}
	return Response{
		ProtocolVersion:           Version,
		RequestID:                 requestID,
		State:                     state,
		GuestToolsVersion:         guestToolsVersion,
		SupportedProtocolVersions: []int{Version},
		Result:                    encodedResult,
		Error:                     responseErr,
	}
}

// ValidateRequest validates the common envelope and restricts the caller to
// the supplied operation set. Unknown JSON fields remain allowed so protocol
// v1 can evolve additively.
func ValidateRequest(request Request, allowedOperations ...Operation) error {
	if request.ProtocolVersion != Version {
		return fmt.Errorf("unsupported protocol version %d", request.ProtocolVersion)
	}
	if err := validateToken("request ID", request.RequestID, MaxRequestIDBytes); err != nil {
		return err
	}
	if err := validateToken("instance UID", request.InstanceUID, MaxInstanceUIDBytes); err != nil {
		return err
	}
	if !slices.Contains(allowedOperations, request.Operation) {
		return fmt.Errorf("unsupported operation %q", request.Operation)
	}
	if !isJSONObject(request.Payload) {
		return errors.New("payload must be a JSON object")
	}
	return nil
}

func ValidateResponse(response Response, requestID string) error {
	if response.ProtocolVersion != Version {
		return fmt.Errorf("unsupported protocol version %d", response.ProtocolVersion)
	}
	if response.RequestID != requestID {
		return errors.New("response request ID does not match")
	}
	if !slices.Contains(response.SupportedProtocolVersions, Version) {
		return errors.New("guest does not report protocol v1 support")
	}
	if strings.TrimSpace(response.GuestToolsVersion) == "" {
		return errors.New("guest tools version is required")
	}
	switch response.State {
	case StateAccepted, StateRunning, StateSucceeded:
		if response.Error != nil {
			return errors.New("non-failed response contains an error")
		}
	case StateFailed:
		if response.Error == nil || response.Error.Code == "" {
			return errors.New("failed response requires an error code")
		}
	default:
		return fmt.Errorf("unsupported response state %q", response.State)
	}
	if !isJSONObject(response.Result) {
		return errors.New("result must be a JSON object")
	}
	return nil
}

func validateToken(name, value string, maxBytes int) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && !strings.ContainsRune("._:-", r) {
			return fmt.Errorf("%s contains invalid characters", name)
		}
	}
	return nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return false
	}
	var value map[string]json.RawMessage
	return json.Unmarshal(trimmed, &value) == nil
}

// EncodeFrame serializes one value as a single unpadded base64url line.
func EncodeFrame(value any) ([]byte, error) {
	document, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("encode protocol message")
	}
	if len(document) > MaxMessageBytes {
		return nil, fmt.Errorf("protocol message exceeds %d bytes", MaxMessageBytes)
	}
	frame := make([]byte, base64.RawURLEncoding.EncodedLen(len(document))+1)
	base64.RawURLEncoding.Encode(frame, document)
	frame[len(frame)-1] = '\n'
	return frame, nil
}

// ReadFrame reads exactly one bounded protocol line. It accepts CRLF because
// serial terminals commonly translate line endings.
func ReadFrame(reader io.Reader) ([]byte, error) {
	buffered := bufio.NewReaderSize(reader, MaxFrameBytes+2)
	frame, err := buffered.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("read protocol frame")
	}
	if len(frame) == 0 {
		return nil, errors.New("protocol frame is empty")
	}
	newlineAt := bytes.IndexByte(frame, '\n')
	if newlineAt < 0 {
		if len(frame) > MaxFrameBytes {
			return nil, fmt.Errorf("protocol frame exceeds %d bytes", MaxFrameBytes)
		}
		return nil, errors.New("protocol frame is not newline terminated")
	}
	frame = frame[:newlineAt]
	frame = bytes.TrimSuffix(frame, []byte{'\r'})
	if len(frame) == 0 {
		return nil, errors.New("protocol frame is empty")
	}
	if len(frame) > MaxFrameBytes {
		return nil, fmt.Errorf("protocol frame exceeds %d bytes", MaxFrameBytes)
	}
	return frame, nil
}

func DecodeRequest(frame []byte) (Request, error) {
	var request Request
	if err := decodeFrame(frame, &request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func DecodeResponse(frame []byte) (Response, error) {
	var response Response
	if err := decodeFrame(frame, &response); err != nil {
		return Response{}, err
	}
	return response, nil
}

func decodeFrame(frame []byte, destination any) error {
	if len(frame) == 0 {
		return errors.New("protocol frame is empty")
	}
	if len(frame) > MaxFrameBytes {
		return fmt.Errorf("protocol frame exceeds %d bytes", MaxFrameBytes)
	}
	document := make([]byte, base64.RawURLEncoding.DecodedLen(len(frame)))
	n, err := base64.RawURLEncoding.Decode(document, frame)
	if err != nil {
		return errors.New("protocol frame is not valid unpadded base64url")
	}
	document = document[:n]
	if len(document) > MaxMessageBytes {
		return fmt.Errorf("protocol message exceeds %d bytes", MaxMessageBytes)
	}
	if err := json.Unmarshal(document, destination); err != nil {
		return errors.New("protocol message is not valid JSON")
	}
	return nil
}
