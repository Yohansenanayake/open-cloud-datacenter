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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/backup"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/testutil"
)

func availableSourceInstance() *dbaasv1.DBInstance {
	return &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "tenant-a", UID: "orders-uid"},
		Spec: dbaasv1.DBInstanceSpec{
			DBInstanceClass:  "db.t3.small",
			AllocatedStorage: 20,
			NetworkRef:       "default/vm-network",
			Backup:           &dbaasv1.BackupSpec{},
		},
		Status: dbaasv1.DBInstanceStatus{
			Phase: dbaasv1.StatusAvailable,
			Resources: dbaasv1.ResourceRefs{
				VMName:         "pg-orders",
				DataVolumeName: "pg-orders-data",
			},
		},
	}
}

func testSnapshot() *dbaasv1.DBSnapshot {
	return &dbaasv1.DBSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-before-upgrade", Namespace: "tenant-a"},
		Spec:       dbaasv1.DBSnapshotSpec{SourceInstanceRef: corev1.LocalObjectReference{Name: "orders"}},
	}
}

func newSnapshotReconciler(t *testing.T, stub *testutil.StubHarvester, objs ...client.Object) (*DBSnapshotReconciler, client.Client) {
	t.Helper()
	c := testutil.NewClient(t, objs...)
	return &DBSnapshotReconciler{Client: c, Harvester: stub}, c
}

func reconcileSnapshot(t *testing.T, r *DBSnapshotReconciler, snap *dbaasv1.DBSnapshot) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: snap.Namespace, Name: snap.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func getSnapshot(t *testing.T, c client.Client, snap *dbaasv1.DBSnapshot) *dbaasv1.DBSnapshot {
	t.Helper()
	var got dbaasv1.DBSnapshot
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: snap.Namespace, Name: snap.Name}, &got); err != nil {
		t.Fatalf("Get DBSnapshot: %v", err)
	}
	return &got
}

func TestDBSnapshotFirstReconcileOnlyAddsFinalizer(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	reconcileSnapshot(t, r, snap)

	got := getSnapshot(t, c, snap)
	if !containsString(got.Finalizers, dbaasv1.DBSnapshotFinalizerName) {
		t.Fatal("finalizer not added on first reconcile")
	}
	if stub.CreateVMBackupCalls != 0 {
		t.Fatal("CreateVMBackup must not be called before the finalizer is present")
	}
}

func TestDBSnapshotRejectsWhenSourceNotFound(t *testing.T) {
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, snap)

	reconcileSnapshot(t, r, snap)

	got := getSnapshot(t, c, snap)
	cond := got.Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonSnapshotSourceNotFound) {
		t.Fatalf("Ready condition = %+v, want False/SourceNotFound", cond)
	}
}

func TestDBSnapshotRejectsWhenSourceHasNoBackupCapability(t *testing.T) {
	source := availableSourceInstance()
	source.Spec.Backup = nil // §2.1: no backup field, no backup capability at all
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	reconcileSnapshot(t, r, snap)

	got := getSnapshot(t, c, snap)
	cond := got.Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonSnapshotSourceBackupDisabled) {
		t.Fatalf("Ready condition = %+v, want False/SourceBackupDisabled", cond)
	}
	if stub.CreateVMBackupCalls != 0 {
		t.Fatal("CreateVMBackup must not be called against a source with no backup capability")
	}
}

func TestDBSnapshotRejectsWhenSourceNotAvailable(t *testing.T) {
	source := availableSourceInstance()
	source.Status.Phase = dbaasv1.StatusStopped
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	reconcileSnapshot(t, r, snap)

	got := getSnapshot(t, c, snap)
	cond := got.Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonSnapshotSourceNotReady) {
		t.Fatalf("Ready condition = %+v, want False/SourceNotReady", cond)
	}
	if stub.CreateVMBackupCalls != 0 {
		t.Fatal("CreateVMBackup must not be called against a not-ready source")
	}
}

func TestDBSnapshotWaitsWhenSnapshotHoldIsHeldByAnother(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	// Simulate repave already holding the lease.
	if _, err := backup.Acquire(context.Background(), c, source.Namespace, backup.SnapshotHoldName(source.UID), "repave", instanceOwnerRef(source), nil); err != nil {
		t.Fatalf("seed competing hold: %v", err)
	}

	res := reconcileSnapshot(t, r, snap)

	if res.RequeueAfter != snapshotHoldRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.RequeueAfter, snapshotHoldRequeue)
	}
	got := getSnapshot(t, c, snap)
	cond := got.Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Reason != string(dbaasv1.ReasonSnapshotHoldWaiting) {
		t.Fatalf("Ready condition = %+v, want reason SnapshotHoldWaiting", cond)
	}
	if stub.CreateVMBackupCalls != 0 {
		t.Fatal("CreateVMBackup must not be called while the lease is held by another")
	}
}

func TestDBSnapshotBackupInProgressRequeues(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{} // ReadyToUse defaults false, ErrorMessage empty: in progress
	r, c := newSnapshotReconciler(t, stub, source, snap)

	res := reconcileSnapshot(t, r, snap)

	if res.RequeueAfter != snapshotBackupPollRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.RequeueAfter, snapshotBackupPollRequeue)
	}
	if stub.CreateVMBackupCalls != 1 {
		t.Fatalf("CreateVMBackupCalls = %d, want 1", stub.CreateVMBackupCalls)
	}
	if stub.LastVMBackupSourceVMName != "pg-orders" {
		t.Fatalf("LastVMBackupSourceVMName = %q, want %q", stub.LastVMBackupSourceVMName, "pg-orders")
	}
	got := getSnapshot(t, c, snap)
	cond := got.Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Reason != string(dbaasv1.ReasonSnapshotBackupInProgress) {
		t.Fatalf("Ready condition = %+v, want reason BackupInProgress", cond)
	}

	// The snapshot hold must still be held while the backup is in progress.
	_, held, err := backup.Held(context.Background(), c, source.Namespace, backup.SnapshotHoldName(source.UID))
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if !held {
		t.Fatal("snapshot hold released before the backup reported ready")
	}
}

