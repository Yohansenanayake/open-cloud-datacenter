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
	"strings"
	"testing"
	"time"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// stubHarvester satisfies harvester.ClientInterface for controller unit tests.
// Set the exported error fields to inject failures into specific methods.
// StopVMCalls and StartVMCalls count how many times each method was called.
type stubHarvester struct {
	readiness    harvester.VMIReadiness
	readinessErr error
	stopVMErr    error
	startVMErr   error
	StopVMCalls  int
	StartVMCalls int
}

func (s *stubHarvester) GetVMIReadiness(_ context.Context, _, _ string) (harvester.VMIReadiness, error) {
	return s.readiness, s.readinessErr
}
func (s *stubHarvester) CreateDataVolume(_ context.Context, _, _ string, _ int, _ string) (string, error) {
	return "", nil
}
func (s *stubHarvester) ResizeDataVolume(_ context.Context, _, _, _ string, _ int) error { return nil }
func (s *stubHarvester) CreatePostgresVM(_ context.Context, _ harvester.VMCreateParams) (string, string, string, string, error) {
	return "", "", "", "", nil
}
func (s *stubHarvester) StopVM(_ context.Context, _, _ string) error {
	s.StopVMCalls++
	return s.stopVMErr
}
func (s *stubHarvester) StartVM(_ context.Context, _, _ string) error {
	s.StartVMCalls++
	return s.startVMErr
}
func (s *stubHarvester) ResizeVM(_ context.Context, _, _ string, _, _ int) error  { return nil }
func (s *stubHarvester) DeleteSecret(_ context.Context, _, _ string) error        { return nil }
func (s *stubHarvester) RemoveCloudInitDisk(_ context.Context, _, _ string) error { return nil }
func (s *stubHarvester) PrepareCloudInitForRepave(_ context.Context, _ harvester.VMCreateParams, _, _, _ string) error {
	return nil
}
func (s *stubHarvester) DeployMonitoring(_ context.Context, _, _, _ string) (string, string, string, string, error) {
	return "", "", "", "", nil
}
func (s *stubHarvester) TeardownAll(_ context.Context, _, _ string, _ dbaasv1.ResourceRefs) error {
	return nil
}
func (s *stubHarvester) ClearDataVolumeOwnerRef(_ context.Context, _, _ string) error { return nil }
func (s *stubHarvester) DeleteDataVolume(_ context.Context, _, _ string) error        { return nil }
func (s *stubHarvester) DeletePVC(_ context.Context, _, _ string) error               { return nil }
func (s *stubHarvester) SwapVMOSDisk(_ context.Context, _, _, _, _ string) (string, error) {
	return "", nil
}

// newWaitReadyReconciler returns a reconciler and a DBInstance wired up for
// phaseWaitReady unit tests. The inst is pre-registered in the fake client.
func newWaitReadyReconciler(stub *stubHarvester) (*DBInstanceReconciler, *dbaasv1.DBInstance) {
	inst := &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "tenant-a", ResourceVersion: "1"},
		Status: dbaasv1.DBInstanceStatus{
			// phaseVM leaves the instance in PhaseVMCreated; phaseWaitReady keeps
			// it there throughout the wait and distinguishes gates via Message.
			ProvisioningPhase: dbaasv1.PhaseVMCreated,
			Resources:         dbaasv1.ResourceRefs{VMName: "pg-orders"},
		},
	}
	scheme := runtime.NewScheme()
	_ = dbaasv1.AddToScheme(scheme)
	fakeClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(inst).
		WithObjects(inst).
		Build()
	r := &DBInstanceReconciler{Client: fakeClient, Harvester: stub}
	return r, inst
}

func TestPhaseWaitReadyRequeuesWhenVMINotRunning(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{Running: false}}
	r, inst := newWaitReadyReconciler(stub)

	result, err := r.phaseWaitReady(context.Background(), inst)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("RequeueAfter = %v, want 10s", result.RequeueAfter)
	}
	// Single wait phase: stays VMCreated; the gate is conveyed via Message.
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseVMCreated {
		t.Fatalf("ProvisioningPhase = %q, want %q", inst.Status.ProvisioningPhase, dbaasv1.PhaseVMCreated)
	}
	if !strings.Contains(inst.Status.Message, "VM booting") {
		t.Fatalf("Message = %q, want gate-1 (VM booting) message", inst.Status.Message)
	}
}

func TestPhaseWaitReadyRequeuesWhenReadinessProbeNotYetPassed(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true,
		IP:      "192.168.40.50",
		Ready:   false, // probe has not passed yet
	}}
	r, inst := newWaitReadyReconciler(stub)

	result, err := r.phaseWaitReady(context.Background(), inst)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("RequeueAfter = %v, want 10s", result.RequeueAfter)
	}
	// Single wait phase: stays VMCreated; the gate is conveyed via Message.
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseVMCreated {
		t.Fatalf("ProvisioningPhase = %q, want %q", inst.Status.ProvisioningPhase, dbaasv1.PhaseVMCreated)
	}
	if !strings.Contains(inst.Status.Message, "PostgreSQL initializing") {
		t.Fatalf("Message = %q, want gate-2 (PostgreSQL initializing) message", inst.Status.Message)
	}
}

func TestPhaseWaitReadyAdvancesWhenBothGatesPass(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true,
		IP:      "192.168.40.50",
		Ready:   true, // readiness probe has passed
	}}
	r, inst := newWaitReadyReconciler(stub)

	result, err := r.phaseWaitReady(context.Background(), inst)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// advance() returns Requeue: true (not a timed re-queue)
	if result == (ctrl.Result{RequeueAfter: 10 * time.Second}) {
		t.Fatalf("should not have returned a 10s re-queue; both gates passed")
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseDatabaseReady {
		t.Fatalf("ProvisioningPhase = %q, want %q", inst.Status.ProvisioningPhase, dbaasv1.PhaseDatabaseReady)
	}
	if inst.Status.Endpoint == nil || inst.Status.Endpoint.Address != "192.168.40.50" {
		t.Fatalf("Endpoint = %v, want address 192.168.40.50", inst.Status.Endpoint)
	}
}
