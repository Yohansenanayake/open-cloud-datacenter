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
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/harvester/harvester/pkg/util"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	dbresource "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

// newProvisionInst returns a DBInstance mid-provisioning: valid spec, finalizer
// already present (Reconcile adds it pre-dispatch in production).
func newProvisionInst() *dbaasv1.DBInstance {
	return &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "orders",
			Namespace:       "tenant-a",
			UID:             "orders-uid",
			Generation:      3,
			Finalizers:      []string{dbaasv1.FinalizerName},
			ResourceVersion: "1",
		},
		Spec: dbaasv1.DBInstanceSpec{
			DBInstanceClass:  "db.t3.small",
			AllocatedStorage: 20,
			NetworkRef:       "tenant-a/data-net",
		},
	}
}

// newProvisionReconciler wires a reconciler with the dbaas + KubeVirt + core +
// monitoring schemes so ensureVM can observe VirtualMachine objects and
// ensureMonitoring can apply its builder-managed children through the fake client.
func newProvisionReconciler(t *testing.T, stub *stubHarvester, objs ...client.Object) *DBInstanceReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		dbaasv1.AddToScheme, kubevirtv1.AddToScheme, corev1.AddToScheme, monitoringv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("add scheme: %v", err)
		}
	}
	fakeClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&dbaasv1.DBInstance{}).
		WithObjects(objs...).
		Build()
	return &DBInstanceReconciler{
		Client:         fakeClient,
		Harvester:      stub,
		GrafanaBaseURL: "https://grafana.example",
	}
}

// testVM returns a VirtualMachine shaped the way CreatePostgresVM builds it for
// newProvisionInst (db.t3.small, 20Gi): runStrategy Always, class cpu/mem in
// resources.limits, data-disk PVC in the volumeClaimTemplates annotation. The
// resize and power steps observe this shape.
func testVM(name, ns string) *kubevirtv1.VirtualMachine {
	return shapedVM(name, ns, "db.t3.small", 20, name+"-data", kubevirtv1.RunStrategyAlways)
}

func shapedVM(name, ns, class string, storageGB int, dvName string, rs kubevirtv1.VirtualMachineRunStrategy) *kubevirtv1.VirtualMachine {
	classSpec := dbaasv1.InstanceClasses[class]
	pvcs := []*corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: dvName},
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(fmt.Sprintf("%dGi", storageGB)),
				},
			},
		},
	}}
	raw, _ := json.Marshal(pvcs)

	vm := &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: map[string]string{util.AnnotationVolumeClaimTemplates: string(raw)},
		},
	}
	vm.Spec.RunStrategy = &rs
	vm.Spec.Template = &kubevirtv1.VirtualMachineInstanceTemplateSpec{}
	vm.Spec.Template.Spec.Domain.Resources.Limits = corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewQuantity(int64(classSpec.CPUCores), resource.DecimalSI),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", classSpec.MemoryMB)),
	}
	return vm
}

// setVMRunStrategy simulates the effect of a StartVM/StopVM provider call on the
// fake cluster (the stub does not touch the fake client's VM object).
func setVMRunStrategy(t *testing.T, c client.Client, name, ns string, rs kubevirtv1.VirtualMachineRunStrategy) {
	t.Helper()
	var vm kubevirtv1.VirtualMachine
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &vm); err != nil {
		t.Fatalf("get vm: %v", err)
	}
	vm.Spec.RunStrategy = &rs
	if err := c.Update(context.Background(), &vm); err != nil {
		t.Fatalf("update vm: %v", err)
	}
}

// --- runner mechanics (injected steps) ---

