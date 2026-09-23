package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing/catalogsync"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// Break caught: activating a new catalog after estimation but before the
// source event commit could leave an old-version event behind after the new
// provider backfill had already passed it.
func TestIngestorHoldsPricingFenceThroughApplyUsageChunk(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, source, path, now := seedCodexIngestionSource(t, dataDir)
	content := `{"type":"session_meta","payload":{"model_provider":"openai"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-test"}}` + "\n" +
		string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(content), 0o600))

	oldSnapshot := testPricingSnapshot(t, "0.000001")
	newSnapshot := testPricingSnapshot(t, "0.000002")
	manager := pricing.NewManager(oldSnapshot)
	interleaved := &applyInterleavingStore{Store: store}
	activationAdmission := make(chan struct{})
	activationDone := make(chan error, 1)
	interleaved.beforeApply = func() {
		observedCtx := &fenceAdmissionContext{Context: ctx, admitted: activationAdmission}
		go func() {
			_, err := manager.Activate(observedCtx, newSnapshot)
			activationDone <- err
		}()
		<-activationAdmission
		select {
		case err := <-activationDone:
			t.Fatalf("activation crossed the ingestion commit fence: %v", err)
		default:
		}
	}

	ingestor := NewIngestor(interleaved, IngestorConfig{
		Clock:   func() time.Time { return now },
		Pricing: manager,
	})
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := <-activationDone; err != nil {
		t.Fatalf("Activate: %v", err)
	}

	var total sql.NullInt64
	var version string
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	mustNoError(t, err)
	defer func() { _ = db.Close() }()
	mustNoError(t, db.QueryRowContext(ctx, `
SELECT estimated_cost_nanos, pricing_version
FROM model_usage_events
WHERE usage_source_id = ?`, source.ID).Scan(&total, &version))
	if !total.Valid || total.Int64 != 86_000 {
		t.Fatalf("estimated total = %+v, want 86000 nano-USD", total)
	}
	if want := oldSnapshot.ProviderVersion("openai"); version != want {
		t.Fatalf("pricing version = %q, want pre-activation %q", version, want)
	}
}

// Break caught: shutdown cancellation while ingestion waits for catalog
// activation admission must leave both the source cursor and events untouched.
func TestIngestorCancelsWhileWaitingForPricingFence(t *testing.T) {
	dataDir := t.TempDir()
	store, source, path, now := seedCodexIngestionSource(t, dataDir)
	content := `{"type":"session_meta","payload":{"model_provider":"openai"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-test"}}` + "\n" +
		string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(content), 0o600))
	manager := pricing.NewManager(testPricingSnapshot(t, "0.000001"))
	release, err := manager.Fence().Acquire(context.Background())
	mustNoError(t, err)
	defer release()

	readComplete := make(chan struct{})
	ingestor := NewIngestor(store, IngestorConfig{
		Clock: func() time.Time { return now }, Pricing: manager,
	})
	ingestor.afterRead = func() { close(readComplete) }
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, ingestErr := ingestor.Ingest(ctx, source.ID)
		result <- ingestErr
	}()
	<-readComplete
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Ingest error = %v, want context canceled", err)
	}
	persisted, ok, err := store.GetUsageSourceForIngestion(context.Background(), source.ID)
	mustNoError(t, err)
	if !ok || persisted.Source.ByteOffset != 0 {
		t.Fatalf("canceled source = %+v ok=%v", persisted.Source, ok)
	}
	aggregates, err := store.ListUsageModelAggregates(context.Background(), sourceSessionID(t, store, source.ID))
	mustNoError(t, err)
	if len(aggregates) != 0 {
		t.Fatalf("canceled ingestion persisted events: %+v", aggregates)
	}
}

// Break caught: optional price arithmetic failure must not strand valid token
// facts or their durable transcript cursor in an endless retry loop.
func TestIngestorPersistsUsageUnpricedWhenEstimationOverflows(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, source, path, now := seedCodexIngestionSource(t, dataDir)
	content := `{"type":"session_meta","payload":{"model_provider":"openai"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-test"}}` + "\n" +
		string(codexTokenLine("overflow", int64(^uint64(0)>>1), 0, 0, 0, 0)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(content), 0o600))
	snapshot := testPricingSnapshot(t, "1")
	pricingErrors := make(chan error, 1)
	ingestor := NewIngestor(store, IngestorConfig{
		Clock:          func() time.Time { return now },
		Pricing:        pricing.NewManager(snapshot),
		OnPricingError: func(err error) { pricingErrors <- err },
	})
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	select {
	case err := <-pricingErrors:
		if err == nil {
			t.Fatal("nil pricing error callback")
		}
	default:
		t.Fatal("pricing overflow was not reported")
	}

	persisted, ok, err := store.GetUsageSourceForIngestion(ctx, source.ID)
	mustNoError(t, err)
	if !ok || persisted.Source.ByteOffset != int64(len(content)) {
		t.Fatalf("overflow source cursor = %+v ok=%v", persisted.Source, ok)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	mustNoError(t, err)
	defer func() { _ = db.Close() }()
	var total sql.NullInt64
	var version string
	var input int64
	mustNoError(t, db.QueryRow(`SELECT input_tokens, estimated_cost_nanos, pricing_version
FROM model_usage_events WHERE usage_source_id = ?`, source.ID).Scan(&input, &total, &version))
	if input != int64(^uint64(0)>>1) || total.Valid || version != snapshot.ProviderVersion("openai") {
		t.Fatalf("overflow event = input %d total %+v version %q", input, total, version)
	}
}

func TestIngestorPersistsVersionedCodexParserStateAcrossChunks(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, source, path, now := seedCodexIngestionSource(t, dataDir)

	first := `{"type":"session_meta","payload":{"model_provider":"openai"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-5.6"}}` + "\n" +
		string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(first), 0o600))
	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("ingest first chunk: %v", err)
	}

	state := readParserStateJSON(t, dataDir, source.ID)
	assertCodexParserState(t, state, 100, "gpt-5.6")

	second := string(codexTokenLine("2026-07-28T10:01:00Z", 160, 90, 0, 35, 8)) + "\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	mustNoError(t, err)
	if _, err := file.WriteString(second); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	mustNoError(t, file.Close())
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("ingest second chunk: %v", err)
	}
	assertTokenAggregate(t, store, sourceSessionID(t, store, source.ID), 195)
	assertCodexParserState(t, readParserStateJSON(t, dataDir, source.ID), 160, "gpt-5.6")
}

