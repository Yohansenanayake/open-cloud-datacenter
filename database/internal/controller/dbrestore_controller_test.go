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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/backup"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/ensure"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/testutil"
)

// ---- fixtures ----

// readySnapshot is a DBSnapshot as DBSnapshotReconciler leaves it once the
// backup completes: source metadata captured, Ready.
func readySnapshot() *dbaasv1.DBSnapshot {
	snap := testSnapshot()
	snap.UID = "orders-before-upgrade-uid"
	snap.Status.Source = &dbaasv1.SourceMetadata{
		InstanceUID:      "orders-uid",
		DBName:           "appdb",
		MasterUsername:   "dbadmin",
		EngineVersion:    "16",
		Port:             5432,
		StorageType:      "longhorn",
		AllocatedStorage: 20,
	}
	snap.Status.DataVolumeSnapshotName = "orders-before-upgrade-1-volume-pg-orders-data"
	snap.Status.SetCondition(metav1.Condition{
		Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionTrue,
		Reason: string(dbaasv1.ReasonSnapshotBackupReady), Message: "backup is ready to use",
	})
	return snap
}

func testRestore() *dbaasv1.DBRestore {
	return &dbaasv1.DBRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orders-restore", Namespace: "tenant-a", UID: "orders-restore-uid",
			Finalizers: []string{dbaasv1.DBRestoreFinalizerName},
		},
		Spec: dbaasv1.DBRestoreSpec{
			SnapshotRef:        corev1.LocalObjectReference{Name: "orders-before-upgrade"},
			TargetInstanceName: "orders-restored",
			DBInstanceClass:    "db.t3.small",
			NetworkRef:         "default/vm-network",
			AllocatedStorage:   20,
		},
	}
}

// capturedRestore is a DBRestore whose snapshot inputs have been captured.
func capturedRestore(snap *dbaasv1.DBSnapshot) *dbaasv1.DBRestore {
	restore := testRestore()
	restore.Status = dbaasv1.DBRestoreStatus{
		Stage:                  dbaasv1.RestoreStageRestoringVolume,
		SnapshotUID:            string(snap.UID),
		SourceInstanceUID:      snap.Status.Source.InstanceUID,
		DataVolumeSnapshotName: snap.Status.DataVolumeSnapshotName,
		Resolved: &dbaasv1.ResolvedRestoreFields{
			DBName:         snap.Status.Source.DBName,
			MasterUsername: snap.Status.Source.MasterUsername,
			EngineVersion:  snap.Status.Source.EngineVersion,
			Port:           snap.Status.Source.Port,
			StorageType:    snap.Status.Source.StorageType,
		},
	}
	return restore
}

func restorePVCName(restore *dbaasv1.DBRestore) string {
	return ensure.RestoreDataVolumeName(restore.Spec.TargetInstanceName, restore.UID)
}

// ourPVC is the restore PVC exactly as this restore would have created it.
func ourPVC(restore *dbaasv1.DBRestore, phase corev1.PersistentVolumeClaimPhase) *corev1.PersistentVolumeClaim {
	return testutil.RestorePVC(restore.Namespace, restorePVCName(restore), restore.Status.DataVolumeSnapshotName,
		map[string]string{dbaasv1.LabelDBRestoreUID: string(restore.UID)}, phase)
}

// fixtureVolumeSnapshot is readySnapshot()'s DataVolumeSnapshotName.
const fixtureVolumeSnapshot = "orders-before-upgrade-1-volume-pg-orders-data"

// stubWithPVCs seeds the given PVCs, and the fixture snapshot's
// VolumeSnapshot as live and ready to use.
func stubWithPVCs(pvcs ...*corev1.PersistentVolumeClaim) *testutil.StubHarvester {
	stub := &testutil.StubHarvester{
		PVCs:            map[string]*corev1.PersistentVolumeClaim{},
		VolumeSnapshots: map[string]harvester.VolumeSnapshotState{fixtureVolumeSnapshot: {ReadyToUse: true}},
	}
	for _, pvc := range pvcs {
		stub.PVCs[pvc.Name] = pvc
	}
	return stub
}

// ourTarget is the target DBInstance as this restore creates it.
func ourTarget(restore *dbaasv1.DBRestore) *dbaasv1.DBInstance {
	return &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name: restore.Spec.TargetInstanceName, Namespace: restore.Namespace,
			UID: "orders-restored-uid", Generation: 1,
		},
		Spec: dbaasv1.DBInstanceSpec{
			DBInstanceClass: "db.t3.small", NetworkRef: "default/vm-network", AllocatedStorage: 20,
			RestoredFrom: &dbaasv1.RestoredFromRef{DBRestoreName: restore.Name, DBRestoreUID: restore.UID},
		},
	}
}

func withCondition(inst *dbaasv1.DBInstance, condType string, status metav1.ConditionStatus, reason dbaasv1.ConditionReason, msg string) *dbaasv1.DBInstance {
	inst.SetCurrentCondition(condType, status, reason, msg)
	return inst
}

func heldBy(restore *dbaasv1.DBRestore) *coordinationv1.Lease {
	holder := string(restore.UID)
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: backup.RestoreHoldName(restore.UID), Namespace: restore.Namespace},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder},
	}
}

// ---- harness ----

func newRestoreReconciler(t *testing.T, stub *testutil.StubHarvester, objs ...client.Object) (*DBRestoreReconciler, client.Client) {
	t.Helper()
	c := testutil.NewClient(t, objs...)
	return &DBRestoreReconciler{Client: c, APIReader: c, Harvester: stub, DatabaseDefaults: operatorconfig.DatabaseDefaults{StorageClass: "longhorn-default"}}, c
}

func reconcileRestore(t *testing.T, r *DBRestoreReconciler, restore *dbaasv1.DBRestore) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: restore.Namespace, Name: restore.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func getRestore(t *testing.T, c client.Client, restore *dbaasv1.DBRestore) *dbaasv1.DBRestore {
	t.Helper()
	var got dbaasv1.DBRestore
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(restore), &got); err != nil {
		t.Fatalf("Get DBRestore: %v", err)
	}
	return &got
}

