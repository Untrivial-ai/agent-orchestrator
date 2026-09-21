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
		"ADD COLUMN auto_inject_ci BOOLEAN NOT NULL DEFAULT TRUE",
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
