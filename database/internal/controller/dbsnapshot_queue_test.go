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

// The DBSnapshot side of backup concurrency control (Phase 10): a snapshot
// starts only while it holds a slot, and every way it stops needing one
// gives it back.

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/backup"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/testutil"
)

const testSlotNamespace = "dbaas-system"

// newQueuedSnapshotReconciler has the backup queue switched on.
func newQueuedSnapshotReconciler(t *testing.T, stub *testutil.StubHarvester, objs ...client.Object) (*DBSnapshotReconciler, client.Client) {
	t.Helper()
	r, c := newSnapshotReconciler(t, stub, objs...)
	r.SlotNamespace = testSlotNamespace
	r.Backup = operatorconfig.BackupConfig{MaxConcurrent: 4, Timeout: 6 * time.Hour}
	return r, c
}

func queuedSnapshot() *dbaasv1.DBSnapshot {
	snap := testSnapshot()
	snap.UID = "snap-uid"
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	return snap
}

func slotsOf(c client.Client) backup.Slots {
	return backup.Slots{Live: c, Writer: c, Namespace: testSlotNamespace}
}

func grantSlot(t *testing.T, c client.Client, snap *dbaasv1.DBSnapshot) {
	t.Helper()
	h := backup.SlotHolder{Namespace: snap.Namespace, Name: snap.Name, UID: snap.UID}
	if ok, err := slotsOf(c).Grant(context.Background(), 0, h); err != nil || !ok {
		t.Fatalf("grant slot: (%v, %v)", ok, err)
	}
}

func holdsSlot(t *testing.T, c client.Client, snap *dbaasv1.DBSnapshot) bool {
	t.Helper()
	slot, err := slotsOf(c).HeldBy(context.Background(), snap.UID)
	if err != nil {
		t.Fatalf("HeldBy: %v", err)
	}
	return slot != nil
}

func wantReady(t *testing.T, c client.Client, snap *dbaasv1.DBSnapshot, status metav1.ConditionStatus, reason dbaasv1.ConditionReason) {
	t.Helper()
	cond := getSnapshot(t, c, snap).Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Status != status || cond.Reason != string(reason) {
		t.Fatalf("Ready = %+v, want %s/%s", cond, status, reason)
	}
}

// Without a slot, an admitted snapshot queues: no hold, no backup, a
// backstop requeue only (the dispatcher wakes it), and one event.
func TestDBSnapshotQueuesWithoutASlot(t *testing.T) {
	source := availableSourceInstance()
	snap := queuedSnapshot()
	stub := &testutil.StubHarvester{}
	r, c := newQueuedSnapshotReconciler(t, stub, source, snap)
	recorder := record.NewFakeRecorder(10)
	r.Recorder = recorder

	res := reconcileSnapshot(t, r, snap)
	reconcileSnapshot(t, r, getSnapshot(t, c, snap))

	wantReady(t, c, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotBackupQueued)
	if res.RequeueAfter != snapshotQueuedRequeue {
		t.Fatalf("RequeueAfter = %v, want the %v backstop", res.RequeueAfter, snapshotQueuedRequeue)
	}
	if stub.CreateVMBackupCalls != 0 || snapshotHoldHeld(t, c, source) {
		t.Fatal("a queued snapshot must neither start a backup nor take the instance hold")
	}
	if n := eventsWithReason(drainEvents(recorder), string(dbaasv1.ReasonSnapshotBackupQueued)); n != 1 {
		t.Fatalf("BackupQueued events = %d, want 1", n)
	}
}

func TestDBSnapshotStartsOnceGrantedASlot(t *testing.T) {
	source := availableSourceInstance()
	snap := queuedSnapshot()
	stub := &testutil.StubHarvester{}
	r, c := newQueuedSnapshotReconciler(t, stub, source, snap)
	grantSlot(t, c, snap)

	reconcileSnapshot(t, r, snap)

	wantReady(t, c, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotBackupInProgress)
	if stub.CreateVMBackupCalls != 1 || !holdsSlot(t, c, snap) {
		t.Fatal("a granted snapshot must start its backup and keep its slot while it runs")
	}
}

// A slot is no use while the instance is busy (repave): it's given back,
// so another snapshot can run meanwhile.
func TestDBSnapshotGivesItsSlotBackWhileTheInstanceIsBusy(t *testing.T) {
	source := availableSourceInstance()
	snap := queuedSnapshot()
	stub := &testutil.StubHarvester{}
	r, c := newQueuedSnapshotReconciler(t, stub, source, snap)
	grantSlot(t, c, snap)
	if _, err := (backup.Holds{Live: c, Writer: c}).Acquire(context.Background(), source.Namespace,
		backup.SnapshotHoldName(source.UID), "repave", instanceOwnerRef(source), nil); err != nil {
		t.Fatalf("seed repave hold: %v", err)
	}

	reconcileSnapshot(t, r, snap)

	wantReady(t, c, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotHoldWaiting)
	if holdsSlot(t, c, snap) || stub.CreateVMBackupCalls != 0 {
		t.Fatal("the slot must be given back while the instance is busy")
	}
}

