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

package testutil

import (
	"context"
	"fmt"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// StubHarvester satisfies harvester.ClientInterface for controller and ensure tests.
// Set the error fields to inject failures into specific methods; the *Calls
// counters record how many times each mutating method was invoked. Name-returning
// methods derive the same deterministic names as the typed client ("pg-<id>",
// "pg-<id>-credentials", ...) so step tests can assert recorded refs.
type StubHarvester struct {
	Readiness             harvester.VMIReadiness
	ReadinessErr          error
	StopVMErr             error
	StopVMForCrashLoopErr error
	ClearCrashLoopHaltErr error
	StartVMErr            error
	CreateVMErr           error
	ResolveVMImageErr     error
	TeardownErr           error
	SwapVMOSDiskErr       error
	DeletePVCErr          error

	StopVMCalls             int
	StopVMForCrashLoopCalls int
	ClearCrashLoopHaltCalls int
	StartVMCalls            int
	CreateVMCalls           int
	ResolveVMImageCalls     int
	ResizeVMCalls           int
	ResizeDVCalls           int
	TeardownCalls           int
	SwapVMOSDiskCalls       int
	DeletePVCCalls          int
	LastHaltedVMIUID        string
	LastVMImageRef          string
	LastResizeDVName        string
	// LastSwapVMOSDiskImageRef captures the most recent SwapVMOSDisk
	// newImageRef input so tests can assert what the controller asked to
	// swap to.
	LastSwapVMOSDiskImageRef string
	// LastDeletedPVCName captures the most recent DeletePVC input.
	LastDeletedPVCName string
	// SwapVMOSDiskNoop, when true, makes SwapVMOSDisk report the idempotent
	// no-op branch (empty oldPVCName) instead of a normal swap.
	SwapVMOSDiskNoop bool

	// LastVMCreateParams captures the most recent CreatePostgresVM input so
	// tests can assert what the controller asked for (e.g. the owner ref).
	LastVMCreateParams *harvester.VMCreateParams
}

func (s *StubHarvester) GetVMIReadiness(_ context.Context, _, _ string) (harvester.VMIReadiness, error) {
	return s.Readiness, s.ReadinessErr
}
func (s *StubHarvester) ResizeDataVolume(_ context.Context, _, _, dvName string, _ int) error {
	s.ResizeDVCalls++
	s.LastResizeDVName = dvName
	return nil
}
func (s *StubHarvester) ResolveVMImage(_ context.Context, ref string) (harvester.ResolvedVMImage, error) {
	s.ResolveVMImageCalls++
	s.LastVMImageRef = ref
	if s.ResolveVMImageErr != nil {
		return harvester.ResolvedVMImage{}, s.ResolveVMImageErr
	}
	return harvester.ResolvedVMImage{Namespace: "default", Name: ref, StorageClassName: "longhorn-image"}, nil
}
func (s *StubHarvester) CreatePostgresVM(_ context.Context, p harvester.VMCreateParams) (string, error) {
	s.CreateVMCalls++
	s.LastVMCreateParams = &p
	// The name is deterministic and returned even on error, matching the real
	// client contract ("record the ref even on partial failure").
	return "pg-" + p.ID, s.CreateVMErr
}
func (s *StubHarvester) StopVM(_ context.Context, _, _ string) error {
	s.StopVMCalls++
	return s.StopVMErr
}
func (s *StubHarvester) StopVMForCrashLoop(_ context.Context, _, _, haltedVMIUID string) error {
	s.StopVMForCrashLoopCalls++
	s.LastHaltedVMIUID = haltedVMIUID
	return s.StopVMForCrashLoopErr
}
func (s *StubHarvester) ClearCrashLoopHalt(_ context.Context, _, _ string) error {
	s.ClearCrashLoopHaltCalls++
	return s.ClearCrashLoopHaltErr
}
func (s *StubHarvester) StartVM(_ context.Context, _, _ string) error {
	s.StartVMCalls++
	return s.StartVMErr
}
func (s *StubHarvester) ResizeVM(_ context.Context, _, _ string, _, _ int) error {
	s.ResizeVMCalls++
	return nil
}
func (s *StubHarvester) TeardownAll(_ context.Context, _, _ string, _ dbaasv1.ResourceRefs) error {
	s.TeardownCalls++
	return s.TeardownErr
}
func (s *StubHarvester) SwapVMOSDisk(_ context.Context, _, _, instID, newImageRef string) (string, string, error) {
	s.SwapVMOSDiskCalls++
	s.LastSwapVMOSDiskImageRef = newImageRef
	if s.SwapVMOSDiskErr != nil {
		return "", "", s.SwapVMOSDiskErr
	}
	newPVCName := fmt.Sprintf("pg-%s-os-%s", instID, newImageRef)
	if s.SwapVMOSDiskNoop {
		return "", newPVCName, nil
	}
	return fmt.Sprintf("pg-%s-os", instID), newPVCName, nil
}
func (s *StubHarvester) DeletePVC(_ context.Context, _, name string) error {
	s.DeletePVCCalls++
	s.LastDeletedPVCName = name
	return s.DeletePVCErr
}