func wantStatus(t *testing.T, got *dbaasv1.DBRestore, stage string, reason dbaasv1.ConditionReason) {
	t.Helper()
	if got.Status.Stage != stage || got.Status.Reason != string(reason) {
		t.Fatalf("Status = %+v, want %s/%s", got.Status, stage, reason)
	}
}

func holdExists(t *testing.T, c client.Client, restore *dbaasv1.DBRestore) bool {
	t.Helper()
	var lease coordinationv1.Lease
	err := c.Get(context.Background(), types.NamespacedName{Namespace: restore.Namespace, Name: backup.RestoreHoldName(restore.UID)}, &lease)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("Get restore hold: %v", err)
	}
	return err == nil
}

func targetExists(t *testing.T, c client.Client, restore *dbaasv1.DBRestore) (*dbaasv1.DBInstance, bool) {
	t.Helper()
	var inst dbaasv1.DBInstance
	err := c.Get(context.Background(), types.NamespacedName{Namespace: restore.Namespace, Name: restore.Spec.TargetInstanceName}, &inst)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("Get target: %v", err)
	}
	return &inst, err == nil
}

// ---- finalizer ----

func TestDBRestoreFirstReconcileOnlyAddsFinalizer(t *testing.T) {
	restore := testRestore()
	restore.Finalizers = nil
	stub := stubWithPVCs()
	r, c := newRestoreReconciler(t, stub, restore, readySnapshot())

	reconcileRestore(t, r, restore)

	got := getRestore(t, c, restore)
	if !containsString(got.Finalizers, dbaasv1.DBRestoreFinalizerName) {
		t.Fatal("finalizer not added on first reconcile")
	}
	if got.Status.Stage != "" || stub.CreateRestorePVCCalls != 0 || holdExists(t, c, restore) {
		t.Fatal("nothing may be captured, held, or created before the finalizer is in place")
	}
}

// ---- Preparing: snapshot admission ----

func TestDBRestoreFailsWhenSnapshotNotFound(t *testing.T) {
	restore := testRestore()
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreSnapshotNotFound)
}

func TestDBRestoreWaitsWithoutHoldingWhileSnapshotNotReady(t *testing.T) {
	restore := testRestore()
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore, testSnapshot()) // no Ready condition yet

	res := reconcileRestore(t, r, restore)

	if res.RequeueAfter != restoreSnapshotPollRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.RequeueAfter, restoreSnapshotPollRequeue)
	}
	got := getRestore(t, c, restore)
	wantStatus(t, got, dbaasv1.RestoreStagePreparing, dbaasv1.ReasonRestoreSnapshotNotReady)
	if got.Status.SnapshotUID != "" || holdExists(t, c, restore) {
		t.Fatal("nothing may be captured or held before the snapshot is Ready")
	}
}

func TestDBRestoreFailsWhenSnapshotWillNeverBeReady(t *testing.T) {
	snap := testSnapshot()
	snap.Status.SetCondition(metav1.Condition{
		Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionFalse,
		Reason: string(dbaasv1.ReasonSnapshotBackupFailed), Message: "backend backup failed",
	})
	restore := testRestore()
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore, snap)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreSnapshotFailed)
}

func TestDBRestoreFailsWhenAllocatedStorageTooSmall(t *testing.T) {
	restore := testRestore()
	restore.Spec.AllocatedStorage = 10 // snapshot recorded 20
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore, readySnapshot())

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreAllocatedStorageTooSmall)
}

func TestDBRestoreCapturesInputsHoldsAndCreatesLabeledPVC(t *testing.T) {
	snap := readySnapshot()
	restore := testRestore()
	stub := stubWithPVCs()
	r, c := newRestoreReconciler(t, stub, restore, snap)

	res := reconcileRestore(t, r, restore)

	got := getRestore(t, c, restore)
	wantStatus(t, got, dbaasv1.RestoreStageRestoringVolume, dbaasv1.ReasonRestoreVolumeRestoring)
	if res.RequeueAfter != restorePVCPollRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.RequeueAfter, restorePVCPollRequeue)
	}
	if got.Status.SnapshotUID != string(snap.UID) || got.Status.SourceInstanceUID != snap.Status.Source.InstanceUID ||
		got.Status.DataVolumeSnapshotName != snap.Status.DataVolumeSnapshotName {
		t.Fatalf("captured identity = %+v, want it taken from the snapshot", got.Status)
	}
	want := dbaasv1.ResolvedRestoreFields{DBName: "appdb", MasterUsername: "dbadmin", EngineVersion: "16", Port: 5432, StorageType: "longhorn"}
	if got.Status.Resolved == nil || *got.Status.Resolved != want {
		t.Fatalf("Resolved = %+v, want %+v", got.Status.Resolved, want)
	}
	if !holdExists(t, c, restore) {
		t.Fatal("restore hold must be held before the PVC is created")
	}
	pvc := stub.PVCs[restorePVCName(restore)]
	if pvc == nil || pvc.Labels[dbaasv1.LabelDBRestoreUID] != string(restore.UID) {
		t.Fatalf("restore PVC = %+v, want it created and labeled with the DBRestore UID", pvc)
	}
	if stub.LastRestorePVCSnapshotName != snap.Status.DataVolumeSnapshotName || stub.LastRestorePVCSizeGB != 20 || stub.LastRestorePVCStorageClass != "longhorn" {
		t.Fatalf("CreateRestorePVC args = %q/%d/%q", stub.LastRestorePVCSnapshotName, stub.LastRestorePVCSizeGB, stub.LastRestorePVCStorageClass)
	}
}

func TestDBRestoreUsesDatabaseDefaultsStorageClassWhenSnapshotStorageTypeEmpty(t *testing.T) {
	snap := readySnapshot()
	snap.Status.Source.StorageType = ""
	restore := testRestore()
	stub := stubWithPVCs()
	r, _ := newRestoreReconciler(t, stub, restore, snap)

	reconcileRestore(t, r, restore)

	if stub.LastRestorePVCStorageClass != "longhorn-default" {
		t.Fatalf("storage class = %q, want the DatabaseDefaults fallback", stub.LastRestorePVCStorageClass)
	}
}

