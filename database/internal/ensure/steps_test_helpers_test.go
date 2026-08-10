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
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/catalog"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/testutil"
)

type stubHarvester = testutil.StubHarvester

var (
	defaultStorageType = operatorconfig.Default().DatabaseDefaults.StorageClass
	defaultMasterUser  = operatorconfig.Default().DatabaseDefaults.MasterUsername
	defaultPort        = operatorconfig.Default().DatabaseDefaults.Port
)

var (
	defaultOSVersion = operatorconfig.Default().DatabaseDefaults.OSVersion
	// defaultBakedImageName is the catalog-resolved image name every test in
	// this package sees for defaultOSVersion, once init() below overrides the
	// real (Pending) seed entry for that stream. This override only affects
	// this test binary — internal/catalog's own tests and every other
	// package run as separate binaries and never see it.
	defaultBakedImageName = "test-" + defaultOSVersion + "-postgres"
)

func init() {
	catalog.BakedImages[defaultBakedImageName] = catalog.BakedImageEntry{
		ImageName:               defaultBakedImageName,
		OSVersion:               defaultOSVersion,
		SupportedEngineVersions: []string{"16", "17"},
		DefaultEngineVersion:    "17",
	}
	catalog.LatestBakedImages[defaultOSVersion] = catalog.BakedImageStream{
		Revision:        defaultBakedImageName,
		ValidationState: catalog.ValidationValidated,
	}
}

type testHarness struct{ Dependencies }

func newProvisionInst() *dbaasv1.DBInstance { return testutil.NewProvisionInstance() }

func newTestHarness(t testing.TB, stub *stubHarvester, objects ...client.Object) *testHarness {
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

func shapedVM(name, namespace, class string, storageGB int, dataVolumeName string, strategy kubevirtv1.VirtualMachineRunStrategy) *kubevirtv1.VirtualMachine {
	return testutil.ShapedVM(name, namespace, class, storageGB, dataVolumeName, strategy)
}

func (r *testHarness) ensurePreflight(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newPreflightStep(r.Dependencies).Run(ctx, inst)
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

func (r *testHarness) ensureResize(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newResizeStep(r.Dependencies).Run(ctx, inst)
}

func (r *testHarness) ensureRepave(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newRepaveStep(r.Dependencies).Run(ctx, inst)
}

func (r *testHarness) ensurePowerState(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newPowerStep(r.Dependencies).Run(ctx, inst)
}

func (r *testHarness) ensureDatabaseHealth(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newHealthStep(r.Dependencies).Run(ctx, inst)
}

func (r *testHarness) ensureBootstrapCleanup(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newBootstrapCleanupStep(r.Dependencies).Run(ctx, inst)
}

func (r *testHarness) ensureMonitoring(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	return newMonitoringStep(r.Dependencies).Run(ctx, inst)
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
