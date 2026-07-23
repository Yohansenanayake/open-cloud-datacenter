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
	"testing"
	"time"
)

func TestOutcomeConstructors(t *testing.T) {
	if got := Satisfied(); got.Outcome != OutcomeSatisfied {
		t.Errorf("Satisfied().Outcome = %q, want %q", got.Outcome, OutcomeSatisfied)
	}

	// pending is event-driven: same OutcomePending, zero RequeueAfter.
	p := Pending("VMCreated", "created virtualmachine")
	if p.Outcome != OutcomePending || p.Reason != "VMCreated" || p.Message != "created virtualmachine" {
		t.Errorf("Pending() = %+v", p)
	}
	if p.ControllerResult.RequeueAfter != 0 {
		t.Errorf("Pending() must be event-driven (RequeueAfter 0), got %v", p.ControllerResult.RequeueAfter)
	}
	if p.Err != nil {
		t.Errorf("Pending() must not carry an error, got %v", p.Err)
	}

	// pendingAfter is the same OutcomePending but with a timer fallback.
	pa := PendingAfter("Starting", "waiting for VMI", 15*time.Second)
	if pa.Outcome != OutcomePending {
		t.Errorf("PendingAfter().Outcome = %q, want %q", pa.Outcome, OutcomePending)
	}
	if pa.ControllerResult.RequeueAfter != 15*time.Second {
		t.Errorf("PendingAfter() RequeueAfter = %v, want 15s", pa.ControllerResult.RequeueAfter)
	}

	tm := Terminal("InvalidClass", "unknown class")
	if tm.Outcome != OutcomeTerminal || tm.Reason != "InvalidClass" {
		t.Errorf("Terminal() = %+v", tm)
	}
	if tm.Err != nil {
		t.Errorf("Terminal() must not carry an error, got %v", tm.Err)
	}

	err := errors.New("boom")
	tr := Transient(err)
	if tr.Outcome != OutcomeTransient {
		t.Errorf("Transient().Outcome = %q, want %q", tr.Outcome, OutcomeTransient)
	}
	if tr.Err != err {
		t.Errorf("Transient() Err = %v, want original error", tr.Err)
	}
}
