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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/backup"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
)

// BackupDispatcher grants backup slots — the only component that decides
// who may start a backup (design: dbaas-backup-concurrency-design.md).
//
// Concurrency safety comes from its key, not its worker count: every event
// maps to the one constant request backupDispatcherKey, and controller-
// runtime's work queue never hands one key to two workers at once, so
// passes never overlap and each sees the previous pass's grants (slots are
// read live). DBSnapshot reconciles never decide about shared capacity;
// they only ask whether they hold a slot. The indexed slot names keep the
// global cap an API-server guarantee even across a leader handover.
type BackupDispatcher struct {
	client.Client
	// APIReader reads slots and the instance snapshot holds live, and
	// confirms a slot holder is gone before reclaiming its slot.
	APIReader client.Reader
	// SlotNamespace is where slot Leases live: the operator namespace.
	SlotNamespace string
	Backup        operatorconfig.BackupConfig
	// Wake signals a snapshot that it was just granted a slot.
	Wake chan<- event.GenericEvent
}

var backupDispatcherKey = reconcile.Request{NamespacedName: types.NamespacedName{Name: "backup-dispatcher"}}

// backupDispatcherResync is a backstop: passes are normally triggered by
// DBSnapshot changes. It catches what no watched event announces — a slot
// deleted by hand, or a repave finishing on an instance whose snapshot was
// skipped as ineligible.
const backupDispatcherResync = time.Minute

func (d *BackupDispatcher) slots() backup.Slots {
	return backup.Slots{Live: d.APIReader, Writer: d.Client, Namespace: d.SlotNamespace}
}

// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;delete

