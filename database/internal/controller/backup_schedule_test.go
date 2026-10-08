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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/testutil"
)

// newScheduleReconciler mirrors testutil.NewClient but additionally registers
// the same field index SetupWithManager installs on the real manager cache —
// pruneAutomatedSnapshots lists by it, and the fake client has no indexes
// unless a builder asks for one.
func newScheduleReconciler(t *testing.T, objs ...client.Object) *DBInstanceReconciler {
	t.Helper()
	c := ctrlfake.NewClientBuilder().
		WithScheme(testutil.NewScheme(t)).
		WithStatusSubresource(&dbaasv1.DBInstance{}, &dbaasv1.DBSnapshot{}).
		WithIndex(&dbaasv1.DBSnapshot{}, snapshotSourceInstanceIdx, snapshotSourceIndexFunc).
		WithObjects(objs...).
		Build()
	return &DBInstanceReconciler{Client: c, Recorder: record.NewFakeRecorder(100)}
}

func backupEnabledInstance(enabled bool) *dbaasv1.DBInstance {
	inst := testutil.NewProvisionInstance()
	inst.Status.Phase = dbaasv1.StatusAvailable
	inst.Spec.Backup = &dbaasv1.BackupSpec{
		Automated: dbaasv1.AutomatedBackupSpec{Enabled: &enabled},
	}
	return inst
}

// defaultScheduledInstantOn returns this instance's stable-minute instant,
// under the default 02:00-03:00 window, on today+dayOffset — a realistic
// persisted NextScheduledSnapshotTime rather than an arbitrary past
// timestamp, so the window-change check (which compares against exactly this
// computation) doesn't mistake the fixture for a window change.
func defaultScheduledInstantOn(inst *dbaasv1.DBInstance, dayOffset int) time.Time {
	return scheduledInstant(inst.UID, 2*time.Hour, time.Hour, time.Now().UTC().AddDate(0, 0, dayOffset))
}

// readyAutomatedSnapshot builds a Ready automated snapshot the scheduler
// created for owner: named for it, and owned by its UID.
func readyAutomatedSnapshot(name string, owner *dbaasv1.DBInstance, age time.Duration) *dbaasv1.DBSnapshot {
	snap := &dbaasv1.DBSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "tenant-a",
			Labels:            map[string]string{dbaasv1.LabelSnapshotOrigin: dbaasv1.SnapshotOriginAutomated},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			OwnerReferences:   []metav1.OwnerReference{*instanceOwnerRef(owner)},
		},
		Spec: dbaasv1.DBSnapshotSpec{SourceInstanceRef: corev1.LocalObjectReference{Name: owner.Name}},
	}
	snap.Status.SetCondition(metav1.Condition{
		Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionTrue,
		Reason: string(dbaasv1.ReasonSnapshotBackupReady), Message: "ready",
	})
	return snap
}

// Scheduling events

func TestBackupScheduleNoSpecBackupIsNoop(t *testing.T) {
	inst := testutil.NewProvisionInstance() // Spec.Backup left nil
	r := newScheduleReconciler(t, inst)

	requeue, err := r.evaluateBackupSchedule(context.Background(), inst)

	if err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}
	if requeue != 0 {
		t.Fatalf("requeue = %v, want 0", requeue)
	}
	if inst.Status.Backup != nil {
		t.Fatalf("Status.Backup = %+v, want nil (backup capability absent)", inst.Status.Backup)
	}
}

func TestBackupScheduleEnabledAtCreationSchedulesNextFutureRun(t *testing.T) {
	inst := backupEnabledInstance(true)
	r := newScheduleReconciler(t, inst)

	_, err := r.evaluateBackupSchedule(context.Background(), inst)
	if err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}

	next := inst.Status.Backup.NextScheduledSnapshotTime
	if next == nil || !next.After(time.Now().UTC()) {
		t.Fatalf("NextScheduledSnapshotTime = %v, want set and strictly future", next)
	}
	assertNoAutomatedSnapshotCreated(t, r, inst)
}