func TestDBRestoreHoldLeaseCarriesSourceUIDLabelAndOwnerRef(t *testing.T) {
	snap := readySnapshot()
	restore := testRestore()
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore, snap)

	reconcileRestore(t, r, restore)

	var lease coordinationv1.Lease
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: restore.Namespace, Name: backup.RestoreHoldName(restore.UID)}, &lease); err != nil {
		t.Fatalf("Get restore hold: %v", err)
	}
	if lease.Labels[backup.SourceUIDLabel] != string(snap.Status.Source.InstanceUID) {
		t.Fatalf("Lease labels = %+v, want %s=%s", lease.Labels, backup.SourceUIDLabel, snap.Status.Source.InstanceUID)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != string(restore.UID) {
		t.Fatalf("Lease holder = %v, want the DBRestore UID", lease.Spec.HolderIdentity)
	}
	if len(lease.OwnerReferences) != 1 || lease.OwnerReferences[0].Kind != "DBRestore" || lease.OwnerReferences[0].UID != restore.UID {
		t.Fatalf("Lease owners = %+v, want the DBRestore", lease.OwnerReferences)
	}
}

// ---- out of band: the snapshot while it's still being read ----

// Finding 2: the snapshot is re-verified live (under the hold) for as long
// as the PVC is still copying from it.
func TestDBRestoreFailsWhenSnapshotDeletedBeforeVolumeBound(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimPending)), restore) // snapshot gone

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreSnapshotNotFound)
	if holdExists(t, c, restore) {
		t.Fatal("hold must be released once the restore has failed")
	}
}

func TestDBRestoreFailsWhenSnapshotIsBeingDeletedBeforeVolumeBound(t *testing.T) {
	snap := readySnapshot()
	now := metav1.Now()
	snap.DeletionTimestamp = &now
	snap.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	restore := capturedRestore(snap)
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimPending)), restore, snap)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreSnapshotDeleting)
}

func TestDBRestoreFailsWhenSnapshotReplacedBySameNamedObject(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	replacement := readySnapshot()
	replacement.UID = "someone-elses-snapshot-uid"
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore, replacement)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreSnapshotReplaced)
}

// Once the PVC is Bound the data has been copied in — the snapshot is no
// longer depended on, so losing it then must not fail the restore.
func TestDBRestoreNoLongerNeedsSnapshotOnceVolumeBound(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	stub := stubWithPVCs(ourPVC(restore, corev1.ClaimBound))
	delete(stub.VolumeSnapshots, fixtureVolumeSnapshot)
	r, c := newRestoreReconciler(t, stub, restore) // DBSnapshot and VolumeSnapshot both gone

	reconcileRestore(t, r, restore)

	got := getRestore(t, c, restore)
	wantStatus(t, got, dbaasv1.RestoreStageStartingDatabase, dbaasv1.ReasonRestoreTargetStarting)
}

// ---- out of band: the VolumeSnapshot behind a Ready DBSnapshot ----

// DBSnapshot.Ready is only a record of when its backup completed; the
// VolumeSnapshot the PVC actually reads from is checked live.
func TestDBRestoreChecksVolumeSnapshotBehindReadyDBSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      *harvester.VolumeSnapshotState // nil: VolumeSnapshot missing
		wantStage  string
		wantReason dbaasv1.ConditionReason
	}{
		{"missing", nil, dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreVolumeSnapshotMissing},
		{"deleting", &harvester.VolumeSnapshotState{ReadyToUse: true, Deleting: true}, dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreVolumeSnapshotMissing},
		{"errored", &harvester.VolumeSnapshotState{ErrorMessage: "content lost"}, dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreVolumeSnapshotFailed},
		{"not ready to use", &harvester.VolumeSnapshotState{}, dbaasv1.RestoreStagePreparing, dbaasv1.ReasonRestoreSnapshotNotReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := readySnapshot()
			restore := testRestore()
			stub := stubWithPVCs()
			delete(stub.VolumeSnapshots, fixtureVolumeSnapshot)
			if tc.state != nil {
				stub.VolumeSnapshots[fixtureVolumeSnapshot] = *tc.state
			}
			r, c := newRestoreReconciler(t, stub, restore, snap)

			reconcileRestore(t, r, restore)

			wantStatus(t, getRestore(t, c, restore), tc.wantStage, tc.wantReason)
			if stub.CreateRestorePVCCalls != 0 {
				t.Fatal("no PVC may be created from a VolumeSnapshot that isn't usable")
			}
		})
	}
}

// The same check guards later passes too, while the PVC is still copying.
func TestDBRestoreFailsWhenVolumeSnapshotDisappearsBeforeVolumeBound(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	stub := stubWithPVCs(ourPVC(restore, corev1.ClaimPending))
	delete(stub.VolumeSnapshots, fixtureVolumeSnapshot)
	r, c := newRestoreReconciler(t, stub, restore, snap)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreVolumeSnapshotMissing)
}

// ---- decisions come from the uncached reader, not a lagging cache ----

// splitReconciler models a lagging informer cache: Client (the cache) lacks
// objects the API server (APIReader) already has.
func splitReconciler(t *testing.T, stub *testutil.StubHarvester, cached []client.Object, live []client.Object) (*DBRestoreReconciler, client.Client) {
	t.Helper()
	c := testutil.NewClient(t, cached...)
	return &DBRestoreReconciler{Client: c, APIReader: testutil.NewClient(t, live...), Harvester: stub}, c
}

// A DBSnapshot created a moment before its DBRestore may not be in the cache
// yet; that must not fail the restore as SnapshotNotFound.
func TestDBRestoreFindsSnapshotTheCacheHasNotSeenYet(t *testing.T) {
	snap := readySnapshot()
	restore := testRestore()
	r, c := splitReconciler(t, stubWithPVCs(), []client.Object{restore}, []client.Object{restore, snap})

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageRestoringVolume, dbaasv1.ReasonRestoreVolumeRestoring)
}

