// Package sqlite owns SQLite connection setup and goose-managed schema
// migrations. Typed CRUD lives in the store subpackage; this package keeps the
// public Open entrypoint and compatibility aliases for callers.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"net/url"
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

// databaseURI preserves filesystem characters instead of interpreting them as
// SQLite URI parameters/fragments. Both pools and read-only opens must address
// the same literal file, including percent signs and Windows drive paths.
func databaseURI(dataDir string) string {
	file := url.URL{Path: filepath.Join(dataDir, "ao.db")}
	return "file:" + file.EscapedPath()
}

// An older raw file: URI may have stored this directory's data elsewhere. Do
// not silently replace that database with an empty one after fixing the URI.
func checkLegacyDatabasePath(dataDir string) error {
	intended := filepath.Join(dataDir, "ao.db")
	if _, err := os.Stat(intended); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect intended database: %w", err)
	}
	raw := intended
	if end := strings.IndexAny(raw, "?#"); end >= 0 {
		raw = raw[:end]
	}
	var decoded strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '%' && i+2 < len(raw) {
			if value, err := url.PathUnescape(raw[i : i+3]); err == nil {
				if value[0] == 0 {
					break
				}
				decoded.WriteString(value)
				i += 2
				continue
			}
		}
		decoded.WriteByte(raw[i])
	}
	legacy := decoded.String()
	if legacy == intended {
		return nil
	}
	info, err := os.Stat(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy database path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	file, err := os.Open(legacy)
	if err != nil {
		return fmt.Errorf("inspect legacy database: %w", err)
	}
	var header [16]byte
	n, readErr := io.ReadFull(file, header[:])
	_ = file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return fmt.Errorf("inspect legacy database: %w", readErr)
	}
	if n == len(header) && string(header[:]) == "SQLite format 3\x00" {
		return fmt.Errorf("legacy SQLite path parsing stored data at %q; refusing to create an empty database at %q: stop AO and recover the legacy database explicitly before retrying", legacy, intended)
	}
	return nil
}

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
	if err := checkLegacyDatabasePath(dataDir); err != nil {
		return nil, err
	}
	dsn := databaseURI(dataDir) + pragmas

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
	dsn := databaseURI(dataDir) + readOnlyPragmas

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
	if err := repairRenumberedAgentInstallJobsMigrationHistory(db); err != nil {
		return fmt.Errorf("repair renumbered agent-install-jobs migration history: %w", err)
	}
	if err := repairRenumberedChatMigrationHistory(db); err != nil {
		return fmt.Errorf("repair renumbered chat migration history: %w", err)
	}
	if err := repairRenumberedUsageCostMigrationHistory(db); err != nil {
		return fmt.Errorf("repair renumbered usage-cost migration history: %w", err)
	}
	if err := prepareCancelledConversationTurnsMigration(db); err != nil {
		return fmt.Errorf("prepare cancelled conversation-turn migration: %w", err)
	}
	if err := repairRenumberedCodexProfileMigrationHistory(db); err != nil {
		return fmt.Errorf("repair renumbered Codex profile migration history: %w", err)
	}
	if err := repairRenumberedAgentInventoryMigrationHistory(db); err != nil {
		return fmt.Errorf("repair renumbered agent-inventory migration history: %w", err)
	}
	if err := prepareUsageCostMigration(db); err != nil {
		return fmt.Errorf("prepare usage cost migration: %w", err)
	}
	if err := prepareAutoInjectReviewMigration(db); err != nil {
		return fmt.Errorf("prepare auto-inject-review migration: %w", err)
	}
	if err := preparePRCommentReviewIDMigration(db); err != nil {
		return fmt.Errorf("prepare PR comment review-id migration: %w", err)
	}
	if err := repairRenumberedAgentSwitchMigrationHistory(db); err != nil {
		return fmt.Errorf("repair renumbered agent-switch migration history: %w", err)
	}
	if err := prepareBurnedSchemaRepairs(db); err != nil {
		return fmt.Errorf("prepare burned schema repairs: %w", err)
	}
	if err := prepareReviewPerHarnessMigration(db); err != nil {
		return fmt.Errorf("prepare review per-harness migration: %w", err)
	}
	if err := prepareBrowserVerifierMigration(db); err != nil {
		return fmt.Errorf("prepare browser verifier migration: %w", err)
	}
	if err := prepareQueuedTurnPromotionMigration(db); err != nil {
		return fmt.Errorf("prepare queued-turn promotion migration: %w", err)
	}
	if err := prepareSessionReviewerAgentConfigMigration(db); err != nil {
		return fmt.Errorf("prepare session reviewer agent-config migration: %w", err)
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

// repairRenumberedAgentInstallJobsMigrationHistory preserves development
// databases opened while the harness-installer branch owned versions 0119 or
// 0120. Main later assigned those versions to idempotent data repairs, so move
// the exact installer-table shape to its canonical 0123 ledger entry and let
// Goose replay the real 0119 and 0120 migrations.
func repairRenumberedAgentInstallJobsMigrationHistory(db *sql.DB) error {
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}

	var installJobShape int
	if err := db.QueryRow(`
SELECT COUNT(*) FROM pragma_table_info('agent_install_jobs')
WHERE name IN (
    'target', 'status', 'method', 'command', 'expected_destination',
    'output', 'error', 'started_at', 'finished_at', 'updated_at'
)`).Scan(&installJobShape); err != nil {
		return err
	}
	if installJobShape != 10 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var canonicalApplied int
	if err := tx.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 123 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&canonicalApplied); err != nil {
		return err
	}
	if canonicalApplied != 0 {
		return tx.Commit()
	}
	if _, err := tx.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (123, 1)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM goose_db_version WHERE version_id IN (119, 120)`); err != nil {
		return err
	}
	return tx.Commit()
}

// prepareCancelledConversationTurnsMigration repairs the staging table left
// by an interrupted 0118 table rebuild. The migration runs without a wrapping
// transaction because it temporarily disables foreign keys, so a process exit
// can leave conversation_turns_next behind and make every retry fail at CREATE
// TABLE. When the source table remains it is still authoritative and the stale
// copy is discarded. If the interruption happened after the source was
// dropped, promote the staging table back to the source name and let 0118
// perform the complete rebuild again.
func prepareCancelledConversationTurnsMigration(db *sql.DB) error {
	var sourceTable, stagingTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'conversation_turns'`,
	).Scan(&sourceTable); err != nil {
		return err
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'conversation_turns_next'`,
	).Scan(&stagingTable); err != nil {
		return err
	}
	if stagingTable == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if sourceTable != 0 {
		if _, err := tx.Exec(`DROP TABLE conversation_turns_next`); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`ALTER TABLE conversation_turns_next RENAME TO conversation_turns`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// prepareUsageCostMigration releases a falsely-applied usage migration before
// 0101 adds columns to its tables. Some field profiles recorded versions
// 44-53 from a foreign build without ever creating the real 0052 usage schema.
func prepareUsageCostMigration(db *sql.DB) error {
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil || gooseTable == 0 {
		return err
	}
	var usageTables int
	if err := db.QueryRow(`
SELECT COUNT(*) FROM sqlite_master
WHERE type = 'table' AND name IN ('usage_bindings', 'usage_sources', 'model_usage_events')`).Scan(&usageTables); err != nil {
		return err
	}
	if usageTables == 3 {
		return nil
	}
	_, err := db.Exec(`DELETE FROM goose_db_version WHERE version_id = 52`)
	return err
}

// prepareAutoInjectReviewMigration preserves development databases whose
// physical review-injection schema exists without the corresponding goose
// ledger entry. Without this repair, goose replays 0084 and startup stops on
// the first duplicate column. Fresh schemas still run the migration normally;
// only the complete four-table shape is accepted as already applied.
func prepareAutoInjectReviewMigration(db *sql.DB) error {
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}

	var applied int
	if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 84 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return nil
	}

	for _, table := range []string{"sessions", "review_run", "pr_reviews", "pr_comment"} {
		var present int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'auto_inject_review'`, table,
		).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			return nil
		}
	}

	_, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (84, 1)`)
	return err
}

// preparePRCommentReviewIDMigration preserves development databases that
// acquired pr_comment.review_id while this migration was being renumbered but
// do not have its canonical 0106 ledger entry. Recording the physically present
// effect prevents goose from replaying the ALTER TABLE over the existing column.
func preparePRCommentReviewIDMigration(db *sql.DB) error {
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}

	var applied, reviewIDColumn int
	if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 106 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return nil
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('pr_comment') WHERE name = 'review_id'`,
	).Scan(&reviewIDColumn); err != nil {
		return err
	}
	if reviewIDColumn == 0 {
		return nil
	}
	_, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (106, 1)`)
	return err
}

// prepareBrowserVerifierMigration preserves development databases that ran an
// earlier version of this branch where the verifier was mistakenly added to
// shipped migration 0048. The restored 0048 no longer owns the column, so mark
// the new 0081 migration applied when its exact schema effect already exists;
// otherwise goose applies 0081 normally.
func prepareBrowserVerifierMigration(db *sql.DB) error {
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}
	var applied int
	if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 81 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return nil
	}
	var verifierColumn int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'browser_capability_verifier'`,
	).Scan(&verifierColumn); err != nil {
		return err
	}
	if verifierColumn == 0 {
		return nil
	}
	_, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (81, 1)`)
	return err
}

// prepareQueuedTurnPromotionMigration preserves development databases that ran
// the promotion migration while it still used version 86 or 88. Upstream now
// owns both numbers, so record the promotion schema as version 89. If version 88
// came from the old promotion migration, remove that ledger entry so goose can
// apply upstream's auto-inject-CI migration at its canonical version.
// prepareSessionReviewerAgentConfigMigration preserves databases that
// already have sessions.reviewer_agent_config from an in-flight local run but
// do not yet record the canonical 0118 migration version. Recording that
// physically present effect prevents goose from replaying the ALTER TABLE over
// the existing column on the next startup.
func prepareSessionReviewerAgentConfigMigration(db *sql.DB) error {
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}

	var applied, reviewerConfigColumn int
	if err := db.QueryRow(`
