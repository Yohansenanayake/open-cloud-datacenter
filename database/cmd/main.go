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

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	dbaasv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/controller"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/gateway"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(dbaasv1alpha1.AddToScheme(scheme))
	utilruntime.Must(kubevirtv1.AddToScheme(scheme))
	utilruntime.Must(monitoringv1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	cfg, err := operatorconfig.Load(flag.CommandLine, os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "load operator configuration: %v\n", err)
		os.Exit(1)
	}

	logOptions, err := buildLogOptions(cfg.Logging)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "configure logging: %v\n", err)
		os.Exit(1)
	}
	ctrl.SetLogger(ctrlzap.New(ctrlzap.UseFlagOptions(logOptions)))

	var tlsOpts []func(*tls.Config)
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}
	if !cfg.Server.EnableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServerOptions := webhook.Options{TLSOpts: tlsOpts}
	if cfg.Server.Webhook.TLS.CertDir != "" {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", cfg.Server.Webhook.TLS.CertDir,
			"webhook-cert-name", cfg.Server.Webhook.TLS.CertFile,
			"webhook-cert-key", cfg.Server.Webhook.TLS.KeyFile)
		webhookServerOptions.CertDir = cfg.Server.Webhook.TLS.CertDir
		webhookServerOptions.CertName = cfg.Server.Webhook.TLS.CertFile
		webhookServerOptions.KeyName = cfg.Server.Webhook.TLS.KeyFile
	}
	webhookServer := webhook.NewServer(webhookServerOptions)

	metricsServerOptions := metricsserver.Options{
		BindAddress:   cfg.Observability.Metrics.BindAddress,
		SecureServing: cfg.Observability.Metrics.Secure,
		TLSOpts:       tlsOpts,
	}
	if cfg.Observability.Metrics.Secure {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	if cfg.Observability.Metrics.TLS.CertDir != "" {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", cfg.Observability.Metrics.TLS.CertDir,
			"metrics-cert-name", cfg.Observability.Metrics.TLS.CertFile,
			"metrics-cert-key", cfg.Observability.Metrics.TLS.KeyFile)
		metricsServerOptions.CertDir = cfg.Observability.Metrics.TLS.CertDir
		metricsServerOptions.CertName = cfg.Observability.Metrics.TLS.CertFile
		metricsServerOptions.KeyName = cfg.Observability.Metrics.TLS.KeyFile
	}

	restConfig := ctrl.GetConfigOrDie()
	hvClient, err := harvester.NewTypedClient(restConfig, cfg.Observability.Grafana.BaseURL)
	if err != nil {
		setupLog.Error(err, "Failed to create Harvester client")
		os.Exit(1)
	}
	hvClient.MgmtLogicalSwitch = cfg.Infrastructure.Harvester.ManagementLogicalSwitch

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: cfg.Server.Health.BindAddress,
		LeaderElection:         cfg.Operator.LeaderElection.Enabled,
		LeaderElectionID:       cfg.Operator.LeaderElection.ID,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	if err := (&controller.DBInstanceReconciler{
		Client:                  mgr.GetClient(),
		Harvester:               hvClient,
		GrafanaBaseURL:          cfg.Observability.Grafana.BaseURL,
		OperatorNamespace:       cfg.Operator.Namespace,
		MaxConcurrentReconciles: cfg.Controller.MaxConcurrentReconciles,
		DatabaseDefaults:        cfg.DatabaseDefaults,
		InstanceClasses:         cfg.InstanceClasses,
		Monitoring:              cfg.Observability.Monitoring,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "dbinstance")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	if cfg.Server.Gateway.Enabled {
		go func() {
			setupLog.Info("Starting REST API gateway", "address", cfg.Server.Gateway.BindAddress)
			if err := gateway.RunGateway(cfg.Server.Gateway.BindAddress, cfg.Server.Gateway.DefaultNamespace,
				restConfig, mgr.GetScheme(), mgr.GetRESTMapper()); err != nil {
				setupLog.Error(err, "Gateway exited with error")
			}
		}()
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

func buildLogOptions(logging operatorconfig.LoggingConfig) (*ctrlzap.Options, error) {
	level, err := parseLogLevel(logging.Level)
	if err != nil {
		return nil, fmt.Errorf("level: %w", err)
	}
	stacktraceLevel, err := parseLogLevel(logging.StacktraceLevel)
	if err != nil {
		return nil, fmt.Errorf("stacktrace level: %w", err)
	}
	timeEncoder, err := parseTimeEncoder(logging.TimeEncoding)
	if err != nil {
		return nil, err
	}

	options := &ctrlzap.Options{
		Development:     logging.Development,
		Level:           zap.NewAtomicLevelAt(level),
		StacktraceLevel: zap.NewAtomicLevelAt(stacktraceLevel),
		TimeEncoder:     timeEncoder,
	}
	switch logging.Encoder {
	case "json":
		ctrlzap.JSONEncoder()(options)
	case "console":
		ctrlzap.ConsoleEncoder()(options)
	default:
		return nil, fmt.Errorf("unsupported encoder %q", logging.Encoder)
	}
	return options, nil
}

func parseLogLevel(value string) (zapcore.Level, error) {
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		return 0, err
	}
	return level, nil
}

func parseTimeEncoder(value string) (zapcore.TimeEncoder, error) {
	encoders := map[string]zapcore.TimeEncoder{
		"epoch":       zapcore.EpochTimeEncoder,
		"millis":      zapcore.EpochMillisTimeEncoder,
		"nano":        zapcore.EpochNanosTimeEncoder,
		"iso8601":     zapcore.ISO8601TimeEncoder,
		"rfc3339":     zapcore.RFC3339TimeEncoder,
		"rfc3339nano": zapcore.RFC3339NanoTimeEncoder,
	}
	encoder, ok := encoders[value]
	if !ok {
		return nil, fmt.Errorf("unsupported time encoding %q", value)
	}
	return encoder, nil
}
