package sqlite

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// shippedMigrations freezes every migration version that has shipped in a
// release, mapped to its exact filename. Once a version number is recorded in
// a user's goose_db_version, any later file reusing that number is silently
// skipped and surfaces as an opaque 500 from a generated query (issue #3475:
// a burned version 40 skipped 0040_add_session_diff_base.sql and killed the
// session list). Renaming, renumbering, or deleting a shipped migration
// produces exactly that class of failure, so this list is append-only.
var shippedMigrations = map[int64]string{
	1:   "0001_init.sql",
	2:   "0002_remove_activity_source.sql",
	3:   "0003_add_session_display_name.sql",
	4:   "0004_scm_observer_schema.sql",
	5:   "0005_pr_last_nudge_signature.sql",
	6:   "0006_pr_session_changed_cdc.sql",
	7:   "0007_allow_implemented_harnesses.sql",
	8:   "0008_add_project_config.sql",
	9:   "0009_workspace_projects.sql",
	10:  "0010_add_first_signal_at.sql",
	11:  "0011_notifications.sql",
	12:  "0012_add_review_tables.sql",
	13:  "0013_review_run_unique_sha.sql",
	14:  "0014_review_run_retry_failed.sql",
	15:  "0015_telemetry_events.sql",
	16:  "0016_review_run_github_review_id.sql",
	17:  "0017_add_session_preview_url.sql",
	18:  "0018_review_run_delivered_at.sql",
	19:  "0019_add_session_preview_revision.sql",
	20:  "0020_review_run_unique_pr_sha.sql",
	21:  "0021_pr_reviews.sql",
	23:  "0023_review_run_retry_cancelled.sql",
	24:  "0024_session_rename_cdc.sql",
	25:  "0025_worker_idle_outbox.sql",
	26:  "0026_allow_fake_harness.sql",
	27:  "0027_shell_terminals.sql",
	28:  "0028_allow_scratch_projects.sql",
	29:  "0029_pr_reviews_body.sql",
	30:  "0030_session_cleanup_facts.sql",
	31:  "0031_notification_history_pagination.sql",
	32:  "0032_pr_state_changed_at.sql",
	33:  "0033_add_session_runtime_launch_id.sql",
	34:  "0034_add_session_workspace_repo_path.sql",
	35:  "0035_add_session_terminate_on_pr_merge.sql",
	36:  "0036_add_shell_terminal_session_id.sql",
	37:  "0037_drop_worker_idle_outbox.sql",
	38:  "0038_orchestrator_reengagement.sql",
	39:  "0039_drop_orchestrator_reengagement.sql",
	40:  "0040_add_session_diff_base.sql",
	41:  "0041_notification_resolution.sql",
	42:  "0042_review_run_unique_per_harness.sql",
	43:  "0043_add_session_pinned.sql",
	44:  "0044_backfill_review_run_batch_id.sql",
	47:  "0047_agent_model_catalog.sql",
	48:  "0048_review_agent_session_id.sql",
	49:  "0049_review_per_harness.sql",
	52:  "0052_model_usage.sql",
	53:  "0053_allow_muse_harness.sql",
	54:  "0054_allow_kimchi_harness.sql",
	66:  "0066_chat_session_mode.sql",
	67:  "0067_app_settings.sql",
	68:  "0068_conversation_turn_settings.sql",
	69:  "0069_conversation_compaction.sql",
	70:  "0070_command_output_and_diffs.sql",
	71:  "0071_conversation_usage.sql",
	72:  "0072_conversation_history_ops.sql",
	73:  "0073_conversation_provider_state.sql",
	74:  "0074_activity_kinds_mcp_and_auto_review.sql",
	75:  "0075_conversation_user_input.sql",
	76:  "0076_conversation_delivery_content_and_cost.sql",
	77:  "0077_cancelled_conversation_activities.sql",
	78:  "0078_session_interface_transitions.sql",
	79:  "0079_session_interface_transition_delivery.sql",
	80:  "0080_review_per_harness.sql",
	81:  "0081_browser_capability_verifier.sql",
	82:  "0082_allow_prime_agent_harness.sql",
	83:  "0083_reconcile_kimchi_prime_agent_harnesses.sql",
	84:  "0084_add_session_auto_inject_review.sql",
	85:  "0085_agent_switching.sql",
	86:  "0086_workspace_repo_default_branch.sql",
	87:  "0087_conversation_branches.sql",
	88:  "0088_add_auto_inject_ci_toggle.sql",
	89:  "0089_conversation_turn_promotion.sql",
	90:  "0090_workspace_git_status.sql",
	91:  "0091_session_auto_review.sql",
	92:  "0092_pr_reviews_target_sha.sql",
	93:  "0093_review_run_trigger_source.sql",
	94:  "0094_agent_switch_recovery.sql",
	95:  "0095_allow_omp_harness.sql",
	96:  "0096_session_worktree_base_ref.sql",
	97:  "0097_pr_provider_identity.sql",
	98:  "0098_session_native_identity_generation.sql",
	99:  "0099_interface_transition_notice_acknowledgement.sql",
	100: "0100_session_model.sql",
	101: "0101_conversation_provider_ownership_epochs.sql",
	102: "0102_canonical_usage.sql",
	103: "0103_review_run_cdc.sql",
	104: "0104_agent_inventory_cache.sql",
	105: "0105_default_session_mode_chat.sql",
	106: "0106_pr_comment_review_id.sql",
	107: "0107_recovered_conversation_turns.sql",
	108: "0108_conversation_retry_source.sql",
	109: "0109_session_latest_user_prompt_at.sql",
	110: "0110_approximate_conversation_branches.sql",
	111: "0111_pr_auto_inject_ci_cdc.sql",
	112: "0112_app_settings_cloud_offering.sql",
	113: "0113_usage_cost_estimation.sql",
	114: "0114_usage_cost_candidate_canonical_index.sql",
	115: "0115_usage_measurement_and_provider_usage.sql",
	116: "0116_usage_billing_provider_source.sql",
	117: "0117_allow_kimi_usage.sql",
	118: "0118_cancelled_conversation_turns.sql",
	119: "0119_finalize_completed_conversation_plans.sql",
	120: "0120_normalize_activity_last_at.sql",
	121: "0121_session_reviewer_agent_config.sql",
	122: "0122_drop_agent_inventory_cache.sql",
	123: "0123_agent_install_jobs.sql",
	124: "0124_codex_account_management.sql",
	125: "0125_agent_switch_failure_observability.sql",
	126: "0126_canonical_repository_identity.sql",
	127: "0127_session_permissions.sql",
	128: "0128_pr_author_avatar_url.sql",
}

