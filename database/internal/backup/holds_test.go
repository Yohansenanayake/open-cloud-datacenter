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
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newFakeClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return ctrlfake.NewClientBuilder().WithScheme(scheme).Build()
}

// testHolds uses one fake client for both roles — the fake client has no
// cache, so reads through it are already "live".
func testHolds(c client.Client) Holds { return Holds{Live: c, Writer: c} }

// A Holds without an uncached reader must refuse to operate rather than
// silently falling back to a (possibly cached) client.
func TestHoldsRefusesToOperateWithoutLiveReader(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)
	h := Holds{Writer: c}

	if _, err := h.Acquire(ctx, "tenant", "hold-a", "holder-1", testOwner(), nil); err == nil {
		t.Fatal("Acquire without a Live reader must error")
	}
	if err := h.Release(ctx, "tenant", "hold-a", "holder-1"); err == nil {
		t.Fatal("Release without a Live reader must error")
	}
	if _, err := h.AnyRestoreHoldForSource(ctx, "tenant", "src"); err == nil {
		t.Fatal("AnyRestoreHoldForSource without a Live reader must error")
	}
	var list coordinationv1.LeaseList
	if err := c.List(ctx, &list); err != nil || len(list.Items) != 0 {
		t.Fatalf("no Lease may be created (list=%v, err=%v)", list.Items, err)
	}
}

// The mutual-exclusion decision is made from Live, never from the Writer's
// own view: here the Writer (standing in for a stale cache) sees nothing,
// but the live API already has another holder's Lease — Acquire must report
// it as held, and must not create a second one.
func TestAcquireDecidesFromLiveReaderNotWriter(t *testing.T) {
	ctx := context.Background()
	live := newFakeClient(t)
	if _, err := testHolds(live).Acquire(ctx, "tenant", "hold-a", "holder-other", testOwner(), nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stale := newFakeClient(t)

	res, err := Holds{Live: live, Writer: stale}.Acquire(ctx, "tenant", "hold-a", "holder-1", testOwner(), nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if res.Acquired || res.HolderIdentity != "holder-other" {
		t.Fatalf("res = %+v, want held by holder-other", res)
	}
	var list coordinationv1.LeaseList
	if err := stale.List(ctx, &list); err != nil || len(list.Items) != 0 {
		t.Fatalf("Acquire must not create a Lease when the live view shows it held (created %d)", len(list.Items))
	}
}

// Release reads the Lease live and preconditions the delete on exactly that
// object — a Lease replaced in between (same name, new UID and
// resourceVersion) survives. The fake client only enforces the
// resourceVersion half of the precondition; the API server enforces both.
func TestReleaseNeverDeletesAReplacedLease(t *testing.T) {
	ctx := context.Background()
	holder := "holder-1"
	leaseWithUID := func(uid types.UID, rv string) *coordinationv1.Lease {
		return &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: "hold-a", Namespace: "tenant", UID: uid, ResourceVersion: rv},
			Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder},
		}
	}
	scheme := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	live := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(leaseWithUID("the-lease-we-read", "5")).Build()
	replaced := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(leaseWithUID("its-replacement", "7")).Build()

	if err := (Holds{Live: live, Writer: replaced}).Release(ctx, "tenant", "hold-a", holder); err == nil {
		t.Fatal("Release must fail rather than delete a Lease that isn't the one it read")
	}
	if _, held, _ := testHolds(replaced).Held(ctx, "tenant", "hold-a"); !held {
		t.Fatal("the replacement Lease must survive")
	}
}

// testOwner is a stand-in DBInstance owner reference — every real caller has
// one (see Acquire's doc comment on why owner is required), so tests that
// aren't specifically exercising that requirement use this rather than nil.
func testOwner() *metav1.OwnerReference {
	return &metav1.OwnerReference{
		APIVersion: "dbaas.opencloud.wso2.com/v1alpha1",
		Kind:       "DBInstance",
		Name:       "orders",
		UID:        types.UID("orders-uid"),
	}
}

func TestAcquireCreatesLeaseForFirstHolder(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)

	res, err := testHolds(c).Acquire(ctx, "tenant", "hold-a", "holder-1", testOwner(), nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !res.Acquired {
		t.Fatalf("Acquire = %+v, want Acquired=true", res)
	}

	holder, held, err := testHolds(c).Held(ctx, "tenant", "hold-a")
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if !held || holder != "holder-1" {
		t.Fatalf("Held = (%q, %v), want (\"holder-1\", true)", holder, held)
	}
}

