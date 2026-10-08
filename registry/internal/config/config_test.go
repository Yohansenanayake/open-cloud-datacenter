package config

import (
	"strings"
	"testing"
)

func TestEnvStr(t *testing.T) {
	t.Run("returns the env var when set", func(t *testing.T) {
		t.Setenv("TEST_ENV_STR", "custom-value")
		if got := envStr("TEST_ENV_STR", "default-value"); got != "custom-value" {
			t.Errorf("envStr() = %q, want %q", got, "custom-value")
		}
	})
	t.Run("falls back to default when unset", func(t *testing.T) {
		if got := envStr("TEST_ENV_STR_NEVER_SET", "default-value"); got != "default-value" {
			t.Errorf("envStr() = %q, want %q", got, "default-value")
		}
	})
	t.Run("treats an explicitly empty value as unset", func(t *testing.T) {
		t.Setenv("TEST_ENV_STR_EMPTY", "")
		if got := envStr("TEST_ENV_STR_EMPTY", "default-value"); got != "default-value" {
			t.Errorf("envStr() = %q, want default %q for an explicitly empty env var", got, "default-value")
		}
	})
}

func TestRequireEnv(t *testing.T) {
	t.Run("returns the value when set", func(t *testing.T) {
		t.Setenv("TEST_REQUIRE_ENV", "required-value")
		got, err := requireEnv("TEST_REQUIRE_ENV")
		if err != nil {
			t.Fatalf("requireEnv() error = %v", err)
		}
		if got != "required-value" {
			t.Errorf("requireEnv() = %q, want %q", got, "required-value")
		}
	})
	t.Run("errors when unset, naming the variable", func(t *testing.T) {
		_, err := requireEnv("TEST_REQUIRE_ENV_NEVER_SET")
		if err == nil {
			t.Fatal("requireEnv() error = nil, want an error for a missing required env var")
		}
		if !strings.Contains(err.Error(), "TEST_REQUIRE_ENV_NEVER_SET") {
			t.Errorf("error = %v, want it to name the missing env var", err)
		}
	})
}

func TestLoad(t *testing.T) {
	// Load requires both to be set; each subtest overrides from this baseline.
	setRequired := func(t *testing.T) {
		t.Setenv("HARBOR_URL", "https://registry.example.com")
		t.Setenv("POD_NAMESPACE", "registry-system")
	}

	t.Run("required vars present, defaults fill the rest", func(t *testing.T) {
		setRequired(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Harbor.URL != "https://registry.example.com" {
			t.Errorf("Harbor.URL = %q, want %q", cfg.Harbor.URL, "https://registry.example.com")
		}
		if cfg.Harbor.Namespace != "registry-system" {
			t.Errorf("Harbor.Namespace = %q, want %q", cfg.Harbor.Namespace, "registry-system")
		}
		if cfg.Harbor.CredentialsSecret != "harbor-credentials" {
			t.Errorf("Harbor.CredentialsSecret default = %q, want %q", cfg.Harbor.CredentialsSecret, "harbor-credentials")
		}
	})

	t.Run("a trailing slash on HARBOR_URL is trimmed", func(t *testing.T) {
		setRequired(t)
		t.Setenv("HARBOR_URL", "https://registry.example.com/")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		// Paths are appended directly, so a trailing slash would produce "//api/...".
		if cfg.Harbor.URL != "https://registry.example.com" {
			t.Errorf("Harbor.URL = %q, want the trailing slash trimmed", cfg.Harbor.URL)
		}
	})

	t.Run("env vars override every default", func(t *testing.T) {
		setRequired(t)
		t.Setenv("HARBOR_CREDENTIALS_SECRET", "custom-creds")
		t.Setenv("METRICS_CERT_DIR", "/tmp/certs")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Harbor.CredentialsSecret != "custom-creds" {
			t.Errorf("Harbor.CredentialsSecret = %q, want override %q", cfg.Harbor.CredentialsSecret, "custom-creds")
		}
		if cfg.MetricsCertDir != "/tmp/certs" {
			t.Errorf("MetricsCertDir = %q, want override %q", cfg.MetricsCertDir, "/tmp/certs")
		}
	})

	t.Run("missing required vars error rather than silently defaulting", func(t *testing.T) {
		for _, missing := range []string{"HARBOR_URL", "POD_NAMESPACE"} {
			t.Run(missing, func(t *testing.T) {
				setRequired(t)
				t.Setenv(missing, "")

				cfg, err := Load()
				if err == nil {
					t.Fatalf("Load() error = nil, want an error when %s is unset", missing)
				}
				if cfg != nil {
					t.Errorf("Load() config = %v, want nil alongside the error", cfg)
				}
				if !strings.Contains(err.Error(), missing) {
					t.Errorf("error = %v, want it to name %s", err, missing)
				}
			})
		}
	})

	t.Run("an unusable HARBOR_URL is rejected at startup", func(t *testing.T) {
		// Every reconcile would fail identically for the same reason, so this
		// belongs at startup as one clear message rather than per Registry.
		for _, bad := range []string{"registry.example.com", "ftp://registry.example.com", "https://", "://nope"} {
			t.Run(bad, func(t *testing.T) {
				setRequired(t)
				t.Setenv("HARBOR_URL", bad)
				if _, err := Load(); err == nil {
					t.Errorf("Load() error = nil, want HARBOR_URL %q rejected", bad)
				}
			})
		}
	})
}