func TestIngestorRejectsInvalidPersistedParserStateWithoutAdvancing(t *testing.T) {
	tests := []struct {
		name            string
		state           string
		replaceArtifact bool
	}{
		{name: "malformed", state: `{"version":`},
		{name: "malformed after artifact replacement", state: `{"version":`, replaceArtifact: true},
		{name: "non object", state: `[]`},
		{name: "unknown field", state: `{"version":1,"source_kind":"codex_rollout","codex":{},"extra":true}`},
		{name: "wrong source kind", state: `{"version":1,"source_kind":"claude_main","codex":{}}`},
		{name: "unsupported version", state: `{"version":2,"source_kind":"codex_rollout","codex":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			dataDir := t.TempDir()
			store, source, path, now := seedCodexIngestionSource(t, dataDir)
			line := string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
			mustNoError(t, os.WriteFile(path, []byte(line), 0o600))
			if test.replaceArtifact {
				replacement := path + ".replacement"
				mustNoError(t, os.WriteFile(replacement, []byte(line), 0o600))
				mustNoError(t, os.Rename(replacement, path))
			}
			writeParserStateJSON(t, dataDir, source.ID, test.state)

			ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
			result, err := ingestor.Ingest(ctx, source.ID)
			if err == nil || !strings.Contains(err.Error(), "parser state") {
				t.Fatalf("ingest error = %v, want parser state collection error", err)
			}
			if result.RetryAt == nil {
				t.Fatalf("result = %+v, want retry schedule", result)
			}
			got, ok, getErr := store.GetUsageSourceForIngestion(ctx, source.ID)
			if getErr != nil || !ok {
				t.Fatalf("get source: ok=%v err=%v", ok, getErr)
			}
			if got.Source.ByteOffset != 0 || got.Source.FailureCount != 1 ||
				got.Source.State != domain.UsageSourceError || got.Source.LastErrorCode != "invalid_parser_state" {
				t.Fatalf("source after invalid state = %+v", got.Source)
			}
			aggregates, aggregateErr := store.ListUsageModelAggregates(ctx, got.SessionID)
			if aggregateErr != nil {
				t.Fatal(aggregateErr)
			}
			if len(aggregates) != 0 {
				t.Fatalf("invalid state emitted usage: %+v", aggregates)
			}
			if persisted := readParserStateJSON(t, dataDir, source.ID); persisted != test.state {
				t.Fatalf("parser state reset to %q, want original %q", persisted, test.state)
			}
		})
	}
}

func TestIngestorReplaysReplacementWithoutStableTimestampAcrossClocks(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{
			name: "missing timestamp",
			line: `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":60,"cache_write_input_tokens":0,"output_tokens":20,"reasoning_output_tokens":5}}}}`,
		},
		{
			name: "invalid timestamp",
			line: `{"timestamp":"not-a-time","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":60,"cache_write_input_tokens":0,"output_tokens":20,"reasoning_output_tokens":5}}}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, source, path, now := seedCodexIngestionSource(t, t.TempDir())
			content := string(codexSessionMetaLine(t, "codex-root", "")) + "\n" + test.line + "\n"
			mustNoError(t, os.WriteFile(path, []byte(content), 0o600))
			ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
			if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
				t.Fatalf("initial ingest: %v", err)
			}

			replacementPath := path + ".replacement"
			mustNoError(t, os.WriteFile(replacementPath, []byte(content), 0o600))
			mustNoError(t, os.Rename(replacementPath, path))
			now = now.Add(time.Hour)
			replacement := reconcileCodexRootReplacement(ctx, t, store, ingestor, source, path)
			if _, err := ingestor.Ingest(ctx, replacement.ID); err != nil {
				t.Fatalf("replay replacement: %v", err)
			}

			got, ok, err := store.GetUsageSourceForIngestion(ctx, replacement.ID)
			if err != nil || !ok {
				t.Fatalf("get replacement: ok=%v err=%v", ok, err)
			}
			if got.Source.ByteOffset != int64(len(content)) || got.Source.LastErrorCode != "" {
				t.Fatalf("replacement source = %+v, want consumed duplicate without conflict", got.Source)
			}
			aggregates, err := store.ListUsageModelAggregates(ctx, got.SessionID)
			mustNoError(t, err)
			if len(aggregates) != 1 || tokenValue(aggregates[0].Tokens.InputTokens)+tokenValue(aggregates[0].Tokens.OutputTokens) != 120 {
				t.Fatalf("aggregates = %+v, want one replay-deduplicated event", aggregates)
			}
		})
	}
}

func TestIngestorReadsIdentityAndContentFromSingleDescriptorAcrossAtomicReplacement(t *testing.T) {
	ctx := context.Background()
	store, source, path, now := seedCodexIngestionSource(t, t.TempDir())
	openedContent := string(codexSessionMetaLine(t, "codex-root", "")) + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-opened"}}` + "\n" +
		string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	replacementContent := string(codexSessionMetaLine(t, "codex-root", "")) + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-replacement"}}` + "\n" +
		string(codexTokenLine("2026-07-28T11:00:00Z", 200, 120, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(openedContent), 0o600))
	replacementPath := path + ".replacement"
	mustNoError(t, os.WriteFile(replacementPath, []byte(replacementContent), 0o600))

	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	replacedPath := false
	ingestor.openTranscript = func(name string) (*os.File, error) {
		file, err := os.Open(name) //nolint:gosec // test-controlled path.
		if err == nil && !replacedPath {
			replacedPath = true
			if renameErr := os.Rename(replacementPath, path); renameErr != nil {
				_ = file.Close()
				return nil, renameErr
			}
		}
		return file, err
	}

	first, err := ingestor.Ingest(ctx, source.ID)
	mustNoError(t, err, "ingest opened generation")
	if first.ReplacementSourceID != 0 {
		t.Fatalf("opened generation was misclassified as replacement: %+v", first)
	}
	assertTokenAggregate(t, store, sourceSessionID(t, store, source.ID), 120)

	replacement := reconcileCodexRootReplacement(ctx, t, store, ingestor, source, path)
	if _, err := ingestor.Ingest(ctx, replacement.ID); err != nil {
		t.Fatalf("ingest replacement generation: %v", err)
	}
	assertTokenAggregate(t, store, sourceSessionID(t, store, source.ID), 340)
}

func TestIngestorDoesNotApplyChunkWhenDescriptorMutatesAfterRead(t *testing.T) {
	ctx := context.Background()
	store, source, path, now := seedCodexIngestionSource(t, t.TempDir())
	beforeContent := string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	afterContent := string(codexTokenLine("2026-07-28T11:00:00Z", 200, 70, 0, 30, 5)) + "\n"
	if len(beforeContent) != len(afterContent) {
		t.Fatalf("fixture lengths = %d/%d, want equal", len(beforeContent), len(afterContent))
	}
	mustNoError(t, os.WriteFile(path, []byte(beforeContent), 0o600))
	beforeModTime := now.Add(-time.Hour)
	mustNoError(t, os.Chtimes(path, beforeModTime, beforeModTime))

	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	ingestor.afterRead = func() {
		mustNoError(t, os.WriteFile(path, []byte(afterContent), 0o600))
		mustNoError(t, os.Chtimes(path, now, now))
	}
	result, err := ingestor.Ingest(ctx, source.ID)
	if err == nil || result.RetryAt == nil {
		t.Fatalf("mutated ingest result = %+v, err=%v, want retry", result, err)
	}
	got, ok, err := store.GetUsageSourceForIngestion(ctx, source.ID)
	if err != nil || !ok {
		t.Fatalf("get source: ok=%v err=%v", ok, err)
	}
	if got.Source.ByteOffset != 0 || got.Source.ParserStateJSON != "{}" || got.Source.FailureCount != 1 {
		t.Fatalf("mutated source advanced: %+v", got.Source)
	}
	aggregates, err := store.ListUsageModelAggregates(ctx, got.SessionID)
	mustNoError(t, err)
	if len(aggregates) != 0 {
		t.Fatalf("mutated read emitted events: %+v", aggregates)
	}

	ingestor.afterRead = nil
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("retry stable descriptor: %v", err)
	}
	assertTokenAggregate(t, store, got.SessionID, 230)
}

func TestIngestorPersistsParsedCheckpointWhenRewriteFollowsFinalVerification(t *testing.T) {
	ctx := context.Background()
	store, source, path, now := seedCodexIngestionSource(t, t.TempDir())
	meta := string(codexSessionMetaLine(t, "codex-root", "")) + "\n"
	beforeContent := meta + string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	afterContent := meta + string(codexTokenLine("2026-07-28T11:00:00Z", 200, 70, 0, 30, 5)) + "\n"
	if len(beforeContent) != len(afterContent) {
		t.Fatalf("fixture lengths = %d/%d, want equal", len(beforeContent), len(afterContent))
	}
	mustNoError(t, os.WriteFile(path, []byte(beforeContent), 0o600))
	interleaved := &applyInterleavingStore{Store: store}
	interleaved.beforeApply = func() {
		mustNoError(t, os.WriteFile(path, []byte(afterContent), 0o600))
	}
	ingestor := NewIngestor(interleaved, IngestorConfig{Clock: func() time.Time { return now }})
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("ingest verified bytes: %v", err)
	}
	assertTokenAggregate(t, store, sourceSessionID(t, store, source.ID), 120)

	replacement := reconcileCodexRootReplacement(ctx, t, store, ingestor, source, path)
	if _, err := ingestor.Ingest(ctx, replacement.ID); err != nil {
		t.Fatalf("ingest replacement generation: %v", err)
	}
	assertTokenAggregate(t, store, sourceSessionID(t, store, source.ID), 350)
}

