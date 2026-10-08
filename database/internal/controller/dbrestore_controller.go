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
	goerrors "errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/backup"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/ensure"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// Restore.Timeout bounds a whole restore (see enforceDeadline); Now is the
// clock, overridable in tests. Recorder emits an event for each status
// transition (finishPass); nil disables events.
type DBRestoreReconciler struct {
	client.Client
	APIReader        client.Reader
	Harvester        harvester.ClientInterface
	Recorder         record.EventRecorder
	DatabaseDefaults operatorconfig.DatabaseDefaults
	Restore          operatorconfig.RestoreConfig
	Now              func() time.Time
}

func (r *DBRestoreReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// deadline is when restore times out: its creation time plus
// restore.timeout. Derived every pass from a fact the API server set and
// nobody can change, never from recorded status. ok is false only for an
// object with no creation time (never the case on a real API server).
func (r *DBRestoreReconciler) deadline(restore *dbaasv1.DBRestore) (time.Time, bool) {
	if restore.CreationTimestamp.IsZero() {
		return time.Time{}, false
	}
	timeout := r.Restore.Timeout
	if timeout <= 0 {
		timeout = operatorconfig.Default().Restore.Timeout
	}
	return restore.CreationTimestamp.Add(timeout), true
}

func (r *DBRestoreReconciler) holds() backup.Holds {
	return backup.Holds{Live: r.APIReader, Writer: r.Client}
}

const (
	restoreSnapshotPollRequeue       = 5 * time.Second
	restoreHoldRequeue               = 5 * time.Second
	restorePVCPollRequeue            = 5 * time.Second
	restoreTargetTeardownPollRequeue = 5 * time.Second
)

// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbrestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbrestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbrestores/finalizers,verbs=update
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbsnapshots,verbs=get;list;watch
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbinstances,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;create;delete
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;delete

// Reconcile is the main entry point called by controller-runtime.
func (r *DBRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	logger := log.FromContext(ctx)
	if r.APIReader == nil {
		return ctrl.Result{}, fmt.Errorf("DBRestoreReconciler needs an uncached APIReader")
	}

	var restore dbaasv1.DBRestore
	if err := r.Get(ctx, req.NamespacedName, &restore); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil // ignore already deleted
		}
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling", "name", restore.Name, "snapshotRef", restore.Spec.SnapshotRef.Name, "targetInstanceName", restore.Spec.TargetInstanceName)

	deleting := !restore.DeletionTimestamp.IsZero()
	hasFinalizer := containsString(restore.Finalizers, dbaasv1.DBRestoreFinalizerName)
	switch {
	case deleting && !hasFinalizer:
		return ctrl.Result{}, nil // cleanup already done
	case !deleting && !hasFinalizer:
		// Nothing is created before the finalizer is in place, so there is
		// nothing yet for finishPass to settle.
		restore.Finalizers = append(restore.Finalizers, dbaasv1.DBRestoreFinalizerName)
		return ctrl.Result{}, r.Update(ctx, &restore)
	}

	before := restore.DeepCopy()
	settled := false
	defer func() {
		// A pass that errors out returns before recording its outcome, which
		// would leave status describing an earlier pass. Say what's actually
		// happening instead (stage and reason stay as last derived).
		if retErr != nil && !restoreFinished(&restore) {
			restore.Status.Message = fmt.Sprintf("retrying after error: %v", retErr)
		}
		stale, err := r.finishPass(ctx, before, &restore, settled)
		retErr = goerrors.Join(retErr, err)
		if stale && retErr == nil {
			result = ctrl.Result{Requeue: true} // re-derive from the object as it is now
		}
	}()

	if deleting {
		result, settled, retErr = r.reconcileDelete(ctx, &restore)
	} else {
		result, settled, retErr = r.reconcileRestore(ctx, &restore)
	}
	return result, retErr
}

// finishPass settles everything a pass must leave consistent, in order:
//  1. The restore hold, r
//  2. Status
//  3. The finalizer
func (r *DBRestoreReconciler) finishPass(ctx context.Context, before, restore *dbaasv1.DBRestore, settled bool) (stale bool, err error) {
	deleting := !restore.DeletionTimestamp.IsZero()
	var errs []error

	holdReleased := false
	if settled {
		if err := r.holds().Release(ctx, restore.Namespace, backup.RestoreHoldName(restore.UID), string(restore.UID)); err != nil {
			errs = append(errs, fmt.Errorf("release restore hold: %w", err))
		} else {
			holdReleased = true
		}
	}

	restore.Status.ObservedGeneration = restore.Generation
	if !equality.Semantic.DeepEqual(before.Status, restore.Status) {
		patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
		err := r.Status().Patch(ctx, restore, patch)
		switch {
		case apierrors.IsConflict(err):
			return true, goerrors.Join(errs...)
		case err != nil && (!deleting || !apierrors.IsNotFound(err)):
			errs = append(errs, fmt.Errorf("patch DBRestore status: %w", err))
		case err == nil && restore.Status.Reason != before.Status.Reason:
			r.recordTransition(restore)
		}
	}

	if deleting && settled && holdReleased {
		if err := r.removeRestoreFinalizer(ctx, client.ObjectKeyFromObject(restore)); err != nil {
			errs = append(errs, fmt.Errorf("remove DBRestore finalizer: %w", err))
		}
	}
	return false, goerrors.Join(errs...)
}

