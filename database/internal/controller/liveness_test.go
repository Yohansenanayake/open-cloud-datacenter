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

// Steady-state liveness (PR6): report-only Degraded on a caught-up instance,
// driven by ensureDatabaseHealth (which absorbed legacy phaseAvailable's
// liveness). All cases must return Satisfied — a blip never gates the pass or
// restarts the VM.

import (
	"context"
	"errors"
	"testing"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

// degradedReason returns the reason of the Degraded condition, or "" if absent.
func degradedReason(inst *dbaasv1.DBInstance) string {
	for _, c := range inst.Status.Conditions {
		if c.Type == dbaasv1.ConditionDegraded && c.Status == metav1.ConditionTrue {
			return c.Reason
		}
	}
	return ""
}

// newCaughtUpFixture returns a reconciler + instance in converged steady state
// (observedGeneration == generation, known VMI UID) — the state where health
// switches from gating to report-only liveness.
func newCaughtUpFixture(stub *stubHarvester) (*DBInstanceReconciler, *dbaasv1.DBInstance) {
	inst := newProvisionInst()
	inst.Status.ObservedGeneration = inst.Generation
	inst.Status.Phase = dbaasv1.StatusAvailable
	inst.Status.LastKnownVMIUID = "vmi-uid-abc"
	inst.Status.Resources.VMName = "pg-orders"
	r := &DBInstanceReconciler{
		Harvester: stub,
		Recorder:  record.NewFakeRecorder(10),
	}
	return r, inst
}

// Probe failing with the agent up: we KNOW PostgreSQL is down. Degraded is
// attributed to PostgresUnreachable, phase turns "degraded", the VM is not
// touched, and the step still returns Satisfied (report-only).
func TestHealthCaughtUpProbeFailureSetsDegraded(t *testing.T) {
	stub := &stubHarvester{
		Readiness: harvester.VMIReadiness{Running: true, Ready: false, AgentConnected: true, IP: "10.0.0.5", VMIUID: "vmi-uid-abc"},
	}
	r, inst := newCaughtUpFixture(stub)

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied (report-only)", res)
	}
	if got := degradedReason(inst); got != string(dbaasv1.ReasonPostgresUnreachable) {
		t.Fatalf("Degraded reason = %q, want %q", got, dbaasv1.ReasonPostgresUnreachable)
	}
	r.finalizeStatus(inst)
	if inst.Status.Phase != dbaasv1.StatusDegraded {
		t.Fatalf("Phase = %q, want %q (user-facing honesty)", inst.Status.Phase, dbaasv1.StatusDegraded)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must be False while degraded")
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM must not be restarted on Readiness failure (stop=%d start=%d)", stub.StopVMCalls, stub.StartVMCalls)
	}
}

// Agent disconnect: health is UNKNOWN, not a PostgreSQL fault — attribution matters.
func TestHealthCaughtUpAgentDisconnectAttributed(t *testing.T) {
	stub := &stubHarvester{
		Readiness: harvester.VMIReadiness{Running: true, Ready: false, AgentConnected: false, VMIUID: "vmi-uid-abc"},
	}
	r, inst := newCaughtUpFixture(stub)

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if got := degradedReason(inst); got != string(dbaasv1.ReasonGuestAgentDisconnected) {
		t.Fatalf("Degraded reason = %q, want %q", got, dbaasv1.ReasonGuestAgentDisconnected)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM must not be restarted on agent disconnect (stop=%d start=%d)", stub.StopVMCalls, stub.StartVMCalls)
	}
}

// A VMI that is gone entirely (out-of-band halt / mid-restart) on a caught-up
// instance is degraded with VMRestarting attribution — still report-only.
func TestHealthCaughtUpVMIGoneAttributedRestarting(t *testing.T) {
	stub := &stubHarvester{} // zero Readiness: not running
	r, inst := newCaughtUpFixture(stub)

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if got := degradedReason(inst); got != string(dbaasv1.ReasonVMRestarting) {
		t.Fatalf("Degraded reason = %q, want %q", got, dbaasv1.ReasonVMRestarting)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM must not be restarted when the VMI is gone (stop=%d start=%d)", stub.StopVMCalls, stub.StartVMCalls)
	}
}

