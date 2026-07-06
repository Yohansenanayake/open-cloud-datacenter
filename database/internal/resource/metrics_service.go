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
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// metricsPort is the postgres_exporter scrape port (exporter deployment inside
// the VM is future work; the Service/ServiceMonitor contract is stable).
const metricsPort = 9187

// MetricsServiceName returns the deterministic per-instance metrics Service
// name (also used by the Endpoints object, which must share it).
func MetricsServiceName(inst *dbaasv1.DBInstance) string {
	return fmt.Sprintf("pg-%s-metrics", inst.Name)
}

// MetricsService is the selectorless headless Service the ServiceMonitor
// scrapes through; the VM's data-net IP is attached via a manual Endpoints
// object (MetricsEndpoints) because the VM is not a pod.
type MetricsService struct {
	Instance *dbaasv1.DBInstance
}

func (b MetricsService) Build() (client.Object, error) {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name:      MetricsServiceName(b.Instance),
		Namespace: b.Instance.Namespace,
	}}, nil
}

func (b MetricsService) Update(obj client.Object) error {
	svc, ok := obj.(*corev1.Service)
	if !ok {
		return wrongTypeErr("MetricsService", obj)
	}
	svc.Labels = map[string]string{
		dbaasv1.LabelInstance: b.Instance.Name,
		dbaasv1.LabelMetrics:  "true",
	}
	svc.Spec.Type = corev1.ServiceTypeClusterIP
	// Headless is a create-time decision: clusterIP is immutable and
	// server-owned after creation, so only assert it on a new object.
	if svc.ResourceVersion == "" {
		svc.Spec.ClusterIP = corev1.ClusterIPNone
	}
	svc.Spec.Selector = nil // endpoints are managed manually (VM IP, not pods)
	svc.Spec.Ports = []corev1.ServicePort{{
		Name:       "metrics",
		Port:       metricsPort,
		TargetPort: intstr.FromInt32(metricsPort),
		Protocol:   corev1.ProtocolTCP,
	}}
	return nil
}
