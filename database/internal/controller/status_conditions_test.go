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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func TestSyncAcceptedConditionTruthTable(t *testing.T) {
	tests := []struct {
		name       string
		preflight  *metav1.Condition
		storage    *metav1.Condition
		want       metav1.ConditionStatus
		wantReason dbaasv1.ConditionReason
	}{
		{"pending when preflight missing", nil, nil, metav1.ConditionUnknown, dbaasv1.ReasonValidationPending},
		{"accepted without abnormal condition", condition(dbaasv1.ConditionPreflightReady, metav1.ConditionTrue, dbaasv1.ReasonPreflightPassed, 3), nil, metav1.ConditionTrue, dbaasv1.ReasonSpecAccepted},
		{"preflight rejection", condition(dbaasv1.ConditionPreflightReady, metav1.ConditionFalse, dbaasv1.ReasonInvalidClass, 3), nil, metav1.ConditionFalse, dbaasv1.ReasonInvalidClass},
		{"storage rejection", condition(dbaasv1.ConditionPreflightReady, metav1.ConditionTrue, dbaasv1.ReasonPreflightPassed, 3), condition(dbaasv1.ConditionStorageChangeRejected, metav1.ConditionTrue, dbaasv1.ReasonUnsupportedShrink, 3), metav1.ConditionFalse, dbaasv1.ReasonUnsupportedShrink},
		{"stale storage rejection ignored", condition(dbaasv1.ConditionPreflightReady, metav1.ConditionTrue, dbaasv1.ReasonPreflightPassed, 3), condition(dbaasv1.ConditionStorageChangeRejected, metav1.ConditionTrue, dbaasv1.ReasonUnsupportedShrink, 2), metav1.ConditionTrue, dbaasv1.ReasonSpecAccepted},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst := newProvisionInst()
			inst.Generation = 3
			if tc.preflight != nil {
				inst.Status.SetCondition(*tc.preflight)
			}
			if tc.storage != nil {
				inst.Status.SetCondition(*tc.storage)
			}
			(&DBInstanceReconciler{}).syncAcceptedCondition(inst)
			got := inst.Status.GetCondition(dbaasv1.ConditionAccepted)
			if got == nil || got.Status != tc.want || got.Reason != string(tc.wantReason) {
				t.Fatalf("Accepted = %+v, want %s/%s", got, tc.want, tc.wantReason)
			}
		})
	}
}

func TestFinalizeStatusAggregatesIntervention(t *testing.T) {
	inst := newProvisionInst()
	inst.SetCurrentCondition(dbaasv1.ConditionCrashLoopHalted, metav1.ConditionTrue, dbaasv1.ReasonCrashLoopDetected, "halted")

	(&DBInstanceReconciler{}).finalizeStatus(inst)

	if !inst.Status.IsConditionTrue(dbaasv1.ConditionInterventionRequired) {
		t.Fatal("InterventionRequired should aggregate CrashLoopHalted")
	}
	if inst.Status.Phase != dbaasv1.StatusCrashLoopHalted {
		t.Fatalf("phase = %q, want crash-loop-halted", inst.Status.Phase)
	}
}

func TestSyncRepaveInProgressClearsAfterSelfHealedDriftSettles(t *testing.T) {
	inst := newProvisionInst()
	inst.Generation = 3
	running := true
	inst.Spec.Running = &running
	inst.SetCurrentCondition(dbaasv1.ConditionRepaveInProgress, metav1.ConditionTrue, dbaasv1.ReasonRepaveApplied, "repave applied")
	inst.SetCurrentCondition(dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "database and monitoring ready")
	// ImageDrift is intentionally absent: a status written by a controller
	// build from before ImageDrift became three-valued. Absence must still
	// read as "not drifted" so an in-flight repave can settle across an
	// operator upgrade.

	(&DBInstanceReconciler{}).syncRepaveInProgressCondition(inst)

	if inst.Status.GetCondition(dbaasv1.ConditionRepaveInProgress) != nil {
		t.Fatal("RepaveInProgress should clear after drift is gone and Ready is current")
	}
}

// TestSyncRepaveInProgressDriftStates pins the exact coupling between
// ImageDrift's three values and the repave activity condition. Before
// ImageDrift gained an explicit False state this step tested the condition's
// PRESENCE, so a written-out False would have pinned RepaveInProgress=True
// forever — every repave would hang one step short of done.
func TestSyncRepaveInProgressDriftStates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		driftState metav1.ConditionStatus
		driftWhy   dbaasv1.ConditionReason
		wantClear  bool
	}{
		{"resolved drift clears", metav1.ConditionFalse, dbaasv1.ReasonImageUpToDate, true},
		// Unknown must clear too: a mid-repave config change that invalidates
		// the stream would otherwise strand RepaveInProgress=True with
		// nothing left able to retire it.
		{"unevaluable drift clears", metav1.ConditionUnknown, dbaasv1.ReasonImageCatalogUnresolved, true},
		{"live drift keeps the repave in progress", metav1.ConditionTrue, dbaasv1.ReasonOSUpdateAvailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := newProvisionInst()
			inst.Generation = 3
			running := true
			inst.Spec.Running = &running
			inst.SetCurrentCondition(dbaasv1.ConditionRepaveInProgress, metav1.ConditionTrue, dbaasv1.ReasonRepaveApplied, "repave applied")
			inst.SetCurrentCondition(dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "database and monitoring ready")
			inst.SetCurrentCondition(dbaasv1.ConditionImageDrift, tc.driftState, tc.driftWhy, "")

			(&DBInstanceReconciler{}).syncRepaveInProgressCondition(inst)

			cleared := inst.Status.GetCondition(dbaasv1.ConditionRepaveInProgress) == nil
			if cleared != tc.wantClear {
				t.Fatalf("RepaveInProgress cleared = %v with ImageDrift=%s/%s, want %v",
					cleared, tc.driftState, tc.driftWhy, tc.wantClear)
			}
		})
	}
}

func condition(typ string, status metav1.ConditionStatus, reason dbaasv1.ConditionReason, generation int64) *metav1.Condition {
	return &metav1.Condition{Type: typ, Status: status, Reason: string(reason), ObservedGeneration: generation}
}