// recordTransition announces the restore's new reason: a Warning for a
// failure, Normal for progress (including success and cancellation).
func (r *DBRestoreReconciler) recordTransition(restore *dbaasv1.DBRestore) {
	if r.Recorder == nil {
		return
	}
	eventType := corev1.EventTypeNormal
	if restore.Status.Stage == dbaasv1.RestoreStageFailed {
		eventType = corev1.EventTypeWarning
	}
	r.Recorder.Event(restore, eventType, restore.Status.Reason, restore.Status.Message)
}

func restoreFinished(restore *dbaasv1.DBRestore) bool {
	return restore.Status.Stage == dbaasv1.RestoreStageSucceeded || restore.Status.Stage == dbaasv1.RestoreStageFailed
}

// reconcileRestore runs one (non-deletion) pass, then applies the restore's
// deadline to its outcome. settled reports that the restore has ended and
// left nothing reading its snapshot.
func (r *DBRestoreReconciler) reconcileRestore(ctx context.Context, restore *dbaasv1.DBRestore) (ctrl.Result, bool, error) {
	// A restore is a one-time operation, like a Job: once it has ended there
	// is nothing further to converge — only its leftovers to settle.
	if !restoreFinished(restore) {
		res, err := r.reconcileRestorePass(ctx, restore)
		res, err = r.enforceDeadline(ctx, restore, res, err)
		if err != nil || !restoreFinished(restore) {
			return res, false, err
		}
	}
	return r.settle(ctx, restore)
}

// settle wraps settleRestorePVC as a pass outcome: a PVC still going away
// is polled, since nothing watched announces its deletion.
func (r *DBRestoreReconciler) settle(ctx context.Context, restore *dbaasv1.DBRestore) (ctrl.Result, bool, error) {
	gone, err := r.settleRestorePVC(ctx, restore)
	switch {
	case err != nil:
		return ctrl.Result{}, false, err
	case !gone:
		return ctrl.Result{RequeueAfter: restorePVCPollRequeue}, false, nil
	}
	return ctrl.Result{}, true, nil
}

// settleRestorePVC deletes an abandoned restore PVC after a live read
// confirms that no target claims it. It only touches PVCs labeled with this
// restore UID. A target that claims the restore owns data-disk cleanup.
// The hold remains until the PVC disappears because it may still be
// copying data from the protected VolumeSnapshot.
func (r *DBRestoreReconciler) settleRestorePVC(ctx context.Context, restore *dbaasv1.DBRestore) (bool, error) {
	if restore.Status.Stage == dbaasv1.RestoreStageSucceeded {
		return true, nil
	}
	pvcName := ensure.RestoreDataVolumeName(restore.Spec.TargetInstanceName, restore.UID)
	pvc, err := r.Harvester.GetPVC(ctx, restore.Namespace, pvcName)
	switch {
	case apierrors.IsNotFound(err):
		return true, nil
	case err != nil:
		return false, err
	case pvc.Labels[dbaasv1.LabelDBRestoreUID] != string(restore.UID):
		return true, nil // not ours to delete, and not reading our snapshot
	}

	// Cached first: a claimant only ever stops us, so trusting a cache
	// that still shows one is safe. Deleting needs the live answer.
	for _, reader := range []client.Reader{r.Client, r.APIReader} {
		target, _, err := r.observeTarget(ctx, reader, restore)
		if err != nil {
			return false, err
		}
		if claimsRestore(target, restore) {
			return true, nil
		}
	}
	if !pvc.DeletionTimestamp.IsZero() {
		return false, nil
	}
	if err := r.Harvester.DeletePVCWithUID(ctx, restore.Namespace, pvcName, pvc.UID); err != nil {
		if apierrors.IsConflict(err) {
			return false, nil // replaced since we read it — re-judged next pass
		}
		return false, err
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(restore, corev1.EventTypeNormal, string(dbaasv1.ReasonRestorePVCDeleted),
			"deleted restore PVC %q: no target took it over", pvcName)
	}
	// An unused PVC usually goes at once; otherwise re-checked next pass.
	if _, err := r.Harvester.GetPVC(ctx, restore.Namespace, pvcName); !apierrors.IsNotFound(err) {
		return false, client.IgnoreNotFound(err)
	}
	return true, nil
}

// claimsRestore reports whether target (nil: none) was created for restore:
// its disk names derive from restore's UID, so it mounts restore's PVC. Wider
// than ownsTarget on purpose — it ignores TargetInstanceUID, so even a
// same-named instance created from a copied manifest protects the PVC it
// would mount.
func claimsRestore(target *dbaasv1.DBInstance, restore *dbaasv1.DBRestore) bool {
	return target != nil && target.Spec.RestoredFrom != nil && target.Spec.RestoredFrom.DBRestoreUID == restore.UID
}