// Just after we create the target, the DBInstance cache may not have it yet;
// that must not be mistaken for "our target was lost".
func TestDBRestoreFindsTargetTheCacheHasNotSeenYet(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := ourTarget(restore)
	restore.Status.TargetInstanceUID = target.UID
	r, c := splitReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)),
		[]client.Object{restore, snap}, []client.Object{restore, snap, target})

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageStartingDatabase, dbaasv1.ReasonRestoreTargetStarting)
}

// countingReader counts Gets by object type, to prove which reads went to
// the API server.
type countingReader struct {
	client.Reader
	gets map[string]int
}

func (c *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.gets[fmt.Sprintf("%T", obj)]++
	return c.Reader.Get(ctx, key, obj, opts...)
}

// The normal path reads the DBSnapshot and DBInstance from the cache only;
// uncached reads are reserved for confirming irreversible decisions (and
// for hold Leases, which are always live).
func TestDBRestoreNormalPassesMakeNoUncachedSnapshotOrTargetReads(t *testing.T) {
	snap := readySnapshot()
	for name, setup := range map[string]func(*dbaasv1.DBRestore) ([]client.Object, *testutil.StubHarvester){
		"volume restoring": func(restore *dbaasv1.DBRestore) ([]client.Object, *testutil.StubHarvester) {
			return []client.Object{restore, snap}, stubWithPVCs(ourPVC(restore, corev1.ClaimPending))
		},
		"target starting": func(restore *dbaasv1.DBRestore) ([]client.Object, *testutil.StubHarvester) {
			target := ourTarget(restore)
			restore.Status.TargetInstanceUID = target.UID
			return []client.Object{restore, snap, target}, stubWithPVCs(ourPVC(restore, corev1.ClaimBound))
		},
	} {
		t.Run(name, func(t *testing.T) {
			restore := capturedRestore(snap)
			objs, stub := setup(restore)
			c := testutil.NewClient(t, objs...)
			live := &countingReader{Reader: c, gets: map[string]int{}}
			r := &DBRestoreReconciler{Client: c, APIReader: live, Harvester: stub}

			reconcileRestore(t, r, restore)

			if n := live.gets["*v1alpha1.DBSnapshot"] + live.gets["*v1alpha1.DBInstance"]; n != 0 {
				t.Fatalf("uncached DBSnapshot/DBInstance reads = %v, want none on a normal pass", live.gets)
			}
			if got := getRestore(t, c, restore); got.Status.Stage == dbaasv1.RestoreStageFailed {
				t.Fatalf("Status = %+v, want a normal (non-failed) pass", got.Status)
			}
		})
	}
}

// Deleting the target on rejection is irreversible: a stale cached copy
// showing Accepted=False (from before the owner fixed the spec) must be
// confirmed against the API server, and the fixed target left alone.
func TestDBRestoreNeverDeletesTargetOverAStaleCachedRejection(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	stale := withCondition(ourTarget(restore), dbaasv1.ConditionAccepted, metav1.ConditionFalse, dbaasv1.ReasonInvalidClass, "unknown dbInstanceClass")
	restore.Status.TargetInstanceUID = stale.UID
	fixed := ourTarget(restore)
	fixed.Generation = 2
	fixed.Spec.DBInstanceClass = "db.t3.medium"
	withCondition(fixed, dbaasv1.ConditionAccepted, metav1.ConditionTrue, dbaasv1.ReasonSpecAccepted, "accepted")
	r, c := splitReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)),
		[]client.Object{restore.DeepCopy(), snap.DeepCopy(), stale}, []client.Object{restore, snap, fixed})

	reconcileRestore(t, r, restore)

	if got, exists := targetExists(t, c, restore); !exists || !got.DeletionTimestamp.IsZero() {
		t.Fatal("target must not be deleted on the strength of a stale cached rejection")
	}
	if got := getRestore(t, c, restore); got.Status.Stage == dbaasv1.RestoreStageFailed {
		t.Fatalf("Status = %+v, must not fail", got.Status)
	}
}

// Cancellation deletes the target, so it decides from the API server: a
// target the cache still shows as starting but that is already Ready must
// be left alone (finding 1's race, with a lagging cache on top).
func TestDBRestoreDeletionNeverCancelsATargetReadyOnTheAPIServer(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	cachedTarget := ourTarget(restore) // cache: not Ready yet
	restore.Status.TargetInstanceUID = cachedTarget.UID
	restore.Status.Stage = dbaasv1.RestoreStageStartingDatabase
	deletingRestore(restore)
	liveTarget := withCondition(ourTarget(restore), dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "ready")
	r, c := splitReconciler(t, stubWithPVCs(), []client.Object{restore.DeepCopy(), cachedTarget}, []client.Object{restore, liveTarget})

	reconcileRestore(t, r, restore)

	if got, exists := targetExists(t, c, restore); !exists || !got.DeletionTimestamp.IsZero() {
		t.Fatal("a target that is Ready on the API server must never be cancelled")
	}
}

// ---- out of band: the restore PVC ----

// Finding 3: a PVC under our name that isn't ours (e.g. a blank one
// Harvester recreated) is never accepted just because it exists and binds.
func TestDBRestoreFailsWhenPVCUnderOurNameIsNotOurs(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	blank := ourPVC(restore, corev1.ClaimBound)
	blank.Labels = nil
	blank.Spec.DataSource = nil
	r, c := newRestoreReconciler(t, stubWithPVCs(blank), restore, snap)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestorePVCConflict)
	if _, exists := targetExists(t, c, restore); exists {
		t.Fatal("target must not be created on top of a PVC that isn't ours")
	}
}

func TestDBRestoreFailsWhenPVCRestoresFromADifferentSnapshot(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	pvc := ourPVC(restore, corev1.ClaimBound)
	pvc.Spec.DataSource.Name = "some-other-volumesnapshot"
	r, c := newRestoreReconciler(t, stubWithPVCs(pvc), restore, snap)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestorePVCConflict)
}

// Finding 7.
func TestDBRestoreFailsWhenPVCLost(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimLost)), restore, snap)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestorePVCLost)
}

