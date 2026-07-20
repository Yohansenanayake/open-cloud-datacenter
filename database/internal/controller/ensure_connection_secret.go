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

	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

// ensureConnectionSecret publishes the endpoint-dependent tenant connection
// contract after database health has established a reachable address. It is a
// guaranteed bounded step: a create/update stops this pass and API failures
// retry through controller-runtime backoff. The durable user-facing state is
// status.resources.connectionSecretName and the Secret itself; the one-pass
// create/update transition deliberately does not add another readiness condition.
func (r *DBInstanceReconciler) ensureConnectionSecret(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	if inst.Status.Endpoint == nil || inst.Status.Endpoint.Address == "" {
		msg := "waiting for database endpoint before connection secret setup"
		return pendingAfter(dbaasv1.ReasonWaitingForEndpoint, msg, credentialRequeue)
	}

	resolved, err := r.credentialsResolver().Resolve(ctx, inst)
	if err != nil {
		return transient(err)
	}
	if resolved.Changed {
		msg := "credential material changed while reconciling the connection secret; waiting for observation"
		return pendingAfter(dbaasv1.ReasonCredentialsCreated, msg, credentialRequeue)
	}

	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}
	op, err := resource.Apply(ctx, r.Client, r.Scheme(), inst, resource.ConnectionSecret{
		Instance:  inst,
		Address:   inst.Status.Endpoint.Address,
		Port:      inst.Status.Endpoint.Port,
		DBName:    dbName,
		CACertPEM: resolved.Material.TLS.CACertPEM,
	})
	if err != nil {
		return transient(err)
	}
	inst.Status.Resources.ConnectionSecretName = resource.ConnectionSecretName(inst)
	if op != controllerutil.OperationResultNone {
		msg := "tenant connection secret reconciled; waiting for observation"
		return pendingAfter(dbaasv1.ReasonConnectionSecretReconciled, msg, credentialRequeue)
	}
	return satisfied()
}
