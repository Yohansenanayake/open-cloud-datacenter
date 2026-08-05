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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/catalog"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// newRepaveFixture: an Available instance, already provisioned on
// "old-revision", with a VM shaped to match its own spec (repave tests don't
// exercise resize drift) whose RunStrategy/observed Readiness the test
// varies to walk the cold-repave sequence. defaultBakedImageName (Validated
// for defaultOSVersion by this package's init()) is always the "latest"
// revision available to repave onto.
func newRepaveFixture(t *testing.T, rs kubevirtv1.VirtualMachineRunStrategy, readiness harvester.VMIReadiness) (*testHarness, *dbaasv1.DBInstance, *stubHarvester) {
	t.Helper()
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders"
	inst.Status.AppliedSpec = &dbaasv1.AppliedSpec{NetworkRef: inst.Spec.NetworkRef}
	inst.Status.CurrentImageRevision = "old-revision"
	inst.Status.Phase = dbaasv1.StatusAvailable
	stub := &stubHarvester{Readiness: readiness}
	vm := shapedVM("pg-orders", "tenant-a", "db.t3.small", 20, "pg-orders-data", rs)
	r := newTestHarness(t, stub, inst, vm)
	return r, inst, stub
}

func triggerRepave(inst *dbaasv1.DBInstance) {
	inst.Annotations = map[string]string{dbaasv1.AnnotationRepaveTrigger: "now"}
}

func noProviderCalls(stub *stubHarvester) bool {
	return stub.StopVMCalls == 0 && stub.SwapVMOSDiskCalls == 0 && stub.DeletePVCCalls == 0
}

// wantDrift asserts ImageDrift's full three-valued state. ImageDrift is never
// removed once a pass has evaluated it — absence would mean Unknown, which is
// reserved for "no validated stream to compare against" — so every assertion
// here checks Status and Reason rather than presence.
func wantDrift(t *testing.T, inst *dbaasv1.DBInstance, status metav1.ConditionStatus, reason dbaasv1.ConditionReason) {
	t.Helper()
	c := inst.Status.GetCondition(dbaasv1.ConditionImageDrift)
	if c == nil {
		t.Fatalf("ImageDrift is absent, want %s/%s", status, reason)
	}
	if c.Status != status || c.Reason != string(reason) {
		t.Fatalf("ImageDrift = %s/%s, want %s/%s", c.Status, c.Reason, status, reason)
	}
}

// --- no-op cases: catalog unresolvable ---

func TestEnsureRepaveNoCatalogEntrySatisfied(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	r.Dependencies.DatabaseDefaults = operatorconfig.DatabaseDefaults{OSVersion: "nonexistent-version"}

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	// Unknown, not False: an unresolvable stream means drift was never
	// evaluated, which must stay distinguishable from "you are up to date".
	wantDrift(t, inst, metav1.ConditionUnknown, dbaasv1.ReasonImageCatalogUnresolved)
	if !noProviderCalls(stub) {
		t.Fatal("no provider calls expected when the feature no-ops")
	}
}

func TestEnsureRepavePendingCatalogEntrySatisfied(t *testing.T) {
	const pendingOSVersion = "test-repave-pending-version"
	catalog.LatestBakedImages[pendingOSVersion] = catalog.BakedImageStream{
		Revision:        "unused-revision",
		ValidationState: catalog.ValidationPending,
	}

	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	r.Dependencies.DatabaseDefaults = operatorconfig.DatabaseDefaults{OSVersion: pendingOSVersion}

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if !noProviderCalls(stub) {
		t.Fatal("no provider calls expected while the stream is still Pending")
	}
}

// --- drift detection (no trigger annotation) ---

func TestEnsureRepaveOnLatestRevisionNoDrift(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	inst.Status.CurrentImageRevision = defaultBakedImageName

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	wantDrift(t, inst, metav1.ConditionFalse, dbaasv1.ReasonImageUpToDate)
	if !noProviderCalls(stub) {
		t.Fatal("no provider calls expected with no drift")
	}
}

