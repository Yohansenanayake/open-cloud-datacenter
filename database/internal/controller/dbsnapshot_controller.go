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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

// DBSnapshotReconciler reconciles DBSnapshot CRDs — manual snapshot creation
// (spec §3.1/§6, design §4/§7). Automated scheduling (a later phase) only
// adds a new caller creating these objects, not new logic here.
//
// Not a formal ensure.Step/Runner pipeline — that pattern is DBInstance-
// specific today. reconcileCreate instead composes three plain stages
// (admitSnapshot, acquireHold, runBackupAttempt) in the same spirit.
//
// APIReader is an uncached reader for the hold Leases: this reconciler's
// "is any restore still reading this source?" check is what lets it delete a
// backend backup, and must not be answered from a stale cache.
//
// DatabaseDefaults resolves the source's defaulted settings when its
// status.appliedSpec doesn't record them (see sourceMetadataFrom).
type DBSnapshotReconciler struct {
	client.Client
	APIReader        client.Reader
	Harvester        harvester.ClientInterface
	DatabaseDefaults operatorconfig.DatabaseDefaults
}

func (r *DBSnapshotReconciler) holds() backup.Holds {
	return backup.Holds{Live: r.APIReader, Writer: r.Client}
}

const (
	snapshotHoldRequeue       = 5 * time.Second
	snapshotBackupPollRequeue = 10 * time.Second
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

// reconcileCreate runs admit → acquireHold → runBackupAttempt, stopping at
// the first that isn't ready to hand off. Terminal outcomes aren't retried:
// diagnose, then create a new DBSnapshot rather than resurrecting this one.
func (r *DBSnapshotReconciler) reconcileCreate(ctx context.Context, snap *dbaasv1.DBSnapshot) (ctrl.Result, error) {
	if cond := snap.Status.GetCondition(dbaasv1.ConditionSnapshotReady); cond != nil {
		if cond.Status == metav1.ConditionTrue || isTerminalSnapshotReason(dbaasv1.ConditionReason(cond.Reason)) {
			return ctrl.Result{}, nil // steady state or rejected — nothing left to do
		}
	}

	source, admitted, res, err := r.admitSnapshot(ctx, snap)
	if !admitted {
		return res, err
	}

	holder, acquired, res, err := r.acquireHold(ctx, snap, &source)
	if !acquired {
		return res, err
	}

	return r.runBackupAttempt(ctx, snap, &source, holder)
}

// admitSnapshot resolves the source and checks §2.1/§3.1 admission.
// admitted=false: return (res, err) as-is — rejected (err nil) or a
// transient fetch error.
func (r *DBSnapshotReconciler) admitSnapshot(ctx context.Context, snap *dbaasv1.DBSnapshot) (source dbaasv1.DBInstance, admitted bool, res ctrl.Result, err error) {
	if getErr := r.Get(ctx, types.NamespacedName{Namespace: snap.Namespace, Name: snap.Spec.SourceInstanceRef.Name}, &source); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			res, err = r.rejectSnapshot(ctx, snap, dbaasv1.ReasonSnapshotSourceNotFound,
				fmt.Sprintf("source DBInstance %q not found", snap.Spec.SourceInstanceRef.Name))
			return dbaasv1.DBInstance{}, false, res, err
		}
		return dbaasv1.DBInstance{}, false, ctrl.Result{}, getErr
	}

	// §2.1: no spec.backup means no backup capability at all, and presence
	// is immutable — this will never resolve on its own.
	if source.Spec.Backup == nil {
		res, err = r.rejectSnapshot(ctx, snap, dbaasv1.ReasonSnapshotSourceBackupDisabled,
			fmt.Sprintf("source DBInstance %q does not have backup capability enabled (spec.backup is absent)", source.Name))
		return dbaasv1.DBInstance{}, false, res, err
	}

	// §3.1: a not-ready source won't resolve on its own, so reject rather
	// than retry — only lease contention (acquireHold) is worth waiting on.
	if source.Status.Phase != dbaasv1.StatusAvailable {
		res, err = r.rejectSnapshot(ctx, snap, dbaasv1.ReasonSnapshotSourceNotReady,
			fmt.Sprintf("source DBInstance is %q, not available", source.Status.Phase))
		return dbaasv1.DBInstance{}, false, res, err
	}

	// Captured once, on first successful admission — never overwritten on a
	// later pass, even if the source's mutable fields (e.g. allocatedStorage)
	// change before the backup finishes. Restore reads only this, never the
	// live source, so it must reflect what was actually backed up.
	if snap.Status.Source == nil {
		snap.Status.Source = sourceMetadataFrom(&source, r.DatabaseDefaults)
	}

	return source, true, ctrl.Result{}, nil
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
	}
}

