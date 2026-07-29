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

import "flag"

func bindFlags(set *flag.FlagSet, defaults Config) {
	set.Bool("operator.leaderElection.enabled", defaults.Operator.LeaderElection.Enabled,
		"Enable controller-manager leader election.")
	set.String("operator.leaderElection.id", defaults.Operator.LeaderElection.ID,
		"Leader-election lease identifier.")
	set.Int("controller.maxConcurrentReconciles", defaults.Controller.MaxConcurrentReconciles,
		"Maximum number of DBInstances reconciled concurrently.")

	set.Bool("server.enableHTTP2", defaults.Server.EnableHTTP2,
		"Enable HTTP/2 for metrics and webhook servers.")
	set.String("server.health.bindAddress", defaults.Server.Health.BindAddress,
		"Health and readiness probe bind address.")
	set.Bool("server.gateway.enabled", defaults.Server.Gateway.Enabled,
		"Enable the DBInstance REST gateway.")
	set.String("server.gateway.bindAddress", defaults.Server.Gateway.BindAddress,
		"DBInstance REST gateway bind address.")
	set.String("server.gateway.defaultNamespace", defaults.Server.Gateway.DefaultNamespace,
		"Namespace used by the DBInstance REST gateway.")
	set.String("server.webhook.tls.certDir", defaults.Server.Webhook.TLS.CertDir,
		"Directory containing webhook TLS files.")
	set.String("server.webhook.tls.certFile", defaults.Server.Webhook.TLS.CertFile,
		"Webhook TLS certificate filename.")
	set.String("server.webhook.tls.keyFile", defaults.Server.Webhook.TLS.KeyFile,
		"Webhook TLS private-key filename.")

	set.String("infrastructure.harvester.managementLogicalSwitch",
		defaults.Infrastructure.Harvester.ManagementLogicalSwitch,
		"Kube-OVN logical switch used for VM launcher management networking.")

	set.String("databaseDefaults.osImage", defaults.DatabaseDefaults.OSImage,
		"Default Harvester VM image for DBInstances.")
	set.String("databaseDefaults.storageClass", defaults.DatabaseDefaults.StorageClass,
		"Default storage class for DBInstances.")
	set.String("databaseDefaults.masterUsername", defaults.DatabaseDefaults.MasterUsername,
		"Default PostgreSQL administrator username.")
	set.Int("databaseDefaults.port", defaults.DatabaseDefaults.Port,
		"Default PostgreSQL port.")
	set.String("databaseDefaults.osVersion", defaults.DatabaseDefaults.OSVersion,
		"Default baked-image OS stream (internal/catalog key) for DBInstance provisioning and repave.")

	set.String("observability.grafana.baseURL", defaults.Observability.Grafana.BaseURL,
		"Base URL used for per-instance Grafana links.")
	set.String("observability.metrics.bindAddress", defaults.Observability.Metrics.BindAddress,
		"Controller metrics bind address; use 0 to disable.")
	set.Bool("observability.metrics.secure", defaults.Observability.Metrics.Secure,
		"Serve controller metrics over HTTPS.")
	set.String("observability.metrics.tls.certDir", defaults.Observability.Metrics.TLS.CertDir,
		"Directory containing metrics TLS files.")
	set.String("observability.metrics.tls.certFile", defaults.Observability.Metrics.TLS.CertFile,
		"Metrics TLS certificate filename.")
	set.String("observability.metrics.tls.keyFile", defaults.Observability.Metrics.TLS.KeyFile,
		"Metrics TLS private-key filename.")
	set.Duration("observability.monitoring.scrapeInterval",
		defaults.Observability.Monitoring.ScrapeInterval,
		"Prometheus ServiceMonitor scrape interval.")

	set.Bool("logging.development", defaults.Logging.Development,
		"Enable development logging.")
	set.String("logging.encoder", defaults.Logging.Encoder,
		"Log encoder: json or console.")
	set.String("logging.level", defaults.Logging.Level,
		"Log level: debug, info, warn, or error.")
	set.String("logging.stacktraceLevel", defaults.Logging.StacktraceLevel,
		"Minimum level that includes a stack trace.")
	set.String("logging.timeEncoding", defaults.Logging.TimeEncoding,
		"Log timestamp encoding.")

}
