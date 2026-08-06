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

package ensure

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/catalog"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

func TestEnsurePreflightUnknownClassIsTerminal(t *testing.T) {
	r := &testHarness{}
	inst := newProvisionInst()
	inst.Spec.DBInstanceClass = "db.bogus"

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTerminal {
		t.Fatalf("res = %+v, want Terminal", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "InvalidClass" {
		t.Fatalf("PreflightReady = %+v, want False/InvalidClass", cond)
	}
}

func TestEnsurePreflightMissingNetworkRefIsTerminal(t *testing.T) {
	r := &testHarness{}
	inst := newProvisionInst()
	inst.Spec.NetworkRef = ""

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTerminal {
		t.Fatalf("res = %+v, want Terminal", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonNetworkRefMissing) {
		t.Fatalf("PreflightReady = %+v, want False/NetworkRefMissing", cond)
	}
}

func TestEnsurePreflightValidSpecIsSatisfied(t *testing.T) {
	stub := &stubHarvester{}
	r := &testHarness{Dependencies: Dependencies{Harvester: stub}}
	inst := newProvisionInst()

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.Resources.NADName != "tenant-a/data-net" {
		t.Fatalf("NADName = %q, want tenant-a/data-net", inst.Status.Resources.NADName)
	}
	if stub.LastVMImageRef != defaultBakedImageName {
		t.Fatalf("resolved image = %q, want default %q", stub.LastVMImageRef, defaultBakedImageName)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("PreflightReady = %+v, want True", cond)
	}
	if cond.ObservedGeneration != inst.Generation {
		t.Fatalf("cond ObservedGeneration = %d, want %d", cond.ObservedGeneration, inst.Generation)
	}
}

// An explicit, unsupported engineVersion is still rejected Terminal — only
// an *unset* engineVersion gets the lenient default-to-highest treatment.
func TestEnsurePreflightUnsupportedEngineVersionIsTerminal(t *testing.T) {
	stub := &stubHarvester{}
	r := &testHarness{Dependencies: Dependencies{Harvester: stub}}
	inst := newProvisionInst()
	inst.Spec.EngineVersion = "15" // not in defaultBakedImageName's ["16","17"]

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTerminal {
		t.Fatalf("res = %+v, want Terminal", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonOSImageInvalid) {
		t.Fatalf("PreflightReady = %+v, want False/OSImageInvalid", cond)
	}
}

// Regression guard: once an instance has been provisioned (AppliedSpec set),
// preflight must never Terminal-reject it just because the catalog's latest
// entry for its OS stream later drops its engineVersion — that would flip an
// already-available instance to incompatible-parameters purely from a
// catalog edit, with no repave ever attempted. Reporting the EOL is
// ensureRepave's job (ImageDrift=EngineVersionEOL); preflight must stay
// Satisfied here.
func TestEnsurePreflightExistingInstanceEngineVersionEOLIsNotTerminal(t *testing.T) {
	stub := &stubHarvester{}
	r := &testHarness{Dependencies: Dependencies{Harvester: stub}}
	inst := newProvisionInst()
	inst.Spec.EngineVersion = "15" // not in defaultBakedImageName's ["16","17"]
	inst.Status.AppliedSpec = &dbaasv1.AppliedSpec{NetworkRef: inst.Spec.NetworkRef, EngineVersion: inst.Spec.EngineVersion}
	inst.Status.CurrentImageRevision = "old-revision"

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied — an existing instance must never be rejected for a catalog-only change", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("PreflightReady = %+v, want True", cond)
	}
	if stub.LastVMImageRef != "" {
		t.Fatalf("ResolveVMImage should not be called for an already-provisioned instance, got ref %q", stub.LastVMImageRef)
	}
}

func TestEnsurePreflightUsesConfiguredClassAndOSVersionDefault(t *testing.T) {
	const customOSVersion = "test-custom-version"
	const customImageName = "test-custom-image"
	catalog.BakedImages[customImageName] = catalog.BakedImageEntry{
		ImageName:               customImageName,
		OSVersion:               customOSVersion,
		SupportedEngineVersions: []string{"16"},
	}
	catalog.LatestBakedImages[customOSVersion] = catalog.BakedImageStream{
		Revision:        customImageName,
		ValidationState: catalog.ValidationValidated,
	}

	stub := &stubHarvester{}
	r := &testHarness{Dependencies: Dependencies{
		Harvester: stub,
		DatabaseDefaults: operatorconfig.DatabaseDefaults{
			OSVersion:      customOSVersion,
			StorageClass:   "custom-storage",
			MasterUsername: "platform_admin",
			Port:           6432,
		},
		InstanceClasses: map[string]dbaasv1.InstanceClassSpec{
			"db.custom": {CPUCores: 3, MemoryMB: 6144, MaxConnections: 250},
		},
	}}
	inst := newProvisionInst()
	inst.Spec.DBInstanceClass = "db.custom"

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied: %+v", res.Outcome, res)
	}
	if stub.LastVMImageRef != customImageName {
		t.Fatalf("resolved image = %q, want %q", stub.LastVMImageRef, customImageName)
	}
}

// A stream that is a genuinely unknown OS version (never registered in the
// catalog at all) cannot self-resolve — no amount of waiting fixes a typo in
// databaseDefaults.osVersion — so it stays Terminal.
func TestEnsurePreflightUnknownOSVersionIsTerminal(t *testing.T) {
	r := &testHarness{Dependencies: Dependencies{
		Harvester:        &stubHarvester{},
		DatabaseDefaults: operatorconfig.DatabaseDefaults{OSVersion: "nonexistent-version"},
	}}
	inst := newProvisionInst()

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTerminal {
		t.Fatalf("res = %+v, want Terminal", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonOSImageInvalid) {
		t.Fatalf("PreflightReady = %+v, want False/OSImageInvalid", cond)
	}
}

// A stream that IS registered but still ValidationPending (image built, not
// yet marked Validated — e.g. before Task 10 registers the real image) is
// also Terminal: catalog data can only change via a rebuild+redeploy, which
// already re-reconciles every instance on its own, so there is nothing a
// retry timer would gain over waiting for that redeploy.
func TestEnsurePreflightPendingCatalogStreamIsTerminal(t *testing.T) {
	const pendingOSVersion = "test-pending-version"
	catalog.LatestBakedImages[pendingOSVersion] = catalog.BakedImageStream{
		Revision:        "unused-revision",
		ValidationState: catalog.ValidationPending,
	}

	r := &testHarness{Dependencies: Dependencies{
		Harvester:        &stubHarvester{},
		DatabaseDefaults: operatorconfig.DatabaseDefaults{OSVersion: pendingOSVersion},
	}}
	inst := newProvisionInst()

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTerminal {
		t.Fatalf("res = %+v, want Terminal", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonOSImageInvalid) {
		t.Fatalf("PreflightReady = %+v, want False/OSImageInvalid", cond)
	}
}

// An edit to an immutable field (vs the create-time AppliedSpec snapshot) is
// refused loudly — the guard that sat on the legacy stop/start/modify paths now
// covers every runner entry.
func TestEnsurePreflightImmutableDriftIsTerminal(t *testing.T) {
	r := &testHarness{}
	inst := newProvisionInst()
	inst.Status.AppliedSpec = &dbaasv1.AppliedSpec{NetworkRef: "tenant-a/old-net"} // spec now says tenant-a/data-net

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTerminal {
		t.Fatalf("res = %+v, want Terminal", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonImmutableFieldChanged) {
		t.Fatalf("PreflightReady = %+v, want False/ImmutableFieldChanged", cond)
	}
}

// A user fixing the spec after a terminal park must clear the failure.
func TestEnsurePreflightRecoversFromTerminalPark(t *testing.T) {
	r := &testHarness{Dependencies: Dependencies{Harvester: &stubHarvester{}}}
	inst := newProvisionInst()
	networkRef := inst.Spec.NetworkRef
	inst.Spec.NetworkRef = ""

	res := r.ensurePreflight(context.Background(), inst)
	if res.Outcome != OutcomeTerminal {
		t.Fatalf("initial result = %+v, want Terminal", res)
	}
	failed := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if failed == nil || failed.Status != metav1.ConditionFalse ||
		failed.Reason != string(dbaasv1.ReasonNetworkRefMissing) || failed.ObservedGeneration != inst.Generation {
		t.Fatalf("initial PreflightReady = %+v, want current-generation False/NetworkRefMissing", failed)
	}

	inst.Spec.NetworkRef = networkRef
	inst.Generation++
	res = r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	recovered := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if recovered == nil || recovered.Status != metav1.ConditionTrue ||
		recovered.Reason != string(dbaasv1.ReasonPreflightPassed) || recovered.ObservedGeneration != inst.Generation {
		t.Fatalf("recovered PreflightReady = %+v, want current-generation True/PreflightPassed", recovered)
	}
}

func TestEnsurePreflightImageFailures(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		outcome Outcome
		status  metav1.ConditionStatus
		reason  dbaasv1.ConditionReason
		requeue bool
	}{
		{"invalid", harvester.ErrVMImageReferenceInvalid, OutcomeTerminal, metav1.ConditionFalse, dbaasv1.ReasonOSImageInvalid, false},
		{"ambiguous", harvester.ErrVMImageAmbiguous, OutcomeTerminal, metav1.ConditionFalse, dbaasv1.ReasonOSImageInvalid, false},
		{"not found", harvester.ErrVMImageNotFound, OutcomeTerminal, metav1.ConditionFalse, dbaasv1.ReasonOSImageNotFound, false},
		{"not ready", harvester.ErrVMImageNotReady, OutcomePending, metav1.ConditionUnknown, dbaasv1.ReasonOSImageNotReady, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &testHarness{Dependencies: Dependencies{Harvester: &stubHarvester{ResolveVMImageErr: tt.err}}}
			inst := newProvisionInst()
			res := r.ensurePreflight(context.Background(), inst)

			if res.Outcome != tt.outcome {
				t.Fatalf("Outcome = %q, want %q", res.Outcome, tt.outcome)
			}
			if (res.ControllerResult.RequeueAfter == preflightRequeue) != tt.requeue {
				t.Fatalf("RequeueAfter = %v, want timer=%v", res.ControllerResult.RequeueAfter, tt.requeue)
			}
			cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
			if cond == nil || cond.Status != tt.status || cond.Reason != string(tt.reason) {
				t.Fatalf("PreflightReady = %+v, want %s/%s", cond, tt.status, tt.reason)
			}
		})
	}
}

func TestEnsurePreflightImageAPIFailureIsTransient(t *testing.T) {
	boom := errors.New("harvester API unavailable")
	r := &testHarness{Dependencies: Dependencies{Harvester: &stubHarvester{ResolveVMImageErr: boom}}}
	inst := newProvisionInst()

	res := r.ensurePreflight(context.Background(), inst)

	if res.Outcome != OutcomeTransient || !errors.Is(res.Err, boom) {
		t.Fatalf("res = %+v, want Transient wrapping API failure", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPreflightReady)
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != string(dbaasv1.ReasonValidationPending) {
		t.Fatalf("PreflightReady = %+v, want Unknown/ValidationPending", cond)
	}
}
