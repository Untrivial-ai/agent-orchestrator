package usage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// claudeAssistantLine is one priced assistant turn. The transcript names no
// provider, which is the whole point: Claude never does.
const claudeAssistantLine = `{"type":"assistant","isSidechain":false,"uuid":"u1","timestamp":"2026-07-01T10:00:00Z","message":{"id":"msg-1","model":"claude-test","stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":4}}}` + "\n"

func seedClaudeChatSource(
	t *testing.T, dataDir string, mode domain.SessionMode, providerHint string,
) (*sqlite.Store, domain.UsageSourceRecord, string, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	store, err := sqlitetest.Open(dataDir)
	mustNoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	mustNoError(t, store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "usage", Path: t.TempDir(), RegisteredAt: now,
	}))
	session, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "usage",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessClaudeCode,
		Mode:      mode,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now,
		UpdatedAt: now,
	})
	mustNoError(t, err)

	binding, err := store.UpsertUsageBinding(ctx, domain.UsageBindingRecord{
		SessionID:    session.ID,
		Harness:      domain.HarnessClaudeCode,
		NativeRootID: "claude-root",
		ProviderHint: providerHint,
		State:        domain.UsageBindingActive,
		UpdatedAt:    now,
	})
	mustNoError(t, err)

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	mustNoError(t, os.WriteFile(path, []byte(claudeAssistantLine), 0o600))
	path = canonicalTranscriptPath(path)
	identity, err := usagesvc.SourceIdentity(ctx, path)
	mustNoError(t, err)
	source, err := store.InsertUsageSource(ctx, domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceClaudeMain,
		NativeSessionID: "claude-root",
		ArtifactPath:    path,
		FileIdentity:    identity,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now,
	})
	mustNoError(t, err)
	return store, source, path, now
}

type persistedAttribution struct {
	provider sql.NullString
	source   sql.NullString
	total    sql.NullInt64
}

func readAttribution(t *testing.T, dataDir string, sourceID int64) persistedAttribution {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	mustNoError(t, err)
	defer func() { _ = db.Close() }()
	var got persistedAttribution
	mustNoError(t, db.QueryRow(`SELECT billing_provider_id, billing_provider_source, estimated_cost_nanos
FROM model_usage_events WHERE usage_source_id = ?`, sourceID).Scan(&got.provider, &got.source, &got.total))
	return got
}

// A chat session has no terminal hook, so an empty route hint on one is final
// rather than early. Deferring to the legacy repairer there leaves the session
// unpriced until the next daemon start, so ingestion resolves it directly.
func TestIngestorInfersChatAttributionFromServedModel(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, source, _, now := seedClaudeChatSource(t, dataDir, domain.SessionModeChat, "")
	snapshot := testPricingSnapshot(t, "1")

	ingestor := NewIngestor(store, IngestorConfig{
		Clock:   func() time.Time { return now },
		Pricing: pricing.NewManager(snapshot),
	})
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	got := readAttribution(t, dataDir, source.ID)
	if got.provider.String != "anthropic" {
		t.Fatalf("billing_provider_id = %+v, want %q", got.provider, "anthropic")
	}
	if got.source.String != string(domain.UsageBillingProviderInferred) {
		t.Fatalf("billing_provider_source = %+v, want %q", got.source, domain.UsageBillingProviderInferred)
	}
	if !got.total.Valid {
		t.Fatal("chat event stayed unpriced; want a write-time estimate")
	}
}

// The guard that matters: a TUI session's hook may still be coming, so
// ingestion must not pre-empt it by guessing from the model name.
func TestIngestorLeavesTUIAttributionToItsHook(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, source, _, now := seedClaudeChatSource(t, dataDir, domain.SessionModeTUI, "")
	snapshot := testPricingSnapshot(t, "1")

	ingestor := NewIngestor(store, IngestorConfig{
		Clock:   func() time.Time { return now },
		Pricing: pricing.NewManager(snapshot),
	})
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	got := readAttribution(t, dataDir, source.ID)
	if got.provider.Valid {
		t.Fatalf("billing_provider_id = %+v, want NULL until a hook names the route", got.provider)
	}
	if got.total.Valid {
		t.Fatalf("estimated_cost_nanos = %+v, want NULL", got.total)
	}
}

// A hook that ran and could not name the route has already answered the
// question the model would be used to guess. Inferring anthropic from a bare
// claude-* name would price a proxy at Anthropic list rates, with no
// observation coming to correct it.
func TestIngestorSkipsChatInferenceForUnidentifiedRoute(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, source, _, now := seedClaudeChatSource(
		t, dataDir, domain.SessionModeChat, pricing.UnidentifiedBillingRoute,
	)
	snapshot := testPricingSnapshot(t, "1")

	ingestor := NewIngestor(store, IngestorConfig{
		Clock:   func() time.Time { return now },
		Pricing: pricing.NewManager(snapshot),
	})
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	got := readAttribution(t, dataDir, source.ID)
	if got.provider.Valid {
		t.Fatalf("billing_provider_id = %+v, want NULL for an unidentified route", got.provider)
	}
}
