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

package credentials

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{dbaasv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("add scheme: %v", err)
		}
	}
	return scheme
}

func testInst() *dbaasv1.DBInstance {
	return &dbaasv1.DBInstance{ObjectMeta: metav1.ObjectMeta{
		Name:      "orders",
		Namespace: "tenant-a",
		UID:       types.UID("orders-uid"),
	}}
}

func newTestResolver(t *testing.T) *Resolver {
	t.Helper()
	scheme := testScheme(t)
	c := ctrlfake.NewClientBuilder().WithScheme(scheme).Build()
	return &Resolver{Client: c, Scheme: scheme, OperatorNamespace: "dbaas-system"}
}

func TestTenantPasswordGenerationFailureIsReturned(t *testing.T) {
	boom := errors.New("entropy unavailable")
	originalRead := randomRead
	randomRead = func([]byte) (int, error) { return 0, boom }
	t.Cleanup(func() { randomRead = originalRead })

	r := newTestResolver(t)
	if _, _, _, err := r.getOrCreateTenant(context.Background(), testInst()); !errors.Is(err, boom) {
		t.Fatalf("getOrCreateTenant error = %v, want entropy error", err)
	}
}

func TestResolveCreatesAllThreeSecretsWithCorrectShapes(t *testing.T) {
	ctx := context.Background()
	inst := testInst()
	r := newTestResolver(t)

	result, err := r.Resolve(ctx, inst)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !result.Changed {
		t.Fatal("first Resolve reported Changed=false, want true")
	}
	m := result.Material
	if m.AdminUser != defaultMasterUser || m.AdminPassword == "" {
		t.Fatalf("Material admin fields = %+v", m)
	}
	if m.ReplPassword == "" || m.ExporterPassword == "" {
		t.Fatalf("Material internal fields = %+v", m)
	}
	if m.TLS == nil || m.TLS.CACertPEM == "" || m.TLS.CAKeyPEM == "" || m.TLS.ServerCertPEM == "" || m.TLS.ServerKeyPEM == "" {
		t.Fatalf("Material TLS = %+v", m.TLS)
	}

	// Tenant credentials Secret: slim (admin_user/admin_password only), owner-ref'd.
	var tenant corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-credentials"}, &tenant); err != nil {
		t.Fatalf("get tenant secret: %v", err)
	}
	if len(tenant.Data) != 0 {
		t.Fatalf("tenant secret Data should be empty on create (StringData only), got %v", tenant.Data)
	}
	if tenant.StringData["admin_user"] != defaultMasterUser || tenant.StringData["admin_password"] != m.AdminPassword {
		t.Fatalf("tenant secret contents = %+v", tenant.StringData)
	}
	if _, hasLegacy := tenant.StringData["ca_cert"]; hasLegacy {
		t.Fatal("tenant secret must not carry TLS keys")
	}
	refs := tenant.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "DBInstance" || refs[0].Controller == nil || !*refs[0].Controller {
		t.Fatalf("tenant secret owner refs = %+v, want controller-owned", refs)
	}

	// Internal Secret: operator namespace, UID-labeled, no owner ref (cross-ns).
	var internal corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: "dbaas-system", Name: InternalSecretName(inst)}, &internal); err != nil {
		t.Fatalf("get internal secret: %v", err)
	}
	if internal.StringData["repl_password"] != m.ReplPassword || internal.StringData["exporter_password"] != m.ExporterPassword {
		t.Fatalf("internal secret contents = %+v", internal.StringData)
	}
	if internal.Labels[dbaasv1.LabelDBInstanceUID] != "orders-uid" || internal.Labels[dbaasv1.LabelInstance] != "orders" {
		t.Fatalf("internal secret labels = %+v", internal.Labels)
	}
	if len(internal.GetOwnerReferences()) != 0 {
		t.Fatalf("internal secret must not carry an owner ref (cross-namespace): %+v", internal.GetOwnerReferences())
	}

	// TLS Secret: operator namespace, kubernetes.io/tls, UID-labeled.
	var tls corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: "dbaas-system", Name: TLSSecretName(inst)}, &tls); err != nil {
		t.Fatalf("get TLS secret: %v", err)
	}
	if tls.Type != corev1.SecretTypeTLS {
		t.Fatalf("TLS secret type = %q, want %q", tls.Type, corev1.SecretTypeTLS)
	}
	if tls.StringData["ca.crt"] != m.TLS.CACertPEM || tls.StringData["tls.crt"] != m.TLS.ServerCertPEM || tls.StringData["tls.key"] != m.TLS.ServerKeyPEM {
		t.Fatalf("TLS secret contents = %+v", tls.StringData)
	}
	if tls.Labels[dbaasv1.LabelDBInstanceUID] != "orders-uid" {
		t.Fatalf("TLS secret labels = %+v", tls.Labels)
	}
}