func TestBackupScheduleReEnabledAfterPauseStartsFreshFutureRun(t *testing.T) {
	inst := backupEnabledInstance(false)
	stalePast := metav1.NewTime(time.Now().Add(-48 * time.Hour))
	inst.Status.Backup = &dbaasv1.BackupStatus{NextScheduledSnapshotTime: &stalePast}
	r := newScheduleReconciler(t, inst)

	// Paused: the stale persisted time is cleared, nothing fires.
	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule (paused): %v", err)
	}
	if inst.Status.Backup.NextScheduledSnapshotTime != nil {
		t.Fatalf("NextScheduledSnapshotTime = %v, want cleared while paused", inst.Status.Backup.NextScheduledSnapshotTime)
	}
	assertNoAutomatedSnapshotCreated(t, r, inst)

	// Re-enabled: recomputes to a fresh future run, not the stale past one.
	*inst.Spec.Backup.Automated.Enabled = true
	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule (re-enabled): %v", err)
	}
	next := inst.Status.Backup.NextScheduledSnapshotTime
	if next == nil || !next.After(time.Now().UTC()) {
		t.Fatalf("NextScheduledSnapshotTime after re-enable = %v, want set and strictly future", next)
	}
	assertNoAutomatedSnapshotCreated(t, r, inst)
}

func TestBackupScheduleOverdueSlotFiresOnceThenJumpsToNextFutureRun(t *testing.T) {
	inst := backupEnabledInstance(true)
	// Simulate a controller outage spanning several missed days: a real,
	// still-matching-the-window slot from 3 days ago, not just any past time.
	overdue := metav1.NewTime(defaultScheduledInstantOn(inst, -3))
	inst.Status.Backup = &dbaasv1.BackupStatus{NextScheduledSnapshotTime: &overdue}
	r := newScheduleReconciler(t, inst)

	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}

	list := listAutomatedSnapshots(t, r, inst)
	if len(list) != 1 {
		t.Fatalf("automated snapshots created = %d, want exactly 1 (no multi-day catch-up)", len(list))
	}
	next := inst.Status.Backup.NextScheduledSnapshotTime
	if next == nil || !next.After(time.Now().UTC()) {
		t.Fatalf("NextScheduledSnapshotTime after firing = %v, want strictly future", next)
	}
}

func TestBackupScheduleWindowChangeRecalculatesNextFutureRun(t *testing.T) {
	inst := backupEnabledInstance(true)
	inst.Spec.Backup.Automated.PreferredWindowUTC = "02:00-03:00"
	original := metav1.NewTime(scheduledInstant(inst.UID, 2*time.Hour, time.Hour, time.Now().UTC().AddDate(0, 0, 1)))
	inst.Status.Backup = &dbaasv1.BackupStatus{NextScheduledSnapshotTime: &original}
	r := newScheduleReconciler(t, inst)

	inst.Spec.Backup.Automated.PreferredWindowUTC = "10:00-11:00"
	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}

	next := inst.Status.Backup.NextScheduledSnapshotTime
	if next == nil || next.Time.Equal(original.Time) {
		t.Fatalf("NextScheduledSnapshotTime = %v, want recalculated away from the old window's %v", next, original.Time)
	}
	if next.Hour() < 10 || next.Hour() >= 11 {
		t.Fatalf("NextScheduledSnapshotTime = %v, want inside the new 10:00-11:00 window", next.Time)
	}
}

func TestBackupScheduleSkipsWhenSourceNotAvailableButAdvancesSchedule(t *testing.T) {
	inst := backupEnabledInstance(true)
	inst.Status.Phase = dbaasv1.StatusStopped
	overdue := metav1.NewTime(defaultScheduledInstantOn(inst, -1))
	inst.Status.Backup = &dbaasv1.BackupStatus{NextScheduledSnapshotTime: &overdue}
	r := newScheduleReconciler(t, inst)

	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}

	assertNoAutomatedSnapshotCreated(t, r, inst)
	next := inst.Status.Backup.NextScheduledSnapshotTime
	if next == nil || !next.After(time.Now().UTC()) {
		t.Fatalf("NextScheduledSnapshotTime = %v, want advanced to the next future run even when skipped", next)
	}
	assertEventReason(t, r, string(dbaasv1.ReasonScheduledSnapshotSkipped))
}

