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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

const monitoringRequeue = 5 * time.Second

// ensureMonitoring reconciles the per-instance monitoring trio — selectorless
// metrics Service, manual Endpoints pinned to the current data-net IP, and
// ServiceMonitor — as builder-managed, controller-owned children. Owner refs
// make the Owns(Service/Endpoints/ServiceMonitor) watches live: deletion or
// drift is repaired on the next pass, and GC backs up the finalizer teardown.
//
// Monitoring is a guaranteed product artifact. A create/update stops this pass
// so the next pass re-observes the persisted trio; API failures retry through
// controller-runtime backoff. Stopping an instance makes the scrape target
// inactive but deliberately retains the monitoring children until deletion.
func (r *DBInstanceReconciler) ensureMonitoring(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	// Desired stopped: retain any existing monitoring children, but do not deploy
	// or retarget them against an inactive endpoint.
	if !wantRunning(inst) {
		setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionFalse,
			dbaasv1.ReasonInstanceStopped, "monitoring target is inactive while the database instance is stopped")
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
	changed := false
	for _, b := range builders {
		op, err := resource.Apply(ctx, r.Client, r.Scheme(), inst, b)
		if err != nil {
			log.FromContext(ctx).Error(err, "monitoring reconcile failed")
			setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionFalse,
				dbaasv1.ReasonMonitoringDeployFailed, err.Error())
			return transient(err)
		}
		if op != controllerutil.OperationResultNone {
			changed = true
		}
	}

	svcName := resource.MetricsServiceName(inst)
	inst.Status.Resources.MetricsServiceName = svcName
	inst.Status.Resources.ServiceMonitor = resource.ServiceMonitorName(inst)
	inst.Status.GrafanaURL = fmt.Sprintf("%s/d/dbaas-%s/postgresql-%s", r.GrafanaBaseURL, inst.Name, inst.Name)
	inst.Status.PrometheusTarget = fmt.Sprintf("%s.%s.svc:9187", svcName, inst.Namespace)
	msg := "metrics Service, Endpoints, and ServiceMonitor observed"
	if changed {
		msg = "metrics Service, Endpoints, and ServiceMonitor reconciled; waiting for observation"
	}
	setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionTrue, dbaasv1.ReasonMonitoringDeployed, msg)
	if changed {
		return pendingAfter(dbaasv1.ReasonMonitoringDeployed, msg, monitoringRequeue)
	}
	return satisfied()
}
