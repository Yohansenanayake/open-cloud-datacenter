/*
Copyright 2017 The Kubernetes Authors.
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

Adapted from github.com/fluxcd/pkg/runtime/patch/patch_test.go at v0.111.0.
*/

package patch

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func patchTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := dbaasv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add DBInstance scheme: %v", err)
	}
	return scheme
}

func patchTestObject() *dbaasv1.DBInstance {
	return &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "tenant", Generation: 1},
	}
}

func statusClient(t *testing.T, obj *dbaasv1.DBInstance, funcs interceptor.Funcs) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(patchTestScheme(t)).
		WithStatusSubresource(&dbaasv1.DBInstance{}).
		WithObjects(obj).
		WithInterceptorFuncs(funcs).
		Build()
}

func getPatchObject(t *testing.T, ctx context.Context, patchClient client.Client, key client.ObjectKey) *dbaasv1.DBInstance {
	t.Helper()
	obj := &dbaasv1.DBInstance{}
	if err := patchClient.Get(ctx, key, obj); err != nil {
		t.Fatalf("get DBInstance: %v", err)
	}
	return obj
}

func TestHelperNoOpProducesNoWrite(t *testing.T) {
	ctx := context.Background()
	obj := patchTestObject()
	patchCalls := 0
	patchClient := statusClient(t, obj, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalls++
			return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
		},
	})
	current := getPatchObject(t, ctx, patchClient, client.ObjectKeyFromObject(obj))
	helper, err := NewHelper(current, patchClient)
	if err != nil {
		t.Fatalf("NewHelper(): %v", err)
	}
	if err := helper.Patch(ctx, current); err != nil {
		t.Fatalf("Patch(): %v", err)
	}
	if patchCalls != 0 {
		t.Fatalf("status patch calls = %d, want 0", patchCalls)
	}
}

func TestOrdinaryStatusPatchDoesNotGetOrRetry(t *testing.T) {
	ctx := context.Background()
	obj := patchTestObject()
	getCalls := 0
	patchCalls := 0
	patchClient := statusClient(t, obj, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			getCalls++
			return c.Get(ctx, key, obj, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalls++
			return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
		},
	})
	current := getPatchObject(t, ctx, patchClient, client.ObjectKeyFromObject(obj))
	helper, err := NewHelper(current, patchClient)
	if err != nil {
		t.Fatalf("NewHelper(): %v", err)
	}
	current.Status.Phase = dbaasv1.StatusCreating
	getCalls = 0

	if err := helper.Patch(ctx, current); err != nil {
		t.Fatalf("Patch(): %v", err)
	}
	if getCalls != 0 {
		t.Fatalf("Get calls = %d, want 0 for ordinary status", getCalls)
	}
	if patchCalls != 1 {
		t.Fatalf("status patch calls = %d, want 1", patchCalls)
	}
}

func TestHelperPreservesForeignCondition(t *testing.T) {
	ctx := context.Background()
	obj := patchTestObject()
	patchClient := statusClient(t, obj, interceptor.Funcs{})
	key := client.ObjectKeyFromObject(obj)
	current := getPatchObject(t, ctx, patchClient, key)
	helper, err := NewHelper(current, patchClient)
	if err != nil {
		t.Fatalf("NewHelper(): %v", err)
	}

	current.SetCurrentCondition(dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "ready")
	current.Status.Phase = dbaasv1.StatusAvailable

	foreignWriter := getPatchObject(t, ctx, patchClient, key)
	foreignWriter.Status.SetCondition(metav1.Condition{
		Type:               "BackupReady",
		Status:             metav1.ConditionTrue,
		Reason:             "Completed",
		ObservedGeneration: foreignWriter.Generation,
	})
	if err := patchClient.Status().Update(ctx, foreignWriter); err != nil {
		t.Fatalf("write foreign condition: %v", err)
	}

	if err := helper.Patch(ctx, current, WithOwnedConditions{Conditions: []string{dbaasv1.ConditionReady}}); err != nil {
		t.Fatalf("Patch(): %v", err)
	}
	got := getPatchObject(t, ctx, patchClient, key)
	if got.Status.GetCondition(dbaasv1.ConditionReady) == nil {
		t.Fatal("owned Ready condition was not persisted")
	}
	if got.Status.GetCondition("BackupReady") == nil {
		t.Fatal("foreign BackupReady condition was overwritten")
	}
	if got.Status.Phase != dbaasv1.StatusAvailable {
		t.Fatalf("Phase = %q, want %q", got.Status.Phase, dbaasv1.StatusAvailable)
	}
}

func TestConditionConflictRetriesInternally(t *testing.T) {
	ctx := context.Background()
	obj := patchTestObject()
	getCalls := 0
	patchCalls := 0
	patchClient := statusClient(t, obj, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			getCalls++
			return c.Get(ctx, key, obj, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalls++
			if patchCalls == 1 {
				return apierrors.NewConflict(
					schema.GroupResource{Group: dbaasv1.GroupVersion.Group, Resource: "dbinstances"},
					obj.GetName(),
					errors.New("injected conflict"),
				)
			}
			return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
		},
	})
	current := getPatchObject(t, ctx, patchClient, client.ObjectKeyFromObject(obj))
	helper, err := NewHelper(current, patchClient)
	if err != nil {
		t.Fatalf("NewHelper(): %v", err)
	}
	current.SetCurrentCondition(dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "ready")
	getCalls = 0

	if err := helper.Patch(ctx, current, WithOwnedConditions{Conditions: []string{dbaasv1.ConditionReady}}); err != nil {
		t.Fatalf("Patch(): %v", err)
	}
	if getCalls != 2 {
		t.Fatalf("condition Get calls = %d, want 2", getCalls)
	}
	if patchCalls < 2 {
		t.Fatalf("status patch calls = %d, want at least 2", patchCalls)
	}
}

