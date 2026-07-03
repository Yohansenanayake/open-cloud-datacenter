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

package controller

import (
	"errors"
	"testing"
	"time"
)

func TestOutcomeConstructors(t *testing.T) {
	if got := satisfied(); got.Outcome != OutcomeSatisfied {
		t.Errorf("satisfied().Outcome = %q, want %q", got.Outcome, OutcomeSatisfied)
	}

	// pending is event-driven: same OutcomePending, zero RequeueAfter.
	p := pending("VMCreated", "created virtualmachine")
	if p.Outcome != OutcomePending || p.Reason != "VMCreated" || p.Message != "created virtualmachine" {
		t.Errorf("pending() = %+v", p)
	}
	if p.Result.RequeueAfter != 0 {
		t.Errorf("pending() must be event-driven (RequeueAfter 0), got %v", p.Result.RequeueAfter)
	}
	if p.Err != nil {
		t.Errorf("pending() must not carry an error, got %v", p.Err)
	}

	// pendingAfter is the same OutcomePending but with a timer fallback.
	pa := pendingAfter("Starting", "waiting for VMI", 15*time.Second)
	if pa.Outcome != OutcomePending {
		t.Errorf("pendingAfter().Outcome = %q, want %q", pa.Outcome, OutcomePending)
	}
	if pa.Result.RequeueAfter != 15*time.Second {
		t.Errorf("pendingAfter() RequeueAfter = %v, want 15s", pa.Result.RequeueAfter)
	}

	tm := terminal("InvalidClass", "unknown class")
	if tm.Outcome != OutcomeTerminal || tm.Reason != "InvalidClass" {
		t.Errorf("terminal() = %+v", tm)
	}
	if tm.Err != nil {
		t.Errorf("terminal() must not carry an error, got %v", tm.Err)
	}

	err := errors.New("boom")
	tr := transient(err)
	if tr.Outcome != OutcomeTransient {
		t.Errorf("transient().Outcome = %q, want %q", tr.Outcome, OutcomeTransient)
	}
	if tr.Err != err {
		t.Errorf("transient() Err = %v, want original error", tr.Err)
	}
}