func TestIngestorRejectsChunkAfterSourceRetirement(t *testing.T) {
	ctx := context.Background()
	store, source, path, now := seedCodexIngestionSource(t, t.TempDir())
	content := string(codexSessionMetaLine(t, "codex-root", "")) + "\n" +
		string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(content), 0o600))

	interleaved := &applyInterleavingStore{Store: store}
	interleaved.beforeApply = func() {
		_, err := store.ReplaceUsageSource(ctx, source.ID, domain.UsageErrorArtifactReplaced, domain.UsageSourceRecord{
			BindingID:       source.BindingID,
			Kind:            source.Kind,
			NativeSessionID: source.NativeSessionID,
			ArtifactPath:    source.ArtifactPath,
			FileIdentity:    source.FileIdentity,
			Generation:      source.Generation + 1,
			State:           domain.UsageSourcePending,
			UpdatedAt:       now.Add(time.Second),
		}, now.Add(time.Second))
		mustNoError(t, err)
	}
	ingestor := NewIngestor(interleaved, IngestorConfig{Clock: func() time.Time { return now }})
	if _, err := ingestor.Ingest(ctx, source.ID); !errors.Is(err, domain.ErrUsageSourceRevisionConflict) {
		t.Fatalf("ingest error = %v, want revision conflict", err)
	}

	sources, err := store.ListUsageSourcesForBinding(ctx, source.BindingID)
	mustNoError(t, err)
	if len(sources) != 2 || sources[0].ByteOffset != 0 ||
		sources[0].State != domain.UsageSourceComplete ||
		sources[0].LastErrorCode != domain.UsageErrorArtifactReplaced {
		t.Fatalf("sources after interleaving = %+v", sources)
	}
	assertTokenAggregate(t, store, sourceSessionID(t, store, source.ID), 0)
}

