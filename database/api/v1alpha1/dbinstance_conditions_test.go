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
	s.SetCondition(metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: "Provisioning"})
	s.SetCondition(metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: "DBInstanceReady"})

	if len(s.Conditions) != 1 {
		t.Fatalf("want 1 condition after upsert, got %d", len(s.Conditions))
	}
	if !s.IsConditionTrue(ConditionReady) {
		t.Fatal("want Ready=True after upsert")
	}
	if got := s.GetCondition(ConditionReady); got == nil || got.Reason != "DBInstanceReady" {
		t.Fatalf("GetCondition returned %+v, want reason=DBInstanceReady", got)
	}
}

func TestDerivePhase(t *testing.T) {
	running, stopped := true, false

	withCond := func(spec DBInstanceSpec, c metav1.Condition) *DBInstance {
		inst := &DBInstance{Spec: spec}
		inst.Status.SetCondition(c)
		return inst
	}

	cases := []struct {
		name string
		inst *DBInstance
		want string
	}{
		{"creating", &DBInstance{}, StatusCreating},
		{"ready", withCond(DBInstanceSpec{}, metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: "DBInstanceReady"}), StatusAvailable},
		{"failed", withCond(DBInstanceSpec{}, metav1.Condition{Type: ConditionFailed, Status: metav1.ConditionTrue, Reason: "InvalidClass"}), StatusFailed},
		{"crashloop-is-failed", withCond(DBInstanceSpec{}, metav1.Condition{Type: ConditionCrashLoopHalted, Status: metav1.ConditionTrue, Reason: "CrashLoopDetected"}), StatusFailed},
		{"stopped", &DBInstance{Spec: DBInstanceSpec{Running: &stopped}}, StatusStopped},
		{"modifying", func() *DBInstance {
			i := &DBInstance{Spec: DBInstanceSpec{Running: &running}}
			i.Generation = 2
			i.Status.ObservedGeneration = 1
			return i
		}(), StatusModifying},
	}

	for _, tc := range cases {
		if got := DerivePhase(tc.inst); got != tc.want {
			t.Errorf("%s: DerivePhase = %q, want %q", tc.name, got, tc.want)
		}
	}
}
