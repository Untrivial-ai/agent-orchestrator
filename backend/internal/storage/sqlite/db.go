// Package sqlite owns SQLite connection setup and goose-managed schema
// migrations. Typed CRUD lives in the store subpackage; this package keeps the
// public Open entrypoint and compatibility aliases for callers.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pressly/goose/v3"

	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"

	// modernc.org/sqlite is the pure-Go (CGO-free) SQLite driver — chosen so the
	// daemon cross-compiles and ships as a static binary with no libsqlite/CGO
	// toolchain dependency, at the cost of some raw throughput vs a C-backed driver.
	_ "modernc.org/sqlite"
)

// Store is the SQLite-backed persistence layer.
type Store = sqlitestore.Store

//go:embed migrations/*.sql
var migrationsFS embed.FS

// pragmas are applied on every connection open. WAL + NORMAL lets readers run
// concurrently with the writer; busy_timeout absorbs brief writer contention;
// foreign_keys enforces the cascades and the CDC triggers' lookups.
const pragmas = "?_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)" +
	"&_pragma=synchronous(NORMAL)"

const readOnlyPragmas = "?mode=ro" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)"

// maxReaders caps the reader pool. WAL allows many concurrent readers.
const maxReaders = 8

// Open opens (creating if absent) the SQLite database under dataDir and returns
// a Store. It uses TWO pools against the same file:
//
//   - a single WRITER connection (writeDB, MaxOpenConns=1): every write goes
//     here, so a write and the CDC triggers' subqueries it fires always see the
//     prior writes on the same connection (read-your-writes). This is required
//     because the pr/pr_checks triggers SELECT from sessions/pr to fill in the
//     event's project_id; a pooled writer could land that read on a connection
//     that hasn't caught up to the commit and read NULL.
//   - a READER pool (readDB, MaxOpenConns=maxReaders): all reads scale across
//     it; WAL readers see the latest committed snapshot.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := "file:" + filepath.Join(dataDir, "ao.db") + pragmas

	writeDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite writer: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	if err := migrate(writeDB); err != nil {
		_ = writeDB.Close()
		return nil, err
	}

	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("open sqlite reader: %w", err)
	}
	readDB.SetMaxOpenConns(maxReaders)
	readDB.SetMaxIdleConns(maxReaders)

	return sqlitestore.NewStore(writeDB, readDB), nil
}

// OpenReadOnly opens an existing SQLite database under dataDir without creating
// the directory, opening a writable connection, or running migrations.
func OpenReadOnly(ctx context.Context, dataDir string) (*Store, error) {
	dsn := "file:" + filepath.Join(dataDir, "ao.db") + readOnlyPragmas

	writeDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only writer: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	if err := writeDB.PingContext(ctx); err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("open sqlite read-only writer: %w", err)
	}

	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("open sqlite read-only reader: %w", err)
	}
	readDB.SetMaxOpenConns(maxReaders)
	readDB.SetMaxIdleConns(maxReaders)
	if err := readDB.PingContext(ctx); err != nil {
		_ = readDB.Close()
		_ = writeDB.Close()
		return nil, fmt.Errorf("open sqlite read-only reader: %w", err)
	}

	return sqlitestore.NewStore(writeDB, readDB), nil
}

// gooseMu serialises calls into goose. goose v3 keeps its baseFS / logger /
// dialect as package-level globals (goose.SetBaseFS, goose.SetLogger,
// goose.SetDialect), so two concurrent Open() calls — uncommon in production
// but normal in -race test runs — race on those writes. The cost of holding the
// mutex is one process-startup migration; readers and writers afterwards never
// touch goose.
var gooseMu sync.Mutex