// Every way a snapshot stops needing its slot gives it back.
func TestDBSnapshotReleasesItsSlotWhenFinished(t *testing.T) {
	for name, tc := range map[string]struct {
		status     *testutil.StubHarvester
		source     func() *dbaasv1.DBInstance
		wantStatus metav1.ConditionStatus
		wantReason dbaasv1.ConditionReason
	}{
		"backup ready": {&testutil.StubHarvester{VMBackupStatus: backupReadyStatus()}, availableSourceInstance,
			metav1.ConditionTrue, dbaasv1.ReasonSnapshotBackupReady},
		"backup failed": {&testutil.StubHarvester{VMBackupStatus: backupFailedStatus("target unreachable")}, availableSourceInstance,
			metav1.ConditionFalse, dbaasv1.ReasonSnapshotBackupFailed},
		"rejected after the grant": {&testutil.StubHarvester{}, deletingSource,
			metav1.ConditionFalse, dbaasv1.ReasonSnapshotSourceDeleting},
	} {
		t.Run(name, func(t *testing.T) {
			snap := queuedSnapshot()
			r, c := newQueuedSnapshotReconciler(t, tc.status, tc.source(), snap)
			grantSlot(t, c, snap)

			reconcileSnapshot(t, r, snap)

			wantReady(t, c, snap, tc.wantStatus, tc.wantReason)
			if holdsSlot(t, c, snap) {
				t.Fatal("a finished snapshot must give its slot back")
			}
		})
	}
}

func TestDBSnapshotDeletionReleasesItsSlot(t *testing.T) {
	source := availableSourceInstance()
	snap := queuedSnapshot()
	now := metav1.Now()
	snap.DeletionTimestamp = &now
	r, c := newQueuedSnapshotReconciler(t, &testutil.StubHarvester{VMBackupPresent: true}, source, snap)
	grantSlot(t, c, snap)

	reconcileSnapshot(t, r, snap)

	if holdsSlot(t, c, snap) {
		t.Fatal("deleting a running snapshot must give its slot back")
	}
}

// ---- backup timeout ----

func runningSnapshot(source *dbaasv1.DBInstance) *dbaasv1.DBSnapshot {
	snap := queuedSnapshot()
	snap.Status.Source = sourceMetadataFrom(source, operatorconfig.DatabaseDefaults{})
	snap.Status.SetCondition(metav1.Condition{
		Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionFalse,
		Reason: string(dbaasv1.ReasonSnapshotBackupInProgress), Message: "waiting",
	})
	return snap
}

// A backup that outlives backup.timeout is deleted and fails, freeing its
// hold and slot — measured from the backup's own creation.
func TestDBSnapshotBackupTimesOut(t *testing.T) {
	source := availableSourceInstance()
	snap := runningSnapshot(source)
	started := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	stub := &testutil.StubHarvester{VMBackupPresent: true}
	stub.VMBackupStatus.CreatedAt = started
	r, c := newQueuedSnapshotReconciler(t, stub, source, snap)
	r.Backup.Timeout = time.Hour
	r.Now = func() time.Time { return started.Add(time.Hour) }
	grantSlot(t, c, snap)
	if _, err := (backup.Holds{Live: c, Writer: c}).Acquire(context.Background(), source.Namespace,
		backup.SnapshotHoldName(source.UID), snapshotHolderIdentity(snap), instanceOwnerRef(source), nil); err != nil {
		t.Fatalf("seed our hold: %v", err)
	}

	reconcileSnapshot(t, r, snap)

	wantReady(t, c, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotBackupTimedOut)
	if stub.DeleteVMBackupCalls != 1 {
		t.Fatal("the timed-out backup must be deleted")
	}
	if holdsSlot(t, c, snap) || snapshotHoldHeld(t, c, source) {
		t.Fatal("a timed-out backup must free its slot and the instance hold")
	}
}

func TestDBSnapshotBackupWithinItsTimeoutKeepsRunning(t *testing.T) {
	source := availableSourceInstance()
	snap := runningSnapshot(source)
	started := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	stub := &testutil.StubHarvester{VMBackupPresent: true}
	stub.VMBackupStatus.CreatedAt = started
	r, c := newQueuedSnapshotReconciler(t, stub, source, snap)
	r.Backup.Timeout = time.Hour
	r.Now = func() time.Time { return started.Add(59 * time.Minute) }

	reconcileSnapshot(t, r, snap)

	wantReady(t, c, snap, metav1.ConditionFalse, dbaasv1.ReasonSnapshotBackupInProgress)
	if stub.DeleteVMBackupCalls != 0 {
		t.Fatal("a backup still within its timeout must not be deleted")
	}
}

// Observed before the deadline is applied: Ready right at the deadline is
// a success.
func TestDBSnapshotBackupReadyAtTheDeadlineSucceeds(t *testing.T) {
	source := availableSourceInstance()
	snap := runningSnapshot(source)
	started := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	stub := &testutil.StubHarvester{VMBackupPresent: true, VMBackupStatus: backupReadyStatus()}
	stub.VMBackupStatus.CreatedAt = started
	r, c := newQueuedSnapshotReconciler(t, stub, source, snap)
	r.Backup.Timeout = time.Hour
	r.Now = func() time.Time { return started.Add(2 * time.Hour) }

	reconcileSnapshot(t, r, snap)

	wantReady(t, c, snap, metav1.ConditionTrue, dbaasv1.ReasonSnapshotBackupReady)
}
