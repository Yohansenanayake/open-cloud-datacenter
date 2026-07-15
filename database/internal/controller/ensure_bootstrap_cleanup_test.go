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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
)

const cleanupCloudInitSecretName = "pg-orders-cloudinit"

// originalCloudInitUserData/NetworkData seed the fixture Secret with content
// clearly distinguishable from redactedCloudInitUserData, so tests can
// assert "redacted" vs "untouched" by content — ensureBootstrapCleanup no
// longer deletes the Secret (a live VMI's mounted volume can't be
// un-mounted, so deleting it just makes kubelet's periodic Secret-volume
// resync fail with FailedMount for the rest of that pod's life).
const originalCloudInitUserData = "ORIGINAL-USERDATA-WITH-SECRETS"
const originalCloudInitNetworkData = "ORIGINAL-NETWORKDATA"

func newCleanupFixture(t *testing.T, stub *stubHarvester, dbReady bool) (*DBInstanceReconciler, *dbaasv1.DBInstance) {
	t.Helper()
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders"
	inst.Status.Resources.CloudInitSecretName = cleanupCloudInitSecretName
	status := metav1.ConditionFalse
	if dbReady {
		status = metav1.ConditionTrue
	}
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, status, "test", "test")

	ciSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cleanupCloudInitSecretName, Namespace: inst.Namespace},
		Data: map[string][]byte{
			"userdata":    []byte(originalCloudInitUserData),
			"networkdata": []byte(originalCloudInitNetworkData),
		},
	}
	r := newProvisionReconciler(t, stub, inst, ciSecret)
	return r, inst
}

func getCloudInitSecret(t *testing.T, r *DBInstanceReconciler, ns string) corev1.Secret {
	t.Helper()
	var got corev1.Secret
	key := types.NamespacedName{Namespace: ns, Name: cleanupCloudInitSecretName}
	if err := r.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get cloud-init secret: %v", err)
	}
	return got
}

func TestEnsureBootstrapCleanupRedactsOnceDBReady(t *testing.T) {
	ctx := context.Background()
	stub := &stubHarvester{}
	r, inst := newCleanupFixture(t, stub, true)

	res := r.ensureBootstrapCleanup(ctx, inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	// The Secret is never deleted, so the ref stays recorded (TeardownAll
	// still needs it when the whole instance is deleted).
	if inst.Status.Resources.CloudInitSecretName != cleanupCloudInitSecretName {
		t.Fatalf("CloudInitSecretName = %q, want kept (%q)", inst.Status.Resources.CloudInitSecretName, cleanupCloudInitSecretName)
	}
	got := getCloudInitSecret(t, r, inst.Namespace)
	if string(got.Data["userdata"]) != redactedCloudInitUserData {
		t.Fatalf("userdata = %q, want redacted", got.Data["userdata"])
	}
	wantNetworkData := credentials.BuildNetworkData(credentials.BootstrapParams{StaticNetwork: inst.Spec.StaticNetwork})
	if string(got.Data["networkdata"]) != wantNetworkData {
		t.Fatalf("networkdata = %q, want freshly-rendered %q, not left as the stale original", got.Data["networkdata"], wantNetworkData)
	}
}

// Cloud-init is only provably consumed once the in-guest probe has passed:
// redacting earlier could interfere with a still-in-progress first boot.
func TestEnsureBootstrapCleanupDefersUntilDBReady(t *testing.T) {
	stub := &stubHarvester{}
	r, inst := newCleanupFixture(t, stub, false)

	res := r.ensureBootstrapCleanup(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied (deferred)", res)
	}
	got := getCloudInitSecret(t, r, inst.Namespace)
	if string(got.Data["userdata"]) != originalCloudInitUserData {
		t.Fatalf("userdata = %q, want untouched original", got.Data["userdata"])
	}
}

func TestEnsureBootstrapCleanupSatisfiedWhenNothingToScrub(t *testing.T) {
	stub := &stubHarvester{}
	r, inst := newCleanupFixture(t, stub, true)
	inst.Status.Resources.CloudInitSecretName = ""

	if res := r.ensureBootstrapCleanup(context.Background(), inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	got := getCloudInitSecret(t, r, inst.Namespace)
	if string(got.Data["userdata"]) != originalCloudInitUserData {
		t.Fatalf("userdata = %q, want untouched (no ref recorded, so nothing to act on)", got.Data["userdata"])
	}
}

// A failed Apply must leave the Secret exactly as it was, for the next
// pass's retry.
func TestEnsureBootstrapCleanupApplyFailureIsTransientAndLeavesSecretUntouched(t *testing.T) {
	ctx := context.Background()
	stub := &stubHarvester{}
	r, inst := newCleanupFixture(t, stub, true)

	watchClient, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture's fake client does not implement client.WithWatch")
	}
	boom := errors.New("apiserver unavailable")
	r.Client = interceptor.NewClient(watchClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if sec, ok := obj.(*corev1.Secret); ok && sec.Name == cleanupCloudInitSecretName {
				return boom
			}
			return c.Update(ctx, obj, opts...)
		},
	})

	res := r.ensureBootstrapCleanup(ctx, inst)

	if res.Outcome != OutcomeTransient || res.Err == nil {
		t.Fatalf("res = %+v, want Transient with error", res)
	}
	got := getCloudInitSecret(t, r, inst.Namespace)
	if string(got.Data["userdata"]) != originalCloudInitUserData {
		t.Fatalf("userdata = %q, want untouched after a failed apply", got.Data["userdata"])
	}
}
