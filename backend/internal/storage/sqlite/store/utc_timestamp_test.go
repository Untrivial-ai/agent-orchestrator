package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestStoreNormalizesPersistedTimestampsToUTC(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)

	local := time.Date(2026, time.January, 2, 7, 0, 0, 0, time.FixedZone("UTC-05", -5*60*60))
	childLocal := local.Add(time.Minute)
	earlierUTC := local.UTC().Add(-30 * time.Minute)
	if err := s.UpsertWorkspaceProject(ctx, domain.ProjectRecord{
		ID:           "later-local",
		Path:         "/tmp/later-local",
		RegisteredAt: local,
	}, []domain.WorkspaceRepoRecord{{
		ProjectID:    "later-local",
		Name:         "api",
		RelativePath: "api",
		RegisteredAt: childLocal,
	}}); err != nil {
		t.Fatalf("upsert workspace project with local timestamps: %v", err)
	}
	if err := s.UpsertProject(ctx, domain.ProjectRecord{
		ID:           "earlier-utc",
		Path:         "/tmp/earlier-utc",
		RegisteredAt: earlierUTC,
	}); err != nil {
		t.Fatalf("upsert project with UTC timestamp: %v", err)
	}

	rec := sampleRecord("later-local")
	rec.CreatedAt = local
	rec.UpdatedAt = local
	rec.Activity.LastActivityAt = local
	rec, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create session with local timestamps: %v", err)
	}
	pinnedAt := local.Add(time.Minute)
	if ok, err := s.SetSessionPinned(ctx, rec.ID, true, &pinnedAt, pinnedAt); err != nil || !ok {
		t.Fatalf("pin session with local timestamps: ok=%v err=%v", ok, err)
	}

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer raw.Close()

	var projectMatchesUTC, projectIsAfterEarlier bool
	if err := raw.QueryRowContext(ctx, `
		SELECT registered_at = ?, registered_at > ?
		FROM projects WHERE id = ?`, local.UTC(), earlierUTC, "later-local").Scan(&projectMatchesUTC, &projectIsAfterEarlier); err != nil {
		t.Fatalf("compare project timestamp: %v", err)
	}
	if !projectMatchesUTC || !projectIsAfterEarlier {
		t.Fatalf("project timestamp comparison = (equal %v, after %v), want (true, true)", projectMatchesUTC, projectIsAfterEarlier)
	}

	var firstProject string
	if err := raw.QueryRowContext(ctx, `SELECT id FROM projects ORDER BY registered_at DESC LIMIT 1`).Scan(&firstProject); err != nil {
		t.Fatalf("order projects by timestamp: %v", err)
	}
	if firstProject != "later-local" {
		t.Fatalf("latest project = %q, want later-local", firstProject)
	}

	var workspaceRepoMatchesUTC bool
	if err := raw.QueryRowContext(ctx, `
		SELECT registered_at = ? FROM workspace_repos
		WHERE project_id = ? AND name = ?`, childLocal.UTC(), "later-local", "api").Scan(&workspaceRepoMatchesUTC); err != nil {
		t.Fatalf("compare transactional workspace repo timestamp: %v", err)
	}
	if !workspaceRepoMatchesUTC {
		t.Fatal("transactional workspace repo timestamp does not match its UTC instant")
	}

	var createdMatchesUTC, activityMatchesUTC, pinnedMatchesUTC bool
	if err := raw.QueryRowContext(ctx, `
		SELECT created_at = ?, activity_last_at = ?, pinned_at = ?
		FROM sessions WHERE id = ?`, local.UTC(), local.UTC(), pinnedAt.UTC(), rec.ID).Scan(
		&createdMatchesUTC, &activityMatchesUTC, &pinnedMatchesUTC,
	); err != nil {
		t.Fatalf("compare session timestamps: %v", err)
	}
	if !createdMatchesUTC || !activityMatchesUTC || !pinnedMatchesUTC {
		t.Fatalf(
			"session timestamp comparison = (created %v, activity %v, pinned %v), want all true",
			createdMatchesUTC, activityMatchesUTC, pinnedMatchesUTC,
		)
	}
}

func TestStorePreservesNullTimestampSemantics(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)
	seedProject(t, s, "null-time")
	rec, err := s.CreateSession(ctx, sampleRecord("null-time"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if ok, err := s.SetSessionPinned(ctx, rec.ID, false, nil, time.Now()); err != nil || !ok {
		t.Fatalf("clear pinned timestamp: ok=%v err=%v", ok, err)
	}

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer raw.Close()

	var pinnedIsNull bool
	if err := raw.QueryRowContext(ctx, `SELECT pinned_at IS NULL FROM sessions WHERE id = ?`, rec.ID).Scan(&pinnedIsNull); err != nil {
		t.Fatalf("read pinned timestamp: %v", err)
	}
	if !pinnedIsNull {
		t.Fatal("pinned_at is not NULL after clearing it")
	}
}