SELECT
    COALESCE((
        SELECT is_applied FROM goose_db_version
        WHERE version_id = 118 ORDER BY id DESC LIMIT 1
    ), 0),
    (SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'reviewer_agent_config')
`).Scan(&applied, &reviewerConfigColumn); err != nil {
		return err
	}
	if applied != 0 || reviewerConfigColumn == 0 {
		return nil
	}

	_, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (118, 1)`)
	return err
}

func prepareQueuedTurnPromotionMigration(db *sql.DB) error {
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

	var promotionColumns int
	if err := tx.QueryRow(`
SELECT COUNT(*) FROM pragma_table_info('conversation_turns')
WHERE name IN ('promotion_started_at', 'promoted_to_turn_id')`).Scan(&promotionColumns); err != nil {
		return err
	}
	if promotionColumns != 2 {
		return tx.Commit()
	}

	var applied89 int
	if err := tx.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 89 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied89); err != nil {
		return err
	}
	if applied89 == 0 {
		if _, err := tx.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (89, 1)`); err != nil {
			return err
		}
	}

	var applied88, autoInjectCIColumns int
	if err := tx.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 88 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied88); err != nil {
		return err
	}
	if applied88 != 0 {
		if err := tx.QueryRow(`
SELECT (SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'auto_inject_ci')
     + (SELECT COUNT(*) FROM pragma_table_info('pr') WHERE name = 'auto_inject_ci')`).Scan(&autoInjectCIColumns); err != nil {
			return err
		}
		if autoInjectCIColumns == 0 {
			if _, err := tx.Exec(`DELETE FROM goose_db_version WHERE version_id = 88`); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
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
	// Probe on a read-only query first. A fresh database (the common import
	// path and the cli import test) has no goose_db_version table yet, so there
	// is nothing to repair; entering a write transaction would needlessly
	// create WAL/SHM files and is the exact I/O surface that surfaced as a
	// transient SQLITE_IOERR_WRITE on macOS CI runners.
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}

	var chatColumn, conversationsTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'session_mode'`,
	).Scan(&chatColumn); err != nil {
		return err
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'conversations'`,
	).Scan(&conversationsTable); err != nil {
		return err
	}
	if chatColumn == 0 || conversationsTable == 0 {
		return nil
	}

	// Only enter a write transaction when there is actual repair work to do.
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

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

// repairRenumberedUsageCostMigrationHistory preserves development databases
// opened while this feature's migrations occupied 0109-0112 and, later,
// 0110-0113. Main now owns 0109-0112, while the same usage migrations live at
// 0113-0116. Record each physically present usage effect at its canonical
// number, then release only the collided main numbers whose physical effects
// are absent.
func repairRenumberedUsageCostMigrationHistory(db *sql.DB) error {
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}

	// provider_hint and the durable event pricing columns arrived together in
	// the first usage-cost migration. Requiring the complete retained shape
	// avoids rewriting unrelated migration history on databases without it.
	var costShape int
	if err := db.QueryRow(`
SELECT (SELECT COUNT(*) FROM pragma_table_info('usage_bindings') WHERE name = 'provider_hint')
     + (SELECT COUNT(*) FROM pragma_table_info('model_usage_events') WHERE name = 'billing_provider_id')
     + (SELECT COUNT(*) FROM pragma_table_info('model_usage_events') WHERE name = 'estimated_cost_nanos')
     + (SELECT COUNT(*) FROM pragma_table_info('model_usage_events') WHERE name = 'pricing_version')`).Scan(&costShape); err != nil {
		return err
	}
	if costShape != 4 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	markApplied := func(version int64) error {
		var applied int
		if err := tx.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = ? ORDER BY id DESC LIMIT 1
), 0)`, version).Scan(&applied); err != nil {
			return err
		}
		if applied != 0 {
			return nil
		}
		_, err := tx.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`,
			version,
		)
		return err
	}

	if err := markApplied(113); err != nil {
		return err
	}
	var canonicalIndex int
	if err := tx.QueryRow(`
