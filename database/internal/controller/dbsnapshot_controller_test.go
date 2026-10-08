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
	"reflect"
	"strings"
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/backup"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
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
	return &DBSnapshotReconciler{Client: c, APIReader: c, Harvester: stub}, c
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
	source.Spec.Backup = nil // no backup field, no backup capability at all
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

func TestDBSnapshotAdmissionCapturesSourceMetadataOnce(t *testing.T) {
	source := availableSourceInstance()
	source.Spec.DBName = "appdb"
	source.Spec.EngineVersion = "17"
	source.Spec.Port = 5432
	source.Spec.StorageType = "longhorn"
	source.Status.CurrentImageRevision = "test-ubuntu-24-04-postgres-r3"
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{} // ReadyToUse defaults false: stays in progress, admission still ran
	r, c := newSnapshotReconciler(t, stub, source, snap)

	reconcileSnapshot(t, r, snap)

	got := getSnapshot(t, c, snap)
	want := &dbaasv1.SourceMetadata{
		InstanceUID:      source.UID,
		DBName:           "appdb",
		MasterUsername:   "dbadmin", // left unset on the source: its effective (defaulted) value
		EngineVersion:    "17",
		Port:             5432,
		StorageType:      "longhorn",
		AllocatedStorage: 20,
		ImageRevision:    "test-ubuntu-24-04-postgres-r3",
		// Hints for a later restore's UI, never inherited by it.
		DBInstanceClass: "db.t3.small",
		NetworkRef:      "default/vm-network",
		Backup:          &dbaasv1.BackupSpec{},
	}
	if got.Status.Source == nil || !reflect.DeepEqual(*got.Status.Source, *want) {
		t.Fatalf("Status.Source = %+v, want %+v", got.Status.Source, want)
	}

	// A later pass, after the source's mutable allocatedStorage changes,
	// must not overwrite what was already captured — it must reflect what
	// was actually backed up, not the source's current state.
	var freshSource dbaasv1.DBInstance
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: source.Namespace, Name: source.Name}, &freshSource); err != nil {
		t.Fatalf("get source: %v", err)
	}
	freshSource.Spec.AllocatedStorage = 100
	if err := c.Update(context.Background(), &freshSource); err != nil {
		t.Fatalf("update source: %v", err)
	}

	reconcileSnapshot(t, r, getSnapshot(t, c, snap))

	got = getSnapshot(t, c, snap)
	if got.Status.Source.AllocatedStorage != 20 {
		t.Fatalf("AllocatedStorage = %d, want the originally-captured 20 (frozen, not re-read from the source)", got.Status.Source.AllocatedStorage)
	}
}

