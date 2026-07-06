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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func TestEnsureVMCreatesWhenAbsent(t *testing.T) {
	inst := newProvisionInst()
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst)

	res := r.ensureVM(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "VMCreated" {
		t.Fatalf("res = %+v, want Pending/VMCreated", res)
	}
	if res.Result.RequeueAfter != 0 {
		t.Fatalf("VM create must be event-driven Pending, got RequeueAfter %v", res.Result.RequeueAfter)
	}
	if stub.CreateVMCalls != 1 {
		t.Fatalf("CreateVMCalls = %d, want 1", stub.CreateVMCalls)
	}

	refs := inst.Status.Resources
	if refs.VMName != "pg-orders" || refs.SecretName != "pg-orders-credentials" ||
		refs.CloudInitSecretName != "pg-orders-cloudinit" || refs.DataVolumeName != "pg-orders-data" {
		t.Fatalf("Resources = %+v, want deterministic pg-orders names", refs)
	}
	if inst.Status.CACertPEM != "CA-PEM" {
		t.Fatalf("CACertPEM = %q", inst.Status.CACertPEM)
	}
	if inst.Status.MasterUserSecret == nil || inst.Status.MasterUserSecret.Name != "pg-orders-credentials" {
		t.Fatalf("MasterUserSecret = %+v", inst.Status.MasterUserSecret)
	}

	a := inst.Status.AppliedSpec
	if a == nil {
		t.Fatal("AppliedSpec not snapshotted")
	}
	if a.MasterUsername != defaultMasterUser || a.OSImage != defaultOSImage ||
		a.Port != defaultPort || a.StorageType != defaultStorageType ||
		a.DBName != "orders" || a.NetworkRef != "tenant-a/data-net" {
		t.Fatalf("AppliedSpec = %+v, want defaulted snapshot", a)
	}

	cond := inst.Status.GetCondition(dbaasv1.ConditionVMReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "VMCreated" {
		t.Fatalf("VMReady = %+v, want False/VMCreated", cond)
	}
	if cond.ObservedGeneration != inst.Generation {
		t.Fatalf("cond ObservedGeneration = %d, want %d", cond.ObservedGeneration, inst.Generation)
	}

	// PR7: the provider is asked to stamp a controller owner reference on the
	// children it creates (VM, credentials/cloud-init Secrets).
	if stub.LastVMCreateParams == nil || stub.LastVMCreateParams.Owner == nil {
		t.Fatal("VMCreateParams.Owner not set")
	}
	owner := stub.LastVMCreateParams.Owner
	if owner.Kind != "DBInstance" || owner.Name != "orders" || owner.Controller == nil || !*owner.Controller {
		t.Fatalf("Owner = %+v, want controller ref to the DBInstance", owner)
	}
}

func TestEnsureVMSatisfiedWhenPresent(t *testing.T) {
	inst := newProvisionInst()
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst, testVM("pg-orders", "tenant-a"))

	res := r.ensureVM(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if stub.CreateVMCalls != 0 {
		t.Fatalf("CreateVMCalls = %d, want 0", stub.CreateVMCalls)
	}
	if inst.Status.Resources.VMName != "pg-orders" {
		t.Fatalf("VMName not re-recorded from observation: %q", inst.Status.Resources.VMName)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionVMReady) {
		t.Fatal("VMReady should be True when VM exists")
	}
}

// Out-of-band `kubectl delete vm`: the ref says the VM was created, but
// observation says it is gone — the step must repair, not trust status.
func TestEnsureVMSelfHealsAfterOutOfBandDelete(t *testing.T) {
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders" // status remembers a VM that no longer exists
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst) // no VM object in the cluster

	res := r.ensureVM(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "VMCreated" {
		t.Fatalf("res = %+v, want Pending/VMCreated (re-create)", res)
	}
	if stub.CreateVMCalls != 1 {
		t.Fatalf("CreateVMCalls = %d, want 1 (self-heal re-create)", stub.CreateVMCalls)
	}
}

func TestEnsureVMCreateErrorIsTransientAndRecordsRefs(t *testing.T) {
	inst := newProvisionInst()
	stub := &stubHarvester{createVMErr: errors.New("harvester unavailable")}
	r := newProvisionReconciler(t, stub, inst)

	res := r.ensureVM(context.Background(), inst)

	if res.Outcome != OutcomeTransient || res.Err == nil {
		t.Fatalf("res = %+v, want Transient with error", res)
	}
	// Refs must be recorded even on failure so TeardownAll can clean up partials.
	refs := inst.Status.Resources
	if refs.VMName == "" || refs.SecretName == "" || refs.CloudInitSecretName == "" {
		t.Fatalf("Resources = %+v, want refs recorded despite create error", refs)
	}
}
