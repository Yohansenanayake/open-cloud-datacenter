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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// MetricsEndpoints is the manual Endpoints object binding the selectorless
// metrics Service to the VM's data-net IP (the database runs in a VM, not a
// pod, so no selector can produce these endpoints). Re-applying with a new IP
// after a restart/migration retargets the scrape without touching the Service.
type MetricsEndpoints struct {
	Instance *dbaasv1.DBInstance
	VMIP     string
}

func (b MetricsEndpoints) Build() (client.Object, error) {
	// Endpoints must share the Service's name to bind to it.
	return &corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{
		Name:      MetricsServiceName(b.Instance),
		Namespace: b.Instance.Namespace,
	}}, nil
}

func (b MetricsEndpoints) Update(obj client.Object) error {
	ep, ok := obj.(*corev1.Endpoints)
	if !ok {
		return wrongTypeErr("MetricsEndpoints", obj)
	}
	ep.Labels = map[string]string{
		dbaasv1.LabelInstance: b.Instance.Name,
		dbaasv1.LabelMetrics:  "true",
	}
	if b.VMIP == "" {
		// A subset needs at least one address; the API server rejects a
		// ports-only subset. Leave Subsets empty until the IP is known.
		ep.Subsets = nil
		return nil
	}
	ep.Subsets = []corev1.EndpointSubset{{
		Addresses: []corev1.EndpointAddress{{IP: b.VMIP}},
		Ports: []corev1.EndpointPort{{
			Name:     "metrics",
			Port:     metricsPort,
			Protocol: corev1.ProtocolTCP,
		}},
	}}
	return nil
}