func TestIngestorReplacesSameInodeWhenPreCursorCheckpointChanges(t *testing.T) {
	tests := []struct {
		name   string
		suffix string
	}{
		{name: "equal size"},
		{name: "larger size", suffix: `{"type":"turn_context","payload":{"model":"gpt-after"}}` + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, source, path, now := seedCodexIngestionSource(t, t.TempDir())
			meta := string(codexSessionMetaLine(t, "codex-root", "")) + "\n"
			beforeContent := meta + string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
			afterContent := meta + string(codexTokenLine("2026-07-28T11:00:00Z", 200, 70, 0, 30, 5)) + "\n" + test.suffix
			if test.suffix == "" && len(afterContent) != len(beforeContent) {
				t.Fatalf("equal-size fixture lengths = %d/%d", len(beforeContent), len(afterContent))
			}
			mustNoError(t, os.WriteFile(path, []byte(beforeContent), 0o600))
			identity, err := usagesvc.SourceIdentity(context.Background(), path)
			mustNoError(t, err)
			ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
			if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
				t.Fatalf("ingest original generation: %v", err)
			}
			assertTokenAggregate(t, store, sourceSessionID(t, store, source.ID), 120)

			mustNoError(t, rewriteUsageFixture(path, afterContent))
			rewrittenIdentity, err := usagesvc.SourceIdentity(context.Background(), path)
			mustNoError(t, err)
			if rewrittenIdentity != identity {
				t.Fatalf("rewrite changed identity: %q != %q", rewrittenIdentity, identity)
			}

			replacement := reconcileCodexRootReplacement(ctx, t, store, ingestor, source, path)
			if replacement.Generation != source.Generation+1 || replacement.ByteOffset != 0 {
				t.Fatalf("replacement source = %+v", replacement)
			}
			if _, err := ingestor.Ingest(ctx, replacement.ID); err != nil {
				t.Fatalf("ingest replacement generation: %v", err)
			}
			assertTokenAggregate(t, store, sourceSessionID(t, store, replacement.ID), 350)
		})
	}
}

func seedCodexIngestionSource(t *testing.T, dataDir string) (*sqlite.Store, domain.UsageSourceRecord, string, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, session := seedUsageTestSession(
		t, dataDir, "usage", domain.HarnessCodex, domain.ActivityIdle, "", now,
	)
	binding, err := store.UpsertUsageBinding(ctx, domain.UsageBindingRecord{
		SessionID:      session.ID,
		Harness:        domain.HarnessCodex,
		NativeRootID:   "codex-root",
		InitialModelID: "fallback-model",
		State:          domain.UsageBindingActive,
		UpdatedAt:      now,
	})
	mustNoError(t, err)
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	mustNoError(t, os.WriteFile(path, nil, 0o600))
	path = canonicalTranscriptPath(path)
	identity, err := usagesvc.SourceIdentity(context.Background(), path)
	mustNoError(t, err)
	source, err := store.InsertUsageSource(ctx, domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: "codex-root",
		ArtifactPath:    path,
		FileIdentity:    identity,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now,
	})
	mustNoError(t, err)
	return store, source, path, now
}

func readParserStateJSON(t *testing.T, dataDir string, sourceID int64) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	mustNoError(t, err)
	defer func() { _ = db.Close() }()
	var state string
	mustNoError(t, db.QueryRow("SELECT parser_state_json FROM usage_sources WHERE id = ?", sourceID).Scan(&state), "read parser state")
	return state
}

func writeParserStateJSON(t *testing.T, dataDir string, sourceID int64, state string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	mustNoError(t, err)
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("UPDATE usage_sources SET parser_state_json = ? WHERE id = ?", state, sourceID); err != nil {
		t.Fatalf("write parser state: %v", err)
	}
}

func assertCodexParserState(t *testing.T, raw string, input int64, model string) {
	t.Helper()
	var state struct {
		Version    int    `json:"version"`
		SourceKind string `json:"source_kind"`
		Codex      struct {
			Baseline struct {
				InputTokens int64 `json:"input_tokens"`
			} `json:"baseline"`
			ModelID             string   `json:"model_id"`
			PendingSpawnCallIDs []string `json:"pending_spawn_call_ids"`
			DiscoveredChildIDs  []string `json:"discovered_child_ids"`
		} `json:"codex"`
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("decode parser state %q: %v", raw, err)
	}
	if state.Version != 1 || state.SourceKind != "codex_rollout" ||
		state.Codex.Baseline.InputTokens != input || state.Codex.ModelID != model ||
		state.Codex.PendingSpawnCallIDs == nil || state.Codex.DiscoveredChildIDs == nil {
		t.Fatalf("Codex parser state = %+v", state)
	}
}

func sourceSessionID(t *testing.T, store *sqlite.Store, sourceID int64) domain.SessionID {
	t.Helper()
	got, ok, err := store.GetUsageSourceForIngestion(context.Background(), sourceID)
	if err != nil || !ok {
		t.Fatalf("get source context: ok=%v err=%v", ok, err)
	}
	return got.SessionID
}

func reconcileCodexRootReplacement(
	ctx context.Context,
	t *testing.T,
	store *sqlite.Store,
	ingestor *Ingestor,
	previous domain.UsageSourceRecord,
	path string,
) domain.UsageSourceRecord {
	t.Helper()
	result, err := ingestor.Ingest(ctx, previous.ID)
	mustNoError(t, err, "detect Codex root replacement")
	if result.ReplacementSourceID != 0 || !result.Reconcile ||
		canonicalTranscriptPath(result.ReconcilePath) != canonicalTranscriptPath(path) {
		t.Fatalf("replacement result = %+v, want root reconciliation", result)
	}
	collector := usagesvc.NewCollector(
		store,
		usagesvc.SourceRoots{CodexSessions: filepath.Dir(path)},
		nil,
	)
	mustNoError(t, collector.ReconcilePath(ctx, path), "reconcile Codex root replacement")
	sources, err := store.ListUsageSourcesForBinding(ctx, previous.BindingID)
	mustNoError(t, err)
	var replacement domain.UsageSourceRecord
	for _, source := range sources {
		if source.ArtifactPath == canonicalTranscriptPath(path) &&
			(source.Generation > replacement.Generation || replacement.ID == 0) {
			replacement = source
		}
	}
	if replacement.ID == 0 || replacement.ID == previous.ID ||
		replacement.NativeSessionID != previous.NativeSessionID || replacement.SubagentID != "" {
		t.Fatalf("validated replacement = %+v, previous=%+v", replacement, previous)
	}
	return replacement
}

