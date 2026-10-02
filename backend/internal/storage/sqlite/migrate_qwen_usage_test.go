package sqlite

import (
	"testing"
	"time"
)

// TestMigration0171DownPreservesKimiUsage catches rolling back Qwen by
// deleting sibling-provider usage owned by an earlier migration.
func TestMigration0171DownPreservesKimiUsage(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 170)
	upTo(t, db, 171)
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO projects (id, path, display_name, registered_at) VALUES ('usage-migration', '/tmp/usage-migration', 'usage', ?)`, now); err != nil {
		t.Fatal(err)
	}
	harnesses := []string{"kimi", "qwen"}
	kinds := map[string]string{"kimi": "kimi_wire", "qwen": "qwen_monthly"}
	for index, harness := range harnesses {
		if _, err := db.Exec(`INSERT INTO sessions (id, project_id, num, kind, harness, activity_state, activity_last_at, is_terminated, created_at, updated_at) VALUES (?, 'usage-migration', ?, 'worker', ?, 'active', ?, 0, ?, ?)`, "session-"+harness, index+1, harness, now, now, now); err != nil {
			t.Fatalf("seed %s session: %v", harness, err)
		}
		result, err := db.Exec(`INSERT INTO usage_bindings (session_id, harness, native_root_id, state, updated_at) VALUES (?, ?, ?, 'active', ?)`, "session-"+harness, harness, "native-"+harness, now)
		if err != nil {
			t.Fatalf("seed %s binding: %v", harness, err)
		}
		bindingID, _ := result.LastInsertId()
		result, err = db.Exec(`INSERT INTO usage_sources (binding_id, kind, native_session_id, artifact_path, state, updated_at) VALUES (?, ?, ?, ?, 'active', ?)`, bindingID, kinds[harness], "native-"+harness, "/tmp/"+harness+".jsonl", now)
		if err != nil {
			t.Fatalf("seed %s source: %v", harness, err)
		}
		sourceID, _ := result.LastInsertId()
		_, err = db.Exec(`INSERT INTO model_usage_events (
            binding_id, usage_source_id, provider_id, model_id,
            usage_measurement_kind, input_tokens, cached_input_tokens,
            uncached_input_tokens, output_tokens, provider_usage_json,
            source_event_key, created_at
        ) VALUES (?, ?, 'openai', 'model', 'native_reported', 3, 1, 2, 1,
                  '{"totalTokens":4}', ?, ?)`, bindingID, sourceID, "event-"+harness, now)
		if err != nil {
			t.Fatalf("seed %s event: %v", harness, err)
		}
	}

	downTo(t, db, 170)
	counts := map[string]int{}
	for _, harness := range harnesses {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM model_usage_events event JOIN usage_bindings binding ON binding.id = event.binding_id WHERE binding.harness = ?`, harness).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[harness] = count
	}
	if counts["kimi"] != 1 || counts["qwen"] != 0 {
		t.Fatalf("events after Qwen rollback = %+v, want kimi:1 qwen:0", counts)
	}
	if _, err := db.Exec(`INSERT INTO usage_bindings (session_id, harness, native_root_id, state, updated_at) VALUES ('session-kimi', 'qwen', 'new-qwen', 'active', ?)`, now); err == nil {
		t.Fatal("rollback still admits Qwen usage bindings")
	}
	if _, err := db.Exec(`INSERT INTO usage_bindings (session_id, harness, native_root_id, state, updated_at) VALUES ('session-kimi', 'pi', 'new-pi', 'active', ?)`, now); err == nil {
		t.Fatal("rollback admits Pi usage absent from predecessor schema")
	}
}
