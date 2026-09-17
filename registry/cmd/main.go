package main

import (
	"net/http"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/controller"
)

var scheme = runtime.NewScheme()

// init registers the API types with the scheme.
func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(registryv1alpha1.AddToScheme(scheme))
}

// main starts the controller manager and the Registry reconciler.
func main() {
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()
	ctrl.SetLogger(zapr.NewLogger(logger))

	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	// Leader election is ON so that running multiple replicas yields exactly
	// one active reconciler (hot standby for failover).
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":8081",
		// Metrics served over HTTPS; the API server validates each scraper's
		// bearer token. CertDir comes from METRICS_CERT_DIR: set, the endpoint
		// serves that certificate and is verifiable under its Service DNS name;
		// unset, it self-signs for localhost and only an insecure scraper can
		// read it. See config/default/kustomization.yaml's
		// [METRICS WITH CERTMANAGER] block for the manifests that mount one.
		Metrics: metricsserver.Options{
			BindAddress:    ":8443",
			SecureServing:  true,
			FilterProvider: filters.WithAuthenticationAndAuthorization,
			CertDir:        cfg.MetricsCertDir,
		},
		LeaderElection:   true,
		LeaderElectionID: "registry-provisioner.opencloud.wso2.com",
	})
	if err != nil {
		logger.Fatal("failed to create controller manager", zap.Error(err))
	}

	registryReconciler := &controller.RegistryReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorder("registry"),
		HarborCfg: cfg.Harbor,
	}
	if err := registryReconciler.SetupWithManager(mgr); err != nil {
		logger.Fatal("failed to setup Registry controller", zap.Error(err))
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Fatal("failed to add healthz check", zap.Error(err))
	}
	// Readiness reports whether the central Harbor is reachable and accepts the
	// operator's credentials, so an unusable Harbor surfaces as a pod that is
	// not Ready rather than only in logs. It is deliberately not a liveness
	// check: retrying is correct behaviour, and a restart would fix nothing.
	if err := mgr.AddReadyzCheck("harbor", func(req *http.Request) error {
		return registryReconciler.CheckHarborAccess(req.Context())
	}); err != nil {
		logger.Fatal("failed to add readyz check", zap.Error(err))
	}

	logger.Info("starting registry operator", zap.String("harbor", cfg.Harbor.URL))
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Fatal("controller manager error", zap.Error(err))
	}
}
