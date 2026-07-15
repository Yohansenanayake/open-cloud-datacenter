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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func TestSyncReadyConditionTrueWhenDatabaseReady(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue, dbaasv1.ReasonPostgresReady, "ready")

	r.syncReadyCondition(inst)

	if !inst.Status.IsConditionTrue(dbaasv1.ConditionReady) {
		t.Fatal("Ready should be True when DatabaseReady is True")
	}
}

func TestSyncReadyConditionFalseWhenStopped(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	stopped := false
	inst.Spec.Running = &stopped
	// Even a stale True DatabaseReady must not leak through: wantRunning wins.
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue, dbaasv1.ReasonPostgresReady, "ready")

	r.syncReadyCondition(inst)

	cond := inst.Status.GetCondition(dbaasv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonStopped) {
		t.Fatalf("Ready = %+v, want False/Stopped", cond)
	}
}

// Ready mirrors DatabaseReady's own reason/message rather than hardcoding one,
// since syncReadyCondition runs regardless of which step last touched
// DatabaseReady (health's degraded report, a crash-loop halt, a resize halt).
func TestSyncReadyConditionMirrorsDatabaseReadyReason(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse,
		dbaasv1.ReasonPostgresUnreachable, "probe failing")

	r.syncReadyCondition(inst)

	cond := inst.Status.GetCondition(dbaasv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonPostgresUnreachable) {
		t.Fatalf("Ready = %+v, want False/PostgresUnreachable", cond)
	}
}

func TestSyncReadyConditionFalseWhenDatabaseReadyNeverSet(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()

	r.syncReadyCondition(inst)

	cond := inst.Status.GetCondition(dbaasv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("Ready = %+v, want False", cond)
	}
}
