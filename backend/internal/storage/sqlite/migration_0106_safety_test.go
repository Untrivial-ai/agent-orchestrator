package sqlite

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"

	_ "modernc.org/sqlite"
)

// Test0106_MigrationPassesClean verifies that migration 0106 succeeds
// when run_reviews has no duplicate run_id values.
func Test0106_MigrationPassesClean(t *testing.T) {
	db := openRawDB(t)
	defer db.Close()

	// Apply migrations 0001-0105.
	gooseReset(t)
	if err := goose.UpTo(db, "migrations", 105); err != nil {
		t.Fatalf("migrate to 105: %v", err)
	}

	// Insert clean test data (no duplicates).
	// Must satisfy FK: projects → development_plans → development_stages → development_tasks → task_runs → run_reviews.
	if _, err := db.Exec(`INSERT INTO projects (id, path, display_name, registered_at) VALUES ('proj-1', '/tmp/p', 'P', '2026-01-01')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO development_plans (id, project_id, title, objective, requirements, implementation_summary, status, created_at, confirmed_at, completed_at) VALUES ('plan-1', 'proj-1', 'P', '', '', '', 'draft', '2026-01-01', NULL, NULL)`); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO development_stages (id, plan_id, sequence, title, status, created_at, started_at, completed_at) VALUES ('stg-1', 'plan-1', 1, 'S', 'pending', '2026-01-01', NULL, NULL)`); err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO development_tasks (id, stage_id, agent_role_id, provider_id, provider_model_id, sequence, title, description, status, created_at, started_at, completed_at) VALUES ('task-1', 'stg-1', '', '', '', 1, 'T', '', 'review', '2026-01-01', NULL, NULL)`); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_runs (id, task_id, attempt, session_id, agent_role_id, provider_id, provider_model_id, provider_display_name, provider_model_name, executor_type, status, result_summary, error_message, created_at) VALUES ('run-1', 'task-1', 1, '', '', '', '', '', '', '', 'succeeded', '', '', '2026-01-01')`); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO run_reviews (id, run_id, source, status, summary, issues, created_at, completed_at) VALUES ('rev-1', 'run-1', 'ai', 'pending', '', '', '2026-01-01', NULL)`); err != nil {
		t.Fatalf("seed review: %v", err)
	}

	// Apply migration 0106 — should succeed.
	gooseReset(t)
	if err := goose.UpTo(db, "migrations", 106); err != nil {
		t.Fatalf("migrate to 106: %v", err)
	}

	// Verify: columns exist and UNIQUE index exists.
	assertColumnExists(t, db, "task_runs", "previous_run_id")
	assertColumnExists(t, db, "task_runs", "retry_mode")
	assertUniqueIndexExists(t, db, "idx_run_reviews_run_unique")

	// Verify: duplicate insert now fails.
	_, err := db.Exec(`INSERT INTO run_reviews (id, run_id, source, status, summary, issues, created_at, completed_at) VALUES ('rev-2', 'run-1', 'human', 'pending', '', '', '2026-01-01', NULL)`)
	if err == nil {
		t.Fatal("expected UNIQUE constraint violation for duplicate run_id, got nil")
	}
}

// Test0106_MigrationFailsOnDuplicateRunID verifies that migration 0106
// fails safely when run_reviews already contains duplicate run_id values.
// The UNIQUE INDEX creation must fail, and no partial migration state
// must remain.
func Test0106_MigrationFailsOnDuplicateRunID(t *testing.T) {
	db := openRawDB(t)
	defer db.Close()

	// Apply migrations 0001-0105.
	gooseReset(t)
	if err := goose.UpTo(db, "migrations", 105); err != nil {
		t.Fatalf("migrate to 105: %v", err)
	}

	// Seed prerequisite data.
	if _, err := db.Exec(`INSERT INTO projects (id, path, display_name, registered_at) VALUES ('proj-1', '/tmp/p', 'P', '2026-01-01')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO development_plans (id, project_id, title, objective, requirements, implementation_summary, status, created_at, confirmed_at, completed_at) VALUES ('plan-1', 'proj-1', 'P', '', '', '', 'draft', '2026-01-01', NULL, NULL)`); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO development_stages (id, plan_id, sequence, title, status, created_at, started_at, completed_at) VALUES ('stg-1', 'plan-1', 1, 'S', 'pending', '2026-01-01', NULL, NULL)`); err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO development_tasks (id, stage_id, agent_role_id, provider_id, provider_model_id, sequence, title, description, status, created_at, started_at, completed_at) VALUES ('task-1', 'stg-1', '', '', '', 1, 'T', '', 'review', '2026-01-01', NULL, NULL)`); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_runs (id, task_id, attempt, session_id, agent_role_id, provider_id, provider_model_id, provider_display_name, provider_model_name, executor_type, status, result_summary, error_message, created_at) VALUES ('run-1', 'task-1', 1, '', '', '', '', '', '', '', 'succeeded', '', '', '2026-01-01')`); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// Insert TWO reviews with the SAME run_id (duplicate).
	if _, err := db.Exec(`INSERT INTO run_reviews (id, run_id, source, status, summary, issues, created_at, completed_at) VALUES ('rev-1', 'run-1', 'ai', 'pending', '', '', '2026-01-01', NULL)`); err != nil {
		t.Fatalf("seed review 1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO run_reviews (id, run_id, source, status, summary, issues, created_at, completed_at) VALUES ('rev-2', 'run-1', 'human', 'pending', '', '', '2026-01-01', NULL)`); err != nil {
		t.Fatalf("seed review 2: %v", err)
	}

	// Apply migration 0106 — must FAIL on UNIQUE INDEX creation.
	gooseReset(t)
	err := goose.UpTo(db, "migrations", 106)
	if err == nil {
		t.Fatal("expected migration 0106 to fail due to duplicate run_id, but it succeeded")
	}
	t.Logf("migration 0106 failed as expected: %v", err)

	// Verify: no partial migration state.
	// After a failed goose migration (goose runs in a transaction),
	// the schema should be in the pre-0106 state.
	assertColumnNotExists(t, db, "task_runs", "previous_run_id")
	assertColumnNotExists(t, db, "task_runs", "retry_mode")
	assertIndexNotExists(t, db, "idx_run_reviews_run_unique")
}

// openRawDB creates a temporary SQLite database for migration testing.
func openRawDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// gooseReset resets goose state for a fresh UpTo call.
func gooseReset(t *testing.T) {
	t.Helper()
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
}

func assertColumnExists(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == column {
			return
		}
	}
	t.Errorf("column %s.%s does not exist", table, column)
}

func assertColumnNotExists(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == column {
			t.Errorf("column %s.%s should not exist but does", table, column)
			return
		}
	}
}

func assertUniqueIndexExists(t *testing.T, db *sql.DB, indexName string) {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='index' AND name=?", indexName)
	if err != nil {
		t.Fatalf("sqlite_master query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Errorf("index %s does not exist", indexName)
	}
}

func assertIndexNotExists(t *testing.T, db *sql.DB, indexName string) {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='index' AND name=?", indexName)
	if err != nil {
		t.Fatalf("sqlite_master query: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Errorf("index %s should not exist but does", indexName)
	}
}
