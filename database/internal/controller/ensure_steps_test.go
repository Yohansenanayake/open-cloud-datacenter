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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// newProvisionInst returns a DBInstance mid-provisioning: valid spec, finalizer
// already present (Reconcile adds it pre-dispatch in production).
func newProvisionInst() *dbaasv1.DBInstance {
	return &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "orders",
			Namespace:       "tenant-a",
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

// newProvisionReconciler wires a reconciler with the dbaas + KubeVirt schemes so
// ensureVM can observe VirtualMachine objects through the fake client.
func newProvisionReconciler(t *testing.T, stub *stubHarvester, objs ...client.Object) *DBInstanceReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := dbaasv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add dbaas scheme: %v", err)
	}
	if err := kubevirtv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add kubevirt scheme: %v", err)
	}
	fakeClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&dbaasv1.DBInstance{}).
		WithObjects(objs...).
		Build()
	return &DBInstanceReconciler{Client: fakeClient, Harvester: stub}
}

func testVM(name, ns string) *kubevirtv1.VirtualMachine {
	return &kubevirtv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
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
	if !got.Status.IsConditionTrue(dbaasv1.ConditionFailed) {
		t.Fatalf("Failed condition not persisted: %+v", got.Status.Conditions)
	}
	if got.Status.Phase != dbaasv1.StatusFailed {
		t.Fatalf("Phase = %q, want %q", got.Status.Phase, dbaasv1.StatusFailed)
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

	// Pass 2: VM observed, VMI ready → walk to Available.
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
	if got.Status.Phase != dbaasv1.StatusAvailable || got.Status.ProvisioningPhase != dbaasv1.PhaseAvailable {
		t.Fatalf("phase = %q/%q, want available/Available", got.Status.Phase, got.Status.ProvisioningPhase)
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
}
