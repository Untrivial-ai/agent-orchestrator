package sqlite

import (
	"database/sql"
	"reflect"
	"testing"
	"time"
)

// TestMigration0167AddsPiAndPreservesKimiUsage verifies the declared Kimi → Pi
// merge order in both directions. Rebuilding the constrained collection tables
// must preserve Kimi row IDs and events while adding and removing only Pi.
func TestMigration0167AddsPiAndPreservesKimiUsage(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 166)
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO projects (id, path, display_name, registered_at)
		VALUES ('usage-migration', '/tmp/usage-migration', 'usage', ?)`, now); err != nil {
		t.Fatal(err)
	}
	for number, harness := range []string{"kimi", "pi"} {
		if _, err := db.Exec(`INSERT INTO sessions (
			id, project_id, num, kind, harness, activity_state, activity_last_at,
			is_terminated, created_at, updated_at
		) VALUES (?, 'usage-migration', ?, 'worker', ?, 'active', ?, 0, ?, ?)`,
			"session-"+harness, number+1, harness, now, now, now); err != nil {
			t.Fatalf("seed %s session: %v", harness, err)
		}
	}

	kimiBindingResult, err := db.Exec(`INSERT INTO usage_bindings (
		session_id, harness, native_root_id, provider_hint, state, updated_at
	) VALUES ('session-kimi', 'kimi', 'native-kimi', 'moonshot', 'active', ?)`, now)
	if err != nil {
		t.Fatalf("seed Kimi binding: %v", err)
	}
	kimiBindingID, _ := kimiBindingResult.LastInsertId()
	kimiSourceResult, err := db.Exec(`INSERT INTO usage_sources (
		binding_id, kind, native_session_id, artifact_path, state, updated_at
	) VALUES (?, 'kimi_wire', 'native-kimi', '/tmp/kimi.jsonl', 'active', ?)`, kimiBindingID, now)
	if err != nil {
		t.Fatalf("seed Kimi source: %v", err)
	}
	kimiSourceID, _ := kimiSourceResult.LastInsertId()
	kimiEventResult, err := db.Exec(`INSERT INTO model_usage_events (
		binding_id, usage_source_id, provider_id, billing_provider_id,
		billing_provider_source, model_id, usage_measurement_kind,
		input_tokens, cached_input_tokens, uncached_input_tokens, output_tokens,
		provider_usage_json, source_event_key, created_at,
		input_cost_nanos, cached_input_cost_nanos, output_cost_nanos,
		estimated_cost_nanos, pricing_version
	) VALUES (?, ?, 'anthropic', 'moonshot', 'observed', 'kimi-test',
		'native_reported', 3, 1, 2, 1, '{"input_tokens":2}', 'event-kimi', ?,
		20, 5, 10, 35, 'catalog-v1')`, kimiBindingID, kimiSourceID, now)
	if err != nil {
		t.Fatalf("seed Kimi event: %v", err)
	}
	kimiEventID, _ := kimiEventResult.LastInsertId()

	upTo(t, db, 167)
	for table, wantColumns := range expectedUsageTableColumns {
		if got := tableColumns(t, db, table); !reflect.DeepEqual(got, wantColumns) {
			t.Fatalf("%s columns after Pi migration = %v, want %v", table, got, wantColumns)
		}
	}
	assertKimiMigrationRows(t, db, kimiBindingID, kimiSourceID, kimiEventID)

	piBindingResult, err := db.Exec(`INSERT INTO usage_bindings (
		session_id, harness, native_root_id, state, updated_at
	) VALUES ('session-pi', 'pi', 'native-pi', 'active', ?)`, now)
	if err != nil {
		t.Fatalf("seed Pi binding: %v", err)
	}
	piBindingID, _ := piBindingResult.LastInsertId()
	piSourceResult, err := db.Exec(`INSERT INTO usage_sources (
		binding_id, kind, native_session_id, artifact_path, state, updated_at
	) VALUES (?, 'pi_session', 'native-pi', '/tmp/pi.jsonl', 'active', ?)`, piBindingID, now)
	if err != nil {
		t.Fatalf("seed Pi source: %v", err)
	}
	piSourceID, _ := piSourceResult.LastInsertId()
	if _, err := db.Exec(`INSERT INTO model_usage_events (
		binding_id, usage_source_id, provider_id, model_id, usage_measurement_kind,
		input_tokens, cached_input_tokens, uncached_input_tokens, output_tokens,
		provider_usage_json, source_event_key, created_at
	) VALUES (?, ?, 'openai', 'gpt-test', 'native_reported', 3, 1, 2, 1,
		'{"input":2,"cacheRead":1}', 'event-pi', ?)`, piBindingID, piSourceID, now); err != nil {
		t.Fatalf("seed Pi event: %v", err)
	}

	downTo(t, db, 166)
	assertKimiMigrationRows(t, db, kimiBindingID, kimiSourceID, kimiEventID)
	var piEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_usage_events WHERE source_event_key = 'event-pi'`).Scan(&piEvents); err != nil {
		t.Fatal(err)
	}
	if piEvents != 0 {
		t.Fatalf("Pi events after rollback = %d, want 0", piEvents)
	}
	if _, err := db.Exec(`INSERT INTO usage_bindings (
		session_id, harness, native_root_id, state, updated_at
	) VALUES ('session-pi', 'pi', 'pi-after-down', 'active', ?)`, now); err == nil {
		t.Fatal("Pi binding remained allowed after rolling migration 0167 back")
	}
}

func assertKimiMigrationRows(t *testing.T, db *sql.DB, bindingID, sourceID, eventID int64) {
	t.Helper()
	var gotBindingID, gotSourceID, gotEventID int64
	var providerHint, sourceKind, sourceEventKey string
	if err := db.QueryRow(`SELECT id, provider_hint FROM usage_bindings
		WHERE native_root_id = 'native-kimi'`).Scan(&gotBindingID, &providerHint); err != nil {
		t.Fatalf("read Kimi binding: %v", err)
	}
	if err := db.QueryRow(`SELECT id, kind FROM usage_sources
		WHERE native_session_id = 'native-kimi'`).Scan(&gotSourceID, &sourceKind); err != nil {
		t.Fatalf("read Kimi source: %v", err)
	}
	if err := db.QueryRow(`SELECT id, source_event_key FROM model_usage_events
		WHERE binding_id = ? AND usage_source_id = ?`, bindingID, sourceID).Scan(&gotEventID, &sourceEventKey); err != nil {
		t.Fatalf("read Kimi event: %v", err)
	}
	if gotBindingID != bindingID || gotSourceID != sourceID || gotEventID != eventID ||
		providerHint != "moonshot" || sourceKind != "kimi_wire" || sourceEventKey != "event-kimi" {
		t.Fatalf("Kimi rows = binding:%d source:%d event:%d provider:%q kind:%q key:%q",
			gotBindingID, gotSourceID, gotEventID, providerHint, sourceKind, sourceEventKey)
	}
}
