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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
	dbresource "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

func TestEnsureCredentialsResolvesAndRecordsRefsWithoutEndpoint(t *testing.T) {
	ctx := context.Background()
	inst := newProvisionInst() // no Endpoint yet
	r := newProvisionReconciler(t, &stubHarvester{}, inst)

	res := r.ensureCredentials(ctx, inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	refs := inst.Status.Resources
	if refs.SecretName != "pg-orders-credentials" {
		t.Fatalf("SecretName = %q, want pg-orders-credentials", refs.SecretName)
	}
	if refs.InternalSecretRef != "dbaas-system/dbi-orders-uid-internal" {
		t.Fatalf("InternalSecretRef = %q", refs.InternalSecretRef)
	}
	if refs.PrivateTLSSecretRef != "dbaas-system/dbi-orders-uid-tls" {
		t.Fatalf("PrivateTLSSecretRef = %q", refs.PrivateTLSSecretRef)
	}
	if inst.Status.MasterUserSecret == nil || inst.Status.MasterUserSecret.Name != "pg-orders-credentials" ||
		inst.Status.MasterUserSecret.Status != dbaasv1.SecretStatusActive {
		t.Fatalf("MasterUserSecret = %+v", inst.Status.MasterUserSecret)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionCredentialsReady) {
		t.Fatal("CredentialsReady should be True")
	}

	// No endpoint yet: the connection Secret must not be created or referenced.
	if refs.ConnectionSecretName != "" {
		t.Fatalf("ConnectionSecretName = %q, want empty (no endpoint yet)", refs.ConnectionSecretName)
	}
	var conn corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: dbresource.ConnectionSecretName(inst)}, &conn); err == nil {
		t.Fatal("connection secret should not exist before an endpoint is known")
	}

	// All three durable secrets actually exist in the cluster.
	var tenant corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-credentials"}, &tenant); err != nil {
		t.Fatalf("tenant credentials secret missing: %v", err)
	}
	var internal corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: "dbaas-system", Name: credentials.InternalSecretName(inst)}, &internal); err != nil {
		t.Fatalf("internal secret missing: %v", err)
	}
	var tls corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: "dbaas-system", Name: credentials.TLSSecretName(inst)}, &tls); err != nil {
		t.Fatalf("TLS secret missing: %v", err)
	}
}

func TestEnsureCredentialsPublishesConnectionSecretOnceEndpointKnown(t *testing.T) {
	ctx := context.Background()
	inst := newProvisionInst()
	inst.Status.Endpoint = &dbaasv1.Endpoint{Address: "192.168.40.50", Port: defaultPort}
	r := newProvisionReconciler(t, &stubHarvester{}, inst)

	res := r.ensureCredentials(ctx, inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.Resources.ConnectionSecretName != "pg-orders-connect" {
		t.Fatalf("ConnectionSecretName = %q, want pg-orders-connect", inst.Status.Resources.ConnectionSecretName)
	}

	var conn corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-connect"}, &conn); err != nil {
		t.Fatalf("connection secret missing: %v", err)
	}
	if conn.StringData["host"] != "192.168.40.50" || conn.StringData["dbname"] != "orders" {
		t.Fatalf("StringData = %+v", conn.StringData)
	}
	if conn.StringData["ca.crt"] == "" {
		t.Fatal("connection secret must carry the CA cert once TLS material is resolved")
	}
	if refs := conn.GetOwnerReferences(); len(refs) != 1 || refs[0].Kind != "DBInstance" {
		t.Fatalf("connection secret owner refs = %+v, want controller-owned", refs)
	}
}

// A resolve failure (material genuinely can't be established) blocks
// provisioning — ensureVM depends on it — so it must be Transient, not
// swallowed like the connection-secret best-effort path below.
func TestEnsureCredentialsResolveFailureIsTransient(t *testing.T) {
	ctx := context.Background()
	inst := newProvisionInst()
	// Seed a broken tenant secret (missing admin_password) so Resolve fails.
	broken := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders-credentials", Namespace: "tenant-a"},
		Data:       map[string][]byte{"admin_user": []byte("dbadmin")},
	}
	r := newProvisionReconciler(t, &stubHarvester{}, inst, broken)

	res := r.ensureCredentials(ctx, inst)

	if res.Outcome != OutcomeTransient || res.Err == nil {
		t.Fatalf("res = %+v, want Transient with error", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionCredentialsReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "CredentialsResolveFailed" {
		t.Fatalf("CredentialsReady = %+v, want False/CredentialsResolveFailed", cond)
	}
}

// The connection Secret is a convenience, not required for the database to
// function — a failure to apply it must not block provisioning.
func TestEnsureCredentialsConnectionSecretFailureIsNonFatal(t *testing.T) {
	ctx := context.Background()
	inst := newProvisionInst()
	inst.Status.Endpoint = &dbaasv1.Endpoint{Address: "192.168.40.50", Port: defaultPort}

	boom := errors.New("apiserver unavailable")
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture's fake client does not implement client.WithWatch")
	}
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if sec, ok := obj.(*corev1.Secret); ok && sec.Name == dbresource.ConnectionSecretName(inst) {
				return boom
			}
			return c.Create(ctx, obj, opts...)
		},
	})

	res := r.ensureCredentials(ctx, inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied (non-fatal)", res.Outcome)
	}
	// Material resolution itself succeeded, so CredentialsReady stays True;
	// only the connection-secret ref is left unset for the next pass to retry.
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionCredentialsReady) {
		t.Fatal("CredentialsReady should remain True — material resolution succeeded")
	}
	if inst.Status.Resources.ConnectionSecretName != "" {
		t.Fatalf("ConnectionSecretName = %q, want unset after a failed apply", inst.Status.Resources.ConnectionSecretName)
	}
}
