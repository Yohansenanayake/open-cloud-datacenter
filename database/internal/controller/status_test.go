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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func TestPatchStatusIfChanged(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := dbaasv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	inst := &dbaasv1.DBInstance{ObjectMeta: metav1.ObjectMeta{Name: "db1", Namespace: "tenant"}}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&dbaasv1.DBInstance{}).
		WithObjects(inst).
		Build()
	r := &DBInstanceReconciler{Client: c}
	ctx := context.Background()
	key := client.ObjectKeyFromObject(inst)

	// No status change -> no error, nothing to persist.
	cur := &dbaasv1.DBInstance{}
	if err := c.Get(ctx, key, cur); err != nil {
		t.Fatalf("get: %v", err)
	}
	original := cur.DeepCopy()
	if err := r.patchStatusIfChanged(ctx, original, cur); err != nil {
		t.Fatalf("no-op patch: %v", err)
	}

	// Status change -> persisted via MergeFrom.
	cur.Status.SetCondition(metav1.Condition{
		Type:    dbaasv1.ConditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  "Provisioning",
		Message: "still provisioning",
	})
	if err := r.patchStatusIfChanged(ctx, original, cur); err != nil {
		t.Fatalf("patch: %v", err)
	}

	got := &dbaasv1.DBInstance{}
	if err := c.Get(ctx, key, got); err != nil {
		t.Fatalf("get after patch: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, dbaasv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("Ready condition not persisted: %+v", got.Status.Conditions)
	}
}
