package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// write saves a config file and clears the secret environment variable,
// which `task` loads from .env, so tests only see their own values.
func write(t *testing.T, content string) string {
	t.Helper()
	t.Setenv(ClientSecretEnv, "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validOIDC = `
public_url: https://chatchat.test
oidc:
  issuer: https://auth.test
  client_id: chatchat
  client_secret: secret
`

func TestLoadRequiresAuthentication(t *testing.T) {
	t.Setenv(ClientSecretEnv, "")
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "public_url") {
		t.Fatalf("err = %v, want public_url error", err)
	}
}

func TestLoadOIDC(t *testing.T) {
	cfg, err := Load(write(t, "listen: \":9000\"\n"+validOIDC))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Listen = ":9000"
	want.PublicURL = "https://chatchat.test"
	want.OIDC = OIDC{Issuer: "https://auth.test", ClientID: "chatchat", ClientSecret: "secret"}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadSecretFromEnv(t *testing.T) {
	path := write(t, strings.Replace(validOIDC, "  client_secret: secret\n", "", 1))
	t.Setenv(ClientSecretEnv, "oidc-env")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDC.ClientSecret != "oidc-env" {
		t.Fatalf("secret = %q", cfg.OIDC.ClientSecret)
	}
}

func TestLoadDevUserNeedsNoOIDC(t *testing.T) {
	if _, err := Load(write(t, "dev_user: eric\n")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	// A misplaced dev_user under oidc must not silently fall back to OIDC.
	_, err := Load(write(t, validOIDC+"  dev_user: eric\n"))
	if err == nil || !strings.Contains(err.Error(), "dev_user") {
		t.Fatalf("err = %v, want unknown dev_user error", err)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	_, err := Load(write(t, ""))
	if err == nil || !strings.Contains(err.Error(), "public_url") {
		t.Fatalf("err = %v, want public_url error", err)
	}
}

func TestLoadLimits(t *testing.T) {
	cfg, err := Load(write(t, `dev_user: eric
limits:
  presence_lease: 2m
  idle_destroy_after: 1h
  page_size_max: 500
`))
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultLimits()
	want.PresenceLease = 2 * time.Minute
	want.IdleDestroyAfter = time.Hour
	want.PageSizeMax = 500
	if cfg.Limits != want {
		t.Fatalf("limits = %+v, want %+v", cfg.Limits, want)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	for _, content := range []string{
		"data_dir: \"\"\ndev_user: eric\n",
		"dev_user: eric\npublic_url: chatchat.test\n",
		strings.Replace(validOIDC, "https://chatchat.test", "https://chatchat.test/sub", 1),
		strings.Replace(validOIDC, "https://chatchat.test", "chatchat.test", 1),
		strings.Replace(validOIDC, "  issuer: https://auth.test\n", "", 1),
		// A wait as long as the lease would mark waiting participants offline.
		"dev_user: eric\nlimits:\n  wait_max: 90s\n",
		"dev_user: eric\nlimits:\n  page_size_default: 300\n",
		"dev_user: eric\nlimits:\n  presence_lease: 0s\n",
	} {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("expected error for:\n%s", content)
		}
	}
}