SELECT COUNT(*) FROM sqlite_master
WHERE type = 'index' AND name = 'idx_model_usage_events_canonical_cost_candidates'`).Scan(&canonicalIndex); err != nil {
		return err
	}
	if canonicalIndex != 0 {
		if err := markApplied(114); err != nil {
			return err
		}
	}

	var measurementShape int
	if err := tx.QueryRow(`
SELECT COUNT(*) FROM pragma_table_info('model_usage_events')
WHERE name IN ('usage_measurement_kind', 'provider_usage_json')`).Scan(&measurementShape); err != nil {
		return err
	}
	if measurementShape == 2 {
		if err := markApplied(115); err != nil {
			return err
		}
	}

	var providerSource int
	if err := tx.QueryRow(`
SELECT COUNT(*) FROM pragma_table_info('model_usage_events')
WHERE name = 'billing_provider_source'`).Scan(&providerSource); err != nil {
		return err
	}
	if providerSource != 0 {
		if err := markApplied(116); err != nil {
			return err
		}
	}

	var latestPromptShape, branchShape, prTriggerShape, cloudShape int
	if err := tx.QueryRow(`
SELECT COUNT(*) FROM pragma_table_info('sessions')
WHERE name = 'latest_user_prompt_at'`).Scan(&latestPromptShape); err != nil {
		return err
	}
	if err := tx.QueryRow(`