func migrate(db *sql.DB) error {
	gooseMu.Lock()
	defer gooseMu.Unlock()
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := repairRenumberedChatMigrationHistory(db); err != nil {
		return fmt.Errorf("repair renumbered chat migration history: %w", err)
	}
	if err := prepareBurnedSchemaRepairs(db); err != nil {
		return fmt.Errorf("prepare burned schema repairs: %w", err)
	}
	if err := prepareReviewPerHarnessMigration(db); err != nil {
		return fmt.Errorf("prepare review per-harness migration: %w", err)
	}
	// Builds can advance a database past a migration that is added or
	// renumbered later (notably across fast-moving Nightly releases). Apply
	// those embedded migrations instead of permanently wedging daemon startup
	// on goose's out-of-order-history guard.
	if err := goose.Up(db, "migrations", goose.WithAllowMissing()); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return reconcileSchema(db)
}

func prepareBurnedSchemaRepairs(db *sql.DB) error {
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}
	for _, rc := range schemaRepairs {
		var applied int
		if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = ? ORDER BY id DESC LIMIT 1
), 0)`, rc.version).Scan(&applied); err != nil {
			return err
		}
		if applied == 0 {
			continue
		}
		var tableCount int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, rc.table,
		).Scan(&tableCount); err != nil {
			return err
		}
		if tableCount == 0 {
			continue
		}
		var columnCount int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, rc.table, rc.column,
		).Scan(&columnCount); err != nil {
			return err
		}
		if columnCount > 0 {
			continue
		}
		if _, err := db.Exec(rc.addDDL); err != nil {
			return err
		}
		for _, stmt := range rc.postAdd {
			if _, err := db.Exec(stmt); err != nil {
				return err
			}
		}
	}
	return nil
}

// prepareReviewPerHarnessMigration makes the fresh 0080 rebuild executable on
// field databases that already recorded 0048/0049 as applied without actually
// running their SQL. Once 0080 has applied, it still repairs the physical review
// table if it is missing columns or constraints expected by current queries, but
// does not recreate review_session after the rebuild drops it.
func prepareReviewPerHarnessMigration(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var gooseTable int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return tx.Commit()
	}
	var applied80 int
	if err := tx.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 80 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied80); err != nil {
		return err
	}
	var applied48 int
	if err := tx.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 48 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied48); err != nil {
		return err
	}
	if applied48 == 0 {
		return tx.Commit()
	}
	var reviewTable int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'review'`,
	).Scan(&reviewTable); err != nil {
		return err
	}
	if reviewTable == 0 {
		return tx.Commit()
	}
	var agentSessionColumn int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('review') WHERE name = 'agent_session_id'`,
	).Scan(&agentSessionColumn); err != nil {
		return err
	}
	if agentSessionColumn == 0 {
		if _, err := tx.Exec(`ALTER TABLE review ADD COLUMN agent_session_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if applied80 != 0 {
		if err := tx.Commit(); err != nil {
			return err
		}
		return repairReviewPerHarnessShape(db)
	}
	var reviewSessionTable int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'review_session'`,
	).Scan(&reviewSessionTable); err != nil {
		return err
	}
	if reviewSessionTable == 0 {
		if _, err := tx.Exec(`
CREATE TABLE review_session (
    session_id         TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id         TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    harness            TEXT NOT NULL,
    reviewer_handle_id TEXT NOT NULL DEFAULT '',
    agent_session_id   TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, harness)
)`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func repairReviewPerHarnessShape(db *sql.DB) error {
	ok, err := reviewHasSessionHarnessUnique(db)
	if err != nil || ok {
		return err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() { _, _ = db.Exec(`PRAGMA foreign_keys=ON`) }()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
CREATE TABLE review_repair (
    id                 TEXT PRIMARY KEY,
    session_id         TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    project_id         TEXT NOT NULL REFERENCES projects (id),
    harness            TEXT NOT NULL,
    pr_url             TEXT NOT NULL DEFAULT '',
    reviewer_handle_id TEXT NOT NULL DEFAULT '',
    agent_session_id   TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMP NOT NULL,
    updated_at         TIMESTAMP NOT NULL,
    UNIQUE(session_id, harness)
)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT INTO review_repair (
    id, session_id, project_id, harness, pr_url, reviewer_handle_id,
    agent_session_id, created_at, updated_at
)
SELECT
    id, session_id, project_id, harness, pr_url, reviewer_handle_id,
    agent_session_id, created_at, updated_at
FROM review`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE review`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE review_repair RENAME TO review`); err != nil {
		return err
	}
	return tx.Commit()
}

