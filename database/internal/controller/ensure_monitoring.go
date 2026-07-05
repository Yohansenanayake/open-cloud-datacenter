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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ensureMonitoring reconciles the per-instance metrics Service/Endpoints/
// ServiceMonitor via the idempotent DeployMonitoring provider call, pinned to the
// current endpoint IP. A deploy failure is non-fatal (the database works without
// monitoring): it is reported via MonitoringReady=False and the step still
// returns Satisfied so provisioning completes; the next pass retries. PR7 replaces
// the provider call with ResourceBuilders + owner refs.
func (r *DBInstanceReconciler) ensureMonitoring(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	// Ordered after ensureDatabaseHealth, so an endpoint is normally present;
	// defensive wait if not.
	if inst.Status.Endpoint == nil || inst.Status.Endpoint.Address == "" {
		msg := "waiting for database endpoint before monitoring setup"
		setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionFalse, "WaitingForEndpoint", msg)
		return pendingAfter("WaitingForEndpoint", msg, healthRequeue)
	}

	svcName, smName, grafanaURL, promTarget, err := r.Harvester.DeployMonitoring(ctx, inst.Name, inst.Namespace, inst.Status.Endpoint.Address)
	if err != nil {
		// Track the Service name regardless: DeployMonitoring creates the Service
		// first, so a partial failure may leave it behind for the finalizer.
		log.FromContext(ctx).Error(err, "monitoring setup failed (non-fatal)")
		inst.Status.Resources.MetricsServiceName = svcName
		setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionFalse,
			"MonitoringDeployFailed", err.Error())
		return satisfied()
	}

	inst.Status.Resources.MetricsServiceName = svcName
	inst.Status.Resources.ServiceMonitor = smName
	inst.Status.GrafanaURL = grafanaURL
	inst.Status.PrometheusTarget = promTarget
	setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionTrue,
		"MonitoringDeployed", "metrics Service and ServiceMonitor reconciled")
	return satisfied()
}