SELECT COUNT(*) FROM pragma_table_info('conversation_branches')
WHERE name IN ('strategy', 'replay_cutoff_sequence', 'replay_truncated', 'provider_scope_id')`).Scan(&branchShape); err != nil {
		return err
	}
	if err := tx.QueryRow(`
SELECT COUNT(*) FROM sqlite_master
WHERE type = 'trigger' AND name = 'pr_cdc_update'
  AND instr(COALESCE(sql, ''), 'auto_inject_ci') > 0`).Scan(&prTriggerShape); err != nil {
		return err
	}
	if err := tx.QueryRow(`
SELECT COUNT(*) FROM pragma_table_info('app_settings')
WHERE name = 'cloud_offering'`).Scan(&cloudShape); err != nil {
		return err
	}
	for _, migration := range []struct {
		version int64
		present bool
	}{
		{version: 109, present: latestPromptShape != 0},
		{version: 110, present: branchShape == 4},
		{version: 111, present: prTriggerShape != 0},
		{version: 112, present: cloudShape != 0},
	} {
		if migration.present {
			continue
		}
		if _, err := tx.Exec(
			`DELETE FROM goose_db_version WHERE version_id = ?`, migration.version,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// repairRenumberedCodexProfileMigrationHistory preserves development
// databases opened before the stacked Codex-profile migrations were moved out
// of version numbers subsequently claimed by main. Early Phase 1 builds used
// 0117 to drop agent_inventory_cache; 0117 now enables Kimi usage. Release the
// collided ledger entry only when the canonical physical effect is absent so
// goose can apply the real migration. The drop remains safe at canonical 0122.
func repairRenumberedCodexProfileMigrationHistory(db *sql.DB) error {
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}

	var applied117 int
	if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 117 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied117); err != nil {
		return err
	}
	if applied117 == 0 {
		return nil
	}

	var kimiUsageShape int
	if err := db.QueryRow(`
