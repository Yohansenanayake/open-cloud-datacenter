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

// Deletion explicitly removes controller-private Secrets in the operator
// namespace, where cross-namespace owner references are unavailable. Tests
// cover recorded references and UID-label cleanup when references are missing.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/backup"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
	statuspatch "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/patch"
)

func newDeletingInst() *dbaasv1.DBInstance {
	inst := newProvisionInst()
	now := metav1.NewTime(time.Now())
	inst.DeletionTimestamp = &now
	inst.Status.Resources.VMName = "pg-orders"
	return inst
}

func operatorSecret(name string, withUIDLabel bool) *corev1.Secret {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dbaas-system"}}
	if withUIDLabel {
		sec.Labels = map[string]string{dbaasv1.LabelDBInstanceUID: "orders-uid"}
	}
	return sec
}

// runReconcileDelete executes the production deletion body and its top-level
// deferred final status patch for tests that exercise deletion directly.
func runReconcileDelete(ctx context.Context, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	patcher := statuspatch.NewSerialPatcher(inst, r.Client)
	result, reconcileErr := r.reconcileDelete(ctx, inst, patcher)
	r.finalizeStatus(inst)
	patchErr := patcher.Patch(ctx, inst, dbInstancePatchOptions()...)
	if !inst.DeletionTimestamp.IsZero() {
		patchErr = kerrors.FilterOut(patchErr, apierrors.IsNotFound)
	}
	return result, errors.Join(reconcileErr, patchErr)
}

func TestReconcileDeleteRemovesOperatorSecretsByRef(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	inst.Status.Resources.InternalSecretRef = "dbaas-system/" + credentials.InternalSecretName(inst)
	inst.Status.Resources.PrivateTLSSecretRef = "dbaas-system/" + credentials.TLSSecretName(inst)

	internal := operatorSecret(credentials.InternalSecretName(inst), true)
	tls := operatorSecret(credentials.TLSSecretName(inst), true)
	r := newProvisionReconciler(t, &stubHarvester{}, inst, internal, tls)

	if _, err := runReconcileDelete(ctx, r, inst); err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	for _, name := range []string{credentials.InternalSecretName(inst), credentials.TLSSecretName(inst)} {
		if err := r.Get(ctx, types.NamespacedName{Namespace: "dbaas-system", Name: name}, &corev1.Secret{}); err == nil {
			t.Fatalf("operator secret %s still exists after delete", name)
		}
	}
	// Removing the last finalizer from an object that already has a
	// DeletionTimestamp causes the (fake, matching real) API server to reap
	// it immediately — so the DBInstance itself is now gone.
	if err := r.Get(ctx, types.NamespacedName{Name: "orders", Namespace: "tenant-a"}, &dbaasv1.DBInstance{}); !apierrors.IsNotFound(err) {
		t.Fatalf("DBInstance = %v, want NotFound (reaped after finalizer removal)", err)
	}
}

// Refs lost (status reset, or a Secret that predates ref-recording) — the
// UID-label sweep must still find and remove them.
func TestReconcileDeleteSweepsByUIDLabelWhenRefsMissing(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst() // InternalSecretRef/PrivateTLSSecretRef left empty

	internal := operatorSecret(credentials.InternalSecretName(inst), true)
	tls := operatorSecret(credentials.TLSSecretName(inst), true)
	r := newProvisionReconciler(t, &stubHarvester{}, inst, internal, tls)

	if _, err := runReconcileDelete(ctx, r, inst); err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	var list corev1.SecretList
	if err := r.List(ctx, &list, client.InNamespace("dbaas-system")); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("operator-namespace secrets remain after label sweep: %+v", list.Items)
	}
}

