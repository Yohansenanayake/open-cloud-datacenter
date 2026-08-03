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

// Package config owns the operator's process-wide configuration schema,
// defaults, source loading, and validation.
package config

import (
	"time"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

const DefaultFilePath = "/etc/dbaas/config.json"

type Config struct {
	Operator         OperatorConfig                       `konf:"operator"`
	Controller       ControllerConfig                     `konf:"controller"`
	Server           ServerConfig                         `konf:"server"`
	Infrastructure   InfrastructureConfig                 `konf:"infrastructure"`
	DatabaseDefaults DatabaseDefaults                     `konf:"databaseDefaults"`
	Observability    ObservabilityConfig                  `konf:"observability"`
	Logging          LoggingConfig                        `konf:"logging"`
	InstanceClasses  map[string]dbaasv1.InstanceClassSpec `konf:"instanceClasses"`
}

type OperatorConfig struct {
	LeaderElection LeaderElectionConfig `konf:"leaderElection"`
}

type LeaderElectionConfig struct {
	Enabled bool   `konf:"enabled"`
	ID      string `konf:"id"`
}

type ControllerConfig struct {
	MaxConcurrentReconciles int `konf:"maxConcurrentReconciles"`
}

type ServerConfig struct {
	EnableHTTP2 bool           `konf:"enableHTTP2"`
	Health      ListenerConfig `konf:"health"`
	Gateway     GatewayConfig  `konf:"gateway"`
	Webhook     TLSConfig      `konf:"webhook"`
}

type ListenerConfig struct {
	BindAddress string `konf:"bindAddress"`
}

type GatewayConfig struct {
	Enabled          bool   `konf:"enabled"`
	BindAddress      string `konf:"bindAddress"`
	DefaultNamespace string `konf:"defaultNamespace"`
}

// TLSConfig represents a server whose only configurable values currently
// belong to its TLS files.
type TLSConfig struct {
	TLS TLSFiles `konf:"tls"`
}

type TLSFiles struct {
	CertDir  string `konf:"certDir"`
	CertFile string `konf:"certFile"`
	KeyFile  string `konf:"keyFile"`
}

type InfrastructureConfig struct {
	Harvester HarvesterConfig `konf:"harvester"`
}

type HarvesterConfig struct {
	ManagementLogicalSwitch string `konf:"managementLogicalSwitch"`
	// ImageNamespace is the Harvester namespace internal/catalog's baked
	// prefix. Empty means ResolveVMImage falls back to "default".
	ImageNamespace string `konf:"imageNamespace"`
}

type DatabaseDefaults struct {
	StorageClass   string `konf:"storageClass"`
	MasterUsername string `konf:"masterUsername"`
	Port           int    `konf:"port"`
	// OSVersion is the internal/catalog stream key (e.g. "24.04"); platform-wide, with no per-instance override.
	OSVersion string `konf:"osVersion"`
}

type ObservabilityConfig struct {
	Grafana    GrafanaConfig    `konf:"grafana"`
	Metrics    MetricsConfig    `konf:"metrics"`
	Monitoring MonitoringConfig `konf:"monitoring"`
}

type GrafanaConfig struct {
	BaseURL string `konf:"baseURL"`
}

type MetricsConfig struct {
	BindAddress string   `konf:"bindAddress"`
	Secure      bool     `konf:"secure"`
	TLS         TLSFiles `konf:"tls"`
}

type MonitoringConfig struct {
	ServiceMonitorLabels map[string]string `konf:"serviceMonitorLabels"`
	ScrapeInterval       time.Duration     `konf:"scrapeInterval"`
}

type LoggingConfig struct {
	Development     bool   `konf:"development"`
	Encoder         string `konf:"encoder"`
	Level           string `konf:"level"`
	StacktraceLevel string `konf:"stacktraceLevel"`
	TimeEncoding    string `konf:"timeEncoding"`
}