SELECT (SELECT COUNT(*) FROM sqlite_master
        WHERE type = 'table' AND name = 'usage_bindings'
          AND instr(COALESCE(sql, ''), '''kimi''') > 0)
     + (SELECT COUNT(*) FROM sqlite_master
        WHERE type = 'table' AND name = 'usage_sources'
          AND instr(COALESCE(sql, ''), '''kimi_wire''') > 0)`).Scan(&kimiUsageShape); err != nil {
		return err
	}
	if kimiUsageShape == 2 {
		return nil
	}

	_, err := db.Exec(`DELETE FROM goose_db_version WHERE version_id = 117`)
	return err
}

// repairRenumberedAgentInventoryMigrationHistory preserves development
// databases opened while this branch used 0119, 0120, or 0121 to drop
// agent_inventory_cache. Main now owns those versions for completed-plan
// finalization, activity timestamp normalization, and reviewer configuration;
// the identical cache drop lives at 0122. An absent cache table plus a collided
// ledger entry identifies the branch history: mark the physical drop at 0122,
// then release the newest collided version so goose can apply main's migration
// with WithAllowMissing.
func repairRenumberedAgentInventoryMigrationHistory(db *sql.DB) error {
	var gooseTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}

	var applied119 int
	if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 119 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied119); err != nil {
		return err
	}
	var applied120 int
	if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 120 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied120); err != nil {
		return err
	}
	var applied121 int
	if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 121 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied121); err != nil {
		return err
	}
	var applied122 int
	if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = 122 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied122); err != nil {
		return err
	}
	if applied122 != 0 || (applied119 == 0 && applied120 == 0 && applied121 == 0) {
		return nil
	}

	var inventoryTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'agent_inventory_cache'`,
	).Scan(&inventoryTable); err != nil {
		return err
	}
	if inventoryTable != 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (122, 1)`); err != nil {
		return err
	}
	collidedVersion := int64(119)
	if applied120 != 0 {
		collidedVersion = 120
	}
	if applied121 != 0 {
		collidedVersion = 121
	}
	if _, err := tx.Exec(`DELETE FROM goose_db_version WHERE version_id = ?`, collidedVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// repairRenumberedAgentSwitchMigrationHistory preserves databases opened by
// earlier revisions of this feature branch. Agent switching first occupied
// 0080/0081, later 0081/0082, and briefly 0083/0084; main now owns 0080
// through 0084. Remap the physically present switching schema to the
// consolidated 0085, then release only the main migration numbers whose
// schema effects are still absent.
func repairRenumberedAgentSwitchMigrationHistory(db *sql.DB) error {
	var gooseTable, agentSwitchTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&gooseTable); err != nil {
		return err
	}
	if gooseTable == 0 {
		return nil
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'agent_switches'`,
	).Scan(&agentSwitchTable); err != nil {
		return err
	}
	if agentSwitchTable == 0 {
		return nil
	}

	reviewUpgraded, err := reviewHasSessionHarnessUnique(db)
	if err != nil {
		return err
	}

	var browserVerifierColumn, primeHarnessShape, reconciledKimchiPrimeHarnessShape, autoInjectReviewColumn int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'browser_capability_verifier'`,
	).Scan(&browserVerifierColumn); err != nil {
		return err
	}
	if err := db.QueryRow(`
