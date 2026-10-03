package postgres

import (
	"context"
	"net/url"
	"os"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
)

func recoveryStore(t *testing.T) (*Store, *Store, domain.Sandbox) {
	t.Helper()
	dsn := os.Getenv("AO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AO_TEST_DATABASE_URL must point to a disposable Postgres database")
	}
	ctx := context.Background()
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.pool.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ao_review_runtime') THEN
			CREATE ROLE ao_review_runtime LOGIN;
		END IF;
	END $$`); err != nil {
		t.Fatal(err)
	}
	if err := GrantRuntimeRole(ctx, dsn, "ao_review_runtime"); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.User("ao_review_runtime")
	store, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.ValidateRuntimeRole(ctx); err != nil {
		t.Fatal(err)
	}
	record := domain.Sandbox{OrgID: uuid.NewString(), SessionID: uuid.NewString(), Provider: "docker"}
	projectID := uuid.NewString()
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ao_organizations(id, auth_provider, slug, display_name, kind) VALUES ($1::uuid, 'local', $1::text, 'Regression', 'team')`, []any{record.OrgID}},
		{`INSERT INTO ao_projects(id, org_id, display_name, repository_url) VALUES ($1, $2, 'Regression', 'https://example.test/repository')`, []any{projectID, record.OrgID}},
		{`INSERT INTO ao_sessions(id, org_id, project_id, kind, harness, display_name, branch) VALUES ($1, $2, $3, 'worker', 'test-harness', 'Regression', 'main')`, []any{record.SessionID, record.OrgID, projectID}},
		{`INSERT INTO ao_sandboxes(session_id, org_id, provider, reconcile_lease_owner, reconcile_lease_until) VALUES ($1, $2, 'docker', 'owner', now() + interval '1 hour')`, []any{record.SessionID, record.OrgID}},
	} {
		if _, err := admin.pool.Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, err := admin.pool.Exec(context.Background(), `DELETE FROM ao_organizations WHERE id = $1`, record.OrgID)
		if err != nil {
			t.Error(err)
		}
	})
	return store, admin, record
}