func reviewHasSessionHarnessUnique(db *sql.DB) (bool, error) {
	rows, err := db.Query(`PRAGMA index_list('review')`)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	uniqueIndexes := []string{}
	for rows.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return false, err
		}
		if unique == 0 {
			continue
		}
		uniqueIndexes = append(uniqueIndexes, name)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, name := range uniqueIndexes {
		if indexColumnsMatch(db, name, []string{"session_id", "harness"}) {
			return true, nil
		}
	}
	return false, nil
}

func indexColumnsMatch(db *sql.DB, indexName string, want []string) bool {
	rows, err := db.Query(`PRAGMA index_info(` + quoteSQLiteIdent(indexName) + `)`)
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	got := make([]string, 0, len(want))
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			return false
		}
		got = append(got, name)
	}
	if rows.Err() != nil || len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func quoteSQLiteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// repairRenumberedChatMigrationHistory preserves databases opened by this
// feature branch before its Chat migrations moved from 0052-0065 to 0066-0079.
// The files are byte-for-byte identical after the rename, so recording the new
// numbers is safer than replaying their ALTER/CREATE statements over an already
// upgraded schema. Version 0052 now belongs to model usage on main; if that
// physical schema is absent, release the burned ledger entry so goose can apply
// the real 0052 migration below.
func repairRenumberedChatMigrationHistory(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var gooseTable, chatColumn, conversationsTable int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return tx.Commit()
	}
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'session_mode'`,
	).Scan(&chatColumn); err != nil {
		return err
	}
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'conversations'`,
	).Scan(&conversationsTable); err != nil {
		return err
	}
	if chatColumn == 0 || conversationsTable == 0 {
		return tx.Commit()
	}

	legacyApplied := false
	for oldVersion := int64(52); oldVersion <= 65; oldVersion++ {
		var applied int
		if err := tx.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = ? ORDER BY id DESC LIMIT 1
), 0)`, oldVersion).Scan(&applied); err != nil {
			return err
		}
		if applied == 0 {
			continue
		}
		legacyApplied = true
		newVersion := oldVersion + 14
		var alreadyMapped int
		if err := tx.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = ? ORDER BY id DESC LIMIT 1
), 0)`, newVersion).Scan(&alreadyMapped); err != nil {
			return err
		}
		if alreadyMapped == 0 {
			if _, err := tx.Exec(
				`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`,
				newVersion,
			); err != nil {
				return err
			}
		}
	}
	if !legacyApplied {
		return tx.Commit()
	}

	var modelUsageTable int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'model_usage_events'`,
	).Scan(&modelUsageTable); err != nil {
		return err
	}
	if modelUsageTable == 0 {
		if _, err := tx.Exec(`DELETE FROM goose_db_version WHERE version_id = 52`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// schemaRepairs lists the column-level effects of migrations that real
// installs are known to skip. Issue #3475/#3476: profiles exist whose
// goose_db_version already records versions 40 through 46 (written by a
// foreign build), so goose silently skips the real migrations carrying those
// numbers and the generated queries then fail with "no such column" — every
// session list 500s while /healthz stays green. A versioned repair migration
// cannot fix this class, because a burned version number is exactly what
// caused it; instead the physical schema is verified on every startup.
//
// Each entry keys on one column. postAdd statements replay the rest of the
// skipped migration's effects (backfills, index swaps) and run ONLY when the
// column was just added, so healthy databases — where those statements would
// clobber live data — are never touched.
//
// Any new migration numbered up to 0046 whose schema the generated queries
// depend on MUST add an entry here, or the burned field profiles skip it and
// regress to the 500s this exists to prevent.
var schemaRepairs = []struct {
	version int64
	table   string
	column  string
	addDDL  string
	postAdd []string
}{
	// 0040_add_session_diff_base.sql
	{version: 40, table: "sessions", column: "diff_base_sha",
		addDDL: `ALTER TABLE sessions ADD COLUMN diff_base_sha TEXT NOT NULL DEFAULT ''`},
	{version: 40, table: "sessions", column: "diff_base_ref",
		addDDL: `ALTER TABLE sessions ADD COLUMN diff_base_ref TEXT NOT NULL DEFAULT ''`},
	// 0041_notification_resolution.sql
	{version: 41, table: "notifications", column: "resolved_at",
		addDDL: `ALTER TABLE notifications ADD COLUMN resolved_at TIMESTAMP`,
		postAdd: []string{
			`UPDATE notifications SET resolved_at = created_at WHERE status = 'read'`,
			`DROP INDEX IF EXISTS idx_notifications_unread_dedupe`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_open_dedupe
    ON notifications(session_id, type, pr_url)
    WHERE status = 'unread' OR resolved_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_notifications_unresolved
    ON notifications(resolved_at, created_at DESC, id DESC)`,
		}},
	// 0042_review_run_unique_per_harness.sql
	{version: 42, table: "sessions", column: "reviewer_harness",
		addDDL: `ALTER TABLE sessions ADD COLUMN reviewer_harness TEXT NOT NULL DEFAULT ''`,
		postAdd: []string{
			`DROP INDEX IF EXISTS idx_review_run_session_pr_sha`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_review_run_session_pr_sha_harness
    ON review_run (session_id, pr_url, target_sha, harness)
    WHERE target_sha != ''
        AND status NOT IN ('failed', 'cancelled')
        AND (status = 'running' OR verdict NOT IN ('', 'changes_requested'))`,
		}},
	// 0043_add_session_pinned.sql. The trigger replay hangs off pinned_at, the
	// second of the two columns: it references both, and SQLite resolves a
	// trigger body at CREATE time, so it cannot run until both exist.
	{version: 43, table: "sessions", column: "is_pinned",
		addDDL: `ALTER TABLE sessions ADD COLUMN is_pinned BOOLEAN NOT NULL DEFAULT 0`},
	{version: 43, table: "sessions", column: "pinned_at",
		addDDL: `ALTER TABLE sessions ADD COLUMN pinned_at DATETIME`,
		postAdd: []string{
			`DROP TRIGGER IF EXISTS sessions_cdc_update`,
			`CREATE TRIGGER sessions_cdc_update
AFTER UPDATE ON sessions
WHEN OLD.activity_state <> NEW.activity_state
    OR OLD.is_terminated <> NEW.is_terminated
    OR (OLD.first_signal_at IS NULL AND NEW.first_signal_at IS NOT NULL)
    OR OLD.preview_url <> NEW.preview_url
    OR OLD.preview_revision <> NEW.preview_revision
    OR OLD.display_name <> NEW.display_name
    OR OLD.terminate_on_pr_merge <> NEW.terminate_on_pr_merge
    OR OLD.is_pinned <> NEW.is_pinned
    OR OLD.pinned_at <> NEW.pinned_at
    OR (OLD.pinned_at IS NULL AND NEW.pinned_at IS NOT NULL)
    OR (OLD.pinned_at IS NOT NULL AND NEW.pinned_at IS NULL)
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_updated',
        json_object(
            'id', NEW.id,
            'activity', NEW.activity_state,
            'isTerminated', json(CASE WHEN NEW.is_terminated THEN 'true' ELSE 'false' END),
            'terminateOnPrMerge', json(CASE WHEN NEW.terminate_on_pr_merge THEN 'true' ELSE 'false' END),
            'previewUrl', NEW.preview_url,
            'previewRevision', NEW.preview_revision,
            'isPinned', json(CASE WHEN NEW.is_pinned THEN 'true' ELSE 'false' END)
        ),
        NEW.updated_at);
END`,
		}},
	// 0081_add_session_model.sql
	{version: 81, table: "sessions", column: "model",
		addDDL: `ALTER TABLE sessions ADD COLUMN model TEXT NOT NULL DEFAULT ''`},
	// A pre-renumbered chat-mode branch created conversations before the
	// current_session_id controller binding existed, then later builds recorded
	// 0052 as applied. Generated chat queries require the column on startup.
	{version: 66, table: "conversations", column: "current_session_id",
		addDDL: `ALTER TABLE conversations ADD COLUMN current_session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL`,
		postAdd: []string{
			`UPDATE conversations SET current_session_id = session_id WHERE current_session_id IS NULL AND session_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS idx_conversations_current_session ON conversations(current_session_id)
    WHERE current_session_id IS NOT NULL`,
		}},
}

// reconcileSchema verifies that the columns in schemaRepairs physically exist
// and replays the skipped migration's effects for any that are missing. It is
// idempotent: a healthy database (migrations applied normally, or one already
// repaired by hand or a previous startup) is left untouched. Failures surface
// as a specific, actionable startup error instead of an opaque INTERNAL_ERROR
// on the first session list.
func reconcileSchema(db *sql.DB) error {
	for _, rc := range schemaRepairs {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, rc.table, rc.column,
		).Scan(&count); err != nil {
			return fmt.Errorf("schema verification: inspect %s.%s: %w", rc.table, rc.column, err)
		}
		if count > 0 {
			continue
		}
		if _, err := db.Exec(rc.addDDL); err != nil {
			return fmt.Errorf(
				"schema repair: %s.%s is missing (a burned goose version skipped the migration that adds it, see #3475) and could not be added: %w",
				rc.table, rc.column, err,
			)
		}
		for _, stmt := range rc.postAdd {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("schema repair: replay skipped migration effects for %s.%s: %w", rc.table, rc.column, err)
			}
		}
	}
	if err := reconcileHarnessConstraint(db); err != nil {
		return err
	}
	return nil
}

const (
	sessionsHarnessCheckWithoutMuse   = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'fake'))`
	sessionsHarnessCheckWithMuse      = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'fake'))`
	sessionsHarnessCheckWithoutMuseQM = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'qm', 'fake'))`
	sessionsHarnessCheckWithMuseQM    = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'qm', 'fake'))`
)

func reconcileHarnessConstraint(db *sql.DB) error {
	var schema string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`,
	).Scan(&schema); err != nil {
		return fmt.Errorf("schema verification: inspect sessions harness constraint: %w", err)
	}
	if strings.Contains(schema, "'muse'") {
		return nil
	}
	if _, err := db.Exec(`PRAGMA writable_schema = ON`); err != nil {
		return fmt.Errorf("schema repair: enable writable_schema for sessions harness constraint: %w", err)
	}
	for _, replacement := range []struct {
		old string
		new string
	}{
		{sessionsHarnessCheckWithoutMuse, sessionsHarnessCheckWithMuse},
		{sessionsHarnessCheckWithoutMuseQM, sessionsHarnessCheckWithMuseQM},
	} {
		if _, err := db.Exec(
			`UPDATE sqlite_master
SET sql = replace(sql, ?, ?)
WHERE type = 'table' AND name = 'sessions'`,
			replacement.old,
			replacement.new,
		); err != nil {
			return fmt.Errorf("schema repair: widen sessions harness constraint for Muse: %w", err)
		}
	}
	if _, err := db.Exec(`PRAGMA writable_schema = RESET`); err != nil {
		return fmt.Errorf("schema repair: reparse sessions harness constraint: %w", err)
	}
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`,
	).Scan(&schema); err != nil {
		return fmt.Errorf("schema verification: inspect repaired sessions harness constraint: %w", err)
	}
	if !strings.Contains(schema, "'muse'") {
		return fmt.Errorf("schema repair: sessions harness constraint is missing Muse and did not match known pre-Muse schema")
	}
	return nil
}