func TestAcquireIsIdempotentForSameHolder(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)

	if _, err := testHolds(c).Acquire(ctx, "tenant", "hold-a", "holder-1", testOwner(), nil); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	res, err := testHolds(c).Acquire(ctx, "tenant", "hold-a", "holder-1", testOwner(), nil)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if !res.Acquired {
		t.Fatalf("re-Acquire by the same holder = %+v, want Acquired=true", res)
	}
}

func TestAcquireRejectsADifferentHolder(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)

	if _, err := testHolds(c).Acquire(ctx, "tenant", "hold-a", "holder-1", testOwner(), nil); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	res, err := testHolds(c).Acquire(ctx, "tenant", "hold-a", "holder-2", testOwner(), nil)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if res.Acquired {
		t.Fatalf("Acquire by a different holder = %+v, want Acquired=false", res)
	}
	if res.HolderIdentity != "holder-1" {
		t.Fatalf("HolderIdentity = %q, want %q", res.HolderIdentity, "holder-1")
	}
}

func TestReleaseByNonHolderIsANoOp(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)

	if _, err := testHolds(c).Acquire(ctx, "tenant", "hold-a", "holder-1", testOwner(), nil); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := testHolds(c).Release(ctx, "tenant", "hold-a", "holder-2"); err != nil {
		t.Fatalf("Release by non-holder: %v", err)
	}

	holder, held, err := testHolds(c).Held(ctx, "tenant", "hold-a")
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if !held || holder != "holder-1" {
		t.Fatalf("lease should still be held by holder-1 after a non-holder's Release; got (%q, %v)", holder, held)
	}
}

func TestReleaseByHolderDeletesTheLease(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)

	if _, err := testHolds(c).Acquire(ctx, "tenant", "hold-a", "holder-1", testOwner(), nil); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := testHolds(c).Release(ctx, "tenant", "hold-a", "holder-1"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	_, held, err := testHolds(c).Held(ctx, "tenant", "hold-a")
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if held {
		t.Fatal("lease should be gone after Release by its holder")
	}

	// Released, so a different holder can now acquire it.
	res, err := testHolds(c).Acquire(ctx, "tenant", "hold-a", "holder-2", testOwner(), nil)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if !res.Acquired {
		t.Fatalf("Acquire after release = %+v, want Acquired=true", res)
	}
}

func TestReleaseOfAnAbsentLeaseIsANoOp(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)

	if err := testHolds(c).Release(ctx, "tenant", "never-acquired", "holder-1"); err != nil {
		t.Fatalf("Release of an absent lease: %v", err)
	}
}

func TestHeldReportsAbsentLease(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)

	holder, held, err := testHolds(c).Held(ctx, "tenant", "hold-a")
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if held || holder != "" {
		t.Fatalf("Held = (%q, %v), want (\"\", false)", holder, held)
	}
}

func TestAcquireSetsTheOwnerReferenceOnCreate(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)
	owner := testOwner()

	if _, err := testHolds(c).Acquire(ctx, "tenant", "hold-a", "holder-1", owner, nil); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	var lease coordinationv1.Lease
	if err := c.Get(ctx, types.NamespacedName{Namespace: "tenant", Name: "hold-a"}, &lease); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(lease.OwnerReferences) != 1 || lease.OwnerReferences[0].UID != owner.UID {
		t.Fatalf("OwnerReferences = %+v, want exactly %+v", lease.OwnerReferences, *owner)
	}
}

// Every hold in this design has exactly one natural owner (the source
// instance for a snapshot hold, the DBRestore itself for a restore hold),
// so there is no legitimate case for an unowned hold — Acquire rejects a nil
// owner rather than silently creating one, since that owner reference is
// what stops a hold orphaned by a bug from becoming permanent garbage.
func TestAcquireRejectsANilOwner(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)

	if _, err := testHolds(c).Acquire(ctx, "tenant", "hold-a", "holder-1", nil, nil); err == nil {
		t.Fatal("Acquire with a nil owner should fail, not silently create an unowned lease")
	}

	_, held, err := testHolds(c).Held(ctx, "tenant", "hold-a")
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if held {
		t.Fatal("a rejected Acquire must not leave a lease behind")
	}
}

func TestSnapshotAndRestoreHoldNamesAreDistinctAndDeterministic(t *testing.T) {
	uid := types.UID("11111111-1111-1111-1111-111111111111")

	snap1 := SnapshotHoldName(uid)
	snap2 := SnapshotHoldName(uid)
	if snap1 != snap2 {
		t.Fatalf("SnapshotHoldName is not deterministic: %q != %q", snap1, snap2)
	}

	restore1 := RestoreHoldName(uid)
	if restore1 == snap1 {
		t.Fatalf("SnapshotHoldName and RestoreHoldName collided for the same UID: %q", snap1)
	}
}
