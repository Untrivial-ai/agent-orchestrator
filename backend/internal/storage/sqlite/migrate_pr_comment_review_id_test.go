package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrateRecognizesPreLedgeredPRCommentReviewID(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 104)

	// Reproduce a database opened while this migration was still being
	// renumbered: the physical column exists, but neither its canonical ledger
	// entry nor the newly assigned 0105 migration has run.
	if _, err := db.Exec(`
ALTER TABLE pr_comment ADD COLUMN review_id TEXT NOT NULL DEFAULT '';
UPDATE app_settings SET default_session_mode = 'tui' WHERE id = 1;
`); err != nil {
		t.Fatalf("seed pre-ledgered review id schema: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate pre-ledgered review id schema: %v", err)
	}

	var reviewIDColumns, applied106 int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('pr_comment') WHERE name = 'review_id'`,
	).Scan(&reviewIDColumns); err != nil {
		t.Fatalf("read review id column: %v", err)
	}
	if err := db.QueryRow(`
SELECT COUNT(*) FROM goose_db_version
WHERE version_id = 106 AND is_applied = 1`).Scan(&applied106); err != nil {
		t.Fatalf("read migration 106 ledger: %v", err)
	}
	if reviewIDColumns != 1 || applied106 != 1 {
		t.Fatalf("review_id columns = %d, applied 106 entries = %d; want 1, 1", reviewIDColumns, applied106)
	}

	var defaultMode string
	if err := db.QueryRow(`SELECT default_session_mode FROM app_settings WHERE id = 1`).Scan(&defaultMode); err != nil {
		t.Fatalf("read default session mode: %v", err)
	}
	if defaultMode != "chat" {
		t.Fatalf("default session mode = %q, want chat after migration 105", defaultMode)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("repeat migration on repaired schema: %v", err)
	}
}

func TestMigrateRepairsMissingPRCommentReviewIDWhenVersionAlreadyClaimed(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 105)

	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (106, 1)`); err != nil {
		t.Fatalf("seed claimed review-id migration: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate database with missing review-id schema: %v", err)
	}

	var reviewIDColumns int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('pr_comment') WHERE name = 'review_id'`,
	).Scan(&reviewIDColumns); err != nil {
		t.Fatalf("read review id column: %v", err)
	}
	if reviewIDColumns != 1 {
		t.Fatalf("review_id columns = %d, want 1", reviewIDColumns)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("repeat migration on repaired schema: %v", err)
	}
}