// acquireHold acquires the snapshot-hold lease shared with repave.
// acquired=false: return (res, err) as-is — held by another (err nil) or
// a transient lease-check failure.
func (r *DBSnapshotReconciler) acquireHold(ctx context.Context, snap *dbaasv1.DBSnapshot, source *dbaasv1.DBInstance) (holder string, acquired bool, res ctrl.Result, err error) {
	holder = snapshotHolderIdentity(snap)
	result, acqErr := r.holds().Acquire(ctx, source.Namespace, backup.SnapshotHoldName(source.UID), holder, instanceOwnerRef(source), nil)
	if acqErr != nil {
		return holder, false, ctrl.Result{}, acqErr
	}
	if !result.Acquired {
		msg := fmt.Sprintf("waiting for %s to finish before this snapshot can start", result.HolderIdentity)
		snap.Status.SetCondition(metav1.Condition{
			Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionFalse,
			Reason: string(dbaasv1.ReasonSnapshotHoldWaiting), Message: msg,
		})
		res, err = r.patchSnapshotStatus(ctx, snap, ctrl.Result{RequeueAfter: snapshotHoldRequeue})
		return holder, false, res, err
	}
	return holder, true, ctrl.Result{}, nil
}

// runBackupAttempt creates the backend backup and tracks it to a terminal
// state, releasing the hold only once it's actually settled.
func (r *DBSnapshotReconciler) runBackupAttempt(ctx context.Context, snap *dbaasv1.DBSnapshot, source *dbaasv1.DBInstance, holder string) (ctrl.Result, error) {
	if err := r.Harvester.CreateVMBackup(ctx, snap.Namespace, snap.Name, source.Status.Resources.VMName, snapshotOwnerRef(snap)); err != nil {
		return ctrl.Result{}, err
	}

	status, err := r.Harvester.GetVMBackupStatus(ctx, snap.Namespace, snap.Name, source.Status.Resources.DataVolumeName)
	if err != nil {
		return ctrl.Result{}, err
	}

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
		snap.Status.SetCondition(metav1.Condition{
			Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionFalse,
			Reason: string(dbaasv1.ReasonSnapshotBackupFailed), Message: status.ErrorMessage,
		})
		return r.patchSnapshotStatus(ctx, snap, ctrl.Result{})

	case status.ReadyToUse:
		if err := r.holds().Release(ctx, source.Namespace, backup.SnapshotHoldName(source.UID), holder); err != nil {
			return ctrl.Result{}, err
		}
		snap.Status.DataVolumeSnapshotName = status.DataVolumeSnapshotName
		snap.Status.SetCondition(metav1.Condition{
			Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionTrue,
			Reason: string(dbaasv1.ReasonSnapshotBackupReady), Message: "backup is ready to use",
		})
		return r.patchSnapshotStatus(ctx, snap, ctrl.Result{})

	default:
		snap.Status.SetCondition(metav1.Condition{
			Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionFalse,
			Reason: string(dbaasv1.ReasonSnapshotBackupInProgress), Message: "waiting for the backup to become ready",
		})
		return r.patchSnapshotStatus(ctx, snap, ctrl.Result{RequeueAfter: snapshotBackupPollRequeue})
	}
}

// reconcileDelete releases any in-flight snapshot hold (safe no-op if we
// don't hold it), waits out any restore reading this source's snapshots
// (protected deletion), deletes the backend backup, and removes the
// finalizer.
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
			return ctrl.Result{RequeueAfter: snapshotHoldRequeue}, nil
		}
	}

	if err := r.Harvester.DeleteVMBackup(ctx, snap.Namespace, snap.Name); err != nil {
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
	snap.Status.SetCondition(metav1.Condition{
		Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionFalse,
		Reason: string(reason), Message: msg,
	})
	return r.patchSnapshotStatus(ctx, snap, ctrl.Result{})
}

func (r *DBSnapshotReconciler) patchSnapshotStatus(ctx context.Context, snap *dbaasv1.DBSnapshot, result ctrl.Result) (ctrl.Result, error) {
	snap.Status.ObservedGeneration = snap.Generation
	if err := r.Status().Update(ctx, snap); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return result, nil
}

func isTerminalSnapshotReason(reason dbaasv1.ConditionReason) bool {
	switch reason {
	case dbaasv1.ReasonSnapshotSourceNotFound, dbaasv1.ReasonSnapshotSourceBackupDisabled,
		dbaasv1.ReasonSnapshotSourceNotReady, dbaasv1.ReasonSnapshotBackupFailed:
		return true
	default:
		return false
	}
}

func snapshotHolderIdentity(snap *dbaasv1.DBSnapshot) string {
	return "snapshot:" + snap.Name
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
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &dbaasv1.DBSnapshot{}, snapshotSourceInstanceIdx, snapshotSourceIndexFunc); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&dbaasv1.DBSnapshot{}).
		Watches(&dbaasv1.DBInstance{}, handler.EnqueueRequestsFromMapFunc(r.mapInstanceToSnapshots)).
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