// enforceDeadline applies restore.timeout to the outcome of a pass. It runs
// after the pass has observed everything, so a target that became ready
// right at the deadline counts as a success, not a timeout. An unfinished
// restore past its deadline fails as RestoreTimedOut — even if the pass
// itself errored, so a restore stuck retrying an error still ends — and its
// unfinished target is deleted, like a rejected one. Otherwise the next
// pass is scheduled no later than the deadline: StartingDatabase waits on
// DBInstance events, which a quietly stuck target never sends.
func (r *DBRestoreReconciler) enforceDeadline(ctx context.Context, restore *dbaasv1.DBRestore, res ctrl.Result, passErr error) (ctrl.Result, error) {
	deadline, ok := r.deadline(restore)
	if !ok || restoreFinished(restore) {
		return res, passErr
	}
	restore.Status.Deadline = &metav1.Time{Time: deadline}

	remaining := deadline.Sub(r.now())
	if remaining > 0 {
		if passErr == nil && (res.RequeueAfter == 0 || res.RequeueAfter > remaining) {
			res.RequeueAfter = remaining
		}
		return res, passErr
	}

	// Timed out. Any target this restore created and that isn't ready (a
	// ready one would have made this pass a success) is torn down — but
	// only once confirmed against the API server, never from a cache.
	stuck := fmt.Sprintf("%s: %s", restore.Status.Stage, restore.Status.Reason)
	live, owned, err := r.observeTarget(ctx, r.APIReader, restore)
	if err != nil {
		return ctrl.Result{}, err
	}
	if owned && live.DeletionTimestamp.IsZero() {
		if live.Status.IsConditionTrue(dbaasv1.ConditionReady) {
			return ctrl.Result{Requeue: true}, nil // ready after all — the next pass records success
		}
		if res, err := r.deleteConfirmedTarget(ctx, live); err != nil || res.Requeue {
			return res, err
		}
	}
	timeout := deadline.Sub(restore.CreationTimestamp.Time)
	failRestore(restore, dbaasv1.ReasonRestoreTimedOut,
		fmt.Sprintf("restore did not finish within %s (last stage %s)", timeout, stuck))
	return ctrl.Result{}, nil
}

