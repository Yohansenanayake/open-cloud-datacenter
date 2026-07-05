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
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionFailed) {
		t.Fatal("Failed condition not set")
	}
	if inst.Status.Phase != dbaasv1.StatusFailed {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusFailed)
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

// A user fixing the spec after a terminal park must clear the failure.
func TestEnsurePreflightRecoversFromTerminalPark(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Status.Phase = dbaasv1.StatusFailed
	setStepCond(inst, dbaasv1.ConditionFailed, metav1.ConditionTrue, "InvalidClass", "unknown class")

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.GetCondition(dbaasv1.ConditionFailed) != nil {
		t.Fatal("Failed condition should be cleared after spec fix")
	}
	if inst.Status.Phase != dbaasv1.StatusCreating {
		t.Fatalf("Phase = %q, want %q after recovery", inst.Status.Phase, dbaasv1.StatusCreating)
	}
}