func TestDBRestoreFailsWithoutRecreatingPVCDeletedAfterTargetCreated(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := ourTarget(restore)
	restore.Status.TargetInstanceUID = target.UID
	stub := stubWithPVCs() // PVC deleted out of band
	r, c := newRestoreReconciler(t, stub, restore, snap, target)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestorePVCLost)
	if stub.CreateRestorePVCCalls != 0 {
		t.Fatal("must not recreate the PVC once the target's VM expects it")
	}
}

func TestDBRestoreWaitsWhilePVCPending(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	stub := stubWithPVCs(ourPVC(restore, corev1.ClaimPending))
	r, c := newRestoreReconciler(t, stub, restore, snap)

	res := reconcileRestore(t, r, restore)

	if res.RequeueAfter != restorePVCPollRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.RequeueAfter, restorePVCPollRequeue)
	}
	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageRestoringVolume, dbaasv1.ReasonRestoreVolumeRestoring)
	if _, exists := targetExists(t, c, restore); exists {
		t.Fatal("target must not be created before the PVC is Bound")
	}
}

// ---- StartingDatabase: the target DBInstance ----

func TestDBRestoreCreatesTargetOnceVolumeBoundAndRecordsItsUID(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	restore.Spec.Backup = &dbaasv1.BackupSpec{}
	restore.Spec.VMPassword = "debug-pw"
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap)

	reconcileRestore(t, r, restore)

	target, exists := targetExists(t, c, restore)
	if !exists {
		t.Fatal("target DBInstance not created")
	}
	s := target.Spec
	if s.DBInstanceClass != "db.t3.small" || s.NetworkRef != "default/vm-network" || s.AllocatedStorage != 20 || s.Backup == nil {
		t.Fatalf("target Spec = %+v, want DBRestore.spec parameters", s)
	}
	if s.DBName != "appdb" || s.MasterUsername != "dbadmin" || s.EngineVersion != "16" || s.Port != 5432 || s.StorageType != "longhorn" {
		t.Fatalf("target Spec = %+v, want fields inherited from the snapshot", s)
	}
	if s.VMPassword != "debug-pw" {
		t.Fatalf("target VMPassword = %q, want it passed through from DBRestore.spec.vmPassword", s.VMPassword)
	}
	if s.RestoredFrom == nil || s.RestoredFrom.DBRestoreName != restore.Name || s.RestoredFrom.DBRestoreUID != restore.UID {
		t.Fatalf("RestoredFrom = %+v, want this DBRestore", s.RestoredFrom)
	}
	got := getRestore(t, c, restore)
	wantStatus(t, got, dbaasv1.RestoreStageStartingDatabase, dbaasv1.ReasonRestoreTargetStarting)
	if got.Status.TargetInstanceUID != target.UID {
		t.Fatalf("TargetInstanceUID = %q, want %q", got.Status.TargetInstanceUID, target.UID)
	}
}

// The target was created but the status write recording it never landed:
// adopted via its live spec.restoredFrom link, not recreated.
func TestDBRestoreAdoptsTargetWhoseUIDWasNeverRecorded(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := ourTarget(restore)
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, target)

	reconcileRestore(t, r, restore)

	got := getRestore(t, c, restore)
	wantStatus(t, got, dbaasv1.RestoreStageStartingDatabase, dbaasv1.ReasonRestoreTargetStarting)
	if got.Status.TargetInstanceUID != target.UID {
		t.Fatalf("TargetInstanceUID = %q, want the adopted target's %q", got.Status.TargetInstanceUID, target.UID)
	}
}

// Finding 6: Phase is a projection; only conditions drive the outcome.
func TestDBRestoreIgnoresTargetPhaseAndWaitsForReadyCondition(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := ourTarget(restore)
	target.Status.Phase = dbaasv1.StatusAvailable // but no Ready condition
	restore.Status.TargetInstanceUID = target.UID
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, target)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageStartingDatabase, dbaasv1.ReasonRestoreTargetStarting)
}

func TestDBRestoreSucceedsAndReleasesHoldWhenTargetReady(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := withCondition(ourTarget(restore), dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "ready")
	restore.Status.TargetInstanceUID = target.UID
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, target, heldBy(restore))

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageSucceeded, dbaasv1.ReasonRestoreSucceeded)
	if holdExists(t, c, restore) {
		t.Fatal("hold must be released once Succeeded")
	}
}

// Hold-leak fix: failing after the hold was acquired in the same pass still
// releases it.
func TestDBRestoreNameConflictFailsReleasesHoldAndLeavesForeignInstanceAlone(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	foreign := ourTarget(restore)
	foreign.Spec.RestoredFrom = nil
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, foreign)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreTargetNameConflict)
	if holdExists(t, c, restore) {
		t.Fatal("hold leaked after a failure in the same pass that acquired it")
	}
	if got, exists := targetExists(t, c, restore); !exists || !got.DeletionTimestamp.IsZero() {
		t.Fatal("an instance this restore didn't create must never be touched")
	}
}

// Finding 4/5: permanent rejection tears the target down and fails in one
// pass — no "mid-teardown" flag to carry across passes.
func TestDBRestoreTargetRejectedIsDeletedAndFailsInOnePass(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := withCondition(ourTarget(restore), dbaasv1.ConditionAccepted, metav1.ConditionFalse, dbaasv1.ReasonInvalidClass, "unknown dbInstanceClass")
	target.Finalizers = []string{dbaasv1.FinalizerName} // DBInstanceReconciler's own teardown gate
	restore.Status.TargetInstanceUID = target.UID
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, target, heldBy(restore))

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreTargetRejected)
	if got, exists := targetExists(t, c, restore); exists && got.DeletionTimestamp.IsZero() {
		t.Fatal("rejected target must have been deleted")
	}
	if holdExists(t, c, restore) {
		t.Fatal("hold must be released once Failed")
	}
}

// Finding 5: a target we created that is gone is lost — never recreated.
func TestDBRestoreFailsWithoutRecreatingTargetDeletedOutOfBand(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	restore.Status.TargetInstanceUID = "orders-restored-uid" // created earlier, now gone
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreTargetLost)
	if _, exists := targetExists(t, c, restore); exists {
		t.Fatal("a lost target must never be recreated")
	}
}

