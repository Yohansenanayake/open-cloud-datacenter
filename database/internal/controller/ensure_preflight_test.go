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
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func TestEnsurePreflightUnknownClassIsTerminal(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Spec.DBInstanceClass = "db.bogus"

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTerminal || res.Reason != "InvalidClass" {
		t.Fatalf("res = %+v, want Terminal/InvalidClass", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "InvalidClass" {
		t.Fatalf("PreflightReady = %+v, want False/InvalidClass", cond)
	}
	r.finalizeStatus(inst)
	if !inst.Status.IsCurrentConditionFalse(dbaasv1.ConditionAccepted, inst.Generation) {
		t.Fatal("Accepted should be False")
	}
	if inst.Status.Phase != dbaasv1.StatusIncompatibleParameters {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusIncompatibleParameters)
	}
}

func TestEnsurePreflightMissingNetworkRefIsTerminal(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Spec.NetworkRef = ""

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTerminal || res.Reason != "NetworkRefMissing" {
		t.Fatalf("res = %+v, want Terminal/NetworkRefMissing", res)
	}
}

func TestEnsurePreflightValidSpecIsSatisfied(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.Resources.NADName != "tenant-a/data-net" {
		t.Fatalf("NADName = %q, want tenant-a/data-net", inst.Status.Resources.NADName)
	}
	r.finalizeStatus(inst)
	if inst.Status.Phase != dbaasv1.StatusCreating {
		t.Fatalf("Phase = %q, want %q on first entry", inst.Status.Phase, dbaasv1.StatusCreating)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("PreflightReady = %+v, want True", cond)
	}
	if cond.ObservedGeneration != inst.Generation {
		t.Fatalf("cond ObservedGeneration = %d, want %d", cond.ObservedGeneration, inst.Generation)
	}
}

// An edit to an immutable field (vs the create-time AppliedSpec snapshot) is
// refused loudly — the guard that sat on the legacy stop/start/modify paths now
// covers every runner entry.
func TestEnsurePreflightImmutableDriftIsTerminal(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Status.AppliedSpec = &dbaasv1.AppliedSpec{NetworkRef: "tenant-a/old-net"} // spec now says tenant-a/data-net

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTerminal || res.Reason != "ImmutableFieldChanged" {
		t.Fatalf("res = %+v, want Terminal/ImmutableFieldChanged", res)
	}
	r.finalizeStatus(inst)
	if !inst.Status.IsCurrentConditionFalse(dbaasv1.ConditionAccepted, inst.Generation) {
		t.Fatal("Accepted should be False on immutable drift")
	}
}

// A user fixing the spec after a terminal park must clear the failure.
func TestEnsurePreflightRecoversFromTerminalPark(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	networkRef := inst.Spec.NetworkRef
	inst.Spec.NetworkRef = ""

	res := r.ensurePreflight(context.Background(), inst)
	if res.Outcome != OutcomeTerminal || res.Reason != dbaasv1.ReasonNetworkRefMissing {
		t.Fatalf("initial result = %+v, want Terminal/NetworkRefMissing", res)
	}
	failed := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if failed == nil || failed.Status != metav1.ConditionFalse || failed.ObservedGeneration != inst.Generation {
		t.Fatalf("initial PreflightReady = %+v, want current-generation False", failed)
	}

	inst.Spec.NetworkRef = networkRef
	inst.Generation++
	res = r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	recovered := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if recovered == nil || recovered.Status != metav1.ConditionTrue ||
		recovered.Reason != string(dbaasv1.ReasonPreflightPassed) || recovered.ObservedGeneration != inst.Generation {
		t.Fatalf("recovered PreflightReady = %+v, want current-generation True/PreflightPassed", recovered)
	}
	r.finalizeStatus(inst)
	if !inst.Status.IsCurrentConditionTrue(dbaasv1.ConditionAccepted, inst.Generation) {
		t.Fatal("Accepted should be True after correcting the spec")
	}
	if inst.Status.Phase != dbaasv1.StatusCreating {
		t.Fatalf("Phase = %q, want %q after recovery", inst.Status.Phase, dbaasv1.StatusCreating)
	}
}
