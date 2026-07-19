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

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

func newMonitoringInst() *dbaasv1.DBInstance {
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders"
	inst.Status.Endpoint = &dbaasv1.Endpoint{Address: "192.168.40.50", Port: defaultPort}
	return inst
}

func convergeMonitoring(t *testing.T, ctx context.Context, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) {
	t.Helper()
	if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomePending {
		t.Fatalf("monitoring apply result = %+v, want Pending", res)
	}
	if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("monitoring observe result = %+v, want Satisfied", res)
	}
}

func TestEnsureMonitoringAppliesTrioWithOwnerRefs(t *testing.T) {
	inst := newMonitoringInst()
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	ctx := context.Background()

	res := r.ensureMonitoring(ctx, inst)

	if res.Outcome != OutcomePending || res.Result.RequeueAfter != monitoringRequeue {
		t.Fatalf("result = %+v, want timed Pending", res)
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

	// Only an unchanged observation is Satisfied.
	if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("second pass = %+v, want Satisfied", res)
	}
}

func TestEnsureMonitoringOmitsGrafanaURLWhenBaseUnset(t *testing.T) {
	inst := newMonitoringInst()
	inst.Status.GrafanaURL = "https://old.example/d/old"
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	r.GrafanaBaseURL = ""

	if res := r.ensureMonitoring(context.Background(), inst); res.Outcome != OutcomePending {
		t.Fatalf("result = %+v, want Pending after monitoring apply", res)
	}
	if inst.Status.GrafanaURL != "" {
		t.Fatalf("GrafanaURL = %q, want empty when base URL is unset", inst.Status.GrafanaURL)
	}
}

// The Owns() payoff: out-of-band deletion of any monitoring child is repaired
// on the next pass, and the mutation stops that pass.
func TestEnsureMonitoringRepairsOutOfBandDeletion(t *testing.T) {
	for _, tc := range []struct {
		name string
		obj  func() client.Object
	}{
		{"service", func() client.Object { return &corev1.Service{} }},
		{"endpoints", func() client.Object { return &corev1.Endpoints{} }},
		{"service-monitor", func() client.Object { return &monitoringv1.ServiceMonitor{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := newMonitoringInst()
			r := newProvisionReconciler(t, &stubHarvester{}, inst)
			ctx := context.Background()

			if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomePending {
				t.Fatalf("initial pass = %+v, want Pending", res)
			}
			if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomeSatisfied {
				t.Fatalf("observation pass = %+v, want Satisfied", res)
			}

			obj := tc.obj()
			obj.SetNamespace("tenant-a")
			if _, ok := obj.(*monitoringv1.ServiceMonitor); ok {
				obj.SetName(resource.ServiceMonitorName(inst))
			} else {
				obj.SetName(resource.MetricsServiceName(inst))
			}
			if err := r.Delete(ctx, obj); err != nil {
				t.Fatalf("out-of-band delete: %v", err)
			}

			if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomePending {
				t.Fatalf("repair pass = %+v, want Pending", res)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(obj), tc.obj()); err != nil {
				t.Fatalf("monitoring child not repaired: %v", err)
			}
		})
	}
}

// A scheme without the ServiceMonitor type makes the third Apply fail.
func TestEnsureMonitoringFailureIsTransient(t *testing.T) {
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

	if res.Outcome != OutcomeTransient || res.Err == nil {
		t.Fatalf("result = %+v, want Transient with error", res)
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

func TestEnsureMonitoringRetainsResourcesWhenStopped(t *testing.T) {
	inst := newMonitoringInst()
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	ctx := context.Background()

	if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomePending {
		t.Fatalf("initial pass = %+v, want Pending", res)
	}
	stopped := false
	inst.Spec.Running = &stopped
	if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("stopped pass = %+v, want Satisfied", res)
	}

	for _, obj := range []client.Object{&corev1.Service{}, &corev1.Endpoints{}, &monitoringv1.ServiceMonitor{}} {
		name := resource.MetricsServiceName(inst)
		if _, ok := obj.(*monitoringv1.ServiceMonitor); ok {
			name = resource.ServiceMonitorName(inst)
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: name}, obj); err != nil {
			t.Fatalf("retained monitoring child %T missing: %v", obj, err)
		}
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionMonitoringReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonInstanceStopped) {
		t.Fatalf("MonitoringReady = %+v, want False/InstanceStopped", cond)
	}
}

func TestEnsureMonitoringRetargetsEndpointsWithBoundedOutcome(t *testing.T) {
	inst := newMonitoringInst()
	r := newProvisionReconciler(t, &stubHarvester{}, inst)
	ctx := context.Background()

	if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomePending {
		t.Fatalf("initial pass = %+v, want Pending", res)
	}
	inst.Status.Endpoint.Address = "192.168.40.99"
	if res := r.ensureMonitoring(ctx, inst); res.Outcome != OutcomePending {
		t.Fatalf("retarget pass = %+v, want Pending", res)
	}

	var endpoints corev1.Endpoints
	if err := r.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: resource.MetricsServiceName(inst)}, &endpoints); err != nil {
		t.Fatalf("get Endpoints: %v", err)
	}
	if got := endpoints.Subsets[0].Addresses[0].IP; got != "192.168.40.99" {
		t.Fatalf("Endpoints IP = %q, want 192.168.40.99", got)
	}
}

func TestReconcileInstanceMonitoringFailureRetriesWithoutCompletingGeneration(t *testing.T) {
	ctx := context.Background()
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true, Ready: true, AgentConnected: true,
		IP: "192.168.40.50", VMIUID: "vmi-uid-abc",
	}}
	r, _ := newLifecycleFixture(t, true, stub)
	inst := getInst(t, r.Client)

	serviceMonitor := &monitoringv1.ServiceMonitor{ObjectMeta: metav1.ObjectMeta{
		Namespace: inst.Namespace,
		Name:      resource.ServiceMonitorName(inst),
	}}
	if err := r.Delete(ctx, serviceMonitor); err != nil {
		t.Fatalf("delete ServiceMonitor: %v", err)
	}

	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture's fake client does not implement client.WithWatch")
	}
	boom := errors.New("service monitor API unavailable")
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*monitoringv1.ServiceMonitor); ok {
				return boom
			}
			return c.Create(ctx, obj, opts...)
		},
	})

	if _, err := r.reconcileInstance(ctx, inst); !errors.Is(err, boom) {
		t.Fatalf("reconcileInstance error = %v, want monitoring API error", err)
	}
	got := getInst(t, r.Client)
	if got.Status.ObservedGeneration != 1 {
		t.Fatalf("ObservedGeneration = %d, want prior converged generation 1", got.Status.ObservedGeneration)
	}
	if !got.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady should remain True when only monitoring failed")
	}
	if got.Status.IsConditionTrue(dbaasv1.ConditionReady) {
		t.Fatal("aggregate Ready should be False when guaranteed monitoring failed")
	}
	monitoringReady := got.Status.GetCondition(dbaasv1.ConditionMonitoringReady)
	if monitoringReady == nil || monitoringReady.Status != metav1.ConditionFalse ||
		monitoringReady.Reason != string(dbaasv1.ReasonMonitoringDeployFailed) {
		t.Fatalf("MonitoringReady = %+v, want False/MonitoringDeployFailed", monitoringReady)
	}
	if got.Status.Phase != dbaasv1.StatusDegraded {
		t.Fatalf("Phase = %q, want %q", got.Status.Phase, dbaasv1.StatusDegraded)
	}
}
