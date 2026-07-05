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
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func newMonitoringInst() *dbaasv1.DBInstance {
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders"
	inst.Status.Endpoint = &dbaasv1.Endpoint{Address: "192.168.40.50", Port: defaultPort}
	return inst
}

func TestEnsureMonitoringDeploysAndRecordsRefs(t *testing.T) {
	stub := &stubHarvester{
		monSvcName:    "pg-orders-metrics",
		monSMName:     "pg-orders-monitor",
		monGrafanaURL: "https://grafana/d/pg-orders",
		monPromTarget: "192.168.40.50:9187",
	}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newMonitoringInst()

	res := r.ensureMonitoring(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if stub.DeployMonitoringCalls != 1 {
		t.Fatalf("DeployMonitoringCalls = %d, want 1", stub.DeployMonitoringCalls)
	}
	if inst.Status.Resources.MetricsServiceName != "pg-orders-metrics" ||
		inst.Status.Resources.ServiceMonitor != "pg-orders-monitor" {
		t.Fatalf("Resources = %+v, want monitoring refs recorded", inst.Status.Resources)
	}
	if inst.Status.GrafanaURL == "" || inst.Status.PrometheusTarget == "" {
		t.Fatalf("GrafanaURL/PrometheusTarget not recorded: %q / %q", inst.Status.GrafanaURL, inst.Status.PrometheusTarget)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionMonitoringReady) {
		t.Fatal("MonitoringReady should be True")
	}
}

// Monitoring failure must not block provisioning: report, then continue.
func TestEnsureMonitoringFailureIsNonFatal(t *testing.T) {
	stub := &stubHarvester{
		deployMonErr: errors.New("prometheus operator absent"),
		monSvcName:   "pg-orders-metrics", // Service is created before the failure
	}
	r := &DBInstanceReconciler{Harvester: stub}
	inst := newMonitoringInst()

	res := r.ensureMonitoring(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied (non-fatal)", res.Outcome)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionMonitoringReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "MonitoringDeployFailed" {
		t.Fatalf("MonitoringReady = %+v, want False/MonitoringDeployFailed", cond)
	}
	// Partial Service must still be tracked for the finalizer.
	if inst.Status.Resources.MetricsServiceName != "pg-orders-metrics" {
		t.Fatalf("MetricsServiceName = %q, want partial Service tracked", inst.Status.Resources.MetricsServiceName)
	}
}

func TestEnsureMonitoringWaitsForEndpoint(t *testing.T) {
	r := &DBInstanceReconciler{Harvester: &stubHarvester{}}
	inst := newProvisionInst() // no endpoint

	res := r.ensureMonitoring(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "WaitingForEndpoint" {
		t.Fatalf("res = %+v, want Pending/WaitingForEndpoint", res)
	}
}
