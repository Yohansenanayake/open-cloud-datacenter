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

// CloudInitSecretName returns the deterministic ephemeral cloud-init Secret
// name KubeVirt's cloudInitNoCloud datasource reads userdata/networkdata from.
func CloudInitSecretName(inst *dbaasv1.DBInstance) string {
	return fmt.Sprintf("pg-%s-cloudinit", inst.Name)
}

// CloudInitSecret is the ephemeral bootstrap payload. ensureVM applies it
// before creating the VM; ensureBootstrapCleanup deletes it once the database
// is provably up.
type CloudInitSecret struct {
	Instance    *dbaasv1.DBInstance
	UserData    string
	NetworkData string
}

func (b CloudInitSecret) Build() (client.Object, error) {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      CloudInitSecretName(b.Instance),
		Namespace: b.Instance.Namespace,
	}}, nil
}

func (b CloudInitSecret) Update(obj client.Object) error {
	sec, ok := obj.(*corev1.Secret)
	if !ok {
		return wrongTypeErr("CloudInitSecret", obj)
	}
	sec.Labels = map[string]string{dbaasv1.LabelInstance: b.Instance.Name}
	sec.Type = corev1.SecretTypeOpaque
	sec.Data = nil
	sec.StringData = map[string]string{
		"userdata":    b.UserData,
		"networkdata": b.NetworkData,
	}
	return nil
}