func TestDBRestoreFailsWhenTargetIsBeingDeletedOutOfBand(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := ourTarget(restore)
	now := metav1.Now()
	target.DeletionTimestamp = &now
	target.Finalizers = []string{dbaasv1.FinalizerName}
	restore.Status.TargetInstanceUID = target.UID
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, target)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreTargetLost)
}

// Our target was deleted and someone recreated one under the same name from
// a copied manifest (same restoredFrom, new UID): not ours, never touched.
func TestDBRestoreTreatsSameNamedReplacementTargetAsLost(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	restore.Status.TargetInstanceUID = "the-original-target-uid"
	replacement := withCondition(ourTarget(restore), dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "ready")
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, replacement)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreTargetLost)
	if got, exists := targetExists(t, c, restore); !exists || !got.DeletionTimestamp.IsZero() {
		t.Fatal("the replacement instance must not be touched")
	}
}

// ---- the hold, reconciled to its desired state ----

// A finished restore releases its hold on every pass — this heals a release
// that failed, or never happened, on an earlier pass.
func TestDBRestoreFinishedRestoreReleasesLingeringHold(t *testing.T) {
	for _, stage := range []string{dbaasv1.RestoreStageSucceeded, dbaasv1.RestoreStageFailed} {
		t.Run(stage, func(t *testing.T) {
			snap := readySnapshot()
			restore := capturedRestore(snap)
			restore.Status.Stage = stage
			r, c := newRestoreReconciler(t, stubWithPVCs(), restore, heldBy(restore))

			reconcileRestore(t, r, restore)

			if holdExists(t, c, restore) {
				t.Fatal("a finished restore must not keep its hold")
			}
		})
	}
}

// ---- status persistence ----

// Finding 9: one patch per pass, and none at all when nothing changed.
func TestDBRestoreSteadyWaitingPassWritesNoStatus(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := ourTarget(restore)
	restore.Status.TargetInstanceUID = target.UID
	setRestoreProgress(restore, dbaasv1.RestoreStageStartingDatabase, dbaasv1.ReasonRestoreTargetStarting,
		`waiting for DBInstance "orders-restored" to become ready`)
	r, c := newRestoreReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, target)
	rvBefore := getRestore(t, c, restore).ResourceVersion

	reconcileRestore(t, r, restore)

	if rv := getRestore(t, c, restore).ResourceVersion; rv != rvBefore {
		t.Fatalf("ResourceVersion %s -> %s: a pass that changed nothing must not write status", rvBefore, rv)
	}
}

// ---- deletion ----

func deletingRestore(restore *dbaasv1.DBRestore) *dbaasv1.DBRestore {
	now := metav1.Now()
	restore.DeletionTimestamp = &now
	return restore
}

func TestDBRestoreDeletionCancelsInProgressRestoreAndWaitsForTarget(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := ourTarget(restore)
	target.Finalizers = []string{dbaasv1.FinalizerName} // stays until DBInstanceReconciler finishes
	restore.Status.TargetInstanceUID = target.UID
	deletingRestore(restore)
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore, target, heldBy(restore))

	res := reconcileRestore(t, r, restore)

	if res.RequeueAfter != restoreTargetTeardownPollRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.RequeueAfter, restoreTargetTeardownPollRequeue)
	}
	if got, _ := targetExists(t, c, restore); got.DeletionTimestamp.IsZero() {
		t.Fatal("cancellation must delete our in-progress target")
	}
	got := getRestore(t, c, restore)
	if !containsString(got.Finalizers, dbaasv1.DBRestoreFinalizerName) || !holdExists(t, c, restore) {
		t.Fatal("finalizer and hold must stay until the target is confirmed gone")
	}
	if got.Status.Reason != string(dbaasv1.ReasonRestoreCancelling) {
		t.Fatalf("Reason = %q, want %q", got.Status.Reason, dbaasv1.ReasonRestoreCancelling)
	}
}

func TestDBRestoreDeletionFinishesOnceTargetGone(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	restore.Status.TargetInstanceUID = "orders-restored-uid" // already torn down
	deletingRestore(restore)
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore, heldBy(restore))

	reconcileRestore(t, r, restore)

	if holdExists(t, c, restore) {
		t.Fatal("hold must be released before the finalizer comes off")
	}
	var gone dbaasv1.DBRestore
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(restore), &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("DBRestore Get err = %v, want NotFound (finalizer removed)", err)
	}
}

func TestDBRestoreDeletionOfSucceededRestoreNeverTouchesTarget(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := ourTarget(restore) // e.g. stopped by its owner after the restore: not Ready
	restore.Status.TargetInstanceUID = target.UID
	restore.Status.Stage = dbaasv1.RestoreStageSucceeded
	deletingRestore(restore)
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore, target)

	reconcileRestore(t, r, restore)

	if got, exists := targetExists(t, c, restore); !exists || !got.DeletionTimestamp.IsZero() {
		t.Fatal("a Succeeded restore's target must survive deletion of the DBRestore")
	}
}

// Finding 1: the Succeeded write never landed, but the target is live and
// Ready — deleting the DBRestore must not delete a healthy database.
func TestDBRestoreDeletionNeverTouchesLiveReadyTargetEvenIfSuccessNotRecorded(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := withCondition(ourTarget(restore), dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "ready")
	restore.Status.TargetInstanceUID = target.UID
	restore.Status.Stage = dbaasv1.RestoreStageStartingDatabase
	deletingRestore(restore)
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore, target, heldBy(restore))

	reconcileRestore(t, r, restore)

	if got, exists := targetExists(t, c, restore); !exists || !got.DeletionTimestamp.IsZero() {
		t.Fatal("a live, Ready target must never be deleted by cancellation")
	}
	if holdExists(t, c, restore) {
		t.Fatal("hold must be released")
	}
}

func TestDBRestoreDeletionNeverTouchesForeignSameNamedInstance(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	foreign := ourTarget(restore)
	foreign.Spec.RestoredFrom = nil
	deletingRestore(restore)
	r, c := newRestoreReconciler(t, stubWithPVCs(), restore, foreign)

	reconcileRestore(t, r, restore)

	if got, exists := targetExists(t, c, restore); !exists || !got.DeletionTimestamp.IsZero() {
		t.Fatal("an instance this restore didn't create must never be touched")
	}
}

