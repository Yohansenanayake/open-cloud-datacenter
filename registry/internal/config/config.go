package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Secret data keys the operator reads Harbor's credentials from.
const (
	HarborUsernameKey = "username"
	HarborPasswordKey = "password"
)

// Config holds the operator's configuration. The operator does not deploy
// Harbor — it drives one that already exists — so its only external state is
// how to reach that Harbor and how to authenticate to it.
type Config struct {
	Harbor HarborConfig

	// MetricsCertDir holds a serving certificate (tls.crt/tls.key) for the
	// metrics endpoint. Empty means the manager self-signs for localhost,
	// which only a scraper skipping verification can read; set it to make the
	// endpoint verifiable under its Service DNS name.
	MetricsCertDir string
}

// HarborConfig locates the Harbor every Registry becomes a project inside.
type HarborConfig struct {
	// URL is the base URL of the central Harbor, e.g.
	// https://registry.example.com. It is also what a Registry reports as its
	// push/pull address, so it must be the name clients actually resolve.
	URL string

	// CredentialsSecret names a Secret holding Harbor credentials under the
	// keys "username" and "password". It lives in Namespace, so the operator
	// never reads a tenant namespace to authenticate.
	CredentialsSecret string

	// Namespace is the operator's own namespace, where CredentialsSecret
	// lives. Supplied by the downward API.
	Namespace string
}

// Load builds the operator configuration from environment variables.
func Load() (*Config, error) {
	harborURL, err := requireEnv("HARBOR_URL")
	if err != nil {
		return nil, err
	}
	if err := validateHarborURL(harborURL); err != nil {
		return nil, err
	}

	// The operator's own namespace, so it can read its credentials Secret
	// without holding Secret access across tenant namespaces.
	podNamespace, err := requireEnv("POD_NAMESPACE")
	if err != nil {
		return nil, err
	}

	return &Config{
		Harbor: HarborConfig{
			URL:               strings.TrimRight(harborURL, "/"),
			CredentialsSecret: envStr("HARBOR_CREDENTIALS_SECRET", "harbor-credentials"),
			Namespace:         podNamespace,
		},
		MetricsCertDir: envStr("METRICS_CERT_DIR", ""),
	}, nil
}

// validateHarborURL rejects a URL that cannot address a Harbor. Catching it at
// startup turns a silent per-Registry failure into one clear message, since
// every reconcile would otherwise fail the same way for the same reason.
func validateHarborURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("HARBOR_URL %q is not a valid URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("HARBOR_URL %q must use http or https, got %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("HARBOR_URL %q has no host", raw)
	}
	return nil
}

// requireEnv returns the value of key, or an error if it is unset or empty.
// Startup still stops on a missing value; returning it lets the caller report
// which one instead of unwinding a stack.
func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("required environment variable %s is not set", key)
	}
	return v, nil
}

// envStr returns the value of key, or def if it is unset or empty.
func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