// A Secret belonging to a different DBInstance (different UID label) must
// survive the sweep — this is the whole point of scoping by UID.
func TestReconcileDeleteSweepDoesNotTouchOtherInstances(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "dbi-other-uid-internal", Namespace: "dbaas-system",
		Labels: map[string]string{dbaasv1.LabelDBInstanceUID: "other-uid"},
	}}
	mine := operatorSecret(credentials.InternalSecretName(inst), true)
	r := newProvisionReconciler(t, &stubHarvester{}, inst, mine, other)

	if _, err := runReconcileDelete(ctx, r, inst); err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	if err := r.Get(ctx, types.NamespacedName{Namespace: "dbaas-system", Name: "dbi-other-uid-internal"}, &corev1.Secret{}); err != nil {
		t.Fatalf("unrelated instance's secret was deleted: %v", err)
	}
}

func TestReconcileDeleteProtectionPublishesBlockedSummary(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	inst.Spec.DeletionProtection = true
	r := newProvisionReconciler(t, &stubHarvester{}, inst)

	if _, err := runReconcileDelete(ctx, r, inst); err != nil {
		t.Fatalf("reconcileDelete returned error for stable protected state: %v", err)
	}

	got := &dbaasv1.DBInstance{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(inst), got); err != nil {
		t.Fatalf("get protected instance: %v", err)
	}
	if got.Status.Phase != dbaasv1.StatusDeleting {
		t.Fatalf("phase = %q, want deleting", got.Status.Phase)
	}
	blocked := got.Status.GetCondition(dbaasv1.ConditionDeletionBlocked)
	if blocked == nil || blocked.Status != metav1.ConditionTrue || blocked.Reason != string(dbaasv1.ReasonDeletionProtected) {
		t.Fatalf("DeletionBlocked = %+v, want True/DeletionProtected", blocked)
	}
	if got.Status.Message != blocked.Message {
		t.Fatalf("summary message = %q, want blocked message %q", got.Status.Message, blocked.Message)
	}
}

func TestReconcileDeleteProtectionReturnsStatusPatchError(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	inst.Spec.DeletionProtection = true
	boom := errors.New("status unavailable")
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture's fake client does not implement client.WithWatch")
	}
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			return boom
		},
	})

	if _, err := runReconcileDelete(ctx, r, inst); !errors.Is(err, boom) {
		t.Fatalf("reconcileDelete error = %v, want status patch error", err)
	}
}

func TestReconcileDeleteContinuesAfterInitialStatusPatchError(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	statusErr := errors.New("status unavailable")
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst)
	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture's fake client does not implement client.WithWatch")
	}
	patchCalls := 0
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalls++
			if patchCalls <= 2 {
				return statusErr
			}
			return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
		},
	})

	if _, err := runReconcileDelete(ctx, r, inst); err != nil {
		t.Fatalf("reconcileDelete error = %v, want best-effort status failure ignored", err)
	}
	if stub.TeardownCalls != 1 {
		t.Fatalf("TeardownAll calls = %d, want 1", stub.TeardownCalls)
	}
}

func TestReconcileDeleteJoinsTeardownAndStatusPatchErrors(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	TeardownErr := errors.New("teardown failed")
	statusErr := errors.New("status unavailable")
	stub := &stubHarvester{TeardownErr: TeardownErr}
	r := newProvisionReconciler(t, stub, inst)
	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture's fake client does not implement client.WithWatch")
	}
	patchCalls := 0
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalls++
			if patchCalls == 3 {
				return statusErr
			}
			return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
		},
	})

	_, err := runReconcileDelete(ctx, r, inst)
	if !errors.Is(err, TeardownErr) || !errors.Is(err, statusErr) {
		t.Fatalf("reconcileDelete error = %v, want teardown and status errors", err)
	}
}

func TestReconcileDeleteJoinsSecretCleanupAndStatusPatchErrors(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	secret := operatorSecret(credentials.InternalSecretName(inst), true)
	inst.Status.Resources.InternalSecretRef = secret.Namespace + "/" + secret.Name
	cleanupErr := errors.New("secret cleanup failed")
	statusErr := errors.New("status unavailable")
	r := newProvisionReconciler(t, &stubHarvester{}, inst, secret)
	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture's fake client does not implement client.WithWatch")
	}
	patchCalls := 0
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return cleanupErr
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalls++
			if patchCalls == 3 {
				return statusErr
			}
			return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
		},
	})

	_, err := runReconcileDelete(ctx, r, inst)
	if !errors.Is(err, cleanupErr) || !errors.Is(err, statusErr) {
		t.Fatalf("reconcileDelete error = %v, want cleanup and status errors", err)
	}
}