func TestDBSnapshotWaitsWhenSnapshotHoldIsHeldByAnother(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	// Simulate repave already holding the lease.
	if _, err := (backup.Holds{Live: c, Writer: c}).Acquire(context.Background(), source.Namespace, backup.SnapshotHoldName(source.UID), "repave", instanceOwnerRef(source), nil); err != nil {
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
	_, held, err := (backup.Holds{Live: c, Writer: c}).Held(context.Background(), source.Namespace, backup.SnapshotHoldName(source.UID))
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
	if got.Status.DataVolumeSnapshotName != backupReadyStatus().DataVolumeSnapshotName {
		t.Fatalf("DataVolumeSnapshotName = %q, want %q", got.Status.DataVolumeSnapshotName, backupReadyStatus().DataVolumeSnapshotName)
	}

	_, held, err := (backup.Holds{Live: c, Writer: c}).Held(context.Background(), source.Namespace, backup.SnapshotHoldName(source.UID))
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
	_, held, err := (backup.Holds{Live: c, Writer: c}).Held(context.Background(), source.Namespace, backup.SnapshotHoldName(source.UID))
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
	if _, err := (backup.Holds{Live: c, Writer: c}).Acquire(context.Background(), source.Namespace, backup.SnapshotHoldName(source.UID), snapshotHolderIdentity(snap), instanceOwnerRef(source), nil); err != nil {
		t.Fatalf("seed in-flight hold: %v", err)
	}

	if err := c.Delete(context.Background(), snap); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reconcileSnapshot(t, r, snap)

	if stub.DeleteVMBackupCalls != 1 {
		t.Fatalf("DeleteVMBackupCalls = %d, want 1", stub.DeleteVMBackupCalls)
	}
	_, held, err := (backup.Holds{Live: c, Writer: c}).Held(context.Background(), source.Namespace, backup.SnapshotHoldName(source.UID))
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

// The restore-hold check that gates deleting the backend backup must read
// the API server, not the cache: a restore hold acquired a moment ago that
// the informer cache hasn't delivered yet must still block the delete.
func TestDBSnapshotDeleteWaitsForRestoreHoldTheCacheHasNotSeenYet(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	snap.Status.Source = &dbaasv1.SourceMetadata{InstanceUID: source.UID}
	stub := &testutil.StubHarvester{}
	cache := testutil.NewClient(t, source, snap)
	live := testutil.NewClient(t, source, snap)
	r := &DBSnapshotReconciler{Client: cache, APIReader: live, Harvester: stub}

	restoringOwner := instanceOwnerRef(&dbaasv1.DBInstance{ObjectMeta: metav1.ObjectMeta{Name: "restore-target", UID: "restoring-uid"}})
	if _, err := (backup.Holds{Live: live, Writer: live}).Acquire(context.Background(), source.Namespace, backup.RestoreHoldName("restoring-uid"), "restoring-uid",
		restoringOwner, map[string]string{backup.SourceUIDLabel: string(source.UID)}); err != nil {
		t.Fatalf("seed restore hold (live only): %v", err)
	}
	if err := cache.Delete(context.Background(), snap); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	reconcileSnapshot(t, r, snap)

	if stub.DeleteVMBackupCalls != 0 {
		t.Fatal("DeleteVMBackup must not run while a restore hold exists on the API server, whatever the cache says")
	}
}

func TestDBSnapshotDeleteWaitsForRestoreHoldOnSource(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	snap.Status.Source = &dbaasv1.SourceMetadata{InstanceUID: source.UID}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	// Simulate a restore in flight, reading from one of this source's snapshots.
	restoringOwner := instanceOwnerRef(&dbaasv1.DBInstance{ObjectMeta: metav1.ObjectMeta{Name: "restore-target", UID: "restoring-uid"}})
	if _, err := (backup.Holds{Live: c, Writer: c}).Acquire(context.Background(), source.Namespace, backup.RestoreHoldName("restoring-uid"), "restoring-uid",
		restoringOwner, map[string]string{backup.SourceUIDLabel: string(source.UID)}); err != nil {
		t.Fatalf("seed restore hold: %v", err)
	}

	if err := c.Delete(context.Background(), snap); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	res := reconcileSnapshot(t, r, snap)

	if res.RequeueAfter != snapshotHoldRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.RequeueAfter, snapshotHoldRequeue)
	}
	if stub.DeleteVMBackupCalls != 0 {
		t.Fatal("DeleteVMBackup must not be called while a restore hold protects this source's snapshots")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: snap.Namespace, Name: snap.Name}, &dbaasv1.DBSnapshot{}); err != nil {
		t.Fatalf("DBSnapshot should still exist while blocked: %v", err)
	}
}

