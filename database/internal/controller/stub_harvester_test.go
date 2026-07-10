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

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// stubHarvester satisfies harvester.ClientInterface for controller unit tests.
// Set the error fields to inject failures into specific methods; the *Calls
// counters record how many times each mutating method was invoked. Name-returning
// methods derive the same deterministic names as the typed client ("pg-<id>",
// "pg-<id>-credentials", ...) so step tests can assert recorded refs.
type stubHarvester struct {
	readiness    harvester.VMIReadiness
	readinessErr error
	stopVMErr    error
	startVMErr   error
	createVMErr  error

	StopVMCalls   int
	StartVMCalls  int
	CreateVMCalls int
	ResizeVMCalls int
	ResizeDVCalls int

	// LastVMCreateParams captures the most recent CreatePostgresVM input so
	// tests can assert what the controller asked for (e.g. the owner ref).
	LastVMCreateParams *harvester.VMCreateParams
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
func (s *stubHarvester) TeardownAll(_ context.Context, _, _ string, _ dbaasv1.ResourceRefs) error {
	return nil
}
