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

package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetConditionUpsert(t *testing.T) {
	s := &DBInstanceStatus{}
	s.SetCondition(metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: string(ReasonProvisioning)})
	s.SetCondition(metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: string(ReasonDBInstanceReady)})

	if len(s.Conditions) != 1 {
		t.Fatalf("want 1 condition after upsert, got %d", len(s.Conditions))
	}
	if !s.IsConditionTrue(ConditionReady) {
		t.Fatal("want Ready=True after upsert")
	}
	if got := s.GetCondition(ConditionReady); got == nil || got.Reason != string(ReasonDBInstanceReady) {
		t.Fatalf("GetCondition returned %+v, want reason=DBInstanceReady", got)
	}
}

func TestDerivePhaseSummary(t *testing.T) {
	running, stopped := true, false

	withCond := func(spec DBInstanceSpec, generation, observed int64, typ string, status metav1.ConditionStatus, reason ConditionReason) *DBInstance {
		inst := &DBInstance{Spec: spec}
		inst.Generation = generation
		inst.Status.ObservedGeneration = observed
		inst.Status.SetCondition(metav1.Condition{Type: typ, Status: status, Reason: string(reason), ObservedGeneration: generation})
		return inst
	}

	cases := []struct {
		name string
		inst *DBInstance
		want string
	}{
		{"creating", &DBInstance{}, StatusCreating},
		{"ready", withCond(DBInstanceSpec{}, 1, 1, ConditionReady, metav1.ConditionTrue, ReasonDBInstanceReady), StatusAvailable},
		{"incompatible", withCond(DBInstanceSpec{}, 2, 1, ConditionAccepted, metav1.ConditionFalse, ReasonInvalidClass), StatusIncompatibleParameters},
		{"degraded", withCond(DBInstanceSpec{}, 1, 1, ConditionDegraded, metav1.ConditionTrue, ReasonPostgresUnreachable), StatusDegraded},
		{"crash-loop-halted", withCond(DBInstanceSpec{}, 1, 1, ConditionCrashLoopHalted, metav1.ConditionTrue, ReasonCrashLoopDetected), StatusCrashLoopHalted},
		{"stopped", withCond(DBInstanceSpec{Running: &stopped}, 2, 1, ConditionPowerStateReady, metav1.ConditionTrue, ReasonStopped), StatusStopped},
		{"stopping", withCond(DBInstanceSpec{Running: &stopped}, 2, 1, ConditionPowerStateReady, metav1.ConditionFalse, ReasonStopping), StatusStopping},
		{"starting-established", withCond(DBInstanceSpec{Running: &running}, 2, 1, ConditionPowerStateReady, metav1.ConditionFalse, ReasonStarting), StatusStarting},
		{"starting-established-database-recovery", withCond(DBInstanceSpec{Running: &running}, 2, 1, ConditionReady, metav1.ConditionFalse, ReasonPostgresInitializing), StatusStarting},
		{"initial-boot-is-creating", withCond(DBInstanceSpec{Running: &running}, 1, 0, ConditionPowerStateReady, metav1.ConditionFalse, ReasonStarting), StatusCreating},
		{"initial-boot-database-recovery-is-creating", withCond(DBInstanceSpec{Running: &running}, 1, 0, ConditionReady, metav1.ConditionFalse, ReasonPostgresInitializing), StatusCreating},
		{"resize-active", withCond(DBInstanceSpec{Running: &running}, 2, 1, ConditionResizeInProgress, metav1.ConditionTrue, ReasonResizeStopping), StatusModifying},
		{"generation-lag-only-is-creating", &DBInstance{Spec: DBInstanceSpec{Running: &running}, ObjectMeta: metav1.ObjectMeta{Generation: 2}, Status: DBInstanceStatus{ObservedGeneration: 1}}, StatusCreating},
	}

	for _, tc := range cases {
		if got := DerivePhaseSummary(tc.inst).Phase; got != tc.want {
			t.Errorf("%s: DerivePhaseSummary().Phase = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDerivePhaseSummaryPriority(t *testing.T) {
	inst := &DBInstance{ObjectMeta: metav1.ObjectMeta{Generation: 2}}
	inst.Status.SetCondition(metav1.Condition{Type: ConditionAccepted, Status: metav1.ConditionFalse, Reason: string(ReasonInvalidClass), ObservedGeneration: 2})
	inst.Status.SetCondition(metav1.Condition{Type: ConditionCrashLoopHalted, Status: metav1.ConditionTrue, Reason: string(ReasonCrashLoopDetected), ObservedGeneration: 1})
	inst.Status.SetCondition(metav1.Condition{Type: ConditionDegraded, Status: metav1.ConditionTrue, Reason: string(ReasonPostgresUnreachable), ObservedGeneration: 1})

	if got := DerivePhaseSummary(inst).Phase; got != StatusCrashLoopHalted {
		t.Fatalf("phase = %q, want crash-loop-halted to win", got)
	}

	now := metav1.Now()
	inst.DeletionTimestamp = &now
	if got := DerivePhaseSummary(inst).Phase; got != StatusDeleting {
		t.Fatalf("phase = %q, want deleting to win", got)
	}
}
