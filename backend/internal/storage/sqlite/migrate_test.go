package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var expectedUsageTableColumns = map[string][]string{
	"usage_bindings": {
		"id", "session_id", "harness", "native_root_id", "initial_model_id",
		"state", "last_error_code", "updated_at", "provider_hint",
	},
	"usage_sources": {
		"id", "binding_id", "kind", "native_session_id", "subagent_id", "artifact_path",
		"file_identity", "generation", "byte_offset", "parser_state_json", "state",
		"failure_count", "anomaly_count", "next_retry_at", "last_error_code", "updated_at",
	},
	"model_usage_events": {
		"id", "binding_id", "usage_source_id", "provider_id", "billing_provider_id", "model_id",
		"usage_measurement_kind", "input_tokens", "cached_input_tokens",
		"uncached_input_tokens", "output_tokens", "provider_usage_json",
		"source_event_key", "created_at",
		"input_cost_nanos", "cached_input_cost_nanos", "output_cost_nanos",
		"estimated_cost_nanos", "pricing_version",
		// 0116 appends: ALTER TABLE has no way to place a column mid-row, and a
		// second full rebuild to move it beside billing_provider_id would cost
		// more than the adjacency is worth.
		"billing_provider_source",
	},
}

// The provider detail tables were the shape 0115 replaced with one bounded
// provider usage object. Nothing may recreate them.
var retiredUsageTables = []string{"openai_usage_event_details", "anthropic_usage_event_details"}

func TestMigrateDefaultsSessionInterfaceToChat(t *testing.T) {
	db := openMigratedTestDB(t)

	var mode string
	if err := db.QueryRow(`SELECT default_session_mode FROM app_settings WHERE id = 1`).Scan(&mode); err != nil {
		t.Fatalf("read default session mode: %v", err)
	}
	if mode != "chat" {
		t.Fatalf("default session mode = %q, want chat", mode)
	}
}

func TestMigrateUpdatesExistingSessionInterfaceDefaultToChat(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 103)

	if _, err := db.Exec(`UPDATE app_settings SET default_session_mode = 'tui' WHERE id = 1`); err != nil {
		t.Fatalf("seed existing TUI default: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate existing database: %v", err)
	}

	var mode string
	if err := db.QueryRow(`SELECT default_session_mode FROM app_settings WHERE id = 1`).Scan(&mode); err != nil {
		t.Fatalf("read default session mode: %v", err)
	}
	if mode != "chat" {
		t.Fatalf("default session mode = %q, want chat after upgrade", mode)
	}
}

func TestMigrateRollbackPreservesSessionInterfacePreference(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 103)

	if _, err := db.Exec(`UPDATE app_settings SET default_session_mode = 'chat' WHERE id = 1`); err != nil {
		t.Fatalf("seed deliberate Chat default: %v", err)
	}
	upTo(t, db, 105)
	downTo(t, db, 104)

	var mode string
	if err := db.QueryRow(`SELECT default_session_mode FROM app_settings WHERE id = 1`).Scan(&mode); err != nil {
		t.Fatalf("read default session mode: %v", err)
	}
	if mode != "chat" {
		t.Fatalf("default session mode after rollback = %q, want preserved chat", mode)
	}
}

func TestUsageTablesKeepOnlyDurableCollectionState(t *testing.T) {
	db := openMigratedTestDB(t)
	for table, wantColumns := range expectedUsageTableColumns {
		got := tableColumns(t, db, table)
		if !reflect.DeepEqual(got, wantColumns) {
			t.Errorf("%s columns = %v, want %v", table, got, wantColumns)
		}
	}
	for _, table := range retiredUsageTables {
		if got := tableColumns(t, db, table); len(got) != 0 {
			t.Errorf("%s still exists with columns %v", table, got)
		}
	}
}

func TestUsageCostMigrationKeepsLegacyRowsUnattributedAndUnpriced(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 94)
	seedUsageMigrationRow(t, db)
	if err := migrate(db); err != nil {
		t.Fatalf("apply cost migration: %v", err)
	}

	var providerHint, pricingVersion, measurementKind string
	var billingProviderID, providerUsage sql.NullString
	var inputCost, cachedInputCost, outputCost, totalCost sql.NullInt64
	if err := db.QueryRow(`
SELECT ub.provider_hint, mue.billing_provider_id, mue.usage_measurement_kind,
       mue.provider_usage_json,
       mue.input_cost_nanos, mue.cached_input_cost_nanos, mue.output_cost_nanos,
       mue.estimated_cost_nanos, mue.pricing_version
FROM usage_bindings ub
JOIN model_usage_events mue ON mue.binding_id = ub.id
WHERE mue.source_event_key = 'migration-event'`).Scan(
		&providerHint, &billingProviderID, &measurementKind, &providerUsage,
		&inputCost, &cachedInputCost, &outputCost, &totalCost, &pricingVersion,
	); err != nil {
		t.Fatalf("read migrated usage facts: %v", err)
	}
	// The counters came from a native record, so the event is native_reported,
	// but nothing observed its bounded provider object: that is the legacy
	// repairer's job, not the migration's.
	if providerHint != "" || pricingVersion != "" || billingProviderID.Valid ||
		measurementKind != "native_reported" || providerUsage.Valid ||
		inputCost.Valid || cachedInputCost.Valid || outputCost.Valid || totalCost.Valid {
		t.Fatalf("legacy defaults = hint:%q billing:%v kind:%q usage:%v costs:%v/%v/%v/%v version:%q",
			providerHint, billingProviderID, measurementKind, providerUsage,
			inputCost, cachedInputCost, outputCost, totalCost, pricingVersion)
	}

	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_model_usage_events_cost_candidates'`).Scan(&indexSQL); err != nil {
		t.Fatalf("read cost candidate index: %v", err)
	}
	if !strings.Contains(indexSQL, "billing_provider_id, pricing_version, id") ||
		!strings.Contains(indexSQL, "estimated_cost_nanos IS NULL") {
		t.Fatalf("cost candidate index = %q", indexSQL)
	}
}