func TestReconcileDeleteEmitsProgressAndFailureEvents(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	teardownErr := errors.New("teardown failed")
	r := newProvisionReconciler(t, &stubHarvester{TeardownErr: teardownErr}, inst)
	recorder, ok := r.Recorder.(*record.FakeRecorder)
	if !ok {
		t.Fatalf("Recorder = %T, want *record.FakeRecorder", r.Recorder)
	}

	if _, err := runReconcileDelete(ctx, r, inst); !errors.Is(err, teardownErr) {
		t.Fatalf("reconcileDelete error = %v, want %v", err, teardownErr)
	}

	var events []string
	for len(events) < 2 {
		select {
		case event := <-recorder.Events:
			events = append(events, event)
		case <-time.After(time.Second):
			t.Fatalf("events = %v, want deletion progress and failure events", events)
		}
	}
	if !strings.Contains(events[0], corev1.EventTypeNormal+" "+string(dbaasv1.ReasonDeletionProgressing)) {
		t.Fatalf("first event = %q, want normal deletion progress", events[0])
	}
	if !strings.Contains(events[1], corev1.EventTypeWarning+" "+string(dbaasv1.ReasonTeardownFailed)) {
		t.Fatalf("second event = %q, want warning teardown failure", events[1])
	}
}

func TestRemoveDBInstanceFinalizerRetriesConflictAndPreservesConcurrentMetadata(t *testing.T) {
	ctx := context.Background()
	inst := newProvisionInst()
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture's fake client does not implement client.WithWatch")
	}
	updateCalls := 0
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			updateCalls++
			if updateCalls == 1 {
				concurrent := &dbaasv1.DBInstance{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(obj), concurrent); err != nil {
					return err
				}
				concurrent.Annotations = map[string]string{"concurrent": "preserved"}
				if err := c.Update(ctx, concurrent); err != nil {
					return err
				}
				return apierrors.NewConflict(
					dbaasv1.GroupVersion.WithResource("dbinstances").GroupResource(),
					obj.GetName(),
					errors.New("injected conflict"),
				)
			}
			return c.Update(ctx, obj, opts...)
		},
	})

	if err := r.removeDBInstanceFinalizer(ctx, client.ObjectKeyFromObject(inst)); err != nil {
		t.Fatalf("removeDBInstanceFinalizer after conflict: %v", err)
	}
	if updateCalls != 2 {
		t.Fatalf("update calls = %d, want 2", updateCalls)
	}
	got := &dbaasv1.DBInstance{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(inst), got); err != nil {
		t.Fatalf("get after finalizer removal: %v", err)
	}
	if controllerutil.ContainsFinalizer(got, dbaasv1.FinalizerName) {
		t.Fatal("DBInstance finalizer was not removed")
	}
	if got.Annotations["concurrent"] != "preserved" {
		t.Fatalf("concurrent annotation = %q, want preserved", got.Annotations["concurrent"])
	}
}

// ---- the snapshot hold ----

func seedSnapshotHold(t *testing.T, r *DBInstanceReconciler, inst *dbaasv1.DBInstance, holder string) {
	t.Helper()
	if _, err := (backup.Holds{Live: r.Client, Writer: r.Client}).Acquire(context.Background(), inst.Namespace,
		backup.SnapshotHoldName(inst.UID), holder, instanceOwnerRef(inst), nil); err != nil {
		t.Fatalf("seed snapshot hold: %v", err)
	}
}

func snapshotHoldOf(t *testing.T, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) (string, bool) {
	t.Helper()
	holder, held, err := (backup.Holds{Live: r.Client, Writer: r.Client}).Held(context.Background(), inst.Namespace, backup.SnapshotHoldName(inst.UID))
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	return holder, held
}

