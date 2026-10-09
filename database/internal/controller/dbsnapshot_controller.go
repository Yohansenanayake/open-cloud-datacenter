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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/backup"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/ensure"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// DBSnapshotReconciler creates and tracks manual and automated backups.
// Creation validates the source, waits for a dispatcher-granted backup slot,
// acquires a hold, and checks live source state before starting the backup.
//
// APIReader provides uncached Lease reads for deletion safety. DatabaseDefaults
// resolves source settings absent from AppliedSpec. SlotNamespace is required
// outside unit tests; Wake receives dispatcher notifications. Backup.Timeout
// bounds execution, and Now supplies the clock.
type DBSnapshotReconciler struct {
	client.Client
	APIReader        client.Reader
	Harvester        harvester.ClientInterface
	Recorder         record.EventRecorder // nil disables events
	DatabaseDefaults operatorconfig.DatabaseDefaults
	Backup           operatorconfig.BackupConfig
	SlotNamespace    string
	Wake             <-chan event.GenericEvent
	Now              func() time.Time
}

func (r *DBSnapshotReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *DBSnapshotReconciler) slotsEnabled() bool { return r.SlotNamespace != "" }

func (r *DBSnapshotReconciler) slots() backup.Slots {
	return backup.Slots{Live: r.APIReader, Writer: r.Client, Namespace: r.SlotNamespace}
}

// releaseSlot frees this snapshot's backup slot, if it holds one. Safe on
// any path: holding none is a no-op.
func (r *DBSnapshotReconciler) releaseSlot(ctx context.Context, snap *dbaasv1.DBSnapshot) error {
	if !r.slotsEnabled() {
		return nil
	}
	return r.slots().Release(ctx, snap.UID)
}

func (r *DBSnapshotReconciler) holds() backup.Holds {
	return backup.Holds{Live: r.APIReader, Writer: r.Client}
}

const (
	snapshotHoldRequeue       = 5 * time.Second
	snapshotBackupPollRequeue = 10 * time.Second
	// snapshotQueuedRequeue is only a backstop: a queued snapshot is woken
	// by the dispatcher the moment it's granted a slot.
	snapshotQueuedRequeue     = time.Minute
	snapshotSourceInstanceIdx = ".spec.sourceInstanceRef.name"
)

// snapshotSourceIndexFunc backs snapshotSourceInstanceIdx — shared by the
// manager's real field indexer (SetupWithManager) and by the scheduler's
// retention pruning, which lists a source's own DBSnapshots the same way.
func snapshotSourceIndexFunc(obj client.Object) []string {
	snap, ok := obj.(*dbaasv1.DBSnapshot)
	if !ok || snap.Spec.SourceInstanceRef.Name == "" {
		return nil
	}
	return []string{snap.Spec.SourceInstanceRef.Name}
}

// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbsnapshots,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbsnapshots/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbsnapshots/finalizers,verbs=update
// +kubebuilder:rbac:groups=harvesterhci.io,resources=virtualmachinebackups,verbs=get;list;watch;create;update;delete

// Reconcile is the main entry point called by controller-runtime.
func (r *DBSnapshotReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var snap dbaasv1.DBSnapshot
	if err := r.Get(ctx, req.NamespacedName, &snap); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil // ignore already deleted
		}
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling", "name", snap.Name, "sourceInstanceRef", snap.Spec.SourceInstanceRef.Name)

	if !snap.DeletionTimestamp.IsZero() {
		if !containsString(snap.Finalizers, dbaasv1.DBSnapshotFinalizerName) {
			return ctrl.Result{}, nil // cleanup already done
		}
		return r.reconcileDelete(ctx, &snap)
	}

	if !containsString(snap.Finalizers, dbaasv1.DBSnapshotFinalizerName) {
		snap.Finalizers = append(snap.Finalizers, dbaasv1.DBSnapshotFinalizerName)
		if err := r.Update(ctx, &snap); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	return r.reconcileCreate(ctx, &snap)
}

