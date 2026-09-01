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
// every pass) and applies a repave when dbaasv1.AnnotationRepaveTrigger's
// value differs from Status.LastAppliedRepaveTrigger: cold-halt the VM, swap
// the OS disk to the catalog's current revision, regenerate cloud-init, and
// let ensurePowerState (ordered after this step) restart it — same
// halt/apply/restart shape as ensureResize, and ordered directly before it so
// the two never fight over the VM's power state. The annotation itself is
// never modified by the controller (Flux ReconcileRequestAnnotation style);
// each accept/reject/apply outcome instead records the value it processed
// into Status.LastAppliedRepaveTrigger through the normal deferred status
// patch, so a new repave only dispatches once the annotation is set to a
// fresh value.
//
// If the catalog can't resolve a stream for databaseDefaults.osVersion (unset,
// unknown, or not yet Validated — see resolveBakedImage), this step no-ops
// entirely: no drift is reported and the trigger annotation is left
// unexamined for the next pass to reconsider once the catalog is validated.
func (r *repaveStep) Run(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	// crash-safety recovery. Runs first, unconditionally,
	// regardless of catalog state or image observability: if a prior pass
	// recorded a pending delete and was interrupted before DeletePVC
	// succeeded, retry it here rather than depending on SwapVMOSDisk's
	// idempotent no-op to notice there's still cleanup to do. Ordered ahead
	// of the self-heal block below on purpose — DeletePVC doesn't depend on
	// a successful Harvester image observation, so a transient image-read
	// failure there must not block this independent, always-retriable cleanup.
	if pending := inst.Status.Resources.PendingDeleteOSDiskPVCName; pending != "" {
		if err := r.Harvester.DeletePVC(ctx, inst.Namespace, pending); err != nil {
			return Transient(err)
		}
		inst.Status.Resources.PendingDeleteOSDiskPVCName = ""
	}

	// Self-heal CurrentImageRevision from the VM's actual OS-disk PVC: a
	// status write here can be lost to a conflicted patch with nothing else
	// to ever retry it (every other writer is gated behind the one-shot
	// repave-trigger annotation).
	//
	// imageID's name half is the real Harvester object name, not
	// necessarily internal/catalog's ImageName string — on a real cluster,
	// imported images typically get an auto-generated object name and only
	// carry the catalog string as DisplayName, which is why
	// ResolveVMImageDisplayName is needed here rather than comparing
	// imageID's name half against the catalog directly (verified against a
	// real deployment: this exact gap is why a lost status write never
	// self-healed and left the instance stuck on RepaveInProgress/Modifying
	// indefinitely, despite the swap and the database both being healthy).
	if inst.Status.AppliedSpec != nil {
		imageID, err := r.Harvester.GetVMOSDiskImageID(ctx, inst.Namespace, vmNameFor(inst))
		if err != nil {
			return Transient(fmt.Errorf("observe VM OS-disk image: %w", err))
		}
		if imageID != "" {
			if imgNs, imgName, found := strings.Cut(imageID, "/"); found {
				displayName, err := r.Harvester.ResolveVMImageDisplayName(ctx, imgNs, imgName)
				if err != nil {
					return Transient(fmt.Errorf("resolve VM OS-disk image display name: %w", err))
				}
				if rev, ok := catalog.RevisionForImageName(displayName); ok && rev != inst.Status.CurrentImageRevision {
					inst.Status.CurrentImageRevision = rev
				}
			}
		}
	}

	defaults := r.databaseDefaults()
	entry, stream, ok := resolveBakedImage(defaults)
	if !ok {
		// Nothing to compare against — the repave feature no-ops exactly as
		// if the catalog were empty. Report Unknown rather than clearing the
		// condition: any stale True from before the stream became
		// unresolvable must stop looking actionable, but "I could not check"
		// is a misconfiguration and must not masquerade as False/up-to-date.
		inst.SetCurrentCondition(dbaasv1.ConditionImageDrift, metav1.ConditionUnknown,
			dbaasv1.ReasonImageCatalogUnresolved,
			fmt.Sprintf("no validated baked-image stream for osVersion %q — image drift cannot be evaluated",
				defaults.OSVersion))
		return Satisfied()
	}

	// --- drift detection: runs every pass a stream is resolvable, NOT gated
	// on the trigger annotation. One condition, Reason carries which flavor
	// a safe update (ReasonOSUpdateAvailable) vs one blocked
	// by an EOL'd engineVersion (ReasonEngineVersionEOL) vs no drift at all
	// (ReasonImageUpToDate). Always written, never removed: an absent
	// condition would mean Unknown, which is what the unresolvable-stream
	// branch above legitimately reports.
	switch {
	case inst.Status.AppliedSpec != nil && inst.Status.CurrentImageRevision == "":
		inst.SetCurrentCondition(dbaasv1.ConditionImageDrift, metav1.ConditionUnknown,
			dbaasv1.ReasonCurrentImageRevisionUnknown,
			"current image revision has not been observed yet — drift cannot be evaluated")

	case inst.Status.AppliedSpec != nil && inst.Status.CurrentImageRevision != stream.Revision:
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

	default:
		// Also covers the pre-provisioning window (AppliedSpec == nil): the
		// VM has not been created yet, and when it is, ensureVM builds it
		// from this very stream — so there is nothing to update to.
		inst.SetCurrentCondition(dbaasv1.ConditionImageDrift, metav1.ConditionFalse,
			dbaasv1.ReasonImageUpToDate,
			fmt.Sprintf("VM is on the current image revision %q", stream.Revision))
	}

	// --- repave dispatch: gated on the trigger annotation differing from
	// the last value this controller processed. The annotation is never
	// modified; Status.LastAppliedRepaveTrigger is what advances instead. ---
	triggerValue, hasTrigger := inst.Annotations[dbaasv1.AnnotationRepaveTrigger]
	if !hasTrigger || triggerValue == inst.Status.LastAppliedRepaveTrigger {
		return Satisfied()
	}

	// An in-flight repave (RepaveInProgress=True) already halted the VM and
	// therefore already moved Phase off Available by the time this step runs
	// again — only gate the true entry point, not a phase change this step
	// caused itself. Without this, a repave that gets as far as stopping the
	// VM can never finish: the next pass sees its own RepaveInProgress=True
	// having flipped Phase to Modifying, Terminal-aborts on that self-caused
	// change, records the trigger as handled, and leaves the VM halted on
	// the old image with no automatic retry.
	if inst.Status.Phase != dbaasv1.StatusAvailable &&
		!inst.Status.IsConditionTrue(dbaasv1.ConditionRepaveInProgress) {
		msg := "repave requires the instance to be Available"
		inst.Status.LastAppliedRepaveTrigger = triggerValue
		// Every other Terminal branch in this package sets a condition
		// before returning — Result.Reason/Message are otherwise dropped
		// entirely (reconcileInstance only reads ControllerResult/Err), so
		// without this a blocked repave leaves no observable trace beyond
		// LastAppliedRepaveTrigger silently catching up.
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
		inst.Status.LastAppliedRepaveTrigger = triggerValue
		inst.SetCurrentCondition(dbaasv1.ConditionRepaveInProgress, metav1.ConditionFalse, dbaasv1.ReasonRepaveBlockedEOL, msg)
		return Terminal(dbaasv1.ReasonRepaveBlockedEOL, msg)
	}
	if inst.Status.CurrentImageRevision == stream.Revision {
		inst.Status.LastAppliedRepaveTrigger = triggerValue
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
	// ClearDataVolumeOwnerRef/DeleteDataVolume —  drops both as
	// unnecessary for this storage backend.
	oldPVC, newPVC, err := r.Harvester.SwapVMOSDisk(ctx, inst.Namespace, vmNameFor(inst), diskIdentifierFor(inst), entry.ImageName)
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
	// Drift resolved by the swap — report it as evaluated-and-clean, not as
	// unevaluated. syncRepaveInProgressCondition keys off this being no
	// longer True.
	inst.SetCurrentCondition(dbaasv1.ConditionImageDrift, metav1.ConditionFalse,
		dbaasv1.ReasonImageUpToDate,
		fmt.Sprintf("VM is on the current image revision %q", stream.Revision))

	if res, stop := r.regenerateCloudInit(ctx, inst, engineVersion); stop {
		return res
	}

	// Record the trigger as handled and report Pending; the power step
	// (ordered after this one) observes desired-running + declared-Halted
	// and restarts on its own — no manual StartVM call needed here.
	inst.Status.LastAppliedRepaveTrigger = triggerValue
	msg := fmt.Sprintf("applied repave to revision %s", stream.Revision)
	inst.SetCurrentCondition(dbaasv1.ConditionRepaveInProgress, metav1.ConditionTrue, dbaasv1.ReasonRepaveApplied, msg)
	inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonRepaveApplied, msg)
	return Pending(dbaasv1.ReasonRepaveApplied, msg)
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
