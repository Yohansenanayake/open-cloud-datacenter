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

// Stop/start lifecycle through the bounded runner (PR5). The legacy
// reconcileStop/reconcileStart/phaseStopped pre-dispatcher paths are gone: the
// power step converges spec.running from observed runStrategy + VMI state across
// multiple reconciles. The stubHarvester does not mutate the fake cluster, so
// tests simulate each provider call's side effect (runStrategy flip, VMI
// teardown) between passes — exactly what Harvester/KubeVirt would do.

import (
	"context"
	"testing"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func boolPtr(b bool) *bool { return &b }

// newLifecycleFixture builds an Available instance whose spec.running was just
// flipped (generation 2 vs observedGeneration 1 — the edit is unobserved), with a
// shaped running VM in the fake cluster, wired through the full Reconcile path.
func newLifecycleFixture(t *testing.T, running bool, stub *stubHarvester) (*DBInstanceReconciler, ctrl.Request) {
	t.Helper()
	ctx := context.Background()
	inst := &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "orders",
			Namespace:  "tenant-a",
			Generation: 2,
			Finalizers: []string{dbaasv1.FinalizerName},
		},
		Spec: dbaasv1.DBInstanceSpec{
			Running:          boolPtr(running),
			DBInstanceClass:  "db.t3.small",
			AllocatedStorage: 20,
			NetworkRef:       "tenant-a/data-net",
		},
		Status: dbaasv1.DBInstanceStatus{
			Phase:              dbaasv1.StatusAvailable,
			ObservedGeneration: 1,
			LastKnownVMIUID:    "vmi-uid-abc",
			Endpoint:           &dbaasv1.Endpoint{Address: "192.168.40.50", Port: defaultPort},
			Resources:          dbaasv1.ResourceRefs{VMName: "pg-orders", DataVolumeName: "pg-orders-data"},
		},
	}
	vm := testVM("pg-orders", "tenant-a") // shaped, runStrategy Always
	r := newProvisionReconciler(t, stub, inst, vm)
	convergeCredentials(t, ctx, r, inst)
	convergeConnectionSecret(t, ctx, r, inst)
	desiredRunning := inst.Spec.Running
	inst.Spec.Running = boolPtr(true) // resources existed before a stop request
	convergeMonitoring(t, ctx, r, inst)
	inst.Spec.Running = desiredRunning
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatalf("persist converged fixture status: %v", err)
	}
	r.Recorder = record.NewFakeRecorder(10)
	return r, ctrl.Request{NamespacedName: types.NamespacedName{Name: "orders", Namespace: "tenant-a"}}
}

func getInst(t *testing.T, c client.Client) *dbaasv1.DBInstance {
	t.Helper()
	var inst dbaasv1.DBInstance
	if err := c.Get(context.Background(), types.NamespacedName{Name: "orders", Namespace: "tenant-a"}, &inst); err != nil {
		t.Fatalf("get instance: %v", err)
	}
	return &inst
}

// stopConverged drives a running=false fixture through the stop sequence:
// pass 1 requests the stop; the test simulates the provider effects; pass 2
// observes full teardown and converges.
func stopConverged(t *testing.T, r *DBInstanceReconciler, req ctrl.Request, stub *stubHarvester) {
	t.Helper()
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("stop reconcile error: %v", err)
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("StopVM called %d times, want 1", stub.StopVMCalls)
	}
	// Visible intermediate progress instead of a premature "stopped".
	if inst := getInst(t, r.Client); inst.Status.Phase != dbaasv1.StatusStopping {
		t.Fatalf("Phase after stop request = %q, want %q", inst.Status.Phase, dbaasv1.StatusStopping)
	}

	// Simulate StopVM's effect and the VMI finishing teardown.
	setVMRunStrategy(t, r.Client, "pg-orders", "tenant-a", kubevirtv1.RunStrategyHalted)
	stub.readiness = harvester.VMIReadiness{}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("converge reconcile error: %v", err)
	}
}

// Stopping converges to phase=stopped across observed passes.
func TestStopConvergesToStopped(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"}}
	r, req := newLifecycleFixture(t, false, stub)

	stopConverged(t, r, req, stub)

	inst := getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusStopped {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStopped)
	}
	if inst.Status.ObservedGeneration != 2 {
		t.Fatalf("ObservedGeneration = %d, want 2 (stop observed)", inst.Status.ObservedGeneration)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionReady) {
		t.Fatal("Ready must not be True on a stopped instance")
	}
}