func instanceGone(t *testing.T, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) bool {
	t.Helper()
	err := r.Get(context.Background(), client.ObjectKeyFromObject(inst), &dbaasv1.DBInstance{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get instance: %v", err)
	}
	return apierrors.IsNotFound(err)
}

func snapshotOf(inst *dbaasv1.DBInstance, name string, reason dbaasv1.ConditionReason, status metav1.ConditionStatus) *dbaasv1.DBSnapshot {
	snap := &dbaasv1.DBSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: inst.Namespace},
		Spec:       dbaasv1.DBSnapshotSpec{SourceInstanceRef: corev1.LocalObjectReference{Name: inst.Name}},
	}
	snap.Status.SetCondition(metav1.Condition{Type: dbaasv1.ConditionSnapshotReady, Status: status, Reason: string(reason), Message: "-"})
	return snap
}

// A backup still reading the VM holds teardown back until it finishes.
func TestReconcileDeleteWaitsForARunningSnapshot(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	running := snapshotOf(inst, "orders-nightly", dbaasv1.ReasonSnapshotBackupInProgress, metav1.ConditionFalse)
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst, running)
	seedSnapshotHold(t, r, inst, snapshotHolderIdentity(running))

	res, err := runReconcileDelete(ctx, r, inst)
	if err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	if stub.TeardownCalls != 0 || stub.MarkVMPVCsCalls != 0 {
		t.Fatal("nothing may be torn down while a backup is still reading the VM")
	}
	if res.RequeueAfter != deletionPollRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.RequeueAfter, deletionPollRequeue)
	}
	got := &dbaasv1.DBInstance{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(inst), got); err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if blocked := got.Status.GetCondition(dbaasv1.ConditionDeletionBlocked); blocked == nil || blocked.Reason != string(dbaasv1.ReasonDeletionWaitingForSnapshot) {
		t.Fatalf("DeletionBlocked = %+v, want reason DeletionWaitingForSnapshot", blocked)
	}
	if _, held := snapshotHoldOf(t, r, inst); !held {
		t.Fatal("the running snapshot's hold must be left to it")
	}
}

// A hold nobody else will ever release — this instance's own repave hold,
// or one whose snapshot is gone, finished, or not this instance's — is
// released, and teardown carries on.
func TestReconcileDeleteReleasesHoldsNobodyElseWillRelease(t *testing.T) {
	inst := newDeletingInst()
	other := inst.DeepCopy()
	other.Name = "billing"
	cases := map[string]struct {
		holder string
		snap   *dbaasv1.DBSnapshot
	}{
		"repave":           {holder: "repave"},
		"snapshot gone":    {holder: snapshotHolderPrefix + "orders-nightly"},
		"snapshot ready":   {holder: snapshotHolderPrefix + "orders-nightly", snap: snapshotOf(inst, "orders-nightly", dbaasv1.ReasonSnapshotBackupReady, metav1.ConditionTrue)},
		"snapshot failed":  {holder: snapshotHolderPrefix + "orders-nightly", snap: snapshotOf(inst, "orders-nightly", dbaasv1.ReasonSnapshotBackupFailed, metav1.ConditionFalse)},
		"another's source": {holder: snapshotHolderPrefix + "orders-nightly", snap: snapshotOf(other, "orders-nightly", dbaasv1.ReasonSnapshotBackupInProgress, metav1.ConditionFalse)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			inst := newDeletingInst()
			objs := []client.Object{inst}
			if tc.snap != nil {
				objs = append(objs, tc.snap)
			}
			stub := &stubHarvester{}
			r := newProvisionReconciler(t, stub, objs...)
			seedSnapshotHold(t, r, inst, tc.holder)

			if _, err := runReconcileDelete(context.Background(), r, inst); err != nil {
				t.Fatalf("reconcileDelete: %v", err)
			}

			if _, held := snapshotHoldOf(t, r, inst); held {
				t.Fatal("the stale hold must be released")
			}
			if stub.TeardownCalls != 1 || !instanceGone(t, r, inst) {
				t.Fatal("teardown must carry on after releasing a stale hold")
			}
		})
	}
}

// ---- disks ----