// deleteConfirmedTarget deletes target, preconditioned on the exact object
// the caller just confirmed against the API server. The delete is durable
// once issued — DBInstanceReconciler's own finalizer finishes it — so the
// caller can mark the restore Failed right away. A conflict means the
// target changed since it was confirmed: requeue and re-judge.
func (r *DBRestoreReconciler) deleteConfirmedTarget(ctx context.Context, target *dbaasv1.DBInstance) (ctrl.Result, error) {
	uid, rv := target.UID, target.ResourceVersion
	if err := r.Delete(ctx, target, client.Preconditions{UID: &uid, ResourceVersion: &rv}); err != nil && !apierrors.IsNotFound(err) {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileRestorePass runs one pass of the (non-deletion) restore. Each
// gate below re-observes what it depends on from the cluster; nothing here
// trusts that an object seen on an earlier pass still exists or is ours.
func (r *DBRestoreReconciler) reconcileRestorePass(ctx context.Context, restore *dbaasv1.DBRestore) (ctrl.Result, error) {

	// 1. Capture the snapshot's inputs, once. Needed first: the hold is
	// labeled with the captured source UID, and the PVC identity check
	// compares against the captured VolumeSnapshot name.
	if restore.Status.SnapshotUID == "" {
		if ok, res, err := r.captureSnapshot(ctx, restore); !ok {
			return res, err
		}
	}

	// 2. Take the hold BEFORE verifying the snapshot and creating the PVC.
	// DBSnapshot deletion checks for restore holds before deleting its
	// backend backup, so verifying after acquiring (lock-then-check) is what
	// closes the window where the snapshot could be deleted between our
	// check and our read.
	acquired, err := r.holds().Acquire(ctx, restore.Namespace, backup.RestoreHoldName(restore.UID), string(restore.UID),
		restoreOwnerRef(restore), map[string]string{backup.SourceUIDLabel: string(restore.Status.SourceInstanceUID)})
	if err != nil {
		return ctrl.Result{}, err
	}
	if !acquired.Acquired {
		setRestoreProgress(restore, dbaasv1.RestoreStageRestoringVolume, dbaasv1.ReasonRestoreHoldWaiting,
			fmt.Sprintf("waiting for %s to release the restore hold", acquired.HolderIdentity))
		return ctrl.Result{RequeueAfter: restoreHoldRequeue}, nil
	}

	// 3. Observe the target DBInstance: cached, confirmed live before
	// failing on it.
	target, owned, err := r.observeTarget(ctx, r.Client, restore)
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason, _ := targetProblem(restore, target, owned); reason != "" {
		if target, owned, err = r.observeTarget(ctx, r.APIReader, restore); err != nil {
			return ctrl.Result{}, err
		}
		if reason, msg := targetProblem(restore, target, owned); reason != "" {
			failRestore(restore, reason, msg)
			return ctrl.Result{}, nil
		}
	}
	if owned && restore.Status.TargetInstanceUID == "" {
		// Created on an earlier pass whose status write didn't land —
		// adopt it via its spec.restoredFrom link.
		restore.Status.TargetInstanceUID = target.UID
	}

	// 4. Observe the restore PVC and verify it's really ours.
	pvcName := ensure.RestoreDataVolumeName(restore.Spec.TargetInstanceName, restore.UID)
	pvc, err := r.Harvester.GetPVC(ctx, restore.Namespace, pvcName)
	switch {
	case apierrors.IsNotFound(err):
		pvc = nil
	case err != nil:
		return ctrl.Result{}, err
	}
	if pvc != nil {
		if !isOurRestorePVC(pvc, restore) {
			return r.failTearingDownTarget(ctx, restore, dbaasv1.ReasonRestorePVCConflict,
				fmt.Sprintf("PVC %q exists but was not created by this restore from VolumeSnapshot %q", pvcName, restore.Status.DataVolumeSnapshotName))
		}
		if !pvc.DeletionTimestamp.IsZero() || pvc.Status.Phase == corev1.ClaimLost {
			return r.failTearingDownTarget(ctx, restore, dbaasv1.ReasonRestorePVCLost,
				fmt.Sprintf("restore PVC %q is lost or being deleted (phase %q)", pvcName, pvc.Status.Phase))
		}
	}
	pvcBound := pvc != nil && pvc.Status.Phase == corev1.ClaimBound

	// 5. While the PVC hasn't finished copying the snapshot's data in, the
	// snapshot is still being read — re-verify it live (now under the hold),
	// down to the VolumeSnapshot itself.
	if !pvcBound {
		if _, ok, res, err := r.readableSnapshot(ctx, restore); !ok {
			return res, err
		}
	}

	// 6. Converge the restore PVC.
	if pvc == nil {
		if owned {
			// The target's VM expects this PVC; recreating it now would race
			// Harvester's VM controller, which would create a blank one.
			return r.failTearingDownTarget(ctx, restore, dbaasv1.ReasonRestorePVCLost,
				fmt.Sprintf("restore PVC %q disappeared after DBInstance %q was created", pvcName, target.Name))
		}
		if err := r.createRestorePVC(ctx, restore, pvcName); err != nil {
			return ctrl.Result{}, err
		}
		setRestoreProgress(restore, dbaasv1.RestoreStageRestoringVolume, dbaasv1.ReasonRestoreVolumeRestoring,
			fmt.Sprintf("created restore PVC %q; waiting for it to become Bound", pvcName))
		return ctrl.Result{RequeueAfter: restorePVCPollRequeue}, nil
	}
	if !pvcBound {
		setRestoreProgress(restore, dbaasv1.RestoreStageRestoringVolume, dbaasv1.ReasonRestoreVolumeRestoring,
			fmt.Sprintf("waiting for restore PVC %q to become Bound (currently %q)", pvcName, pvc.Status.Phase))
		return ctrl.Result{RequeueAfter: restorePVCPollRequeue}, nil
	}

	// 7. Converge the target DBInstance.
	if target == nil {
		created, err := r.createTarget(ctx, restore)
		if apierrors.IsInvalid(err) {
			// The spec is immutable and its inputs frozen, so the same
			// rejection would repeat forever.
			failRestore(restore, dbaasv1.ReasonRestoreTargetInvalid,
				fmt.Sprintf("the API server rejected DBInstance %q: %v", restore.Spec.TargetInstanceName, err))
			return ctrl.Result{}, nil
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		restore.Status.TargetInstanceUID = created.UID
		setRestoreProgress(restore, dbaasv1.RestoreStageStartingDatabase, dbaasv1.ReasonRestoreTargetStarting,
			fmt.Sprintf("created DBInstance %q; waiting for it to become ready", created.Name))
		return ctrl.Result{}, nil // the DBInstance watch re-triggers us
	}
	return r.observeTargetReadiness(ctx, restore, target)
}

// captureSnapshot resolves spec.snapshotRef, validates it, and
// captures the inputs later passes need. Captured inputs are frozen on
// purpose; the snapshot itself is still re-verified live (readableSnapshot)
// for as long as the restore depends on it.
func (r *DBRestoreReconciler) captureSnapshot(ctx context.Context, restore *dbaasv1.DBRestore) (ok bool, res ctrl.Result, err error) {
	snap, ok, res, err := r.readableSnapshot(ctx, restore)
	if !ok {
		return false, res, err
	}
	if snap.Status.Source == nil {
		failRestore(restore, dbaasv1.ReasonRestoreInvalidSnapshotState,
			fmt.Sprintf("DBSnapshot %q is Ready but has no captured source metadata", snap.Name))
		return false, ctrl.Result{}, nil
	}
	// The target's guest verifies the restored cluster against exactly these
	// values, so an empty one would only surface as a VM that never becomes
	// ready. Fail here, with a reason, before creating anything.
	if src := snap.Status.Source; src.DBName == "" || src.MasterUsername == "" || src.EngineVersion == "" {
		failRestore(restore, dbaasv1.ReasonRestoreInvalidSnapshotState,
			fmt.Sprintf("DBSnapshot %q does not record the source's effective dbName/masterUsername/engineVersion (got %q/%q/%q); take a new snapshot",
				snap.Name, src.DBName, src.MasterUsername, src.EngineVersion))
		return false, ctrl.Result{}, nil
	}
	if restore.Spec.AllocatedStorage < snap.Status.Source.AllocatedStorage {
		failRestore(restore, dbaasv1.ReasonRestoreAllocatedStorageTooSmall,
			fmt.Sprintf("allocatedStorage %d is smaller than the snapshot's recorded size %d", restore.Spec.AllocatedStorage, snap.Status.Source.AllocatedStorage))
		return false, ctrl.Result{}, nil
	}

	restore.Status.SnapshotUID = string(snap.UID)
	restore.Status.SourceInstanceUID = snap.Status.Source.InstanceUID
	restore.Status.SourceInstanceName = snap.Spec.SourceInstanceRef.Name
	restore.Status.DataVolumeSnapshotName = snap.Status.DataVolumeSnapshotName
	restore.Status.Resolved = &dbaasv1.ResolvedRestoreFields{
		DBName:         snap.Status.Source.DBName,
		MasterUsername: snap.Status.Source.MasterUsername,
		EngineVersion:  snap.Status.Source.EngineVersion,
		Port:           snap.Status.Source.Port,
		StorageType:    snap.Status.Source.StorageType,
	}
	return true, ctrl.Result{}, nil
}

// readableSnapshot reports whether spec.snapshotRef can be read from right
// now: it exists, is the same object admission captured (not a same-named
// replacement), isn't being deleted, and is Ready — and so is the
// VolumeSnapshot the restore PVC actually reads from. The DBSnapshot's Ready
// condition was recorded when its backup completed and is never re-checked
// by DBSnapshotReconciler, so it alone can't prove the data is still there.
// ok=false: restore.Status is already set (Failed, or waiting with res set),
// or err is a transient read error.
func (r *DBRestoreReconciler) readableSnapshot(ctx context.Context, restore *dbaasv1.DBRestore) (*dbaasv1.DBSnapshot, bool, ctrl.Result, error) {
	snap, err := r.getSnapshot(ctx, r.Client, restore)
	if err != nil {
		return nil, false, ctrl.Result{}, err
	}
	reason, msg, terminal := snapshotProblem(restore, snap)
	if terminal {
		// Would fail the restore — confirm against the API server first.
		if snap, err = r.getSnapshot(ctx, r.APIReader, restore); err != nil {
			return nil, false, ctrl.Result{}, err
		}
		reason, msg, terminal = snapshotProblem(restore, snap)
	}
	switch {
	case terminal:
		failRestore(restore, reason, msg)
		return nil, false, ctrl.Result{}, nil
	case reason != "":
		setRestoreProgress(restore, dbaasv1.RestoreStagePreparing, reason, msg)
		return nil, false, ctrl.Result{RequeueAfter: restoreSnapshotPollRequeue}, nil
	}
	return r.readableVolumeSnapshot(ctx, restore, snap)
}

// getSnapshot reads spec.snapshotRef through reader; nil when not found.
func (r *DBRestoreReconciler) getSnapshot(ctx context.Context, reader client.Reader, restore *dbaasv1.DBRestore) (*dbaasv1.DBSnapshot, error) {
	snap := &dbaasv1.DBSnapshot{}
	if err := reader.Get(ctx, types.NamespacedName{Namespace: restore.Namespace, Name: restore.Spec.SnapshotRef.Name}, snap); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return snap, nil
}

// snapshotProblem reports why snap (nil: not found) can't be read from right
// now — reason "" means it can. terminal separates a permanent problem from
// one worth waiting on. A pure function of what was observed, so the same
// judgment runs on the cached and the confirming read.
func snapshotProblem(restore *dbaasv1.DBRestore, snap *dbaasv1.DBSnapshot) (reason dbaasv1.ConditionReason, msg string, terminal bool) {
	name := restore.Spec.SnapshotRef.Name
	switch {
	case snap == nil:
		return dbaasv1.ReasonRestoreSnapshotNotFound, fmt.Sprintf("DBSnapshot %q not found", name), true
	case restore.Status.SnapshotUID != "" && string(snap.UID) != restore.Status.SnapshotUID:
		return dbaasv1.ReasonRestoreSnapshotReplaced,
			fmt.Sprintf("DBSnapshot %q was replaced by a different object (UID %s, captured %s)", name, snap.UID, restore.Status.SnapshotUID), true
	case !snap.DeletionTimestamp.IsZero():
		return dbaasv1.ReasonRestoreSnapshotDeleting, fmt.Sprintf("DBSnapshot %q is being deleted", name), true
	}
	cond := snap.Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	switch {
	case cond != nil && cond.Status == metav1.ConditionTrue:
		return "", "", false
	case cond != nil && isTerminalSnapshotReason(dbaasv1.ConditionReason(cond.Reason)):
		return dbaasv1.ReasonRestoreSnapshotFailed, fmt.Sprintf("DBSnapshot %q will never become ready: %s", name, cond.Message), true
	default:
		return dbaasv1.ReasonRestoreSnapshotNotReady, fmt.Sprintf("waiting for DBSnapshot %q to become ready", name), false
	}
}

// readableVolumeSnapshot checks the VolumeSnapshot a Ready DBSnapshot points
// at, live. Missing, being deleted, or errored is permanent; not yet
// ready-to-use is waited on.
func (r *DBRestoreReconciler) readableVolumeSnapshot(ctx context.Context, restore *dbaasv1.DBRestore, snap *dbaasv1.DBSnapshot) (*dbaasv1.DBSnapshot, bool, ctrl.Result, error) {
	name := restore.Status.DataVolumeSnapshotName
	if name == "" {
		name = snap.Status.DataVolumeSnapshotName
	}
	if name == "" {
		failRestore(restore, dbaasv1.ReasonRestoreInvalidSnapshotState,
			fmt.Sprintf("DBSnapshot %q is Ready but records no data-volume VolumeSnapshot", snap.Name))
		return nil, false, ctrl.Result{}, nil
	}

	vs, err := r.Harvester.GetVolumeSnapshotState(ctx, restore.Namespace, name)
	switch {
	case apierrors.IsNotFound(err):
		failRestore(restore, dbaasv1.ReasonRestoreVolumeSnapshotMissing,
			fmt.Sprintf("VolumeSnapshot %q behind DBSnapshot %q no longer exists", name, snap.Name))
		return nil, false, ctrl.Result{}, nil
	case err != nil:
		return nil, false, ctrl.Result{}, err
	case vs.Deleting:
		failRestore(restore, dbaasv1.ReasonRestoreVolumeSnapshotMissing,
			fmt.Sprintf("VolumeSnapshot %q behind DBSnapshot %q is being deleted", name, snap.Name))
		return nil, false, ctrl.Result{}, nil
	case vs.ErrorMessage != "":
		failRestore(restore, dbaasv1.ReasonRestoreVolumeSnapshotFailed,
			fmt.Sprintf("VolumeSnapshot %q behind DBSnapshot %q reports an error: %s", name, snap.Name, vs.ErrorMessage))
		return nil, false, ctrl.Result{}, nil
	case !vs.ReadyToUse:
		setRestoreProgress(restore, dbaasv1.RestoreStagePreparing, dbaasv1.ReasonRestoreSnapshotNotReady,
			fmt.Sprintf("waiting for VolumeSnapshot %q behind DBSnapshot %q to be ready to use", name, snap.Name))
		return nil, false, ctrl.Result{RequeueAfter: restoreSnapshotPollRequeue}, nil
	}
	return snap, true, ctrl.Result{}, nil
}

// observeTarget reads the target DBInstance through reader. owned means it
// is the one this restore created: its immutable spec.restoredFrom names this
// DBRestore's UID and, once recorded, its UID matches TargetInstanceUID (a
// same-named instance recreated by someone else, even from a copied
// manifest, is not ours).
func (r *DBRestoreReconciler) observeTarget(ctx context.Context, reader client.Reader, restore *dbaasv1.DBRestore) (target *dbaasv1.DBInstance, owned bool, err error) {
	target = &dbaasv1.DBInstance{}
	if getErr := reader.Get(ctx, types.NamespacedName{Namespace: restore.Namespace, Name: restore.Spec.TargetInstanceName}, target); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			return nil, false, nil
		}
		return nil, false, getErr
	}
	return target, ownsTarget(restore, target), nil
}

func ownsTarget(restore *dbaasv1.DBRestore, target *dbaasv1.DBInstance) bool {
	return target.Spec.RestoredFrom != nil && target.Spec.RestoredFrom.DBRestoreUID == restore.UID &&
		(restore.Status.TargetInstanceUID == "" || restore.Status.TargetInstanceUID == target.UID)
}

// targetProblem reports whether the observed target state means the restore
// can never proceed (reason "" means it can). Pure, like snapshotProblem.
func targetProblem(restore *dbaasv1.DBRestore, target *dbaasv1.DBInstance, owned bool) (dbaasv1.ConditionReason, string) {
	name := restore.Spec.TargetInstanceName
	switch {
	case restore.Status.TargetInstanceUID != "" && !owned:
		// We created a target once; it's gone (or the name now belongs to
		// something else). Never recreate it.
		return dbaasv1.ReasonRestoreTargetLost, fmt.Sprintf("DBInstance %q created by this restore no longer exists", name)
	case target != nil && !owned:
		return dbaasv1.ReasonRestoreTargetNameConflict, fmt.Sprintf("a DBInstance named %q already exists and was not created by this DBRestore", name)
	case owned && !target.DeletionTimestamp.IsZero():
		return dbaasv1.ReasonRestoreTargetLost, fmt.Sprintf("DBInstance %q created by this restore is being deleted", name)
	}
	return "", ""
}

func targetRejected(target *dbaasv1.DBInstance) bool {
	return target.Status.IsCurrentConditionFalse(dbaasv1.ConditionAccepted, target.Generation)
}

// isOurRestorePVC re-verifies a PVC under the expected name is the one this
// restore created: our label, and restoring from our captured VolumeSnapshot.
// A blank PVC (e.g. one Harvester's VM controller recreated from the
// volumeClaimTemplates annotation after ours was deleted) fails this.
func isOurRestorePVC(pvc *corev1.PersistentVolumeClaim, restore *dbaasv1.DBRestore) bool {
	ds := pvc.Spec.DataSource
	return pvc.Labels[dbaasv1.LabelDBRestoreUID] == string(restore.UID) &&
		ds != nil && ds.Kind == "VolumeSnapshot" && ds.Name == restore.Status.DataVolumeSnapshotName
}

// createRestorePVC creates the restore PVC under the name the target's own
// ensureVM will compute (ensure.RestoreDataVolumeName).
//
// No owner reference, deliberately: this PVC becomes the target's live data
// volume and outlives the DBRestore, and Harvester's VM controller never
// re-owners a PVC it finds already existing — owning it by the DBRestore
// would make deleting the DBRestore record cascade-delete the live database.
func (r *DBRestoreReconciler) createRestorePVC(ctx context.Context, restore *dbaasv1.DBRestore, pvcName string) error {
	storageClass := restore.Status.Resolved.StorageType
	if storageClass == "" {
		storageClass = r.DatabaseDefaults.StorageClass
	}
	return r.Harvester.CreateRestorePVC(ctx, restore.Namespace, pvcName, restore.Status.DataVolumeSnapshotName,
		restore.Spec.AllocatedStorage, storageClass, map[string]string{dbaasv1.LabelDBRestoreUID: string(restore.UID)})
}

// createTarget creates the target DBInstance with spec.restoredFrom set in
// the same Create() call. AlreadyExists
// is returned as an error: the next pass re-observes whoever holds the name
// and decides from that, rather than assuming the winner was us.
func (r *DBRestoreReconciler) createTarget(ctx context.Context, restore *dbaasv1.DBRestore) (*dbaasv1.DBInstance, error) {
	target := &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: restore.Spec.TargetInstanceName, Namespace: restore.Namespace},
		Spec: dbaasv1.DBInstanceSpec{
			DBInstanceClass:  restore.Spec.DBInstanceClass,
			EngineVersion:    restore.Status.Resolved.EngineVersion,
			DBName:           restore.Status.Resolved.DBName,
			Port:             restore.Status.Resolved.Port,
			MasterUsername:   restore.Status.Resolved.MasterUsername,
			AllocatedStorage: restore.Spec.AllocatedStorage,
			StorageType:      restore.Status.Resolved.StorageType,
			NetworkRef:       restore.Spec.NetworkRef,
			StaticNetwork:    restore.Spec.StaticNetwork,
			Backup:           restore.Spec.Backup,
			VMPassword:       restore.Spec.VMPassword,
			RestoredFrom: &dbaasv1.RestoredFromRef{
				DBRestoreName:      restore.Name,
				DBRestoreUID:       restore.UID,
				DBSnapshotName:     restore.Spec.SnapshotRef.Name,
				DBSnapshotUID:      types.UID(restore.Status.SnapshotUID),
				SourceInstanceName: restore.Status.SourceInstanceName,
				SourceInstanceUID:  restore.Status.SourceInstanceUID,
			},
		},
	}
	if err := r.Create(ctx, target); err != nil {
		return nil, err
	}
	return target, nil
}

