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

package resource

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestCloudInitSecretAppliesWithOwnerRef(t *testing.T) {
	ctx := context.Background()
	owner := testOwner()
	scheme := testScheme(t)
	c := ctrlfake.NewClientBuilder().WithScheme(scheme).Build()

	op, err := Apply(ctx, c, scheme, owner, CloudInitSecret{Instance: owner, UserData: "USERDATA", NetworkData: "NETWORKDATA"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if op != controllerutil.OperationResultCreated {
		t.Fatalf("op = %q, want created", op)
	}

	var got corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: CloudInitSecretName(owner)}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	assertControllerRef(t, &got)
	if got.StringData["userdata"] != "USERDATA" || got.StringData["networkdata"] != "NETWORKDATA" {
		t.Fatalf("StringData = %+v", got.StringData)
	}
}

// ensureVM re-applies with freshly rendered content on every create-VM pass
// (e.g. after a partial failure); the Secret must always match latest content.
func TestCloudInitSecretOverwritesOnReapply(t *testing.T) {
	ctx := context.Background()
	owner := testOwner()
	scheme := testScheme(t)
	c := ctrlfake.NewClientBuilder().WithScheme(scheme).Build()

	if _, err := Apply(ctx, c, scheme, owner, CloudInitSecret{Instance: owner, UserData: "OLD", NetworkData: "OLD-NET"}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	op, err := Apply(ctx, c, scheme, owner, CloudInitSecret{Instance: owner, UserData: "NEW", NetworkData: "NEW-NET"})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if op != controllerutil.OperationResultUpdated {
		t.Fatalf("op = %q, want updated", op)
	}

	var got corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: CloudInitSecretName(owner)}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.StringData["userdata"] != "NEW" || got.StringData["networkdata"] != "NEW-NET" {
		t.Fatalf("StringData = %+v, want overwritten with new content", got.StringData)
	}
}
