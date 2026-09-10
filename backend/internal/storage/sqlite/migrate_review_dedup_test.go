package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

// upTo migrates the db to a specific goose version, sharing migrate()'s goose
// global setup under gooseMu.
func upTo(t *testing.T, db *sql.DB, version int64) {
	t.Helper()
	gooseMu.Lock()
	defer gooseMu.Unlock()
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.UpTo(db, "migrations", version); err != nil {
		t.Fatalf("migrate to %d: %v", version, err)
	}
}

func downTo(t *testing.T, db *sql.DB, version int64) {
	t.Helper()
	gooseMu.Lock()
	defer gooseMu.Unlock()
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.DownTo(db, "migrations", version); err != nil {
		t.Fatalf("down to %d: %v", version, err)
	}
}

// TestMigration0013DedupesExistingDuplicates guards the data-safety concern in
// #246: a pre-#242 daemon could already hold duplicate (session_id, target_sha)
// review_run rows, on which CREATE UNIQUE INDEX would fail and wedge startup. The
// migration must collapse each group to one survivor first. We open without the
// foreign_keys pragma so review_run rows can be seeded without the full
// project/session/review parent chain — the dedup is pure data movement.
func TestMigration0013DedupesExistingDuplicates(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Stop just before 0013: review tables exist, the unique index does not.
	upTo(t, db, 12)

	// One duplicate group on shaA (a stale run, a completed pass carrying the
	// verdict, and a newer still-running pass), plus a distinct sha and two
	// empty-sha rows that the partial index excludes and must all survive.
	seed := []struct{ id, sha, status, createdAt string }{
		{"r-old", "shaA", "running", "2026-06-01T00:00:00Z"},
		{"r-complete", "shaA", "complete", "2026-06-02T00:00:00Z"},
		{"r-new-running", "shaA", "running", "2026-06-03T00:00:00Z"},
		{"r-other-sha", "shaB", "running", "2026-06-01T00:00:00Z"},
		{"r-empty-1", "", "running", "2026-06-01T00:00:00Z"},
		{"r-empty-2", "", "running", "2026-06-02T00:00:00Z"},
	}
	for _, r := range seed {
		if _, err := db.Exec(
			`INSERT INTO review_run (id, review_id, session_id, harness, pr_url, target_sha, status, verdict, body, created_at)
			 VALUES (?, 'rev-1', 's1', 'claude-code', '', ?, ?, '', '', ?)`,
			r.id, r.sha, r.status, r.createdAt,
		); err != nil {
			t.Fatalf("seed %s: %v", r.id, err)
		}
	}

	// Applying 0013 dedupes, then builds the unique index.
	upTo(t, db, 13)

	survivors := map[string]bool{}
	rows, err := db.Query(`SELECT id FROM review_run`)
	if err != nil {
		t.Fatalf("query survivors: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		survivors[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	// shaA collapses to the completed pass; everything else is untouched.
	want := []string{"r-complete", "r-other-sha", "r-empty-1", "r-empty-2"}
	if len(survivors) != len(want) {
		t.Fatalf("survivors = %v, want exactly %v", survivors, want)
	}
	for _, id := range want {
		if !survivors[id] {
			t.Errorf("expected %q to survive the dedup", id)
		}
	}

	// The index is live and now rejects a fresh duplicate.
	if _, err := db.Exec(
		`INSERT INTO review_run (id, review_id, session_id, harness, pr_url, target_sha, status, verdict, body, created_at)
		 VALUES ('dup', 'rev-1', 's1', 'claude-code', '', 'shaA', 'running', '', '', '2026-06-04T00:00:00Z')`,
	); err == nil {
		t.Fatal("expected unique-index violation inserting a duplicate (session_id, target_sha)")
	}
}

func TestMigration0044BackfillsBatchlessReviewRuns(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 43)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO review_run (id, review_id, session_id, harness, pr_url, target_sha, status, verdict, body, created_at)
		 VALUES ('run-1', 'review-1', 'session-1', 'claude-code', 'pr1', 'sha1', 'running', '', '', '2026-06-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed batchless review run: %v", err)
	}

	upTo(t, db, 44)
	var batchID string
	if err := db.QueryRow(`SELECT batch_id FROM review_run WHERE id = 'run-1'`).Scan(&batchID); err != nil {
		t.Fatalf("query migrated batch id: %v", err)
	}
	if batchID != "run-1" {
		t.Fatalf("batch_id = %q, want run id", batchID)
	}
}

func TestMigration0080MovesReviewerSessionsIntoPerHarnessReviewRows(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 48)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys for review-only fixture: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO review (
	id, session_id, project_id, harness, pr_url, reviewer_handle_id,
	agent_session_id, created_at, updated_at
) VALUES (
	'review-codex', 'session-1', 'project-1', 'codex', 'pr-1',
	'codex-handle', 'codex-agent-session', '2026-08-01T00:00:00Z', '2026-08-01T00:00:00Z'
);
INSERT INTO review_session (
	session_id, project_id, harness, reviewer_handle_id,
	agent_session_id, created_at, updated_at
) VALUES (
	'session-1', 'project-1', 'opencode', 'opencode-handle',
	'opencode-agent-session', '2026-08-02T00:00:00Z', '2026-08-02T00:00:00Z'
);`); err != nil {
		t.Fatalf("seed 0048 review state: %v", err)
	}

	upTo(t, db, 80)

	rows, err := db.Query(`
SELECT harness, reviewer_handle_id, agent_session_id
FROM review
WHERE session_id = 'session-1'
ORDER BY harness`)
	if err != nil {
		t.Fatalf("query migrated reviews: %v", err)
	}
	defer rows.Close()

	got := map[string][2]string{}
	for rows.Next() {
		var harness, handleID, agentSessionID string
		if err := rows.Scan(&harness, &handleID, &agentSessionID); err != nil {
			t.Fatalf("scan migrated review: %v", err)
		}
		got[harness] = [2]string{handleID, agentSessionID}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("migrated review rows: %v", err)
	}
	if len(got) != 2 || got["codex"] != [2]string{"codex-handle", "codex-agent-session"} || got["opencode"] != [2]string{"opencode-handle", "opencode-agent-session"} {
		t.Fatalf("migrated reviews = %#v", got)
	}

	// Prove the generated upsert conflict target now matches a physical unique
	// constraint and that a second harness can coexist for one worker.
	if _, err := db.Exec(`
INSERT INTO review (
	id, session_id, project_id, harness, pr_url, reviewer_handle_id,
	agent_session_id, created_at, updated_at
) VALUES (
	'ignored', 'session-1', 'project-1', 'codex', 'pr-1',
	'new-codex-handle', '', '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z'
)
ON CONFLICT (session_id, harness) DO UPDATE SET
	reviewer_handle_id = excluded.reviewer_handle_id,
	updated_at = excluded.updated_at;`); err != nil {
		t.Fatalf("per-harness upsert: %v", err)
	}

	var handleID string
	if err := db.QueryRow(`SELECT reviewer_handle_id FROM review WHERE session_id = 'session-1' AND harness = 'codex'`).Scan(&handleID); err != nil {
		t.Fatalf("query updated codex row: %v", err)
	}
	if handleID != "new-codex-handle" {
		t.Fatalf("codex handle = %q, want new-codex-handle", handleID)
	}

	var reviewSessionTableCount int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'review_session'`).Scan(&reviewSessionTableCount); err != nil {
		t.Fatalf("query review_session table: %v", err)
	}
	if reviewSessionTableCount != 0 {
		t.Fatalf("review_session table count = %d, want 0", reviewSessionTableCount)
	}
}