func TestDBSnapshotReadyReleasesTheHoldAndRecordsManualOrigin(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{VMBackupStatus: backupReadyStatus()}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	reconcileSnapshot(t, r, snap)

	got := getSnapshot(t, c, snap)
	cond := got.Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != string(dbaasv1.ReasonSnapshotBackupReady) {
		t.Fatalf("Ready condition = %+v, want True/BackupReady", cond)
	}
	if got.Status.Origin != dbaasv1.SnapshotOriginManual {
		t.Fatalf("Origin = %q, want %q", got.Status.Origin, dbaasv1.SnapshotOriginManual)
	}

	_, held, err := backup.Held(context.Background(), c, source.Namespace, backup.SnapshotHoldName(source.UID))
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if held {
		t.Fatal("snapshot hold still held after the backup became ready")
	}
}

func TestDBSnapshotReadyRecordsAutomatedOriginFromLabel(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Labels = map[string]string{dbaasv1.LabelSnapshotOrigin: dbaasv1.SnapshotOriginAutomated}
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{VMBackupStatus: backupReadyStatus()}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	reconcileSnapshot(t, r, snap)

	got := getSnapshot(t, c, snap)
	if got.Status.Origin != dbaasv1.SnapshotOriginAutomated {
		t.Fatalf("Origin = %q, want %q", got.Status.Origin, dbaasv1.SnapshotOriginAutomated)
	}
}

func TestDBSnapshotReadyIsSteadyStateOnRetry(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{VMBackupStatus: backupReadyStatus()}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	reconcileSnapshot(t, r, snap)
	firstCreateCalls := stub.CreateVMBackupCalls

	reconcileSnapshot(t, r, getSnapshot(t, c, snap))

	if stub.CreateVMBackupCalls != firstCreateCalls {
		t.Fatalf("CreateVMBackupCalls grew from %d to %d on a steady-state re-reconcile", firstCreateCalls, stub.CreateVMBackupCalls)
	}
}

func TestDBSnapshotFailedBackupCleansUpAndReleasesHold(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{VMBackupStatus: backupFailedStatus("backup target unreachable")}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	reconcileSnapshot(t, r, snap)

	got := getSnapshot(t, c, snap)
	cond := got.Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonSnapshotBackupFailed) {
		t.Fatalf("Ready condition = %+v, want False/BackupFailed", cond)
	}
	if cond.Message != "backup target unreachable" {
		t.Fatalf("Message = %q, want the backend error surfaced verbatim", cond.Message)
	}
	if stub.DeleteVMBackupCalls != 1 {
		t.Fatalf("DeleteVMBackupCalls = %d, want 1 (clean up the failed backend attempt)", stub.DeleteVMBackupCalls)
	}
	_, held, err := backup.Held(context.Background(), c, source.Namespace, backup.SnapshotHoldName(source.UID))
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if held {
		t.Fatal("snapshot hold still held after a failed backup was cleaned up")
	}
}

func TestDBSnapshotDeleteReleasesInFlightHoldAndDeletesBackend(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	// Simulate creation still in flight: this snapshot holds the lease.
	if _, err := backup.Acquire(context.Background(), c, source.Namespace, backup.SnapshotHoldName(source.UID), snapshotHolderIdentity(snap), instanceOwnerRef(source), nil); err != nil {
		t.Fatalf("seed in-flight hold: %v", err)
	}

	if err := c.Delete(context.Background(), snap); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reconcileSnapshot(t, r, snap)

	if stub.DeleteVMBackupCalls != 1 {
		t.Fatalf("DeleteVMBackupCalls = %d, want 1", stub.DeleteVMBackupCalls)
	}
	_, held, err := backup.Held(context.Background(), c, source.Namespace, backup.SnapshotHoldName(source.UID))
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if held {
		t.Fatal("in-flight snapshot hold still held after deletion cleanup")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: snap.Namespace, Name: snap.Name}, &dbaasv1.DBSnapshot{}); err == nil {
		t.Fatal("DBSnapshot still exists after its finalizer should have been removed")
	}
}

func TestDBSnapshotDeleteWithSourceGoneSkipsReleaseButDeletesBackend(t *testing.T) {
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, snap) // no source instance seeded

	if err := c.Delete(context.Background(), snap); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	res := reconcileSnapshot(t, r, snap)
	_ = res

	if stub.DeleteVMBackupCalls != 1 {
		t.Fatalf("DeleteVMBackupCalls = %d, want 1 even when the source is already gone", stub.DeleteVMBackupCalls)
	}
}

func backupReadyStatus() harvester.VMBackupStatus {
	return harvester.VMBackupStatus{
		ReadyToUse:             true,
		DataVolumeSnapshotName: "orders-before-upgrade-volume-pg-orders-data",
	}
}

func backupFailedStatus(msg string) harvester.VMBackupStatus {
	return harvester.VMBackupStatus{ErrorMessage: msg}
}
