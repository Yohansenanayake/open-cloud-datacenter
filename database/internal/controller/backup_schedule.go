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
	"hash/fnv"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// Automated snapshot scheduling and retention (spec §3.2/§3.3). Deliberately
// not an ensure.Step: that pipeline's Runner stops at the first non-Satisfied
// step and only the stopping step's ctrl.Result is honored (internal/ensure/runner.go),
// so a step here would either gate the rest of DBInstance provisioning on a
// once-a-day timer or have no way to ever request a timely wake-up. Instead
// this runs once per reconcile from reconcileInstance, after the ensure run,
// and its own RequeueAfter is merged into the result rather than replacing it
const (
	defaultRetainCount        = 7
	defaultPreferredWindowUTC = "02:00-03:00"
	automatedSnapshotDateFmt  = "20060102"
)

// evaluateBackupSchedule decides whether today's automated snapshot is due,
// creates it if so, and prunes automated snapshots past retainCount. It
// returns how soon it would like to be woken up again (0 = no preference).
func (r *DBInstanceReconciler) evaluateBackupSchedule(ctx context.Context, inst *dbaasv1.DBInstance) (time.Duration, error) {
	if inst.Spec.Backup == nil {
		return 0, nil
	}
	automated := inst.Spec.Backup.Automated

	if automated.Enabled != nil && !*automated.Enabled {
		//if disabled, until user re-enables, No automated backup scheduling and pruning happens
		if inst.Status.Backup != nil {
			inst.Status.Backup.NextScheduledSnapshotTime = nil
		}
		return 0, nil
	}

	windowStart, windowDur := parsePreferredWindow(automated.PreferredWindowUTC)
	now := time.Now().UTC()

	if inst.Status.Backup == nil {
		inst.Status.Backup = &dbaasv1.BackupStatus{}
	}
	next := inst.Status.Backup.NextScheduledSnapshotTime

	//recompute from scratch, always landing strictly in the future.
	if next == nil || !scheduledInstant(inst.UID, windowStart, windowDur, next.Time).Equal(next.Time) {
		fresh := nextOccurrence(inst.UID, windowStart, windowDur, now)
		inst.Status.Backup.NextScheduledSnapshotTime = &metav1.Time{Time: fresh}
		return r.pruneAndReturn(ctx, inst, automated, time.Until(fresh))
	}

	if now.Before(next.Time) {
		return r.pruneAndReturn(ctx, inst, automated, time.Until(next.Time))
	}

	// This slot is due, whether on time or late (e.g. after downtime). Try it
	// once and move on — a failed or skipped slot is never retried same-day
	if err := r.attemptScheduledSnapshot(ctx, inst, next.Time); err != nil {
		return 0, err
	}
	nextFuture := nextOccurrence(inst.UID, windowStart, windowDur, now)
	inst.Status.Backup.NextScheduledSnapshotTime = &metav1.Time{Time: nextFuture}
	return r.pruneAndReturn(ctx, inst, automated, time.Until(nextFuture))
}

func (r *DBInstanceReconciler) pruneAndReturn(ctx context.Context, inst *dbaasv1.DBInstance, automated dbaasv1.AutomatedBackupSpec, requeue time.Duration) (time.Duration, error) {
	retainCount := automated.RetainCount
	if retainCount <= 0 {
		retainCount = defaultRetainCount
	}
	if err := r.pruneAutomatedSnapshots(ctx, inst, retainCount); err != nil {
		return 0, err
	}
	return requeue, nil
}

// attemptScheduledSnapshot is one scheduled admission attempt. If source is not ready, it is skipped.
func (r *DBInstanceReconciler) attemptScheduledSnapshot(ctx context.Context, inst *dbaasv1.DBInstance, scheduledFor time.Time) error {
	logger := log.FromContext(ctx)

	if inst.Status.Phase != dbaasv1.StatusAvailable {
		msg := fmt.Sprintf("skipped automated snapshot: source is %q, not available", inst.Status.Phase)
		logger.Info(msg, "instance", inst.Name)
		if r.Recorder != nil {
			r.Recorder.Eventf(inst, corev1.EventTypeWarning, string(dbaasv1.ReasonScheduledSnapshotSkipped), "%s", msg)
		}
		return nil
	}

	// Owned by the instance, so the garbage collector deletes automated
	// snapshots once their instance is gone — matched by UID, never by
	// name — while manual snapshots (never owned) survive it. Each GC
	// delete still goes through the DBSnapshot's own protected deletion.
	name := fmt.Sprintf("%s-auto-%s", inst.Name, scheduledFor.Format(automatedSnapshotDateFmt))
	snap := &dbaasv1.DBSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       inst.Namespace,
			Labels:          map[string]string{dbaasv1.LabelSnapshotOrigin: dbaasv1.SnapshotOriginAutomated},
			OwnerReferences: []metav1.OwnerReference{*instanceOwnerRef(inst)},
		},
		Spec: dbaasv1.DBSnapshotSpec{
			SourceInstanceRef: corev1.LocalObjectReference{Name: inst.Name},
		},
	}
	if err := r.Create(ctx, snap); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil // this slot's snapshot already exists — nothing to do
		}
		return fmt.Errorf("create automated DBSnapshot %s: %w", name, err)
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(inst, corev1.EventTypeNormal, string(dbaasv1.ReasonScheduledSnapshotCreated), "Created automated snapshot %s", name)
	}
	return nil
}

