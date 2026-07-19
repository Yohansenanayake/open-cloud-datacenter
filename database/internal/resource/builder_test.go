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

package resource

import (
	"context"
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		dbaasv1.AddToScheme, corev1.AddToScheme, monitoringv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("add scheme: %v", err)
		}
	}
	return scheme
}

func testOwner() *dbaasv1.DBInstance {
	return &dbaasv1.DBInstance{ObjectMeta: metav1.ObjectMeta{
		Name:      "orders",
		Namespace: "tenant-a",
		UID:       types.UID("uid-1234"),
	}}
}

// assertControllerRef verifies the object is controller-owned by the DBInstance —
// the GC contract (fake/envtest run no GC controller, so the ref IS the test).
func assertControllerRef(t *testing.T, obj client.Object) {
	t.Helper()
	refs := obj.GetOwnerReferences()
	if len(refs) != 1 {
		t.Fatalf("owner refs = %d, want 1 (%+v)", len(refs), refs)
	}
	ref := refs[0]
	if ref.Kind != "DBInstance" || ref.Name != "orders" || ref.UID != types.UID("uid-1234") {
		t.Fatalf("owner ref = %+v, want the DBInstance", ref)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Fatal("owner ref must be a controller reference")
	}
}

// Every builder: create → Created with owner ref; unchanged re-apply → None.
func TestApplyBuildersIdempotentWithOwnerRefs(t *testing.T) {
	ctx := context.Background()
	owner := testOwner()
	c := ctrlfake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	builders := map[string]Builder{
		"service":        MetricsService{Instance: owner},
		"endpoints":      MetricsEndpoints{Instance: owner, VMIP: "192.168.40.50"},
		"servicemonitor": ServiceMonitor{Instance: owner},
	}
	for name, b := range builders {
		op, err := Apply(ctx, c, testScheme(t), owner, b)
		if err != nil {
			t.Fatalf("%s: first apply: %v", name, err)
		}
		if op != controllerutil.OperationResultCreated {
			t.Fatalf("%s: first apply op = %q, want created", name, op)
		}

		obj, _ := b.Build()
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			t.Fatalf("%s: get: %v", name, err)
		}
		assertControllerRef(t, obj)

		op, err = Apply(ctx, c, testScheme(t), owner, b)
		if err != nil {
			t.Fatalf("%s: second apply: %v", name, err)
		}
		if op != controllerutil.OperationResultNone {
			t.Fatalf("%s: second apply op = %q, want none (idempotent)", name, op)
		}
	}
}

func TestConnectionSecretFormatsIPv6JDBCURL(t *testing.T) {
	secret := &corev1.Secret{}
	builder := ConnectionSecret{
		Instance: testOwner(), Address: "2001:db8::1", Port: 5432, DBName: "orders",
	}
	if err := builder.Update(secret); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, want := string(secret.Data["jdbcUrl"]), "jdbc:postgresql://[2001:db8::1]:5432/orders?ssl=true&sslmode=verify-ca"; got != want {
		t.Fatalf("jdbcUrl = %q, want %q", got, want)
	}
}

// The Service builder must not clobber the server-assigned clusterIP on update.
func TestMetricsServiceDoesNotClobberClusterIP(t *testing.T) {
	ctx := context.Background()
	owner := testOwner()
	scheme := testScheme(t)

	// Simulate a pre-existing Service whose clusterIP the API server allocated.
	existing := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name: MetricsServiceName(owner), Namespace: "tenant-a",
	}}
	existing.Spec.ClusterIP = "10.43.0.7"
	c := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	if _, err := Apply(ctx, c, scheme, owner, MetricsService{Instance: owner}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var got corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Name: MetricsServiceName(owner), Namespace: "tenant-a"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.ClusterIP != "10.43.0.7" {
		t.Fatalf("clusterIP = %q, want preserved 10.43.0.7", got.Spec.ClusterIP)
	}
	if len(got.Spec.Ports) != 1 || got.Spec.Ports[0].Port != metricsPort {
		t.Fatalf("ports = %+v, want asserted metrics port", got.Spec.Ports)
	}
}

// An IP change (VM restart / live migration) retargets the Endpoints.
func TestMetricsEndpointsRetargetsOnIPChange(t *testing.T) {
	ctx := context.Background()
	owner := testOwner()
	scheme := testScheme(t)
	c := ctrlfake.NewClientBuilder().WithScheme(scheme).Build()

	if _, err := Apply(ctx, c, scheme, owner, MetricsEndpoints{Instance: owner, VMIP: "192.168.40.50"}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	op, err := Apply(ctx, c, scheme, owner, MetricsEndpoints{Instance: owner, VMIP: "192.168.40.99"})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if op != controllerutil.OperationResultUpdated {
		t.Fatalf("op = %q, want updated on IP change", op)
	}

	var got corev1.Endpoints
	if err := c.Get(ctx, types.NamespacedName{Name: MetricsServiceName(owner), Namespace: "tenant-a"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Subsets[0].Addresses[0].IP != "192.168.40.99" {
		t.Fatalf("endpoint IP = %q, want retargeted to 192.168.40.99", got.Subsets[0].Addresses[0].IP)
	}
}
