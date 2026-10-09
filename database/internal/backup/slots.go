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

package backup

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Backup slots cap how many DBSnapshot backups run at once (design:
// dbaas-backup-concurrency-design.md). A slot is a Lease in the operator
// namespace named dbaas-backup-slot-<index>, index in [0, max): it exists
// only while granted, and its holder is the snapshot allowed to run. The
// indexed names make the global cap an API-server guarantee — two grants of
// one index can't both succeed — even if two operators briefly overlap.
// Only the backup dispatcher grants; a snapshot only checks, and releases,
// its own slot.
const (
	slotLeasePrefix = "dbaas-backup-slot-"

	// SlotLabel marks every slot Lease, so they can be listed together.
	SlotLabel = "dbaas.opencloud.wso2.com/backup-slot"
	// SlotNamespaceLabel carries the holder snapshot's namespace, which is
	// what per-namespace shares are counted by.
	SlotNamespaceLabel = "dbaas.opencloud.wso2.com/snapshot-namespace"
	// SlotSnapshotAnnotation carries the holder as "<namespace>/<name>".
	SlotSnapshotAnnotation = "dbaas.opencloud.wso2.com/snapshot"
)

// SlotHolder identifies the DBSnapshot a slot is granted to. UID is the
// identity; namespace and name locate it.
type SlotHolder struct {
	Namespace string
	Name      string
	UID       types.UID
}

func (h SlotHolder) String() string { return h.Namespace + "/" + h.Name }

// Slot is one granted slot as read from the API server.
type Slot struct {
	Index  int
	Holder SlotHolder

	uid             types.UID
	resourceVersion string
}

// Slots performs slot operations. Like Holds, every read is uncached (Live):
// a dispatcher pass must see the grants the previous pass just made, or it
// would grant the same capacity twice.
type Slots struct {
	Live      client.Reader
	Writer    client.Writer
	Namespace string
}

func (s Slots) check() error {
	if s.Live == nil || s.Writer == nil || s.Namespace == "" {
		return fmt.Errorf("backup.Slots needs an uncached Live reader, a Writer and a namespace")
	}
	return nil
}

// SlotName is the Lease name of slot index.
func SlotName(index int) string { return slotLeasePrefix + strconv.Itoa(index) }

// List returns every granted slot, ordered by index. A Lease carrying the
// slot label but not a slot's shape (bad name, no holder) is skipped: it
// grants nothing.
func (s Slots) List(ctx context.Context) ([]Slot, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	var leases coordinationv1.LeaseList
	if err := s.Live.List(ctx, &leases, client.InNamespace(s.Namespace), client.HasLabels{SlotLabel}); err != nil {
		return nil, err
	}
	slots := make([]Slot, 0, len(leases.Items))
	for i := range leases.Items {
		if slot, ok := slotFrom(&leases.Items[i]); ok {
			slots = append(slots, slot)
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].Index < slots[j].Index })
	return slots, nil
}

func slotFrom(lease *coordinationv1.Lease) (Slot, bool) {
	index, err := strconv.Atoi(strings.TrimPrefix(lease.Name, slotLeasePrefix))
	if err != nil || !strings.HasPrefix(lease.Name, slotLeasePrefix) || index < 0 ||
		lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
		return Slot{}, false
	}
	ns, name, _ := strings.Cut(lease.Annotations[SlotSnapshotAnnotation], "/")
	if ns == "" {
		ns = lease.Labels[SlotNamespaceLabel]
	}
	return Slot{
		Index:           index,
		Holder:          SlotHolder{Namespace: ns, Name: name, UID: types.UID(*lease.Spec.HolderIdentity)},
		uid:             lease.UID,
		resourceVersion: lease.ResourceVersion,
	}, true
}