// observeTargetReadiness derives the outcome from the target's live
// conditions — never its Status.Phase, which DerivePhaseSummary documents
// as a projection that must not gate reconciliation. This controller never
// duplicates DBInstanceReconciler's own spec validation; a permanent
// rejection is observed here, as a current-generation Accepted=False.
func (r *DBRestoreReconciler) observeTargetReadiness(ctx context.Context, restore *dbaasv1.DBRestore, target *dbaasv1.DBInstance) (ctrl.Result, error) {
	switch {
	case target.Status.IsConditionTrue(dbaasv1.ConditionReady):
		restore.Status.Stage = dbaasv1.RestoreStageSucceeded
		restore.Status.Reason = string(dbaasv1.ReasonRestoreSucceeded)
		restore.Status.Message = fmt.Sprintf("DBInstance %q is ready", target.Name)
		return ctrl.Result{}, nil

	case targetRejected(target):
		// Deleting is irreversible, so confirm the rejection against the API
		// server first: a stale cached copy could predate a spec fix.
		live, owned, err := r.observeTarget(ctx, r.APIReader, restore)
		if err != nil {
			return ctrl.Result{}, err
		}
		if live == nil || !owned || !live.DeletionTimestamp.IsZero() || !targetRejected(live) {
			return ctrl.Result{Requeue: true}, nil // re-derive next pass from fresh state
		}
		// Tear the target down; the DBRestore survives as the
		// diagnostic record.
		if res, err := r.deleteConfirmedTarget(ctx, live); err != nil || res.Requeue {
			return res, err
		}
		accepted := live.Status.GetCondition(dbaasv1.ConditionAccepted)
		failRestore(restore, dbaasv1.ReasonRestoreTargetRejected,
			fmt.Sprintf("DBInstance %q permanently rejected its parameters: %s", target.Name, accepted.Message))
		return ctrl.Result{}, nil

	default:
		setRestoreProgress(restore, dbaasv1.RestoreStageStartingDatabase, dbaasv1.ReasonRestoreTargetStarting,
			fmt.Sprintf("waiting for DBInstance %q to become ready", target.Name))
		return ctrl.Result{}, nil // the DBInstance watch re-triggers us
	}
}

