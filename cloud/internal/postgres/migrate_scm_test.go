package postgres

import (
	"strings"
	"testing"
)

func TestCloudSCMWebhookMigrationContract(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/00046_cloud_scm_webhooks.sql")
	if err != nil {
		t.Fatalf("read phase 2 migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS auto_inject_ci BOOLEAN NOT NULL DEFAULT TRUE",
		"CREATE TABLE ao_github_pr_applications",
		"CREATE TABLE ao_ci_feedback_outbox",
		"UNIQUE (pull_request_id, application_key)",
		"application_key TEXT NOT NULL UNIQUE",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing %q", required)
		}
	}
}

func TestCloudSCMObservationParityMigrationContract(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/00047_cloud_pr_observation_parity.sql")
	if err != nil {
		t.Fatalf("read parity migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS auto_inject_review BOOLEAN NOT NULL DEFAULT TRUE",
		"ADD COLUMN IF NOT EXISTS terminate_on_pr_merge BOOLEAN NOT NULL DEFAULT TRUE",
		"CREATE TABLE ao_pr_reviews",
		"CREATE TABLE ao_pr_review_comments",
		"provider_review_id TEXT NOT NULL",
		"provider_comment_id TEXT NOT NULL",
		"ENABLE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("parity migration missing %q", required)
		}
	}
}

func TestPullRequestRefreshFallbackMigrationContract(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/00049_pr_refresh_fallback.sql")
	if err != nil {
		t.Fatalf("read PR refresh fallback migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE ao_pr_refresh_fallbacks",
		"CHECK (reason IN ('', 'webhook_failed', 'webhook_silent'))",
		"FOREIGN KEY (org_id, pull_request_id)",
		"REFERENCES ao_pull_requests(org_id, id) ON DELETE CASCADE",
		"CREATE INDEX ao_pr_refresh_fallbacks_due_idx",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"CREATE POLICY ao_pr_refresh_fallbacks_tenant_policy",
		"org_id = ao_current_org_id()",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("fallback migration missing %q", required)
		}
	}
}
