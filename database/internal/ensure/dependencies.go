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

package ensure

import (
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// Dependencies contains the shared services and configuration used by steps.
// It embeds the Kubernetes client so existing step code can use Client methods
// and Scheme directly without retaining a dependency on the controller type.
type Dependencies struct {
	client.Client
	Harvester         harvester.ClientInterface
	Recorder          record.EventRecorder
	GrafanaBaseURL    string
	OperatorNamespace string
	DatabaseDefaults  operatorconfig.DatabaseDefaults
	InstanceClasses   map[string]dbaasv1.InstanceClassSpec
	Monitoring        operatorconfig.MonitoringConfig
}

func (d Dependencies) credentialsResolver() *credentials.Resolver {
	return &credentials.Resolver{
		Client:            d.Client,
		Scheme:            d.Scheme(),
		OperatorNamespace: d.OperatorNamespace,
		DefaultMasterUser: d.databaseDefaults().MasterUsername,
	}
}

func (d Dependencies) operatorNamespace() string { return d.OperatorNamespace }

func (d Dependencies) databaseDefaults() operatorconfig.DatabaseDefaults {
	defaults := d.DatabaseDefaults
	builtIn := operatorconfig.Default().DatabaseDefaults
	if defaults.StorageClass == "" {
		defaults.StorageClass = builtIn.StorageClass
	}
	if defaults.MasterUsername == "" {
		defaults.MasterUsername = builtIn.MasterUsername
	}
	if defaults.Port == 0 {
		defaults.Port = builtIn.Port
	}
	if defaults.OSVersion == "" {
		defaults.OSVersion = builtIn.OSVersion
	}
	return defaults
}

func (d Dependencies) instanceClasses() map[string]dbaasv1.InstanceClassSpec {
	if len(d.InstanceClasses) == 0 {
		return dbaasv1.InstanceClasses
	}
	return d.InstanceClasses
}

func (d Dependencies) monitoringConfig() operatorconfig.MonitoringConfig {
	monitoring := d.Monitoring
	builtIn := operatorconfig.Default().Observability.Monitoring
	if len(monitoring.ServiceMonitorLabels) == 0 {
		monitoring.ServiceMonitorLabels = builtIn.ServiceMonitorLabels
	}
	if monitoring.ScrapeInterval == 0 {
		monitoring.ScrapeInterval = builtIn.ScrapeInterval
	}
	return monitoring
}
