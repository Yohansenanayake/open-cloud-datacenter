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
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

func TestEnsurePreflightUnknownClassIsTerminal(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Spec.DBInstanceClass = "db.bogus"

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTerminal {
		t.Fatalf("res = %+v, want Terminal", res)
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

	if res.Outcome != OutcomeTerminal {
		t.Fatalf("res = %+v, want Terminal", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonNetworkRefMissing) {
		t.Fatalf("PreflightReady = %+v, want False/NetworkRefMissing", cond)
	}
}

func TestEnsurePreflightValidSpecIsSatisfied(t *testing.T) {
	stub := &stubHarvester{}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newProvisionInst()

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.Resources.NADName != "tenant-a/data-net" {
		t.Fatalf("NADName = %q, want tenant-a/data-net", inst.Status.Resources.NADName)
	}
	if stub.LastVMImageRef != defaultOSImage {
		t.Fatalf("resolved image = %q, want default %q", stub.LastVMImageRef, defaultOSImage)
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

	if res.Outcome != OutcomeTerminal {
		t.Fatalf("res = %+v, want Terminal", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonImmutableFieldChanged) {
		t.Fatalf("PreflightReady = %+v, want False/ImmutableFieldChanged", cond)
	}
	r.finalizeStatus(inst)
	if !inst.Status.IsCurrentConditionFalse(dbaasv1.ConditionAccepted, inst.Generation) {
		t.Fatal("Accepted should be False on immutable drift")
	}
}

// A user fixing the spec after a terminal park must clear the failure.
func TestEnsurePreflightRecoversFromTerminalPark(t *testing.T) {
	r := &DBInstanceReconciler{Harvester: &stubHarvester{}}
	inst := newProvisionInst()
	networkRef := inst.Spec.NetworkRef
	inst.Spec.NetworkRef = ""

	res := r.ensurePreflight(context.Background(), inst)
	if res.Outcome != OutcomeTerminal {
		t.Fatalf("initial result = %+v, want Terminal", res)
	}
	failed := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if failed == nil || failed.Status != metav1.ConditionFalse ||
		failed.Reason != string(dbaasv1.ReasonNetworkRefMissing) || failed.ObservedGeneration != inst.Generation {
		t.Fatalf("initial PreflightReady = %+v, want current-generation False/NetworkRefMissing", failed)
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

func TestEnsurePreflightImageFailures(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		outcome StepOutcome
		status  metav1.ConditionStatus
		reason  dbaasv1.ConditionReason
		requeue bool
	}{
		{"invalid", harvester.ErrVMImageReferenceInvalid, OutcomeTerminal, metav1.ConditionFalse, dbaasv1.ReasonOSImageInvalid, false},
		{"ambiguous", harvester.ErrVMImageAmbiguous, OutcomeTerminal, metav1.ConditionFalse, dbaasv1.ReasonOSImageInvalid, false},
		{"not found", harvester.ErrVMImageNotFound, OutcomeTerminal, metav1.ConditionFalse, dbaasv1.ReasonOSImageNotFound, false},
		{"not ready", harvester.ErrVMImageNotReady, OutcomePending, metav1.ConditionUnknown, dbaasv1.ReasonOSImageNotReady, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &DBInstanceReconciler{Harvester: &stubHarvester{resolveVMImageErr: tt.err}}
			inst := newProvisionInst()
			res := r.ensurePreflight(context.Background(), inst)

			if res.Outcome != tt.outcome {
				t.Fatalf("Outcome = %q, want %q", res.Outcome, tt.outcome)
			}
			if (res.Result.RequeueAfter == preflightRequeue) != tt.requeue {
				t.Fatalf("RequeueAfter = %v, want timer=%v", res.Result.RequeueAfter, tt.requeue)
			}
			cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
			if cond == nil || cond.Status != tt.status || cond.Reason != string(tt.reason) {
				t.Fatalf("PreflightReady = %+v, want %s/%s", cond, tt.status, tt.reason)
			}
		})
	}
}

func TestEnsurePreflightImageAPIFailureIsTransient(t *testing.T) {
	boom := errors.New("harvester API unavailable")
	r := &DBInstanceReconciler{Harvester: &stubHarvester{resolveVMImageErr: boom}}
	inst := newProvisionInst()

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTransient || !errors.Is(res.Err, boom) {
		t.Fatalf("res = %+v, want Transient wrapping API failure", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != string(dbaasv1.ReasonValidationPending) {
		t.Fatalf("PreflightReady = %+v, want Unknown/ValidationPending", cond)
	}
}