// burnedVersion reports version numbers that must never be (re)used: they
// shipped in a release and were then deleted, so real installs have them
// recorded as applied while this repository ships no file with that number. A
// new file claiming one would be skipped silently there.
//
//   - 22 shipped in a nightly (#2412) and was deleted by the revert.
//
// Beware of the adjacent hazard this cannot catch: at least one field profile
// has versions 40 through 51 recorded as applied by a foreign build
// (#3475/#3476), so migrations numbered up to 0051 are skipped there entirely.
// Any such migration whose schema the generated queries depend on must add a
// schemaRepairs entry in db.go.
func burnedVersion(v int64) bool {
	return v == 22
}

// TestMigrationVersionLedger enforces the append-only migration ledger: every
// migration file has exactly one entry in shippedMigrations (adding a
// migration means appending its entry in the same change, where a review can
// see the claimed number), no shipped file is renamed or deleted, and no file
// reuses a burned number. Renumbering or deleting a released migration is what
// produces silently-skipped migrations and unexplainable 500s months later
// (issue #3475).
func TestMigrationVersionLedger(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}

	present := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		version, err := goose.NumericComponent(e.Name())
		if err != nil {
			t.Errorf("migration %q has no version goose can parse: %v", e.Name(), err)
			continue
		}
		// Two files claiming one number is the failure this ledger exists to
		// prevent, and it is easy to reach from a branch: goose refuses to
		// start at all, so it takes down the daemon rather than one query.
		if other, clash := present[version]; clash {
			t.Errorf("migrations %q and %q both claim version %d: goose refuses to start on a duplicate version",
				other, e.Name(), version)
		}
		present[version] = e.Name()

		if burnedVersion(version) {
			t.Errorf("migration %q claims burned version %d: that number is recorded as applied on real installs, so this file would be skipped silently there; use a fresh number",
				e.Name(), version)
			continue
		}
		shipped, ok := shippedMigrations[version]
		switch {
		case ok && shipped != e.Name():
			t.Errorf("migration version %d was renamed from %q to %q: installs that already applied it will never run the new file", version, shipped, e.Name())
		case !ok:
			t.Errorf("migration %q (version %d) is not in the shippedMigrations ledger; append it in the same change so the claimed number is reviewed",
				e.Name(), version)
		}
	}

	for version, name := range shippedMigrations {
		if _, ok := present[version]; !ok {
			t.Errorf("ledgered migration %q (version %d) was deleted: installs that have not applied it yet will silently miss its schema, and the number is burned for reuse", name, version)
		}
	}
}

