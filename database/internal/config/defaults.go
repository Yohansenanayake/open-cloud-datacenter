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

package config

import (
	"maps"
	"time"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func Default() Config {
	return Config{
		Operator: OperatorConfig{
			LeaderElection: LeaderElectionConfig{
				Enabled: false,
				ID:      "734f9ee3.opencloud.wso2.com",
			},
		},
		Controller: ControllerConfig{MaxConcurrentReconciles: 1},
		Server: ServerConfig{
			EnableHTTP2: false,
			Health:      ListenerConfig{BindAddress: ":8081"},
			Gateway: GatewayConfig{
				Enabled:          true,
				BindAddress:      ":8080",
				DefaultNamespace: "default",
			},
			Webhook: TLSConfig{TLS: defaultTLSFiles()},
		},
		Infrastructure: InfrastructureConfig{
			Harvester: HarvesterConfig{ManagementLogicalSwitch: "ovn-default"},
		},
		DatabaseDefaults: DatabaseDefaults{
			OSImage:        "ubuntu-22.04-server-cloudimg-amd64.img",
			StorageClass:   "longhorn",
			MasterUsername: "dbadmin",
			Port:           5432,
			OSVersion:      "24.04",
		},
		Observability: ObservabilityConfig{
			Grafana: GrafanaConfig{BaseURL: "https://grafana.monitoring.svc"},
			Metrics: MetricsConfig{
				BindAddress: "0",
				Secure:      true,
				TLS:         defaultTLSFiles(),
			},
			Monitoring: MonitoringConfig{
				ServiceMonitorLabels: map[string]string{"release": "prometheus"},
				ScrapeInterval:       15 * time.Second,
			},
		},
		Logging: LoggingConfig{
			Development:     false,
			Encoder:         "json",
			Level:           "info",
			StacktraceLevel: "error",
			TimeEncoding:    "rfc3339",
		},
		InstanceClasses: maps.Clone(dbaasv1.InstanceClasses),
	}
}

func defaultTLSFiles() TLSFiles {
	return TLSFiles{
		CertFile: "tls.crt",
		KeyFile:  "tls.key",
	}
}
