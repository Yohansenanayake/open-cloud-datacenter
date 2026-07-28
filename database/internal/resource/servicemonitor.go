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
	"maps"
	"time"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ServiceMonitorName returns the deterministic per-instance ServiceMonitor name.
func ServiceMonitorName(inst *dbaasv1.DBInstance) string {
	return fmt.Sprintf("pg-%s-monitor", inst.Name)
}

// ServiceMonitor points the cluster Prometheus (release=prometheus) at the
// instance's metrics Service.
type ServiceMonitor struct {
	Instance       *dbaasv1.DBInstance
	Labels         map[string]string
	ScrapeInterval time.Duration
}

func (b ServiceMonitor) Build() (client.Object, error) {
	return &monitoringv1.ServiceMonitor{ObjectMeta: metav1.ObjectMeta{
		Name:      ServiceMonitorName(b.Instance),
		Namespace: b.Instance.Namespace,
	}}, nil
}

func (b ServiceMonitor) Update(obj client.Object) error {
	sm, ok := obj.(*monitoringv1.ServiceMonitor)
	if !ok {
		return wrongTypeErr("ServiceMonitor", obj)
	}
	labels := maps.Clone(b.Labels)
	if len(labels) == 0 {
		labels = map[string]string{"release": "prometheus"}
	}
	labels[dbaasv1.LabelInstance] = b.Instance.Name
	sm.Labels = labels
	sm.Spec.Selector = metav1.LabelSelector{
		MatchLabels: map[string]string{
			dbaasv1.LabelMetrics:  "true",
			dbaasv1.LabelInstance: b.Instance.Name,
		},
	}
	interval := b.ScrapeInterval
	if interval == 0 {
		interval = 15 * time.Second
	}
	sm.Spec.Endpoints = []monitoringv1.Endpoint{{
		Port:     "metrics",
		Interval: monitoringv1.Duration(interval.String()),
		Path:     "/metrics",
	}}
	return nil
}