// The VM's own disks are deleted through the VM: marked for Harvester's VM
// finalizer, never deleted directly while the VM exists (Harvester would
// recreate a blank one). Once the VM is gone, the known names are deleted
// directly as a backstop — never anything another instance could own.
func TestReconcileDeleteDeletesDisksThroughTheVMThenDirectly(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst() // disk identifier "orders-ordersui"
	inst.Status.Resources.OSDiskPVCName = "pg-orders-ordersui-os-ubuntu-noble-r2"
	inst.Status.Resources.PendingDeleteOSDiskPVCName = "pg-orders-ordersui-os"
	stub := &stubHarvester{
		VMPresent:  true,
		VMPVCNames: []string{"pg-orders-ordersui-data", "pg-orders-ordersui-os-ubuntu-noble-r2", "shared-tools-disk"},
	}
	r := newProvisionReconciler(t, stub, inst)

	res, err := runReconcileDelete(ctx, r, inst)
	if err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	wantMarked := []string{"pg-orders-ordersui-data", "pg-orders-ordersui-os-ubuntu-noble-r2"}
	if !slices.Equal(stub.MarkedPVCNames, wantMarked) {
		t.Fatalf("marked for removal = %v, want %v (only this instance's own disks)", stub.MarkedPVCNames, wantMarked)
	}
	if stub.TeardownCalls != 1 {
		t.Fatalf("TeardownAll calls = %d, want 1", stub.TeardownCalls)
	}
	if len(stub.DeletedPVCNames) != 0 {
		t.Fatalf("deleted %v while the VM still exists", stub.DeletedPVCNames)
	}
	if res.RequeueAfter != deletionPollRequeue || instanceGone(t, r, inst) {
		t.Fatal("must wait, finalizer in place, for the VM to be gone")
	}

	stub.VMPresent = false // Harvester finished deleting the VM
	latest := &dbaasv1.DBInstance{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(inst), latest); err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if _, err := runReconcileDelete(ctx, r, latest); err != nil {
		t.Fatalf("second reconcileDelete: %v", err)
	}

	wantDeleted := []string{"pg-orders-ordersui-data", "pg-orders-ordersui-os", "pg-orders-ordersui-os-ubuntu-noble-r2"}
	if !slices.Equal(stub.DeletedPVCNames, wantDeleted) {
		t.Fatalf("deleted PVCs = %v, want %v", stub.DeletedPVCNames, wantDeleted)
	}
	if !instanceGone(t, r, inst) {
		t.Fatal("finalizer must come off once the disks are deleted")
	}
}

// A recorded name that isn't this instance's own (another UID salt) is never
// deleted, however it got into status.
func TestReconcileDeleteNeverDeletesARecordedPVCThatIsNotOurs(t *testing.T) {
	inst := newDeletingInst()
	inst.Status.Resources.OSDiskPVCName = "pg-orders-0ldsalt0-os"
	inst.Status.Resources.PendingDeleteOSDiskPVCName = "pg-billing-b1111111-os-ubuntu"
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst)

	if _, err := runReconcileDelete(context.Background(), r, inst); err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	for _, name := range stub.DeletedPVCNames {
		if strings.Contains(name, "0ldsalt0") || strings.Contains(name, "billing") {
			t.Fatalf("deleted %q, which is not this instance's", name)
		}
	}
}

// A restore target's data disk is its restore PVC, named for the DBRestore.
func TestReconcileDeleteDeletesARestoreTargetsRestorePVC(t *testing.T) {
	inst := newDeletingInst()
	inst.Spec.RestoredFrom = &dbaasv1.RestoredFromRef{DBRestoreName: "orders-restore", DBRestoreUID: "restore-uid-1234"}
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst)

	if _, err := runReconcileDelete(context.Background(), r, inst); err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	if want := "pg-orders-restore-restoreu-data"; !slices.Contains(stub.DeletedPVCNames, want) {
		t.Fatalf("deleted PVCs = %v, want to include %q", stub.DeletedPVCNames, want)
	}
}