// pruneAutomatedSnapshots deletes automated DBSnapshots past retainCount,
// keeping the newest. Only this instance's own (owned by its UID — a
// same-named predecessor's are never touched), Ready, not-yet-deleting ones
// count or are pruned: a failed, in-progress or already-deleting one is
// neither retained capacity nor a deletion target.
//
// No restore-hold check here: DBSnapshot deletion itself waits for any
// restore reading the source's snapshots, so a pruned snapshot a restore
// still needs is over-retained (spec §3.3) until that restore ends.
func (r *DBInstanceReconciler) pruneAutomatedSnapshots(ctx context.Context, inst *dbaasv1.DBInstance, retainCount int) error {
	var list dbaasv1.DBSnapshotList
	if err := r.List(ctx, &list, client.InNamespace(inst.Namespace),
		client.MatchingFields{snapshotSourceInstanceIdx: inst.Name},
		client.MatchingLabels{dbaasv1.LabelSnapshotOrigin: dbaasv1.SnapshotOriginAutomated},
	); err != nil {
		return err
	}

	ready := make([]*dbaasv1.DBSnapshot, 0, len(list.Items))
	for i := range list.Items {
		snap := &list.Items[i]
		if metav1.IsControlledBy(snap, inst) && snap.DeletionTimestamp.IsZero() &&
			snap.Status.IsConditionTrue(dbaasv1.ConditionSnapshotReady) {
			ready = append(ready, snap)
		}
	}
	if len(ready) <= retainCount {
		return nil
	}
	sort.Slice(ready, func(i, j int) bool {
		return ready[i].CreationTimestamp.After(ready[j].CreationTimestamp.Time)
	})

	for _, snap := range ready[retainCount:] {
		uid := snap.UID
		if err := r.Delete(ctx, snap, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("prune automated DBSnapshot %s: %w", snap.Name, err)
		}
	}
	return nil
}

// parsePreferredWindow parses "HH:MM-HH:MM" into the window's start-of-day
// offset and duration. Falls back to the documented default on anything
// malformed or empty (e.g. a unit test building a DBInstance by hand, where
// the +kubebuilder:default never ran) rather than erroring out of a
// report-only step.
func parsePreferredWindow(window string) (start, duration time.Duration) {
	parse := func(w string) (time.Duration, time.Duration, bool) {
		lo, hi, ok := strings.Cut(w, "-")
		if !ok {
			return 0, 0, false
		}
		loT, err := time.Parse("15:04", lo)
		if err != nil {
			return 0, 0, false
		}
		hiT, err := time.Parse("15:04", hi)
		if err != nil {
			return 0, 0, false
		}
		s := loT.Sub(loT.Truncate(24 * time.Hour))
		e := hiT.Sub(hiT.Truncate(24 * time.Hour))
		if e <= s {
			return 0, 0, false
		}
		return s, e - s, true
	}

	if s, d, ok := parse(window); ok {
		return s, d
	}
	s, d, _ := parse(defaultPreferredWindowUTC)
	return s, d
}

// stableOffsetWithinWindow deterministically hashes the instance UID to one
// offset inside a window of the given duration, so every instance's daily
// snapshot lands at its own stable minute instead of all instances starting
// at the window's edge together.
func stableOffsetWithinWindow(uid types.UID, windowDur time.Duration) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(uid))
	minutes := int64(windowDur / time.Minute)
	if minutes <= 0 {
		return 0
	}
	return time.Duration(int64(h.Sum32())%minutes) * time.Minute
}

// scheduledInstant is the exact timestamp this instance's stable slot falls
// on for the UTC calendar date of "on" (only on's date is used, its
// time-of-day is ignored) — e.g. window 02:00-03:00 plus a 12-minute offset
// gives that date's 02:12 UTC.
//
// A pure function of (uid, window, date): calling it again for the same date
// always gives the same answer. That's what lets evaluateBackupSchedule
// detect a changed window by simply recomputing and comparing, with no extra
// state needed to remember what the old window was.
func scheduledInstant(uid types.UID, windowStart, windowDur time.Duration, on time.Time) time.Time {
	offset := stableOffsetWithinWindow(uid, windowDur)
	midnight := time.Date(on.Year(), on.Month(), on.Day(), 0, 0, 0, 0, time.UTC)
	return midnight.Add(windowStart).Add(offset)
}

// nextOccurrence returns the next instant, strictly after "after", that this
// instance's stable slot occurs: today's slot if it hasn't happened yet,
// otherwise tomorrow's.
func nextOccurrence(uid types.UID, windowStart, windowDur time.Duration, after time.Time) time.Time {
	candidate := scheduledInstant(uid, windowStart, windowDur, after)
	if !candidate.After(after) {
		candidate = scheduledInstant(uid, windowStart, windowDur, after.AddDate(0, 0, 1))
	}
	return candidate
}
