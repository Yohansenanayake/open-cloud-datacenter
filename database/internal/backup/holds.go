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

// Package backup coordinates snapshots and restores with Kubernetes Leases.
// Snapshot holds are per source instance; restore holds are per attempt.
// DeletionTimestamp prevents new operations during deletion.
//
// Holds have no renewal or expiry. Only the holder or deletion cleanup releases
// them; elapsed time never permits another operation to take the hold.
package backup

import (
	"context"
	"fmt"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SnapshotHoldName is the deterministic Lease name for a source instance's
// snapshot hold — at most one is ever active per instance, shared with
// repave (they exclude each other through this same Lease, not two separate
// mechanisms).
func SnapshotHoldName(sourceUID types.UID) string {
	return fmt.Sprintf("dbaas-snapshot-hold-%s", sourceUID)
}

// RestoreHoldName is the deterministic Lease name for one restore attempt's
// hold, keyed by that DBRestore's own UID — not the target DBInstance's,
// which doesn't exist yet when the hold is first acquired (see
// DBRestoreReconciler.restoreVolume). A source can have several of these
// active at once — one per concurrent restore reading from one of its
// snapshots.
func RestoreHoldName(restoreUID types.UID) string {
	return fmt.Sprintf("dbaas-restore-hold-%s", restoreUID)
}

// SourceUIDLabel labels every hold Lease with the source instance's UID, so
// deletion can find every restore hold naming a given source via a live List
// without needing to know each restore attempt's DBRestore UID in advance.
const SourceUIDLabel = "dbaas.opencloud.wso2.com/source-uid"

// Holds performs hold operations. Every read goes through Live, an uncached
// reader (in production, the manager's APIReader): these reads decide mutual
// exclusion, and a decision made from an informer cache that hasn't yet seen
// another party's Lease — or its deletion — isn't mutual exclusion at all.
// Writes go through Writer. There is deliberately no fallback to a cached
// client: a Holds without Live refuses to operate.
type Holds struct {
	Live   client.Reader
	Writer client.Writer
}

func (h Holds) check() error {
	if h.Live == nil || h.Writer == nil {
		return fmt.Errorf("backup.Holds needs both an uncached Live reader and a Writer")
	}
	return nil
}

// AcquireResult reports the outcome of Acquire.
type AcquireResult struct {
	// Acquired is true when the caller now holds the lease (either it just
	// created it, or it already held it — Acquire is idempotent for its
	// own holder).
	Acquired bool
	// HolderIdentity is who currently holds the lease when Acquired is
	// false, for error messages/logging.
	HolderIdentity string
}

// Acquire creates an owned Lease or returns its current holder. It never
// replaces another holder. The required owner reference permits garbage
// collection after owner deletion, but callers must still wait for holds
// before deleting owners. Holds do not expire.
func (h Holds) Acquire(ctx context.Context, namespace, name, holderIdentity string, owner *metav1.OwnerReference, extraLabels map[string]string) (AcquireResult, error) {
	if err := h.check(); err != nil {
		return AcquireResult{}, err
	}
	if owner == nil {
		return AcquireResult{}, fmt.Errorf("acquire hold %s/%s: owner must not be nil", namespace, name)
	}

	key := types.NamespacedName{Namespace: namespace, Name: name}
	var lease coordinationv1.Lease
	err := h.Live.Get(ctx, key, &lease)
	switch {
	case err == nil:
		return resultFor(&lease, holderIdentity), nil
	case !apierrors.IsNotFound(err):
		return AcquireResult{}, err
	}

	holder := holderIdentity
	lease = coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			Labels:          extraLabels,
			OwnerReferences: []metav1.OwnerReference{*owner},
		},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder},
	}
	createErr := h.Writer.Create(ctx, &lease)
	switch {
	case createErr == nil:
		return AcquireResult{Acquired: true}, nil
	case !apierrors.IsAlreadyExists(createErr):
		return AcquireResult{}, createErr
	}

	// Lost a create race: report the real holder rather than assuming it's
	// us. One live re-read — if the winner already released it again, that's
	// a transient error, and the caller's next pass simply retries.
	if err := h.Live.Get(ctx, key, &lease); err != nil {
		return AcquireResult{}, fmt.Errorf("acquire hold %s/%s: re-read after losing create race: %w", namespace, name, err)
	}
	return resultFor(&lease, holderIdentity), nil
}

func resultFor(lease *coordinationv1.Lease, holderIdentity string) AcquireResult {
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == holderIdentity {
		return AcquireResult{Acquired: true}
	}
	held := ""
	if lease.Spec.HolderIdentity != nil {
		held = *lease.Spec.HolderIdentity
	}
	return AcquireResult{Acquired: false, HolderIdentity: held}
}

// Release deletes the named Lease if, and only if, it is currently held by
// holderIdentity. Releasing a Lease held by someone else, or one that's
// already gone, is a no-op — safe to call unconditionally during cleanup.
// The delete is preconditioned on the exact object read, so a Lease that was
// replaced in between is never deleted by mistake.
func (h Holds) Release(ctx context.Context, namespace, name, holderIdentity string) error {
	if err := h.check(); err != nil {
		return err
	}
	var lease coordinationv1.Lease
	err := h.Live.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &lease)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return err
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != holderIdentity {
		return nil
	}
	uid, rv := lease.UID, lease.ResourceVersion
	if err := h.Writer.Delete(ctx, &lease, client.Preconditions{UID: &uid, ResourceVersion: &rv}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// AnyRestoreHoldForSource reports whether a restore-hold Lease exists for
// sourceUID. Used by DBSnapshot deletion (wait before deleting the backend
// backup) and, later, source DBInstance deletion.
//
// Coarse: holds aren't labeled by snapshot (that's read from the owning
// DBRestore's spec.snapshotRef instead), so this can't tell "restoring from
// this snapshot" from "restoring from a sibling snapshot of the same
// source" — never unsafe, just sometimes broader than necessary.
func (h Holds) AnyRestoreHoldForSource(ctx context.Context, namespace string, sourceUID types.UID) (bool, error) {
	if err := h.check(); err != nil {
		return false, err
	}
	var list coordinationv1.LeaseList
	if err := h.Live.List(ctx, &list, client.InNamespace(namespace), client.MatchingLabels{SourceUIDLabel: string(sourceUID)}); err != nil {
		return false, err
	}
	return len(list.Items) > 0, nil
}

// Held reports whether the named Lease currently exists and, if so, who
// holds it.
func (h Holds) Held(ctx context.Context, namespace, name string) (holderIdentity string, held bool, err error) {
	if err := h.check(); err != nil {
		return "", false, err
	}
	var lease coordinationv1.Lease
	getErr := h.Live.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &lease)
	switch {
	case apierrors.IsNotFound(getErr):
		return "", false, nil
	case getErr != nil:
		return "", false, getErr
	}
	if lease.Spec.HolderIdentity == nil {
		return "", false, nil
	}
	return *lease.Spec.HolderIdentity, true, nil
}