func seedUsageMigrationRow(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO projects (id, path, display_name, registered_at)
VALUES ('migration-project', '/tmp/migration-project', 'migration-project', CURRENT_TIMESTAMP);
INSERT INTO sessions (
    id, project_id, num, harness, activity_last_at, workspace_path, branch, created_at, updated_at
)
VALUES (
    'migration-session', 'migration-project', 1, 'codex', CURRENT_TIMESTAMP,
    '/tmp/migration-session', 'test', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
);
INSERT INTO usage_bindings (session_id, harness, native_root_id, state, updated_at)
VALUES ('migration-session', 'codex', 'native-root', 'active', CURRENT_TIMESTAMP);
INSERT INTO usage_sources (binding_id, kind, artifact_path, state, updated_at)
VALUES (last_insert_rowid(), 'codex_rollout', '/tmp/rollout.jsonl', 'active', CURRENT_TIMESTAMP);
INSERT INTO model_usage_events (
    binding_id, usage_source_id, model_id, input_tokens, uncached_input_tokens,
    cache_read_tokens, cache_write_tokens, output_tokens, source_event_key
)
SELECT binding_id, id, 'gpt-test', 10, 8, 2, 0, 3, 'migration-event'
FROM usage_sources WHERE artifact_path = '/tmp/rollout.jsonl';
`); err != nil {
		t.Fatalf("seed migrated usage row: %v", err)
	}
}

func TestUsageSchemaUpgradePreservesEarlierPRData(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 43)

	// Reproduce the wider tables and burned migration history created by an
	// earlier checkout of this PR.
	if _, err := db.Exec(`
CREATE TABLE usage_bindings (
    id INTEGER PRIMARY KEY, session_id TEXT, harness TEXT, native_root_id TEXT,
    initial_model_id TEXT NOT NULL DEFAULT '', state TEXT,
    last_error_code TEXT NOT NULL DEFAULT '', first_seen_at TIMESTAMP,
    last_seen_at TIMESTAMP, updated_at TIMESTAMP
);
CREATE TABLE usage_sources (
    id INTEGER PRIMARY KEY, binding_id INTEGER, kind TEXT,
    native_session_id TEXT NOT NULL DEFAULT '', subagent_id TEXT NOT NULL DEFAULT '',
    artifact_path TEXT, file_identity TEXT NOT NULL DEFAULT '',
    generation INTEGER NOT NULL DEFAULT 0, byte_offset INTEGER NOT NULL DEFAULT 0,
    parser_state_json TEXT NOT NULL DEFAULT '{}', state TEXT,
    failure_count INTEGER NOT NULL DEFAULT 0, anomaly_count INTEGER NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMP, last_error_code TEXT NOT NULL DEFAULT '',
    last_observed_at TIMESTAMP, created_at TIMESTAMP, updated_at TIMESTAMP
);
CREATE TABLE model_usage_events (
    id INTEGER PRIMARY KEY, binding_id INTEGER, usage_source_id INTEGER,
    project_id TEXT, session_id TEXT, harness TEXT, provider TEXT,
    model_id TEXT, observed_at TIMESTAMP, input_tokens INTEGER,
    uncached_input_tokens INTEGER, cache_read_tokens INTEGER,
    cache_write_tokens INTEGER, output_tokens INTEGER, reasoning_tokens INTEGER,
    source_event_key TEXT, created_at TIMESTAMP
);
INSERT INTO usage_bindings
    (id, session_id, harness, native_root_id, state, updated_at)
VALUES (1, 'session-1', 'codex', 'native-1', 'active', '2026-08-01T00:00:00Z');
INSERT INTO usage_sources
    (id, binding_id, kind, native_session_id, artifact_path, state, updated_at)
VALUES (1, 1, 'codex_rollout', 'native-1', '/tmp/rollout.jsonl', 'active', '2026-08-01T00:00:00Z');
INSERT INTO model_usage_events
    (id, binding_id, usage_source_id, model_id, input_tokens, uncached_input_tokens,
     cache_read_tokens, cache_write_tokens, output_tokens, source_event_key)
VALUES (1, 1, 1, 'gpt-test', 120, 100, 20, 0, 30, 'event-1');
`); err != nil {
		t.Fatalf("seed legacy usage: %v", err)
	}
	for version := 44; version <= 51; version++ {
		if _, err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version,
		); err != nil {
			t.Fatalf("seed migration %d: %v", version, err)
		}
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate earlier usage schema: %v", err)
	}
	for table, wantColumns := range expectedUsageTableColumns {
		if got := tableColumns(t, db, table); !reflect.DeepEqual(got, wantColumns) {
			t.Errorf("%s columns = %v, want %v", table, got, wantColumns)
		}
	}
	var inputTokens, outputTokens int
	if err := db.QueryRow(
		`SELECT input_tokens, output_tokens FROM model_usage_events WHERE source_event_key = 'event-1'`,
	).Scan(&inputTokens, &outputTokens); err != nil {
		t.Fatalf("read preserved usage event: %v", err)
	}
	if inputTokens != 120 || outputTokens != 30 {
		t.Fatalf("preserved usage = (%d, %d), want (120, 30)", inputTokens, outputTokens)
	}
	var catalogTables int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'agent_model_catalog'`,
	).Scan(&catalogTables); err != nil || catalogTables != 1 {
		t.Fatalf("agent_model_catalog table count = %d, err = %v", catalogTables, err)
	}
	var viewCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'view' AND name = 'usage_session_integrity'`,
	).Scan(&viewCount); err != nil || viewCount != 1 {
		t.Fatalf("usage_session_integrity view count = %d, err = %v", viewCount, err)
	}
	for _, table := range []string{"usage_sources", "model_usage_events"} {
		var staleReferences int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_foreign_key_list(?) WHERE "table" LIKE '%_next'`, table,
		).Scan(&staleReferences); err != nil || staleReferences != 0 {
			t.Fatalf("%s stale compatibility foreign keys = %d, err = %v", table, staleReferences, err)
		}
	}
}