func TestBackupScheduleCreatesDeterministicallyNamedSnapshotWhenDue(t *testing.T) {
	inst := backupEnabledInstance(true)
	due := metav1.NewTime(defaultScheduledInstantOn(inst, -1))
	inst.Status.Backup = &dbaasv1.BackupStatus{NextScheduledSnapshotTime: &due}
	r := newScheduleReconciler(t, inst)

	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}

	list := listAutomatedSnapshots(t, r, inst)
	if len(list) != 1 {
		t.Fatalf("automated snapshots = %d, want 1", len(list))
	}
	wantName := inst.Name + "-auto-" + due.Format(automatedSnapshotDateFmt)
	if list[0].Name != wantName {
		t.Fatalf("name = %q, want %q", list[0].Name, wantName)
	}
	if list[0].Spec.SourceInstanceRef.Name != inst.Name {
		t.Fatalf("SourceInstanceRef = %q, want %q", list[0].Spec.SourceInstanceRef.Name, inst.Name)
	}
	// Owned by the instance's UID, so GC deletes it with the instance.
	if !metav1.IsControlledBy(&list[0], inst) {
		t.Fatalf("OwnerReferences = %+v, want controller ref to DBInstance %s (UID %s)", list[0].OwnerReferences, inst.Name, inst.UID)
	}
	assertEventReason(t, r, string(dbaasv1.ReasonScheduledSnapshotCreated))

	// Re-entrant: reconciling again for the same due slot must not error or
	// duplicate — the deterministic name makes Create's AlreadyExists a no-op.
	inst.Status.Backup.NextScheduledSnapshotTime = &due
	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("second evaluateBackupSchedule for the same slot: %v", err)
	}
	if got := listAutomatedSnapshots(t, r, inst); len(got) != 1 {
		t.Fatalf("automated snapshots after re-entry = %d, want still 1", len(got))
	}
}

// Snapshot retention

func TestBackupSchedulePrunesAutomatedSnapshotsPastRetainCount(t *testing.T) {
	inst := backupEnabledInstance(true)
	inst.Spec.Backup.Automated.RetainCount = 2
	future := metav1.NewTime(time.Now().Add(24 * time.Hour))
	inst.Status.Backup = &dbaasv1.BackupStatus{NextScheduledSnapshotTime: &future} // not due this pass

	oldest := readyAutomatedSnapshot("orders-auto-1", inst, 3*time.Hour)
	middle := readyAutomatedSnapshot("orders-auto-2", inst, 2*time.Hour)
	newest := readyAutomatedSnapshot("orders-auto-3", inst, 1*time.Hour)
	manual := testSnapshot() // survives regardless of count
	notReady := readyAutomatedSnapshot("orders-auto-pending", inst, 30*time.Minute)
	notReady.Status.Conditions = nil // not yet Ready: never counted or pruned

	r := newScheduleReconciler(t, inst, oldest, middle, newest, manual, notReady)

	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}

	assertSnapshotExists(t, r, oldest.Name, false)
	assertEventReason(t, r, string(dbaasv1.ReasonScheduledSnapshotPruned))
	assertSnapshotExists(t, r, middle.Name, true)
	assertSnapshotExists(t, r, newest.Name, true)
	assertSnapshotExists(t, r, manual.Name, true)
	assertSnapshotExists(t, r, notReady.Name, true)
}

// A same-named predecessor's automated snapshots (another UID) are not this
// instance's to prune, nor counted against its retention; one already being
// deleted is neither usable capacity nor deleted again.
func TestBackupSchedulePruneIgnoresPredecessorAndDeletingSnapshots(t *testing.T) {
	inst := backupEnabledInstance(true)
	inst.Spec.Backup.Automated.RetainCount = 1
	future := metav1.NewTime(time.Now().Add(24 * time.Hour))
	inst.Status.Backup = &dbaasv1.BackupStatus{NextScheduledSnapshotTime: &future}

	predecessor := inst.DeepCopy()
	predecessor.UID = "orders-old-uid"
	theirs := readyAutomatedSnapshot("orders-auto-old", predecessor, 5*time.Hour)
	deleting := readyAutomatedSnapshot("orders-auto-4", inst, 30*time.Minute) // newest, but not capacity
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{dbaasv1.DBSnapshotFinalizerName}
	older := readyAutomatedSnapshot("orders-auto-2", inst, 3*time.Hour)
	newest := readyAutomatedSnapshot("orders-auto-3", inst, 1*time.Hour)

	r := newScheduleReconciler(t, inst, theirs, deleting, older, newest)
	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}

	assertSnapshotExists(t, r, theirs.Name, true)
	assertSnapshotExists(t, r, deleting.Name, true) // still held by its finalizer
	assertSnapshotExists(t, r, older.Name, false)
	assertSnapshotExists(t, r, newest.Name, true)
}

func TestBackupSchedulePauseDoesNotPrune(t *testing.T) {
	inst := backupEnabledInstance(false)
	inst.Spec.Backup.Automated.RetainCount = 1

	oldest := readyAutomatedSnapshot("orders-auto-1", inst, 2*time.Hour)
	newest := readyAutomatedSnapshot("orders-auto-2", inst, 1*time.Hour)
	r := newScheduleReconciler(t, inst, oldest, newest)

	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}

	assertSnapshotExists(t, r, oldest.Name, true)
	assertSnapshotExists(t, r, newest.Name, true)
}