func TestDBSnapshotDeleteProceedsOnceRestoreHoldReleased(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	snap.Status.Source = &dbaasv1.SourceMetadata{InstanceUID: source.UID}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	restoringOwner := instanceOwnerRef(&dbaasv1.DBInstance{ObjectMeta: metav1.ObjectMeta{Name: "restore-target", UID: "restoring-uid"}})
	if _, err := (backup.Holds{Live: c, Writer: c}).Acquire(context.Background(), source.Namespace, backup.RestoreHoldName("restoring-uid"), "restoring-uid",
		restoringOwner, map[string]string{backup.SourceUIDLabel: string(source.UID)}); err != nil {
		t.Fatalf("seed restore hold: %v", err)
	}
	if err := c.Delete(context.Background(), snap); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reconcileSnapshot(t, r, snap) // blocked pass

	if err := (backup.Holds{Live: c, Writer: c}).Release(context.Background(), source.Namespace, backup.RestoreHoldName("restoring-uid"), "restoring-uid"); err != nil {
		t.Fatalf("release restore hold: %v", err)
	}
	reconcileSnapshot(t, r, snap)

	if stub.DeleteVMBackupCalls != 1 {
		t.Fatalf("DeleteVMBackupCalls = %d, want 1 once the restore hold is released", stub.DeleteVMBackupCalls)
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

// A source that left its restore-inherited settings to defaults must be
// recorded with what it actually runs — not empty strings, which would make
// a restored target apply its *own* defaults (dbName = the target's name, a
// database the restored disk doesn't contain).
func TestDBSnapshotAdmissionRecordsEffectiveValuesForDefaultedSourceFields(t *testing.T) {
	source := availableSourceInstance()                                   // dbName, masterUsername, engineVersion, port, storageType all unset
	source.Status.CurrentImageRevision = "ubuntu-2404-postgres-v20260815" // catalog default engine version: 18
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	r, c := newSnapshotReconciler(t, &testutil.StubHarvester{}, source, snap)

	reconcileSnapshot(t, r, snap)

	got := getSnapshot(t, c, snap).Status.Source
	if got == nil {
		t.Fatal("Status.Source not captured")
	}
	if got.DBName != source.Name || got.MasterUsername != "dbadmin" || got.EngineVersion != "18" ||
		got.Port != 5432 || got.StorageType != "longhorn" {
		t.Fatalf("Status.Source = %+v, want the source's effective values (dbName=%s, dbadmin, 18, 5432, longhorn)", got, source.Name)
	}
}

// status.appliedSpec — what the source was provisioned with — wins over the
// operator's *current* defaults, which may have changed since.
func TestDBSnapshotAdmissionPrefersAppliedSpecOverCurrentDefaults(t *testing.T) {
	source := availableSourceInstance()
	source.Status.AppliedSpec = &dbaasv1.AppliedSpec{
		DBName: "orders", MasterUsername: "legacy_admin", Port: 6432, StorageType: "longhorn-fast",
	}
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	c := testutil.NewClient(t, source, snap)
	r := &DBSnapshotReconciler{Client: c, APIReader: c, Harvester: &testutil.StubHarvester{},
		DatabaseDefaults: operatorconfig.DatabaseDefaults{MasterUsername: "platform_admin", Port: 5432, StorageClass: "longhorn"}}

	reconcileSnapshot(t, r, snap)

	got := getSnapshot(t, c, snap).Status.Source
	if got == nil || got.DBName != "orders" || got.MasterUsername != "legacy_admin" || got.Port != 6432 || got.StorageType != "longhorn-fast" {
		t.Fatalf("Status.Source = %+v, want the values recorded in status.appliedSpec", got)
	}
}

// ---- source deletion ----

func deletingSource() *dbaasv1.DBInstance {
	source := availableSourceInstance()
	now := metav1.Now()
	source.DeletionTimestamp = &now
	source.Finalizers = []string{dbaasv1.FinalizerName}
	source.Status.Phase = dbaasv1.StatusDeleting
	return source
}

func snapshotHoldHeld(t *testing.T, c client.Client, source *dbaasv1.DBInstance) bool {
	t.Helper()
	_, held, err := (backup.Holds{Live: c, Writer: c}).Held(context.Background(), source.Namespace, backup.SnapshotHoldName(source.UID))
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	return held
}

func TestDBSnapshotRejectsWhenSourceIsBeingDeleted(t *testing.T) {
	source := deletingSource()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	reconcileSnapshot(t, r, snap)

	cond := getSnapshot(t, c, snap).Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonSnapshotSourceDeleting) {
		t.Fatalf("Ready condition = %+v, want False/SourceDeleting", cond)
	}
	if stub.CreateVMBackupCalls != 0 || snapshotHoldHeld(t, c, source) {
		t.Fatal("no backup may start, nor hold be taken, on a source being deleted")
	}
}

// Check-then-lock: the cache may not have seen the source's deletion yet
// when the hold is taken. The live re-check under the hold must catch it —
// source teardown proceeds once it sees the hold free, so a backup started
// here would read a VM being deleted.
func TestDBSnapshotRechecksSourceLiveAfterTakingTheHold(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)
	r.APIReader = interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := cl.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if inst, ok := obj.(*dbaasv1.DBInstance); ok {
				now := metav1.Now()
				inst.DeletionTimestamp = &now // the API server already has the delete
			}
			return nil
		},
	})

	reconcileSnapshot(t, r, snap)

	cond := getSnapshot(t, c, snap).Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Reason != string(dbaasv1.ReasonSnapshotSourceDeleting) {
		t.Fatalf("Ready condition = %+v, want reason SourceDeleting", cond)
	}
	if stub.CreateVMBackupCalls != 0 {
		t.Fatal("no backup may start once the live source is being deleted")
	}
	if snapshotHoldHeld(t, c, source) {
		t.Fatal("the hold taken before the re-check must be released")
	}
}

