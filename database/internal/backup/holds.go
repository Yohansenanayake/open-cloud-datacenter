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

// Package backup holds the coordination primitives for DBaaS backup and
// restore, described in yohan-docs/backups/harvester-vm-backup/. There is no
// custom coordination CRD: a snapshot hold is a plain coordination.k8s.io/v1
// Lease per source instance, a restore hold is a Lease per restore attempt,
// and the deletion gate is the target object's own deletionTimestamp — never
// a separate flag.
//
// These Leases are existence/holder markers only, not full leader-election
// objects: no renewal loop, no time-based expiry, no stealing a stale lease.
// A hold is released only by an explicit Release call from the holder that
// acquired it (or by the deletion-cleanup path that follows a failed/
// cancelled attempt) — never by elapsed time.
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
// hold. A source can have several of these active at once — one per
// concurrent restore reading from one of its snapshots.
func RestoreHoldName(targetUID types.UID) string {
	return fmt.Sprintf("dbaas-restore-hold-%s", targetUID)
}

// SourceUIDLabel labels every hold Lease with the source instance's UID, so
// deletion can find every restore hold naming a given source via a live List
// without needing to know each restore attempt's target UID in advance.
const SourceUIDLabel = "dbaas.opencloud.wso2.com/source-uid"

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

// Acquire creates the named Lease with holderIdentity if absent. If it
// already exists and is held by holderIdentity, that's treated as already
// acquired (safe to call again across reconciles). If held by someone else,
// Acquire reports that holder and does not touch the Lease.
//
// owner is set as the Lease's sole owner reference and is required, not
// optional — every hold in this design has exactly one natural owner (the
// source instance for a snapshot hold, the restoring instance for a restore
// hold), so there is no legitimate case for an unowned hold. Acquire rejects
// a nil owner rather than silently creating one, because that owner
// reference is a safety net: it's not a substitute for actively waiting on a
// hold before allowing its owner to be deleted (that's the deletion path's
// job), but it ensures a hold orphaned by a bug or an unhandled case doesn't
// become permanent garbage — Kubernetes' own garbage collector cleans it up
// once the owner is actually gone. This is event-driven, not time-based —
// Leases here never expire on elapsed time (see the package doc).
func Acquire(ctx context.Context, c client.Client, namespace, name, holderIdentity string, owner *metav1.OwnerReference, extraLabels map[string]string) (AcquireResult, error) {
	if owner == nil {
		return AcquireResult{}, fmt.Errorf("acquire hold %s/%s: owner must not be nil", namespace, name)
	}

	var lease coordinationv1.Lease
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &lease)
	switch {
	case err == nil:
		if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == holderIdentity {
			return AcquireResult{Acquired: true}, nil
		}
		held := ""
		if lease.Spec.HolderIdentity != nil {
			held = *lease.Spec.HolderIdentity
		}
		return AcquireResult{Acquired: false, HolderIdentity: held}, nil

	case apierrors.IsNotFound(err):
		holder := holderIdentity
		lease = coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				Namespace:       namespace,
				Labels:          extraLabels,
				OwnerReferences: []metav1.OwnerReference{*owner},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity: &holder,
			},
		}
		if createErr := c.Create(ctx, &lease); createErr != nil {
			if apierrors.IsAlreadyExists(createErr) {
				// Lost a create race — re-read and report the real holder
				// rather than treating this as acquired.
				return Acquire(ctx, c, namespace, name, holderIdentity, owner, extraLabels)
			}
			return AcquireResult{}, createErr
		}
		return AcquireResult{Acquired: true}, nil

	default:
		return AcquireResult{}, err
	}
}

// Release deletes the named Lease if, and only if, it is currently held by
// holderIdentity. Releasing a Lease held by someone else, or one that's
// already gone, is a no-op — safe to call unconditionally during cleanup.
func Release(ctx context.Context, c client.Client, namespace, name, holderIdentity string) error {
	var lease coordinationv1.Lease
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &lease)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return err
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != holderIdentity {
		return nil
	}
	if err := c.Delete(ctx, &lease); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// Held reports whether the named Lease currently exists and, if so, who
// holds it. A live Get, never served from a cache the caller doesn't
// control — callers deciding whether to acquire a hold must see the current
// state, not a stale one.
func Held(ctx context.Context, c client.Client, namespace, name string) (holderIdentity string, held bool, err error) {
	var lease coordinationv1.Lease
	getErr := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &lease)
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