// Reconcile is one dispatcher pass: reclaim stale slots, then grant free
// ones to the oldest eligible waiting snapshots, and wake them.
func (d *BackupDispatcher) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	granted, err := d.slots().List(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	live := granted[:0]
	for _, slot := range granted {
		stale, why, err := d.staleSlot(ctx, slot)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !stale {
			live = append(live, slot)
			continue
		}
		logger.Info("Reclaiming stale backup slot", "slot", slot.Index, "holder", slot.Holder.String(), "reason", why)
		if err := d.slots().Free(ctx, slot); err != nil {
			return ctrl.Result{}, err
		}
	}

	var snapshots dbaasv1.DBSnapshotList
	if err := d.List(ctx, &snapshots); err != nil {
		return ctrl.Result{}, err
	}
	waiting := make([]backup.Candidate, 0, len(snapshots.Items))
	for i := range snapshots.Items {
		if snap := &snapshots.Items[i]; waitingForSlot(snap) {
			waiting = append(waiting, backup.Candidate{
				Holder:  backup.SlotHolder{Namespace: snap.Namespace, Name: snap.Name, UID: snap.UID},
				Created: snap.CreationTimestamp.Time,
				Source:  snap.Spec.SourceInstanceRef.Name,
			})
		}
	}

	var eligibilityErr error
	grants := backup.PlanGrants(waiting, live, d.Backup.MaxConcurrent, d.Backup.PerNamespace(), func(c backup.Candidate) bool {
		ok, err := d.instanceFree(ctx, c)
		if err != nil && eligibilityErr == nil {
			eligibilityErr = err
		}
		return ok
	})
	if eligibilityErr != nil {
		return ctrl.Result{}, eligibilityErr
	}

	for _, g := range grants {
		ok, err := d.slots().Grant(ctx, g.Index, g.Holder)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !ok {
			// Taken since this pass listed slots — only possible if another
			// operator is briefly also dispatching. Re-plan next pass.
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Info("Granted backup slot", "slot", g.Index, "snapshot", g.Holder.String())
		d.wake(g.Holder)
	}
	return ctrl.Result{RequeueAfter: backupDispatcherResync}, nil
}

// waitingForSlot reports whether snap is queued for a slot: admitted and
// waiting (BackupQueued), or given its slot back because its instance was
// busy (SnapshotHoldWaiting) — the latter is re-granted once the instance
// is free (instanceFree). Read from the cache: this only orders and
// selects; the grant itself is what's atomic.
func waitingForSlot(snap *dbaasv1.DBSnapshot) bool {
	if !snap.DeletionTimestamp.IsZero() {
		return false
	}
	cond := snap.Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	return cond != nil && cond.Status == metav1.ConditionFalse &&
		(cond.Reason == string(dbaasv1.ReasonSnapshotBackupQueued) || cond.Reason == string(dbaasv1.ReasonSnapshotHoldWaiting))
}

// instanceFree reports whether c's source instance can take a backup now:
// its snapshot hold is free (or already this snapshot's). A mid-repave
// instance would only make the snapshot hand the slot straight back. A
// missing source is "free": the snapshot rejects itself on its next pass.
func (d *BackupDispatcher) instanceFree(ctx context.Context, c backup.Candidate) (bool, error) {
	var source dbaasv1.DBInstance
	if err := d.Get(ctx, types.NamespacedName{Namespace: c.Holder.Namespace, Name: c.Source}, &source); err != nil {
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	}
	holds := backup.Holds{Live: d.APIReader, Writer: d.Client}
	holder, held, err := holds.Held(ctx, source.Namespace, backup.SnapshotHoldName(source.UID))
	if err != nil {
		return false, err
	}
	return !held || holder == snapshotHolderPrefix+c.Holder.Name, nil
}

// staleSlot reports whether slot's holder no longer needs it: gone,
// replaced by a same-named snapshot, being deleted, or finished. A cached
// read that says "still needed" is trusted (that only keeps a slot); one
// that says stale is confirmed live before the slot is reclaimed.
func (d *BackupDispatcher) staleSlot(ctx context.Context, slot backup.Slot) (bool, string, error) {
	if slot.Holder.Name == "" {
		return true, "slot names no snapshot", nil
	}
	key := types.NamespacedName{Namespace: slot.Holder.Namespace, Name: slot.Holder.Name}
	for _, reader := range []client.Reader{d.Client, d.APIReader} {
		var snap dbaasv1.DBSnapshot
		err := reader.Get(ctx, key, &snap)
		if err != nil && !apierrors.IsNotFound(err) {
			return false, "", err
		}
		why := slotHolderGone(&snap, err, slot.Holder.UID)
		if why == "" {
			return false, "", nil
		}
		if reader == d.APIReader {
			return true, why, nil
		}
	}
	return false, "", nil
}

func slotHolderGone(snap *dbaasv1.DBSnapshot, getErr error, uid types.UID) string {
	switch {
	case apierrors.IsNotFound(getErr):
		return "snapshot not found"
	case snap.UID != uid:
		return "snapshot replaced by a same-named one"
	case !snap.DeletionTimestamp.IsZero():
		return "snapshot is being deleted"
	}
	if cond := snap.Status.GetCondition(dbaasv1.ConditionSnapshotReady); cond != nil &&
		(cond.Status == metav1.ConditionTrue || isTerminalSnapshotReason(dbaasv1.ConditionReason(cond.Reason))) {
		return fmt.Sprintf("snapshot finished (%s)", cond.Reason)
	}
	return ""
}

// wake asks the DBSnapshot controller to reconcile a newly granted snapshot
// now. Never blocks a pass: if the channel is full, the snapshot's own
// backstop requeue picks the grant up instead.
func (d *BackupDispatcher) wake(h backup.SlotHolder) {
	if d.Wake == nil {
		return
	}
	snap := &dbaasv1.DBSnapshot{ObjectMeta: metav1.ObjectMeta{Namespace: h.Namespace, Name: h.Name}}
	select {
	case d.Wake <- event.GenericEvent{Object: snap}:
	default:
	}
}

// queueChanged keeps dispatcher passes to the DBSnapshot changes that can
// free or need a slot: creation, deletion (and its start), and a change of
// Ready reason (queued, started, finished).
var queueChanged = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return true },
	DeleteFunc:  func(event.DeleteEvent) bool { return true },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldSnap, ok1 := e.ObjectOld.(*dbaasv1.DBSnapshot)
		newSnap, ok2 := e.ObjectNew.(*dbaasv1.DBSnapshot)
		if !ok1 || !ok2 {
			return true
		}
		return readyReason(oldSnap) != readyReason(newSnap) ||
			oldSnap.DeletionTimestamp.IsZero() != newSnap.DeletionTimestamp.IsZero()
	},
}

func readyReason(snap *dbaasv1.DBSnapshot) string {
	if cond := snap.Status.GetCondition(dbaasv1.ConditionSnapshotReady); cond != nil {
		return cond.Reason
	}
	return ""
}

// SetupWithManager registers the dispatcher. It owns no type (Named, no
// For): every DBSnapshot change that matters maps to the one dispatcher key.
// There's deliberately no Lease watch — a cluster-wide Lease informer would
// also receive every node's heartbeat Lease; a slot is only ever freed
// alongside a DBSnapshot change, which already triggers a pass.
func (d *BackupDispatcher) SetupWithManager(mgr ctrl.Manager) error {
	if d.SlotNamespace == "" || d.Wake == nil {
		return fmt.Errorf("BackupDispatcher needs SlotNamespace and Wake")
	}
	if d.Backup.MaxConcurrent < 1 {
		return fmt.Errorf("BackupDispatcher needs backup.maxConcurrent >= 1")
	}
	if d.APIReader == nil {
		d.APIReader = mgr.GetAPIReader()
	}
	toDispatcher := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{backupDispatcherKey}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("backup-dispatcher").
		Watches(&dbaasv1.DBSnapshot{}, toDispatcher, builder.WithPredicates(queueChanged)).
		Complete(d)
}
