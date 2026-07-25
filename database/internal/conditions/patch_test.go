/*
Copyright 2020 The Kubernetes Authors.
Copyright 2021 The Flux authors.
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

Adapted from github.com/fluxcd/pkg/runtime/conditions/patch_test.go at v0.111.0.
*/

package conditions

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func testCondition(conditionType string, status metav1.ConditionStatus, reason string) metav1.Condition {
	return metav1.Condition{Type: conditionType, Status: status, Reason: reason, ObservedGeneration: 3}
}

func testInstance(conditionList ...metav1.Condition) *dbaasv1.DBInstance {
	return &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "tenant", Generation: 3},
		Status:     dbaasv1.DBInstanceStatus{Conditions: conditionList},
	}
}

func TestNewPatchAndApply(t *testing.T) {
	readyFalse := testCondition("Ready", metav1.ConditionFalse, "Progressing")
	readyTrue := testCondition("Ready", metav1.ConditionTrue, "Succeeded")
	foreign := testCondition("BackupReady", metav1.ConditionTrue, "Completed")

	tests := []struct {
		name    string
		before  *dbaasv1.DBInstance
		after   *dbaasv1.DBInstance
		latest  *dbaasv1.DBInstance
		owned   []string
		want    []metav1.Condition
		wantErr bool
	}{
		{
			name:   "add",
			before: testInstance(),
			after:  testInstance(readyTrue),
			latest: testInstance(foreign),
			owned:  []string{"Ready"},
			want:   []metav1.Condition{foreign, readyTrue},
		},
		{
			name:   "change owned and preserve foreign",
			before: testInstance(readyFalse),
			after:  testInstance(readyTrue),
			latest: testInstance(readyFalse, foreign),
			owned:  []string{"Ready"},
			want:   []metav1.Condition{readyTrue, foreign},
		},
		{
			name:   "remove owned",
			before: testInstance(readyFalse, foreign),
			after:  testInstance(foreign),
			latest: testInstance(readyFalse, foreign),
			owned:  []string{"Ready"},
			want:   []metav1.Condition{foreign},
		},
		{
			name:    "unowned concurrent change conflicts",
			before:  testInstance(readyFalse),
			after:   testInstance(readyTrue),
			latest:  testInstance(testCondition("Ready", metav1.ConditionUnknown, "OtherWriter")),
			wantErr: true,
		},
		{
			name:   "owned concurrent change uses reconciled value",
			before: testInstance(readyFalse),
			after:  testInstance(readyTrue),
			latest: testInstance(testCondition("Ready", metav1.ConditionUnknown, "OtherWriter")),
			owned:  []string{"Ready"},
			want:   []metav1.Condition{readyTrue},
		},
		{
			name:   "already applied state succeeds",
			before: testInstance(readyFalse),
			after:  testInstance(readyTrue),
			latest: testInstance(readyTrue),
			want:   []metav1.Condition{readyTrue},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := NewPatch(tt.before, tt.after)
			err := diff.Apply(tt.latest, WithOwnedConditions(tt.owned...))
			if tt.wantErr {
				if err == nil {
					t.Fatal("Apply() error = nil, want conflict")
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply(): %v", err)
			}
			got := tt.latest.GetConditions()
			if len(got) != len(tt.want) {
				t.Fatalf("conditions length = %d, want %d: %+v", len(got), len(tt.want), got)
			}
			for _, want := range tt.want {
				gotCondition := Get(tt.latest, want.Type)
				if gotCondition == nil || !hasSameState(gotCondition, &want) {
					t.Fatalf("condition %q = %+v, want %+v", want.Type, gotCondition, want)
				}
			}
		})
	}
}

func TestNewPatchNoChanges(t *testing.T) {
	ready := testCondition("Ready", metav1.ConditionTrue, "Succeeded")
	if diff := NewPatch(testInstance(ready), testInstance(ready)); !diff.IsZero() {
		t.Fatalf("NewPatch() = %+v, want zero patch", diff)
	}
}

func TestSetPreservesCurrentConditionSemantics(t *testing.T) {
	transition := metav1.Now()
	inst := testInstance(metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "FirstReason",
		LastTransitionTime: transition,
		ObservedGeneration: 2,
	})

	Set(inst, &metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "UpdatedReason",
		ObservedGeneration: 3,
	})

	got := Get(inst, "Ready")
	if got == nil {
		t.Fatal("Ready condition is missing")
	}
	if !got.LastTransitionTime.Equal(&transition) {
		t.Fatalf("LastTransitionTime = %v, want %v", got.LastTransitionTime, transition)
	}
	if got.ObservedGeneration != 3 {
		t.Fatalf("ObservedGeneration = %d, want 3", got.ObservedGeneration)
	}
}