// The load-bearing invariant (ported from the pre-PR8 Harvester client test):
// re-resolving must reuse existing material, not regenerate it — otherwise a
// booted VM's password/CA would diverge from what's persisted.
func TestResolveReusesExistingMaterialOnReentry(t *testing.T) {
	ctx := context.Background()
	inst := testInst()
	r := newTestResolver(t)

	first, err := r.Resolve(ctx, inst)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	second, err := r.Resolve(ctx, inst)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if !first.Changed || second.Changed {
		t.Fatalf("Changed = first:%t second:%t, want true then false", first.Changed, second.Changed)
	}
	m1, m2 := first.Material, second.Material

	if m1.AdminPassword != m2.AdminPassword {
		t.Fatalf("admin password changed across re-entry: %q -> %q", m1.AdminPassword, m2.AdminPassword)
	}
	if m1.ReplPassword != m2.ReplPassword || m1.ExporterPassword != m2.ExporterPassword {
		t.Fatalf("internal passwords changed across re-entry")
	}
	if m1.TLS.CACertPEM != m2.TLS.CACertPEM || m1.TLS.ServerCertPEM != m2.TLS.ServerCertPEM {
		t.Fatalf("TLS material changed across re-entry")
	}
}

// Covers the asymmetric partial state: the cloud-init Secret is gone (scrubbed
// or lost) but the three durable Secrets survive — Resolve must still reuse,
// never regenerate.
func TestResolveReusesMaterialWhenOnlyDurableSecretsSurvive(t *testing.T) {
	ctx := context.Background()
	inst := testInst()
	r := newTestResolver(t)

	first, err := r.Resolve(ctx, inst)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}

	// Nothing else to delete here — cloud-init is out of this package's scope
	// (internal/resource owns it) — but re-resolving with all three durable
	// Secrets already present must still be a pure read.
	second, err := r.Resolve(ctx, inst)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if second.Changed {
		t.Fatal("re-observation of durable Secrets reported a mutation")
	}
	m1, m2 := first.Material, second.Material
	if *m1.TLS != *m2.TLS {
		t.Fatalf("TLS bundle diverged: %+v vs %+v", m1.TLS, m2.TLS)
	}
}

func TestResolveHonoursSpecMasterUsername(t *testing.T) {
	ctx := context.Background()
	inst := testInst()
	inst.Spec.MasterUsername = "custom_admin"
	r := newTestResolver(t)

	result, err := r.Resolve(ctx, inst)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	m := result.Material
	if m.AdminUser != "custom_admin" {
		t.Fatalf("AdminUser = %q, want custom_admin", m.AdminUser)
	}
}

func TestResolveErrorsOnTenantSecretMissingAdminPassword(t *testing.T) {
	ctx := context.Background()
	inst := testInst()
	scheme := testScheme(t)
	broken := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TenantCredentialsSecretName(inst), Namespace: "tenant-a"},
		Data:       map[string][]byte{"admin_user": []byte("dbadmin")}, // no admin_password
	}
	c := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(broken).Build()
	r := &Resolver{Client: c, Scheme: scheme, OperatorNamespace: "dbaas-system"}

	if _, err := r.Resolve(ctx, inst); err == nil {
		t.Fatal("Resolve succeeded despite missing admin_password, want error")
	}
}