// Recovery: probe Ready again clears Degraded and republishes the endpoint.
func TestHealthCaughtUpHealthyClearsDegraded(t *testing.T) {
	stub := &stubHarvester{
		Readiness: harvester.VMIReadiness{Running: true, Ready: false, AgentConnected: true, VMIUID: "vmi-uid-abc"},
	}
	r, inst := newCaughtUpFixture(stub)
	ctx := context.Background()

	if res := r.ensureDatabaseHealth(ctx, inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("degraded cycle: %+v", res)
	}
	if degradedReason(inst) == "" {
		t.Fatal("Degraded condition not set after Readiness failure")
	}

	stub.Readiness = harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, IP: "10.0.0.5", VMIUID: "vmi-uid-abc"}
	res := r.ensureDatabaseHealth(ctx, inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("healthy cycle: %+v", res)
	}
	if got := degradedReason(inst); got != "" {
		t.Fatalf("Degraded still set after recovery (reason=%q)", got)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady should be True after recovery")
	}
	if inst.Status.Endpoint == nil || inst.Status.Endpoint.Address != "10.0.0.5" {
		t.Fatalf("Endpoint = %+v, want refreshed to 10.0.0.5", inst.Status.Endpoint)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM calls during report-only liveness (stop=%d start=%d)", stub.StopVMCalls, stub.StartVMCalls)
	}
}

// RF-3 regression, PR6 shape: a failed VMI fetch is not a health signal. The
// step returns Transient (taxonomy §8.2 — backoff) but must not flip Degraded
// from the zero-value Readiness and must not touch the VM.
func TestHealthReadinessFetchErrorIsTransientAndLeavesConditionsUntouched(t *testing.T) {
	stub := &stubHarvester{ReadinessErr: errors.New("apiserver timeout")}
	r, inst := newCaughtUpFixture(stub)

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomeTransient {
		t.Fatalf("res = %+v, want Transient", res)
	}
	if got := degradedReason(inst); got != "" {
		t.Fatalf("Degraded set from a fetch error (reason=%q), want none", got)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM calls on fetch error (stop=%d start=%d), want none", stub.StopVMCalls, stub.StartVMCalls)
	}
}

// A single unplanned restart (below the crash-loop threshold) on a caught-up
// instance: counted, reported as degraded, absorbed — never a gate, never a fail.
func TestHealthCaughtUpRestartBelowThresholdAbsorbed(t *testing.T) {
	stub := &stubHarvester{
		Readiness: harvester.VMIReadiness{Running: false, VMIUID: "vmi-uid-new"}, // rebooting under a fresh UID
	}
	r, inst := newCaughtUpFixture(stub)

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied (absorbed restart)", res)
	}
	if inst.Status.RestartCount != 1 || inst.Status.RecentUnplannedRestarts != 1 {
		t.Fatalf("counters = restart:%d recent:%d, want 1/1", inst.Status.RestartCount, inst.Status.RecentUnplannedRestarts)
	}
	if inst.Status.LastKnownVMIUID != "vmi-uid-new" {
		t.Fatalf("LastKnownVMIUID = %q, want re-baselined to the new UID", inst.Status.LastKnownVMIUID)
	}
	r.finalizeStatus(inst)
	if inst.Status.Phase != dbaasv1.StatusDegraded {
		t.Fatalf("Phase = %q, want %q while rebooting", inst.Status.Phase, dbaasv1.StatusDegraded)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		t.Fatal("CrashLoopHalted must not be set below the threshold")
	}
	if stub.StopVMCalls != 0 {
		t.Fatalf("StopVM called %d times below threshold, want 0", stub.StopVMCalls)
	}
}
