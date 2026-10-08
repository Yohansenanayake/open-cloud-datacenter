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
	"reflect"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testSlots(t *testing.T) Slots {
	c := newFakeClient(t)
	return Slots{Live: c, Writer: c, Namespace: "dbaas-system"}
}

func holder(ns, name string) SlotHolder {
	return SlotHolder{Namespace: ns, Name: name, UID: types.UID(ns + "-" + name + "-uid")}
}

func TestSlotsRefuseToOperateWithoutLiveReaderOrNamespace(t *testing.T) {
	c := newFakeClient(t)
	for name, s := range map[string]Slots{
		"no live reader": {Writer: c, Namespace: "dbaas-system"},
		"no namespace":   {Live: c, Writer: c},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.List(context.Background()); err == nil {
				t.Fatal("List must error")
			}
			if _, err := s.Grant(context.Background(), 0, holder("a", "s1")); err == nil {
				t.Fatal("Grant must error")
			}
		})
	}
}

func TestSlotGrantIsExclusivePerIndex(t *testing.T) {
	ctx := context.Background()
	s := testSlots(t)

	if granted, err := s.Grant(ctx, 0, holder("a", "s1")); err != nil || !granted {
		t.Fatalf("first Grant = (%v, %v), want granted", granted, err)
	}
	if granted, err := s.Grant(ctx, 0, holder("b", "s2")); err != nil || granted {
		t.Fatalf("second Grant of the same index = (%v, %v), want not granted, no error", granted, err)
	}

	slots, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(slots) != 1 || slots[0].Index != 0 || slots[0].Holder != holder("a", "s1") {
		t.Fatalf("slots = %+v, want only index 0 held by a/s1", slots)
	}
}

func TestSlotHeldByAndRelease(t *testing.T) {
	ctx := context.Background()
	s := testSlots(t)
	for i, h := range []SlotHolder{holder("a", "s1"), holder("b", "s2")} {
		if _, err := s.Grant(ctx, i, h); err != nil {
			t.Fatalf("Grant: %v", err)
		}
	}

	got, err := s.HeldBy(ctx, holder("b", "s2").UID)
	if err != nil || got == nil || got.Index != 1 {
		t.Fatalf("HeldBy(b/s2) = (%+v, %v), want slot 1", got, err)
	}
	if err := s.Release(ctx, holder("b", "s2").UID); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got, _ := s.HeldBy(ctx, holder("b", "s2").UID); got != nil {
		t.Fatalf("b/s2 still holds %+v after Release", got)
	}
	if got, _ := s.HeldBy(ctx, holder("a", "s1").UID); got == nil {
		t.Fatal("releasing b/s2 must not free a/s1's slot")
	}
	if err := s.Release(ctx, "nobody"); err != nil {
		t.Fatalf("releasing nothing must be a no-op, got %v", err)
	}
}

// Free is preconditioned on the exact object read: a slot re-granted in
// between (same name, new UID and resourceVersion) survives, and Free
// doesn't error — the slot it meant to free is already gone. The fake
// client only enforces the resourceVersion half of the precondition; the
// API server enforces both.
func TestSlotFreeNeverFreesAReGrantedSlot(t *testing.T) {
	ctx := context.Background()
	identity := string(holder("b", "s2").UID)
	regranted := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name: SlotName(0), Namespace: "dbaas-system", UID: "regranted-uid", ResourceVersion: "7",
			Labels:      map[string]string{SlotLabel: "true", SlotNamespaceLabel: "b"},
			Annotations: map[string]string{SlotSnapshotAnnotation: "b/s2"},
		},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &identity},
	}
	scheme := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	c := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(regranted).Build()
	s := Slots{Live: c, Writer: c, Namespace: "dbaas-system"}
	stale := Slot{Index: 0, Holder: holder("a", "s1"), uid: "the-slot-we-read", resourceVersion: "5"}

	if err := s.Free(ctx, stale); err != nil {
		t.Fatalf("Free of a stale read must not error, got %v", err)
	}
	if got, _ := s.HeldBy(ctx, holder("b", "s2").UID); got == nil {
		t.Fatal("a stale Free deleted the slot re-granted to b/s2")
	}
}

// Leases that carry the slot label but aren't valid slots grant nothing.
func TestSlotListSkipsMalformedLeases(t *testing.T) {
	ctx := context.Background()
	s := testSlots(t)
	for _, name := range []string{"dbaas-backup-slot-x", "something-else"} {
		lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: s.Namespace, Labels: map[string]string{SlotLabel: "true"},
		}}
		if err := s.Writer.Create(ctx, lease); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if slots, err := s.List(ctx); err != nil || len(slots) != 0 {
		t.Fatalf("List = (%+v, %v), want no slots", slots, err)
	}
}

// ---- PlanGrants ----

var t0 = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

func waiting(ns, name string, ageOrder int) Candidate {
	return Candidate{Holder: holder(ns, name), Created: t0.Add(time.Duration(ageOrder) * time.Second)}
}

func slot(index int, ns, name string) Slot {
	return Slot{Index: index, Holder: holder(ns, name)}
}

func grantNames(grants []Grant) []string {
	out := make([]string, 0, len(grants))
	for _, g := range grants {
		out = append(out, fmt.Sprintf("%d:%s", g.Index, g.Holder))
	}
	return out
}