func TestCanonicalUsageMigrationBackfillsProviderAwareMetrics(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 100)
	now := time.Unix(1700000000, 0).UTC()
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config)
VALUES ('usage', '/repo/usage', ?, '{}');
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('usage-1', 'usage', 1, 'codex', ?, ?, ?),
       ('usage-2', 'usage', 2, 'claude-code', ?, ?, ?);
INSERT INTO usage_bindings (id, session_id, harness, native_root_id, state, updated_at)
VALUES (1, 'usage-1', 'codex', 'codex-root', 'complete', ?),
       (2, 'usage-2', 'claude-code', 'claude-root', 'complete', ?);
INSERT INTO usage_sources (id, binding_id, kind, artifact_path, state, updated_at)
VALUES (1, 1, 'codex_rollout', '/tmp/codex.jsonl', 'complete', ?),
       (2, 2, 'claude_main', '/tmp/claude.jsonl', 'complete', ?);
INSERT INTO model_usage_events
    (id, binding_id, usage_source_id, model_id, input_tokens, uncached_input_tokens,
     cache_read_tokens, cache_write_tokens, output_tokens, reasoning_tokens, source_event_key)
VALUES (1, 1, 1, 'gpt-test', 100, 30, 60, 10, 20, 3, 'codex-event'),
       (2, 2, 2, 'claude-test', 100, 10, 50, 40, 20, NULL, 'claude-event');
`, now, now, now, now, now, now, now, now, now, now, now); err != nil {
		t.Fatalf("seed pre-canonical usage: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate canonical usage: %v", err)
	}
	type canonicalRow struct {
		provider                     string
		input, cached, uncached, out int64
		measurementKind              string
	}
	for _, test := range []struct {
		key, provider    string
		cached, uncached int64
	}{
		{key: "codex-event", provider: "openai", cached: 60, uncached: 40},
		{key: "claude-event", provider: "anthropic", cached: 50, uncached: 50},
	} {
		var row canonicalRow
		if err := db.QueryRow(`SELECT provider_id, input_tokens, cached_input_tokens,
