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

package ensure

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
)

const credentialRequeue = 5 * time.Second

type credentialsStep struct{ Dependencies }

func newCredentialsStep(deps Dependencies) Step { return &credentialsStep{Dependencies: deps} }

func (*credentialsStep) Name() string { return "credentials" }

// ensureCredentials resolves the durable credential/TLS material: the
// tenant admin-credentials Secret, and the two operator-namespace private
// Secrets (internal DB credentials, TLS). Material is generated at most once
// per Secret — re-resolving on later passes only reads what already exists,
// preserving the reuse-on-reentry invariant (a regenerated password/CA would
// diverge from an already-booted VM). Creation stops this pass so the next pass
// re-observes the persisted material before ensureVM is allowed to proceed.
func (r *credentialsStep) Run(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	result, err := r.credentialsResolver().Resolve(ctx, inst)
	if err != nil {
		inst.SetCurrentCondition(dbaasv1.ConditionCredentialsReady, metav1.ConditionFalse,
			dbaasv1.ReasonCredentialsResolveFailed, err.Error())
		return Transient(err)
	}

	opNS := r.operatorNamespace()
	inst.Status.Resources.AdminCredentialsSecretName = credentials.TenantCredentialsSecretName(inst)
	inst.Status.Resources.InternalSecretRef = fmt.Sprintf("%s/%s", opNS, credentials.InternalSecretName(inst))
	inst.Status.Resources.PrivateTLSSecretRef = fmt.Sprintf("%s/%s", opNS, credentials.TLSSecretName(inst))
	if result.Changed {
		msg := "credential material created; waiting for observation"
		inst.SetCurrentCondition(dbaasv1.ConditionCredentialsReady, metav1.ConditionTrue,
			dbaasv1.ReasonCredentialsCreated, msg)
		// PendingAfter() is used here since cross-namespace owner references is not allowed
		return PendingAfter(dbaasv1.ReasonCredentialsCreated, msg, credentialRequeue)
	}

	inst.SetCurrentCondition(dbaasv1.ConditionCredentialsReady, metav1.ConditionTrue,
		dbaasv1.ReasonCredentialsProvisioned, "admin credentials and private material observed")
	return Satisfied()
}