func TestConditionConflictRetryExhaustionReturnsError(t *testing.T) {
	ctx := context.Background()
	obj := patchTestObject()
	getCalls := 0
	patchCalls := 0
	patchClient := statusClient(t, obj, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			getCalls++
			return c.Get(ctx, key, obj, opts...)
		},
		SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			patchCalls++
			return apierrors.NewConflict(
				schema.GroupResource{Group: dbaasv1.GroupVersion.Group, Resource: "dbinstances"},
				obj.Name,
				errors.New("persistent conflict"),
			)
		},
	})
	current := getPatchObject(t, ctx, patchClient, client.ObjectKeyFromObject(obj))
	helper, err := NewHelper(current, patchClient)
	if err != nil {
		t.Fatalf("NewHelper(): %v", err)
	}
	current.SetCurrentCondition(dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "ready")
	getCalls = 0

	if err := helper.Patch(ctx, current, WithOwnedConditions{Conditions: []string{dbaasv1.ConditionReady}}); err == nil {
		t.Fatal("Patch() error = nil, want retry exhaustion")
	}
	if getCalls != 5 {
		t.Fatalf("condition Get calls = %d, want 5", getCalls)
	}
	if patchCalls < 5 {
		t.Fatalf("status patch calls = %d, want at least 5", patchCalls)
	}
}

func TestTwoStalePatchersPreserveSeparatelyOwnedConditions(t *testing.T) {
	ctx := context.Background()
	obj := patchTestObject()
	patchClient := statusClient(t, obj, interceptor.Funcs{})
	key := client.ObjectKeyFromObject(obj)

	controllerA := getPatchObject(t, ctx, patchClient, key)
	controllerB := getPatchObject(t, ctx, patchClient, key)
	patcherA := NewSerialPatcher(controllerA, patchClient)
	patcherB := NewSerialPatcher(controllerB, patchClient)

	controllerA.SetCurrentCondition(dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "ready")
	if err := patcherA.Patch(ctx, controllerA, WithOwnedConditions{Conditions: []string{dbaasv1.ConditionReady}}); err != nil {
		t.Fatalf("controller A Patch(): %v", err)
	}

	controllerB.Status.SetCondition(metav1.Condition{
		Type:               "BackupReady",
		Status:             metav1.ConditionTrue,
		Reason:             "Completed",
		ObservedGeneration: controllerB.Generation,
	})
	if err := patcherB.Patch(ctx, controllerB, WithOwnedConditions{Conditions: []string{"BackupReady"}}); err != nil {
		t.Fatalf("controller B Patch(): %v", err)
	}

	got := getPatchObject(t, ctx, patchClient, key)
	if got.Status.GetCondition(dbaasv1.ConditionReady) == nil {
		t.Fatal("controller B overwrote controller A's Ready condition")
	}
	if got.Status.GetCondition("BackupReady") == nil {
		t.Fatal("controller B's BackupReady condition was not persisted")
	}
}

func TestSerialPatcherRetainsBaseAfterFailure(t *testing.T) {
	ctx := context.Background()
	obj := patchTestObject()
	boom := errors.New("status unavailable")
	patchCalls := 0
	patchClient := statusClient(t, obj, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalls++
			if patchCalls == 1 {
				return boom
			}
			return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
		},
	})
	key := client.ObjectKeyFromObject(obj)
	current := getPatchObject(t, ctx, patchClient, key)
	serial := NewSerialPatcher(current, patchClient)
	current.Status.Phase = dbaasv1.StatusCreating

	if err := serial.Patch(ctx, current); !errors.Is(err, boom) {
		t.Fatalf("first Patch() error = %v, want %v", err, boom)
	}
	if err := serial.Patch(ctx, current); err != nil {
		t.Fatalf("second Patch(): %v", err)
	}
	got := getPatchObject(t, ctx, patchClient, key)
	if got.Status.Phase != dbaasv1.StatusCreating {
		t.Fatalf("Phase = %q, want %q", got.Status.Phase, dbaasv1.StatusCreating)
	}
}

func TestSerialPatcherConsecutivePatches(t *testing.T) {
	ctx := context.Background()
	obj := patchTestObject()
	patchClient := statusClient(t, obj, interceptor.Funcs{})
	key := client.ObjectKeyFromObject(obj)
	current := getPatchObject(t, ctx, patchClient, key)
	serial := NewSerialPatcher(current, patchClient)

	current.Status.Phase = dbaasv1.StatusCreating
	if err := serial.Patch(ctx, current); err != nil {
		t.Fatalf("first Patch(): %v", err)
	}
	current.Status.Phase = dbaasv1.StatusAvailable
	if err := serial.Patch(ctx, current); err != nil {
		t.Fatalf("second Patch(): %v", err)
	}

	got := getPatchObject(t, ctx, patchClient, key)
	if got.Status.Phase != dbaasv1.StatusAvailable {
		t.Fatalf("Phase = %q, want %q", got.Status.Phase, dbaasv1.StatusAvailable)
	}
}