func TestMigration0103RoundTripsWithoutChangeLogCompatViews(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 103)

	var leakedCompatRefs int
	if err := db.QueryRow(`
SELECT count(*)
FROM sqlite_master
WHERE type = 'trigger' AND sql LIKE '%change_log_old%'`).Scan(&leakedCompatRefs); err != nil {
		t.Fatalf("query trigger compatibility refs after up: %v", err)
	}
	if leakedCompatRefs != 0 {
		t.Fatalf("triggers still reference change_log_old after up: %d", leakedCompatRefs)
	}

	downTo(t, db, 102)
	upTo(t, db, 103)

	var reviewRunTriggerCount int
	if err := db.QueryRow(`
SELECT count(*)
FROM sqlite_master
WHERE type = 'trigger' AND name IN ('review_run_cdc_insert', 'review_run_cdc_update')`).Scan(&reviewRunTriggerCount); err != nil {
		t.Fatalf("query review-run triggers after up/down/up: %v", err)
	}
	if reviewRunTriggerCount != 2 {
		t.Fatalf("review-run trigger count after up/down/up = %d, want 2", reviewRunTriggerCount)
	}
}

func TestPrepareReviewPerHarnessMigrationRepairsApplied0080StaleReviewShape(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
CREATE TABLE goose_db_version (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	version_id INTEGER NOT NULL,
	is_applied INTEGER NOT NULL,
	tstamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (48, 1), (49, 1), (80, 1);
CREATE TABLE projects (
	id TEXT PRIMARY KEY
);
CREATE TABLE sessions (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id)
);
INSERT INTO projects (id) VALUES ('project-1');
INSERT INTO sessions (id, project_id) VALUES ('session-1', 'project-1');
CREATE TABLE review (
	id                 TEXT PRIMARY KEY,
	session_id         TEXT NOT NULL,
	project_id         TEXT NOT NULL,
	harness            TEXT NOT NULL,
	pr_url             TEXT NOT NULL DEFAULT '',
	reviewer_handle_id TEXT NOT NULL DEFAULT '',
	created_at         TIMESTAMP NOT NULL,
	updated_at         TIMESTAMP NOT NULL,
	UNIQUE(session_id)
);
`); err != nil {
		t.Fatalf("seed drifted review schema: %v", err)
	}

	if err := prepareReviewPerHarnessMigration(db); err != nil {
		t.Fatalf("repair review schema: %v", err)
	}

	var agentSessionColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('review') WHERE name = 'agent_session_id'`).Scan(&agentSessionColumn); err != nil {
		t.Fatalf("query review columns: %v", err)
	}
	if agentSessionColumn != 1 {
		t.Fatalf("agent_session_id column count = %d, want 1", agentSessionColumn)
	}

	if _, err := db.Exec(`
INSERT INTO review (
	id, session_id, project_id, harness, pr_url, reviewer_handle_id,
	agent_session_id, created_at, updated_at
) VALUES (
	'review-muse', 'session-1', 'project-1', 'muse', 'pr-1',
	'muse-handle', '', '2026-08-03T00:00:00Z', '2026-08-03T00:00:00Z'
)
ON CONFLICT (session_id, harness) DO UPDATE SET
	reviewer_handle_id = excluded.reviewer_handle_id,
	updated_at = excluded.updated_at;`); err != nil {
		t.Fatalf("per-harness upsert after repair: %v", err)
	}

	var reviewSessionTableCount int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'review_session'`).Scan(&reviewSessionTableCount); err != nil {
		t.Fatalf("query review_session table: %v", err)
	}
	if reviewSessionTableCount != 0 {
		t.Fatalf("review_session table count = %d, want 0", reviewSessionTableCount)
	}
}
