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
	"sort"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/backup"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/testutil"
)

var dispatchT0 = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

func newDispatcher(t *testing.T, maxConcurrent int, objs ...client.Object) (*BackupDispatcher, client.Client, chan event.GenericEvent) {
	t.Helper()
	c := testutil.NewClient(t, objs...)
	wake := make(chan event.GenericEvent, 100)
	return &BackupDispatcher{
		Client: c, APIReader: c, SlotNamespace: testSlotNamespace,
		Backup: operatorconfig.BackupConfig{MaxConcurrent: maxConcurrent, Timeout: 6 * time.Hour},
		Wake:   wake,
	}, c, wake
}

func sourceIn(ns, name string) *dbaasv1.DBInstance {
	src := availableSourceInstance()
	src.Namespace, src.Name, src.UID = ns, name, types.UID(ns+"-"+name+"-uid")
	return src
}

// snapshotIn is a DBSnapshot in the given Ready state (reason "" = none
// yet), created ageOrder seconds after dispatchT0.
func snapshotIn(ns, name, source string, ageOrder int, reason dbaasv1.ConditionReason) *dbaasv1.DBSnapshot {
	snap := &dbaasv1.DBSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, UID: types.UID(ns + "-" + name + "-uid"),
			CreationTimestamp: metav1.NewTime(dispatchT0.Add(time.Duration(ageOrder) * time.Second)),
		},
		Spec: dbaasv1.DBSnapshotSpec{SourceInstanceRef: corev1.LocalObjectReference{Name: source}},
	}
	if reason != "" {
		status := metav1.ConditionFalse
		if reason == dbaasv1.ReasonSnapshotBackupReady {
			status = metav1.ConditionTrue
		}
		snap.Status.SetCondition(metav1.Condition{Type: dbaasv1.ConditionSnapshotReady, Status: status, Reason: string(reason), Message: "-"})
	}
	return snap
}

func dispatch(t *testing.T, d *BackupDispatcher) {
	t.Helper()
	res, err := d.Reconcile(context.Background(), backupDispatcherKey)
	if err != nil {
		t.Fatalf("dispatcher pass: %v", err)
	}
	if res.RequeueAfter != backupDispatcherResync {
		t.Fatalf("RequeueAfter = %v, want the %v resync", res.RequeueAfter, backupDispatcherResync)
	}
}

// slotHolders lists "<ns>/<name>" for every granted slot, sorted.
func slotHolders(t *testing.T, c client.Client) []string {
	t.Helper()
	slots, err := slotsOf(c).List(context.Background())
	if err != nil {
		t.Fatalf("list slots: %v", err)
	}
	var out []string
	for _, s := range slots {
		out = append(out, s.Holder.String())
	}
	sort.Strings(out)
	return out
}

