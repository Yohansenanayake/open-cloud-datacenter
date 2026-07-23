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
	"testing"

	"k8s.io/client-go/tools/record"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/testutil"
)

type stubHarvester = testutil.StubHarvester

type testHarness struct{ Dependencies }

func newProvisionInst() *dbaasv1.DBInstance { return testutil.NewProvisionInstance() }

func newProvisionReconciler(t testing.TB, stub *stubHarvester, objects ...client.Object) *testHarness {
	t.Helper()
	return &testHarness{Dependencies: Dependencies{
		Client:            testutil.NewClient(t, objects...),
		Harvester:         stub,
		Recorder:          record.NewFakeRecorder(100),
		GrafanaBaseURL:    "https://grafana.example",
		OperatorNamespace: "dbaas-system",
	}}
}

func testVM(name, namespace string) *kubevirtv1.VirtualMachine {
	return testutil.VM(name, namespace)
}

func (r *testHarness) ensureCredentials(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newCredentialsStep(r.Dependencies).Run(ctx, inst)
}

func (r *testHarness) ensureConnectionSecret(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newConnectionSecretStep(r.Dependencies).Run(ctx, inst)
}

func (r *testHarness) ensureVM(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newVMStep(r.Dependencies).Run(ctx, inst)
}

func (r *testHarness) ensureDatabaseHealth(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newHealthStep(r.Dependencies).Run(ctx, inst)
}

func (r *testHarness) ensureBootstrapCleanup(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newBootstrapCleanupStep(r.Dependencies).Run(ctx, inst)
}

func (r *testHarness) markGenerationReconciled(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newGenerationStep().Run(ctx, inst)
}

func newHealthHarness(stub *stubHarvester) *testHarness {
	return &testHarness{Dependencies: Dependencies{
		Harvester: stub,
		Recorder:  record.NewFakeRecorder(100),
	}}
}
