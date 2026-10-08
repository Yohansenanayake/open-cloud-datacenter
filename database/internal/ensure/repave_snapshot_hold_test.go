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

package ensure

import (
	"context"
	"testing"

	kubevirtv1 "kubevirt.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/backup"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// These tests verify that snapshots and repave exclude each other through
// the shared snapshot-hold Lease. Snapshot activity is simulated by directly
// acquiring and checking the Lease.

func TestEnsureRepaveWaitsForAnInProgressSnapshotHold(t *testing.T) {
	r, inst, stub := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	triggerRepave(inst, "now")

	acquired, err := (backup.Holds{Live: r.Client, Writer: r.Client}).Acquire(context.Background(), inst.Namespace, backup.SnapshotHoldName(inst.UID), "snapshot:orders-daily-20260918", ownerRefFor(inst), nil)
	if err != nil {
		t.Fatalf("simulate an in-progress snapshot: Acquire: %v", err)
	}
	if !acquired.Acquired {
		t.Fatalf("test setup: snapshot Acquire = %+v, want Acquired=true", acquired)
	}

	res := r.ensureRepave(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != dbaasv1.ReasonRepaveWaitingForSnapshotHold {
		t.Fatalf("res = %+v, want Pending/RepaveWaitingForSnapshotHold", res)
	}
	if !noProviderCalls(stub) {
		t.Fatal("repave must not touch the VM while a snapshot holds the lease")
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionRepaveInProgress)
	if cond == nil || cond.Reason != string(dbaasv1.ReasonRepaveWaitingForSnapshotHold) {
		t.Fatalf("RepaveInProgress = %+v, want reason RepaveWaitingForSnapshotHold", cond)
	}
}

func TestEnsureRepaveHoldingTheLeaseBlocksASnapshot(t *testing.T) {
	r, inst, _ := newRepaveFixture(t, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	triggerRepave(inst, "now")

	res := r.ensureRepave(context.Background(), inst)
	if res.Outcome != OutcomePending || res.Reason != dbaasv1.ReasonRepaveStopping {
		t.Fatalf("res = %+v, want Pending/RepaveStopping (repave should have acquired the lease and started stopping the VM)", res)
	}

	// A would-be snapshot creation tries to acquire the same lease next.
	snapshotAcquire, err := (backup.Holds{Live: r.Client, Writer: r.Client}).Acquire(context.Background(), inst.Namespace, backup.SnapshotHoldName(inst.UID), "snapshot:orders-daily-20260918", ownerRefFor(inst), nil)
	if err != nil {
		t.Fatalf("snapshot Acquire: %v", err)
	}
	if snapshotAcquire.Acquired {
		t.Fatal("a snapshot must not be able to acquire the lease while repave holds it")
	}
	if snapshotAcquire.HolderIdentity != repaveSnapshotHoldHolder {
		t.Fatalf("HolderIdentity = %q, want %q", snapshotAcquire.HolderIdentity, repaveSnapshotHoldHolder)
	}
}