// A snapshot whose recorded source settings are incomplete (taken before
// effective-value capture) fails fast with a reason, instead of producing a
// target whose guest refuses the restored disk and never becomes ready.
func TestDBRestoreFailsFastOnSnapshotWithoutEffectiveSourceSettings(t *testing.T) {
	for name, blank := range map[string]func(*dbaasv1.SourceMetadata){
		"dbName":         func(s *dbaasv1.SourceMetadata) { s.DBName = "" },
		"masterUsername": func(s *dbaasv1.SourceMetadata) { s.MasterUsername = "" },
		"engineVersion":  func(s *dbaasv1.SourceMetadata) { s.EngineVersion = "" },
	} {
		t.Run(name, func(t *testing.T) {
			snap := readySnapshot()
			blank(snap.Status.Source)
			restore := testRestore()
			stub := stubWithPVCs()
			r, c := newRestoreReconciler(t, stub, restore, snap)

			reconcileRestore(t, r, restore)

			wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreInvalidSnapshotState)
			if stub.CreateRestorePVCCalls != 0 || holdExists(t, c, restore) {
				t.Fatal("nothing may be held or created for an unusable snapshot")
			}
		})
	}
}

// restoreReconcilerFailingTargetCreate is a reconciler whose client fails
// every DBInstance Create with createErr — a stand-in for the API server's
// CRD validation, which the fake client doesn't evaluate.
func restoreReconcilerFailingTargetCreate(t *testing.T, createErr error, objs ...client.Object) (*DBRestoreReconciler, client.Client) {
	t.Helper()
	c := ctrlfake.NewClientBuilder().
		WithScheme(testutil.NewScheme(t)).
		WithStatusSubresource(&dbaasv1.DBInstance{}, &dbaasv1.DBSnapshot{}, &dbaasv1.DBRestore{}).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*dbaasv1.DBInstance); ok {
					return createErr
				}
				return cl.Create(ctx, obj, opts...)
			},
		}).Build()
	return &DBRestoreReconciler{Client: c, APIReader: c, Harvester: stubWithPVCs(ourPVCForCapturedFixture()), DatabaseDefaults: operatorconfig.DatabaseDefaults{StorageClass: "longhorn"}}, c
}

func ourPVCForCapturedFixture() *corev1.PersistentVolumeClaim {
	return ourPVC(capturedRestore(readySnapshot()), corev1.ClaimBound)
}

// The spec is immutable and the captured inputs frozen, so a target the API
// server rejects as invalid can never be created: fail with the server's
// reason rather than retrying forever behind a stale status message.
func TestDBRestoreFailsWhenTargetIsRejectedAsInvalid(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	invalid := apierrors.NewInvalid(schema.GroupKind{Group: "dbaas.opencloud.wso2.com", Kind: "DBInstance"}, restore.Spec.TargetInstanceName,
		field.ErrorList{field.Invalid(field.NewPath("spec", "dbName"), "rt-src-1", "should match '^[a-z_][a-z0-9_]{0,62}$'")})
	r, c := restoreReconcilerFailingTargetCreate(t, invalid, restore, snap, heldBy(restore))

	reconcileRestore(t, r, restore)

	got := getRestore(t, c, restore)
	wantStatus(t, got, dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreTargetInvalid)
	if !strings.Contains(got.Status.Message, "spec.dbName") {
		t.Fatalf("Message = %q, want the API server's reason", got.Status.Message)
	}
	if holdExists(t, c, restore) {
		t.Fatal("hold must be released once Failed")
	}
}

// Any other error leaves the restore running, but status must say what's
// happening — not keep describing an earlier pass (the e2e run showed
// "waiting for PVC to become Bound (Pending)" for 40 minutes after it bound).
func TestDBRestoreSurfacesRetryingErrorsInStatus(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	setRestoreProgress(restore, dbaasv1.RestoreStageRestoringVolume, dbaasv1.ReasonRestoreVolumeRestoring,
		`waiting for restore PVC to become Bound (currently "Pending")`)
	r, c := restoreReconcilerFailingTargetCreate(t, errors.New("etcdserver: request timed out"), restore, snap)

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(restore)}); err == nil {
		t.Fatal("a transient create error must be returned for retry")
	}

	got := getRestore(t, c, restore)
	if got.Status.Stage == dbaasv1.RestoreStageFailed {
		t.Fatal("a transient error must not fail the restore")
	}
	if !strings.Contains(got.Status.Message, "retrying after error") || !strings.Contains(got.Status.Message, "request timed out") {
		t.Fatalf("Message = %q, want the error being retried", got.Status.Message)
	}
}

// ---- deadline (restore.timeout) ----

var deadlineNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// createdAgo stamps restore as created `ago` before deadlineNow; the
// reconciler's clock is pinned to deadlineNow and its timeout to 6h.
func createdAgo(restore *dbaasv1.DBRestore, ago time.Duration) {
	restore.CreationTimestamp = metav1.NewTime(deadlineNow.Add(-ago))
}

func newDeadlineReconciler(t *testing.T, stub *testutil.StubHarvester, objs ...client.Object) (*DBRestoreReconciler, client.Client) {
	t.Helper()
	r, c := newRestoreReconciler(t, stub, objs...)
	r.Restore = operatorconfig.RestoreConfig{RecoveryTimeout: time.Hour, Timeout: 6 * time.Hour}
	r.Now = func() time.Time { return deadlineNow }
	return r, c
}

func TestDBRestoreSchedulesItsNextPassNoLaterThanTheDeadline(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := ourTarget(restore) // starting: no Ready condition, so no event-driven wake-up is guaranteed
	restore.Status.TargetInstanceUID = target.UID
	createdAgo(restore, 6*time.Hour-90*time.Second)
	r, c := newDeadlineReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, target)

	res := reconcileRestore(t, r, restore)

	if res.RequeueAfter <= 0 || res.RequeueAfter > 90*time.Second {
		t.Fatalf("RequeueAfter = %v, want a wake-up at the deadline (90s away)", res.RequeueAfter)
	}
	got := getRestore(t, c, restore)
	wantStatus(t, got, dbaasv1.RestoreStageStartingDatabase, dbaasv1.ReasonRestoreTargetStarting)
	if got.Status.Deadline == nil || !got.Status.Deadline.Time.Equal(restore.CreationTimestamp.Add(6*time.Hour)) {
		t.Fatalf("status.deadline = %v, want creation + 6h", got.Status.Deadline)
	}
}

