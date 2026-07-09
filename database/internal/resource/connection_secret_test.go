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

func TestConnectionSecretAppliesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	owner := testOwner()
	scheme := testScheme(t)
	c := ctrlfake.NewClientBuilder().WithScheme(scheme).Build()
	b := ConnectionSecret{Instance: owner, Address: "192.168.40.50", Port: 5432, DBName: "orders", CACertPEM: "CA-PEM"}

	op, err := Apply(ctx, c, scheme, owner, b)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if op != controllerutil.OperationResultCreated {
		t.Fatalf("op = %q, want created", op)
	}

	var got corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: ConnectionSecretName(owner)}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	assertControllerRef(t, &got)
	if got.StringData["host"] != "192.168.40.50" || got.StringData["port"] != "5432" || got.StringData["dbname"] != "orders" {
		t.Fatalf("StringData = %+v", got.StringData)
	}
	if got.StringData["jdbcUrl"] != "jdbc:postgresql://192.168.40.50:5432/orders?ssl=true&sslmode=verify-ca" {
		t.Fatalf("jdbcUrl = %q", got.StringData["jdbcUrl"])
	}
	if got.StringData["sslmode"] != "verify-ca" || got.StringData["ca.crt"] != "CA-PEM" {
		t.Fatalf("StringData = %+v", got.StringData)
	}

	// No password material, ever.
	for _, key := range []string{"password", "admin_password", "ca.key", "tls.key"} {
		if _, ok := got.StringData[key]; ok {
			t.Fatalf("connection secret must not contain %q", key)
		}
	}

	op, err = Apply(ctx, c, scheme, owner, b)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if op != controllerutil.OperationResultNone {
		t.Fatalf("second apply op = %q, want none (idempotent)", op)
	}
}

// A restart/migration changes the VM's data-net IP; the connection Secret is
// reconciled every pass, so it must retarget.
func TestConnectionSecretRetargetsOnAddressChange(t *testing.T) {
	ctx := context.Background()
	owner := testOwner()
	scheme := testScheme(t)
	c := ctrlfake.NewClientBuilder().WithScheme(scheme).Build()

	if _, err := Apply(ctx, c, scheme, owner, ConnectionSecret{Instance: owner, Address: "192.168.40.50", Port: 5432, DBName: "orders", CACertPEM: "CA-PEM"}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	op, err := Apply(ctx, c, scheme, owner, ConnectionSecret{Instance: owner, Address: "192.168.40.99", Port: 5432, DBName: "orders", CACertPEM: "CA-PEM"})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if op != controllerutil.OperationResultUpdated {
		t.Fatalf("op = %q, want updated on address change", op)
	}

	var got corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: ConnectionSecretName(owner)}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.StringData["host"] != "192.168.40.99" {
		t.Fatalf("host = %q, want retargeted to 192.168.40.99", got.StringData["host"])
	}
}