// --- helpers ---

func listAutomatedSnapshots(t *testing.T, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) []dbaasv1.DBSnapshot {
	t.Helper()
	var list dbaasv1.DBSnapshotList
	if err := r.List(context.Background(), &list, client.InNamespace(inst.Namespace),
		client.MatchingFields{snapshotSourceInstanceIdx: inst.Name},
		client.MatchingLabels{dbaasv1.LabelSnapshotOrigin: dbaasv1.SnapshotOriginAutomated},
	); err != nil {
		t.Fatalf("list automated snapshots: %v", err)
	}
	return list.Items
}

func assertNoAutomatedSnapshotCreated(t *testing.T, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) {
	t.Helper()
	if got := listAutomatedSnapshots(t, r, inst); len(got) != 0 {
		t.Fatalf("automated snapshots created = %d, want 0", len(got))
	}
}

func assertSnapshotExists(t *testing.T, r *DBInstanceReconciler, name string, want bool) {
	t.Helper()
	var got dbaasv1.DBSnapshot
	err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-a", Name: name}, &got)
	exists := err == nil
	if exists != want {
		t.Fatalf("snapshot %q exists = %v, want %v (err=%v)", name, exists, want, err)
	}
}

func assertEventReason(t *testing.T, r *DBInstanceReconciler, wantReason string) {
	t.Helper()
	rec, ok := r.Recorder.(*record.FakeRecorder)
	if !ok {
		t.Fatal("Recorder is not a FakeRecorder")
	}
	select {
	case e := <-rec.Events:
		if !strings.Contains(e, wantReason) {
			t.Fatalf("event %q does not mention reason %q", e, wantReason)
		}
	default:
		t.Fatalf("no event recorded, want one mentioning %q", wantReason)
	}
}

// failedAutomatedSnapshot is an automated snapshot owned by owner that
// ended with reason (failed, timed out or rejected).
func failedAutomatedSnapshot(name string, owner *dbaasv1.DBInstance, age time.Duration, reason dbaasv1.ConditionReason) *dbaasv1.DBSnapshot {
	snap := readyAutomatedSnapshot(name, owner, age)
	snap.Status.Conditions = nil
	snap.Status.SetCondition(metav1.Condition{
		Type: dbaasv1.ConditionSnapshotReady, Status: metav1.ConditionFalse, Reason: string(reason), Message: "failed",
	})
	return snap
}

// A source whose backups keep failing must not accumulate one failed
// snapshot a day forever: the newest retainCount failed ones are kept for
// diagnosis, older ones pruned. Failed ones never count against the Ready
// snapshots' retention, and in-progress ones are never touched.
func TestBackupSchedulePrunesFailedAutomatedSnapshotsSeparately(t *testing.T) {
	inst := backupEnabledInstance(true)
	inst.Spec.Backup.Automated.RetainCount = 2
	future := metav1.NewTime(time.Now().Add(24 * time.Hour))
	inst.Status.Backup = &dbaasv1.BackupStatus{NextScheduledSnapshotTime: &future}

	failedOldest := failedAutomatedSnapshot("orders-auto-f1", inst, 9*time.Hour, dbaasv1.ReasonSnapshotBackupFailed)
	failedOld := failedAutomatedSnapshot("orders-auto-f2", inst, 8*time.Hour, dbaasv1.ReasonSnapshotBackupTimedOut)
	failedNewer := failedAutomatedSnapshot("orders-auto-f3", inst, 7*time.Hour, dbaasv1.ReasonSnapshotSourceNotReady)
	failedNewest := failedAutomatedSnapshot("orders-auto-f4", inst, 6*time.Hour, dbaasv1.ReasonSnapshotBackupFailed)
	ready1 := readyAutomatedSnapshot("orders-auto-r1", inst, 5*time.Hour)
	ready2 := readyAutomatedSnapshot("orders-auto-r2", inst, 4*time.Hour)
	inProgress := failedAutomatedSnapshot("orders-auto-running", inst, 10*time.Hour, dbaasv1.ReasonSnapshotBackupInProgress)
	predecessor := inst.DeepCopy()
	predecessor.UID = "orders-old-uid"
	theirsFailed := failedAutomatedSnapshot("orders-auto-old", predecessor, 20*time.Hour, dbaasv1.ReasonSnapshotBackupFailed)

	r := newScheduleReconciler(t, inst, failedOldest, failedOld, failedNewer, failedNewest, ready1, ready2, inProgress, theirsFailed)
	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}

	assertSnapshotExists(t, r, failedOldest.Name, false)
	assertSnapshotExists(t, r, failedOld.Name, false)
	assertSnapshotExists(t, r, failedNewer.Name, true)
	assertSnapshotExists(t, r, failedNewest.Name, true)
	assertSnapshotExists(t, r, ready1.Name, true) // 2 Ready within retainCount 2: failures don't count against them
	assertSnapshotExists(t, r, ready2.Name, true)
	assertSnapshotExists(t, r, inProgress.Name, true)
	assertSnapshotExists(t, r, theirsFailed.Name, true)
}