func woken(wake chan event.GenericEvent) []string {
	var out []string
	for len(wake) > 0 {
		e := <-wake
		out = append(out, e.Object.GetNamespace()+"/"+e.Object.GetName())
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// Tenant a's backlog is older, but a gets only its share (2 of 4); b's
// younger snapshot is served instead of waiting behind a's backlog.
func TestDispatcherGrantsOldestWithinGlobalCapAndNamespaceShare(t *testing.T) {
	d, c, wake := newDispatcher(t, 4,
		sourceIn("a", "db1"), sourceIn("b", "db2"),
		snapshotIn("a", "s1", "db1", 1, dbaasv1.ReasonSnapshotBackupQueued),
		snapshotIn("a", "s2", "db1", 2, dbaasv1.ReasonSnapshotBackupQueued),
		snapshotIn("a", "s3", "db1", 3, dbaasv1.ReasonSnapshotBackupQueued),
		snapshotIn("b", "s4", "db2", 4, dbaasv1.ReasonSnapshotBackupQueued),
	)

	dispatch(t, d)

	want := []string{"a/s1", "a/s2", "b/s4"}
	if got := slotHolders(t, c); !equalStrings(got, want) {
		t.Fatalf("slots = %v, want %v", got, want)
	}
	if got := woken(wake); !equalStrings(got, want) {
		t.Fatalf("woken = %v, want each granted snapshot woken: %v", got, want)
	}

	// Steady state: another pass grants nothing more and wakes no one.
	dispatch(t, d)
	if got := slotHolders(t, c); !equalStrings(got, want) || len(woken(wake)) != 0 {
		t.Fatalf("a second pass changed the grants: %v", got)
	}
}

// Only snapshots queued for a slot are candidates.
func TestDispatcherGrantsOnlyQueuedSnapshots(t *testing.T) {
	deleting := snapshotIn("a", "deleting", "db1", 1, dbaasv1.ReasonSnapshotBackupQueued)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	d, c, _ := newDispatcher(t, 4, sourceIn("a", "db1"),
		deleting,
		snapshotIn("a", "new", "db1", 2, ""),
		snapshotIn("a", "done", "db1", 3, dbaasv1.ReasonSnapshotBackupReady),
		snapshotIn("a", "rejected", "db1", 4, dbaasv1.ReasonSnapshotSourceNotReady),
		snapshotIn("a", "queued", "db1", 5, dbaasv1.ReasonSnapshotBackupQueued),
	)

	dispatch(t, d)

	if got := slotHolders(t, c); !equalStrings(got, []string{"a/queued"}) {
		t.Fatalf("slots = %v, want only the queued snapshot granted", got)
	}
}

// A snapshot whose instance is mid-repave is skipped, not granted a slot it
// would only hand back; the next eligible snapshot gets it.
func TestDispatcherSkipsASnapshotWhoseInstanceIsBusy(t *testing.T) {
	busy := sourceIn("a", "db1")
	d, c, _ := newDispatcher(t, 1, busy, sourceIn("b", "db2"),
		snapshotIn("a", "s1", "db1", 1, dbaasv1.ReasonSnapshotHoldWaiting),
		snapshotIn("b", "s2", "db2", 2, dbaasv1.ReasonSnapshotBackupQueued),
	)
	if _, err := (backup.Holds{Live: c, Writer: c}).Acquire(context.Background(), busy.Namespace,
		backup.SnapshotHoldName(busy.UID), "repave", instanceOwnerRef(busy), nil); err != nil {
		t.Fatalf("seed repave hold: %v", err)
	}

	dispatch(t, d)

	if got := slotHolders(t, c); !equalStrings(got, []string{"b/s2"}) {
		t.Fatalf("slots = %v, want b/s2 (a/s1's instance is busy)", got)
	}

	// Once repave is done, a/s1 is eligible again.
	if err := (backup.Holds{Live: c, Writer: c}).Release(context.Background(), busy.Namespace, backup.SnapshotHoldName(busy.UID), "repave"); err != nil {
		t.Fatalf("release repave hold: %v", err)
	}
	if err := slotsOf(c).Release(context.Background(), "b-s2-uid"); err != nil {
		t.Fatalf("free b/s2: %v", err)
	}
	dispatch(t, d)
	if got := slotHolders(t, c); !equalStrings(got, []string{"a/s1"}) {
		t.Fatalf("slots = %v, want a/s1 once its instance is free", got)
	}
}

// Slots whose holder no longer needs them are reclaimed; a running
// holder's slot is kept.
func TestDispatcherReclaimsStaleSlots(t *testing.T) {
	deleting := snapshotIn("a", "deleting", "db1", 4, dbaasv1.ReasonSnapshotBackupInProgress)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	replaced := snapshotIn("a", "replaced", "db1", 5, dbaasv1.ReasonSnapshotBackupInProgress)
	d, c, _ := newDispatcher(t, 8, sourceIn("a", "db1"),
		snapshotIn("a", "running", "db1", 1, dbaasv1.ReasonSnapshotBackupInProgress),
		snapshotIn("a", "done", "db1", 2, dbaasv1.ReasonSnapshotBackupReady),
		snapshotIn("a", "failed", "db1", 3, dbaasv1.ReasonSnapshotBackupFailed),
		deleting, replaced,
	)
	holders := []backup.SlotHolder{
		{Namespace: "a", Name: "running", UID: "a-running-uid"},
		{Namespace: "a", Name: "done", UID: "a-done-uid"},
		{Namespace: "a", Name: "failed", UID: "a-failed-uid"},
		{Namespace: "a", Name: "deleting", UID: "a-deleting-uid"},
		{Namespace: "a", Name: "replaced", UID: "the-snapshot-it-replaced"},
		{Namespace: "a", Name: "gone", UID: "a-gone-uid"},
	}
	for i, h := range holders {
		if _, err := slotsOf(c).Grant(context.Background(), i, h); err != nil {
			t.Fatalf("seed slot: %v", err)
		}
	}

	dispatch(t, d)

	if got := slotHolders(t, c); !equalStrings(got, []string{"a/running"}) {
		t.Fatalf("slots = %v, want only the running snapshot's kept", got)
	}
}

// Reclaiming needs the live answer: a cache that hasn't seen a running
// holder yet must not cost it its slot.
func TestDispatcherConfirmsStalenessLive(t *testing.T) {
	running := snapshotIn("a", "running", "db1", 1, dbaasv1.ReasonSnapshotBackupInProgress)
	d, c, _ := newDispatcher(t, 4, sourceIn("a", "db1"), running)
	if _, err := slotsOf(c).Grant(context.Background(), 0,
		backup.SlotHolder{Namespace: "a", Name: "running", UID: running.UID}); err != nil {
		t.Fatalf("seed slot: %v", err)
	}
	// The cache lags: it doesn't have the snapshot yet. The API server does.
	d.Client = interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*dbaasv1.DBSnapshot); ok {
				return apierrors.NewNotFound(schema.GroupResource{Resource: "dbsnapshots"}, key.Name)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})

	dispatch(t, d)

	if got := slotHolders(t, c); !equalStrings(got, []string{"a/running"}) {
		t.Fatalf("slots = %v, want the running snapshot's slot kept", got)
	}
}