uncached_input_tokens, output_tokens, usage_measurement_kind
FROM model_usage_events WHERE source_event_key = ?`, test.key).Scan(
			&row.provider, &row.input, &row.cached, &row.uncached, &row.out,
			&row.measurementKind,
		); err != nil {
			t.Fatalf("read %s canonical event: %v", test.key, err)
		}
		// Exact arithmetic over native counters is still native_reported.
		if row.provider != test.provider || row.input != 100 || row.cached != test.cached || row.uncached != test.uncached ||
			row.out != 20 || row.measurementKind != "native_reported" {
			t.Fatalf("%s canonical row = %+v", test.key, row)
		}
	}
	if err := migrate(db); err != nil {
		t.Fatalf("repeat canonical migration: %v", err)
	}
}

// TestUsageMeasurementMigrationFoldsCostsAndRetiresDetailTables covers 0115
// directly: a pre-0115 profile is seeded through the migrations that shipped
// before it, then upgraded.
func TestUsageMeasurementMigrationFoldsCostsAndRetiresDetailTables(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 114)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config) VALUES ('measure', '/repo/measure', ?, '{}');
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('measure-1', 'measure', 1, 'claude-code', ?, ?, ?);
INSERT INTO usage_bindings (id, session_id, harness, native_root_id, state, updated_at)
VALUES (1, 'measure-1', 'claude-code', 'root', 'complete', ?);
INSERT INTO usage_sources (id, binding_id, kind, artifact_path, state, updated_at)
VALUES (1, 1, 'claude_main', '/tmp/claude.jsonl', 'complete', ?);
INSERT INTO model_usage_events (
    id, binding_id, usage_source_id, provider_id, billing_provider_id, model_id,
    input_tokens, input_provenance, cached_input_tokens, cached_input_provenance,
    uncached_input_tokens, uncached_input_provenance, output_tokens, output_provenance,
    source_event_key, uncached_input_cost_nanos, cache_read_cost_nanos,
    cache_write_cost_nanos, output_cost_nanos, estimated_cost_nanos, pricing_version
) VALUES
    (1, 1, 1, 'anthropic', 'anthropic', 'claude-test', 100, 'derived', 40, 'reported', 60, 'derived', 20, 'reported',
     'priced', 30, 10, 15, 50, 105, 'catalog-v1'),
    (2, 1, 1, 'anthropic', 'anthropic', 'claude-test', 100, 'derived', 40, 'reported', 60, 'derived', 20, 'reported',
     'half-priced', 30, 10, NULL, 50, NULL, 'catalog-v1'),
    (3, 1, 1, 'anthropic', NULL, 'claude-test', NULL, 'unknown', NULL, 'unknown', NULL, 'unknown', NULL, 'unknown',
     'uncollected', NULL, NULL, NULL, NULL, NULL, '');
INSERT INTO anthropic_usage_event_details (event_id, anthropic_direct_uncached_input_tokens, anthropic_cache_creation_input_tokens)
VALUES (1, 50, 10);
`, now, now, now, now, now, now); err != nil {
		t.Fatalf("seed pre-measurement usage: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("apply measurement migration: %v", err)
	}

	for _, test := range []struct {
		key             string
		wantKind        string
		wantInput       sql.NullInt64
		wantCachedInput sql.NullInt64
		wantTotal       sql.NullInt64
	}{
		// Fresh input plus cache write become one input charge.
		{key: "priced", wantKind: "native_reported", wantInput: sql.NullInt64{Int64: 45, Valid: true},
			wantCachedInput: sql.NullInt64{Int64: 10, Valid: true}, wantTotal: sql.NullInt64{Int64: 105, Valid: true}},
		// A half-known input charge stays unknown rather than under-reporting.
		{key: "half-priced", wantKind: "native_reported", wantCachedInput: sql.NullInt64{Int64: 10, Valid: true}},
		{key: "uncollected", wantKind: "unknown"},
	} {
		var kind string
		var providerUsage sql.NullString
		var input, cachedInput, total sql.NullInt64
		if err := db.QueryRow(`SELECT usage_measurement_kind, provider_usage_json,
input_cost_nanos, cached_input_cost_nanos, estimated_cost_nanos
FROM model_usage_events WHERE source_event_key = ?`, test.key).Scan(
			&kind, &providerUsage, &input, &cachedInput, &total,
		); err != nil {
			t.Fatalf("read %s migrated event: %v", test.key, err)
		}
		if kind != test.wantKind || input != test.wantInput || cachedInput != test.wantCachedInput || total != test.wantTotal {
			t.Fatalf("%s migrated row = kind:%q input:%v cached:%v total:%v", test.key, kind, input, cachedInput, total)
		}
		// Detail-table counters are never rewritten into a provider object AO
		// never observed; the legacy repairer refills it from the transcript.
		if providerUsage.Valid {
			t.Fatalf("%s invented a provider usage object: %q", test.key, providerUsage.String)
		}
	}

	for _, table := range retiredUsageTables {
		if got := tableColumns(t, db, table); len(got) != 0 {
			t.Fatalf("%s survived the migration with columns %v", table, got)
		}
	}
	for _, index := range []string{
		"idx_model_usage_events_binding_model", "idx_model_usage_events_usage_source",
		"idx_model_usage_events_cost_candidates",
		"idx_model_usage_events_canonical_cost_candidates",
	} {
		var name string
		if err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index,
		).Scan(&name); err != nil {
			t.Fatalf("index %s was not recreated: %v", index, err)
		}
	}
	// provider_id no longer reaches a detail table and no query filters by it,
	// so its index is retired rather than rebuilt.
	var retired string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_model_usage_events_provider'`,
	).Scan(&retired); err == nil {
		t.Fatal("idx_model_usage_events_provider survived the rebuild")
	}
	if err := migrate(db); err != nil {
		t.Fatalf("repeat measurement migration: %v", err)
	}
}

// TestBillingProviderSourceMigrationMarksExistingAttributionsObserved covers
// 0116. Every row attributed before the column existed came from the transcript
// or the route hint, so all of them must survive as observations: mislabelling
// one as an inference would invite a later repair to overwrite a fact.
func TestBillingProviderSourceMigrationMarksExistingAttributionsObserved(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 115)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config) VALUES ('attrib', '/repo/attrib', ?, '{}');
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('attrib-1', 'attrib', 1, 'claude-code', ?, ?, ?);
INSERT INTO usage_bindings (id, session_id, harness, native_root_id, state, updated_at)
VALUES (1, 'attrib-1', 'claude-code', 'root', 'complete', ?);
INSERT INTO usage_sources (id, binding_id, kind, artifact_path, state, updated_at)
VALUES (1, 1, 'claude_main', '/tmp/claude.jsonl', 'complete', ?);
INSERT INTO model_usage_events (
    id, binding_id, usage_source_id, provider_id, billing_provider_id, model_id,
    usage_measurement_kind, input_tokens, cached_input_tokens, uncached_input_tokens,
    output_tokens, source_event_key
) VALUES
    (1, 1, 1, 'anthropic', 'anthropic', 'claude-test', 'native_reported', 100, 40, 60, 20, 'attributed'),
    (2, 1, 1, 'anthropic', NULL, 'claude-test', 'native_reported', 100, 40, 60, 20, 'unattributed');
