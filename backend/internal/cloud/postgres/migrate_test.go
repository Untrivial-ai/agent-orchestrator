package postgres

import (
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestCloudMigrationsAreTenantScoped(t *testing.T) {
	goose.SetBaseFS(migrationFS)
	migrations, err := goose.CollectMigrations("migrations", 0, goose.MaxVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 4 || migrations[0].Version != 1 || migrations[1].Version != 2 || migrations[2].Version != 3 || migrations[3].Version != 4 {
		t.Fatalf("migrations = %#v", migrations)
	}
	migration, err := migrationFS.ReadFile("migrations/00001_auth_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(migration)
	for _, required := range []string{
		"CREATE TABLE ao_users",
		"CREATE TABLE ao_auth_sessions",
		"CREATE TABLE ao_organizations",
		"CREATE TABLE ao_org_memberships",
		"ALTER TABLE ao_organizations FORCE ROW LEVEL SECURITY",
		"ALTER TABLE ao_org_memberships FORCE ROW LEVEL SECURITY",
		"SECURITY DEFINER",
		"REVOKE ALL ON TABLE",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration does not contain %q", required)
		}
	}
	for _, deferred := range []string{
		"ao_projects",
		"ao_sessions",
		"ao_sandboxes",
		"ao_events",
		"ao_github",
	} {
		if strings.Contains(sql, deferred) {
			t.Fatalf("foundation unexpectedly contains deferred table %q", deferred)
		}
	}
	workspaceMigration, err := migrationFS.ReadFile("migrations/00002_cloud_workspaces.sql")
	if err != nil {
		t.Fatal(err)
	}
	workspaceSQL := string(workspaceMigration)
	for _, required := range []string{
		"CREATE TABLE ao_cloud_workspaces",
		"ALTER TABLE ao_cloud_workspaces FORCE ROW LEVEL SECURITY",
		"ao_cloud_workspaces_insert",
	} {
		if !strings.Contains(workspaceSQL, required) {
			t.Fatalf("workspace migration does not contain %q", required)
		}
	}
	runtimeMigration, err := migrationFS.ReadFile("migrations/00003_session_runtimes.sql")
	if err != nil {
		t.Fatal(err)
	}
	runtimeSQL := string(runtimeMigration)
	for _, required := range []string{"CREATE TABLE ao_cloud_session_runtimes", "UNIQUE (workspace_id, session_id)", "ALTER TABLE ao_cloud_session_runtimes FORCE ROW LEVEL SECURITY"} {
		if !strings.Contains(runtimeSQL, required) {
			t.Fatalf("runtime migration does not contain %q", required)
		}
	}
	generationMigration, err := migrationFS.ReadFile("migrations/00004_session_runtime_generation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generationMigration), "ADD COLUMN generation BIGINT NOT NULL DEFAULT 1") {
		t.Fatal("runtime generation migration does not add the concurrency guard")
	}
}
