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
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

// credentialsResolver builds the Resolver for this pass. Cheap: no external
// deps beyond the manager client and the configured operator namespace.
func (r *DBInstanceReconciler) credentialsResolver() *credentials.Resolver {
	return &credentials.Resolver{
		Client:            r.Client,
		Scheme:            r.Scheme(),
		OperatorNamespace: r.operatorNamespace(),
	}
}

// ensureCredentials resolves the durable credential/TLS material (PR8): the
// tenant admin-credentials Secret, and the two operator-namespace private
// Secrets (internal DB credentials, TLS). Material is generated at most once
// per Secret — re-resolving on later passes only reads what already exists,
// preserving the reuse-on-reentry invariant (a regenerated password/CA would
// diverge from an already-booted VM). Once the database endpoint is known, it
// also publishes the tenant connection Secret.
func (r *DBInstanceReconciler) ensureCredentials(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	material, err := r.credentialsResolver().Resolve(ctx, inst)
	if err != nil {
		setStepCond(inst, dbaasv1.ConditionCredentialsReady, metav1.ConditionFalse,
			"CredentialsResolveFailed", err.Error())
		return transient(err)
	}

	opNS := r.operatorNamespace()
	inst.Status.Resources.SecretName = credentials.TenantCredentialsSecretName(inst)
	inst.Status.Resources.InternalSecretRef = fmt.Sprintf("%s/%s", opNS, credentials.InternalSecretName(inst))
	inst.Status.Resources.PrivateTLSSecretRef = fmt.Sprintf("%s/%s", opNS, credentials.TLSSecretName(inst))
	inst.Status.MasterUserSecret = &dbaasv1.MasterUserSecretRef{
		Name:   inst.Status.Resources.SecretName,
		Status: dbaasv1.SecretStatusActive,
	}
	setStepCond(inst, dbaasv1.ConditionCredentialsReady, metav1.ConditionTrue,
		"CredentialsProvisioned", "admin credentials and private material resolved")

	// The connection Secret needs a reachable endpoint; ensureDatabaseHealth
	// (later in the step order) hasn't run yet on a fresh instance, so this
	// converges on the pass after health first publishes one — and refreshes
	// on every later IP change since this step re-applies on every pass.
	if inst.Status.Endpoint == nil || inst.Status.Endpoint.Address == "" {
		return satisfied()
	}

	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}
	if _, err := resource.Apply(ctx, r.Client, r.Scheme(), inst, resource.ConnectionSecret{
		Instance:  inst,
		Address:   inst.Status.Endpoint.Address,
		Port:      inst.Status.Endpoint.Port,
		DBName:    dbName,
		CACertPEM: material.TLS.CACertPEM,
	}); err != nil {
		// Non-fatal, like monitoring: the connection Secret is a convenience,
		// not required for the database to function. Retry next pass.
		log.FromContext(ctx).Error(err, "connection secret reconcile failed (non-fatal)")
		return satisfied()
	}
	inst.Status.Resources.ConnectionSecretName = resource.ConnectionSecretName(inst)

	return satisfied()
}