`, now, now, now, now, now, now); err != nil {
		t.Fatalf("seed pre-source usage: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("apply billing provider source migration: %v", err)
	}

	for _, test := range []struct {
		key  string
		want sql.NullString
	}{
		{key: "attributed", want: sql.NullString{String: "observed", Valid: true}},
		// No provider, so no source: an attribution nobody made cannot be
		// labelled with how it was reached.
		{key: "unattributed"},
	} {
		var got sql.NullString
		if err := db.QueryRow(
			`SELECT billing_provider_source FROM model_usage_events WHERE source_event_key = ?`, test.key,
		).Scan(&got); err != nil {
			t.Fatalf("read %s attribution source: %v", test.key, err)
		}
		if got != test.want {
			t.Fatalf("%s billing_provider_source = %v, want %v", test.key, got, test.want)
		}
	}

	if _, err := db.Exec(
		`UPDATE model_usage_events SET billing_provider_source = 'guessed' WHERE id = 1`,
	); err == nil {
		t.Fatal("billing_provider_source accepted a value outside the enum")
	}

	var indexSQL string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_model_usage_events_open_attribution'`,
	).Scan(&indexSQL); err != nil {
		t.Fatalf("read open attribution index: %v", err)
	}
	if !strings.Contains(indexSQL, "billing_provider_id IS NULL") ||
		!strings.Contains(indexSQL, "billing_provider_source = 'inferred'") {
		t.Fatalf("open attribution index = %q", indexSQL)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("repeat billing provider source migration: %v", err)
	}
}

// TestKimiUsageMigrationPreservesCurrentUsageFacts covers 0117 directly. The
// harness/source enum rebuild must retain columns introduced by 0113-0116 and
// leave the bounded provider usage object untouched.
func TestKimiUsageMigrationPreservesCurrentUsageFacts(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 116)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config) VALUES ('kimi-migration', '/repo/kimi', ?, '{}');
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('kimi-migration-1', 'kimi-migration', 1, 'claude-code', ?, ?, ?);
INSERT INTO usage_bindings (
    id, session_id, harness, native_root_id, state, updated_at, provider_hint
) VALUES (1, 'kimi-migration-1', 'claude-code', 'root', 'complete', ?, 'anthropic');
INSERT INTO usage_sources (id, binding_id, kind, artifact_path, state, updated_at)
VALUES (1, 1, 'claude_main', '/tmp/claude.jsonl', 'complete', ?);
INSERT INTO model_usage_events (
    id, binding_id, usage_source_id, provider_id, billing_provider_id,
    billing_provider_source, model_id, usage_measurement_kind,
    input_tokens, cached_input_tokens, uncached_input_tokens, output_tokens,
    provider_usage_json, source_event_key
) VALUES (
    1, 1, 1, 'anthropic', 'anthropic', 'observed', 'claude-test',
    'native_reported', 100, 40, 60, 20,
    '{"input_tokens":60,"cache_read_input_tokens":40,"output_tokens":20}',
    'pre-kimi'
);`, now, now, now, now, now, now); err != nil {
		t.Fatalf("seed pre-Kimi usage schema: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("apply Kimi usage migration: %v", err)
	}

	var providerHint, measurementKind, providerUsage, billingSource string
	if err := db.QueryRow(`
SELECT binding.provider_hint, event.usage_measurement_kind,
       event.provider_usage_json, event.billing_provider_source
FROM usage_bindings binding
JOIN model_usage_events event ON event.binding_id = binding.id
WHERE event.source_event_key = 'pre-kimi'`).Scan(
		&providerHint, &measurementKind, &providerUsage, &billingSource,
	); err != nil {
		t.Fatalf("read usage facts after Kimi migration: %v", err)
	}
	if providerHint != "anthropic" || measurementKind != "native_reported" ||
		providerUsage != `{"input_tokens":60,"cache_read_input_tokens":40,"output_tokens":20}` ||
		billingSource != "observed" {
		t.Fatalf("migrated usage facts = hint:%q kind:%q usage:%q billing-source:%q",
			providerHint, measurementKind, providerUsage, billingSource)
	}

	if _, err := db.Exec(`