// reconcileDelete cancels an unfinished restore and cleans up its target
// and PVC before releasing holds and removing the finalizer.
//
// A ready target is preserved, even if Succeeded was never recorded. Live
// state and deletion preconditions protect targets that become ready or
// change identity between observation and deletion.
func (r *DBRestoreReconciler) reconcileDelete(ctx context.Context, restore *dbaasv1.DBRestore) (ctrl.Result, bool, error) {
	// Uncached: every outcome here is irreversible — deleting the target, or
	// declaring teardown done and removing the finalizer — and a stale cache
	// could miss a target that just became Ready, or one just created.
	// Deletion is a once-per-DBRestore event, so this costs little.
	target, owned, err := r.observeTarget(ctx, r.APIReader, restore)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	if !owned {
		return r.settle(ctx, restore) // no target of ours — only the PVC may be left
	}

	liveReady := target.DeletionTimestamp.IsZero() && target.Status.IsConditionTrue(dbaasv1.ConditionReady)
	if restore.Status.Stage == dbaasv1.RestoreStageSucceeded || liveReady {
		if liveReady && restore.Status.Stage != dbaasv1.RestoreStageSucceeded {
			restore.Status.Stage = dbaasv1.RestoreStageSucceeded
			restore.Status.Reason = string(dbaasv1.ReasonRestoreSucceeded)
			restore.Status.Message = fmt.Sprintf("DBInstance %q is ready", target.Name)
		}
		return ctrl.Result{}, true, nil
	}

	if target.DeletionTimestamp.IsZero() {
		uid, rv := target.UID, target.ResourceVersion
		if err := r.Delete(ctx, target, client.Preconditions{UID: &uid, ResourceVersion: &rv}); err != nil && !apierrors.IsNotFound(err) {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, false, nil
			}
			return ctrl.Result{}, false, err
		}
	}
	restore.Status.Reason = string(dbaasv1.ReasonRestoreCancelling)
	restore.Status.Message = fmt.Sprintf("restore cancelled; waiting for DBInstance %q to be torn down", target.Name)
	return ctrl.Result{RequeueAfter: restoreTargetTeardownPollRequeue}, false, nil
}