SELECT COUNT(*)
FROM sqlite_master
WHERE type = 'table'
  AND name = 'sessions'
  AND instr(COALESCE(sql, ''), '''prime-agent''') > 0`).Scan(&primeHarnessShape); err != nil {
		return err
	}
	if err := db.QueryRow(`
SELECT COUNT(*)
FROM sqlite_master
WHERE type = 'table'
  AND name = 'sessions'
  AND instr(COALESCE(sql, ''), '''kimchi''') > 0
  AND instr(COALESCE(sql, ''), '''prime-agent''') > 0`).Scan(&reconciledKimchiPrimeHarnessShape); err != nil {
		return err
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'auto_inject_review'`,
	).Scan(&autoInjectReviewColumn); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Earlier feature builds split finalized-handoff storage into 0084, while
	// still earlier builds only had the base switching table. Repair either
	// shape before recording the now-consolidated 0085 as applied. The status is
	// deliberately not inferred from retained paths: old rows remain unknown.
	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "final_handoff_path", ddl: `ALTER TABLE agent_switches ADD COLUMN final_handoff_path TEXT NOT NULL DEFAULT ''`},
		{name: "final_handoff_hash", ddl: `ALTER TABLE agent_switches ADD COLUMN final_handoff_hash TEXT NOT NULL DEFAULT ''`},
		{name: "source_transcript_status", ddl: `ALTER TABLE agent_switches ADD COLUMN source_transcript_status TEXT NOT NULL DEFAULT 'not_attempted' CHECK (source_transcript_status IN ('not_attempted', 'available', 'unavailable'))`},
		{name: "semantic_handoff_included", ddl: `ALTER TABLE agent_switches ADD COLUMN semantic_handoff_included INTEGER NOT NULL DEFAULT 0 CHECK (semantic_handoff_included IN (0, 1))`},
	} {
		var present int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('agent_switches') WHERE name = ?`, column.name,
		).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			if _, err := tx.Exec(column.ddl); err != nil {
				return err
			}
		}
	}

	var applied85 int
	if err := tx.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
	WHERE version_id = 85 ORDER BY id DESC LIMIT 1
), 0)`).Scan(&applied85); err != nil {
		return err
	}
	if applied85 == 0 {
		if _, err := tx.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (85, 1)`); err != nil {
			return err
		}
	}

	for version, mainEffectPresent := range map[int64]bool{
		80: reviewUpgraded,
		81: browserVerifierColumn != 0,
		82: primeHarnessShape != 0,
		83: reconciledKimchiPrimeHarnessShape != 0,
		84: autoInjectReviewColumn != 0,
	} {
		if mainEffectPresent {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM goose_db_version WHERE version_id = ?`, version); err != nil {
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
// Any migration whose schema the generated queries depend on MUST add an entry
// here when a burned field profile can skip it, or the session list can regress
// to the 500s this exists to prevent.
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
	// 0081_browser_capability_verifier.sql. Keep the generated session queries
	// healthy even if a field database has already burned this migration number.
	{version: 81, table: "sessions", column: "browser_capability_verifier",
		addDDL: `ALTER TABLE sessions ADD COLUMN browser_capability_verifier TEXT NOT NULL DEFAULT ''`},
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
	// Version 86 was briefly used by queued-turn promotion on a development
	// branch. Ensure those databases also receive upstream's version-86 effect.
	{version: 86, table: "workspace_repos", column: "default_branch",
		addDDL: `ALTER TABLE workspace_repos ADD COLUMN default_branch TEXT NOT NULL DEFAULT ''`},
	// These columns are referenced by every generated conversation turn/message
	// query, so verify their physical presence on every startup.
	{version: 89, table: "conversation_turns", column: "promotion_started_at",
		addDDL: `ALTER TABLE conversation_turns ADD COLUMN promotion_started_at TIMESTAMP`},
	{version: 89, table: "conversation_turns", column: "promoted_to_turn_id",
		addDDL: `ALTER TABLE conversation_turns ADD COLUMN promoted_to_turn_id TEXT REFERENCES conversation_turns(id) ON DELETE SET NULL`},
	// 0106_pr_comment_review_id.sql. Session projections join this column, so a
	// field database that recorded 0106 without its physical effect must be
	// repaired before the first list request.
	{version: 106, table: "pr_comment", column: "review_id",
		addDDL: `ALTER TABLE pr_comment ADD COLUMN review_id TEXT NOT NULL DEFAULT ''`},
	// 0108_conversation_retry_source.sql. Some stacked development builds
	// recorded 0108 without applying its physical schema; 0118 rebuilds this
	// table and must see the retry lineage column before goose reaches it.
	{version: 108, table: "conversation_turns", column: "retry_of_turn_id",
		addDDL: `ALTER TABLE conversation_turns ADD COLUMN retry_of_turn_id TEXT REFERENCES conversation_turns(id) ON DELETE RESTRICT`,
		postAdd: []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_conversation_turns_retry_source
    ON conversation_turns(conversation_id, retry_of_turn_id)
    WHERE retry_of_turn_id IS NOT NULL`,
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
	sessionsHarnessCheckWithoutMuse                   = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'fake'))`
	sessionsHarnessCheckWithMuse                      = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'fake'))`
	sessionsHarnessCheckWithoutMuseQM                 = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'qm', 'fake'))`
	sessionsHarnessCheckWithMuseQM                    = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'qm', 'fake'))`
	sessionsHarnessCheckWithMuseKimchi                = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'kimchi', 'autohand', 'fake'))`
	sessionsHarnessCheckWithMuseQMKimchi              = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'kimchi', 'autohand', 'qm', 'fake'))`
	sessionsHarnessCheckWithMusePrimeAgent            = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'prime-agent', 'autohand', 'fake'))`
	sessionsHarnessCheckWithMuseQMPrimeAgent          = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'prime-agent', 'autohand', 'qm', 'fake'))`
	sessionsHarnessCheckWithMuseQMLegacyPrimeAgent    = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'qm', 'prime-agent', 'fake'))`
	sessionsHarnessCheckWithMuseKimchiPrimeAgent      = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'kimchi', 'prime-agent', 'autohand', 'fake'))`
	sessionsHarnessCheckWithMuseQMKimchiPrimeAgent    = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'kimchi', 'prime-agent', 'autohand', 'qm', 'fake'))`
	sessionsHarnessCheckWithMuseKimchiPrimeAgentOMP   = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'kimchi', 'prime-agent', 'autohand', 'omp', 'fake'))`
	sessionsHarnessCheckWithMuseQMKimchiPrimeAgentOMP = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'kimchi', 'prime-agent', 'autohand', 'omp', 'qm', 'fake'))`
)

