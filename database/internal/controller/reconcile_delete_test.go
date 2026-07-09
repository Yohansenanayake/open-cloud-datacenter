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

// Deletion cleanup for the two controller-private, cross-namespace Secrets
// (PR8). They can't carry an owner reference (different namespace than the
// DBInstance), so reconcileDelete must remove them itself: by the recorded
// ref first, then a UID-label sweep as backstop for a ref lost to a status
// reset or a Secret created before the ref was recorded.

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
)

func newDeletingInst() *dbaasv1.DBInstance {
	inst := newProvisionInst()
	now := metav1.NewTime(time.Now())
	inst.DeletionTimestamp = &now
	inst.Status.Resources.VMName = "pg-orders"
	return inst
}

func operatorSecret(name string, withUIDLabel bool) *corev1.Secret {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dbaas-system"}}
	if withUIDLabel {
		sec.Labels = map[string]string{dbaasv1.LabelDBInstanceUID: "orders-uid"}
	}
	return sec
}

func TestReconcileDeleteRemovesOperatorSecretsByRef(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	inst.Status.Resources.InternalSecretRef = "dbaas-system/" + credentials.InternalSecretName(inst)
	inst.Status.Resources.PrivateTLSSecretRef = "dbaas-system/" + credentials.TLSSecretName(inst)

	internal := operatorSecret(credentials.InternalSecretName(inst), true)
	tls := operatorSecret(credentials.TLSSecretName(inst), true)
	r := newProvisionReconciler(t, &stubHarvester{}, inst, internal, tls)

	if _, err := r.reconcileDelete(ctx, inst); err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	for _, name := range []string{credentials.InternalSecretName(inst), credentials.TLSSecretName(inst)} {
		if err := r.Get(ctx, types.NamespacedName{Namespace: "dbaas-system", Name: name}, &corev1.Secret{}); err == nil {
			t.Fatalf("operator secret %s still exists after delete", name)
		}
	}
	// Removing the last finalizer from an object that already has a
	// DeletionTimestamp causes the (fake, matching real) API server to reap
	// it immediately — so the DBInstance itself is now gone.
	if err := r.Get(ctx, types.NamespacedName{Name: "orders", Namespace: "tenant-a"}, &dbaasv1.DBInstance{}); !apierrors.IsNotFound(err) {
		t.Fatalf("DBInstance = %v, want NotFound (reaped after finalizer removal)", err)
	}
}

// Refs lost (status reset, or a Secret that predates ref-recording) — the
// UID-label sweep must still find and remove them.
func TestReconcileDeleteSweepsByUIDLabelWhenRefsMissing(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst() // InternalSecretRef/PrivateTLSSecretRef left empty

	internal := operatorSecret(credentials.InternalSecretName(inst), true)
	tls := operatorSecret(credentials.TLSSecretName(inst), true)
	r := newProvisionReconciler(t, &stubHarvester{}, inst, internal, tls)

	if _, err := r.reconcileDelete(ctx, inst); err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	var list corev1.SecretList
	if err := r.List(ctx, &list, client.InNamespace("dbaas-system")); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("operator-namespace secrets remain after label sweep: %+v", list.Items)
	}
}

// A Secret belonging to a different DBInstance (different UID label) must
// survive the sweep — this is the whole point of scoping by UID.
func TestReconcileDeleteSweepDoesNotTouchOtherInstances(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "dbi-other-uid-internal", Namespace: "dbaas-system",
		Labels: map[string]string{dbaasv1.LabelDBInstanceUID: "other-uid"},
	}}
	mine := operatorSecret(credentials.InternalSecretName(inst), true)
	r := newProvisionReconciler(t, &stubHarvester{}, inst, mine, other)

	if _, err := r.reconcileDelete(ctx, inst); err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	if err := r.Get(ctx, types.NamespacedName{Namespace: "dbaas-system", Name: "dbi-other-uid-internal"}, &corev1.Secret{}); err != nil {
		t.Fatalf("unrelated instance's secret was deleted: %v", err)
	}
}
