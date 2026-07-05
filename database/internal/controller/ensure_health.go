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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// healthRequeue is the timer fallback while waiting for the VM/PostgreSQL to come
// up. The VMI watch (PR1) usually re-triggers sooner; the timer covers windows
// with no VMI events (e.g. before the VMI object exists).
const healthRequeue = 10 * time.Second

// ensureDatabaseHealth is the provisioning readiness gate (ported from
// phaseWaitReady): two observed gates, both from GetVMIReadiness.
//
//	gate 1: VMI Running and the guest agent has reported a data-net IP;
//	gate 2: KubeVirt's VMI readiness probe (pg_isready inside the guest) passes.
//
// Both layers are runtime-only — this step writes nothing to the platform, so its
// Pending is always the timer flavor. When Ready, it publishes the Endpoint and
// returns Satisfied.
//
// PR6 expands this step with steady-state liveness: crash-loop tracking (first,
// unconditionally), report-only Degraded once caught up, and endpoint refresh.
func (r *DBInstanceReconciler) ensureDatabaseHealth(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	readiness, err := r.Harvester.GetVMIReadiness(ctx, inst.Namespace, inst.Status.Resources.VMName)
	if err != nil && !apierrors.IsNotFound(err) {
		return transient(err)
	}

	// Gate 1 — NotFound (VMI not created yet), not Running, or no data-net IP.
	if err != nil || !readiness.Running || readiness.IP == "" {
		msg := "VM booting; waiting for guest agent and data-net IP"
		setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, "VMBooting", msg)
		inst.Status.Message = msg
		return pendingAfter("VMBooting", msg, healthRequeue)
	}

	port := specPort(inst.Spec.Port)

	// Gate 2 — the in-guest readiness probe has not passed yet.
	if !readiness.Ready {
		msg := fmt.Sprintf("PostgreSQL initializing; readiness probe not passing at %s:%d", readiness.IP, port)
		setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, "PostgresInitializing", msg)
		inst.Status.Message = msg
		return pendingAfter("PostgresInitializing", msg, healthRequeue)
	}

	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}
	inst.Status.Endpoint = &dbaasv1.Endpoint{
		Address: readiness.IP,
		Port:    port,
		JDBCURL: fmt.Sprintf("jdbc:postgresql://%s:%d/%s?ssl=true&sslmode=verify-ca", readiness.IP, port, dbName),
	}
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue,
		"PostgresReady", "PostgreSQL is ready")
	return satisfied()
}