func reconcileHarnessConstraint(db *sql.DB) error {
	var schema string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`,
	).Scan(&schema); err != nil {
		return fmt.Errorf("schema verification: inspect sessions harness constraint: %w", err)
	}
	needsMuse := !strings.Contains(schema, "'muse'")
	needsKimchi := !strings.Contains(schema, "'kimchi'")
	needsPrimeAgent := !strings.Contains(schema, "'prime-agent'")
	needsOMP := !strings.Contains(schema, "'omp'")
	if !needsMuse && !needsKimchi && !needsPrimeAgent && !needsOMP {
		return nil
	}
	if _, err := db.Exec(`PRAGMA writable_schema = ON`); err != nil {
		return fmt.Errorf("schema repair: enable writable_schema for sessions harness constraint: %w", err)
	}
	type replacement struct {
		old string
		new string
	}
	var repairs []replacement
	if needsMuse {
		repairs = append(repairs,
			replacement{sessionsHarnessCheckWithoutMuse, sessionsHarnessCheckWithMuse},
			replacement{sessionsHarnessCheckWithoutMuseQM, sessionsHarnessCheckWithMuseQM},
		)
	}
	if needsKimchi {
		// After the Muse repair (if any), the constraint will be in one of
		// these known states — each gets Kimchi inserted in the same
		// position as migration 0054 does.
		repairs = append(repairs,
			replacement{sessionsHarnessCheckWithMuse, sessionsHarnessCheckWithMuseKimchi},
			replacement{sessionsHarnessCheckWithMuseQM, sessionsHarnessCheckWithMuseQMKimchi},
			replacement{sessionsHarnessCheckWithMusePrimeAgent, sessionsHarnessCheckWithMuseKimchiPrimeAgent},
			replacement{sessionsHarnessCheckWithMuseQMPrimeAgent, sessionsHarnessCheckWithMuseQMKimchiPrimeAgent},
			replacement{sessionsHarnessCheckWithMuseQMLegacyPrimeAgent, sessionsHarnessCheckWithMuseQMKimchiPrimeAgent},
		)
	}
	if needsPrimeAgent {
		repairs = append(repairs,
			replacement{sessionsHarnessCheckWithMuseKimchi, sessionsHarnessCheckWithMuseKimchiPrimeAgent},
			replacement{sessionsHarnessCheckWithMuseQMKimchi, sessionsHarnessCheckWithMuseQMKimchiPrimeAgent},
		)
	}
	if needsOMP {
		repairs = append(repairs,
			replacement{sessionsHarnessCheckWithMuseKimchiPrimeAgent, sessionsHarnessCheckWithMuseKimchiPrimeAgentOMP},
			replacement{sessionsHarnessCheckWithMuseQMKimchiPrimeAgent, sessionsHarnessCheckWithMuseQMKimchiPrimeAgentOMP},
		)
	}
	for _, r := range repairs {
		if _, err := db.Exec(
			`UPDATE sqlite_master
SET sql = replace(sql, ?, ?)
WHERE type = 'table' AND name = 'sessions'`,
			r.old,
			r.new,
		); err != nil {
			return fmt.Errorf("schema repair: widen sessions harness constraint: %w", err)
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
	if !strings.Contains(schema, "'kimchi'") {
		return fmt.Errorf("schema repair: sessions harness constraint is missing Kimchi and did not match known pre-Kimchi schema")
	}
	if !strings.Contains(schema, "'prime-agent'") {
		return fmt.Errorf("schema repair: sessions harness constraint is missing Prime Agent and did not match known pre-Prime-Agent schema")
	}
	if !strings.Contains(schema, "'omp'") {
		return fmt.Errorf("schema repair: sessions harness constraint is missing OMP and did not match known pre-OMP schema")
	}
	return nil
}