// reconcileCreate starts the backend backup if it doesn't exist yet
// (startBackup), then tracks it to a terminal state (trackBackup). Whether
// it has started is the live VMBackup's existence, never a recorded field.
// Admission gates only the start: once a backup exists it is tracked to its
// end whatever the source's phase — a source that starts deleting waits for
// it (DBInstanceReconciler.settleSnapshotHold), and rejecting it midway would
// strand its hold. Terminal outcomes aren't retried: diagnose, then create a
// new DBSnapshot rather than resurrecting this one.
func (r *DBSnapshotReconciler) reconcileCreate(ctx context.Context, snap *dbaasv1.DBSnapshot) (ctrl.Result, error) {
	if cond := snap.Status.GetCondition(dbaasv1.ConditionSnapshotReady); cond != nil {
		if cond.Status == metav1.ConditionTrue || isTerminalSnapshotReason(dbaasv1.ConditionReason(cond.Reason)) {
			return ctrl.Result{}, nil // steady state or rejected — nothing left to do
		}
	}

	var source dbaasv1.DBInstance
	err := r.Get(ctx, types.NamespacedName{Namespace: snap.Namespace, Name: snap.Spec.SourceInstanceRef.Name}, &source)
	switch {
	case apierrors.IsNotFound(err) || (err == nil && snap.Status.Source != nil && snap.Status.Source.InstanceUID != source.UID):
		// Gone, or replaced by a same-named instance this snapshot never
		// admitted. Its hold (if any) is owned by the old source, so the
		// garbage collector removes it with that source.
		return r.rejectSnapshot(ctx, snap, dbaasv1.ReasonSnapshotSourceNotFound,
			fmt.Sprintf("source DBInstance %q not found", snap.Spec.SourceInstanceRef.Name))
	case err != nil:
		return ctrl.Result{}, err
	}

	holder := snapshotHolderIdentity(snap)
	status, err := r.Harvester.GetVMBackupStatus(ctx, snap.Namespace, snap.Name, source.Status.Resources.DataVolumeName)
	if apierrors.IsNotFound(err) {
		if started, res, err := r.startBackup(ctx, snap, &source, holder); !started {
			return res, err
		}
		status, err = r.Harvester.GetVMBackupStatus(ctx, snap.Namespace, snap.Name, source.Status.Resources.DataVolumeName)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if snap.Status.Source == nil {
		// The backup exists but the pass that started it didn't get to
		// record what it captured.
		snap.Status.Source = sourceMetadataFrom(&source, r.DatabaseDefaults)
	}
	// Display only: when the backup started (queue time excluded) and how
	// far along it is, both read off the backup itself.
	if snap.Status.StartTime == nil && !status.CreatedAt.IsZero() {
		snap.Status.StartTime = &metav1.Time{Time: status.CreatedAt}
	}
	snap.Status.Progress = int32(min(max(status.Progress, 0), 100))
	return r.trackBackup(ctx, snap, &source, holder, status)
}

// startBackup runs admission, waits for a backup slot, takes the snapshot
// hold, confirms the source live under it, and creates the backend backup.
// started=false: return (res, err) as-is — rejected, queued, waiting on the
// hold, or a transient error.
func (r *DBSnapshotReconciler) startBackup(ctx context.Context, snap *dbaasv1.DBSnapshot, source *dbaasv1.DBInstance, holder string) (started bool, res ctrl.Result, err error) {
	if admitted, res, err := r.admitSnapshot(ctx, snap, source); !admitted {
		return false, res, err
	}
	// Only the dispatcher grants slots; a snapshot only asks whether it
	// holds one (live), so it never decides about shared capacity itself.
	if r.slotsEnabled() {
		slot, err := r.slots().HeldBy(ctx, snap.UID)
		if err != nil {
			return false, ctrl.Result{}, err
		}
		if slot == nil {
			res, err := r.setReady(ctx, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotBackupQueued,
				"waiting for a backup slot", ctrl.Result{RequeueAfter: snapshotQueuedRequeue})
			return false, res, err
		}
	}
	if acquired, res, err := r.acquireHold(ctx, snap, source, holder); !acquired {
		return false, res, err
	}
	// Check-then-lock: source deletion checks for this hold before deleting
	// the VM, so confirming the source isn't being deleted *after* taking it
	// is what keeps a backup from starting on a VM teardown is about to
	// delete. Live, and once per snapshot — only before the backup exists.
	live, rejected, res, err := r.confirmSourceLive(ctx, snap, source, holder)
	if rejected || err != nil {
		return false, res, err
	}
	// Captured from that same live read, under the hold — repave and resize
	// can't change the source between here and the backup — and never
	// overwritten once the backup exists. Restore reads only this, never the
	// live source, so it must describe what was actually backed up.
	snap.Status.Source = sourceMetadataFrom(live, r.DatabaseDefaults)
	if err := r.Harvester.CreateVMBackup(ctx, snap.Namespace, snap.Name, source.Status.Resources.VMName, snapshotOwnerRef(snap)); err != nil {
		return false, ctrl.Result{}, err
	}
	return true, ctrl.Result{}, nil
}

// admitSnapshot validates backup capability and readiness of the cached source.
// admitted=false: return (res, err) as-is — the snapshot was rejected.
func (r *DBSnapshotReconciler) admitSnapshot(ctx context.Context, snap *dbaasv1.DBSnapshot, source *dbaasv1.DBInstance) (admitted bool, res ctrl.Result, err error) {
	// no spec.backup means no backup capability at all, and presence
	// is immutable — this will never resolve on its own.
	if source.Spec.Backup == nil {
		res, err = r.rejectSnapshot(ctx, snap, dbaasv1.ReasonSnapshotSourceBackupDisabled,
			fmt.Sprintf("source DBInstance %q does not have backup capability enabled (spec.backup is absent)", source.Name))
		return false, res, err
	}

	// A source being deleted is about to lose its VM. startBackup
	// re-checks this live, under the hold.
	if !source.DeletionTimestamp.IsZero() {
		res, err = r.rejectSnapshot(ctx, snap, dbaasv1.ReasonSnapshotSourceDeleting,
			fmt.Sprintf("source DBInstance %q is being deleted", source.Name))
		return false, res, err
	}

	// Snapshot requests require an available source at admission.
	if source.Status.Phase != dbaasv1.StatusAvailable {
		res, err = r.rejectSnapshot(ctx, snap, dbaasv1.ReasonSnapshotSourceNotReady,
			fmt.Sprintf("source DBInstance is %q, not available", source.Status.Phase))
		return false, res, err
	}

	return true, ctrl.Result{}, nil
}

// sourceMetadataFrom records the source's *effective* settings — what it
// actually runs with — not its spec as written. A restore inherits these
// verbatim; recording a defaulted field as empty would make the target apply
// its own default instead (e.g. dbName defaulting to the *target's* name, a
// database the restored disk doesn't contain).
func sourceMetadataFrom(source *dbaasv1.DBInstance, defaults operatorconfig.DatabaseDefaults) *dbaasv1.SourceMetadata {
	eff := ensure.EffectiveSettingsFor(source, defaults)
	return &dbaasv1.SourceMetadata{
		InstanceUID:      source.UID,
		DBName:           eff.DBName,
		MasterUsername:   eff.MasterUsername,
		EngineVersion:    eff.EngineVersion,
		Port:             eff.Port,
		StorageType:      eff.StorageType,
		AllocatedStorage: source.Spec.AllocatedStorage,
		ImageRevision:    source.Status.CurrentImageRevision,
		DBInstanceClass:  source.Spec.DBInstanceClass,
		NetworkRef:       source.Spec.NetworkRef,
		Backup:           source.Spec.Backup.DeepCopy(),
	}
}

// acquireHold acquires the snapshot-hold lease shared with repave for
// holder. acquired=false: return (res, err) as-is — held by another (err
// nil) or a transient lease-check failure.
func (r *DBSnapshotReconciler) acquireHold(ctx context.Context, snap *dbaasv1.DBSnapshot, source *dbaasv1.DBInstance, holder string) (acquired bool, res ctrl.Result, err error) {
	result, acqErr := r.holds().Acquire(ctx, source.Namespace, backup.SnapshotHoldName(source.UID), holder, instanceOwnerRef(source), nil)
	if acqErr != nil {
		return false, ctrl.Result{}, acqErr
	}
	if !result.Acquired {
		// A slot is no use while the instance is busy: give it back, so
		// another snapshot can run meanwhile. Order is by creation time, so
		// this one keeps its place.
		if err := r.releaseSlot(ctx, snap); err != nil {
			return false, ctrl.Result{}, err
		}
		msg := fmt.Sprintf("waiting for %s to finish before this snapshot can start", result.HolderIdentity)
		res, err = r.setReady(ctx, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotHoldWaiting, msg, ctrl.Result{RequeueAfter: snapshotHoldRequeue})
		return false, res, err
	}
	return true, ctrl.Result{}, nil
}

// trackBackup follows an existing backend backup to a terminal state,
// releasing the hold only once it's actually settled.
func (r *DBSnapshotReconciler) trackBackup(ctx context.Context, snap *dbaasv1.DBSnapshot, source *dbaasv1.DBInstance, holder string, status harvester.VMBackupStatus) (ctrl.Result, error) {
	// The scheduler labels what it creates; a user-created request never
	// carries this label, so its absence means Manual.
	snap.Status.Origin = dbaasv1.SnapshotOriginManual
	if snap.Labels[dbaasv1.LabelSnapshotOrigin] == dbaasv1.SnapshotOriginAutomated {
		snap.Status.Origin = dbaasv1.SnapshotOriginAutomated
	}

	switch {
	case status.ErrorMessage != "":
		if err := r.Harvester.DeleteVMBackup(ctx, snap.Namespace, snap.Name); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.holds().Release(ctx, source.Namespace, backup.SnapshotHoldName(source.UID), holder); err != nil {
			return ctrl.Result{}, err
		}
		return r.setReady(ctx, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotBackupFailed, status.ErrorMessage, ctrl.Result{})

	case status.ReadyToUse:
		if err := r.holds().Release(ctx, source.Namespace, backup.SnapshotHoldName(source.UID), holder); err != nil {
			return ctrl.Result{}, err
		}
		snap.Status.DataVolumeSnapshotName = status.DataVolumeSnapshotName
		return r.setReady(ctx, snap, metav1.ConditionTrue, dbaasv1.ReasonSnapshotBackupReady, "backup is ready to use", ctrl.Result{})

	default:
		// Judged after the backup's own state, so one found Ready right at
		// the deadline is a success, not a timeout.
		if r.Backup.Timeout > 0 && !status.CreatedAt.IsZero() {
			if deadline := status.CreatedAt.Add(r.Backup.Timeout); !r.now().Before(deadline) {
				return r.failBackupTimedOut(ctx, snap, source, holder, deadline)
			}
		}
		return r.setReady(ctx, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotBackupInProgress, "waiting for the backup to become ready",
			ctrl.Result{RequeueAfter: snapshotBackupPollRequeue})
	}
}

// failBackupTimedOut ends a backup that outlived backup.timeout: it's
// deleted, the hold released, and the terminal status (setReady) frees the
// slot — so a stuck backup can't hold capacity forever.
func (r *DBSnapshotReconciler) failBackupTimedOut(ctx context.Context, snap *dbaasv1.DBSnapshot, source *dbaasv1.DBInstance, holder string, deadline time.Time) (ctrl.Result, error) {
	if err := r.Harvester.DeleteVMBackup(ctx, snap.Namespace, snap.Name); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.holds().Release(ctx, source.Namespace, backup.SnapshotHoldName(source.UID), holder); err != nil {
		return ctrl.Result{}, err
	}
	return r.setReady(ctx, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotBackupTimedOut,
		fmt.Sprintf("backup did not finish within %s (deadline %s)", r.Backup.Timeout, deadline.UTC().Format(time.RFC3339)), ctrl.Result{})
}

// confirmSourceLive re-reads the source live and returns it. If it is gone,
// replaced by a same-named object, or being deleted, it releases the hold
// and rejects the snapshot instead.
func (r *DBSnapshotReconciler) confirmSourceLive(ctx context.Context, snap *dbaasv1.DBSnapshot, source *dbaasv1.DBInstance, holder string) (*dbaasv1.DBInstance, bool, ctrl.Result, error) {
	var live dbaasv1.DBInstance
	err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(source), &live)
	var reason dbaasv1.ConditionReason
	var msg string
	switch {
	case apierrors.IsNotFound(err) || (err == nil && live.UID != source.UID):
		reason, msg = dbaasv1.ReasonSnapshotSourceNotFound, fmt.Sprintf("source DBInstance %q no longer exists", source.Name)
	case err != nil:
		return nil, false, ctrl.Result{}, err
	case !live.DeletionTimestamp.IsZero():
		reason, msg = dbaasv1.ReasonSnapshotSourceDeleting, fmt.Sprintf("source DBInstance %q is being deleted", source.Name)
	default:
		return &live, false, ctrl.Result{}, nil
	}
	if err := r.holds().Release(ctx, source.Namespace, backup.SnapshotHoldName(source.UID), holder); err != nil {
		return nil, false, ctrl.Result{}, err
	}
	res, err := r.rejectSnapshot(ctx, snap, reason, msg)
	return nil, true, res, err
}