func TestIngestorCollectsCodexSourceDiscoveredAfterStartup(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, session := seedUsageTestSession(
		t, t.TempDir(), "usage", domain.HarnessCodex, domain.ActivityIdle, "native-late", now,
	)
	root := filepath.Join(t.TempDir(), "sessions")
	collector := usagesvc.NewCollector(store, usagesvc.SourceRoots{CodexSessions: root}, nil)
	mustNoError(t, collector.BackfillActive(ctx), "backfill")

	path := filepath.Join(root, "2026", "07", "28", "rollout-native-late.jsonl")
	mustNoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	content := string(codexSessionMetaLine(t, "native-late", "")) + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-5.6"}}` + "\n" +
		string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(content), 0o600))

	mustNoError(t, collector.ReconcileSources(ctx, -1), "reconcile")
	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	ingestAllWatchable(ctx, t, store, ingestor)
	assertTokenAggregate(t, store, session.ID, 120)
}

func TestIngestorCompletesCodexExitWhoseSourceAppearsLate(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, session := seedUsageTestSession(
		t, t.TempDir(), "usage", domain.HarnessCodex, domain.ActivityExited, "native-final", now,
	)
	root := filepath.Join(t.TempDir(), "sessions")
	collector := usagesvc.NewCollector(store, usagesvc.SourceRoots{CodexSessions: root}, nil)
	mustNoError(t, collector.BackfillActive(ctx), "backfill")
	session.IsTerminated = true
	session.UpdatedAt = now.Add(time.Second)
	mustNoError(t, store.UpdateSession(ctx, session), "terminate session after finalization registration")

	path := filepath.Join(root, "2026", "07", "28", "rollout-native-final.jsonl")
	mustNoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	content := string(codexSessionMetaLine(t, "native-final", "")) + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-5.6"}}` + "\n" +
		string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(content), 0o600))
	mustNoError(t, collector.ReconcileSources(ctx, -1), "reconcile")
	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	ingestAllWatchable(ctx, t, store, ingestor)
	assertTokenAggregate(t, store, session.ID, 120)
	now = now.Add(defaultFinalizationWait + time.Second)
	ingestAllWatchable(ctx, t, store, ingestor)
	bindings, err := store.ListUsageBindingsForSession(ctx, session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != domain.UsageBindingComplete {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
}

func TestIngestorPreservesCursorWhenCodexRolloutMovesToArchive(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, session := seedUsageTestSession(
		t, t.TempDir(), "usage", domain.HarnessCodex, domain.ActivityIdle, "native-move", now,
	)
	sessionsRoot := filepath.Join(t.TempDir(), "sessions")
	archiveRoot := filepath.Join(t.TempDir(), "archived_sessions")
	activePath := filepath.Join(sessionsRoot, "2026", "07", "28", "rollout-native-move.jsonl")
	mustNoError(t, os.MkdirAll(filepath.Dir(activePath), 0o700))
	initial := string(codexSessionMetaLine(t, "native-move", "")) + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-5.6"}}` + "\n" +
		string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(activePath, []byte(initial), 0o600))
	collector := usagesvc.NewCollector(store, usagesvc.SourceRoots{
		CodexSessions: sessionsRoot,
		CodexArchived: archiveRoot,
	}, nil)
	mustNoError(t, collector.BackfillActive(ctx), "backfill")
	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	ingestAllWatchable(ctx, t, store, ingestor)
	assertTokenAggregate(t, store, session.ID, 120)

	archivedPath := filepath.Join(archiveRoot, filepath.Base(activePath))
	mustNoError(t, os.MkdirAll(filepath.Dir(archivedPath), 0o700))
	mustNoError(t, os.Rename(activePath, archivedPath))
	now = now.Add(30 * time.Second)
	sources, err := store.ListWatchableUsageSources(ctx)
	mustNoError(t, err)
	for _, source := range sources {
		result, ingestErr := ingestor.Ingest(ctx, source.ID)
		if ingestErr != nil && result.RetryAt == nil {
			t.Fatalf("ingest relocated source %d: %v", source.ID, ingestErr)
		}
	}
	mustNoError(t, collector.ReconcileSources(ctx, -1), "relocation reconcile")
	ingestAllWatchable(ctx, t, store, ingestor)
	assertTokenAggregate(t, store, session.ID, 120)

	file, err := os.OpenFile(archivedPath, os.O_APPEND|os.O_WRONLY, 0o600)
	mustNoError(t, err)
	if _, err := file.Write(append(codexTokenLine("2026-07-28T10:01:00Z", 150, 90, 0, 30, 8), '\n')); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	now = now.Add(30 * time.Second)
	ingestAllWatchable(ctx, t, store, ingestor)
	assertTokenAggregate(t, store, session.ID, 180)
}

func TestCoordinatorCollectsCodexUsageFromFilesystemEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now().UTC()
	store, session := seedUsageTestSession(
		t, t.TempDir(), "usage", domain.HarnessCodex, domain.ActivityIdle, "native-watch", now,
	)

	base := t.TempDir()
	sessionsRoot := filepath.Join(base, "sessions")
	archiveRoot := filepath.Join(base, "archived_sessions")
	transcript := filepath.Join(sessionsRoot, "2026", "07", "28", "rollout-native-watch.jsonl")
	mustNoError(t, os.MkdirAll(filepath.Dir(transcript), 0o700))
	initial := string(codexSessionMetaLine(t, "native-watch", "")) + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-5.6"}}` + "\n"
	mustNoError(t, os.WriteFile(transcript, []byte(initial), 0o600))

	watcher, err := NewTranscriptWatcher(context.Background(), []string{sessionsRoot, archiveRoot})
	mustNoError(t, err)
	collector := usagesvc.NewCollector(store, usagesvc.SourceRoots{
		CodexSessions: sessionsRoot,
		CodexArchived: archiveRoot,
	}, nil)
	ingestor := NewIngestor(store, IngestorConfig{})
	coordinator := NewCoordinator(store, ingestor, watcher, CoordinatorConfig{
		Workers:    1,
		Initialize: collector.BackfillActive,
		Reconcile: func(reconcileCtx context.Context) error {
			return collector.ReconcileSources(reconcileCtx, 0)
		},
	})
	done := coordinator.Start(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("usage coordinator did not stop")
		}
	})

	waitForWatchableSource(ctx, t, store, transcript)
	appendJSONLRecord(t, transcript, codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5))
	waitForTokenAggregate(t, store, session.ID, 120)

	appendJSONLRecord(t, transcript, codexTokenLine("2026-07-28T10:01:00Z", 150, 90, 0, 30, 8))
	waitForTokenAggregate(t, store, session.ID, 180)
}