// ---- windows that cross midnight (e.g. 23:00-01:00) ----

func TestParsePreferredWindow(t *testing.T) {
	for window, want := range map[string][2]time.Duration{
		"02:00-03:00": {2 * time.Hour, time.Hour},
		"23:00-01:00": {23 * time.Hour, 2 * time.Hour}, // ends the next UTC day
		"23:30-00:00": {23*time.Hour + 30*time.Minute, 30 * time.Minute},
		"10:00-10:00": {2 * time.Hour, time.Hour}, // empty: the default
		"garbage":     {2 * time.Hour, time.Hour},
	} {
		start, dur := parsePreferredWindow(window)
		if start != want[0] || dur != want[1] {
			t.Errorf("parsePreferredWindow(%q) = (%v, %v), want (%v, %v)", window, start, dur, want[0], want[1])
		}
	}
}

// lateSlotUID finds a UID whose stable offset in a 2h window is at least an
// hour, so its slot in a 23:00-01:00 window falls after midnight.
func lateSlotUID(t *testing.T) types.UID {
	t.Helper()
	for i := 0; i < 1000; i++ {
		uid := types.UID(fmt.Sprintf("late-slot-%d", i))
		if stableOffsetWithinWindow(uid, 2*time.Hour) >= time.Hour {
			return uid
		}
	}
	t.Fatal("no UID with an after-midnight slot found")
	return ""
}

// At 00:10, a slot at 00:30 that belongs to yesterday's 23:00-01:00 window
// is the next run — not tomorrow night's.
func TestNextOccurrenceInAWindowCrossingMidnight(t *testing.T) {
	uid := lateSlotUID(t)
	start, dur := parsePreferredWindow("23:00-01:00")
	day := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	slot := scheduledInstant(uid, start, dur, day) // falls on Oct 9, after midnight
	if slot.Day() != 9 {
		t.Fatalf("test setup: slot %v should fall on Oct 9", slot)
	}

	justAfterMidnight := time.Date(2026, 10, 9, 0, 0, 30, 0, time.UTC)
	if got := nextOccurrence(uid, start, dur, justAfterMidnight); !got.Equal(slot) {
		t.Fatalf("nextOccurrence(%v) = %v, want the still-ahead slot %v", justAfterMidnight, got, slot)
	}
	if got := nextOccurrence(uid, start, dur, slot); !got.Equal(slot.AddDate(0, 0, 1)) {
		t.Fatalf("nextOccurrence(slot) = %v, want the next day's %v", got, slot.AddDate(0, 0, 1))
	}
}

// A due after-midnight slot is recognized as this window's own, so it
// fires — not mistaken for a changed window and pushed into the future.
func TestBackupScheduleFiresADueSlotInAWindowCrossingMidnight(t *testing.T) {
	inst := backupEnabledInstance(true)
	inst.UID = lateSlotUID(t)
	inst.Spec.Backup.Automated.PreferredWindowUTC = "23:00-01:00"
	start, dur := parsePreferredWindow("23:00-01:00")
	due := metav1.NewTime(scheduledInstant(inst.UID, start, dur, time.Now().UTC().AddDate(0, 0, -2)))
	inst.Status.Backup = &dbaasv1.BackupStatus{NextScheduledSnapshotTime: &due}
	r := newScheduleReconciler(t, inst)

	if _, err := r.evaluateBackupSchedule(context.Background(), inst); err != nil {
		t.Fatalf("evaluateBackupSchedule: %v", err)
	}

	if got := listAutomatedSnapshots(t, r, inst); len(got) != 1 {
		t.Fatalf("automated snapshots = %d, want the due slot %v to fire", len(got), due.Time)
	}
}
