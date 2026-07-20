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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// newHealthInst returns an instance whose VM was already created (the state the
// old phaseWaitReady tests started from).
func newHealthInst() *dbaasv1.DBInstance {
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders"
	return inst
}

// Ported from TestPhaseWaitReadyRequeuesWhenVMINotRunning.
func TestEnsureDatabaseHealthPendingWhileVMINotRunning(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{Running: false}}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newHealthInst()

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending", res)
	}
	if res.Result.RequeueAfter != healthRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.Result.RequeueAfter, healthRequeue)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionDatabaseReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonVMBooting) {
		t.Fatalf("DatabaseReady = %+v, want False/VMBooting", cond)
	}
	if !strings.Contains(cond.Message, "VM booting") {
		t.Fatalf("condition message = %q, want gate-1 message", cond.Message)
	}
}

// A VMI that does not exist yet (first boot) is the same gate, not an error.
func TestEnsureDatabaseHealthPendingWhileVMIAbsent(t *testing.T) {
	notFound := apierrors.NewNotFound(
		schema.GroupResource{Group: "kubevirt.io", Resource: "virtualmachineinstances"}, "pg-orders")
	stub := &stubHarvester{readinessErr: notFound}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newHealthInst()

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending on VMI NotFound", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionDatabaseReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonVMBooting) {
		t.Fatalf("DatabaseReady = %+v, want False/VMBooting", cond)
	}
}

// Ported from TestPhaseWaitReadyRequeuesWhenReadinessProbeNotYetPassed.
func TestEnsureDatabaseHealthPendingWhileProbeNotPassing(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true,
		IP:      "192.168.40.50",
		Ready:   false, // probe has not passed yet
	}}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newHealthInst()

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending", res)
	}
	if res.Result.RequeueAfter != healthRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.Result.RequeueAfter, healthRequeue)
	}
	if cond := inst.Status.GetCondition(dbaasv1.ConditionDatabaseReady); cond == nil ||
		cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonPostgresInitializing) ||
		!strings.Contains(cond.Message, "PostgreSQL initializing") {
		t.Fatalf("DatabaseReady = %+v, want False/PostgresInitializing with gate-2 message", cond)
	}
}

// Ported from TestPhaseWaitReadyAdvancesWhenBothGatesPass.
func TestEnsureDatabaseHealthSatisfiedWhenReady(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true,
		IP:      "192.168.40.50",
		Ready:   true,
	}}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newHealthInst()

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	ep := inst.Status.Endpoint
	if ep == nil || ep.Address != "192.168.40.50" || ep.Port != defaultPort {
		t.Fatalf("Endpoint = %+v, want 192.168.40.50:%d", ep, defaultPort)
	}
	if !strings.Contains(ep.JDBCURL, "sslmode=verify-ca") {
		t.Fatalf("JDBCURL = %q, want SSL-enforcing URL", ep.JDBCURL)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady should be True")
	}
}

func TestCrashLoopRecoveryWaitsForDataNetIP(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-recovered",
	}}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newHealthInst()
	inst.Status.LastKnownVMIUID = "vmi-halted"
	setStepCond(inst, dbaasv1.ConditionCrashLoopHalted, metav1.ConditionTrue,
		dbaasv1.ReasonCrashLoopDetected, "seeded")

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Result.RequeueAfter != healthRequeue {
		t.Fatalf("res = %+v, want short Pending", res)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		t.Fatal("CrashLoopHalted must remain set until the data-net IP is observed")
	}
}

func TestCrashLoopDoesNotRecoverFromHaltedVMIStillTearingDown(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true, Ready: true, AgentConnected: true, IP: "10.0.0.4", VMIUID: "vmi-halted",
	}}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newHealthInst()
	inst.Status.LastKnownVMIUID = "vmi-halted"
	setStepCond(inst, dbaasv1.ConditionCrashLoopHalted, metav1.ConditionTrue,
		dbaasv1.ReasonCrashLoopDetected, "seeded")

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Result.RequeueAfter != crashLoopParkRequeue {
		t.Fatalf("res = %+v, want parked Pending", res)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		t.Fatal("CrashLoopHalted was cleared by the VMI being halted")
	}
	if stub.ClearCrashLoopHaltCalls != 0 {
		t.Fatalf("ClearCrashLoopHalt calls = %d, want 0", stub.ClearCrashLoopHaltCalls)
	}
}

func TestCrashLoopRecoveryKeepsConditionWhenMarkerClearFails(t *testing.T) {
	clearErr := errors.New("VM update failed")
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{
			Running: true, Ready: true, AgentConnected: true, IP: "10.0.0.5", VMIUID: "vmi-recovered",
		},
		clearCrashLoopHaltErr: clearErr,
	}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newHealthInst()
	inst.Status.LastKnownVMIUID = "vmi-halted"
	setStepCond(inst, dbaasv1.ConditionCrashLoopHalted, metav1.ConditionTrue,
		dbaasv1.ReasonCrashLoopDetected, "seeded")

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomeTransient || !errors.Is(res.Err, clearErr) {
		t.Fatalf("res = %+v, want Transient carrying clear error", res)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		t.Fatal("CrashLoopHalted must remain True when marker clearing fails")
	}
}

// A non-NotFound readiness error is infrastructure failure, not a boot gate.
func TestEnsureDatabaseHealthGenericErrorIsTransient(t *testing.T) {
	boom := errors.New("apiserver timeout")
	stub := &stubHarvester{readinessErr: boom}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newHealthInst()

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomeTransient || !errors.Is(res.Err, boom) {
		t.Fatalf("res = %+v, want Transient carrying the error", res)
	}
}