func TestIngestorPersistsAppendOnlyUsageAcrossRestartAndFinalization(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, session := seedUsageTestSession(
		t, t.TempDir(), "usage", domain.HarnessCodex, domain.ActivityActive, "", now,
	)
	binding := seedUsageTestBinding(t, store, session, "native-1", domain.UsageBindingActive, now)
	path := t.TempDir() + "/rollout.jsonl"
	initial := `{"type":"session_meta","payload":{"id":"native-1","model_provider":"openai","source":"cli"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-5.6"}}` + "\n" +
		string(codexTokenLine("2026-07-01T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(initial), 0o600))
	path = canonicalTranscriptPath(path)
	identity, err := usagesvc.SourceIdentity(context.Background(), path)
	mustNoError(t, err)
	source, err := store.InsertUsageSource(ctx, domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: "native-1",
		ArtifactPath:    path,
		FileIdentity:    identity,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now,
	})
	mustNoError(t, err)
	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	ingestSourceFully(ctx, t, ingestor, source.ID)
	assertTokenAggregate(t, store, session.ID, 120)

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	mustNoError(t, err)
	if _, err := file.Write(append(codexTokenLine("2026-07-01T10:00:01Z", 150, 90, 0, 30, 8), '\n')); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	now = now.Add(30 * time.Second)
	ingestSourceFully(ctx, t, ingestor, source.ID)
	assertTokenAggregate(t, store, session.ID, 180)

	restarted := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	ingestSourceFully(ctx, t, restarted, source.ID)
	assertTokenAggregate(t, store, session.ID, 180)

	replacement := `{"type":"session_meta","payload":{"id":"native-1","model_provider":"openai-replacement","source":"cli"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-5.6-mini"}}` + "\n" +
		string(codexTokenLine("2026-07-01T11:00:00Z", 10, 5, 0, 2, 1)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(replacement), 0o600))
	_ = reconcileCodexRootReplacement(ctx, t, store, restarted, source, path)
	ingestAllWatchable(ctx, t, store, restarted)
	assertTokenAggregate(t, store, session.ID, 192)
	sources, err := store.ListUsageSourcesForBinding(ctx, binding.ID)
	if err != nil || len(sources) != 2 ||
		sources[0].State != domain.UsageSourceComplete ||
		sources[0].LastErrorCode != domain.UsageErrorArtifactReplaced {
		t.Fatalf("replacement sources=%+v err=%v", sources, err)
	}

	if _, err := store.UpdateUsageBindingState(ctx, binding.ID, domain.UsageBindingFinalizing, "", now); err != nil {
		t.Fatal(err)
	}
	ingestAllWatchable(ctx, t, store, restarted)
	mustNoError(t, osAppend(path, string(codexTokenLine("2026-07-01T11:00:01Z", 15, 8, 0, 3, 1))))
	now = now.Add(time.Second)
	ingestAllWatchable(ctx, t, store, restarted)
	assertTokenAggregate(t, store, session.ID, 192)
	now = now.Add(defaultFinalizationWait + time.Second)
	ingestAllWatchable(ctx, t, store, restarted)
	assertTokenAggregate(t, store, session.ID, 198)
	got, ok, err := store.GetUsageSourceForIngestion(ctx, source.ID)
	if err != nil || !ok {
		t.Fatalf("source ok=%v err=%v", ok, err)
	}
	if got.Source.State != domain.UsageSourceComplete {
		t.Fatalf("source state = %s", got.Source.State)
	}
	bindings, err := store.ListUsageBindingsForSession(ctx, session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != domain.UsageBindingComplete {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
}

func TestIngestorRestartsFinalTailQuiescenceWhenTailChanges(t *testing.T) {
	ctx := context.Background()
	store, source, path, now := seedCodexIngestionSource(t, t.TempDir())
	completePrefix := `{"type":"turn_context","payload":{"model":"gpt-tail"}}` + "\n"
	finalRecord := string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5))
	split := len(finalRecord) / 2
	mustNoError(t, os.WriteFile(path, []byte(completePrefix+finalRecord[:split]), 0o600))
	if _, err := store.UpdateUsageBindingState(
		ctx,
		source.BindingID,
		domain.UsageBindingFinalizing,
		"",
		now,
	); err != nil {
		t.Fatal(err)
	}
	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})

	first, err := ingestor.Ingest(ctx, source.ID)
	mustNoError(t, err, "observe partial tail")
	if first.RetryAt == nil {
		t.Fatalf("first result = %+v, want quiet retry", first)
	}
	now = now.Add(defaultFinalizationWait + time.Second)
	mustNoError(t, osAppend(path, finalRecord[split:]))
	second, err := ingestor.Ingest(ctx, source.ID)
	mustNoError(t, err, "observe changed valid tail")
	if second.RetryAt == nil || !second.RetryAt.After(now) {
		t.Fatalf("changed-tail result = %+v, want restarted quiet retry", second)
	}
	got, ok, err := store.GetUsageSourceForIngestion(ctx, source.ID)
	if err != nil || !ok {
		t.Fatalf("source after changed tail: ok=%v err=%v", ok, err)
	}
	if got.Source.ByteOffset != int64(len(completePrefix)) || got.Source.State == domain.UsageSourceComplete {
		t.Fatalf("changed tail advanced source: %+v", got.Source)
	}
	assertTokenAggregate(t, store, got.SessionID, 0)

	now = now.Add(defaultFinalizationWait + time.Second)
	third, err := ingestor.Ingest(ctx, source.ID)
	mustNoError(t, err, "consume stable valid tail")
	if third.RetryAt != nil {
		t.Fatalf("stable valid tail result = %+v", third)
	}
	got, ok, err = store.GetUsageSourceForIngestion(ctx, source.ID)
	if err != nil || !ok {
		t.Fatalf("completed source: ok=%v err=%v", ok, err)
	}
	if got.Source.State != domain.UsageSourceComplete || got.Source.ByteOffset != int64(len(completePrefix+finalRecord)) {
		t.Fatalf("completed source = %+v", got.Source)
	}
	assertTokenAggregate(t, store, got.SessionID, 120)
}