// TestEnsureRepaveSelfHealsCurrentImageRevisionFromVMReality covers the gap
// found in a real deployment: a repave's swap succeeds for real (Harvester
// stamps the new OS-disk PVC with its ImageID), but the status patch that
// would have recorded CurrentImageRevision to match is lost to a conflict —
// and nothing else would ever retry that specific write, since every other
// writer is gated behind the one-shot repave-trigger annotation. The next
// pass must notice the mismatch against the VM's actual OS-disk PVC and
// correct it on its own, with no provider mutation and no trigger needed.
func TestEnsureRepaveSelfHealsCurrentImageRevisionFromVMReality(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	// inst.Status.CurrentImageRevision is "old-revision" from the fixture —
	// stale relative to what the VM's OS-disk PVC actually says.
	stub.OSDiskImageID = "default/" + defaultBakedImageName

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if inst.Status.CurrentImageRevision != defaultBakedImageName {
		t.Fatalf("CurrentImageRevision = %q, want self-healed to %q", inst.Status.CurrentImageRevision, defaultBakedImageName)
	}
	wantDrift(t, inst, metav1.ConditionFalse, dbaasv1.ReasonImageUpToDate)
	if !noProviderCalls(stub) {
		t.Fatal("self-heal must not perform any VM/disk operation — it only corrects a status field")
	}
}

// TestEnsureRepaveSelfHealIgnoresUnresolvableImageID guards against a false
// correction: an ImageID that doesn't map to any registered catalog entry
// (or is empty, e.g. VM not created yet) must leave CurrentImageRevision
// untouched rather than zeroing/misreporting it.
func TestEnsureRepaveSelfHealIgnoresUnresolvableImageID(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	stub.OSDiskImageID = "default/some-image-not-in-the-catalog"

	_ = r.ensureRepave(context.Background(), inst)

	if inst.Status.CurrentImageRevision != "old-revision" {
		t.Fatalf("CurrentImageRevision = %q, want unchanged when the observed ImageID is unresolvable", inst.Status.CurrentImageRevision)
	}
}

func TestEnsureRepaveSelfHealReturnsTransientOnImageObservationError(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	stub.OSDiskImageIDErr = errors.New("harvester unavailable")

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeTransient {
		t.Fatalf("res = %+v, want Transient", res)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "observe VM OS-disk image: harvester unavailable") {
		t.Fatalf("res.Err = %v, want contextual image-observation error", res.Err)
	}
}

func TestEnsureRepaveDriftEngineVersionSupportedReportsOSUpdateAvailable(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	inst.Spec.EngineVersion = "16" // in defaultBakedImageName's SupportedEngineVersions

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionImageDrift)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != string(dbaasv1.ReasonOSUpdateAvailable) {
		t.Fatalf("ImageDrift = %+v, want True/OSUpdateAvailable", cond)
	}
	if !noProviderCalls(stub) {
		t.Fatal("no provider calls expected without the trigger annotation")
	}
}

// Regression guard: engineVersion is +optional and was never enforced
// pre-catalog — an unset value must be treated as supported, not EOL'd,
// exactly like preflight already does (they share engineVersionSupported).
func TestEnsureRepaveUnsetEngineVersionTreatedAsSupported(t *testing.T) {
	r, inst, _ := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	inst.Spec.EngineVersion = ""

	r.ensureRepave(context.Background(), inst)

	cond := inst.Status.GetCondition(dbaasv1.ConditionImageDrift)
	if cond == nil || cond.Reason != string(dbaasv1.ReasonOSUpdateAvailable) {
		t.Fatalf("ImageDrift = %+v, want Reason=OSUpdateAvailable for an unset engineVersion", cond)
	}
}

func TestEnsureRepaveDriftEngineVersionDroppedReportsEngineVersionEOL(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	inst.Spec.EngineVersion = "15" // not in defaultBakedImageName's SupportedEngineVersions

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionImageDrift)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != string(dbaasv1.ReasonEngineVersionEOL) {
		t.Fatalf("ImageDrift = %+v, want True/EngineVersionEOL", cond)
	}
	if !noProviderCalls(stub) {
		t.Fatal("no provider calls expected without the trigger annotation")
	}
}

// --- trigger annotation: rejections ---

