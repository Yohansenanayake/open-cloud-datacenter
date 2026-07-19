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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

func TestPatchStatusIfChangedRetriesConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := dbaasv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	inst := &dbaasv1.DBInstance{ObjectMeta: metav1.ObjectMeta{Name: "db1", Namespace: "tenant"}}
	patchCalls := 0
	getCalls := 0
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&dbaasv1.DBInstance{}).
		WithObjects(inst).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				getCalls++
				return c.Get(ctx, key, obj, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				patchCalls++
				if patchCalls == 1 {
					return apierrors.NewConflict(schema.GroupResource{Group: dbaasv1.GroupVersion.Group, Resource: "dbinstances"}, obj.GetName(), errors.New("injected conflict"))
				}
				return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()
	r := &DBInstanceReconciler{Client: c}
	ctx := context.Background()
	key := client.ObjectKeyFromObject(inst)

	cur := &dbaasv1.DBInstance{}
	if err := c.Get(ctx, key, cur); err != nil {
		t.Fatalf("get: %v", err)
	}
	original := cur.DeepCopy()
	cur.Status.Phase = dbaasv1.StatusCreating
	getCalls = 0

	if err := r.patchStatusIfChanged(ctx, original, cur); err != nil {
		t.Fatalf("patch after conflict: %v", err)
	}
	if patchCalls != 2 {
		t.Fatalf("patch calls = %d, want 2", patchCalls)
	}
	if getCalls != 2 {
		t.Fatalf("get calls = %d, want one per attempt", getCalls)
	}
	if cur.ResourceVersion != original.ResourceVersion {
		t.Fatalf("caller resourceVersion = %q, want original %q", cur.ResourceVersion, original.ResourceVersion)
	}

	got := &dbaasv1.DBInstance{}
	if err := c.Get(ctx, key, got); err != nil {
		t.Fatalf("get after retry: %v", err)
	}
	if got.Status.Phase != dbaasv1.StatusCreating {
		t.Fatalf("phase = %q, want %q", got.Status.Phase, dbaasv1.StatusCreating)
	}
}