func TestIngestorDropsMalformedFinalTailOnlyAfterTwoQuietObservations(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, source, path, now := seedCodexIngestionSource(t, dataDir)
	completePrefix := `{"type":"turn_context","payload":{"model":"gpt-tail"}}` + "\n"
	malformedTail := `{"type":"event_msg","payload":`
	mustNoError(t, os.WriteFile(path, []byte(completePrefix+malformedTail), 0o600))
	if _, err := store.UpdateUsageBindingState(
		ctx,
		source.BindingID,
		domain.UsageBindingFinalizing,
		"",
		now,
	); err != nil {
		t.Fatal(err)
	}
	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})

	first, err := ingestor.Ingest(ctx, source.ID)
	mustNoError(t, err, "observe malformed tail")
	if first.RetryAt == nil {
		t.Fatalf("first result = %+v, want quiet retry", first)
	}
	persistedState := readParserStateJSON(t, dataDir, source.ID)
	if strings.Contains(persistedState, malformedTail) {
		t.Fatalf("parser state persisted transcript tail: %s", persistedState)
	}
	now = now.Add(defaultFinalizationWait + time.Second)
	second, err := ingestor.Ingest(ctx, source.ID)
	mustNoError(t, err, "first quiet observation")
	if second.RetryAt == nil || !second.RetryAt.After(now) {
		t.Fatalf("first quiet result = %+v, want additional quiet retry", second)
	}
	got, ok, err := store.GetUsageSourceForIngestion(ctx, source.ID)
	if err != nil || !ok {
		t.Fatalf("pending source: ok=%v err=%v", ok, err)
	}
	if got.Source.ByteOffset != int64(len(completePrefix)) || got.Source.AnomalyCount != 0 ||
		got.Source.State == domain.UsageSourceComplete {
		t.Fatalf("malformed tail consumed too early: %+v", got.Source)
	}

	now = now.Add(defaultFinalizationWait + time.Second)
	third, err := ingestor.Ingest(ctx, source.ID)
	mustNoError(t, err, "second quiet observation")
	if third.RetryAt != nil {
		t.Fatalf("second quiet result = %+v", third)
	}
	got, ok, err = store.GetUsageSourceForIngestion(ctx, source.ID)
	if err != nil || !ok {
		t.Fatalf("completed source: ok=%v err=%v", ok, err)
	}
	if got.Source.State != domain.UsageSourceComplete ||
		got.Source.ByteOffset != int64(len(completePrefix+malformedTail)) ||
		got.Source.AnomalyCount != 1 || got.Source.LastErrorCode != domain.UsageErrorMalformedJSONL {
		t.Fatalf("completed malformed source = %+v", got.Source)
	}
}

