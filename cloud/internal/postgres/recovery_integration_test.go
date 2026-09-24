package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	record := domain.Sandbox{OrgID: uuid.NewString(), SessionID: uuid.NewString(), Provider: "docker", PreparationGeneration: 1}
	projectID := uuid.NewString()
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ao_organizations(id, auth_provider, slug, display_name, kind) VALUES ($1::uuid, 'local', $1::text, 'Regression', 'team')`, []any{record.OrgID}},
		{`INSERT INTO ao_projects(id, org_id, display_name, repository_url) VALUES ($1, $2, 'Regression', 'https://example.test/repository')`, []any{projectID, record.OrgID}},
		{`INSERT INTO ao_sessions(id, org_id, project_id, kind, harness, display_name, branch) VALUES ($1, $2, $3, 'worker', 'test-harness', 'Regression', 'main')`, []any{record.SessionID, record.OrgID, projectID}},
		{`INSERT INTO ao_sandboxes(session_id, org_id, provider, preparation_generation, reconcile_lease_owner, reconcile_lease_until) VALUES ($1, $2, 'docker', 1, 'owner', now() + interval '1 hour')`, []any{record.SessionID, record.OrgID}},
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

func TestRecoveryStartupPromptsSurviveWorkerReplacement(t *testing.T) {
	store, admin, record := recoveryStore(t)
	ctx := context.Background()
	connect := func(epoch int64, workerID string) {
		t.Helper()
		if _, err := admin.pool.Exec(ctx, `UPDATE ao_worker_connections SET disconnected_at = now() WHERE session_id = $1`, record.SessionID); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.pool.Exec(ctx, `INSERT INTO ao_worker_connections(session_id, org_id, sandbox_id, epoch, worker_id, version)
			VALUES ($1, $2, $1, $3, $4, 'test')`, record.SessionID, record.OrgID, epoch, workerID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnsureWorkerAgentTerminal(ctx, record.OrgID, record.SessionID, workerID, epoch, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	message := func(text string) {
		t.Helper()
		if err := store.withOrg(ctx, record.OrgID, func(tx pgx.Tx) error {
			_, err := appendUserMessage(ctx, tx, record.OrgID, record.SessionID, text, 0, "", nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	ready := func(epoch int64, workerID string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"epoch": epoch, "workerId": workerID})
		if _, err := store.AppendSessionEvent(ctx, record.OrgID, record.SessionID, "agent.ready", payload); err != nil {
			t.Fatal(err)
		}
	}
	connect(1, "first")
	message("initial")
	message("follow-up")
	ready(1, "first")
	connect(2, "replacement")
	message("after restart")
	var count int
	if err := admin.pool.QueryRow(ctx, `SELECT count(*) FROM ao_worker_requests WHERE session_id = $1 AND kind = 'terminal.input'`, record.SessionID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("premature terminal deliveries=%d err=%v", count, err)
	}
	ready(2, "replacement")
	message("after readiness behind queued work")
	for _, prompt := range []string{"initial", "follow-up", "after restart", "after readiness behind queued work"} {
		turn, claimed, err := store.ClaimWorkerTurn(ctx, record.OrgID, record.SessionID, "replacement", 2)
		if err != nil || !claimed || turn.Prompt != prompt {
			t.Fatalf("want=%q turn=%+v claimed=%v err=%v", prompt, turn, claimed, err)
		}
		if _, err := store.FinishWorkerTurn(ctx, record.OrgID, record.SessionID, "replacement", turn.ID, 2, turn.Attempt, "completed", ""); err != nil {
			t.Fatal(err)
		}
	}
	message("idle ready fast path")
	if err := admin.pool.QueryRow(ctx, `SELECT count(*) FROM ao_worker_requests WHERE session_id = $1 AND kind = 'terminal.input'`, record.SessionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("ready terminal deliveries=%d err=%v", count, err)
	}
}

func TestRecoveryCreationFencesDeletionAndSurvivesLeaseLoss(t *testing.T) {
	store, admin, record := recoveryStore(t)
	ctx := context.Background()
	id := uuid.NewString()
	if err := store.BeginSandboxCreation(ctx, "owner", record, id); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSandboxDeletion(ctx, "owner", record.OrgID, record.SessionID); !errors.Is(err, ErrSandboxLeaseLost) {
		t.Fatalf("unresolved creation allowed deletion: %v", err)
	}
	if _, err := admin.pool.Exec(ctx, `UPDATE ao_sandboxes SET preparation_generation = 2, desired_state = 'deleted', reconcile_lease_owner = 'replacement' WHERE session_id = $1`, record.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSandboxCreationResult(ctx, record.OrgID, record.SessionID, id, "late-environment"); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveSandboxCreation(ctx, record.OrgID, record.SessionID, id, "adopted"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired creation was adopted: %v", err)
	}
	if err := store.RenewSandboxClaim(ctx, "owner", record.OrgID, record.SessionID, 1, time.Minute); !errors.Is(err, ErrSandboxLeaseLost) {
		t.Fatalf("old claim accepted: %v", err)
	}
	rows, err := store.ListSandboxCreations(ctx, record.OrgID, record.SessionID)
	if err != nil || len(rows) != 1 || rows[0].EnvironmentID != "late-environment" {
		t.Fatalf("persisted result=%+v err=%v", rows, err)
	}
	foreignRows, err := store.ListSandboxCreations(ctx, uuid.NewString(), record.SessionID)
	if err != nil || len(foreignRows) != 0 {
		t.Fatalf("tenant isolation: rows=%+v err=%v", foreignRows, err)
	}
	if err := store.ResolveSandboxCreation(ctx, record.OrgID, record.SessionID, id, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSandboxDeletion(ctx, "replacement", record.OrgID, record.SessionID); err != nil {
		t.Fatal(err)
	}
}