func setRestoreProgress(restore *dbaasv1.DBRestore, stage string, reason dbaasv1.ConditionReason, msg string) {
	restore.Status.Stage = stage
	restore.Status.Reason = string(reason)
	restore.Status.Message = msg
}

// failTearingDownTarget fails the restore after deleting the unfinished
// target it created, as the timeout and rejection paths do: a
// target whose restore PVC is lost or not ours can never become a usable
// restore, and nothing else would delete it, or the disks it holds. The
// target is confirmed live first. One that is already Ready means the
// restore succeeded, so that is recorded instead and the target is never
// touched — requeueing for it, as the timeout path does, would only hit
// this same failure again before the readiness check.
func (r *DBRestoreReconciler) failTearingDownTarget(ctx context.Context, restore *dbaasv1.DBRestore, reason dbaasv1.ConditionReason, msg string) (ctrl.Result, error) {
	live, owned, err := r.observeTarget(ctx, r.APIReader, restore)
	if err != nil {
		return ctrl.Result{}, err
	}
	if owned && live.DeletionTimestamp.IsZero() {
		if live.Status.IsConditionTrue(dbaasv1.ConditionReady) {
			return r.observeTargetReadiness(ctx, restore, live)
		}
		if res, err := r.deleteConfirmedTarget(ctx, live); err != nil || res.Requeue {
			return res, err
		}
	}
	failRestore(restore, reason, msg)
	return ctrl.Result{}, nil
}

