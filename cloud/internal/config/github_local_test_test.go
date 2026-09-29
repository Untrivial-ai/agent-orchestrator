package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func setGitHubLocalTestEnvironment(t *testing.T, environment string, localTest bool) {
	t.Helper()
	t.Setenv("AO_CLOUD_ENV", environment)
	t.Setenv("AO_CLOUD_DATABASE_URL", "postgres://example.invalid/ao")
	t.Setenv("AO_CLOUD_LOCAL_AUTH", "true")
	t.Setenv("AO_CLOUD_SANDBOX_PROVIDER", "docker")
	t.Setenv("AO_CLOUD_PUBLIC_URL", "https://ao-cloud-test.example")
	t.Setenv("AO_CLOUD_WORKER_SIGNING_KEY", strings.Repeat("w", 32))
	t.Setenv("AO_CLOUD_GITHUB_LOCAL_TEST", map[bool]string{true: "true", false: "false"}[localTest])
	t.Setenv("AO_CLOUD_GITHUB_APP_ID", "1234")
	t.Setenv("AO_CLOUD_GITHUB_APP_SLUG", "ao-cloud-test")
	t.Setenv("AO_CLOUD_GITHUB_CLIENT_ID", "client-id")
	t.Setenv("AO_CLOUD_GITHUB_CLIENT_SECRET", "client-secret")
	t.Setenv("AO_CLOUD_GITHUB_PRIVATE_KEY", "private-key")
	t.Setenv("AO_CLOUD_GITHUB_WEBHOOK_SECRET", "webhook-secret")
	t.Setenv("AO_CLOUD_GITHUB_STATE_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AO_CLOUD_REPOSITORY_BROKER_TOKEN", strings.Repeat("b", 32))
}

func TestLoadPullRequestWebhookSilenceGrace(t *testing.T) {
	setGitHubLocalTestEnvironment(t, "development", true)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PRWebhookSilenceGrace != 2*time.Minute {
		t.Fatalf("default silence grace = %s, want 2m", cfg.PRWebhookSilenceGrace)
	}

	t.Setenv("AO_CLOUD_PR_WEBHOOK_SILENCE_GRACE", "4m")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PRWebhookSilenceGrace != 4*time.Minute {
		t.Fatalf("configured silence grace = %s, want 4m", cfg.PRWebhookSilenceGrace)
	}
}

func TestLoadAllowsGitHubAppInDevelopmentOnlyWithLocalTestOptIn(t *testing.T) {
	setGitHubLocalTestEnvironment(t, "development", true)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.GitHub.Enabled() {
		t.Fatal("GitHub App should be enabled")
	}
}

func TestLoadRejectsGitHubAppInDevelopmentWithoutLocalTestOptIn(t *testing.T) {
	setGitHubLocalTestEnvironment(t, "development", false)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("Load() error = %v, want production-only error", err)
	}
}

func TestLoadRejectsGitHubLocalTestOptInOutsideDevelopment(t *testing.T) {
	setGitHubLocalTestEnvironment(t, "production", true)
	// Reach the local-test guard before unrelated hosted-environment checks.
	t.Setenv("AO_CLOUD_LOCAL_AUTH", "false")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "only be enabled in development") {
		t.Fatalf("Load() error = %v, want development-only error", err)
	}
}