// Grant creates slot index for holder. granted is false, with no error,
// when that index is already taken — someone else's grant won.
func (s Slots) Grant(ctx context.Context, index int, holder SlotHolder) (granted bool, err error) {
	if err := s.check(); err != nil {
		return false, err
	}
	identity := string(holder.UID)
	now := metav1.NewMicroTime(time.Now())
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:        SlotName(index),
			Namespace:   s.Namespace,
			Labels:      map[string]string{SlotLabel: "true", SlotNamespaceLabel: holder.Namespace},
			Annotations: map[string]string{SlotSnapshotAnnotation: holder.String()},
		},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &identity, AcquireTime: &now},
	}
	switch err := s.Writer.Create(ctx, lease); {
	case err == nil:
		return true, nil
	case apierrors.IsAlreadyExists(err):
		return false, nil
	default:
		return false, err
	}
}

// HeldBy returns the slot granted to the snapshot with uid, if any.
func (s Slots) HeldBy(ctx context.Context, uid types.UID) (*Slot, error) {
	slots, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range slots {
		if slots[i].Holder.UID == uid {
			return &slots[i], nil
		}
	}
	return nil, nil
}

// Release frees every slot granted to the snapshot with uid. Releasing
// none is a no-op, so it's safe to call on every terminal path.
func (s Slots) Release(ctx context.Context, uid types.UID) error {
	slots, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, slot := range slots {
		if slot.Holder.UID == uid {
			if err := s.Free(ctx, slot); err != nil {
				return err
			}
		}
	}
	return nil
}

// Free deletes exactly the slot object that was read, preconditioned on its
// UID and resourceVersion: a slot re-granted in between is never freed by
// mistake. Already gone is success.
func (s Slots) Free(ctx context.Context, slot Slot) error {
	if err := s.check(); err != nil {
		return err
	}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: SlotName(slot.Index), Namespace: s.Namespace}}
	uid, rv := slot.uid, slot.resourceVersion
	if err := s.Writer.Delete(ctx, lease, client.Preconditions{UID: &uid, ResourceVersion: &rv}); err != nil &&
		!apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return err
	}
	return nil
}

// Candidate is a snapshot waiting for a slot.
type Candidate struct {
	Holder  SlotHolder
	Created time.Time
	// Source is the snapshot's source DBInstance name, for the caller's
	// eligibility check (is that instance's snapshot hold free?).
	Source string
}

// Grant is one slot the dispatcher should create.
type Grant struct {
	Index  int
	Holder SlotHolder
}

// PlanGrants assigns the lowest free slot indices within global and per-namespace
// limits. Existing grants above a lowered global limit are allowed to drain.
// Candidates are ordered by creation time, then namespace/name. Namespaces at
// their limit and ineligible candidates are skipped so other tenants can proceed.
// The optional eligibility check runs only for candidates otherwise able to
// receive a slot; nil accepts all candidates.
func PlanGrants(waiting []Candidate, granted []Slot, maxConcurrent, perNamespace int, eligible func(Candidate) bool) []Grant {
	used := map[int]bool{}
	perNS := map[string]int{}
	holding := map[types.UID]bool{}
	for _, slot := range granted {
		used[slot.Index] = true
		perNS[slot.Holder.Namespace]++
		holding[slot.Holder.UID] = true
	}
	free := maxConcurrent - len(granted)
	if free <= 0 {
		return nil
	}

	queue := append([]Candidate(nil), waiting...)
	sort.SliceStable(queue, func(i, j int) bool {
		a, b := queue[i], queue[j]
		if !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		if a.Holder.Namespace != b.Holder.Namespace {
			return a.Holder.Namespace < b.Holder.Namespace
		}
		return a.Holder.Name < b.Holder.Name
	})

	var grants []Grant
	next := 0
	for _, c := range queue {
		if free == 0 {
			break
		}
		if holding[c.Holder.UID] || perNS[c.Holder.Namespace] >= perNamespace {
			continue
		}
		if eligible != nil && !eligible(c) {
			continue
		}
		for used[next] {
			next++
		}
		grants = append(grants, Grant{Index: next, Holder: c.Holder})
		used[next] = true
		perNS[c.Holder.Namespace]++
		holding[c.Holder.UID] = true
		free--
	}
	return grants
}