func TestPlanGrants(t *testing.T) {
	for name, tc := range map[string]struct {
		waiting  []Candidate
		granted  []Slot
		k, kNS   int
		eligible func(Candidate) bool
		want     []string
	}{
		"nothing waiting": {
			k: 4, kNS: 2, want: []string{},
		},
		"oldest first, up to the global cap": {
			waiting: []Candidate{waiting("a", "s3", 3), waiting("b", "s1", 1), waiting("c", "s2", 2), waiting("d", "s4", 4)},
			k:       3, kNS: 2,
			want: []string{"0:b/s1", "1:c/s2", "2:a/s3"},
		},
		"global cap already reached": {
			waiting: []Candidate{waiting("a", "s1", 1)},
			granted: []Slot{slot(0, "b", "x"), slot(1, "c", "y")},
			k:       2, kNS: 2,
			want: []string{},
		},
		// Tenant a's backlog is older, but once a has used its share, b's
		// younger snapshot is served instead of waiting behind it.
		"a namespace at its share is skipped, not waited behind": {
			waiting: []Candidate{waiting("a", "s1", 1), waiting("a", "s2", 2), waiting("a", "s3", 3), waiting("b", "s4", 4)},
			k:       4, kNS: 2,
			want: []string{"0:a/s1", "1:a/s2", "2:b/s4"},
		},
		"existing grants count against the share": {
			waiting: []Candidate{waiting("a", "s2", 2), waiting("b", "s3", 3)},
			granted: []Slot{slot(0, "a", "s1"), slot(1, "a", "s0")},
			k:       4, kNS: 2,
			want: []string{"2:b/s3"},
		},
		"new grants take the lowest free indices": {
			waiting: []Candidate{waiting("b", "s2", 2), waiting("c", "s3", 3)},
			granted: []Slot{slot(1, "a", "s1")},
			k:       4, kNS: 2,
			want: []string{"0:b/s2", "2:c/s3"},
		},
		"a candidate that already holds a slot is not granted twice": {
			waiting: []Candidate{waiting("a", "s1", 1), waiting("b", "s2", 2)},
			granted: []Slot{slot(0, "a", "s1")},
			k:       4, kNS: 2,
			want: []string{"1:b/s2"},
		},
		// After the cap is lowered, slots above it still count until they
		// drain, and nothing is granted above the new cap.
		"lowered cap: excess drains before anything new starts": {
			waiting: []Candidate{waiting("c", "s9", 9)},
			granted: []Slot{slot(2, "a", "s1"), slot(3, "b", "s2")},
			k:       2, kNS: 1,
			want: []string{},
		},
		"ties in creation time break by namespace, then name": {
			waiting: []Candidate{waiting("b", "s1", 1), waiting("a", "s2", 1), waiting("a", "s1", 1)},
			k:       2, kNS: 2,
			want: []string{"0:a/s1", "1:a/s2"},
		},
		// An ineligible candidate (its instance mid-repave) is skipped like
		// one over its share: the next eligible one gets the slot.
		"an ineligible candidate is skipped, not waited behind": {
			waiting: []Candidate{waiting("a", "s1", 1), waiting("b", "s2", 2)},
			k:       1, kNS: 1,
			eligible: func(c Candidate) bool { return c.Holder.Name != "s1" },
			want:     []string{"0:b/s2"},
		},
		"share of 1 with a cap of 1": {
			waiting: []Candidate{waiting("a", "s1", 1), waiting("a", "s2", 2)},
			k:       1, kNS: 1,
			want: []string{"0:a/s1"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := grantNames(PlanGrants(tc.waiting, tc.granted, tc.k, tc.kNS, tc.eligible))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("grants = %v, want %v", got, tc.want)
			}
		})
	}
}

// Invariant check over many random-ish shapes: grants never push the total
// over the cap, any namespace over its share, or reuse an index.
func TestPlanGrantsNeverExceedsCaps(t *testing.T) {
	namespaces := []string{"a", "b", "c"}
	for k := 1; k <= 5; k++ {
		kNS := max(1, (k+1)/2)
		for pre := 0; pre <= k; pre++ {
			var granted []Slot
			for i := 0; i < pre; i++ {
				granted = append(granted, slot(i, namespaces[i%len(namespaces)], fmt.Sprintf("g%d", i)))
			}
			var queue []Candidate
			for i := 0; i < 12; i++ {
				queue = append(queue, waiting(namespaces[(i*7)%len(namespaces)], fmt.Sprintf("w%d", i), i))
			}

			grants := PlanGrants(queue, granted, k, kNS, nil)

			total := len(granted) + len(grants)
			perNS := map[string]int{}
			indices := map[int]bool{}
			for _, s := range granted {
				perNS[s.Holder.Namespace]++
				indices[s.Index] = true
			}
			for _, g := range grants {
				perNS[g.Holder.Namespace]++
				if indices[g.Index] || g.Index >= k {
					t.Fatalf("k=%d pre=%d: grant index %d reused or out of range", k, pre, g.Index)
				}
				indices[g.Index] = true
			}
			if len(grants) > 0 && total > k {
				t.Fatalf("k=%d pre=%d: %d in flight after granting, cap %d", k, pre, total, k)
			}
			for ns, n := range perNS {
				if n > kNS && len(grants) > 0 {
					for _, g := range grants {
						if g.Holder.Namespace == ns {
							t.Fatalf("k=%d pre=%d: namespace %s at %d over share %d after a grant to it", k, pre, ns, n, kNS)
						}
					}
				}
			}
		}
	}
}

// eligible is consulted only for candidates that would otherwise be
// granted, so its (live-read) cost is bounded by the free slots, not the
// queue length.
func TestPlanGrantsChecksEligibilityOnlyWhenItMatters(t *testing.T) {
	var queue []Candidate
	for i := 0; i < 50; i++ {
		queue = append(queue, waiting("a", fmt.Sprintf("s%02d", i), i))
	}
	calls := 0
	grants := PlanGrants(queue, nil, 2, 2, func(Candidate) bool { calls++; return true })
	if len(grants) != 2 || calls != 2 {
		t.Fatalf("grants = %d, eligibility checks = %d; want 2 and 2 for 2 free slots", len(grants), calls)
	}
}