func TestResolvePartialStateCreatesMissingSecretWithoutRotatingExistingMaterial(t *testing.T) {
	ctx := context.Background()
	inst := testInst()
	r := newTestResolver(t)

	first, err := r.Resolve(ctx, inst)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	internal := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: r.OperatorNamespace,
		Name:      InternalSecretName(inst),
	}}
	if err := r.Client.Delete(ctx, internal); err != nil {
		t.Fatalf("delete internal Secret: %v", err)
	}

	second, err := r.Resolve(ctx, inst)
	if err != nil {
		t.Fatalf("partial-state Resolve: %v", err)
	}
	if !second.Changed {
		t.Fatal("partial-state Resolve reported Changed=false")
	}
	if second.Material.AdminPassword != first.Material.AdminPassword ||
		second.Material.TLS.CACertPEM != first.Material.TLS.CACertPEM {
		t.Fatal("existing tenant or TLS material rotated while repairing partial state")
	}
}

func TestResolveAdoptsAlreadyExistsRaceWinner(t *testing.T) {
	ctx := context.Background()
	inst := testInst()
	r := newTestResolver(t)
	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fake client does not implement client.WithWatch")
	}
	winnerPassword := "winner-password"
	intercepted := false
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			sec, isSecret := obj.(*corev1.Secret)
			if !intercepted && isSecret && sec.Name == TenantCredentialsSecretName(inst) {
				intercepted = true
				winner := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: sec.Name, Namespace: sec.Namespace},
					StringData: map[string]string{"admin_user": "dbadmin", "admin_password": winnerPassword},
				}
				if err := c.Create(ctx, winner); err != nil {
					return err
				}
				return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, sec.Name)
			}
			return c.Create(ctx, obj, opts...)
		},
	})

	result, err := r.Resolve(ctx, inst)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !result.Changed || result.Material.AdminPassword != winnerPassword {
		t.Fatalf("result = %+v, want Changed with race winner material", result)
	}
}

func TestResolveRejectsMalformedInternalAndTLSSecrets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Resolver, *dbaasv1.DBInstance) error
	}{
		{
			name: "internal credentials missing exporter password",
			mutate: func(r *Resolver, inst *dbaasv1.DBInstance) error {
				key := types.NamespacedName{Namespace: r.OperatorNamespace, Name: InternalSecretName(inst)}
				var sec corev1.Secret
				if err := r.Client.Get(context.Background(), key, &sec); err != nil {
					return err
				}
				delete(sec.StringData, "exporter_password")
				delete(sec.Data, "exporter_password")
				return r.Client.Update(context.Background(), &sec)
			},
		},
		{
			name: "TLS missing CA key",
			mutate: func(r *Resolver, inst *dbaasv1.DBInstance) error {
				key := types.NamespacedName{Namespace: r.OperatorNamespace, Name: TLSSecretName(inst)}
				var sec corev1.Secret
				if err := r.Client.Get(context.Background(), key, &sec); err != nil {
					return err
				}
				delete(sec.StringData, "ca.key")
				delete(sec.Data, "ca.key")
				return r.Client.Update(context.Background(), &sec)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			inst := testInst()
			r := newTestResolver(t)
			if _, err := r.Resolve(ctx, inst); err != nil {
				t.Fatalf("seed Resolve: %v", err)
			}
			if err := tc.mutate(r, inst); err != nil {
				t.Fatalf("mutate Secret: %v", err)
			}
			if _, err := r.Resolve(ctx, inst); err == nil {
				t.Fatal("Resolve succeeded with malformed persisted material")
			}
		})
	}
}
