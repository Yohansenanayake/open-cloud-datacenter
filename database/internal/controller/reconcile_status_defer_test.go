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
	"strings"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func TestReconcileDeferPersistsStatusOnReconcileError(t *testing.T) {
	ctx := context.Background()
	inst := newProvisionInst()
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	r.EnsureRunner = nil

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
	if err == nil || !strings.Contains(err.Error(), "ensure runner is not configured") {
		t.Fatalf("Reconcile() error = %v, want ensure runner error", err)
	}

	got := &dbaasv1.DBInstance{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(inst), got); err != nil {
		t.Fatalf("get reconciled DBInstance: %v", err)
	}
	if got.Status.GetCondition(dbaasv1.ConditionAccepted) == nil {
		t.Fatalf("deferred finalization did not persist Accepted: %+v", got.Status.Conditions)
	}
}

func TestReconcileDeferJoinsReconcileAndPatchErrors(t *testing.T) {
	ctx := context.Background()
	inst := newProvisionInst()
	patchErr := errors.New("status unavailable")
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	r.EnsureRunner = nil

	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture's fake client does not implement client.WithWatch")
	}
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			return patchErr
		},
	})

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
	if err == nil || !strings.Contains(err.Error(), "ensure runner is not configured") || !errors.Is(err, patchErr) {
		t.Fatalf("Reconcile() error = %v, want reconcile and patch errors", err)
	}
}