// A backup already running when its source starts deleting is tracked to
// its end, not rejected: source teardown waits for its hold, and a rejected
// snapshot would never release it.
func TestDBSnapshotInFlightBackupFinishesWhileSourceDeletes(t *testing.T) {
	source := deletingSource()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	snap.Status.Source = sourceMetadataFrom(availableSourceInstance(), operatorconfig.DatabaseDefaults{})
	snap.Status.SetCondition(metav1.Condition{
		Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionFalse,
		Reason: string(dbaasv1.ReasonSnapshotBackupInProgress), Message: "waiting",
	})
	stub := &testutil.StubHarvester{VMBackupPresent: true}
	r, c := newSnapshotReconciler(t, stub, source, snap)
	if _, err := (backup.Holds{Live: c, Writer: c}).Acquire(context.Background(), source.Namespace, backup.SnapshotHoldName(source.UID),
		snapshotHolderIdentity(snap), instanceOwnerRef(source), nil); err != nil {
		t.Fatalf("seed our hold: %v", err)
	}

	reconcileSnapshot(t, r, snap)

	cond := getSnapshot(t, c, snap).Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Reason != string(dbaasv1.ReasonSnapshotBackupInProgress) {
		t.Fatalf("Ready condition = %+v, want still BackupInProgress", cond)
	}
	if !snapshotHoldHeld(t, c, source) {
		t.Fatal("the hold must stay while the backup runs")
	}

	stub.VMBackupStatus = backupReadyStatus()
	reconcileSnapshot(t, r, getSnapshot(t, c, snap))

	cond = getSnapshot(t, c, snap).Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %+v, want True", cond)
	}
	if snapshotHoldHeld(t, c, source) {
		t.Fatal("the hold must be released once the backup is ready")
	}
	if stub.CreateVMBackupCalls != 0 {
		t.Fatal("an existing backup must not be re-created")
	}
}

// A same-named instance that replaced the source this snapshot admitted is
// not its source.
func TestDBSnapshotRejectsWhenSourceWasReplaced(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	snap.Status.Source = &dbaasv1.SourceMetadata{InstanceUID: "the-original-uid"}
	stub := &testutil.StubHarvester{VMBackupPresent: true}
	r, c := newSnapshotReconciler(t, stub, source, snap)

	reconcileSnapshot(t, r, snap)

	cond := getSnapshot(t, c, snap).Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Reason != string(dbaasv1.ReasonSnapshotSourceNotFound) {
		t.Fatalf("Ready condition = %+v, want reason SourceNotFound", cond)
	}
}

// A snapshot waiting on the hold (e.g. behind a repave) records nothing yet:
// what it records must be the source as backed up, read under the hold —
// not as it was before a resize or repave that ran while it waited.
func TestDBSnapshotCapturesSourceUnderTheHoldNotWhileWaiting(t *testing.T) {
	ctx := context.Background()
	source := availableSourceInstance()
	source.Status.CurrentImageRevision = "r1"
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)
	holds := backup.Holds{Live: c, Writer: c}
	if _, err := holds.Acquire(ctx, source.Namespace, backup.SnapshotHoldName(source.UID), "repave", instanceOwnerRef(source), nil); err != nil {
		t.Fatalf("seed repave hold: %v", err)
	}

	reconcileSnapshot(t, r, snap)
	if got := getSnapshot(t, c, snap); got.Status.Source != nil {
		t.Fatalf("Status.Source = %+v recorded while waiting for the hold, want nil", got.Status.Source)
	}

	// Repave and a resize finish, then release the hold.
	var latest dbaasv1.DBInstance
	if err := c.Get(ctx, client.ObjectKeyFromObject(source), &latest); err != nil {
		t.Fatalf("get source: %v", err)
	}
	latest.Spec.AllocatedStorage = 40
	if err := c.Update(ctx, &latest); err != nil {
		t.Fatalf("update source: %v", err)
	}
	latest.Status.CurrentImageRevision = "r2"
	if err := c.Status().Update(ctx, &latest); err != nil {
		t.Fatalf("update source status: %v", err)
	}
	if err := holds.Release(ctx, source.Namespace, backup.SnapshotHoldName(source.UID), "repave"); err != nil {
		t.Fatalf("release repave hold: %v", err)
	}

	reconcileSnapshot(t, r, getSnapshot(t, c, snap))

	got := getSnapshot(t, c, snap).Status.Source
	if got == nil || got.AllocatedStorage != 40 || got.ImageRevision != "r2" {
		t.Fatalf("Status.Source = %+v, want allocatedStorage 40 and imageRevision r2 (the source as backed up)", got)
	}
}