// If dispatch passes ever overlap (two operators during a leader
// handover — within one operator they can't, the work queue serializes the
// single key), the indexed slots still cap the total at K.
func TestDispatcherOverlappingPassesNeverExceedTheGlobalCap(t *testing.T) {
	var objs []client.Object
	for ns := 0; ns < 4; ns++ {
		objs = append(objs, sourceIn(fmt.Sprintf("ns%d", ns), "db"))
		for i := 0; i < 5; i++ {
			objs = append(objs, snapshotIn(fmt.Sprintf("ns%d", ns), fmt.Sprintf("s%d", i), "db", ns*10+i, dbaasv1.ReasonSnapshotBackupQueued))
		}
	}
	const k = 3
	d, c, _ := newDispatcher(t, k, objs...)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = d.Reconcile(context.Background(), backupDispatcherKey)
		}()
	}
	wg.Wait()

	if got := slotHolders(t, c); len(got) > k {
		t.Fatalf("%d slots granted by overlapping passes, cap %d: %v", len(got), k, got)
	}
}

func TestDispatcherPredicatePassesOnlyQueueChanges(t *testing.T) {
	base := snapshotIn("a", "s1", "db1", 1, dbaasv1.ReasonSnapshotBackupInProgress)
	sameReason := base.DeepCopy()
	sameReason.Status.Conditions[0].Message = "still waiting"
	newReason := base.DeepCopy()
	newReason.Status.Conditions[0].Reason = string(dbaasv1.ReasonSnapshotBackupReady)
	deleting := base.DeepCopy()
	now := metav1.Now()
	deleting.DeletionTimestamp = &now

	for name, tc := range map[string]struct {
		newObj *dbaasv1.DBSnapshot
		want   bool
	}{
		"message only":      {sameReason, false},
		"reason changed":    {newReason, true},
		"deletion starting": {deleting, true},
	} {
		if got := queueChanged.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: tc.newObj}); got != tc.want {
			t.Errorf("%s: predicate = %v, want %v", name, got, tc.want)
		}
	}
}
