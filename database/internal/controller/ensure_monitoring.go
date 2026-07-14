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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

// ensureMonitoring reconciles the per-instance monitoring trio — selectorless
// metrics Service, manual Endpoints pinned to the current data-net IP, and
// ServiceMonitor — as builder-managed, controller-owned children. Owner refs
// make the Owns(Service/ServiceMonitor) watches live: out-of-band deletion or
// drift is repaired on the next pass, and GC backs up the finalizer teardown.
//
// A deploy failure is non-fatal (the database works without monitoring): it is
// reported via MonitoringReady=False and the step still returns Satisfied so
// provisioning completes; the next pass retries. Created/updated results also
// return Satisfied rather than stopping the pass — applying these three is
// cheap and idempotent, unlike the slow VM create, which is the one step
// worth stopping a pass for.
func (r *DBInstanceReconciler) ensureMonitoring(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	// Desired stopped: nothing to scrape; don't deploy against a dead endpoint.
	if !wantRunning(inst) {
		setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionFalse,
			dbaasv1.ReasonInstanceStopped, "instance is stopped")
		return satisfied()
	}

	// Ordered after ensureDatabaseHealth, so an endpoint is normally present;
	// defensive wait if not.
	if inst.Status.Endpoint == nil || inst.Status.Endpoint.Address == "" {
		msg := "waiting for database endpoint before monitoring setup"
		setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionFalse, dbaasv1.ReasonWaitingForEndpoint, msg)
		return pendingAfter(dbaasv1.ReasonWaitingForEndpoint, msg, healthRequeue)
	}

	builders := []resource.Builder{
		resource.MetricsService{Instance: inst},
		resource.MetricsEndpoints{Instance: inst, VMIP: inst.Status.Endpoint.Address},
		resource.ServiceMonitor{Instance: inst},
	}
	for _, b := range builders {
		if _, err := resource.Apply(ctx, r.Client, r.Scheme(), inst, b); err != nil {
			log.FromContext(ctx).Error(err, "monitoring reconcile failed (non-fatal)")
			setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionFalse,
				dbaasv1.ReasonMonitoringDeployFailed, err.Error())
			return satisfied()
		}
	}

	svcName := resource.MetricsServiceName(inst)
	inst.Status.Resources.MetricsServiceName = svcName
	inst.Status.Resources.ServiceMonitor = resource.ServiceMonitorName(inst)
	inst.Status.GrafanaURL = fmt.Sprintf("%s/d/dbaas-%s/postgresql-%s", r.GrafanaBaseURL, inst.Name, inst.Name)
	inst.Status.PrometheusTarget = fmt.Sprintf("%s.%s.svc:9187", svcName, inst.Namespace)
	setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionTrue,
		dbaasv1.ReasonMonitoringDeployed, "metrics Service, Endpoints, and ServiceMonitor reconciled")
	return satisfied()
}
