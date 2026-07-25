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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

func TestReconcileInstanceMonitoringFailureRetriesWithoutCompletingGeneration(t *testing.T) {
	ctx := context.Background()
	stub := &stubHarvester{Readiness: harvester.VMIReadiness{
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
	resetEnsureRunner(r)

	if _, err := runReconcileInstance(ctx, r, inst); !errors.Is(err, boom) {
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