INSERT INTO usage_bindings (session_id, harness, native_root_id, state, updated_at)
VALUES ('kimi-migration-1', 'kimi', 'kimi-root', 'active', ?);
INSERT INTO usage_sources (binding_id, kind, artifact_path, state, updated_at)
SELECT id, 'kimi_wire', '/tmp/kimi/wire.jsonl', 'active', ?
FROM usage_bindings WHERE harness = 'kimi';`, now, now); err != nil {
		t.Fatalf("insert Kimi usage binding and source: %v", err)
	}
}

func TestCompletedPlanMigrationRepairsStructuredStateWithoutChangingProviderEvents(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 118)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config)
VALUES ('plan-migration', '/repo/plan', ?, '{}');
INSERT INTO sessions (
    id, project_id, num, harness, activity_last_at, created_at, updated_at, session_mode
) VALUES ('plan-migration-1', 'plan-migration', 1, 'codex', ?, ?, ?, 'chat');
INSERT INTO conversations (
    id, scope, project_id, session_id, current_session_id, latest_sequence,
    created_at, updated_at, active_branch_id
) VALUES (
    'conversation-1', 'session', 'plan-migration', 'plan-migration-1',
    'plan-migration-1', 2, ?, ?, 'branch-1'
);
INSERT INTO conversation_branches (
    id, conversation_id, session_id, provider_conversation_id, created_at
) VALUES ('branch-1', 'conversation-1', 'plan-migration-1', 'thread-1', ?);
INSERT INTO conversation_turns (
    id, conversation_id, handled_by_session_id, provider_turn_id, state,
    requested_at, completed_at, plan_json, branch_id
) VALUES
    ('turn-completed', 'conversation-1', 'plan-migration-1', 'provider-completed',
     'completed', ?, ?,
     '{"steps":[{"text":"one","status":"in_progress"},{"text":"two","status":"pending"}]}',
     'branch-1'),
    ('turn-failed', 'conversation-1', 'plan-migration-1', 'provider-failed',
     'failed', ?, ?,
     '{"steps":[{"text":"one","status":"in_progress"},{"text":"two","status":"pending"}]}',
     'branch-1');
INSERT INTO conversation_activities (
    id, conversation_id, turn_id, sequence, revision, kind, status, summary,
    detail_json, provider_item_id, created_at, updated_at, branch_id
) VALUES
    ('activity-completed', 'conversation-1', 'turn-completed', 1, 3, 'plan',
     'completed', 'Plan 0/2: one',
     '{"event":"plan","steps":[{"text":"one","status":"in_progress"},{"text":"two","status":"pending"}]}',
     'ao-plan-provider-completed', ?, ?, 'branch-1'),
    ('activity-failed', 'conversation-1', 'turn-failed', 2, 4, 'plan',
     'failed', 'Plan 0/2: one',
     '{"event":"plan","steps":[{"text":"one","status":"in_progress"},{"text":"two","status":"pending"}]}',
     'ao-plan-provider-failed', ?, ?, 'branch-1');
INSERT INTO conversation_provider_events (
    conversation_id, session_id, provider_event_id, method, payload_json, received_at, branch_id
) VALUES
    ('conversation-1', 'plan-migration-1', 'event-plan', 'turn.plan', '{"raw":"plan"}', ?, 'branch-1'),
    ('conversation-1', 'plan-migration-1', 'event-completed', 'turn.completed', '{"raw":"completed"}', ?, 'branch-1');`,
		now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now); err != nil {
		t.Fatalf("seed stale completed plan: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("apply completed-plan migration: %v", err)
	}

	var turnStatuses, activityStatuses, summary string
	var revision int
	if err := db.QueryRow(`
SELECT json_extract(turn.plan_json, '$.steps[0].status') || ',' ||
       json_extract(turn.plan_json, '$.steps[1].status'),
       json_extract(activity.detail_json, '$.steps[0].status') || ',' ||
       json_extract(activity.detail_json, '$.steps[1].status'),
       activity.summary, activity.revision
FROM conversation_turns turn
JOIN conversation_activities activity ON activity.turn_id = turn.id
WHERE turn.id = 'turn-completed'`).Scan(
		&turnStatuses, &activityStatuses, &summary, &revision,
	); err != nil {
		t.Fatalf("read repaired completed plan: %v", err)
	}
	if turnStatuses != "completed,completed" || activityStatuses != "completed,completed" ||
		summary != "Plan 2/2 steps done" || revision != 4 {
		t.Fatalf("repaired plan = turn:%q activity:%q summary:%q revision:%d",
			turnStatuses, activityStatuses, summary, revision)
	}

	var failedTurnStatus, failedActivityStatus string
	var failedRevision int
	if err := db.QueryRow(`
SELECT json_extract(turn.plan_json, '$.steps[0].status'),
       json_extract(activity.detail_json, '$.steps[0].status'), activity.revision
FROM conversation_turns turn
JOIN conversation_activities activity ON activity.turn_id = turn.id
WHERE turn.id = 'turn-failed'`).Scan(
		&failedTurnStatus, &failedActivityStatus, &failedRevision,
	); err != nil {
		t.Fatalf("read failed plan control: %v", err)
	}
	if failedTurnStatus != "in_progress" || failedActivityStatus != "in_progress" || failedRevision != 4 {
		t.Fatalf("failed plan was changed = turn:%q activity:%q revision:%d",
			failedTurnStatus, failedActivityStatus, failedRevision)
	}

	var rawEvents string
	if err := db.QueryRow(`
SELECT group_concat(method || ':' || payload_json, '|')
FROM (SELECT method, payload_json FROM conversation_provider_events ORDER BY id)`).Scan(&rawEvents); err != nil {
		t.Fatalf("read raw provider archive: %v", err)
	}
	if rawEvents != `turn.plan:{"raw":"plan"}|turn.completed:{"raw":"completed"}` {
		t.Fatalf("provider archive changed: %q", rawEvents)
	}
}

func openMigratedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s columns: %v", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	return columns
}

