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

package ensure

import (
	"errors"
	"fmt"
	"testing"
)

func TestTerminalErrorError(t *testing.T) {
	if got := (&TerminalError{Reason: "R", Message: "M"}).Error(); got != "R: M" {
		t.Errorf("TerminalError.Error() = %q, want %q", got, "R: M")
	}
}

func TestClassifyError(t *testing.T) {
	// Terminal sentinel -> OutcomeTerminal, preserving reason/message.
	res := classifyError(terminalErr("InvalidClass", `unknown dbInstanceClass "x"`))
	if res.Outcome != OutcomeTerminal {
		t.Fatalf("terminal sentinel: Outcome = %q, want %q", res.Outcome, OutcomeTerminal)
	}
	if res.Reason != "InvalidClass" || res.Message != `unknown dbInstanceClass "x"` {
		t.Errorf("terminal sentinel: Reason/Message = %q/%q", res.Reason, res.Message)
	}
	if res.Err != nil {
		t.Errorf("terminal outcome must not carry an error, got %v", res.Err)
	}

	// Wrapped terminal still classified as Terminal (errors.As unwraps %w).
	wrapped := fmt.Errorf("ensureVM: %w", terminalErr("InvalidClass", "bad"))
	if res := classifyError(wrapped); res.Outcome != OutcomeTerminal {
		t.Errorf("wrapped terminal: Outcome = %q, want %q", res.Outcome, OutcomeTerminal)
	}

	// Default: any other error -> Transient, original error preserved for backoff.
	generic := errors.New("api timeout")
	res = classifyError(generic)
	if res.Outcome != OutcomeTransient {
		t.Fatalf("generic error: Outcome = %q, want %q", res.Outcome, OutcomeTransient)
	}
	if res.Err != generic {
		t.Errorf("generic error: Err = %v, want original error", res.Err)
	}
}
