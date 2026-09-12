package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

const (
	testCodexRootID     = "11111111-1111-4111-8111-111111111111"
	testCodexChildID    = "22222222-2222-4222-8222-222222222222"
	testCodexOverflowID = "33333333-3333-4333-8333-333333333333"
	testCodexBudgetCode = "codex_source_budget_exceeded"
)

type codexBudgetFixture struct {
	store     *sqlite.Store
	collector *Collector
	session   domain.SessionRecord
	binding   domain.UsageBindingRecord
	root      string
	rootPath  string
	now       time.Time
}

func TestCollectorCodexBudgetCountsLogicalIDsAndAllowsExistingGenerations(t *testing.T) {
	fixture := newCodexBudgetFixture(t, 2)
	ctx := context.Background()
	childPath := filepath.Join(fixture.root, "active", "rollout-child.jsonl")
	writeUsageFixture(t, childPath, codexSessionMetaFixture(t, testCodexChildID, testCodexRootID))

	changed, err := fixture.collector.registerSource(
		ctx,
		fixture.binding,
		domain.UsageSourceCodexRollout,
		testCodexChildID,
		testCodexChildID,
		childPath,
		fixture.now,
		false,
	)
	if err != nil || !changed {
		t.Fatalf("register admitted child: changed=%v err=%v", changed, err)
	}

	replaced := childPath + ".replaced"
	mustNoError(t, os.Rename(childPath, replaced))
	writeUsageFixture(t, childPath, codexSessionMetaFixture(t, testCodexChildID, testCodexRootID))
	changed, err = fixture.collector.registerSource(
		ctx,
		fixture.binding,
		domain.UsageSourceCodexRollout,
		testCodexChildID,
		testCodexChildID,
		childPath,
		fixture.now.Add(time.Second),
		false,
	)
	if err != nil || !changed {
		t.Fatalf("register replacement generation: changed=%v err=%v", changed, err)
	}

	archivePath := filepath.Join(fixture.root, "archive", "rollout-child.jsonl")
	mustNoError(t, os.MkdirAll(filepath.Dir(archivePath), 0o700))
	mustNoError(t, os.Rename(childPath, archivePath))
	changed, err = fixture.collector.registerSource(
		ctx,
		fixture.binding,
		domain.UsageSourceCodexRollout,
		testCodexChildID,
		testCodexChildID,
		archivePath,
		fixture.now.Add(2*time.Second),
		false,
	)
	if err != nil || !changed {
		t.Fatalf("register archive relocation: changed=%v err=%v", changed, err)
	}

	overflowPath := filepath.Join(fixture.root, "active", "rollout-overflow.jsonl")
	writeUsageFixture(t, overflowPath, codexSessionMetaFixture(t, testCodexOverflowID, testCodexRootID))
	changed, err = fixture.collector.registerSource(
		ctx,
		fixture.binding,
		domain.UsageSourceCodexRollout,
		testCodexOverflowID,
		testCodexOverflowID,
		overflowPath,
		fixture.now.Add(3*time.Second),
		false,
	)
	if err != nil || changed {
		t.Fatalf("register overflow child: changed=%v err=%v", changed, err)
	}

	sources, err := fixture.store.ListUsageSourcesForBinding(ctx, fixture.binding.ID)
	mustNoError(t, err)
	distinct := make(map[string]struct{})
	childGenerations := 0
	for _, source := range sources {
		if source.NativeSessionID != "" {
			distinct[source.NativeSessionID] = struct{}{}
		}
		if source.NativeSessionID == testCodexChildID {
			childGenerations++
		}
		if source.NativeSessionID == testCodexOverflowID {
			t.Fatal("overflow child source was persisted")
		}
	}
	if len(distinct) != 2 || childGenerations != 3 {
		t.Fatalf("logical sources=%d child generations=%d, want 2 and 3", len(distinct), childGenerations)
	}
	assertCodexBudgetMarker(t, fixture.store, fixture.session.ID, domain.UsageBindingPartial)
	watchable, err := fixture.store.ListWatchableUsageSources(ctx)
	mustNoError(t, err)
	watchableIDs := make(map[string]struct{})
	for _, source := range watchable {
		watchableIDs[source.NativeSessionID] = struct{}{}
	}
	if _, ok := watchableIDs[testCodexRootID]; !ok {
		t.Fatal("partial binding made the admitted root source unwatchable")
	}
	if _, ok := watchableIDs[testCodexChildID]; !ok {
		t.Fatal("partial binding made the admitted child source unwatchable")
	}
	reader := NewSummaryReader(fixture.store)
	compact, err := reader.ListCompact(ctx, fixture.session.ProjectID)
	if err != nil || len(compact) != 0 {
		t.Fatalf("zero-usage compact summary=%+v err=%v, want no card metric", compact, err)
	}
	detail, err := reader.Get(ctx, fixture.session.ID)
	if err != nil || !detail.Incomplete {
		t.Fatalf("live detail summary=%+v err=%v", detail, err)
	}
}