// TestMigrateAllowsEveryShippedHarness guards against the collapsed-migration
// silent-no-op concern: a hand-written replace() that fails to widen the
// sessions.harness CHECK (because the target substring drifted) leaves the
// schema accepting only the original harnesses while migrate() still reports
// success. This test opens a fresh DB, runs the migrations, and asserts the
// live sessions schema admits every harness the domain ships, building the
// expected set from the domain constants so it can't silently drift.
func TestMigrateAllowsEveryShippedHarness(t *testing.T) {
	db := openMigratedTestDB(t)

	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='sessions'",
	).Scan(&schema); err != nil {
		t.Fatalf("read sessions schema: %v", err)
	}
	harnesses := domain.AllHarnesses

	for _, h := range harnesses {
		if !strings.Contains(schema, "'"+string(h)+"'") {
			t.Errorf("sessions.harness CHECK is missing harness %q — the migration that widens it silently no-opped; schema:\n%s", h, schema)
		}
	}
}

func TestMigrateRepairsSkippedMuseHarnessConstraint(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 43)

	for version := 44; version <= 53; version++ {
		if _, err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version,
		); err != nil {
			t.Fatalf("seed migration %d: %v", version, err)
		}
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate skipped muse harness schema: %v", err)
	}
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='sessions'",
	).Scan(&schema); err != nil {
		t.Fatalf("read sessions schema: %v", err)
	}
	if !strings.Contains(schema, "'muse'") {
		t.Fatalf("sessions.harness CHECK is missing muse after repair:\n%s", schema)
	}
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config)
VALUES ('agent-orchestrator', '/repo/agent-orchestrator', ?, '{}');
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('agent-orchestrator-1', 'agent-orchestrator', 1, 'muse', ?, ?, ?);
`, time.Unix(100, 0).UTC(), time.Unix(101, 0).UTC(), time.Unix(101, 0).UTC(), time.Unix(101, 0).UTC()); err != nil {
		t.Fatalf("insert muse session after repair: %v", err)
	}
}

func TestMigrateRepairsSkippedMuseHarnessConstraintWithLegacyQM(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 43)

	if _, err := db.Exec(`PRAGMA writable_schema = ON`); err != nil {
		t.Fatalf("enable writable_schema: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE sqlite_master
SET sql = replace(sql, ?, ?)
WHERE type = 'table' AND name = 'sessions'`,
		`CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'fake'))`,
		`CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'qm', 'fake'))`,
	); err != nil {
		t.Fatalf("seed legacy qm harness constraint: %v", err)
	}
	if _, err := db.Exec(`PRAGMA writable_schema = RESET`); err != nil {
		t.Fatalf("reparse legacy qm harness constraint: %v", err)
	}
	for version := 44; version <= 53; version++ {
		if _, err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version,
		); err != nil {
			t.Fatalf("seed migration %d: %v", version, err)
		}
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate skipped muse harness schema with legacy qm: %v", err)
	}
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='sessions'",
	).Scan(&schema); err != nil {
		t.Fatalf("read sessions schema: %v", err)
	}
	for _, harness := range []string{"'muse'", "'qm'", "'kimchi'"} {
		if !strings.Contains(schema, harness) {
			t.Fatalf("sessions.harness CHECK is missing %s after repair:\n%s", harness, schema)
		}
	}
}

// TestMigration0054AddsKimchiToLegacyQMConstraint seeds a QM-variant constraint
// (the legacy constraint that includes 'qm' alongside 'muse') and runs only
// migration 0054, asserting that the QM replace pair inserted 'kimchi' into
// the constraint. Without the QM pair, the replace() source string omits 'qm'
// and no-ops, leaving Kimchi inserts to fail with a CHECK violation.
func TestMigration0054AddsKimchiToLegacyQMConstraint(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 53)

	// Simulate a legacy QM-variant database: swap the constraint from the
	// post-0053 state (muse, no qm) to the QM variant (muse, qm, no kimchi).
	if _, err := db.Exec(`PRAGMA writable_schema = ON`); err != nil {
		t.Fatalf("enable writable_schema: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE sqlite_master
SET sql = replace(sql, ?, ?)
WHERE type = 'table' AND name = 'sessions'`,
		sessionsHarnessCheckWithMuse,
		sessionsHarnessCheckWithMuseQM,
	); err != nil {
		t.Fatalf("seed legacy qm harness constraint: %v", err)
	}
	if _, err := db.Exec(`PRAGMA writable_schema = RESET`); err != nil {
		t.Fatalf("reparse legacy qm harness constraint: %v", err)
	}

	// Run only migration 0054 (versions 1–53 are already applied).
	upTo(t, db, 54)

	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='sessions'",
	).Scan(&schema); err != nil {
		t.Fatalf("read sessions schema: %v", err)
	}
	for _, harness := range []string{"'muse'", "'qm'", "'kimchi'"} {
		if !strings.Contains(schema, harness) {
			t.Fatalf("sessions.harness CHECK is missing %s after migration 0054:\n%s", harness, schema)
		}
	}
}

// TestMigrateRepairsKimchiConstraintWithPrimeAgentAndLegacyQM reproduces the
// shared dev profile: the Kimchi migration is recorded as applied, while a
// later checkout widened the physical constraint for Prime Agent and legacy QM
// without retaining Kimchi. Startup must converge the known constraint without
// dropping either existing harness or rejecting existing Prime Agent rows.
func TestMigrateRepairsKimchiConstraintWithPrimeAgentAndLegacyQM(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 80)

	const primeAgentQMConstraint = `CHECK (harness IN ('', 'claude-code', 'codex', 'aider', 'opencode', 'grok', 'droid', 'amp', 'agy', 'crush', 'cursor', 'qwen', 'copilot', 'goose', 'auggie', 'continue', 'devin', 'cline', 'kimi', 'muse', 'kiro', 'kilocode', 'vibe', 'pi', 'autohand', 'qm', 'prime-agent', 'fake'))`
	if _, err := db.Exec(`PRAGMA writable_schema = ON`); err != nil {
		t.Fatalf("enable writable_schema: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE sqlite_master
SET sql = replace(sql, ?, ?)
WHERE type = 'table' AND name = 'sessions'`,
		sessionsHarnessCheckWithMuseKimchi,
		primeAgentQMConstraint,
	); err != nil {
		t.Fatalf("seed prime-agent qm harness constraint: %v", err)
	}
	if _, err := db.Exec(`PRAGMA writable_schema = RESET`); err != nil {
		t.Fatalf("reparse prime-agent qm harness constraint: %v", err)
	}

	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config)
