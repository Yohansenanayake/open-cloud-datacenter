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

package guestconsole

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLeaseExcludesConcurrentHolderAndReleaseAllowsHandoff(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	manager := NewLeaseManager(client.CoordinationV1())
	manager.Now = func() time.Time { return now }

	first, err := manager.Acquire(ctx, "tenant-a", "orders-uid", "holder-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(ctx, "tenant-a", "orders-uid", "holder-2"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second acquire error = %v, want ErrBusy", err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(ctx, "tenant-a", "orders-uid", "holder-2"); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestLeaseCanBeTakenOverAfterExpiry(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	manager := NewLeaseManager(client.CoordinationV1())
	manager.Now = func() time.Time { return now }

	if _, err := manager.Acquire(ctx, "tenant-a", "orders-uid", "old-holder"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(LeaseDuration + time.Second)
	if _, err := manager.Acquire(ctx, "tenant-a", "orders-uid", "new-holder"); err != nil {
		t.Fatalf("expired lease takeover: %v", err)
	}
	lease, err := client.CoordinationV1().Leases("tenant-a").Get(ctx, LeaseName("orders-uid"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := value(lease.Spec.HolderIdentity); got != "new-holder" {
		t.Fatalf("holder = %q, want new-holder", got)
	}
	if lease.Spec.LeaseTransitions == nil || *lease.Spec.LeaseTransitions != 1 {
		t.Fatalf("lease transitions = %v, want 1", lease.Spec.LeaseTransitions)
	}
}

func TestLeaseRenewalExtendsRenewTime(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	manager := NewLeaseManager(client.CoordinationV1())
	manager.Now = func() time.Time { return now }
	guard, err := manager.Acquire(ctx, "tenant-a", "orders-uid", "holder-1")
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(RenewInterval)
	if err := guard.renew(ctx); err != nil {
		t.Fatal(err)
	}
	lease, err := client.CoordinationV1().Leases("tenant-a").Get(ctx, LeaseName("orders-uid"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Spec.RenewTime == nil || !lease.Spec.RenewTime.Time.Equal(now) {
		t.Fatalf("renew time = %v, want %s", lease.Spec.RenewTime, now)
	}
}

func TestLeaseNameIsDeterministicAndBounded(t *testing.T) {
	name := LeaseName("orders-uid")
	if name != LeaseName("orders-uid") || name == LeaseName("another-uid") {
		t.Fatalf("lease name is not deterministically UID-scoped: %q", name)
	}
	if len(name) > 63 {
		t.Fatalf("lease name length = %d", len(name))
	}
}