func TestCollectorCodexBudgetBoundsRecursiveDiscoveryAndReconcilePath(t *testing.T) {
	tests := []struct {
		name      string
		reconcile func(context.Context, *codexBudgetFixture, string) error
	}{
		{
			name: "full source reconciliation",
			reconcile: func(ctx context.Context, fixture *codexBudgetFixture, _ string) error {
				return fixture.collector.ReconcileSources(ctx, -1)
			},
		},
		{
			name: "path reconciliation",
			reconcile: func(ctx context.Context, fixture *codexBudgetFixture, childPath string) error {
				return fixture.collector.ReconcilePath(ctx, childPath)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCodexBudgetFixture(t, 1)
			ctx := context.Background()
			childPath := filepath.Join(
				fixture.root,
				"2026",
				"08",
				"02",
				"rollout-child-"+testCodexChildID+".jsonl",
			)
			writeUsageFixture(t, childPath, codexSessionMetaFixture(t, testCodexChildID, testCodexRootID))
			setCodexDiscoveredChildren(t, fixture.store, onlyBudgetSource(t, fixture), testCodexChildID)

			mustNoError(t, test.reconcile(ctx, fixture, childPath), "reconcile")
			sources, err := fixture.store.ListUsageSourcesForBinding(ctx, fixture.binding.ID)
			if err != nil || len(sources) != 1 {
				t.Fatalf("sources=%+v err=%v, want only root", sources, err)
			}
			assertCodexBudgetMarker(t, fixture.store, fixture.session.ID, domain.UsageBindingPartial)
		})
	}
}

