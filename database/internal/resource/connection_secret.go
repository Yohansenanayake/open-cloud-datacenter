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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ConnectionSecretName returns the deterministic tenant-facing connection
// Secret name.
func ConnectionSecretName(inst *dbaasv1.DBInstance) string {
	return fmt.Sprintf("pg-%s-connect", inst.Name)
}

// ConnectionSecret is the stable, password-free connection contract for
// tenant applications: host/port/dbname/jdbcUrl/sslmode/ca.crt. Reconciled
// every pass so the address tracks a restart/migration IP change.
type ConnectionSecret struct {
	Instance  *dbaasv1.DBInstance
	Address   string
	Port      int
	DBName    string
	CACertPEM string
}

func (b ConnectionSecret) Build() (client.Object, error) {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      ConnectionSecretName(b.Instance),
		Namespace: b.Instance.Namespace,
	}}, nil
}

func (b ConnectionSecret) Update(obj client.Object) error {
	sec, ok := obj.(*corev1.Secret)
	if !ok {
		return wrongTypeErr("ConnectionSecret", obj)
	}
	sec.Labels = map[string]string{dbaasv1.LabelInstance: b.Instance.Name}
	sec.Type = corev1.SecretTypeOpaque
	sec.Data = nil // StringData is authoritative; clear any stale keys from a prior shape
	sec.StringData = map[string]string{
		"host":    b.Address,
		"port":    fmt.Sprintf("%d", b.Port),
		"dbname":  b.DBName,
		"jdbcUrl": fmt.Sprintf("jdbc:postgresql://%s:%d/%s?ssl=true&sslmode=verify-ca", b.Address, b.Port, b.DBName),
		"sslmode": "verify-ca",
		"ca.crt":  b.CACertPEM,
	}
	return nil
}
