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

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/ensure"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/testutil"
)

type stubHarvester = testutil.StubHarvester
type StepOutcome = ensure.Outcome

const (
	OutcomeSatisfied = ensure.OutcomeSatisfied
	OutcomePending   = ensure.OutcomePending
	OutcomeTerminal  = ensure.OutcomeTerminal
	OutcomeTransient = ensure.OutcomeTransient

	defaultOSImage            = "ubuntu-22.04-server-cloudimg-amd64.img"
	defaultStorageType        = "longhorn"
	defaultMasterUser         = "dbadmin"
	defaultPort               = 5432
	credentialRequeue         = 5 * time.Second
	powerRequeue              = 5 * time.Second
	healthRequeue             = 10 * time.Second
	monitoringRequeue         = 5 * time.Second
	preflightRequeue          = 10 * time.Second
	crashLoopParkRequeue      = 30 * time.Second
	crashLoopWindow           = 10 * time.Minute
	crashLoopThreshold        = 3
	redactedCloudInitUserData = "#cloud-config\n{}\n"
)

func (r *DBInstanceReconciler) testEnsureDependencies() ensure.Dependencies {
	return ensure.Dependencies{
		Client:            r.Client,
		Harvester:         r.Harvester,
		Recorder:          r.Recorder,
		GrafanaBaseURL:    r.GrafanaBaseURL,
		OperatorNamespace: r.operatorNamespace(),
	}
}

func (r *DBInstanceReconciler) resetEnsureRunner() {
	r.EnsureRunner = ensure.NewDefaultRunner(r.testEnsureDependencies())
}

func (r *DBInstanceReconciler) runTestStep(ctx context.Context, inst *dbaasv1.DBInstance, name string) ensure.Result {
	for _, step := range ensure.NewDefaultSteps(r.testEnsureDependencies()) {
		if step.Name() == name {
			return step.Run(ctx, inst)
		}
	}
	return ensure.Transient(fmt.Errorf("unknown test ensure step %q", name))
}

func (r *DBInstanceReconciler) ensurePreflight(ctx context.Context, inst *dbaasv1.DBInstance) ensure.Result {
	return r.runTestStep(ctx, inst, "preflight")
}

func (r *DBInstanceReconciler) ensureCredentials(ctx context.Context, inst *dbaasv1.DBInstance) ensure.Result {
	return r.runTestStep(ctx, inst, "credentials")
}

func (r *DBInstanceReconciler) ensureVM(ctx context.Context, inst *dbaasv1.DBInstance) ensure.Result {
	return r.runTestStep(ctx, inst, "vm")
}

func (r *DBInstanceReconciler) ensureResize(ctx context.Context, inst *dbaasv1.DBInstance) ensure.Result {
	return r.runTestStep(ctx, inst, "resize")
}

func (r *DBInstanceReconciler) ensurePowerState(ctx context.Context, inst *dbaasv1.DBInstance) ensure.Result {
	return r.runTestStep(ctx, inst, "power")
}

func (r *DBInstanceReconciler) ensureDatabaseHealth(ctx context.Context, inst *dbaasv1.DBInstance) ensure.Result {
	return r.runTestStep(ctx, inst, "health")
}

func (r *DBInstanceReconciler) ensureConnectionSecret(ctx context.Context, inst *dbaasv1.DBInstance) ensure.Result {
	return r.runTestStep(ctx, inst, "connection-secret")
}

func (r *DBInstanceReconciler) ensureMonitoring(ctx context.Context, inst *dbaasv1.DBInstance) ensure.Result {
	return r.runTestStep(ctx, inst, "monitoring")
}

func (r *DBInstanceReconciler) ensureBootstrapCleanup(ctx context.Context, inst *dbaasv1.DBInstance) ensure.Result {
	return r.runTestStep(ctx, inst, "bootstrap-cleanup")
}

func (r *DBInstanceReconciler) markGenerationReconciled(ctx context.Context, inst *dbaasv1.DBInstance) ensure.Result {
	return r.runTestStep(ctx, inst, "generation-reconciled")
}

func convergeCredentials(t interface {
	Helper()
	Fatalf(string, ...any)
}, ctx context.Context, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) {
	t.Helper()
	if result := r.ensureCredentials(ctx, inst); result.Outcome != OutcomePending {
		t.Fatalf("credential create result = %+v, want Pending", result)
	}
	if result := r.ensureCredentials(ctx, inst); result.Outcome != OutcomeSatisfied {
		t.Fatalf("credential observe result = %+v, want Satisfied", result)
	}
}

func convergeConnectionSecret(t interface {
	Helper()
	Fatalf(string, ...any)
}, ctx context.Context, r *DBInstanceReconciler, inst *dbaasv1.DBInstance) {
	t.Helper()
	if result := r.ensureConnectionSecret(ctx, inst); result.Outcome != OutcomePending {
		t.Fatalf("connection secret apply result = %+v, want Pending", result)
	}
	if result := r.ensureConnectionSecret(ctx, inst); result.Outcome != OutcomeSatisfied {
		t.Fatalf("connection secret observe result = %+v, want Satisfied", result)
	}
}