// ---- events ----

func TestDBSnapshotAnnouncesEachTransitionOnce(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	stub := &testutil.StubHarvester{}
	r, c := newSnapshotReconciler(t, stub, source, snap)
	recorder := record.NewFakeRecorder(10)
	r.Recorder = recorder

	reconcileSnapshot(t, r, snap)
	reconcileSnapshot(t, r, getSnapshot(t, c, snap)) // still in progress
	stub.VMBackupStatus = backupReadyStatus()
	reconcileSnapshot(t, r, getSnapshot(t, c, snap))

	events := drainEvents(recorder)
	if len(events) != 2 || !strings.HasPrefix(events[0], "Normal BackupInProgress ") || !strings.HasPrefix(events[1], "Normal BackupReady ") {
		t.Fatalf("events = %q, want one BackupInProgress then one BackupReady", events)
	}
}

func TestDBSnapshotRejectionIsAWarning(t *testing.T) {
	source := deletingSource()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	r, _ := newSnapshotReconciler(t, &testutil.StubHarvester{}, source, snap)
	recorder := record.NewFakeRecorder(10)
	r.Recorder = recorder

	reconcileSnapshot(t, r, snap)

	events := drainEvents(recorder)
	if len(events) != 1 || !strings.HasPrefix(events[0], "Warning SourceDeleting ") {
		t.Fatalf("events = %q, want one Warning SourceDeleting", events)
	}
}

// Blocked deletion is visible in status (Ready=False) and announced once,
// however long the restore keeps it waiting.
func TestDBSnapshotDeletionBlockedByRestoreIsVisibleAndAnnouncedOnce(t *testing.T) {
	source := availableSourceInstance()
	snap := testSnapshot()
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	snap.Status.Source = &dbaasv1.SourceMetadata{InstanceUID: source.UID}
	now := metav1.Now()
	snap.DeletionTimestamp = &now
	stub := &testutil.StubHarvester{VMBackupPresent: true}
	r, c := newSnapshotReconciler(t, stub, source, snap)
	recorder := record.NewFakeRecorder(10)
	r.Recorder = recorder
	restoreHold := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Name: backup.RestoreHoldName("some-restore-uid"), Namespace: source.Namespace,
		Labels: map[string]string{backup.SourceUIDLabel: string(source.UID)},
	}}
	if err := c.Create(context.Background(), restoreHold); err != nil {
		t.Fatalf("seed restore hold: %v", err)
	}

	reconcileSnapshot(t, r, snap)
	reconcileSnapshot(t, r, getSnapshot(t, c, snap))

	cond := getSnapshot(t, c, snap).Status.GetCondition(dbaasv1.ConditionSnapshotReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonSnapshotDeletionWaitingForRestore) {
		t.Fatalf("Ready = %+v, want False/DeletionWaitingForRestore", cond)
	}
	events := drainEvents(recorder)
	if len(events) != 1 || !strings.HasPrefix(events[0], "Warning DeletionWaitingForRestore ") {
		t.Fatalf("events = %q, want exactly one Warning DeletionWaitingForRestore", events)
	}
	if stub.DeleteVMBackupCalls != 0 {
		t.Fatal("the backend backup must survive while a restore holds it")
	}
}
