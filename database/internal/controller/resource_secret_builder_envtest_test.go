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
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	dbresource "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

var secretBuilderSequence atomic.Int64

// These resource-package specs intentionally share the controller envtest API
// server rather than using controller-runtime's fake
// client. Secret.stringData is write-only API input: the API server merges it
// into data and omits it from reads, a behavior the fake client does not model.
var _ = Describe("Secret builders against the API server", func() {
	ctx := context.Background()

	newOwner := func() *dbaasv1.DBInstance {
		name := fmt.Sprintf("secret-builder-%d", secretBuilderSequence.Add(1))
		owner := &dbaasv1.DBInstance{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: dbaasv1.DBInstanceSpec{
				DBInstanceClass:  "db.t3.small",
				AllocatedStorage: 20,
				NetworkRef:       "default/vm-network",
			},
		}
		Expect(k8sClient.Create(ctx, owner)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, owner) })
		return owner
	}

	It("persists and re-observes a connection Secret without perpetual updates", func() {
		owner := newOwner()
		builder := dbresource.ConnectionSecret{
			Instance: owner, Address: "192.168.40.50", Port: 5432,
			DBName: "orders", CACertPEM: "CA-PEM",
		}

		op, err := dbresource.Apply(ctx, k8sClient, scheme.Scheme, owner, builder)
		Expect(err).NotTo(HaveOccurred())
		Expect(op).To(Equal(controllerutil.OperationResultCreated))

		key := types.NamespacedName{Namespace: owner.Namespace, Name: dbresource.ConnectionSecretName(owner)}
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, secret) })
		Expect(secret.StringData).To(BeEmpty())
		Expect(string(secret.Data["host"])).To(Equal("192.168.40.50"))
		Expect(string(secret.Data["port"])).To(Equal("5432"))
		Expect(string(secret.Data["dbname"])).To(Equal("orders"))
		Expect(string(secret.Data["jdbcUrl"])).To(Equal("jdbc:postgresql://192.168.40.50:5432/orders?ssl=true&sslmode=verify-ca"))
		Expect(string(secret.Data["sslmode"])).To(Equal("verify-ca"))
		Expect(string(secret.Data["ca.crt"])).To(Equal("CA-PEM"))
		for _, key := range []string{"password", "admin_password", "ca.key", "tls.key"} {
			Expect(secret.Data).NotTo(HaveKey(key))
		}
		Expect(secret.OwnerReferences).To(HaveLen(1))
		Expect(secret.OwnerReferences[0].UID).To(Equal(owner.UID))
		Expect(secret.OwnerReferences[0].Controller).NotTo(BeNil())
		Expect(*secret.OwnerReferences[0].Controller).To(BeTrue())

		op, err = dbresource.Apply(ctx, k8sClient, scheme.Scheme, owner, builder)
		Expect(err).NotTo(HaveOccurred())
		Expect(op).To(Equal(controllerutil.OperationResultNone))

		builder.Address = "192.168.40.99"
		op, err = dbresource.Apply(ctx, k8sClient, scheme.Scheme, owner, builder)
		Expect(err).NotTo(HaveOccurred())
		Expect(op).To(Equal(controllerutil.OperationResultUpdated))
		Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
		Expect(string(secret.Data["host"])).To(Equal("192.168.40.99"))
	})

	It("persists and re-observes a cloud-init Secret without perpetual updates", func() {
		owner := newOwner()
		builder := dbresource.CloudInitSecret{
			Instance: owner, UserData: "USERDATA", NetworkData: "NETWORKDATA",
		}

		op, err := dbresource.Apply(ctx, k8sClient, scheme.Scheme, owner, builder)
		Expect(err).NotTo(HaveOccurred())
		Expect(op).To(Equal(controllerutil.OperationResultCreated))

		key := types.NamespacedName{Namespace: owner.Namespace, Name: dbresource.CloudInitSecretName(owner)}
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, secret) })
		Expect(secret.StringData).To(BeEmpty())
		Expect(string(secret.Data["userdata"])).To(Equal("USERDATA"))
		Expect(string(secret.Data["networkdata"])).To(Equal("NETWORKDATA"))

		op, err = dbresource.Apply(ctx, k8sClient, scheme.Scheme, owner, builder)
		Expect(err).NotTo(HaveOccurred())
		Expect(op).To(Equal(controllerutil.OperationResultNone))

		builder.UserData = "NEW"
		builder.NetworkData = "NEW-NET"
		op, err = dbresource.Apply(ctx, k8sClient, scheme.Scheme, owner, builder)
		Expect(err).NotTo(HaveOccurred())
		Expect(op).To(Equal(controllerutil.OperationResultUpdated))
		Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
		Expect(string(secret.Data["userdata"])).To(Equal("NEW"))
		Expect(string(secret.Data["networkdata"])).To(Equal("NEW-NET"))
	})
})
