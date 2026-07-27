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

func condition(typ string, status metav1.ConditionStatus, reason dbaasv1.ConditionReason, generation int64) *metav1.Condition {
	return &metav1.Condition{Type: typ, Status: status, Reason: string(reason), ObservedGeneration: generation}
}
