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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	dbaasv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// The x-kubernetes-validations transition rules (PR9) are enforced by the API
// server itself, so — unlike the rest of this package's fake-client-backed
// unit tests — these specs run against the real envtest API server
// (k8sClient from suite_test.go); a fake client never evaluates CEL.
var _ = Describe("DBInstance immutable-field CEL rules", func() {
	ctx := context.Background()
	var name string
	counter := 0

	BeforeEach(func() {
		counter++
		name = fmt.Sprintf("cel-immutable-%d", counter)
	})

	createInstance := func() *dbaasv1alpha1.DBInstance {
		inst := &dbaasv1alpha1.DBInstance{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: dbaasv1alpha1.DBInstanceSpec{
				DBInstanceClass:  "db.t3.small",
				AllocatedStorage: 20,
				NetworkRef:       "default/vm-network",
				EngineVersion:    "16",
				VMPassword:       "initial",
				StaticNetwork: &dbaasv1alpha1.NetworkConfig{
					Address:     "192.168.40.50/24",
					Gateway:     "192.168.40.1",
					Nameservers: []string{"1.1.1.1"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, inst)
		})
		return inst
	}

	It("rejects changing networkRef after creation", func() {
		inst := createInstance()
		inst.Spec.NetworkRef = "default/other-network"
		err := k8sClient.Update(ctx, inst)
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("networkRef is immutable after creation"))
	})

	It("rejects changing engineVersion after creation", func() {
		inst := createInstance()
		inst.Spec.EngineVersion = "17"
		err := k8sClient.Update(ctx, inst)
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("engineVersion is immutable after creation"))
	})

	It("rejects changing vmPassword after creation", func() {
		inst := createInstance()
		inst.Spec.VMPassword = "changed"
		err := k8sClient.Update(ctx, inst)
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("vmPassword is immutable after creation"))
	})

	It("rejects changing staticNetwork after creation", func() {
		inst := createInstance()
		inst.Spec.StaticNetwork.Address = "192.168.40.99/24"
		err := k8sClient.Update(ctx, inst)
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("staticNetwork is immutable after creation"))
	})

	bareInstance := func() *dbaasv1alpha1.DBInstance {
		inst := &dbaasv1alpha1.DBInstance{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: dbaasv1alpha1.DBInstanceSpec{
				DBInstanceClass:  "db.t3.small",
				AllocatedStorage: 20,
				NetworkRef:       "default/vm-network",
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, inst)
		})
		return inst
	}

	// The CEL rules use plain "self == oldSelf": Kubernetes only evaluates a
	// transition rule once the field already has a value on the old object,
	// so filling in a still-unset optional immutable field is a permitted
	// one-time transition — only a set→different-value edit is rejected
	// (covered by the sibling "rejects changing ..." cases above).
	It("allows setting engineVersion once, after creation, when initially unset", func() {
		inst := bareInstance()
		inst.Spec.EngineVersion = "16"
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())

		var got dbaasv1alpha1.DBInstance
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &got)).To(Succeed())
		Expect(got.Spec.EngineVersion).To(Equal("16"))
	})

	It("allows setting vmPassword once, after creation, when initially unset", func() {
		inst := bareInstance()
		inst.Spec.VMPassword = "first-set"
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())

		var got dbaasv1alpha1.DBInstance
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &got)).To(Succeed())
		Expect(got.Spec.VMPassword).To(Equal("first-set"))
	})

	It("allows setting staticNetwork once, after creation, when initially unset", func() {
		inst := bareInstance()
		inst.Spec.StaticNetwork = &dbaasv1alpha1.NetworkConfig{
			Address:     "192.168.40.50/24",
			Gateway:     "192.168.40.1",
			Nameservers: []string{"1.1.1.1"},
		}
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())

		var got dbaasv1alpha1.DBInstance
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &got)).To(Succeed())
		Expect(got.Spec.StaticNetwork).NotTo(BeNil())
		Expect(got.Spec.StaticNetwork.Address).To(Equal("192.168.40.50/24"))
	})

	It("allows a mutable field to change while the immutable fields stay the same", func() {
		inst := createInstance()
		inst.Spec.AllocatedStorage = 30
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())

		var got dbaasv1alpha1.DBInstance
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &got)).To(Succeed())
		Expect(got.Spec.AllocatedStorage).To(Equal(30))
	})
})
