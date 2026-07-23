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
	"context"
	"testing"
	"time"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

type fakeStep struct {
	name string
	run  func(context.Context, *dbaasv1.DBInstance) Result
}

func (s fakeStep) Name() string { return s.name }

func (s fakeStep) Run(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return s.run(ctx, inst)
}

func TestRunnerStopsAtFirstNonSatisfied(t *testing.T) {
	var ran []string
	step := func(name string, result Result) Step {
		return fakeStep{name: name, run: func(context.Context, *dbaasv1.DBInstance) Result {
			ran = append(ran, name)
			return result
		}}
	}

	result := NewRunner(
		step("a", Satisfied()),
		step("b", PendingAfter("Waiting", "waiting", 10*time.Second)),
		step("c", Satisfied()),
	).Run(context.Background(), &dbaasv1.DBInstance{})

	if result.Outcome != OutcomePending {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OutcomePending)
	}
	if len(ran) != 2 || ran[0] != "a" || ran[1] != "b" {
		t.Fatalf("ran = %v, want [a b]", ran)
	}
}

func TestRunnerAllSatisfied(t *testing.T) {
	ok := fakeStep{name: "ok", run: func(context.Context, *dbaasv1.DBInstance) Result {
		return Satisfied()
	}}
	result := NewRunner(ok, ok, ok).Run(context.Background(), &dbaasv1.DBInstance{})
	if result.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OutcomeSatisfied)
	}
}

func TestRunnerUnknownOutcomeIsTransient(t *testing.T) {
	bogus := fakeStep{name: "bogus", run: func(context.Context, *dbaasv1.DBInstance) Result {
		return Result{Outcome: Outcome("Bogus")}
	}}
	result := NewRunner(bogus).Run(context.Background(), &dbaasv1.DBInstance{})
	if result.Outcome != OutcomeTransient || result.Err == nil {
		t.Fatalf("want Transient with error, got %+v", result)
	}
}