func TestReconcileDeleteDiskDeleteFailureKeepsTheFinalizer(t *testing.T) {
	inst := newDeletingInst()
	boom := errors.New("pvc delete refused")
	stub := &stubHarvester{DeletePVCErr: boom}
	r := newProvisionReconciler(t, stub, inst)

	if _, err := runReconcileDelete(context.Background(), r, inst); !errors.Is(err, boom) {
		t.Fatalf("reconcileDelete error = %v, want %v", err, boom)
	}
	if instanceGone(t, r, inst) {
		t.Fatal("finalizer must stay until the disks are deleted")
	}
}

// Teardown spans several passes; only its start is announced.
func TestReconcileDeleteAnnouncesTeardownOnce(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	stub := &stubHarvester{VMPresent: true}
	r := newProvisionReconciler(t, stub, inst)
	recorder := r.Recorder.(*record.FakeRecorder)

	for range 2 {
		latest := &dbaasv1.DBInstance{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(inst), latest); err != nil {
			t.Fatalf("get instance: %v", err)
		}
		if _, err := runReconcileDelete(ctx, r, latest); err != nil {
			t.Fatalf("reconcileDelete: %v", err)
		}
	}

	announced := 0
	for len(recorder.Events) > 0 {
		if strings.Contains(<-recorder.Events, string(dbaasv1.ReasonDeletionProgressing)) {
			announced++
		}
	}
	if announced != 1 {
		t.Fatalf("%s events = %d, want 1 across two passes", dbaasv1.ReasonDeletionProgressing, announced)
	}
}

// ---- events ----

func TestReconcileDeleteAnnouncesEachWaitOnce(t *testing.T) {
	ctx := context.Background()
	inst := newDeletingInst()
	running := snapshotOf(inst, "orders-nightly", dbaasv1.ReasonSnapshotBackupInProgress, metav1.ConditionFalse)
	stub := &stubHarvester{VMPresent: true}
	r := newProvisionReconciler(t, stub, inst, running)
	recorder := r.Recorder.(*record.FakeRecorder)
	seedSnapshotHold(t, r, inst, snapshotHolderIdentity(running))

	pass := func() {
		t.Helper()
		latest := &dbaasv1.DBInstance{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(inst), latest); err != nil {
			t.Fatalf("get instance: %v", err)
		}
		if _, err := runReconcileDelete(ctx, r, latest); err != nil {
			t.Fatalf("reconcileDelete: %v", err)
		}
	}
	pass()
	pass() // backup still running
	if err := (backup.Holds{Live: r.Client, Writer: r.Client}).Release(ctx, inst.Namespace, backup.SnapshotHoldName(inst.UID), snapshotHolderIdentity(running)); err != nil {
		t.Fatalf("release: %v", err)
	}
	pass()
	pass() // VM still going

	events := drainEvents(recorder)
	for _, reason := range []dbaasv1.ConditionReason{dbaasv1.ReasonDeletionWaitingForSnapshot, dbaasv1.ReasonDeletionWaitingForVM} {
		if n := eventsWithReason(events, string(reason)); n != 1 {
			t.Fatalf("%s events = %d, want 1 (events %q)", reason, n, events)
		}
	}
}

// A snapshot hold its DBSnapshot should have released is surfaced; this
// instance's own repave hold is released quietly.
func TestReconcileDeleteWarnsOnlyAboutStaleSnapshotHolds(t *testing.T) {
	for holder, wantWarnings := range map[string]int{snapshotHolderPrefix + "orders-gone": 1, "repave": 0} {
		t.Run(holder, func(t *testing.T) {
			inst := newDeletingInst()
			r := newProvisionReconciler(t, &stubHarvester{}, inst)
			seedSnapshotHold(t, r, inst, holder)

			if _, err := runReconcileDelete(context.Background(), r, inst); err != nil {
				t.Fatalf("reconcileDelete: %v", err)
			}

			events := drainEvents(r.Recorder.(*record.FakeRecorder))
			if n := eventsWithReason(events, string(dbaasv1.ReasonStaleSnapshotHoldReleased)); n != wantWarnings {
				t.Fatalf("StaleSnapshotHoldReleased events = %d, want %d (events %q)", n, wantWarnings, events)
			}
		})
	}
}