// A concurrently approved branch can claim the next free number first, so this
// branch's migrations have to survive being applied to a database that already
// records a version they never shipped. goose runs with WithAllowMissing, which
// is what makes the resulting gap harmless.
func TestMigrationsApplyOverAForeignInterleavedVersion(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	upTo(t, db, 103)

	// Stand in for the other branch's migration: applied here, absent from this
	// tree, and numbered below everything this branch adds.
	if _, err := db.Exec(
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (104, 1)`,
	); err != nil {
		t.Fatalf("seed foreign migration: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate over a foreign interleaved version: %v", err)
	}
	for table, wantColumns := range expectedUsageTableColumns {
		if got := tableColumns(t, db, table); !reflect.DeepEqual(got, wantColumns) {
			t.Errorf("%s columns = %v, want %v", table, got, wantColumns)
		}
	}
	var duplicates int
	if err := db.QueryRow(`
SELECT COUNT(*) FROM (
    SELECT version_id FROM goose_db_version GROUP BY version_id HAVING COUNT(*) > 1
)`).Scan(&duplicates); err != nil || duplicates != 0 {
		t.Fatalf("duplicate applied versions = %d, err = %v", duplicates, err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("repeat migration over a foreign interleaved version: %v", err)
	}
}

// TestSessionListSucceedsOnBurnedMigrationHistory reproduces the #3475/#3476
// field profile: goose_db_version already records versions 40 through 51
// (written by a foreign build), so goose silently skips the real
// 0040_add_session_diff_base.sql and the sessions table lacks the diff-base
// columns. Startup schema reconciliation must repair the physical schema so
// the session list works instead of returning 500 INTERNAL_ERROR.
func TestSessionListSucceedsOnBurnedMigrationHistory(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 39) // the real 0040 has not run; diff-base columns are absent
	for v := 40; v <= 51; v++ {
		if _, err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, v,
		); err != nil {
			t.Fatalf("seed burned version %d: %v", v, err)
		}
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate burned profile: %v", err)
	}

	ctx := t.Context()
	store := sqlitestore.NewStore(db, db)
	if _, err := db.Exec(`
INSERT INTO projects (
	id, path, repo_origin_url, display_name, registered_at, config, kind
) VALUES (?, ?, ?, ?, ?, ?, ?)
`,
		"mer",
		`C:\src\mer`,
		"https://example.com/mer.git",
		"Mer",
		"2026-07-23T00:00:00Z",
		`{"worker":{"agent":"claude-code"}}`,
		"single_repo",
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	rec := domain.SessionRecord{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessClaudeCode,
		Activity:  domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{
			Branch:        "ao/mer-1/root",
			WorkspacePath: `C:\Users\mer\.ao\data\worktrees\mer\mer-1`,
			DiffBaseSHA:   "0f0e0d0c0b0a09080706050403020100ffeeddcc",
			DiffBaseRef:   "refs/remotes/origin/main",
		},
	}
	created, err := store.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create session on repaired schema: %v", err)
	}

	sessions, err := store.ListAllSessions(ctx)
	if err != nil {
		t.Fatalf("session list on burned profile: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatalf("sessions = %+v, want the created session", sessions)
	}
	if sessions[0].Metadata.DiffBaseSHA != rec.Metadata.DiffBaseSHA || sessions[0].Metadata.DiffBaseRef != rec.Metadata.DiffBaseRef {
		t.Fatalf("diff base metadata = (%q, %q), want it round-tripped",
			sessions[0].Metadata.DiffBaseSHA, sessions[0].Metadata.DiffBaseRef)
	}
	now := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	if err := store.UpsertReview(ctx, domain.Review{
		ID:               "review-1",
		SessionID:        created.ID,
		ProjectID:        "mer",
		Harness:          domain.ReviewerCodex,
		ReviewerHandleID: "review-mer-1",
		AgentSessionID:   "reviewer-native-1",
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("upsert review on repaired schema: %v", err)
	}
	review, ok, err := store.GetReviewBySessionAndHarness(ctx, created.ID, domain.ReviewerCodex)
	if err != nil {
		t.Fatalf("get review on repaired schema: %v", err)
	}
	if !ok || review.AgentSessionID != "reviewer-native-1" {
		t.Fatalf("review = %+v, ok=%v, want persisted reviewer native id", review, ok)
	}

	// The repair is idempotent: a second startup on the repaired database (and
	// on any healthy database) is a no-op, never a duplicate-column error.
	if err := migrate(db); err != nil {
		t.Fatalf("repeat migrate on repaired schema: %v", err)
	}
	for table, want := range map[string][]string{
		"sessions":      {"diff_base_sha", "diff_base_ref", "reviewer_harness", "browser_capability_verifier", "auto_inject_review", "auto_inject_ci"},
		"pr":            {"auto_inject_ci"},
		"notifications": {"resolved_at"},
	} {
		for _, column := range want {
			var columns int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
			).Scan(&columns); err != nil {
				t.Fatal(err)
			}
			if columns != 1 {
				t.Fatalf("%s.%s count = %d, want exactly 1 after repair", table, column, columns)
			}
		}
	}
}