func TestEnsureRepaveTriggerNotAvailableIsTerminal(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	inst.Status.Phase = dbaasv1.StatusModifying
	triggerRepave(inst)

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeTerminal || res.Reason != dbaasv1.ReasonRepaveNotAvailable {
		t.Fatalf("res = %+v, want Terminal/RepaveNotAvailable", res)
	}
	if _, ok := inst.Annotations[dbaasv1.AnnotationRepaveTrigger]; ok {
		t.Fatal("trigger annotation should be cleared")
	}
	if stub.StopVMCalls != 0 {
		t.Fatal("must not touch the VM when the instance isn't Available")
	}
	// Regression guard: Result.Reason/Message are otherwise dropped entirely
	// (reconcileInstance only reads ControllerResult/Err) — without a
	// condition, a blocked repave leaves no observable trace at all.
	cond := inst.Status.GetCondition(dbaasv1.ConditionRepaveInProgress)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonRepaveNotAvailable) {
		t.Fatalf("RepaveInProgress = %+v, want False/RepaveNotAvailable", cond)
	}
}

// Regression guard for M1's Bug-2-class ordering: engineVersion must be
// re-validated before any destructive operation, so no StopVM call happens.
func TestEnsureRepaveTriggerEngineVersionEOLBlockedIsTerminal(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	inst.Spec.EngineVersion = "15"
	triggerRepave(inst)

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeTerminal || res.Reason != dbaasv1.ReasonRepaveBlockedEOL {
		t.Fatalf("res = %+v, want Terminal/RepaveBlockedEOL", res)
	}
	if _, ok := inst.Annotations[dbaasv1.AnnotationRepaveTrigger]; ok {
		t.Fatal("trigger annotation should be cleared")
	}
	if stub.StopVMCalls != 0 {
		t.Fatal("must not stop the VM for a repave that's blocked before any destructive step")
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionRepaveInProgress)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonRepaveBlockedEOL) {
		t.Fatalf("RepaveInProgress = %+v, want False/RepaveBlockedEOL", cond)
	}
}

// M1's never-executed no-op case: already on the target revision.
func TestEnsureRepaveTriggerAlreadyOnTargetIsNoop(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	inst.Status.CurrentImageRevision = defaultBakedImageName
	triggerRepave(inst)

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if _, ok := inst.Annotations[dbaasv1.AnnotationRepaveTrigger]; ok {
		t.Fatal("trigger annotation should be cleared")
	}
	if !noProviderCalls(stub) {
		t.Fatal("no StopVM/SwapVMOSDisk calls expected for a same-revision no-op trigger")
	}
}

// --- trigger annotation: cold-repave sequence ---

func TestEnsureRepaveTriggerVMRunningStopsIt(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	triggerRepave(inst)

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending", res)
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("StopVMCalls = %d, want 1", stub.StopVMCalls)
	}
	if stub.SwapVMOSDiskCalls != 0 {
		t.Fatal("must not swap the disk while the VM may still be running")
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must go False once the VM is halted for repave")
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionRepaveInProgress)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != string(dbaasv1.ReasonRepaveStopping) {
		t.Fatalf("RepaveInProgress = %+v, want True/RepaveStopping", cond)
	}
	// Trigger annotation is only cleared once the repave actually applies or
	// is rejected, not while it's merely in progress.
	if _, ok := inst.Annotations[dbaasv1.AnnotationRepaveTrigger]; !ok {
		t.Fatal("trigger annotation should remain set while stopping")
	}
}

func TestEnsureRepaveTriggerWaitsForTeardown(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{Running: true})
	triggerRepave(inst)

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomePending || res.ControllerResult.RequeueAfter != powerRequeue {
		t.Fatalf("res = %+v, want Pending with powerRequeue timer", res)
	}
	if stub.StopVMCalls != 0 || stub.SwapVMOSDiskCalls != 0 {
		t.Fatalf("no provider calls during teardown wait, got stop:%d swap:%d", stub.StopVMCalls, stub.SwapVMOSDiskCalls)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must stay False while waiting for teardown")
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionRepaveInProgress)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != string(dbaasv1.ReasonRepaveWaitingForTeardown) {
		t.Fatalf("RepaveInProgress = %+v, want True/RepaveWaitingForTeardown", cond)
	}
}

