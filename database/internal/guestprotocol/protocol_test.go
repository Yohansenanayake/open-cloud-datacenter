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

package guestprotocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestFrameRoundTrip(t *testing.T) {
	request := Request{
		ProtocolVersion: Version,
		RequestID:       "request-123",
		InstanceUID:     "instance-456",
		Operation:       OperationProbe,
		Payload:         json.RawMessage(`{}`),
	}
	encoded, err := EncodeFrame(request)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte{'='}) {
		t.Fatalf("frame is padded: %q", encoded)
	}
	frame, err := ReadFrame(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRequest(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRequest(decoded, OperationProbe); err != nil {
		t.Fatal(err)
	}
	if decoded.RequestID != request.RequestID || decoded.InstanceUID != request.InstanceUID {
		t.Fatalf("decoded request = %+v", decoded)
	}
}

func TestDecodeRequestAllowsAdditiveFields(t *testing.T) {
	document := []byte(`{"protocolVersion":1,"requestID":"r1","instanceUID":"i1","operation":"probe","payload":{},"futureField":true}`)
	frame := make([]byte, base64.RawURLEncoding.EncodedLen(len(document)))
	base64.RawURLEncoding.Encode(frame, document)
	request, err := DecodeRequest(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRequest(request, OperationProbe); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRequestRejectsInvalidEnvelopes(t *testing.T) {
	valid := Request{ProtocolVersion: Version, RequestID: "r1", InstanceUID: "i1", Operation: OperationProbe, Payload: json.RawMessage(`{}`)}
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{"unsupported version", func(r *Request) { r.ProtocolVersion = 2 }},
		{"missing request ID", func(r *Request) { r.RequestID = "" }},
		{"invalid request ID", func(r *Request) { r.RequestID = "contains space" }},
		{"long request ID", func(r *Request) { r.RequestID = strings.Repeat("a", MaxRequestIDBytes+1) }},
		{"missing instance UID", func(r *Request) { r.InstanceUID = "" }},
		{"invalid instance UID", func(r *Request) { r.InstanceUID = "uid/escape" }},
		{"unsupported operation", func(r *Request) { r.Operation = "shell" }},
		{"non-object payload", func(r *Request) { r.Payload = json.RawMessage(`[]`) }},
		{"missing payload", func(r *Request) { r.Payload = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := ValidateRequest(request, OperationProbe); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestFrameLimitsAndMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		frame []byte
	}{
		{"empty", nil},
		{"padded base64", []byte("e30=")},
		{"invalid alphabet", []byte("not+url")},
		{"invalid JSON", []byte(base64.RawURLEncoding.EncodeToString([]byte("{")))},
		{"oversized", bytes.Repeat([]byte{'a'}, MaxFrameBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeRequest(test.frame); err == nil {
				t.Fatal("expected decode error")
			}
		})
	}

	if _, err := ReadFrame(bytes.NewReader([]byte("abc"))); err == nil {
		t.Fatal("expected unterminated-frame error")
	}
	if frame, err := ReadFrame(bytes.NewReader([]byte("abc\ndef\n"))); err != nil || string(frame) != "abc" {
		t.Fatalf("read first frame = %q, %v", frame, err)
	}
	if _, err := EncodeFrame(map[string]string{"data": strings.Repeat("x", MaxMessageBytes)}); err == nil {
		t.Fatal("expected oversized-message error")
	}
}

func TestReadFrameAcrossTTYSizeChunks(t *testing.T) {
	encoded, err := EncodeFrame(map[string]string{"data": strings.Repeat("x", 12*1024)})
	if err != nil {
		t.Fatal(err)
	}
	reader := &chunkReader{reader: bytes.NewReader(encoded), max: 4095}
	frame, err := ReadFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.TrimSuffix(encoded, []byte{'\n'})
	if !bytes.Equal(frame, want) {
		t.Fatalf("frame length = %d, want %d", len(frame), len(want))
	}
}

func TestValidateResponse(t *testing.T) {
	response := Success("r1", "0.1.0", struct{}{})
	if err := ValidateResponse(response, "r1"); err != nil {
		t.Fatal(err)
	}

	response.RequestID = "wrong"
	if err := ValidateResponse(response, "r1"); err == nil {
		t.Fatal("expected request ID mismatch")
	}

	failed := Failure("r1", "0.1.0", CodeInvalidRequest, "request rejected")
	if err := ValidateResponse(failed, "r1"); err != nil {
		t.Fatal(err)
	}
	failed.Error = nil
	if err := ValidateResponse(failed, "r1"); err == nil {
		t.Fatal("expected missing error code failure")
	}
}

type chunkReader struct {
	reader *bytes.Reader
	max    int
}

func (reader *chunkReader) Read(buffer []byte) (int, error) {
	if len(buffer) > reader.max {
		buffer = buffer[:reader.max]
	}
	return reader.reader.Read(buffer)
}
