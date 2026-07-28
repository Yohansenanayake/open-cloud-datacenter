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
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/ensure"
	statuspatch "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/patch"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/testutil"
)

type stubHarvester = testutil.StubHarvester

const (
	defaultPort               = 5432
	credentialRequeue         = 5 * time.Second
	monitoringRequeue         = 5 * time.Second
	crashLoopParkRequeue      = 30 * time.Second
	crashLoopThreshold        = 3
	redactedCloudInitUserData = "#cloud-config\n{}\n"
)

func testEnsureDependencies(r *DBInstanceReconciler) ensure.Dependencies {
	operatorNamespace := r.OperatorNamespace
	if operatorNamespace == "" {
		operatorNamespace = "dbaas-system"
	}
	return ensure.Dependencies{
		Client:            r.Client,
		Harvester:         r.Harvester,
		Recorder:          r.Recorder,
		GrafanaBaseURL:    r.GrafanaBaseURL,
		OperatorNamespace: operatorNamespace,
	}
}

func resetEnsureRunner(r *DBInstanceReconciler) {
	r.EnsureRunner = ensure.NewDefaultRunner(testEnsureDependencies(r))
}

// runReconcileInstance executes the production instance body and its top-level
// deferred final status patch for tests that exercise the body directly.
func runReconcileInstance(ctx context.Context, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	patcher := statuspatch.NewSerialPatcher(inst, r.Client)
	result, reconcileErr := r.reconcileInstance(ctx, inst)
	r.finalizeStatus(inst)
	patchErr := patcher.Patch(ctx, inst, dbInstancePatchOptions()...)
	return result, errors.Join(reconcileErr, patchErr)
}

func runEnsureStep(ctx context.Context, r *DBInstanceReconciler, inst *dbaasv1.DBInstance, name string) ensure.Result {
	for _, step := range ensure.NewDefaultSteps(testEnsureDependencies(r)) {
		if step.Name() == name {
			return step.Run(ctx, inst)
		}
	}
	return ensure.Transient(fmt.Errorf("unknown test ensure step %q", name))
}

func convergeCredentials(t interface {
	Helper()
	Fatalf(string, ...any)
}, ctx context.Context, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) {
	t.Helper()
	if result := runEnsureStep(ctx, r, inst, "credentials"); result.Outcome != ensure.OutcomePending {
		t.Fatalf("credential create result = %+v, want Pending", result)
	}
	if result := runEnsureStep(ctx, r, inst, "credentials"); result.Outcome != ensure.OutcomeSatisfied {
		t.Fatalf("credential observe result = %+v, want Satisfied", result)
	}
}

func convergeConnectionSecret(t interface {
	Helper()
	Fatalf(string, ...any)
}, ctx context.Context, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) {
	t.Helper()
	if result := runEnsureStep(ctx, r, inst, "connection-secret"); result.Outcome != ensure.OutcomePending {
		t.Fatalf("connection secret apply result = %+v, want Pending", result)
	}
	if result := runEnsureStep(ctx, r, inst, "connection-secret"); result.Outcome != ensure.OutcomeSatisfied {
		t.Fatalf("connection secret observe result = %+v, want Satisfied", result)
	}
}

func convergeMonitoring(t interface {
	Helper()
	Fatalf(string, ...any)
}, ctx context.Context, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) {
	t.Helper()
	if result := runEnsureStep(ctx, r, inst, "monitoring"); result.Outcome != ensure.OutcomePending {
		t.Fatalf("monitoring apply result = %+v, want Pending", result)
	}
	if result := runEnsureStep(ctx, r, inst, "monitoring"); result.Outcome != ensure.OutcomeSatisfied {
		t.Fatalf("monitoring observe result = %+v, want Satisfied", result)
	}
}