func TestEnsureRepaveTriggerAppliesSwapWhenDown(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})
	convergeCredentials(t, context.Background(), r, inst) // regenerateCloudInit needs stable, already-resolved Material
	triggerRepave(inst)

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != dbaasv1.ReasonRepaveApplied {
		t.Fatalf("res = %+v, want Pending/RepaveApplied", res)
	}
	if stub.SwapVMOSDiskCalls != 1 {
		t.Fatalf("SwapVMOSDiskCalls = %d, want 1", stub.SwapVMOSDiskCalls)
	}
	if stub.DeletePVCCalls != 1 {
		t.Fatalf("DeletePVCCalls = %d, want 1", stub.DeletePVCCalls)
	}
	if stub.LastDeletedPVCName == "" {
		t.Fatal("DeletePVC should have been called with a non-empty old PVC name")
	}
	if inst.Status.Resources.OSDiskPVCName == "" || inst.Status.Resources.OSDiskPVCName == "pg-orders-os" {
		t.Fatalf("OSDiskPVCName = %q, want a new revision-suffixed name", inst.Status.Resources.OSDiskPVCName)
	}
	if inst.Status.CurrentImageRevision != defaultBakedImageName {
		t.Fatalf("CurrentImageRevision = %q, want %q", inst.Status.CurrentImageRevision, defaultBakedImageName)
	}
	if inst.Status.Resources.PendingDeleteOSDiskPVCName != "" {
		t.Fatal("PendingDeleteOSDiskPVCName should be cleared once DeletePVC succeeds")
	}
	wantDrift(t, inst, metav1.ConditionFalse, dbaasv1.ReasonImageUpToDate)
	if _, ok := inst.Annotations[dbaasv1.AnnotationRepaveTrigger]; ok {
		t.Fatal("trigger annotation should be cleared once applied")
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionRepaveInProgress)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != string(dbaasv1.ReasonRepaveApplied) {
		t.Fatalf("RepaveInProgress = %+v, want True/RepaveApplied", cond)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must go False once repave is applied — power hasn't restarted the VM yet")
	}
	// The cloud-init Secret is rebuilt as part of applying the repave —
	// inst.Spec.EngineVersion is unset, so its content must reflect
	// effectiveEngineVersion's default (defaultBakedImageName's highest
	// supported version, "17"), not an empty ENGINE_VERSION.
	if inst.Status.Resources.CloudInitSecretName == "" {
		t.Fatal("CloudInitSecretName should be (re)recorded after regenerating cloud-init")
	}
	var ci corev1.Secret
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-a", Name: inst.Status.Resources.CloudInitSecretName}, &ci); err != nil {
		t.Fatalf("cloud-init secret missing: %v", err)
	}
	if !strings.Contains(string(ci.Data["userdata"]), "ENGINE_VERSION=17") {
		t.Fatalf("cloud-init userdata missing defaulted ENGINE_VERSION=17: %s", ci.Data["userdata"])
	}
}

// Once a repave has applied and the VM is still down (power hasn't restarted
// it yet), a later pass with no outstanding trigger just hands off to power.
func TestEnsureRepaveSwapAppliedVMStillDownSatisfied(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})
	inst.Status.CurrentImageRevision = defaultBakedImageName

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if !noProviderCalls(stub) {
		t.Fatal("no provider calls expected once the revision already matches")
	}
}

func TestEnsureRepaveSwapErrorIsTransientNoStatusUpdate(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})
	stub.SwapVMOSDiskErr = errors.New("harvester unavailable")
	triggerRepave(inst)

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeTransient || res.Err == nil {
		t.Fatalf("res = %+v, want Transient with error", res)
	}
	if inst.Status.Resources.OSDiskPVCName != "" {
		t.Fatal("OSDiskPVCName must not update when SwapVMOSDisk fails")
	}
	if inst.Status.CurrentImageRevision != "old-revision" {
		t.Fatal("CurrentImageRevision must not update when SwapVMOSDisk fails")
	}
	if inst.Status.Resources.PendingDeleteOSDiskPVCName != "" {
		t.Fatal("PendingDeleteOSDiskPVCName must not be set when the swap itself never succeeded")
	}
}