// reconcileDelete releases any in-flight snapshot hold (safe no-op if we
// don't hold it), waits out any restore reading this source's snapshots
// (protected deletion, shown as Ready=False/DeletionWaitingForRestore),
// deletes the backend backup, and removes the finalizer.
func (r *DBSnapshotReconciler) reconcileDelete(ctx context.Context, snap *dbaasv1.DBSnapshot) (ctrl.Result, error) {
	var source dbaasv1.DBInstance
	err := r.Get(ctx, types.NamespacedName{Namespace: snap.Namespace, Name: snap.Spec.SourceInstanceRef.Name}, &source)
	switch {
	case err == nil:
		holder := snapshotHolderIdentity(snap)
		if relErr := r.holds().Release(ctx, source.Namespace, backup.SnapshotHoldName(source.UID), holder); relErr != nil {
			return ctrl.Result{}, relErr
		}
	case apierrors.IsNotFound(err):
		// Source gone: its lease (owned by it) is already GC'd if it existed.
	default:
		return ctrl.Result{}, err
	}

	// snap.Status.Source is nil only if admission never succeeded, meaning
	// no backend backup was ever created either — nothing to protect.
	if snap.Status.Source != nil {
		held, holdErr := r.holds().AnyRestoreHoldForSource(ctx, snap.Namespace, snap.Status.Source.InstanceUID)
		if holdErr != nil {
			return ctrl.Result{}, holdErr
		}
		if held {
			msg := fmt.Sprintf("deletion is waiting for restores reading source DBInstance %q's snapshots to finish", snap.Spec.SourceInstanceRef.Name)
			return r.setReady(ctx, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotDeletionWaitingForRestore, msg, ctrl.Result{RequeueAfter: snapshotHoldRequeue})
		}
	}

	if err := r.Harvester.DeleteVMBackup(ctx, snap.Namespace, snap.Name); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.releaseSlot(ctx, snap); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.removeSnapshotFinalizer(ctx, client.ObjectKeyFromObject(snap))
}

