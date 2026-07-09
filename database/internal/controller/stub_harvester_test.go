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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// stubHarvester satisfies harvester.ClientInterface for controller unit tests.
// Set the error fields to inject failures into specific methods; the *Calls
// counters record how many times each mutating method was invoked. Name-returning
// methods derive the same deterministic names as the typed client ("pg-<id>",
// "pg-<id>-credentials", ...) so step tests can assert recorded refs.
type stubHarvester struct {
	readiness          harvester.VMIReadiness
	readinessErr       error
	stopVMErr          error
	startVMErr         error
	createVMErr        error
	removeCloudInitErr error

	StopVMCalls   int
	StartVMCalls  int
	CreateVMCalls int
	ResizeVMCalls int
	ResizeDVCalls int

	// LastVMCreateParams captures the most recent CreatePostgresVM input so
	// tests can assert what the controller asked for (e.g. the owner ref).
	LastVMCreateParams *harvester.VMCreateParams

	// OpsLog records the order of cleanup-relevant calls so tests can assert
	// sequencing (e.g. cloud-init disk removal MUST precede secret deletion).
	OpsLog []string
}

func (s *stubHarvester) GetVMIReadiness(_ context.Context, _, _ string) (harvester.VMIReadiness, error) {
	return s.readiness, s.readinessErr
}
func (s *stubHarvester) ResizeDataVolume(_ context.Context, _, _, _ string, _ int) error {
	s.ResizeDVCalls++
	return nil
}
func (s *stubHarvester) CreatePostgresVM(_ context.Context, p harvester.VMCreateParams) (string, error) {
	s.CreateVMCalls++
	s.LastVMCreateParams = &p
	// The name is deterministic and returned even on error, matching the real
	// client contract ("record the ref even on partial failure").
	return "pg-" + p.ID, s.createVMErr
}
func (s *stubHarvester) StopVM(_ context.Context, _, _ string) error {
	s.StopVMCalls++
	return s.stopVMErr
}
func (s *stubHarvester) StartVM(_ context.Context, _, _ string) error {
	s.StartVMCalls++
	return s.startVMErr
}
func (s *stubHarvester) ResizeVM(_ context.Context, _, _ string, _, _ int) error {
	s.ResizeVMCalls++
	return nil
}
func (s *stubHarvester) RemoveCloudInitDisk(_ context.Context, _, _ string) error {
	s.OpsLog = append(s.OpsLog, "RemoveCloudInitDisk")
	return s.removeCloudInitErr
}
func (s *stubHarvester) TeardownAll(_ context.Context, _, _ string, _ dbaasv1.ResourceRefs) error {
	return nil
}

// wrapClientForSecretDeleteTracking makes r.Client record "DeleteSecret" into
// stub.OpsLog when the named Secret is deleted. DeleteSecret moved off
// harvester.ClientInterface (PR9) onto the controller's own client — it's a
// plain corev1.Secret, not a Harvester resource — but the FailedMount-guard
// tests still need to observe it ordered against RemoveCloudInitDisk (a
// Harvester call, tracked separately in the same OpsLog).
func wrapClientForSecretDeleteTracking(t *testing.T, r *DBInstanceReconciler, stub *stubHarvester, secretName string) {
	t.Helper()
	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture's fake client does not implement client.WithWatch")
	}
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if sec, ok := obj.(*corev1.Secret); ok && sec.Name == secretName {
				stub.OpsLog = append(stub.OpsLog, "DeleteSecret")
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
}