func TestCollectorCodexBudgetResumePreservesPartialMarker(t *testing.T) {
	fixture := newCodexBudgetFixture(t, 1)
	ctx := context.Background()
	childPath := filepath.Join(fixture.root, "active", "rollout-child.jsonl")
	writeUsageFixture(t, childPath, codexSessionMetaFixture(t, testCodexChildID, testCodexRootID))
	setCodexDiscoveredChildren(t, fixture.store, onlyBudgetSource(t, fixture), testCodexChildID)
	mustNoError(t, fixture.collector.registerDiscoveredCodexChildren(ctx, fixture.binding, fixture.now))
	if _, err := fixture.store.UpdateUsageBindingState(
		ctx,
		fixture.binding.ID,
		domain.UsageBindingPartial,
		testCodexBudgetCode,
		fixture.now,
	); err != nil {
		t.Fatal(err)
	}
	source := onlyBudgetSource(t, fixture)
	if _, err := fixture.store.MarkUsageSourceState(
		ctx,
		source.ID,
		domain.UsageSourceComplete,
		"",
		nil,
		fixture.now,
	); err != nil {
		t.Fatal(err)
	}

	if err := fixture.collector.RecordHook(ctx, fixture.session.ID, HookSignal{
		Harness:         domain.HarnessCodex,
		Event:           "session-start",
		NativeSessionID: testCodexRootID,
		TranscriptPath:  fixture.rootPath,
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	assertCodexBudgetMarker(t, fixture.store, fixture.session.ID, domain.UsageBindingPartial)
	resumed := onlyBudgetSource(t, fixture)
	if resumed.State != domain.UsageSourceActive {
		t.Fatalf("resumed source state=%s, want active", resumed.State)
	}

	if _, err := fixture.store.UpdateUsageBindingState(
		ctx,
		fixture.binding.ID,
		domain.UsageBindingFinalizing,
		testCodexBudgetCode,
		fixture.now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.MarkUsageSourceState(
		ctx,
		resumed.ID,
		domain.UsageSourceComplete,
		"",
		nil,
		fixture.now,
	); err != nil {
		t.Fatal(err)
	}
	if err := fixture.collector.RecordHook(ctx, fixture.session.ID, HookSignal{
		Harness:         domain.HarnessCodex,
		Event:           "session-start",
		NativeSessionID: testCodexRootID,
		TranscriptPath:  fixture.rootPath,
	}); err != nil {
		t.Fatalf("resume finalizing binding: %v", err)
	}
	assertCodexBudgetMarker(t, fixture.store, fixture.session.ID, domain.UsageBindingPartial)
	if state := onlyBudgetSource(t, fixture).State; state != domain.UsageSourceActive {
		t.Fatalf("resumed finalizing source state=%s, want active", state)
	}
}

func TestCollectorCodexBudgetRestartFinalizesPersistedPartialForExitedSession(t *testing.T) {
	fixture := newCodexBudgetFixture(t, 1)
	ctx := context.Background()
	childPath := filepath.Join(
		fixture.root,
		"2026",
		"08",
		"02",
		"rollout-child-"+testCodexChildID+".jsonl",
	)
	writeUsageFixture(t, childPath, codexSessionMetaFixture(t, testCodexChildID, testCodexRootID))
	setCodexDiscoveredChildren(t, fixture.store, onlyBudgetSource(t, fixture), testCodexChildID)
	mustNoError(t, fixture.collector.ReconcileSources(ctx, -1), "record live overflow")
	assertCodexBudgetMarker(t, fixture.store, fixture.session.ID, domain.UsageBindingPartial)

	fixture.session.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: fixture.now}
	fixture.session.UpdatedAt = fixture.now
	mustNoError(t, fixture.store.UpdateSession(ctx, fixture.session))
	restarted := newCollectorWithCodexSourceLimit(fixture.store, SourceRoots{
		CodexSessions: fixture.root,
		CodexArchived: fixture.root,
	}, nil, 1)
	mustNoError(t, restarted.ReconcileSources(ctx, -1), "restart reconcile")
	assertCodexBudgetMarker(t, fixture.store, fixture.session.ID, domain.UsageBindingFinalizing)
}

func TestCollectorCodexBudgetFinalizationWaitsThenPersistsPartialAcrossRestart(t *testing.T) {
	for _, hookEvent := range []string{"process-exited", "session-end"} {
		t.Run(hookEvent, func(t *testing.T) {
			testCollectorCodexBudgetFinalizationWaitsThenPersistsPartialAcrossRestart(t, hookEvent)
		})
	}
}

func testCollectorCodexBudgetFinalizationWaitsThenPersistsPartialAcrossRestart(t *testing.T, hookEvent string) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := sqlitetest.Open(dataDir)
	mustNoError(t, err)
	root := t.TempDir()
	now := time.Unix(1700000000, 0).UTC()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:           "budget-restart",
		Path:         t.TempDir(),
		RegisteredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "budget-restart",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		Metadata:  domain.SessionMetadata{AgentSessionID: testCodexRootID},
		CreatedAt: now,
		UpdatedAt: now,
	})
	mustNoError(t, err)
	collector := newCollectorWithCodexSourceLimit(store, SourceRoots{
		CodexSessions: root,
		CodexArchived: root,
	}, nil, 2)
	collector.now = func() time.Time { return now }
	rootPath := filepath.Join(root, "2026", "08", "02", "rollout-root-"+testCodexRootID+".jsonl")
	childPath := filepath.Join(root, "active", "rollout-child.jsonl")
	overflowPath := filepath.Join(root, "active", "rollout-overflow.jsonl")
	writeUsageFixture(t, rootPath, codexSessionMetaFixture(t, testCodexRootID, ""))
	writeUsageFixture(t, childPath, codexSessionMetaFixture(t, testCodexChildID, testCodexRootID))
	writeUsageFixture(t, overflowPath, codexSessionMetaFixture(t, testCodexOverflowID, testCodexRootID))
	if err := collector.RecordHook(ctx, session.ID, HookSignal{
		Harness:         domain.HarnessCodex,
		Event:           "session-start",
		NativeSessionID: testCodexRootID,
		TranscriptPath:  rootPath,
	}); err != nil {
		t.Fatal(err)
	}
	binding := onlyUsageBindingForBudget(t, store, session.ID)
	if _, err := collector.registerSource(
		ctx,
		binding,
		domain.UsageSourceCodexRollout,
		testCodexChildID,
		testCodexChildID,
		childPath,
		now,
		false,
	); err != nil {
		t.Fatal(err)
	}
	sources, err := store.ListUsageSourcesForBinding(ctx, binding.ID)
	mustNoError(t, err)
	var rootSource, childSource domain.UsageSourceRecord
	for _, source := range sources {
		switch source.NativeSessionID {
		case testCodexRootID:
			rootSource = source
		case testCodexChildID:
			childSource = source
		}
	}
	setCodexDiscoveredChildren(t, store, rootSource, testCodexChildID, testCodexOverflowID)
	rootContext, ok, err := store.GetUsageSourceForIngestion(ctx, rootSource.ID)
	if err != nil || !ok {
		t.Fatalf("reload root source: ok=%v err=%v", ok, err)
	}
	rootSource = rootContext.Source
	if _, err := collector.registerSource(
		ctx,
		binding,
		domain.UsageSourceCodexRollout,
		testCodexOverflowID,
		testCodexOverflowID,
		overflowPath,
		now,
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyUsageChunk(ctx, rootSource.ID, rootSource.ByteOffset, rootSource.UpdatedAt, domain.SourceCursorState{
		ByteOffset:      rootSource.ByteOffset,
		ParserStateJSON: codexParserStateWithChildren(t, testCodexChildID, testCodexOverflowID),
		State:           domain.UsageSourceActive,
		UpdatedAt:       now,
	}, []domain.ModelUsageEvent{{
		ProviderID:        domain.UsageProviderOpenAI,
		ModelID:           "gpt-5",
		MeasurementKind:   domain.UsageMeasurementNativeReported,
		Tokens:            testUsageMetrics(10, 0, 10, 2),
		ProviderUsageJSON: `{"last_token_usage":{"cache_write_input_tokens":0}}`,
		SourceEventKey:    "budget-root-event",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := collector.RecordHook(ctx, session.ID, HookSignal{
		Harness:         domain.HarnessCodex,
		Event:           hookEvent,
		NativeSessionID: testCodexRootID,
		TranscriptPath:  rootPath,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkUsageSourceState(ctx, rootSource.ID, domain.UsageSourceComplete, "", nil, now); err != nil {
		t.Fatal(err)
	}
	mustNoError(t, collector.settleFinalizingBinding(ctx, binding.ID, now))
	assertCodexBudgetMarker(t, store, session.ID, domain.UsageBindingFinalizing)
	if _, err := store.MarkUsageSourceState(ctx, childSource.ID, domain.UsageSourceComplete, "", nil, now); err != nil {
		t.Fatal(err)
	}
	mustNoError(t, collector.settleFinalizingBinding(ctx, binding.ID, now))
	assertCodexBudgetMarker(t, store, session.ID, domain.UsageBindingPartial)

	mustNoError(t, store.Close())
	store, err = sqlite.Open(dataDir)
	mustNoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	restarted := newCollectorWithCodexSourceLimit(store, SourceRoots{
		CodexSessions: root,
		CodexArchived: root,
	}, nil, 2)
	mustNoError(t, restarted.BackfillActive(ctx), "restart backfill")
	assertCodexBudgetMarker(t, store, session.ID, domain.UsageBindingPartial)

	reader := NewSummaryReader(store)
	compact, err := reader.ListCompact(ctx, session.ProjectID)
	if err != nil || len(compact) != 1 {
		t.Fatalf("compact=%+v err=%v", compact, err)
	}
	if compact[0].ProcessedTokens == nil || *compact[0].ProcessedTokens != 12 || !compact[0].Incomplete {
		t.Fatalf("compact summary=%+v", compact[0])
	}
	detail, err := reader.Get(ctx, session.ID)
	mustNoError(t, err)
	if !detail.Incomplete ||
		detail.Totals.InputTokens == nil || *detail.Totals.InputTokens != 10 {
		t.Fatalf("detail summary=%+v", detail)
	}
}

func newCodexBudgetFixture(t *testing.T, limit int) *codexBudgetFixture {
	t.Helper()
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, testCodexRootID, false)
	root := t.TempDir()
	rootPath := filepath.Join(root, "active", "rollout-root.jsonl")
	writeUsageFixture(t, rootPath, codexSessionMetaFixture(t, testCodexRootID, ""))
	now := time.Unix(1700000000, 0).UTC()
	collector := newCollectorWithCodexSourceLimit(store, SourceRoots{
		CodexSessions: root,
		CodexArchived: root,
	}, nil, limit)
	collector.now = func() time.Time { return now }
	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Harness:         domain.HarnessCodex,
		Event:           "session-start",
		NativeSessionID: testCodexRootID,
		TranscriptPath:  rootPath,
	}); err != nil {
		t.Fatal(err)
	}
	return &codexBudgetFixture{
		store:     store,
		collector: collector,
		session:   session,
		binding:   onlyUsageBindingForBudget(t, store, session.ID),
		root:      root,
		rootPath:  rootPath,
		now:       now,
	}
}

func onlyUsageBindingForBudget(
	t *testing.T,
	store *sqlite.Store,
	sessionID domain.SessionID,
) domain.UsageBindingRecord {
	t.Helper()
	bindings, err := store.ListUsageBindingsForSession(context.Background(), sessionID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	return bindings[0]
}

func onlyBudgetSource(t *testing.T, fixture *codexBudgetFixture) domain.UsageSourceRecord {
	t.Helper()
	sources, err := fixture.store.ListUsageSourcesForBinding(context.Background(), fixture.binding.ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	return sources[0]
}

func setCodexDiscoveredChildren(
	t *testing.T,
	store *sqlite.Store,
	source domain.UsageSourceRecord,
	childIDs ...string,
) {
	t.Helper()
	err := store.ApplyUsageChunk(context.Background(), source.ID, source.ByteOffset, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset:      source.ByteOffset,
		ParserStateJSON: codexParserStateWithChildren(t, childIDs...),
		State:           source.State,
		FailureCount:    source.FailureCount,
		AnomalyCount:    source.AnomalyCount,
		UpdatedAt:       time.Now().UTC(),
	}, nil)
	mustNoError(t, err)
}

func codexParserStateWithChildren(t *testing.T, childIDs ...string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"version":     1,
		"source_kind": domain.UsageSourceCodexRollout,
		"codex": map[string]any{
			"baseline":               map[string]any{},
			"pending_spawn_call_ids": []string{},
			"discovered_child_ids":   childIDs,
		},
	})
	mustNoError(t, err)
	return string(raw)
}

func assertCodexBudgetMarker(
	t *testing.T,
	store *sqlite.Store,
	sessionID domain.SessionID,
	wantState domain.UsageBindingState,
) {
	t.Helper()
	binding := onlyUsageBindingForBudget(t, store, sessionID)
	if binding.State != wantState || binding.LastErrorCode != testCodexBudgetCode {
		t.Fatalf("binding=%+v, want state=%s marker=%s", binding, wantState, testCodexBudgetCode)
	}
}
