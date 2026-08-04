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
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/catalog"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

type repaveStep struct{ Dependencies }

func newRepaveStep(deps Dependencies) Step { return &repaveStep{Dependencies: deps} }

func (*repaveStep) Name() string { return "repave" }

// ensureRepave reports baked-image drift (ConditionImageDrift, report-only,
// every pass) and applies a repave when dbaasv1.AnnotationRepaveTrigger is
// "now": cold-halt the VM, swap the OS disk to the catalog's current
// revision, regenerate cloud-init, and let ensurePowerState (ordered after
// this step) restart it — same halt/apply/restart shape as ensureResize, and
// ordered directly before it so the two never fight over the VM's power
// state.
//
// If the catalog can't resolve a stream for databaseDefaults.osVersion (unset,
// unknown, or not yet Validated — see resolveBakedImage), this step no-ops
// entirely: no drift is reported and a trigger annotation is left untouched
// for the next pass to reconsider once the catalog is validated.
func (r *repaveStep) Run(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	// Self-heal CurrentImageRevision from the VM's actual OS-disk PVC: a
	// status write here can be lost to a conflicted patch with nothing else
	// to ever retry it (every other writer is gated behind the one-shot
	// repave-trigger annotation).
	if inst.Status.AppliedSpec != nil {
		if imageID, err := r.Harvester.GetVMOSDiskImageID(ctx, inst.Namespace, vmNameFor(inst)); err == nil && imageID != "" {
			if _, imageName, found := strings.Cut(imageID, "/"); found {
				if rev, ok := catalog.RevisionForImageName(imageName); ok && rev != inst.Status.CurrentImageRevision {
					inst.Status.CurrentImageRevision = rev
				}
			}
		}
	}

	// Decision #16 — crash-safety recovery. Runs first, unconditionally,
	// regardless of catalog state: if a prior pass recorded a pending delete
	// and was interrupted before DeletePVC succeeded, retry it here rather
	// than depending on SwapVMOSDisk's idempotent no-op to notice there's
	// still cleanup to do.
	if pending := inst.Status.Resources.PendingDeleteOSDiskPVCName; pending != "" {
		if err := r.Harvester.DeletePVC(ctx, inst.Namespace, pending); err != nil {
			return Transient(err)
		}
		inst.Status.Resources.PendingDeleteOSDiskPVCName = ""
	}

	defaults := r.databaseDefaults()
	entry, stream, ok := resolveBakedImage(defaults)
	if !ok {
		// Nothing to compare against — the feature no-ops exactly as if the
		// catalog were empty. Clear any stale drift from before the stream
		// became unresolvable (e.g. a config change) rather than leaving it
		// to look actionable.
		inst.Status.RemoveCondition(dbaasv1.ConditionImageDrift)
		return Satisfied()
	}

	// --- drift detection: runs every pass a stream is resolvable, NOT gated
	// on the trigger annotation. One condition, Reason carries which flavor
	// (decision #9) — a safe update (ReasonOSUpdateAvailable) vs one blocked
	// by an EOL'd engineVersion (ReasonEngineVersionEOL).
	if inst.Status.AppliedSpec != nil && inst.Status.CurrentImageRevision != stream.Revision {
		if _, ok := effectiveEngineVersion(inst.Spec.EngineVersion, entry); ok {
			inst.SetCurrentCondition(dbaasv1.ConditionImageDrift, metav1.ConditionTrue,
				dbaasv1.ReasonOSUpdateAvailable,
				fmt.Sprintf("VM is on image revision %q; revision %q available — annotate with %s=now to repave",
					inst.Status.CurrentImageRevision, stream.Revision, dbaasv1.AnnotationRepaveTrigger))
		} else {
			inst.SetCurrentCondition(dbaasv1.ConditionImageDrift, metav1.ConditionTrue,
				dbaasv1.ReasonEngineVersionEOL,
				fmt.Sprintf("engineVersion %q is not available in revision %q (available: %v) — migrate data before repaving",
					inst.Spec.EngineVersion, stream.Revision, entry.SupportedEngineVersions))
		}
	} else {
		inst.Status.RemoveCondition(dbaasv1.ConditionImageDrift)
	}

	// --- repave dispatch: gated on the trigger annotation ---
	if inst.Annotations[dbaasv1.AnnotationRepaveTrigger] != "now" {
		return Satisfied()
	}

	// An in-flight repave (RepaveInProgress=True) already halted the VM and
	// therefore already moved Phase off Available by the time this step runs
	// again — only gate the true entry point, not a phase change this step
	// caused itself. Without this, a repave that gets as far as stopping the
	// VM can never finish: the next pass sees its own RepaveInProgress=True
	// having flipped Phase to Modifying, Terminal-aborts on that self-caused
	// change, clears the trigger, and leaves the VM halted on the old image
	// with no automatic retry .
	if inst.Status.Phase != dbaasv1.StatusAvailable &&
		!inst.Status.IsConditionTrue(dbaasv1.ConditionRepaveInProgress) {
		msg := "repave requires the instance to be Available"
		if err := r.clearTriggerAnnotation(ctx, inst); err != nil {
			return Transient(err)
		}
		// Every other Terminal branch in this package sets a condition
		// before returning — Result.Reason/Message are otherwise dropped
		// entirely (reconcileInstance only reads ControllerResult/Err), so
		// without this a blocked repave leaves no observable trace beyond
		// the trigger annotation silently disappearing.
		inst.SetCurrentCondition(dbaasv1.ConditionRepaveInProgress, metav1.ConditionFalse, dbaasv1.ReasonRepaveNotAvailable, msg)
		return Terminal(dbaasv1.ReasonRepaveNotAvailable, msg)
	}
	// Re-check engineVersion compatibility independently of the drift
	// condition's cached Reason above — defense-in-depth, validate again
	// immediately before any destructive operation (§7 ordering safety).
	engineVersion, ok := effectiveEngineVersion(inst.Spec.EngineVersion, entry)
	if !ok {
		msg := fmt.Sprintf("engineVersion %q is not available in revision %q; migrate data before repaving",
			inst.Spec.EngineVersion, stream.Revision)
		if err := r.clearTriggerAnnotation(ctx, inst); err != nil {
			return Transient(err)
		}
		inst.SetCurrentCondition(dbaasv1.ConditionRepaveInProgress, metav1.ConditionFalse, dbaasv1.ReasonRepaveBlockedEOL, msg)
		return Terminal(dbaasv1.ReasonRepaveBlockedEOL, msg)
	}
	if inst.Status.CurrentImageRevision == stream.Revision {
		if err := r.clearTriggerAnnotation(ctx, inst); err != nil {
			return Transient(err)
		}
		return Satisfied()
	}

	var vm kubevirtv1.VirtualMachine
	if err := r.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: vmNameFor(inst)}, &vm); err != nil {
		return Transient(err)
	}
	halted := vm.Spec.RunStrategy != nil && *vm.Spec.RunStrategy == kubevirtv1.RunStrategyHalted

	if !halted {
		msg := fmt.Sprintf("stopping VM for repave to revision %s", stream.Revision)
		inst.SetCurrentCondition(dbaasv1.ConditionRepaveInProgress, metav1.ConditionTrue, dbaasv1.ReasonRepaveStopping, msg)
		if err := r.Harvester.StopVM(ctx, inst.Namespace, vmNameFor(inst)); err != nil {
			return Transient(err)
		}
		// Same reasoning as ensureResize's halting branches: set
		// DatabaseReady=False here so the aggregate Ready condition doesn't
		// stay stale-True for the whole repave window (health/power haven't
		// run yet this pass — the runner stops at this step's Pending).
		inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonRepaveStopping, msg)
		return Pending(dbaasv1.ReasonRepaveStopping, msg)
	}

	readiness, err := r.Harvester.GetVMIReadiness(ctx, inst.Namespace, vmNameFor(inst))
	if err != nil && !apierrors.IsNotFound(err) {
		return Transient(err)
	}
	if err == nil && readiness.Running {
		msg := "waiting for VM to stop before repave"
		inst.SetCurrentCondition(dbaasv1.ConditionRepaveInProgress, metav1.ConditionTrue, dbaasv1.ReasonRepaveWaitingForTeardown, msg)
		inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonRepaveWaitingForTeardown, msg)
		return PendingAfter(dbaasv1.ReasonRepaveWaitingForTeardown, msg, powerRequeue)
	}

	// VM is down: swap the OS disk, then delete the old one. No
	// ClearDataVolumeOwnerRef/DeleteDataVolume — decision #5 drops both as
	// unnecessary for this storage backend.
	oldPVC, newPVC, err := r.Harvester.SwapVMOSDisk(ctx, inst.Namespace, vmNameFor(inst), inst.Name, entry.ImageName)
	if err != nil {
		return Transient(err) // status untouched — don't claim a swap that didn't happen
	}
	if oldPVC != "" {
		// Decision #16: record before attempting the delete, not after — if
		// DeletePVC fails (or the process dies right here), this survives the
		// one status patch at the end of Reconcile, and the recovery check
		// at the top of this function retries it next pass.
		inst.Status.Resources.PendingDeleteOSDiskPVCName = oldPVC
		if err := r.Harvester.DeletePVC(ctx, inst.Namespace, oldPVC); err != nil {
			return Transient(err)
		}
		inst.Status.Resources.PendingDeleteOSDiskPVCName = ""
	}
	inst.Status.Resources.OSDiskPVCName = newPVC
	inst.Status.CurrentImageRevision = stream.Revision
	inst.Status.RemoveCondition(dbaasv1.ConditionImageDrift) // drift resolved by the swap

	if res, stop := r.regenerateCloudInit(ctx, inst, engineVersion); stop {
		return res
	}

	// Clear the annotation and report Pending; the power step (ordered after
	// this one) observes desired-running + declared-Halted and restarts on
	// its own — no manual StartVM call needed here.
	if err := r.clearTriggerAnnotation(ctx, inst); err != nil {
		return Transient(err)
	}
	msg := fmt.Sprintf("applied repave to revision %s", stream.Revision)
	inst.SetCurrentCondition(dbaasv1.ConditionRepaveInProgress, metav1.ConditionTrue, dbaasv1.ReasonRepaveApplied, msg)
	inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonRepaveApplied, msg)
	return Pending(dbaasv1.ReasonRepaveApplied, msg)
}