func TestDBRestoreTimesOutBeforeTheTargetExists(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	createdAgo(restore, 7*time.Hour)
	r, c := newDeadlineReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimPending)), restore, snap, heldBy(restore))

	reconcileRestore(t, r, restore)

	got := getRestore(t, c, restore)
	wantStatus(t, got, dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreTimedOut)
	if !strings.Contains(got.Status.Message, "RestoringVolume") {
		t.Fatalf("Message = %q, want the stage it was stuck in", got.Status.Message)
	}
	if holdExists(t, c, restore) {
		t.Fatal("hold must be released once timed out")
	}
	if _, exists := targetExists(t, c, restore); exists {
		t.Fatal("a timed-out restore must not go on to create its target")
	}
}

func TestDBRestoreTimeoutDeletesTheUnfinishedTarget(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := ourTarget(restore) // never became ready
	target.Finalizers = []string{dbaasv1.FinalizerName}
	restore.Status.TargetInstanceUID = target.UID
	createdAgo(restore, 7*time.Hour)
	r, c := newDeadlineReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, target, heldBy(restore))

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreTimedOut)
	if got, exists := targetExists(t, c, restore); exists && got.DeletionTimestamp.IsZero() {
		t.Fatal("the unfinished target must be deleted on timeout")
	}
	if holdExists(t, c, restore) {
		t.Fatal("hold must be released once timed out")
	}
}

// The pass observes everything before the deadline is applied: a target
// that turned ready right at the deadline is a success, not a timeout.
func TestDBRestoreTargetReadyAtTheDeadlineSucceeds(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := withCondition(ourTarget(restore), dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "ready")
	restore.Status.TargetInstanceUID = target.UID
	createdAgo(restore, 7*time.Hour)
	r, c := newDeadlineReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, target)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageSucceeded, dbaasv1.ReasonRestoreSucceeded)
	if got, exists := targetExists(t, c, restore); !exists || !got.DeletionTimestamp.IsZero() {
		t.Fatal("a ready target must never be deleted by the deadline")
	}
}

// A restore stuck retrying an error still ends at its deadline.
func TestDBRestoreTimesOutEvenWhileRetryingAnError(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	createdAgo(restore, 7*time.Hour)
	r, c := restoreReconcilerFailingTargetCreate(t, errors.New("etcdserver: request timed out"), restore, snap)
	r.Restore = operatorconfig.RestoreConfig{RecoveryTimeout: time.Hour, Timeout: 6 * time.Hour}
	r.Now = func() time.Time { return deadlineNow }

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreTimedOut)
}

// The deadline is derived from metadata.creationTimestamp every pass, never
// from recorded status: a status.deadline edited to the past doesn't end a
// restore that still has time.
func TestDBRestoreIgnoresRecordedDeadline(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	createdAgo(restore, time.Hour)
	past := metav1.NewTime(deadlineNow.Add(-time.Hour))
	restore.Status.Deadline = &past
	r, c := newDeadlineReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimPending)), restore, snap)

	reconcileRestore(t, r, restore)

	got := getRestore(t, c, restore)
	if got.Status.Stage == dbaasv1.RestoreStageFailed {
		t.Fatalf("Status = %+v: a recorded deadline must not drive the timeout", got.Status)
	}
	if got.Status.Deadline == nil || !got.Status.Deadline.Time.Equal(restore.CreationTimestamp.Add(6*time.Hour)) {
		t.Fatalf("status.deadline = %v, want it re-derived as creation + 6h", got.Status.Deadline)
	}
}

// A restore that ended for its own reason in this pass keeps that reason,
// even past its deadline — the timeout only applies to unfinished restores.
func TestDBRestoreFailureReasonIsNotOverwrittenByTheDeadline(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	target := withCondition(ourTarget(restore), dbaasv1.ConditionAccepted, metav1.ConditionFalse, dbaasv1.ReasonInvalidClass, "unknown dbInstanceClass")
	restore.Status.TargetInstanceUID = target.UID
	createdAgo(restore, 7*time.Hour)
	r, c := newDeadlineReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)), restore, snap, target)

	reconcileRestore(t, r, restore)

	wantStatus(t, getRestore(t, c, restore), dbaasv1.RestoreStageFailed, dbaasv1.ReasonRestoreTargetRejected)
}

// The timeout deletes a target only after confirming it live: a target the
// lagging cache still shows as starting, but that is ready on the API
// server, is never deleted.
func TestDBRestoreTimeoutNeverDeletesATargetReadyOnTheAPIServer(t *testing.T) {
	snap := readySnapshot()
	restore := capturedRestore(snap)
	cached := ourTarget(restore)
	restore.Status.TargetInstanceUID = cached.UID
	createdAgo(restore, 7*time.Hour)
	live := withCondition(ourTarget(restore), dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "ready")
	r, c := splitReconciler(t, stubWithPVCs(ourPVC(restore, corev1.ClaimBound)),
		[]client.Object{restore.DeepCopy(), snap.DeepCopy(), cached}, []client.Object{restore, snap, live})
	r.Restore = operatorconfig.RestoreConfig{RecoveryTimeout: time.Hour, Timeout: 6 * time.Hour}
	r.Now = func() time.Time { return deadlineNow }

	reconcileRestore(t, r, restore)

	if got, exists := targetExists(t, c, restore); !exists || !got.DeletionTimestamp.IsZero() {
		t.Fatal("a target ready on the API server must not be deleted by the timeout")
	}
	if got := getRestore(t, c, restore); got.Status.Stage == dbaasv1.RestoreStageFailed {
		t.Fatalf("Status = %+v, must not time out a target that is ready", got.Status)
	}
}