// Decision #16: DeletePVC failing after a successful swap must still record
// PendingDeleteOSDiskPVCName, so the next pass's recovery check can retry
// the delete without depending on SwapVMOSDisk's idempotent no-op.
func TestEnsureRepaveDeletePVCErrorAfterSwapRecordsPendingDelete(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})
	stub.DeletePVCErr = errors.New("transient RBAC error")
	triggerRepave(inst)

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeTransient || res.Err == nil {
		t.Fatalf("res = %+v, want Transient with error", res)
	}
	if inst.Status.Resources.PendingDeleteOSDiskPVCName == "" {
		t.Fatal("PendingDeleteOSDiskPVCName should be set to the old PVC name even though DeletePVC failed")
	}
}

// Regression guard for decision #16: a pending delete recorded by a prior
// interrupted pass is retried before any drift-detection or dispatch logic
// runs — no trigger annotation needed, no VM object needed.
func TestEnsureRepaveRecoversPendingDeleteBeforeAnythingElse(t *testing.T) {
	inst := newProvisionInst()
	inst.Status.CurrentImageRevision = defaultBakedImageName // avoid unrelated drift noise
	inst.Status.Resources.PendingDeleteOSDiskPVCName = "pg-orders-os-stale"
	stub := &stubHarvester{}
	r := newTestHarness(t, stub, inst)

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if stub.DeletePVCCalls != 1 {
		t.Fatalf("DeletePVCCalls = %d, want 1", stub.DeletePVCCalls)
	}
	if stub.LastDeletedPVCName != "pg-orders-os-stale" {
		t.Fatalf("LastDeletedPVCName = %q, want pg-orders-os-stale", stub.LastDeletedPVCName)
	}
	if inst.Status.Resources.PendingDeleteOSDiskPVCName != "" {
		t.Fatal("PendingDeleteOSDiskPVCName should be cleared once the recovery delete succeeds")
	}
}

// Stopping the VM sets RepaveInProgress=True, which flips Phase away from
// Available on the next status derivation — the Phase gate must not mistake
// that self-caused change for the instance having become unavailable and
// abort the repave it's in the middle of applying.
func TestEnsureRepaveContinuesAfterOwnPhaseChange(t *testing.T) {
	ctx := context.Background()
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	convergeCredentials(t, ctx, r, inst) // regenerateCloudInit (pass 2) needs stable Material
	triggerRepave(inst)

	// Pass 1: VM running -> stop it.
	res := r.ensureRepave(ctx, inst)
	if res.Outcome != OutcomePending || res.Reason != dbaasv1.ReasonRepaveStopping {
		t.Fatalf("pass 1 = %+v, want Pending/RepaveStopping", res)
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("StopVMCalls = %d, want 1", stub.StopVMCalls)
	}

	// Simulate finalizeStatus deriving Phase from the conditions pass 1 just
	// set — this is the exact mechanism that flips Phase to Modifying.
	inst.Status.Phase = dbaasv1.DerivePhaseSummary(inst).Phase
	if inst.Status.Phase != dbaasv1.StatusModifying {
		t.Fatalf("test setup: Phase = %q, want %q after RepaveInProgress=True", inst.Status.Phase, dbaasv1.StatusModifying)
	}

	// Simulate Harvester having actually stopped the VM and torn down its
	// VMI by now.
	var vm kubevirtv1.VirtualMachine
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders"}, &vm); err != nil {
		t.Fatalf("get VM: %v", err)
	}
	halted := kubevirtv1.RunStrategyHalted
	vm.Spec.RunStrategy = &halted
	if err := r.Update(ctx, &vm); err != nil {
		t.Fatalf("update VM: %v", err)
	}
	stub.Readiness = harvester.VMIReadiness{}

	// Pass 2: trigger annotation is still set, Phase is now Modifying
	// (self-caused) — must proceed to the swap, not Terminal-abort.
	res = r.ensureRepave(ctx, inst)
	if res.Outcome != OutcomePending || res.Reason != dbaasv1.ReasonRepaveApplied {
		t.Fatalf("pass 2 = %+v, want Pending/RepaveApplied — repave must continue past its own Phase gate", res)
	}
	if stub.SwapVMOSDiskCalls != 1 {
		t.Fatalf("SwapVMOSDiskCalls = %d, want 1", stub.SwapVMOSDiskCalls)
	}
	if _, ok := inst.Annotations[dbaasv1.AnnotationRepaveTrigger]; ok {
		t.Fatal("trigger annotation should be cleared once applied")
	}
}