// removeSnapshotFinalizer re-fetches on every retry to avoid combining a
// fresh resourceVersion with a stale object — mirrors removeDBInstanceFinalizer.
func (r *DBSnapshotReconciler) removeSnapshotFinalizer(ctx context.Context, key client.ObjectKey) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &dbaasv1.DBSnapshot{}
		if err := r.Get(ctx, key, latest); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if !containsString(latest.Finalizers, dbaasv1.DBSnapshotFinalizerName) {
			return nil
		}
		latest.Finalizers = removeString(latest.Finalizers, dbaasv1.DBSnapshotFinalizerName)
		return r.Update(ctx, latest)
	})
}

func (r *DBSnapshotReconciler) rejectSnapshot(ctx context.Context, snap *dbaasv1.DBSnapshot, reason dbaasv1.ConditionReason, msg string) (ctrl.Result, error) {
	return r.setReady(ctx, snap, metav1.ConditionFalse, reason, msg, ctrl.Result{})
}

// setReady sets the Ready condition and writes status — the one place the
// snapshot's status is written. A write that changes the reason is
// announced as an event, only once it has landed: the update carries the
// resourceVersion, so a pass that started from a stale copy conflicts and
// emits nothing.
func (r *DBSnapshotReconciler) setReady(ctx context.Context, snap *dbaasv1.DBSnapshot, status metav1.ConditionStatus,
	reason dbaasv1.ConditionReason, msg string, result ctrl.Result) (ctrl.Result, error) {
	prev := ""
	if cond := snap.Status.GetCondition(dbaasv1.ConditionSnapshotReady); cond != nil {
		prev = cond.Reason
	}
	// A finished snapshot — Ready, failed, timed out or rejected — runs no
	// backup, so it frees its slot (if any) before recording that.
	if status == metav1.ConditionTrue || isTerminalSnapshotReason(reason) {
		if err := r.releaseSlot(ctx, snap); err != nil {
			return ctrl.Result{}, err
		}
	}
	snap.Status.SetCondition(metav1.Condition{
		Type: dbaasv1.ConditionSnapshotReady, Status: status, Reason: string(reason), Message: msg,
	})
	snap.Status.Phase = snapshotPhase(snap, status, reason)
	if snap.Status.CompletionTime == nil && (snap.Status.Phase == dbaasv1.SnapshotPhaseReady || snap.Status.Phase == dbaasv1.SnapshotPhaseFailed) {
		snap.Status.CompletionTime = &metav1.Time{Time: r.now()}
	}
	if status == metav1.ConditionTrue {
		snap.Status.Progress = 100
	}
	snap.Status.ObservedGeneration = snap.Generation
	if err := r.Status().Update(ctx, snap); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	if r.Recorder != nil && prev != string(reason) {
		eventType := corev1.EventTypeNormal
		if isTerminalSnapshotReason(reason) || reason == dbaasv1.ReasonSnapshotDeletionWaitingForRestore {
			eventType = corev1.EventTypeWarning
		}
		r.Recorder.Event(snap, eventType, string(reason), msg)
	}
	return result, nil
}