// clearTriggerAnnotation removes AnnotationRepaveTrigger once a repave has
// either applied or been rejected. Annotations live in ObjectMeta, not
// Status, so this is a real object Update — separate from, and issued
// before, the single deferred status patch every other write in this file
// participates in.
//
// DBInstance has a status subresource, so the API server (and the fake
// client used in tests) ignore inst.Status in the request and return the
// object with whatever Status was last durably persisted — which
// client-go then decodes into inst, in place. Since nothing this Run() call
// has done to inst.Status is durable yet (only the single deferred status
// patch at the very end of Reconcile persists it), a bare Update here would
// silently wipe out every Status mutation made earlier in this same pass
// (verified empirically, not just inferred). Save and restore it around the
// call.
func (r *repaveStep) clearTriggerAnnotation(ctx context.Context, inst *dbaasv1.DBInstance) error {
	if _, ok := inst.Annotations[dbaasv1.AnnotationRepaveTrigger]; !ok {
		return nil
	}
	delete(inst.Annotations, dbaasv1.AnnotationRepaveTrigger)
	status := inst.Status
	err := r.Update(ctx, inst)
	inst.Status = status
	return err
}

// regenerateCloudInit rebuilds the cloud-init Secret from durable credential
// Material, mirroring createVM's initial build (vm.go) — DBName/
// MasterUsername/Port/etc. are all immutable (AppliedSpec), so reusing the
// same BuildCloudInit/resource.Apply sequence keeps a repave's cloud-init in
// lockstep with creation rather than risking a second, divergent renderer.
// engineVersion is the caller's already-resolved effectiveEngineVersion —
// bootstrap.sh activates exactly this version against the freshly swapped
// image. Returns (Result, true) when Run should return that Result
// immediately — mirrors trackRestarts's (Result, bool) idiom in health.go.
func (r *repaveStep) regenerateCloudInit(ctx context.Context, inst *dbaasv1.DBInstance, engineVersion string) (Result, bool) {
	classSpec, ok := r.instanceClasses()[inst.Spec.DBInstanceClass]
	if !ok {
		// ensurePreflight/ensureResize validate this first; defensive.
		msg := fmt.Sprintf("unknown dbInstanceClass %q", inst.Spec.DBInstanceClass)
		return Terminal(dbaasv1.ReasonInvalidClass, msg), true
	}
	defaults := r.databaseDefaults()
	masterUser := inst.Spec.MasterUsername
	if masterUser == "" {
		masterUser = defaults.MasterUsername
	}
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}

	resolved, err := r.credentialsResolver().Resolve(ctx, inst)
	if err != nil {
		return Transient(err), true
	}
	if resolved.Changed {
		msg := "credential material changed unexpectedly while regenerating repave cloud-init; waiting for observation"
		return PendingAfter(dbaasv1.ReasonCredentialsCreated, msg, credentialRequeue), true
	}
	userdata, networkdata := credentials.BuildCloudInit(credentials.BootstrapParams{
		ID:             inst.Name,
		DBName:         dbName,
		Port:           specPortWithDefault(inst.Spec.Port, defaults.Port),
		MasterUser:     masterUser,
		MaxConnections: classSpec.MaxConnections,
		BackupEnabled:  inst.Spec.BackupRetentionPeriod > 0,
		BackupWindow:   inst.Spec.PreferredBackupWindow,
		S3Config:       inst.Spec.S3BackupConfig,
		VMPassword:     inst.Spec.VMPassword,
		StaticNetwork:  inst.Spec.StaticNetwork,
		EngineVersion:  engineVersion,
	}, resolved.Material)

	cloudInitName := resource.CloudInitSecretName(inst)
	if _, err := resource.Apply(ctx, r.Client, r.Scheme(), inst, resource.CloudInitSecret{
		Instance:    inst,
		UserData:    userdata,
		NetworkData: networkdata,
	}); err != nil {
		return Transient(err), true
	}
	inst.Status.Resources.CloudInitSecretName = cloudInitName
	return Result{}, false
}
