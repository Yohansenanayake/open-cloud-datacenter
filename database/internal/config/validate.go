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
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

func (c Config) Validate() error {
	if c.Operator.LeaderElection.Enabled && strings.TrimSpace(c.Operator.LeaderElection.ID) == "" {
		return fmt.Errorf("operator.leaderElection.id must not be empty when leader election is enabled")
	}
	if c.Controller.MaxConcurrentReconciles < 1 {
		return fmt.Errorf("controller.maxConcurrentReconciles must be at least 1")
	}
	if err := validateBindAddress("server.health.bindAddress", c.Server.Health.BindAddress, false); err != nil {
		return err
	}
	if c.Server.Gateway.Enabled {
		if err := validateBindAddress("server.gateway.bindAddress", c.Server.Gateway.BindAddress, false); err != nil {
			return err
		}
		if problems := validation.IsDNS1123Label(c.Server.Gateway.DefaultNamespace); len(problems) > 0 {
			return fmt.Errorf("server.gateway.defaultNamespace %q is invalid: %s",
				c.Server.Gateway.DefaultNamespace, strings.Join(problems, ", "))
		}
	}
	if err := validateTLSFiles("server.webhook.tls", c.Server.Webhook.TLS); err != nil {
		return err
	}
	if c.DatabaseDefaults.StorageClass == "" {
		return fmt.Errorf("databaseDefaults.storageClass must not be empty")
	}
	if c.DatabaseDefaults.MasterUsername == "" {
		return fmt.Errorf("databaseDefaults.masterUsername must not be empty")
	}
	if c.DatabaseDefaults.Port < 1 || c.DatabaseDefaults.Port > 65535 {
		return fmt.Errorf("databaseDefaults.port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.DatabaseDefaults.OSVersion) == "" {
		return fmt.Errorf("databaseDefaults.osVersion must not be empty")
	}
	if problems := validation.IsDNS1123Label(c.Infrastructure.Harvester.ImageNamespace); len(problems) > 0 {
		return fmt.Errorf("infrastructure.harvester.imageNamespace %q is invalid: %s",
			c.Infrastructure.Harvester.ImageNamespace, strings.Join(problems, ", "))
	}
	if c.Observability.Grafana.BaseURL != "" {
		parsed, err := url.ParseRequestURI(c.Observability.Grafana.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("observability.grafana.baseURL %q must be an absolute URL", c.Observability.Grafana.BaseURL)
		}
	}
	if err := validateBindAddress("observability.metrics.bindAddress",
		c.Observability.Metrics.BindAddress, true); err != nil {
		return err
	}
	if err := validateTLSFiles("observability.metrics.tls", c.Observability.Metrics.TLS); err != nil {
		return err
	}
	if c.Observability.Monitoring.ScrapeInterval <= 0 {
		return fmt.Errorf("observability.monitoring.scrapeInterval must be positive")
	}
	if err := validateLogging(c.Logging); err != nil {
		return err
	}
	if len(c.InstanceClasses) == 0 {
		return fmt.Errorf("instanceClasses must contain at least one class")
	}
	for name, class := range c.InstanceClasses {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("instanceClasses contains an empty class name")
		}
		if class.CPUCores < 1 {
			return fmt.Errorf("instanceClasses.%s.cpuCores must be positive", name)
		}
		if class.MemoryMB < 1 {
			return fmt.Errorf("instanceClasses.%s.memoryMB must be positive", name)
		}
		if class.MaxConnections < 1 {
			return fmt.Errorf("instanceClasses.%s.maxConnections must be positive", name)
		}
	}
	return nil
}

func validateBindAddress(path, value string, allowDisabled bool) error {
	if allowDisabled && value == "0" {
		return nil
	}
	if value == "" {
		return fmt.Errorf("%s must not be empty", path)
	}
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("%s %q is invalid: %w", path, value, err)
	}
	if port == "" {
		return fmt.Errorf("%s %q must include a port", path, value)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("%s %q must use a numeric port between 1 and 65535", path, value)
	}
	return nil
}

func validateTLSFiles(path string, files TLSFiles) error {
	if files.CertDir == "" {
		return nil
	}
	if files.CertFile == "" {
		return fmt.Errorf("%s.certFile must not be empty when certDir is set", path)
	}
	if files.KeyFile == "" {
		return fmt.Errorf("%s.keyFile must not be empty when certDir is set", path)
	}
	return nil
}

func validateLogging(logging LoggingConfig) error {
	if !slices.Contains([]string{"json", "console"}, logging.Encoder) {
		return fmt.Errorf("logging.encoder %q must be json or console", logging.Encoder)
	}
	levels := []string{"debug", "info", "warn", "error", "panic"}
	if !slices.Contains(levels, logging.Level) {
		return fmt.Errorf("logging.level %q is invalid", logging.Level)
	}
	if !slices.Contains(levels, logging.StacktraceLevel) {
		return fmt.Errorf("logging.stacktraceLevel %q is invalid", logging.StacktraceLevel)
	}
	encodings := []string{"epoch", "millis", "nano", "iso8601", "rfc3339", "rfc3339nano"}
	if !slices.Contains(encodings, logging.TimeEncoding) {
		return fmt.Errorf("logging.timeEncoding %q is invalid", logging.TimeEncoding)
	}
	return nil
}