// snapshotPhase derives the display phase from the Ready condition being
// written, so the two never disagree.
func snapshotPhase(snap *dbaasv1.DBSnapshot, status metav1.ConditionStatus, reason dbaasv1.ConditionReason) string {
	switch {
	case !snap.DeletionTimestamp.IsZero():
		return dbaasv1.SnapshotPhaseDeleting
	case status == metav1.ConditionTrue:
		return dbaasv1.SnapshotPhaseReady
	case isTerminalSnapshotReason(reason):
		return dbaasv1.SnapshotPhaseFailed
	case reason == dbaasv1.ReasonSnapshotBackupInProgress:
		return dbaasv1.SnapshotPhaseInProgress
	default: // BackupQueued, SnapshotHoldWaiting
		return dbaasv1.SnapshotPhaseQueued
	}
}

func isTerminalSnapshotReason(reason dbaasv1.ConditionReason) bool {
	switch reason {
	case dbaasv1.ReasonSnapshotSourceNotFound, dbaasv1.ReasonSnapshotSourceBackupDisabled,
		dbaasv1.ReasonSnapshotSourceNotReady, dbaasv1.ReasonSnapshotSourceDeleting, dbaasv1.ReasonSnapshotBackupFailed,
		dbaasv1.ReasonSnapshotBackupTimedOut:
		return true
	default:
		return false
	}
}

