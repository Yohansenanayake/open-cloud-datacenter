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
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

func newMonitoringInst() *dbaasv1.DBInstance {
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders"
	inst.Status.Endpoint = &dbaasv1.Endpoint{Address: "192.168.40.50", Port: defaultPort}
	return inst
}

func TestEnsureMonitoringAppliesTrioWithOwnerRefs(t *testing.T) {
	inst := newMonitoringInst()
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	ctx := context.Background()

	res := r.ensureMonitoring(ctx, inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}

	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-metrics"}, &svc); err != nil {
		t.Fatalf("metrics Service missing: %v", err)
	}
	var ep corev1.Endpoints
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-metrics"}, &ep); err != nil {
		t.Fatalf("metrics Endpoints missing: %v", err)
	}
	if ep.Subsets[0].Addresses[0].IP != "192.168.40.50" {
		t.Fatalf("Endpoints IP = %q, want the instance endpoint", ep.Subsets[0].Addresses[0].IP)
	}
	var sm monitoringv1.ServiceMonitor
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-monitor"}, &sm); err != nil {
		t.Fatalf("ServiceMonitor missing: %v", err)
	}
	for _, obj := range []metav1.Object{&svc, &ep, &sm} {
		refs := obj.GetOwnerReferences()
		if len(refs) != 1 || refs[0].Kind != "DBInstance" || refs[0].Controller == nil || !*refs[0].Controller {
			t.Fatalf("%s owner refs = %+v, want controller-owned", obj.GetName(), refs)
		}
	}

	if inst.Status.Resources.MetricsServiceName != "pg-orders-metrics" ||
		inst.Status.Resources.ServiceMonitor != "pg-orders-monitor" {
		t.Fatalf("Resources = %+v, want monitoring refs recorded", inst.Status.Resources)
	}
	if inst.Status.GrafanaURL != "https://grafana.example/d/dbaas-orders/postgresql-orders" {
		t.Fatalf("GrafanaURL = %q", inst.Status.GrafanaURL)
	}
	if inst.Status.PrometheusTarget != "pg-orders-metrics.tenant-a.svc:9187" {
		t.Fatalf("PrometheusTarget = %q", inst.Status.PrometheusTarget)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionMonitoringReady) {
		t.Fatal("MonitoringReady should be True")
	}

	// Idempotent: a second pass converges with no error.
	if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("second pass = %+v, want Satisfied", res)
	}
}

// The Owns() payoff: an out-of-band deletion of a monitoring child is repaired
// on the next pass (the watch fires the reconcile; the builder re-creates).
func TestEnsureMonitoringRepairsOutOfBandDeletion(t *testing.T) {
	inst := newMonitoringInst()
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	ctx := context.Background()

	if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("initial pass = %+v", res)
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "pg-orders-metrics"}}
	if err := r.Delete(ctx, svc); err != nil {
		t.Fatalf("out-of-band delete: %v", err)
	}

	if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("repair pass = %+v", res)
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-metrics"}, &corev1.Service{}); err != nil {
		t.Fatalf("metrics Service not repaired after out-of-band deletion: %v", err)
	}
}

// Monitoring failure must not block provisioning: report, then continue.
// A scheme without the ServiceMonitor type makes the third Apply fail.
func TestEnsureMonitoringFailureIsNonFatal(t *testing.T) {
	inst := newMonitoringInst()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		dbaasv1.AddToScheme, kubevirtv1.AddToScheme, corev1.AddToScheme, // no monitoringv1
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("add scheme: %v", err)
		}
	}
	fakeClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&dbaasv1.DBInstance{}).
		WithObjects(inst).
		Build()
	r := &DBInstanceReconciler{Client: fakeClient, Harvester: &stubHarvester{}, GrafanaBaseURL: "https://grafana.example"}

	res := r.ensureMonitoring(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied (non-fatal)", res.Outcome)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionMonitoringReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "MonitoringDeployFailed" {
		t.Fatalf("MonitoringReady = %+v, want False/MonitoringDeployFailed", cond)
	}
}

func TestEnsureMonitoringWaitsForEndpoint(t *testing.T) {
	inst := newProvisionInst() // no endpoint
	r := newProvisionReconciler(t, &stubHarvester{}, inst)

	res := r.ensureMonitoring(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "WaitingForEndpoint" {
		t.Fatalf("res = %+v, want Pending/WaitingForEndpoint", res)
	}
}

func TestEnsureMonitoringSkipsWhenStopped(t *testing.T) {
	inst := newMonitoringInst()
	stopped := false
	inst.Spec.Running = &stopped
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	ctx := context.Background()

	res := r.ensureMonitoring(ctx, inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	// Nothing deployed against a stopped instance.
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: resource.MetricsServiceName(inst)}, &corev1.Service{}); err == nil {
		t.Fatal("metrics Service deployed for a stopped instance")
	}
}
