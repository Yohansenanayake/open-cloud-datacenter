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
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
	dbresource "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

const (
	originalCloudInitUserData    = "ORIGINAL-USERDATA-WITH-SECRETS"
	originalCloudInitNetworkData = "ORIGINAL-NETWORKDATA"
)

var bootstrapCleanupSequence atomic.Int64

var _ = Describe("Bootstrap cleanup against the API server", func() {
	ctx := context.Background()

	newFixture := func(createSecret bool) (*DBInstanceReconciler, *dbaasv1.DBInstance, types.NamespacedName) {
		name := fmt.Sprintf("bootstrap-cleanup-%d", bootstrapCleanupSequence.Add(1))
		inst := &dbaasv1.DBInstance{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: dbaasv1.DBInstanceSpec{
				DBInstanceClass:  "db.t3.small",
				AllocatedStorage: 20,
				NetworkRef:       "default/vm-network",
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, inst) })

		secretName := dbresource.CloudInitSecretName(inst)
		inst.Status.Resources.CloudInitSecretName = secretName
		setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue,
			dbaasv1.ReasonPostgresReady, "PostgreSQL is ready")
		key := types.NamespacedName{Namespace: inst.Namespace, Name: secretName}
		DeferCleanup(func() {
			err := k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}})
			Expect(client.IgnoreNotFound(err)).To(Succeed())
		})

		if createSecret {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
				Data: map[string][]byte{
					"userdata":    []byte(originalCloudInitUserData),
					"networkdata": []byte(originalCloudInitNetworkData),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		}

		return &DBInstanceReconciler{Client: k8sClient}, inst, key
	}

	It("stops after redaction and is satisfied on unchanged re-observation", func() {
		r, inst, key := newFixture(true)

		res := r.ensureBootstrapCleanup(ctx, inst)
		Expect(res.Outcome).To(Equal(OutcomePending))
		Expect(res.Reason).To(Equal(dbaasv1.ReasonBootstrapCleanupReconciled))
		Expect(res.Result.RequeueAfter).To(BeZero())
		Expect(inst.Status.Resources.CloudInitSecretName).To(Equal(key.Name))

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
		Expect(string(secret.Data["userdata"])).To(Equal(redactedCloudInitUserData))
		wantNetworkData := credentials.BuildNetworkData(credentials.BootstrapParams{StaticNetwork: inst.Spec.StaticNetwork})
		Expect(string(secret.Data["networkdata"])).To(Equal(wantNetworkData))
		resourceVersion := secret.ResourceVersion

		res = r.ensureBootstrapCleanup(ctx, inst)
		Expect(res.Outcome).To(Equal(OutcomeSatisfied))
		Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
		Expect(secret.ResourceVersion).To(Equal(resourceVersion))
	})

	It("recreates a missing referenced Secret and converges on re-observation", func() {
		r, inst, key := newFixture(false)

		res := r.ensureBootstrapCleanup(ctx, inst)
		Expect(res.Outcome).To(Equal(OutcomePending))
		Expect(res.Reason).To(Equal(dbaasv1.ReasonBootstrapCleanupReconciled))

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
		Expect(string(secret.Data["userdata"])).To(Equal(redactedCloudInitUserData))
		Expect(res.Result.RequeueAfter).To(BeZero())

		res = r.ensureBootstrapCleanup(ctx, inst)
		Expect(res.Outcome).To(Equal(OutcomeSatisfied))
	})

	It("returns Transient when the API update fails", func() {
		r, inst, key := newFixture(true)
		boom := errors.New("apiserver unavailable")
		r.Client = interceptor.NewClient(k8sClient, interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if secret, ok := obj.(*corev1.Secret); ok && secret.Name == key.Name {
					return boom
				}
				return c.Update(ctx, obj, opts...)
			},
		})

		res := r.ensureBootstrapCleanup(ctx, inst)
		Expect(res.Outcome).To(Equal(OutcomeTransient))
		Expect(res.Err).To(MatchError(boom))

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
		Expect(string(secret.Data["userdata"])).To(Equal(originalCloudInitUserData))
	})

	It("does not report convergence when a missing Secret cannot be created", func() {
		r, inst, key := newFixture(false)
		boom := errors.New("apiserver unavailable")
		r.Client = interceptor.NewClient(k8sClient, interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if secret, ok := obj.(*corev1.Secret); ok && secret.Name == key.Name {
					return boom
				}
				return c.Create(ctx, obj, opts...)
			},
		})

		res := r.ensureBootstrapCleanup(ctx, inst)
		Expect(res.Outcome).To(Equal(OutcomeTransient))
		Expect(res.Err).To(MatchError(boom))
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Secret{}))).To(BeTrue())
	})
})