// snapshotHolderPrefix starts every DBSnapshot's snapshot-hold identity;
// the rest is the DBSnapshot's name.
const snapshotHolderPrefix = "snapshot:"

func snapshotHolderIdentity(snap *dbaasv1.DBSnapshot) string {
	return snapshotHolderPrefix + snap.Name
}

func instanceOwnerRef(inst *dbaasv1.DBInstance) *metav1.OwnerReference {
	controller := true
	return &metav1.OwnerReference{
		APIVersion:         dbaasv1.GroupVersion.String(),
		Kind:               "DBInstance",
		Name:               inst.Name,
		UID:                inst.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &controller,
	}
}

func snapshotOwnerRef(snap *dbaasv1.DBSnapshot) *metav1.OwnerReference {
	controller := true
	return &metav1.OwnerReference{
		APIVersion:         dbaasv1.GroupVersion.String(),
		Kind:               "DBSnapshot",
		Name:               snap.Name,
		UID:                snap.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &controller,
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(list []string, s string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// SetupWithManager also indexes DBSnapshot by source name and watches
// DBInstance, so a source becoming Available promptly re-reconciles any
// DBSnapshot waiting on it instead of waiting for the next resync.
func (r *DBSnapshotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("dbaas-controller")
	}
	if r.SlotNamespace == "" || r.Wake == nil {
		return fmt.Errorf("DBSnapshotReconciler needs SlotNamespace and Wake: backups must go through the backup dispatcher")
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &dbaasv1.DBSnapshot{}, snapshotSourceInstanceIdx, snapshotSourceIndexFunc); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&dbaasv1.DBSnapshot{}).
		Watches(&dbaasv1.DBInstance{}, handler.EnqueueRequestsFromMapFunc(r.mapInstanceToSnapshots)).
		WatchesRawSource(source.Channel(r.Wake, &handler.EnqueueRequestForObject{})). // granted a slot
		Named("dbsnapshot").
		Complete(r)
}

func (r *DBSnapshotReconciler) mapInstanceToSnapshots(ctx context.Context, obj client.Object) []reconcile.Request {
	inst, ok := obj.(*dbaasv1.DBInstance)
	if !ok {
		return nil
	}
	var list dbaasv1.DBSnapshotList
	if err := r.List(ctx, &list, client.InNamespace(inst.Namespace), client.MatchingFields{snapshotSourceInstanceIdx: inst.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, snap := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: snap.Namespace, Name: snap.Name}})
	}
	return reqs
}