// Port of TestStoppedInstanceIsNotResurrected (RF-1): idle reconciles of a
// stopped instance must not restart the VM, re-stop it, or flip the phase.
func TestStoppedInstanceIsNotResurrected(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"}}
	r, req := newLifecycleFixture(t, false, stub)
	ctx := context.Background()

	stopConverged(t, r, req, stub)

	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("idle reconcile %d error: %v", i, err)
		}
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("StopVM called %d times, want 1 (no resurrection loop)", stub.StopVMCalls)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVM called %d times, want 0 (no liveness restart while stopped)", stub.StartVMCalls)
	}
	if inst := getInst(t, r.Client); inst.Status.Phase != dbaasv1.StatusStopped {
		t.Fatalf("Phase = %q after idle reconciles, want %q", inst.Status.Phase, dbaasv1.StatusStopped)
	}
}

// Port of TestStartReentersProvisioningChain: running=true starts the VM, resets
// the UID baseline (planned start), and converges to available only after the
// health gate passes — not immediately.
func TestStartReentersChainAndConverges(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"}}
	r, req := newLifecycleFixture(t, false, stub)
	ctx := context.Background()

	stopConverged(t, r, req, stub)

	// User flips running back to true.
	inst := getInst(t, r.Client)
	inst.Spec.Running = boolPtr(true)
	// The fake client does not implement API-server generation increments.
	// Model the real spec-update behavior so health remains a convergence gate.
	inst.Generation++
	if err := r.Update(ctx, inst); err != nil {
		t.Fatalf("spec update: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("start reconcile error: %v", err)
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("StartVM called %d times, want 1", stub.StartVMCalls)
	}
	inst = getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusStarting {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStarting)
	}
	if inst.Status.LastKnownVMIUID != "" {
		t.Fatalf("LastKnownVMIUID = %q, want cleared (planned start must not count as unplanned restart)", inst.Status.LastKnownVMIUID)
	}

	// Simulate the start subresource flipping runStrategy and the VM coming up,
	// but PostgreSQL not being ready yet. An established instance must stay in
	// Starting rather than falling back to Creating after power converges.
	setVMRunStrategy(t, r.Client, "pg-orders", "tenant-a", kubevirtv1.RunStrategyAlways)
	stub.readiness = harvester.VMIReadiness{Running: true, IP: "192.168.40.50", AgentConnected: true, VMIUID: "vmi-uid-new"}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("database recovery reconcile error: %v", err)
	}
	inst = getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusStarting {
		t.Fatalf("phase = %q, want starting while PostgreSQL initializes", inst.Status.Phase)
	}

	stub.readiness.Ready = true

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("converge reconcile error: %v", err)
	}
	inst = getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusAvailable {
		t.Fatalf("phase = %q, want available after health gate", inst.Status.Phase)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionReady) {
		t.Fatal("Ready should be True after start converges")
	}
}

// Port of TestStartWaitsForVMITeardown: StartVM must not be called while the
// previous VMI still exists (the start subresource would reject it).
func TestStartWaitsForVMITeardown(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"}}
	r, req := newLifecycleFixture(t, false, stub)
	ctx := context.Background()

	// Stop requested; runStrategy flipped, but the VMI is STILL tearing down.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("stop reconcile error: %v", err)
	}
	setVMRunStrategy(t, r.Client, "pg-orders", "tenant-a", kubevirtv1.RunStrategyHalted)

	// User immediately flips running back to true.
	inst := getInst(t, r.Client)
	inst.Spec.Running = boolPtr(true)
	if err := r.Update(ctx, inst); err != nil {
		t.Fatalf("spec update: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("start reconcile error: %v", err)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVM called %d times while VMI still running, want 0", stub.StartVMCalls)
	}

	// VMI finishes terminating — next reconcile starts the VM.
	stub.readiness = harvester.VMIReadiness{}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second start reconcile error: %v", err)
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("StartVM called %d times after teardown, want 1", stub.StartVMCalls)
	}
}

// An instance stuck at phase=stopping with running=true routes through the
// runner and starts, instead of dead-ending.
func TestStuckStoppingShapeRecovers(t *testing.T) {
	stub := &stubHarvester{} // VMI gone
	r, req := newLifecycleFixture(t, true, stub)
	ctx := context.Background()

	// Force the stuck shape.
	inst := getInst(t, r.Client)
	inst.Status.Phase = dbaasv1.StatusStopping
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatalf("status seed: %v", err)
	}
	setVMRunStrategy(t, r.Client, "pg-orders", "tenant-a", kubevirtv1.RunStrategyHalted)

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("StartVM called %d times, want 1 (stuck instance must route to start)", stub.StartVMCalls)
	}
	if inst := getInst(t, r.Client); inst.Status.Phase != dbaasv1.StatusStarting {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStarting)
	}
}