func TestRunEnsureStepsStopsAtFirstNonSatisfied(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := &dbaasv1.DBInstance{}
	var ran []string
	mk := func(name string, res StepResult) ensureStep {
		return ensureStep{name: name, run: func(context.Context, *dbaasv1.DBInstance) StepResult {
			ran = append(ran, name)
			return res
		}}
	}

	steps := []ensureStep{
		mk("a", satisfied()),
		mk("b", pendingAfter("Waiting", "waiting", 10*time.Second)),
		mk("c", satisfied()),
	}
	res := r.runEnsureSteps(context.Background(), inst, steps)

	if res.Outcome != OutcomePending {
		t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomePending)
	}
	if len(ran) != 2 || ran[0] != "a" || ran[1] != "b" {
		t.Fatalf("ran = %v, want [a b] (step c must not run)", ran)
	}
}

func TestRunEnsureStepsAllSatisfied(t *testing.T) {
	r := &DBInstanceReconciler{}
	ok := ensureStep{name: "ok", run: func(context.Context, *dbaasv1.DBInstance) StepResult {
		return satisfied()
	}}
	res := r.runEnsureSteps(context.Background(), &dbaasv1.DBInstance{}, []ensureStep{ok, ok, ok})
	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeSatisfied)
	}
}

func TestRunEnsureStepsUnknownOutcomeIsTransient(t *testing.T) {
	r := &DBInstanceReconciler{}
	bogus := ensureStep{name: "bogus", run: func(context.Context, *dbaasv1.DBInstance) StepResult {
		return StepResult{Outcome: StepOutcome("Bogus")}
	}}
	res := r.runEnsureSteps(context.Background(), &dbaasv1.DBInstance{}, []ensureStep{bogus})
	if res.Outcome != OutcomeTransient || res.Err == nil {
		t.Fatalf("want Transient with error, got %+v", res)
	}
}

// --- runProvisioning outcome→Result mapping through real scenarios ---

