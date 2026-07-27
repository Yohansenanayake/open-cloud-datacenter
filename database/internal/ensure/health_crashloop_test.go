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
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

func TestCrashLoopHaltsAtThreshold(t *testing.T) {
	stub := &stubHarvester{
		Readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"},
	}
	r, inst := newCaughtUpFixture(stub)
	ctx := context.Background()

	for i := 1; i <= crashLoopThreshold; i++ {
		stub.Readiness.VMIUID = fmt.Sprintf("vmi-uid-crash-%d", i)
		res := r.ensureDatabaseHealth(ctx, inst)

		if i < crashLoopThreshold {
			if res.Outcome != OutcomeSatisfied {
				t.Fatalf("cycle %d: res = %+v, want Satisfied (absorbed)", i, res)
			}
			if inst.Status.RecentUnplannedRestarts != i {
				t.Fatalf("cycle %d: RecentUnplannedRestarts = %d, want %d", i, inst.Status.RecentUnplannedRestarts, i)
			}
			continue
		}

		if res.Outcome != OutcomePending {
			t.Fatalf("threshold cycle: res = %+v, want Pending", res)
		}
		if res.ControllerResult.RequeueAfter != crashLoopParkRequeue {
			t.Fatalf("RequeueAfter = %v, want %v", res.ControllerResult.RequeueAfter, crashLoopParkRequeue)
		}
	}

	if stub.StopVMForCrashLoopCalls != 1 {
		t.Fatalf("StopVMForCrashLoop called %d times, want 1", stub.StopVMForCrashLoopCalls)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("unexpected VM calls: stop=%d start=%d", stub.StopVMCalls, stub.StartVMCalls)
	}
	if stub.LastHaltedVMIUID != "vmi-uid-crash-3" {
		t.Fatalf("halted VMI UID = %q, want vmi-uid-crash-3", stub.LastHaltedVMIUID)
	}
	halted := inst.Status.GetCondition(dbaasv1.ConditionCrashLoopHalted)
	if halted == nil || halted.Status != metav1.ConditionTrue ||
		halted.Reason != string(dbaasv1.ReasonCrashLoopDetected) {
		t.Fatalf("CrashLoopHalted = %+v, want True/CrashLoopDetected", halted)
	}
}

func TestCrashLoopChainResetsAfterQuietGap(t *testing.T) {
	stub := &stubHarvester{
		Readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-new"},
	}
	r, inst := newCaughtUpFixture(stub)
	stale := metav1.NewTime(time.Now().Add(-crashLoopWindow - time.Minute))
	inst.Status.RecentUnplannedRestarts = crashLoopThreshold - 1
	inst.Status.LastUnplannedRestartTime = &stale

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if inst.Status.RecentUnplannedRestarts != 1 {
		t.Fatalf("RecentUnplannedRestarts = %d, want 1", inst.Status.RecentUnplannedRestarts)
	}
	if stub.StopVMCalls != 0 {
		t.Fatalf("StopVM called %d times, want 0", stub.StopVMCalls)
	}
}