func failRestore(restore *dbaasv1.DBRestore, reason dbaasv1.ConditionReason, msg string) {
	setRestoreProgress(restore, dbaasv1.RestoreStageFailed, reason, msg)
}

func restoreOwnerRef(restore *dbaasv1.DBRestore) *metav1.OwnerReference {
	controller := true
	return &metav1.OwnerReference{
		APIVersion:         dbaasv1.GroupVersion.String(),
		Kind:               "DBRestore",
		Name:               restore.Name,
		UID:                restore.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &controller,
	}
}

// removeRestoreFinalizer re-fetches on every retry to avoid combining a
// fresh resourceVersion with a stale object — mirrors removeSnapshotFinalizer.
func (r *DBRestoreReconciler) removeRestoreFinalizer(ctx context.Context, key client.ObjectKey) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &dbaasv1.DBRestore{}
		if err := r.Get(ctx, key, latest); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if !containsString(latest.Finalizers, dbaasv1.DBRestoreFinalizerName) {
			return nil
		}
		latest.Finalizers = removeString(latest.Finalizers, dbaasv1.DBRestoreFinalizerName)
		return r.Update(ctx, latest)
	})
}

// SetupWithManager wires DBRestore into the manager. Watching DBInstance
// lets a target's status change promptly re-reconcile its DBRestore;
// mapInstanceToRestore needs no List/field index, since the target names its
// DBRestore directly via spec.restoredFrom.
func (r *DBRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("dbaas-controller")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbaasv1.DBRestore{}).
		Watches(&dbaasv1.DBInstance{}, handler.EnqueueRequestsFromMapFunc(r.mapInstanceToRestore)).
		Named("dbrestore").
		Complete(r)
}

func (r *DBRestoreReconciler) mapInstanceToRestore(_ context.Context, obj client.Object) []reconcile.Request {
	inst, ok := obj.(*dbaasv1.DBInstance)
	if !ok || inst.Spec.RestoredFrom == nil {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: inst.Namespace, Name: inst.Spec.RestoredFrom.DBRestoreName}}}
}