func TestRunProvisioningTerminalParks(t *testing.T) {
	inst := newProvisionInst()
	inst.Spec.DBInstanceClass = "db.bogus"
	r := newProvisionReconciler(t, &stubHarvester{}, inst)

	result, err := r.runProvisioning(context.Background(), inst)

	if err != nil {
		t.Fatalf("terminal must park without error, got %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("terminal must not requeue, got %+v", result)
	}

	got := &dbaasv1.DBInstance{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(inst), got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Status.IsCurrentConditionFalse(dbaasv1.ConditionAccepted, got.Generation) {
		t.Fatalf("Accepted=False not persisted: %+v", got.Status.Conditions)
	}
	if got.Status.Phase != dbaasv1.StatusIncompatibleParameters {
		t.Fatalf("Phase = %q, want %q", got.Status.Phase, dbaasv1.StatusIncompatibleParameters)
	}
}

func TestRunProvisioningTransientReturnsError(t *testing.T) {
	inst := newProvisionInst()
	boom := errors.New("vmi lookup boom")
	stub := &stubHarvester{readinessErr: boom}
	// VM object present so the pass reaches the health gate.
	r := newProvisionReconciler(t, stub, inst, testVM("pg-orders", "tenant-a"))

	_, err := r.runProvisioning(context.Background(), inst)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected transient error", err)
	}
}

func TestRunProvisioningFullWalk(t *testing.T) {
	ctx := context.Background()
	inst := newProvisionInst()
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true,
		IP:      "192.168.40.50",
		Ready:   true,
	}}
	r := newProvisionReconciler(t, stub, inst)
	key := client.ObjectKeyFromObject(inst)

	// Pass 1: no VM observed → create it, stop the pass (event-driven Pending).
	result, err := r.runProvisioning(ctx, inst)
	if err != nil || result != (ctrl.Result{}) {
		t.Fatalf("pass 1 = (%+v, %v), want zero Result and nil error", result, err)
	}
	if stub.CreateVMCalls != 1 {
		t.Fatalf("CreateVMCalls = %d, want 1", stub.CreateVMCalls)
	}
	after1 := &dbaasv1.DBInstance{}
	if err := r.Get(ctx, key, after1); err != nil {
		t.Fatalf("get after pass 1: %v", err)
	}
	if after1.Status.Resources.VMName != "pg-orders" {
		t.Fatalf("VMName = %q, want pg-orders", after1.Status.Resources.VMName)
	}
	// ensureCredentials runs before ensureVM in step order, so the tenant
	// credentials ref and the two operator-namespace refs are already set
	// after pass 1 — before the VM (and its cloud-init Secret) even exist.
	if after1.Status.Resources.SecretName != "pg-orders-credentials" {
		t.Fatalf("SecretName = %q after pass 1, want pg-orders-credentials", after1.Status.Resources.SecretName)
	}
	if after1.Status.Resources.InternalSecretRef != "dbaas-system/dbi-orders-uid-internal" {
		t.Fatalf("InternalSecretRef = %q after pass 1", after1.Status.Resources.InternalSecretRef)
	}
	if after1.Status.Resources.PrivateTLSSecretRef != "dbaas-system/dbi-orders-uid-tls" {
		t.Fatalf("PrivateTLSSecretRef = %q after pass 1", after1.Status.Resources.PrivateTLSSecretRef)
	}
	if after1.Status.IsConditionTrue(dbaasv1.ConditionReady) {
		t.Fatal("Ready must not be True after pass 1")
	}
	if cond := after1.Status.GetCondition(dbaasv1.ConditionVMReady); cond == nil || cond.Reason != "VMCreated" {
		t.Fatalf("VMReady condition = %+v, want reason VMCreated", cond)
	}

	// KubeVirt "creates" the VM out of band.
	if err := r.Create(ctx, testVM("pg-orders", "tenant-a")); err != nil {
		t.Fatalf("create vm: %v", err)
	}

	// Pass 2: VM observed, VMI ready → health passes, then bootstrap-cleanup
	// redacts the consumed cloud-init secret's userdata (a cheap idempotent
	// apply — see ensureMonitoring's §4.1 exception, which this step now
	// follows too), so the walk completes to Available in the same pass.
	if err := r.Get(ctx, key, inst); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if result, err = r.runProvisioning(ctx, inst); err != nil || result != (ctrl.Result{}) {
		t.Fatalf("pass 2 = (%+v, %v), want zero Result and nil error", result, err)
	}

	got := &dbaasv1.DBInstance{}
	if err := r.Get(ctx, key, got); err != nil {
		t.Fatalf("get after pass 2: %v", err)
	}
	// The cloud-init Secret is redacted, not deleted — the ref stays recorded.
	if got.Status.Resources.CloudInitSecretName != "pg-orders-cloudinit" {
		t.Fatalf("cloud-init ref = %q after pass 2, want kept (pg-orders-cloudinit)", got.Status.Resources.CloudInitSecretName)
	}
	var ciSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-cloudinit"}, &ciSecret); err != nil {
		t.Fatalf("cloud-init secret missing: %v", err)
	}
	if ciSecret.StringData["userdata"] != redactedCloudInitUserData {
		t.Fatalf("cloud-init userdata = %q, want redacted", ciSecret.StringData["userdata"])
	}
	if got.Status.Phase != dbaasv1.StatusAvailable {
		t.Fatalf("phase = %q, want available", got.Status.Phase)
	}
	if got.Status.ObservedGeneration != 3 {
		t.Fatalf("ObservedGeneration = %d, want 3", got.Status.ObservedGeneration)
	}
	for _, c := range []string{dbaasv1.ConditionPreflightReady, dbaasv1.ConditionVMReady, dbaasv1.ConditionDatabaseReady, dbaasv1.ConditionMonitoringReady, dbaasv1.ConditionReady} {
		if !got.Status.IsConditionTrue(c) {
			t.Errorf("condition %s not True: %+v", c, got.Status.GetCondition(c))
		}
	}
	if got.Status.Endpoint == nil || got.Status.Endpoint.Address != "192.168.40.50" {
		t.Fatalf("Endpoint = %+v, want address 192.168.40.50", got.Status.Endpoint)
	}

	// The builder-managed monitoring trio exists, controller-owned (PR7).
	for _, check := range []struct {
		name string
		obj  client.Object
	}{
		{dbresource.MetricsServiceName(got), &corev1.Service{}},
		{dbresource.MetricsServiceName(got), &corev1.Endpoints{}},
		{dbresource.ServiceMonitorName(got), &monitoringv1.ServiceMonitor{}},
	} {
		if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: check.name}, check.obj); err != nil {
			t.Fatalf("monitoring child %s (%T) missing: %v", check.name, check.obj, err)
		}
		refs := check.obj.GetOwnerReferences()
		if len(refs) != 1 || refs[0].Kind != "DBInstance" || refs[0].Controller == nil || !*refs[0].Controller {
			t.Fatalf("monitoring child %s owner refs = %+v, want controller-owned by the DBInstance", check.name, refs)
		}
	}

	// Pass 3: ensureCredentials runs before ensureDatabaseHealth in step
	// order, so on pass 2 it couldn't yet see the Endpoint health had just
	// set that same pass — it needs one more reconcile to notice status.
	// Endpoint is now populated and publish the connection Secret. In a real
	// cluster this pass is triggered automatically (pass 2's status write
	// bumps resourceVersion, firing the DBInstance watch); here it's driven
	// manually like the others. This is unrelated to bootstrap-cleanup no
	// longer stopping the pass — it was already true before, just masked by
	// bootstrap-cleanup's old one-shot Pending coincidentally forcing the
	// same extra pass.
	if err := r.Get(ctx, key, inst); err != nil {
		t.Fatalf("refetch for pass 3: %v", err)
	}
	if result, err = r.runProvisioning(ctx, inst); err != nil || result != (ctrl.Result{}) {
		t.Fatalf("pass 3 = (%+v, %v), want zero Result and nil error", result, err)
	}
	if err := r.Get(ctx, key, got); err != nil {
		t.Fatalf("get after pass 3: %v", err)
	}

	// PR8: the full credential/TLS secret inventory exists — slim tenant
	// credentials + connection Secret in the tenant namespace, internal +
	// TLS Secrets in the operator namespace — and status.caCertPem is gone
	// (the field no longer exists on DBInstanceStatus at all).
	if got.Status.Resources.ConnectionSecretName != "pg-orders-connect" {
		t.Fatalf("ConnectionSecretName = %q, want pg-orders-connect", got.Status.Resources.ConnectionSecretName)
	}
	if got.Status.MasterUserSecret == nil || got.Status.MasterUserSecret.Name != "pg-orders-credentials" {
		t.Fatalf("MasterUserSecret = %+v", got.Status.MasterUserSecret)
	}

	var tenantCred corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-credentials"}, &tenantCred); err != nil {
		t.Fatalf("tenant credentials secret missing: %v", err)
	}
	if tenantCred.StringData["admin_password"] == "" {
		t.Fatal("tenant credentials secret has no admin_password")
	}
	if _, hasLegacy := tenantCred.StringData["ca_cert"]; hasLegacy {
		t.Fatal("tenant credentials secret must not carry TLS keys")
	}

	var conn corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-connect"}, &conn); err != nil {
		t.Fatalf("connection secret missing: %v", err)
	}
	if conn.StringData["host"] != "192.168.40.50" || conn.StringData["ca.crt"] == "" {
		t.Fatalf("connection secret StringData = %+v", conn.StringData)
	}

	for _, name := range []string{"dbi-orders-uid-internal", "dbi-orders-uid-tls"} {
		var sec corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Namespace: "dbaas-system", Name: name}, &sec); err != nil {
			t.Fatalf("operator-namespace secret %s missing: %v", name, err)
		}
		if sec.Labels[dbaasv1.LabelDBInstanceUID] != "orders-uid" {
			t.Fatalf("%s labels = %+v, want dbinstance-uid=orders-uid", name, sec.Labels)
		}
	}
}
