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

	// PlaintextURL reports that URL is http and that this was explicitly
	// permitted. It exists so the operator can say so on every start: a
	// cleartext Harbor works exactly like an encrypted one until someone reads
	// the password off the wire, so nothing else would ever raise it.
	PlaintextURL bool
}

// Load builds the operator configuration from environment variables. It reads
// HARBOR_URL and POD_NAMESPACE, both required, and validates the URL before
// anything else runs.
//
// POD_NAMESPACE is where the operator itself runs, supplied by the downward
// API. It is what lets the operator read its Harbor credentials from its own
// namespace instead of holding Secret access across tenant namespaces.
func Load() (*Config, error) {
	harborURL, err := requireEnv("HARBOR_URL")
	if err != nil {
		return nil, err
	}
	allowPlaintext := envStr("HARBOR_ALLOW_PLAINTEXT_URL", "") == "true"
	if err := validateHarborURL(harborURL, allowPlaintext); err != nil {
		return nil, err
	}

	podNamespace, err := requireEnv("POD_NAMESPACE")
	if err != nil {
		return nil, err
	}

	return &Config{
		Harbor: HarborConfig{
			URL:               strings.TrimRight(harborURL, "/"),
			CredentialsSecret: envStr("HARBOR_CREDENTIALS_SECRET", "harbor-credentials"),
			Namespace:         podNamespace,
			PlaintextURL:      allowPlaintext && strings.HasPrefix(harborURL, "http://"),
		},
		MetricsCertDir: envStr("METRICS_CERT_DIR", ""),
	}, nil
}

// validateHarborURL rejects a URL that cannot address a Harbor. Catching it at
// startup turns a silent per-Registry failure into one clear message, since
// every reconcile would otherwise fail the same way for the same reason.
//
// Plaintext http is refused unless allowPlaintext is set. The client sends the
// Harbor password as Basic Auth on every request, so an http URL puts an
// administrative credential on the wire in clear — and the mistake is quiet,
// because everything keeps working. Requiring it to be asked for by name means
// it can only happen on purpose.
func validateHarborURL(raw string, allowPlaintext bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("HARBOR_URL %q is not a valid URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("HARBOR_URL %q must use http or https, got %q", raw, u.Scheme)
	}
	if u.Scheme == "http" && !allowPlaintext {
		return fmt.Errorf("HARBOR_URL %q uses http, which sends the Harbor password in clear on every "+
			"request; use https, or set HARBOR_ALLOW_PLAINTEXT_URL=true for a local development Harbor", raw)
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
