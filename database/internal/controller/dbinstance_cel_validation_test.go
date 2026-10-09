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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	dbaasv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// The x-kubernetes-validations transition rules are enforced by the API
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

// dbName/masterUsername identifier rules, enforced by the real API server.
var _ = Describe("DBInstance dbName and masterUsername identifier rules", func() {
	ctx := context.Background()
	counter := 0

	create := func(dbName, masterUsername string) error {
		counter++
		inst := &dbaasv1alpha1.DBInstance{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("cel-ident-%d", counter), Namespace: "default"},
			Spec: dbaasv1alpha1.DBInstanceSpec{
				DBInstanceClass:  "db.t3.small",
				AllocatedStorage: 20,
				NetworkRef:       "default/vm-network",
				DBName:           dbName,
				MasterUsername:   masterUsername,
			},
		}
		err := k8sClient.Create(ctx, inst)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, inst) })
		}
		return err
	}

	DescribeTable("accepts unquoted lowercase identifiers",
		func(dbName, masterUsername string) {
			Expect(create(dbName, masterUsername)).To(Succeed())
		},
		Entry("plain", "orders", "dbadmin"),
		Entry("underscores and digits", "orders_db_2", "app_owner_1"),
		Entry("leading underscore", "_orders", "_admin"),
	)

	DescribeTable("rejects names that need quoting or are reserved",
		func(dbName, masterUsername, wantMessage string) {
			err := create(dbName, masterUsername)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
			Expect(err.Error()).To(ContainSubstring(wantMessage))
		},
		Entry("dbName with a hyphen", "orders-db", "", "spec.dbName"),
		Entry("dbName with uppercase", "Orders", "", "spec.dbName"),
		Entry("dbName with $", "orders$", "", "spec.dbName"),
		Entry("dbName starting with a digit", "1orders", "", "spec.dbName"),
		Entry("dbName postgres", "postgres", "", "built-in PostgreSQL database"),
		Entry("dbName template1", "template1", "", "built-in PostgreSQL database"),
		Entry("masterUsername with a hyphen", "", "db-admin", "spec.masterUsername"),
		Entry("masterUsername with uppercase", "", "Admin", "spec.masterUsername"),
		Entry("masterUsername postgres", "", "postgres", "reserved role"),
		Entry("masterUsername postgres_exporter", "", "postgres_exporter", "reserved role"),
		Entry("masterUsername pg_ prefix", "", "pg_admin", "reserved role"),
	)

	// The default must always be writable back into spec.dbName — restore
	// copies a source's effective dbName into the target's spec.
	It("accepts DefaultDBName of any instance name as an explicit dbName", func() {
		for _, instanceName := range []string{
			"orders", "orders-db", "rt-src-1791115069", "a.b.c", "9lives", "postgres", "template0",
			"x123456789-123456789-123456789-123456789-123456789-123456789-123456789",
		} {
			Expect(create(dbaasv1alpha1.DefaultDBName(instanceName), "")).To(Succeed(), "instance name %q", instanceName)
		}
	})
})

var _ = Describe("DBInstance and restore-target name rule", func() {
	ctx := context.Background()

	instance := func(name string) *dbaasv1alpha1.DBInstance {
		return &dbaasv1alpha1.DBInstance{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: dbaasv1alpha1.DBInstanceSpec{
				DBInstanceClass: "db.t3.small", AllocatedStorage: 20, NetworkRef: "default/vm-network",
			},
		}
	}
	restore := func(name, target string) *dbaasv1alpha1.DBRestore {
		return &dbaasv1alpha1.DBRestore{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: dbaasv1alpha1.DBRestoreSpec{
				SnapshotRef:        corev1.LocalObjectReference{Name: "some-snapshot"},
				TargetInstanceName: target,
				DBInstanceClass:    "db.t3.small",
				NetworkRef:         "default/vm-network",
				AllocatedStorage:   20,
			},
		}
	}
	longest := "n" + strings.Repeat("x", dbaasv1alpha1.MaxInstanceNameLength-1)

	It("accepts a name of exactly the maximum length, and later updates to it", func() {
		inst := instance(longest)
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, inst) })

		// The rule is create-only: an update re-checks nothing about the name.
		inst.Spec.AllocatedStorage = 30
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())
	})

	DescribeTable("rejects names its child objects can't use",
		func(name string) {
			err := k8sClient.Create(ctx, instance(name))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
			Expect(err.Error()).To(ContainSubstring("pg-<name>-metrics"))
		},
		Entry("one character too long", longest+"x"),
		Entry("a dot (a valid object name, but not a Service name or hostname)", "orders.prod"),
	)

	It("holds a restore's target name to the same rule", func() {
		ok := restore("cel-name-target-ok", longest)
		Expect(k8sClient.Create(ctx, ok)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ok) })

		for i, target := range []string{longest + "x", "orders.prod", "Orders"} {
			err := k8sClient.Create(ctx, restore(fmt.Sprintf("cel-name-target-bad-%d", i), target))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "target %q: error %v", target, err)
			Expect(err.Error()).To(ContainSubstring("spec.targetInstanceName"))
		}
	})

	// Both would otherwise be accepted and fail late: an empty snapshot
	// name at lookup, an empty network only once the target is created.
	It("rejects a restore with an empty snapshot name or network", func() {
		noSnapshot := restore("cel-restore-no-snapshot", "orders-restored")
		noSnapshot.Spec.SnapshotRef.Name = ""
		err := k8sClient.Create(ctx, noSnapshot)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
		Expect(err.Error()).To(ContainSubstring("snapshotRef.name must not be empty"))

		noNetwork := restore("cel-restore-no-network", "orders-restored")
		noNetwork.Spec.NetworkRef = ""
		err = k8sClient.Create(ctx, noNetwork)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
		Expect(err.Error()).To(ContainSubstring("spec.networkRef"))
	})
})

// restoredFrom's field rule (self == oldSelf) only runs when both objects
// have the field, so its presence needs its own rule: adding or removing it
// would change the instance's disk names.
var _ = Describe("DBInstance restoredFrom presence rule", func() {
	ctx := context.Background()

	create := func(name string, from *dbaasv1alpha1.RestoredFromRef) *dbaasv1alpha1.DBInstance {
		inst := &dbaasv1alpha1.DBInstance{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: dbaasv1alpha1.DBInstanceSpec{
				DBInstanceClass: "db.t3.small", AllocatedStorage: 20, NetworkRef: "default/vm-network",
				RestoredFrom: from,
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, inst) })
		return inst
	}
	from := func() *dbaasv1alpha1.RestoredFromRef {
		return &dbaasv1alpha1.RestoredFromRef{DBRestoreName: "orders-restore", DBRestoreUID: "restore-uid"}
	}

	It("rejects adding restoredFrom to an instance created without it", func() {
		inst := create("cel-restoredfrom-add", nil)
		inst.Spec.RestoredFrom = from()
		err := k8sClient.Update(ctx, inst)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
		Expect(err.Error()).To(ContainSubstring("restoredFrom cannot be added or removed"))
	})

	It("rejects removing restoredFrom from a restored instance", func() {
		inst := create("cel-restoredfrom-remove", from())
		inst.Spec.RestoredFrom = nil
		err := k8sClient.Update(ctx, inst)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error: %v", err)
		Expect(err.Error()).To(ContainSubstring("restoredFrom cannot be added or removed"))
	})

	It("still allows other updates to a restored instance", func() {
		inst := create("cel-restoredfrom-keep", from())
		inst.Spec.AllocatedStorage = 30
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())
	})
})