func TestIngestorLateAppendReturnsCompletedBindingToFinalizing(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, session := seedUsageTestSession(
		t, t.TempDir(), "usage", domain.HarnessCodex, domain.ActivityIdle, "", now,
	)
	binding := seedUsageTestBinding(t, store, session, "native-late-append", domain.UsageBindingActive, now)
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	content := `{"type":"turn_context","payload":{"model":"gpt-5.6"}}` + "\n" +
		string(codexTokenLine("2026-07-01T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(content), 0o600))
	identity, err := usagesvc.SourceIdentity(context.Background(), path)
	mustNoError(t, err)
	source, err := store.InsertUsageSource(ctx, domain.UsageSourceRecord{
		BindingID:    binding.ID,
		Kind:         domain.UsageSourceCodexRollout,
		ArtifactPath: path,
		FileIdentity: identity,
		State:        domain.UsageSourcePending,
		UpdatedAt:    now,
	})
	mustNoError(t, err)
	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	ingestSourceFully(ctx, t, ingestor, source.ID)
	if _, err := store.MarkUsageSourceState(ctx, source.ID, domain.UsageSourceComplete, "", nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateUsageBindingState(ctx, binding.ID, domain.UsageBindingFinalizing, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteUsageBindingIfSettled(ctx, binding.ID, now); err != nil {
		t.Fatal(err)
	}

	appendJSONLRecord(t, path, codexTokenLine("2026-07-01T10:01:00Z", 150, 90, 0, 30, 8))
	now = now.Add(time.Second)
	result, err := ingestor.Ingest(ctx, source.ID)
	mustNoError(t, err)
	if result.RetryAt == nil {
		t.Fatal("late append did not schedule a finalization quiet period")
	}
	bindings, err := store.ListUsageBindingsForSession(ctx, session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != domain.UsageBindingFinalizing {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	now = now.Add(defaultFinalizationWait + time.Second)
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	bindings, err = store.ListUsageBindingsForSession(ctx, session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != domain.UsageBindingComplete {
		t.Fatalf("settled bindings=%+v err=%v", bindings, err)
	}
	assertTokenAggregate(t, store, session.ID, 180)
}

func TestIngestorStopsRetryingConflictingNativeEvent(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, session := seedUsageTestSession(
		t, t.TempDir(), "usage", domain.HarnessClaudeCode, domain.ActivityIdle, "", now,
	)
	binding := seedUsageTestBinding(t, store, session, "claude-root", domain.UsageBindingActive, now)
	path := filepath.Join(t.TempDir(), "claude-root.jsonl")
	line := `{"type":"assistant","uuid":"native-message","message":{"id":"msg-1","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}` + "\n"
	mustNoError(t, os.WriteFile(path, []byte(line), 0o600))
	identity, err := usagesvc.SourceIdentity(context.Background(), path)
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
	contextSource, ok, err := store.GetUsageSourceForIngestion(ctx, source.ID)
	if err != nil || !ok {
		t.Fatalf("source ok=%v err=%v", ok, err)
	}
	parsed := parseRecords(contextSource, []jsonlRecord{{Data: []byte(line), Offset: 0}}, int64(len(line)), now)
	if parsed.err != nil {
		t.Fatal(parsed.err)
	}
	conflict := parsed.Events[0]
	(*conflict.Tokens.OutputTokens)++
	if err := store.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 0,
		State:      domain.UsageSourcePending,
		UpdatedAt:  now,
	}, []domain.ModelUsageEvent{conflict}); err != nil {
		t.Fatal(err)
	}

	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("conflict ingest: %v", err)
	}
	got, ok, err := store.GetUsageSourceForIngestion(ctx, source.ID)
	if err != nil || !ok {
		t.Fatalf("source ok=%v err=%v", ok, err)
	}
	if got.Source.State != domain.UsageSourceComplete ||
		got.Source.LastErrorCode != domain.UsageErrorSourceEventConflict {
		t.Fatalf("source=%+v", got.Source)
	}
}

func TestRetryDelayUsesBoundedBackoff(t *testing.T) {
	tests := []struct {
		failure int64
		want    time.Duration
	}{
		{failure: 1, want: 30 * time.Second},
		{failure: 2, want: time.Minute},
		{failure: 3, want: 2 * time.Minute},
		{failure: 4, want: 5 * time.Minute},
		{failure: 20, want: 5 * time.Minute},
	}
	for _, test := range tests {
		if got := retryDelay(test.failure); got != test.want {
			t.Errorf("retryDelay(%d)=%s, want %s", test.failure, got, test.want)
		}
	}
}

func ingestAllWatchable(ctx context.Context, t *testing.T, store *sqlite.Store, ingestor *Ingestor) {
	t.Helper()
	for pass := 0; pass < 16; pass++ {
		sources, err := store.ListWatchableUsageSources(ctx)
		mustNoError(t, err)
		again := false
		for _, source := range sources {
			result, ingestErr := ingestor.Ingest(ctx, source.ID)
			if ingestErr != nil && result.RetryAt == nil {
				t.Fatalf("ingest source %d: %v", source.ID, ingestErr)
			}
			again = again || result.More || result.Refresh
		}
		if !again {
			return
		}
	}
	t.Fatal("usage ingestion did not settle")
}

func ingestSourceFully(ctx context.Context, t *testing.T, ingestor *Ingestor, sourceID int64) {
	t.Helper()
	for pass := 0; pass < 16; pass++ {
		result, err := ingestor.Ingest(ctx, sourceID)
		if err != nil && result.RetryAt == nil {
			t.Fatalf("ingest source %d: %v", sourceID, err)
		}
		if !result.More {
			return
		}
	}
	t.Fatalf("usage source %d did not reach EOF", sourceID)
}

type applyInterleavingStore struct {
	*sqlite.Store
	beforeApply func()
}

type fenceAdmissionContext struct {
	context.Context
	admitted chan struct{}
	once     sync.Once
}

func (c *fenceAdmissionContext) Err() error {
	c.once.Do(func() { close(c.admitted) })
	return c.Context.Err()
}

func testPricingSnapshot(t *testing.T, openAIInputRate string) *pricing.Snapshot {
	t.Helper()
	root := t.TempDir()
	upstream := []byte(`{
  "anthropic/claude-test": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0,
    "output_cost_per_token": 0
  },
  "openai/gpt-test": {
    "litellm_provider": "openai",
    "mode": "responses",
    "input_cost_per_token": ` + openAIInputRate + `,
    "cache_read_input_token_cost": 0.0000001,
    "output_cost_per_token": 0.000002
  },
  "zai/glm-test": {
    "litellm_provider": "zai",
    "mode": "chat",
    "input_cost_per_token": 0,
    "output_cost_per_token": 0
  }
}`)
	_, err := catalogsync.Sync(root, upstream, catalogsync.Source{
		Repository: "BerriAI/litellm",
		Revision:   "0123456789abcdef0123456789abcdef01234567",
		Path:       "model_prices_and_context_window.json",
	})
	mustNoError(t, err)
	catalog, err := pricing.NewCache(root).Load(t.Context())
	mustNoError(t, err)
	return catalog.Snapshot()
}

func (s *applyInterleavingStore) ApplyUsageChunk(
	ctx context.Context,
	sourceID int64,
	expectedOffset int64,
	expectedRevision time.Time,
	nextState domain.SourceCursorState,
	events []domain.ModelUsageEvent,
) error {
	if beforeApply := s.beforeApply; beforeApply != nil {
		s.beforeApply = nil
		beforeApply()
	}
	return s.Store.ApplyUsageChunk(ctx, sourceID, expectedOffset, expectedRevision, nextState, events)
}

func assertTokenAggregate(t *testing.T, store *sqlite.Store, sessionID domain.SessionID, total int64) {
	t.Helper()
	aggregates, err := store.ListUsageModelAggregates(context.Background(), sessionID)
	mustNoError(t, err)
	var got int64
	for _, aggregate := range aggregates {
		got += tokenValue(aggregate.Tokens.InputTokens) + tokenValue(aggregate.Tokens.OutputTokens)
	}
	if got != total {
		t.Fatalf("total tokens = %d, want %d; aggregates=%+v", got, total, aggregates)
	}
}

func waitForWatchableSource(ctx context.Context, t *testing.T, store *sqlite.Store, path string) {
	t.Helper()
	path = canonicalTranscriptPath(path)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sources, err := store.ListWatchableUsageSources(ctx)
		mustNoError(t, err)
		for _, source := range sources {
			if canonicalTranscriptPath(source.ArtifactPath) == path {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("usage source %q was not registered", path)
}

func appendJSONLRecord(t *testing.T, path string, record []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	mustNoError(t, err)
	if _, err := file.Write(append(record, '\n')); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	mustNoError(t, file.Close())
}

func rewriteUsageFixture(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func waitForTokenAggregate(t *testing.T, store *sqlite.Store, sessionID domain.SessionID, total int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		aggregates, err := store.ListUsageModelAggregates(context.Background(), sessionID)
		mustNoError(t, err)
		var got int64
		for _, aggregate := range aggregates {
			got += tokenValue(aggregate.Tokens.InputTokens) + tokenValue(aggregate.Tokens.OutputTokens)
		}
		if got == total {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertTokenAggregate(t, store, sessionID, total)
}