VALUES ('agent-orchestrator', '/repo/agent-orchestrator', ?, '{}');
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('agent-orchestrator-1', 'agent-orchestrator', 1, 'prime-agent', ?, ?, ?);
`, time.Unix(100, 0).UTC(), time.Unix(101, 0).UTC(), time.Unix(101, 0).UTC(), time.Unix(101, 0).UTC()); err != nil {
		t.Fatalf("seed prime-agent session: %v", err)
	}
	for _, version := range []int{81, 82, 83} {
		if _, err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version,
		); err != nil {
			t.Fatalf("seed migration %d: %v", version, err)
		}
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate prime-agent qm profile: %v", err)
	}
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='sessions'",
	).Scan(&schema); err != nil {
		t.Fatalf("read sessions schema: %v", err)
	}
	for _, harness := range []string{"'muse'", "'qm'", "'kimchi'", "'prime-agent'"} {
		if !strings.Contains(schema, harness) {
			t.Fatalf("sessions.harness CHECK is missing %s after repair:\n%s", harness, schema)
		}
	}
	var primeAgentSessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE harness = 'prime-agent'`).Scan(&primeAgentSessions); err != nil {
		t.Fatalf("count prime-agent sessions: %v", err)
	}
	if primeAgentSessions != 1 {
		t.Fatalf("prime-agent session count = %d, want 1", primeAgentSessions)
	}
}

// TestMigrateRepairsOMPHarnessConstraint reproduces a dev profile that has the
// OMP migration recorded but still carries the pre-OMP physical CHECK
// constraint. Startup must repair the schema so new OMP sessions can be
// inserted without losing existing harness variants.
func TestMigrateRepairsOMPHarnessConstraint(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 94)
	if _, err := db.Exec(
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`,
		95,
	); err != nil {
		t.Fatalf("seed migration 95: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate pre-omp profile: %v", err)
	}
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='sessions'",
	).Scan(&schema); err != nil {
		t.Fatalf("read sessions schema: %v", err)
	}
	for _, harness := range []string{"'omp'"} {
		if !strings.Contains(schema, harness) {
			t.Fatalf("sessions.harness CHECK is missing %s after repair:\n%s", harness, schema)
		}
	}
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, config)
VALUES ('agent-orchestrator', '/repo/agent-orchestrator', ?, '{}');
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('agent-orchestrator-1', 'agent-orchestrator', 1, 'omp', ?, ?, ?);
`, time.Unix(100, 0).UTC(), time.Unix(101, 0).UTC(), time.Unix(101, 0).UTC(), time.Unix(101, 0).UTC()); err != nil {
		t.Fatalf("insert omp session after repair: %v", err)
	}
}

func TestOpenReadOnlyDoesNotCreateDatabase(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenReadOnly(context.Background(), dataDir); err == nil {
		t.Fatal("OpenReadOnly succeeded for missing database")
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("data dir stat err = %v, want not exist", err)
	}
}

func TestOpenReadOnlyDoesNotMigrate(t *testing.T) {
	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL,
    repo_origin_url TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    registered_at TIMESTAMP NOT NULL,
    archived_at TIMESTAMP
);
INSERT INTO projects (id, path, registered_at) VALUES ('alpha', '/repos/alpha', ?);
`, time.Unix(100, 0).UTC()); err != nil {
		_ = db.Close()
		t.Fatalf("seed old schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	store, err := OpenReadOnly(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.ListProjects(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Fatalf("ListProjects err = %v, want old-schema column failure", err)
	}

	checkDB, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open check db: %v", err)
	}
	defer func() { _ = checkDB.Close() }()

	var schema string
	if err := checkDB.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='projects'",
	).Scan(&schema); err != nil {
		t.Fatalf("read projects schema: %v", err)
	}
	if strings.Contains(schema, "config") || strings.Contains(schema, "kind") {
		t.Fatalf("OpenReadOnly migrated projects schema:\n%s", schema)
	}
}
