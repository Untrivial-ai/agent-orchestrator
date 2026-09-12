package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestCollectorRegistersFinalizesAndReactivatesSource(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-1", false)
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "07", "27", "rollout-native-1.jsonl")
	mustNoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	mustNoError(t, os.WriteFile(path, []byte(codexSessionMetaFixture(t, "native-1", "")), 0o600))
	wakes := 0
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, func(bool) { wakes++ })
	now := time.Unix(1700000000, 0).UTC()
	collector.now = func() time.Time { return now }

	err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Harness:         domain.HarnessCodex,
		ProviderHint:    " openai ",
		Event:           "session-start",
		NativeSessionID: "native-1",
		TranscriptPath:  path,
		ModelID:         "gpt-5.6",
	})
	if err != nil {
		t.Fatalf("record start: %v", err)
	}
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	if bindings[0].State != domain.UsageBindingActive || bindings[0].InitialModelID != "gpt-5.6" || bindings[0].ProviderHint != "" ||
		sources[0].State != domain.UsageSourceActive || wakes == 0 {
		t.Fatalf("registered binding=%+v source=%+v wakes=%d", bindings[0], sources[0], wakes)
	}

}

func TestCollectorPersistsOnlyCanonicalClaudeProviderHints(t *testing.T) {
	tests := []struct {
		name    string
		harness domain.AgentHarness
		raw     string
		want    string
	}{
		{name: "canonical anthropic", harness: domain.HarnessClaudeCode, raw: "anthropic", want: "anthropic"},
		{name: "trimmed canonical zai alias", harness: domain.HarnessClaudeCode, raw: " Z.AI ", want: "zai"},
		{name: "canonical bedrock", harness: domain.HarnessClaudeCode, raw: "bedrock", want: "bedrock"},
		{name: "canonical vertex", harness: domain.HarnessClaudeCode, raw: "VERTEX_AI", want: "vertex_ai"},
		{name: "custom provider", harness: domain.HarnessClaudeCode, raw: "custom-provider"},
		{name: "credential", harness: domain.HarnessClaudeCode, raw: "sk-secret-credential"},
		{name: "url", harness: domain.HarnessClaudeCode, raw: "https://api.z.ai/v1?key=secret"},
		{name: "codex hint", harness: domain.HarnessCodex, raw: "openai"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := collectorTestStore(t)
			const nativeID = "native-provider-hint"
			session := collectorTestSession(t, store, test.harness, nativeID, false)
			signal := HookSignal{
				Harness:         test.harness,
				Event:           "session-start",
				NativeSessionID: nativeID,
				ModelID:         "source-model",
				ProviderHint:    test.raw,
			}
			roots := SourceRoots{}
			if test.harness == domain.HarnessCodex {
				root := filepath.Join(t.TempDir(), "sessions")
				path := filepath.Join(root, "rollout-native-provider-hint.jsonl")
				writeUsageFixture(t, path, codexSessionMetaFixture(t, nativeID, ""))
				roots.CodexSessions = root
				signal.TranscriptPath = path
			}
			collector := NewCollector(store, roots, nil)
			if err := collector.RecordHook(context.Background(), session.ID, signal); err != nil {
				t.Fatalf("record hook: %v", err)
			}
			bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
			if err != nil || len(bindings) != 1 {
				t.Fatalf("bindings=%+v err=%v", bindings, err)
			}
			if bindings[0].ProviderHint != test.want {
				t.Fatalf("persisted provider hint = %q, want %q", bindings[0].ProviderHint, test.want)
			}
		})
	}
}

// Break caught: a Claude binding that predates its first hook holds events that
// can never be attributed, because a Claude transcript names no provider. The
// hook's route hint is the only evidence, and it arrives long after those events
// were stored — so the moment it lands has to reopen historical repair, or the
// session stays unpriced until the next daemon start.
func TestCollectorReopensHistoricalRepairWhenAClaudeRouteFirstArrives(t *testing.T) {
	store := collectorTestStore(t)
	const nativeID = "native-late-route"
	session := collectorTestSession(t, store, domain.HarnessClaudeCode, nativeID, false)
	collector := NewCollector(store, SourceRoots{}, nil)
	resolved := 0
	collector.OnRouteResolved(func() { resolved++ })

	hook := func(hint string) {
		t.Helper()
		if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
			Harness:         domain.HarnessClaudeCode,
			Event:           "session-start",
			NativeSessionID: nativeID,
			ModelID:         "claude-test",
			ProviderHint:    hint,
		}); err != nil {
			t.Fatalf("record hook: %v", err)
		}
	}

	// A binding created without a route has no history to reopen yet.
	hook("")
	if resolved != 0 {
		t.Fatalf("route resolutions = %d after the first routeless hook, want 0", resolved)
	}

	hook("anthropic")
	if resolved != 1 {
		t.Fatalf("route resolutions = %d once the route arrives, want 1", resolved)
	}

	// Every later hook repeats the same route. Reopening repair on each one
	// would rescan every legacy source on every turn.
	hook("anthropic")
	if resolved != 1 {
		t.Fatalf("route resolutions = %d after a repeat route, want it to stay 1", resolved)
	}
}

// Break caught: the first hook can prove that a custom Claude route exists
// without naming it. When a later hook identifies that route, treating the
// non-empty "unidentified" sentinel as already resolved leaves the historical
// events on their inferred or unavailable price until the next daemon start.
func TestCollectorReopensHistoricalRepairWhenAClaudeRouteBecomesIdentified(t *testing.T) {
	store := collectorTestStore(t)
	const nativeID = "native-identified-route"
	session := collectorTestSession(t, store, domain.HarnessClaudeCode, nativeID, false)
	collector := NewCollector(store, SourceRoots{}, nil)
	resolved := 0
	collector.OnRouteResolved(func() { resolved++ })

	hook := func(hint string) {
		t.Helper()
		if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
			Harness:         domain.HarnessClaudeCode,
			Event:           "session-start",
			NativeSessionID: nativeID,
			ModelID:         "claude-test",
			ProviderHint:    hint,
		}); err != nil {
			t.Fatalf("record hook: %v", err)
		}
	}

	hook(pricing.UnidentifiedBillingRoute)
	if resolved != 0 {
		t.Fatalf("route resolutions = %d after the unidentified route, want 0", resolved)
	}

	hook("anthropic")
	if resolved != 1 {
		t.Fatalf("route resolutions = %d once the route is identified, want 1", resolved)
	}

	hook("anthropic")
	if resolved != 1 {
		t.Fatalf("route resolutions = %d after a repeat route, want it to stay 1", resolved)
	}
}

func TestCollectorSerializesFinalizationAgainstEarlierHook(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-serialized", false)
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "rollout-native-serialized.jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, "native-serialized", ""))

	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)
	now := time.Unix(1700000000, 0).UTC()
	entered := make(chan struct{})
	release := make(chan struct{})
	var first sync.Once
	collector.now = func() time.Time {
		block := false
		first.Do(func() {
			block = true
			close(entered)
		})
		if block {
			<-release
		}
		return now
	}

	ordinaryDone := make(chan error, 1)
	go func() {
		ordinaryDone <- collector.RecordHook(context.Background(), session.ID, HookSignal{
			Event:           "notification",
			NativeSessionID: "native-serialized",
			TranscriptPath:  path,
		})
	}()
	<-entered

	finalDone := make(chan error, 1)
	go func() {
		finalDone <- collector.RecordHook(context.Background(), session.ID, HookSignal{
			Event:           "process-exited",
			NativeSessionID: "native-serialized",
			TranscriptPath:  path,
		})
	}()
	close(release)
	mustNoError(t, <-ordinaryDone, "ordinary hook")
	mustNoError(t, <-finalDone, "final hook")

	binding, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, "native-serialized")
	if err != nil || !ok {
		t.Fatalf("binding ok=%v err=%v", ok, err)
	}
	if binding.State != domain.UsageBindingFinalizing {
		t.Fatalf("binding state=%s, want finalizing", binding.State)
	}
}

type delayedFinalizeStore struct {
	collectorStore
	entered chan<- struct{}
	release <-chan struct{}
}

func (s *delayedFinalizeStore) FinalizeUsageBindingsForSessionLaunch(
	ctx context.Context,
	sessionID domain.SessionID,
	expectedLaunchID string,
	expectedSessionRevision time.Time,
	at time.Time,
) ([]domain.UsageBindingRecord, error) {
	close(s.entered)
	<-s.release
	return s.collectorStore.FinalizeUsageBindingsForSessionLaunch(
		ctx,
		sessionID,
		expectedLaunchID,
		expectedSessionRevision,
		at,
	)
}

type blockedAfterFinalizeStore struct {
	collectorStore
	finalized chan<- struct{}
	release   <-chan struct{}
}

func (s *blockedAfterFinalizeStore) FinalizeUsageBindingsForSessionLaunch(
	ctx context.Context,
	sessionID domain.SessionID,
	expectedLaunchID string,
	expectedSessionRevision time.Time,
	at time.Time,
) ([]domain.UsageBindingRecord, error) {
	bindings, err := s.collectorStore.FinalizeUsageBindingsForSessionLaunch(
		ctx,
		sessionID,
		expectedLaunchID,
		expectedSessionRevision,
		at,
	)
	if err != nil {
		return nil, err
	}
	close(s.finalized)
	<-s.release
	return bindings, nil
}

type blockedBeforeCollectorFinalizer struct {
	collector *Collector
	entered   chan<- struct{}
	release   <-chan struct{}
}

func (f *blockedBeforeCollectorFinalizer) FinalizeSession(
	ctx context.Context,
	sessionID domain.SessionID,
	expectedLaunchID string,
	expectedSessionRevision time.Time,
) error {
	close(f.entered)
	<-f.release
	return f.collector.FinalizeSession(ctx, sessionID, expectedLaunchID, expectedSessionRevision)
}

func TestCollectorFinalizationSkipsRelaunchCommittedBeforeStorageFence(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-relaunched", false)
	session.Metadata.RuntimeLaunchID = "launch-old"
	mustNoError(t, store.UpdateSession(context.Background(), session))
	now := time.Unix(1700000000, 0).UTC()
	binding := seedCollectorUsageBinding(t, store, session, "native-relaunched", domain.UsageBindingActive, now, "")
	entered := make(chan struct{})
	release := make(chan struct{})
	collector := NewCollector(&delayedFinalizeStore{
		collectorStore: store,
		entered:        entered,
		release:        release,
	}, SourceRoots{}, nil)

	done := make(chan error, 1)
	go func() {
		done <- collector.FinalizeSession(context.Background(), session.ID, "launch-old", session.UpdatedAt)
	}()
	<-entered
	session.Metadata.RuntimeLaunchID = "launch-new"
	session.UpdatedAt = now.Add(time.Second)
	mustNoError(t, store.UpdateSession(context.Background(), session))
	close(release)
	mustNoError(t, <-done)

	got, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, binding.NativeRootID)
	if err != nil || !ok {
		t.Fatalf("binding ok=%v err=%v", ok, err)
	}
	if got.State != domain.UsageBindingActive {
		t.Fatalf("stale finalizer changed live binding state to %s", got.State)
	}
}

func TestCollectorFinalizationSkipsActivityCommittedBeforeStorageFence(t *testing.T) {
	store := collectorTestStore(t)
	now := time.Now().UTC()
	session := collectorTestSession(t, store, domain.HarnessClaudeCode, "native-before-fence", false)
	session.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-2 * time.Minute)}
	session.Metadata.RuntimeLaunchID = "launch-current"
	session.UpdatedAt = now.Add(-2 * time.Minute)
	mustNoError(t, store.UpdateSession(context.Background(), session))
	binding := seedCollectorUsageBinding(
		t, store, session, "native-before-fence", domain.UsageBindingActive, session.UpdatedAt, "",
	)

	collector := NewCollector(store, SourceRoots{}, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	manager := lifecycle.New(store, nil)
	manager.SetUsageFinalizer(&blockedBeforeCollectorFinalizer{
		collector: collector,
		entered:   entered,
		release:   release,
	})

	reaperDone := make(chan error, 1)
	go func() {
		reaperDone <- manager.ApplyRuntimeObservation(context.Background(), session.ID, ports.RuntimeFacts{
			Runtime:  ports.ProbeDead,
			LaunchID: "launch-current",
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reaper did not reach usage finalizer")
	}

	if err := manager.ApplyActivitySignal(context.Background(), session.ID, ports.ActivitySignal{
		Valid:          true,
		State:          domain.ActivityActive,
		Timestamp:      now.Add(time.Second),
		Event:          "post-tool-use",
		AgentSessionID: "native-before-fence",
		LaunchID:       "launch-current",
	}); err != nil {
		t.Fatal(err)
	}
	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Event:           "post-tool-use",
		LaunchID:        "launch-current",
		NativeSessionID: "native-before-fence",
	}); err != nil {
		t.Fatal(err)
	}
	committedSession, ok, err := store.GetSession(context.Background(), session.ID)
	if err != nil || !ok {
		t.Fatalf("session before finalizer ok=%v err=%v", ok, err)
	}
	committedBinding, ok, err := store.GetUsageBinding(
		context.Background(),
		session.ID,
		session.Harness,
		binding.NativeRootID,
	)
	if err != nil || !ok {
		t.Fatalf("binding before finalizer ok=%v err=%v", ok, err)
	}
	if committedSession.UpdatedAt.Equal(session.UpdatedAt) ||
		!committedBinding.UpdatedAt.After(binding.UpdatedAt) ||
		committedBinding.State != domain.UsageBindingActive {
		t.Fatalf("activity/usage did not commit before finalizer: session=%+v binding=%+v", committedSession, committedBinding)
	}

	close(release)
	select {
	case err := <-reaperDone:
		mustNoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("reaper did not finish after finalizer release")
	}
	gotSession, ok, err := store.GetSession(context.Background(), session.ID)
	if err != nil || !ok {
		t.Fatalf("session after finalizer ok=%v err=%v", ok, err)
	}
	gotBinding, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, binding.NativeRootID)
	if err != nil || !ok {
		t.Fatalf("binding after finalizer ok=%v err=%v", ok, err)
	}
	if gotSession.IsTerminated || gotBinding.State != domain.UsageBindingActive {
		t.Fatalf("pre-finalizer activity lost: terminated=%v binding=%s", gotSession.IsTerminated, gotBinding.State)
	}
}

func TestCollectorSessionStartReactivatesAfterOldGenerationFinalization(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-reactivated", false)
	session.Metadata.RuntimeLaunchID = "launch-old"
	mustNoError(t, store.UpdateSession(context.Background(), session))
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "rollout-native-reactivated.jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, "native-reactivated", ""))
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)

	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Event:           "session-start",
		LaunchID:        "launch-old",
		NativeSessionID: "native-reactivated",
		TranscriptPath:  path,
	}); err != nil {
		t.Fatal(err)
	}
	mustNoError(t, collector.FinalizeSession(context.Background(), session.ID, "launch-old", session.UpdatedAt))
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != domain.UsageBindingFinalizing {
		t.Fatalf("finalized bindings=%+v err=%v", bindings, err)
	}

	session.Metadata.RuntimeLaunchID = "launch-new"
	mustNoError(t, store.UpdateSession(context.Background(), session))
	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Event:           "session-start",
		LaunchID:        "launch-new",
		NativeSessionID: "native-reactivated",
		TranscriptPath:  path,
	}); err != nil {
		t.Fatal(err)
	}
	bindings, err = store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != domain.UsageBindingActive {
		t.Fatalf("reactivated bindings=%+v err=%v", bindings, err)
	}
}

func TestCollectorReactivateSessionPreservesCursorWithoutHook(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-restored", false)
	session.Metadata.RuntimeLaunchID = "launch-new"
	mustNoError(t, store.UpdateSession(context.Background(), session))
	now := time.Unix(1700000000, 0).UTC()
	binding := seedCollectorUsageBinding(
		t, store, session, "native-restored", domain.UsageBindingComplete, now, "",
	)
	source, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: "native-restored",
		ArtifactPath:    filepath.Join(t.TempDir(), "rollout-native-restored.jsonl"),
		FileIdentity:    "device:inode",
		ByteOffset:      321,
		ParserStateJSON: `{"version":1}`,
		State:           domain.UsageSourceComplete,
		UpdatedAt:       now,
	})
	mustNoError(t, err)
	wakes := 0
	collector := NewCollector(store, SourceRoots{}, func(reconcile bool) {
		if reconcile {
			wakes++
		}
	})

	mustNoError(t, collector.ReactivateSession(context.Background(), session.ID, "launch-new"))
	gotBinding, ok, err := store.GetUsageBinding(
		context.Background(), session.ID, session.Harness, "native-restored",
	)
	if err != nil || !ok || gotBinding.State != domain.UsageBindingActive {
		t.Fatalf("reactivated binding=%+v ok=%v err=%v", gotBinding, ok, err)
	}
	gotSource, ok, err := store.GetUsageSourceForIngestion(context.Background(), source.ID)
	if err != nil || !ok {
		t.Fatalf("reactivated source ok=%v err=%v", ok, err)
	}
	if gotSource.Source.State != domain.UsageSourceActive || gotSource.Source.ByteOffset != 321 ||
		gotSource.Source.ParserStateJSON != `{"version":1}` || wakes != 1 {
		t.Fatalf("reactivated source=%+v wakes=%d", gotSource.Source, wakes)
	}
	watchable, err := store.ListWatchableUsageSources(context.Background())
	if err != nil || len(watchable) != 1 || watchable[0].ID != source.ID {
		t.Fatalf("watchable sources=%+v err=%v", watchable, err)
	}
}

func TestCollectorRegistersChatUsageWhenLifecycleMarksControllerSpawned(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestChatSession(t, store, domain.HarnessCodex, "", false)

	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "08", "18", "rollout-native-chat.jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, "native-chat", ""))
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)
	manager := lifecycle.New(store, nil)
	manager.SetUsageFinalizer(collector)

	mustNoError(t, manager.MarkSpawned(context.Background(), session.ID, domain.SessionMetadata{
		ProviderConversationID: "native-chat",
	}))
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("chat usage bindings=%+v err=%v, want one binding for the provider conversation", bindings, err)
	}
	if bindings[0].NativeRootID != "native-chat" || bindings[0].State != domain.UsageBindingActive {
		t.Fatalf("chat usage binding=%+v, want active native-chat binding", bindings[0])
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("chat usage sources=%+v err=%v, want the provider rollout", sources, err)
	}
	wantPath := canonicalUsagePath(t, path)
	if sources[0].ArtifactPath != wantPath {
		t.Fatalf("chat usage source path=%q, want %q", sources[0].ArtifactPath, wantPath)
	}
}

func TestCollectorReactivateSessionRejectsStaleLaunch(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-stale", false)
	session.Metadata.RuntimeLaunchID = "launch-current"
	mustNoError(t, store.UpdateSession(context.Background(), session))
	binding := seedCollectorUsageBinding(
		t, store, session, "native-stale", domain.UsageBindingComplete, time.Now().UTC(), "",
	)
	collector := NewCollector(store, SourceRoots{}, nil)

	mustNoError(t, collector.ReactivateSession(context.Background(), session.ID, "launch-old"))
	got, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, "native-stale")
	if err != nil || !ok || got.State != binding.State {
		t.Fatalf("stale launch changed binding=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestCollectorHookLifecycleTransitions(t *testing.T) {
	complete, active := domain.UsageSourceComplete, domain.UsageSourceActive
	tests := []struct {
		name, event, hookLaunch string
		activity                domain.ActivityState
		binding                 domain.UsageBindingState
		wantBinding             domain.UsageBindingState
		wantSource              *domain.UsageSourceState
	}{
		{"current activity reactivates", "post-tool-use", "launch-current", domain.ActivityIdle, domain.UsageBindingFinalizing, domain.UsageBindingActive, &active},
		{"terminal event finalizes", "process-exited", "launch-current", domain.ActivityIdle, domain.UsageBindingActive, domain.UsageBindingFinalizing, nil},
		{"exited session ignores activity", "post-tool-use", "launch-current", domain.ActivityExited, domain.UsageBindingFinalizing, domain.UsageBindingFinalizing, &complete},
		{"stale launch ignores activity", "post-tool-use", "launch-stale", domain.ActivityIdle, domain.UsageBindingFinalizing, domain.UsageBindingFinalizing, &complete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := collectorTestStore(t)
			nativeID := "native-lifecycle"
			session := collectorTestSessionWithActivity(t, store, domain.HarnessClaudeCode, nativeID, false, test.activity)
			session.Metadata.RuntimeLaunchID = "launch-current"
			mustNoError(t, store.UpdateSession(context.Background(), session))
			now := time.Now().UTC()
			binding := seedCollectorUsageBinding(t, store, session, nativeID, test.binding, now, "")
			source, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
				BindingID: binding.ID, Kind: domain.UsageSourceClaudeMain, NativeSessionID: nativeID,
				ArtifactPath: "/tmp/native-lifecycle.jsonl", FileIdentity: nativeID,
				State: domain.UsageSourceComplete, UpdatedAt: now,
			})
			mustNoError(t, err)

			collector := NewCollector(store, SourceRoots{}, nil)
			mustNoError(t, collector.RecordHook(context.Background(), session.ID, HookSignal{
				Event: test.event, LaunchID: test.hookLaunch, NativeSessionID: nativeID,
			}))
			got, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, nativeID)
			if err != nil || !ok || got.State != test.wantBinding {
				t.Fatalf("binding = %+v, ok=%v err=%v; want %s", got, ok, err, test.wantBinding)
			}
			if test.wantSource != nil {
				got, ok, err := store.GetUsageSourceForIngestion(context.Background(), source.ID)
				if err != nil || !ok || got.Source.State != *test.wantSource {
					t.Fatalf("source = %+v, ok=%v err=%v; want %s", got.Source, ok, err, *test.wantSource)
				}
			}
		})
	}
}

func TestCollectorIgnoresIneligibleHooks(t *testing.T) {
	tests := []struct {
		name, event string
		harness     domain.AgentHarness
		activity    domain.ActivityState
		terminated  bool
		withPath    bool
	}{
		{"terminated session", "process-exited", domain.HarnessClaudeCode, domain.ActivityIdle, true, true},
		{"terminal recovery without source", "process-exited", domain.HarnessClaudeCode, domain.ActivityExited, false, false},
		{"unsupported harness", "post-tool-use", domain.HarnessAider, domain.ActivityIdle, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := collectorTestStore(t)
			session := collectorTestSessionWithActivity(t, store, test.harness, "native-ignored", test.terminated, test.activity)
			session.Metadata.RuntimeLaunchID = "launch-current"
			mustNoError(t, store.UpdateSession(context.Background(), session))
			signal := HookSignal{Event: test.event, LaunchID: "launch-current", NativeSessionID: "native-ignored"}
			roots := SourceRoots{}
			if test.withPath {
				roots.ClaudeProjects = filepath.Join(t.TempDir(), "projects")
				signal.TranscriptPath = filepath.Join(roots.ClaudeProjects, "workspace", "native-ignored.jsonl")
				writeUsageFixture(t, signal.TranscriptPath, "{}\n")
			}
			mustNoError(t, NewCollector(store, roots, nil).RecordHook(context.Background(), session.ID, signal))
			bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
			if err != nil || len(bindings) != 0 {
				t.Fatalf("bindings = %+v, err=%v; want none", bindings, err)
			}
		})
	}
}

func TestCollectorTerminalRecoveryRegistersProvidedOrDiscoveredSource(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provided bool
	}{
		{name: "provided", provided: true},
		{name: "discovered"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := collectorTestStore(t)
			session := collectorTestSessionWithActivity(
				t,
				store,
				domain.HarnessClaudeCode,
				"native-recovery",
				false,
				domain.ActivityExited,
			)
			session.Metadata.RuntimeLaunchID = "launch-current"
			mustNoError(t, store.UpdateSession(context.Background(), session))
			root := filepath.Join(t.TempDir(), "projects")
			path := filepath.Join(root, "workspace", "native-recovery.jsonl")
			writeUsageFixture(t, path, "{}\n")
			signal := HookSignal{
				Event:           "process-exited",
				LaunchID:        "launch-current",
				NativeSessionID: "native-recovery",
			}
			if tt.provided {
				signal.TranscriptPath = path
			}

			collector := NewCollector(store, SourceRoots{ClaudeProjects: root}, nil)
			mustNoError(t, collector.RecordHook(context.Background(), session.ID, signal))
			bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
			if err != nil || len(bindings) != 1 {
				t.Fatalf("terminal recovery bindings=%+v err=%v", bindings, err)
			}
			sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
			if err != nil || len(sources) != 1 {
				t.Fatalf("terminal recovery sources=%+v err=%v", sources, err)
			}
			if bindings[0].State != domain.UsageBindingFinalizing || sources[0].ArtifactPath != canonicalUsagePath(t, path) {
				t.Fatalf("terminal recovery binding/source=%+v/%+v", bindings[0], sources[0])
			}
		})
	}
}

func TestCollectorCurrentActivityReactivatesBindingDuringReaperFinalization(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessClaudeCode, "native-race", false)
	now := time.Now().UTC()
	session.Activity.LastActivityAt = now.Add(-2 * time.Minute)
	session.Metadata.RuntimeLaunchID = "launch-current"
	mustNoError(t, store.UpdateSession(context.Background(), session))
	binding := seedCollectorUsageBinding(t, store, session, "native-race", domain.UsageBindingActive, now, "")

	finalized := make(chan struct{})
	release := make(chan struct{})
	collector := NewCollector(&blockedAfterFinalizeStore{
		collectorStore: store,
		finalized:      finalized,
		release:        release,
	}, SourceRoots{}, nil)
	manager := lifecycle.New(store, nil)
	manager.SetUsageFinalizer(collector)

	reaperDone := make(chan error, 1)
	go func() {
		reaperDone <- manager.ApplyRuntimeObservation(context.Background(), session.ID, ports.RuntimeFacts{
			Runtime:  ports.ProbeDead,
			LaunchID: "launch-current",
		})
	}()
	select {
	case <-finalized:
	case <-time.After(2 * time.Second):
		t.Fatal("finalizer did not commit")
	}
	during, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, binding.NativeRootID)
	if err != nil || !ok {
		t.Fatalf("binding during finalization ok=%v err=%v", ok, err)
	}
	if during.State != domain.UsageBindingFinalizing {
		t.Fatalf("binding during finalization=%s, want finalizing", during.State)
	}

	activityApplied := make(chan struct{})
	hookDone := make(chan error, 1)
	go func() {
		err := manager.ApplyActivitySignal(context.Background(), session.ID, ports.ActivitySignal{
			Valid:          true,
			State:          domain.ActivityActive,
			Timestamp:      now.Add(time.Second),
			Event:          "post-tool-use",
			AgentSessionID: "native-race",
			LaunchID:       "launch-current",
		})
		close(activityApplied)
		if err == nil {
			err = collector.RecordHook(context.Background(), session.ID, HookSignal{
				Event:           "post-tool-use",
				LaunchID:        "launch-current",
				NativeSessionID: "native-race",
			})
		}
		hookDone <- err
	}()
	select {
	case <-activityApplied:
	case <-time.After(2 * time.Second):
		t.Fatal("current activity did not persist while finalizer was blocked")
	}
	close(release)
	mustNoError(t, <-reaperDone)
	mustNoError(t, <-hookDone)

	gotSession, ok, err := store.GetSession(context.Background(), session.ID)
	if err != nil || !ok {
		t.Fatalf("session ok=%v err=%v", ok, err)
	}
	gotBinding, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, binding.NativeRootID)
	if err != nil || !ok {
		t.Fatalf("binding after activity ok=%v err=%v", ok, err)
	}
	if gotSession.IsTerminated || gotBinding.State != domain.UsageBindingActive {
		t.Fatalf("session terminated=%v binding=%s, want false/active", gotSession.IsTerminated, gotBinding.State)
	}
}

func TestCollectorIgnoresUsageSignalFromStaleRuntimeLaunch(t *testing.T) {
	store := collectorTestStore(t)
	now := time.Now().UTC()
	session, err := store.CreateSession(context.Background(), domain.SessionRecord{
		ProjectID: "usage-test",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		Metadata: domain.SessionMetadata{
			AgentSessionID:  "native-fenced",
			RuntimeLaunchID: "launch-current",
		},
		CreatedAt: now,
		UpdatedAt: now,
	})
	mustNoError(t, err)
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "07", "28", "rollout-native-fenced.jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, "native-fenced", ""))
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)

	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Harness:         domain.HarnessCodex,
		Event:           "session-start",
		LaunchID:        "launch-current",
		NativeSessionID: "native-fenced",
		TranscriptPath:  path,
	}); err != nil {
		t.Fatal(err)
	}
	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Event:    "process-exited",
		LaunchID: "launch-old",
	}); err != nil {
		t.Fatal(err)
	}

	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	if bindings[0].State != domain.UsageBindingActive {
		t.Fatalf("stale launch finalized usage binding: %+v", bindings[0])
	}
}

func TestCollectorRejectsPathOutsideProviderRootAndSymlinkEscape(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessClaudeCode, "claude-1", false)
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	outside := filepath.Join(base, "outside.jsonl")
	mustNoError(t, os.MkdirAll(root, 0o700))
	mustNoError(t, os.WriteFile(outside, []byte("{}\n"), 0o600))
	collector := NewCollector(store, SourceRoots{ClaudeProjects: root}, nil)
	signal := HookSignal{
		Harness:         domain.HarnessClaudeCode,
		Event:           "session-start",
		NativeSessionID: "claude-1",
		TranscriptPath:  outside,
	}
	if err := collector.RecordHook(context.Background(), session.ID, signal); err == nil {
		t.Fatal("outside path accepted")
	} else if strings.Contains(err.Error(), outside) {
		t.Fatalf("validation error exposed artifact path: %v", err)
	}

	link := filepath.Join(root, "escape.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	signal.TranscriptPath = link
	if err := collector.RecordHook(context.Background(), session.ID, signal); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestCollectorRejectsHookPathAttributionMismatches(t *testing.T) {
	t.Run("Codex session id", func(t *testing.T) {
		store := collectorTestStore(t)
		session := collectorTestSession(t, store, domain.HarnessCodex, "codex-claimed", false)
		root := filepath.Join(t.TempDir(), "sessions")
		path := filepath.Join(root, "2026", "08", "02", "rollout.jsonl")
		writeUsageFixture(t, path, codexSessionMetaFixture(t, "codex-other", ""))
		collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)

		err := collector.RecordHook(context.Background(), session.ID, HookSignal{
			Event:           "session-start",
			NativeSessionID: "codex-claimed",
			TranscriptPath:  path,
		})
		if err == nil {
			t.Fatal("Codex path with another session id was accepted")
		}
		assertNoUsageSourcesForSession(t, store, session.ID)
	})

	t.Run("Codex child claimed as root", func(t *testing.T) {
		store := collectorTestStore(t)
		session := collectorTestSession(t, store, domain.HarnessCodex, "codex-child", false)
		root := filepath.Join(t.TempDir(), "sessions")
		path := filepath.Join(root, "2026", "08", "02", "child.jsonl")
		writeUsageFixture(t, path, codexSessionMetaFixture(t, "codex-child", "codex-parent"))
		collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)

		err := collector.RecordHook(context.Background(), session.ID, HookSignal{
			Event:           "session-start",
			NativeSessionID: "codex-child",
			TranscriptPath:  path,
		})
		if err == nil {
			t.Fatal("Codex child rollout was accepted as a root source")
		}
		assertNoUsageSourcesForSession(t, store, session.ID)
	})

	t.Run("Claude main filename", func(t *testing.T) {
		store := collectorTestStore(t)
		session := collectorTestSession(t, store, domain.HarnessClaudeCode, "claude-claimed", false)
		root := filepath.Join(t.TempDir(), "projects")
		path := filepath.Join(root, "workspace", "claude-other.jsonl")
		writeUsageFixture(t, path, "{}\n")
		collector := NewCollector(store, SourceRoots{ClaudeProjects: root}, nil)

		err := collector.RecordHook(context.Background(), session.ID, HookSignal{
			Event:           "session-start",
			NativeSessionID: "claude-claimed",
			TranscriptPath:  path,
		})
		if err == nil {
			t.Fatal("Claude path with another root filename was accepted")
		}
		assertNoUsageSourcesForSession(t, store, session.ID)
	})

	t.Run("Claude subagent root", func(t *testing.T) {
		store := collectorTestStore(t)
		session := collectorTestSession(t, store, domain.HarnessClaudeCode, "claude-root", false)
		root := filepath.Join(t.TempDir(), "projects")
		path := filepath.Join(root, "workspace", "claude-other", "subagents", "agent-sub-1.jsonl")
		writeUsageFixture(t, path, "{}\n")
		collector := NewCollector(store, SourceRoots{ClaudeProjects: root}, nil)

		err := collector.RecordHook(context.Background(), session.ID, HookSignal{
			Event:                  "subagent-stop",
			NativeSessionID:        "claude-root",
			SubagentID:             "sub-1",
			SubagentTranscriptPath: path,
		})
		if err == nil {
			t.Fatal("Claude subagent path under another root session was accepted")
		}
		assertNoUsageSourcesForSession(t, store, session.ID)
	})

	t.Run("Claude subagent id", func(t *testing.T) {
		store := collectorTestStore(t)
		session := collectorTestSession(t, store, domain.HarnessClaudeCode, "claude-root", false)
		root := filepath.Join(t.TempDir(), "projects")
		path := filepath.Join(root, "workspace", "claude-root", "subagents", "agent-sub-other.jsonl")
		writeUsageFixture(t, path, "{}\n")
		collector := NewCollector(store, SourceRoots{ClaudeProjects: root}, nil)

		err := collector.RecordHook(context.Background(), session.ID, HookSignal{
			Event:                  "subagent-stop",
			NativeSessionID:        "claude-root",
			SubagentID:             "sub-1",
			SubagentTranscriptPath: path,
		})
		if err == nil {
			t.Fatal("Claude subagent path with another agent id was accepted")
		}
		assertNoUsageSourcesForSession(t, store, session.ID)
	})
}

func TestCollectorRejectsCodexChildPathWithWrongParent(t *testing.T) {
	ctx := context.Background()
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "codex-root", false)
	now := time.Unix(1700000000, 0).UTC()
	binding := seedCollectorUsageBinding(t, store, session, "codex-root", domain.UsageBindingActive, now, "")
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "08", "02", "child.jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, "codex-child", "codex-wrong-parent"))
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)

	if _, err := collector.registerSource(
		ctx,
		binding,
		domain.UsageSourceCodexRollout,
		"codex-child",
		"codex-child",
		path,
		now,
		false,
	); err == nil {
		t.Fatal("Codex child path with another parent was accepted")
	}
	assertNoUsageSourcesForSession(t, store, session.ID)
}

func TestCollectorPersistsCodexChildDirectParent(t *testing.T) {
	const (
		parentID = "11111111-1111-4111-8111-111111111111"
		childID  = "22222222-2222-4222-8222-222222222222"
	)
	ctx := context.Background()
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, parentID, false)
	now := time.Unix(1700000000, 0).UTC()
	binding := seedCollectorUsageBinding(t, store, session, parentID, domain.UsageBindingActive, now, "")
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "08", "02", "child.jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, childID, parentID))
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)

	if _, err := collector.registerSourceWithExpectedParent(
		ctx,
		binding,
		domain.UsageSourceCodexRollout,
		childID,
		childID,
		path,
		now,
		false,
		parentID,
	); err != nil {
		t.Fatalf("register child: %v", err)
	}
	sources, err := store.ListUsageSourcesForBinding(ctx, binding.ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("child sources = %+v, err=%v", sources, err)
	}
	var state struct {
		Version    int                    `json:"version"`
		SourceKind domain.UsageSourceKind `json:"source_kind"`
		Codex      *struct {
			NativeSessionID string `json:"native_session_id"`
			DirectParentID  string `json:"direct_parent_id"`
		} `json:"codex"`
	}
	mustNoError(t, json.Unmarshal([]byte(sources[0].ParserStateJSON), &state), "decode child parser state")
	if state.Version != 1 || state.SourceKind != domain.UsageSourceCodexRollout ||
		state.Codex == nil || state.Codex.NativeSessionID != childID || state.Codex.DirectParentID != parentID {
		t.Fatalf("child parser state = %s", sources[0].ParserStateJSON)
	}
}

func TestCollectorDoesNotDoubleAttributeCodexChildAsRoot(t *testing.T) {
	ctx := context.Background()
	store := collectorTestStore(t)
	parentSession := collectorTestSession(t, store, domain.HarnessCodex, "codex-parent", false)
	root := filepath.Join(t.TempDir(), "sessions")
	childPath := filepath.Join(root, "2026", "08", "02", "child.jsonl")
	writeUsageFixture(t, childPath, codexSessionMetaFixture(t, "codex-child", "codex-parent"))
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)
	now := time.Unix(1700000000, 0).UTC()
	parentBinding := seedCollectorUsageBinding(
		t, store, parentSession, "codex-parent", domain.UsageBindingActive, now, "",
	)
	if _, err := collector.registerSource(
		ctx,
		parentBinding,
		domain.UsageSourceCodexRollout,
		"codex-child",
		"codex-child",
		childPath,
		now,
		false,
	); err != nil {
		t.Fatalf("register child under parent: %v", err)
	}

	childSession := collectorTestSession(t, store, domain.HarnessCodex, "codex-child", false)
	if err := collector.RecordHook(ctx, childSession.ID, HookSignal{
		Event:           "session-start",
		NativeSessionID: "codex-child",
		TranscriptPath:  childPath,
	}); err == nil {
		t.Fatal("child rollout was also accepted as a root source")
	}
	parentSources, err := store.ListUsageSourcesForBinding(ctx, parentBinding.ID)
	if err != nil || len(parentSources) != 1 || parentSources[0].SubagentID != "codex-child" {
		t.Fatalf("parent sources = %+v, err=%v", parentSources, err)
	}
	assertNoUsageSourcesForSession(t, store, childSession.ID)
}

func TestCollectorAttributionMismatchDoesNotReplaceExistingSource(t *testing.T) {
	ctx := context.Background()
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "codex-root", false)
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "08", "02", "rollout.jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, "codex-root", ""))
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)
	signal := HookSignal{
		Event:           "session-start",
		NativeSessionID: "codex-root",
		TranscriptPath:  path,
	}
	mustNoError(t, collector.RecordHook(ctx, session.ID, signal), "register original source")
	binding, ok, err := store.GetUsageBinding(ctx, session.ID, session.Harness, "codex-root")
	if err != nil || !ok {
		t.Fatalf("binding: ok=%v err=%v", ok, err)
	}
	sources, err := store.ListUsageSourcesForBinding(ctx, binding.ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("original sources=%+v err=%v", sources, err)
	}
	original := sources[0]
	replacement := path + ".replacement"
	writeUsageFixture(t, replacement, codexSessionMetaFixture(t, "codex-other", ""))
	mustNoError(t, os.Rename(replacement, path))

	if err := collector.RecordHook(ctx, session.ID, signal); err == nil {
		t.Fatal("mismatched replacement was accepted")
	}
	sources, err = store.ListUsageSourcesForBinding(ctx, binding.ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources after rejection=%+v err=%v", sources, err)
	}
	if sources[0].ID != original.ID || sources[0].State != original.State ||
		sources[0].LastErrorCode != original.LastErrorCode {
		t.Fatalf("original source changed after rejection: before=%+v after=%+v", original, sources[0])
	}
}

func TestCollectorBackfillsOnlyNonTerminatedSupportedSessions(t *testing.T) {
	store := collectorTestStore(t)
	active := collectorTestSession(t, store, domain.HarnessClaudeCode, "active-native", false)
	_ = collectorTestSession(t, store, domain.HarnessClaudeCode, "terminated-native", true)
	_ = collectorTestSession(t, store, domain.HarnessAider, "unsupported-native", false)
	root := filepath.Join(t.TempDir(), "projects")
	path := filepath.Join(root, "workspace", "active-native.jsonl")
	mustNoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	mustNoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	collector := NewCollector(store, SourceRoots{ClaudeProjects: root}, nil)
	mustNoError(t, collector.BackfillActive(context.Background()), "backfill")
	bindings, err := store.ListUsageBindingsForSession(context.Background(), active.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("active bindings=%+v err=%v", bindings, err)
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("active sources=%+v err=%v", sources, err)
	}
}

func TestCollectorBackfillsChatSessionFromProviderConversationID(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestChatSession(t, store, domain.HarnessCodex, "native-chat-backfill", false)
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "08", "18", "rollout-native-chat-backfill.jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, "native-chat-backfill", ""))
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)

	mustNoError(t, collector.BackfillActive(context.Background()), "backfill chat usage")
	binding, ok, err := store.GetUsageBinding(
		context.Background(), session.ID, session.Harness, "native-chat-backfill",
	)
	if err != nil || !ok || binding.State != domain.UsageBindingActive {
		t.Fatalf("chat backfill binding=%+v ok=%v err=%v", binding, ok, err)
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), binding.ID)
	if err != nil || len(sources) != 1 || sources[0].ArtifactPath != canonicalUsagePath(t, path) {
		t.Fatalf("chat backfill sources=%+v err=%v", sources, err)
	}
}

func TestCollectorReconcilesCodexSourceCreatedAfterDaemonStart(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-late", false)
	root := filepath.Join(t.TempDir(), "sessions")
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)

	mustNoError(t, collector.BackfillActive(context.Background()), "initial backfill")
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != domain.UsageBindingDiscovering {
		t.Fatalf("initial bindings=%+v err=%v", bindings, err)
	}

	path := filepath.Join(root, "2026", "07", "28", "rollout-native-late.jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, "native-late", ""))
	mustNoError(t, collector.ReconcileSources(context.Background(), 8), "reconcile")

	bindings, _ = store.ListUsageBindingsForSession(context.Background(), session.ID)
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	mustNoError(t, err)
	if bindings[0].State != domain.UsageBindingActive || sources[0].ArtifactPath != resolvedPath {
		t.Fatalf("binding/source=%+v/%+v", bindings[0], sources[0])
	}
}

func TestCollectorDiscoversFinalizingCodexSourceAndArchivedRelocation(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSessionWithActivity(t, store, domain.HarnessCodex, "native-exit", false, domain.ActivityExited)
	sessionsRoot := filepath.Join(t.TempDir(), "sessions")
	archiveRoot := filepath.Join(t.TempDir(), "archived_sessions")
	collector := NewCollector(store, SourceRoots{CodexSessions: sessionsRoot, CodexArchived: archiveRoot}, nil)

	mustNoError(t, collector.BackfillActive(context.Background()), "backfill finalizing")
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != domain.UsageBindingFinalizing {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}

	activePath := filepath.Join(sessionsRoot, "2026", "07", "28", "rollout-native-exit.jsonl")
	writeUsageFixture(t, activePath, codexSessionMetaFixture(t, "native-exit", ""))
	mustNoError(t, collector.ReconcileSources(context.Background(), 8), "discover active path")
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("active sources=%+v err=%v", sources, err)
	}

	archivedPath := filepath.Join(archiveRoot, filepath.Base(activePath))
	mustNoError(t, os.MkdirAll(filepath.Dir(archivedPath), 0o700))
	mustNoError(t, os.Rename(activePath, archivedPath))
	mustNoError(t, collector.ReconcileSources(context.Background(), 8), "discover archived path")
	sources, err = store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 2 {
		t.Fatalf("relocated sources=%+v err=%v", sources, err)
	}
	resolvedArchivedPath, err := filepath.EvalSymlinks(archivedPath)
	mustNoError(t, err)
	if sources[0].State != domain.UsageSourceComplete || sources[1].ArtifactPath != resolvedArchivedPath ||
		sources[0].LastErrorCode != domain.UsageErrorArtifactReplaced ||
		sources[1].ByteOffset != sources[0].ByteOffset {
		t.Fatalf("relocated sources=%+v", sources)
	}
	watchable, err := store.ListWatchableUsageSources(context.Background())
	mustNoError(t, err)
	if len(watchable) != 1 || watchable[0].ID != sources[1].ID {
		t.Fatalf("watchable relocated sources=%+v, want only generation %d", watchable, sources[1].ID)
	}
}

func TestCollectorBackfillPreservesCompletedExitedBinding(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSessionWithActivity(t, store, domain.HarnessCodex, "native-complete", false, domain.ActivityExited)
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "07", "28", "rollout-native-complete.jsonl")
	writeUsageFixture(t, path, `{"type":"session_meta"}`+"\n")
	now := time.Now().UTC()
	binding := seedCollectorUsageBinding(t, store, session, "native-complete", domain.UsageBindingComplete, now, "")
	identity, err := SourceIdentity(context.Background(), path)
	mustNoError(t, err)
	source, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
		BindingID:    binding.ID,
		Kind:         domain.UsageSourceCodexRollout,
		ArtifactPath: path,
		FileIdentity: identity,
		State:        domain.UsageSourceComplete,
		UpdatedAt:    now,
	})
	mustNoError(t, err)

	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)
	mustNoError(t, collector.BackfillActive(context.Background()), "backfill")
	gotBinding, _, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, "native-complete")
	mustNoError(t, err)
	gotSource, ok, err := store.GetUsageSourceForIngestion(context.Background(), source.ID)
	if err != nil || !ok {
		t.Fatalf("source ok=%v err=%v", ok, err)
	}
	if gotBinding.State != domain.UsageBindingComplete || gotSource.Source.State != domain.UsageSourceComplete {
		t.Fatalf("backfill reopened completed usage: binding=%s source=%s", gotBinding.State, gotSource.Source.State)
	}
}

func TestCollectorBackfillReactivatesLiveSourceFromStoredCursor(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-live", false)
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "08", "06", "rollout-native-live.jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, "native-live", ""))
	path, err := filepath.EvalSymlinks(path)
	mustNoError(t, err)
	now := time.Now().UTC()
	binding := seedCollectorUsageBinding(t, store, session, "native-live", domain.UsageBindingComplete, now, "")
	identity, err := SourceIdentity(context.Background(), path)
	mustNoError(t, err)
	parserState, err := initialCodexParserState("native-live", "")
	mustNoError(t, err)
	source, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: "native-live",
		ArtifactPath:    path,
		FileIdentity:    identity,
		ByteOffset:      12,
		ParserStateJSON: parserState,
		State:           domain.UsageSourceError,
		FailureCount:    4,
		LastErrorCode:   domain.UsageErrorInvalidParserState,
		UpdatedAt:       now,
	})
	mustNoError(t, err)

	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)
	mustNoError(t, collector.BackfillActive(context.Background()), "backfill")
	gotBinding, _, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, "native-live")
	mustNoError(t, err)
	gotSource, ok, err := store.GetUsageSourceForIngestion(context.Background(), source.ID)
	if err != nil || !ok {
		t.Fatalf("source ok=%v err=%v", ok, err)
	}
	if gotBinding.State != domain.UsageBindingActive || gotSource.Source.State != domain.UsageSourceActive ||
		gotSource.Source.ByteOffset != 12 || gotSource.Source.FailureCount != 0 || gotSource.Source.LastErrorCode != "" {
		t.Fatalf("backfilled binding/source=%+v/%+v", gotBinding, gotSource.Source)
	}
}

func TestCollectorBackfillDiscoversClaudeSubagentSources(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessClaudeCode, "claude-root", false)
	root := filepath.Join(t.TempDir(), "projects")
	mainPath := filepath.Join(root, "workspace", "claude-root.jsonl")
	subagentPath := filepath.Join(root, "workspace", "claude-root", "subagents", "agent-sub-7.jsonl")
	writeUsageFixture(t, mainPath, `{"type":"assistant"}`+"\n")
	writeUsageFixture(t, subagentPath, `{"type":"assistant"}`+"\n")

	collector := NewCollector(store, SourceRoots{ClaudeProjects: root}, nil)
	mustNoError(t, collector.BackfillActive(context.Background()), "backfill")
	bindings, _ := store.ListUsageBindingsForSession(context.Background(), session.ID)
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	if sources[0].Kind != domain.UsageSourceClaudeMain ||
		sources[1].Kind != domain.UsageSourceClaudeSubagent ||
		sources[1].SubagentID != "sub-7" {
		t.Fatalf("sources=%+v", sources)
	}
}

func TestCollectorRegisteringClaudeSiblingDoesNotRetireExistingSubagent(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessClaudeCode, "claude-siblings", false)
	root := filepath.Join(t.TempDir(), "projects")
	mainPath := filepath.Join(root, "workspace", "claude-siblings.jsonl")
	firstPath := filepath.Join(root, "workspace", "claude-siblings", "subagents", "agent-first.jsonl")
	secondPath := filepath.Join(root, "workspace", "claude-siblings", "subagents", "agent-second.jsonl")
	writeUsageFixture(t, mainPath, `{"type":"assistant"}`+"\n")
	writeUsageFixture(t, firstPath, `{"type":"assistant","agent":"first"}`+"\n")
	writeUsageFixture(t, secondPath, `{"type":"assistant","agent":"second"}`+"\n")

	collector := NewCollector(store, SourceRoots{ClaudeProjects: root}, nil)
	mustNoError(t, collector.BackfillActive(context.Background()), "backfill")
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	mustNoError(t, err)
	states := make(map[string]domain.UsageSourceState)
	for _, source := range sources {
		if source.Kind == domain.UsageSourceClaudeSubagent {
			states[source.SubagentID] = source.State
		}
	}
	if states["first"] != domain.UsageSourcePending || states["second"] != domain.UsageSourcePending {
		t.Fatalf("Claude sibling states = %+v, sources=%+v", states, sources)
	}
}

func TestCollectorSubagentStopReactivatesCompletedSource(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessClaudeCode, "claude-late", false)
	root := filepath.Join(t.TempDir(), "projects")
	subagentPath := filepath.Join(root, "workspace", "claude-late", "subagents", "agent-sub-late.jsonl")
	initial := []byte(`{"type":"assistant"}` + "\n")
	writeUsageFixture(t, subagentPath, string(initial))
	now := time.Now().UTC()
	binding := seedCollectorUsageBinding(t, store, session, "claude-late", domain.UsageBindingComplete, now, "")
	identity, err := SourceIdentity(context.Background(), subagentPath)
	mustNoError(t, err)
	resolvedSubagentPath, err := filepath.EvalSymlinks(subagentPath)
	mustNoError(t, err)
	source, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceClaudeSubagent,
		NativeSessionID: "claude-late",
		SubagentID:      "sub-late",
		ArtifactPath:    resolvedSubagentPath,
		FileIdentity:    identity,
		ByteOffset:      int64(len(initial)),
		State:           domain.UsageSourceComplete,
		UpdatedAt:       now,
	})
	mustNoError(t, err)
	file, err := os.OpenFile(subagentPath, os.O_APPEND|os.O_WRONLY, 0)
	mustNoError(t, err)
	if _, err := file.WriteString(`{"type":"assistant","late":true}` + "\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	mustNoError(t, file.Close())

	collector := NewCollector(store, SourceRoots{ClaudeProjects: root}, nil)
	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Event:                  "subagent-stop",
		NativeSessionID:        "claude-late",
		SubagentID:             "sub-late",
		SubagentTranscriptPath: subagentPath,
	}); err != nil {
		t.Fatalf("record subagent stop: %v", err)
	}

	gotBinding, _, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, "claude-late")
	mustNoError(t, err)
	gotSource, ok, err := store.GetUsageSourceForIngestion(context.Background(), source.ID)
	if err != nil || !ok {
		t.Fatalf("source ok=%v err=%v", ok, err)
	}
	if gotBinding.State != domain.UsageBindingFinalizing || gotSource.Source.State != domain.UsageSourceActive {
		t.Fatalf("binding/source states=%s/%s", gotBinding.State, gotSource.Source.State)
	}
}

func TestDiscoverClaudeSubagentPathsReturnsAllSources(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "claude-many.jsonl")
	writeUsageFixture(t, mainPath, "{}\n")
	for index := 0; index < 129; index++ {
		path := filepath.Join(root, "claude-many", "subagents", fmt.Sprintf("agent-%03d.jsonl", index))
		writeUsageFixture(t, path, "{}\n")
	}

	paths, err := discoverClaudeSubagentPaths(context.Background(), mainPath)
	mustNoError(t, err)
	if len(paths) != 129 {
		t.Fatalf("discovered %d subagent paths, want 129", len(paths))
	}
}

func TestCollectorDiscoveryLimitRotatesPendingBindings(t *testing.T) {
	store := collectorTestStore(t)
	first := collectorTestSession(t, store, domain.HarnessCodex, "native-first", false)
	second := collectorTestSession(t, store, domain.HarnessCodex, "native-second", false)
	root := filepath.Join(t.TempDir(), "sessions")
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)
	now := time.Unix(1700000000, 0).UTC()
	collector.now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	mustNoError(t, collector.BackfillActive(context.Background()), "backfill")

	secondPath := filepath.Join(root, "2026", "07", "28", "rollout-native-second.jsonl")
	writeUsageFixture(t, secondPath, codexSessionMetaFixture(t, "native-second", ""))
	mustNoError(t, collector.ReconcileSources(context.Background(), 1), "first reconcile")
	mustNoError(t, collector.ReconcileSources(context.Background(), 1), "second reconcile")
	firstBindings, _ := store.ListUsageBindingsForSession(context.Background(), first.ID)
	secondBindings, _ := store.ListUsageBindingsForSession(context.Background(), second.ID)
	firstSources, _ := store.ListUsageSourcesForBinding(context.Background(), firstBindings[0].ID)
	secondSources, _ := store.ListUsageSourcesForBinding(context.Background(), secondBindings[0].ID)
	if len(firstSources) != 0 || len(secondSources) != 1 {
		t.Fatalf("first/second sources=%+v/%+v", firstSources, secondSources)
	}
}

func TestCollectorResumeReactivatesAllLatestCodexSources(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-resume", false)
	now := time.Now().UTC()
	binding := seedCollectorUsageBinding(t, store, session, "native-resume", domain.UsageBindingComplete, now, "")
	oldSource, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: "native-resume",
		ArtifactPath:    "/tmp/usage-parent.jsonl",
		FileIdentity:    "parent-old",
		Generation:      0,
		State:           domain.UsageSourceComplete,
		UpdatedAt:       now,
	})
	mustNoError(t, err)
	latestSource, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: "native-resume",
		ArtifactPath:    "/tmp/usage-parent.jsonl",
		FileIdentity:    "parent-latest",
		Generation:      1,
		State:           domain.UsageSourceComplete,
		UpdatedAt:       now,
	})
	mustNoError(t, err)
	const childID = "22222222-2222-4222-8222-222222222222"
	childSource, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: childID,
		SubagentID:      childID,
		ArtifactPath:    "/tmp/usage-child.jsonl",
		FileIdentity:    "child",
		State:           domain.UsageSourceComplete,
		UpdatedAt:       now,
	})
	mustNoError(t, err)

	collector := NewCollector(store, SourceRoots{}, nil)
	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Event:           "session-start",
		NativeSessionID: "native-resume",
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	oldContext, _, _ := store.GetUsageSourceForIngestion(context.Background(), oldSource.ID)
	latestContext, _, _ := store.GetUsageSourceForIngestion(context.Background(), latestSource.ID)
	childContext, _, _ := store.GetUsageSourceForIngestion(context.Background(), childSource.ID)
	if oldContext.Source.State != domain.UsageSourceComplete ||
		latestContext.Source.State != domain.UsageSourceActive ||
		childContext.Source.State != domain.UsageSourceActive {
		t.Fatalf("old/latest/child states=%s/%s/%s", oldContext.Source.State, latestContext.Source.State, childContext.Source.State)
	}
}

func TestCollectorReconcilesPersistedCodexChildrenRecursively(t *testing.T) {
	store := collectorTestStore(t)
	const (
		rootID       = "11111111-1111-4111-8111-111111111111"
		childID      = "22222222-2222-4222-8222-222222222222"
		grandchildID = "33333333-3333-4333-8333-333333333333"
		wrongID      = "44444444-4444-4444-8444-444444444444"
	)
	session := collectorTestSession(t, store, domain.HarnessCodex, rootID, false)
	base := t.TempDir()
	sessionsRoot := filepath.Join(base, "sessions")
	archiveRoot := filepath.Join(base, "archived_sessions")
	rootPath := filepath.Join(sessionsRoot, "2026", "07", "28", "rollout-"+rootID+".jsonl")
	childPath := filepath.Join(sessionsRoot, "2026", "07", "28", "rollout-"+childID+".jsonl")
	wrongPath := filepath.Join(sessionsRoot, "2026", "07", "28", "rollout-"+wrongID+".jsonl")
	grandchildPath := filepath.Join(archiveRoot, "rollout-"+grandchildID+".jsonl")
	writeUsageFixture(t, rootPath, codexSessionMetaFixture(t, rootID, ""))
	writeUsageFixture(t, childPath, codexSessionMetaFixture(t, childID, rootID))
	writeUsageFixture(t, wrongPath, codexSessionMetaFixture(t, wrongID, "not-the-root"))
	writeUsageFixture(t, grandchildPath, codexSessionMetaFixture(t, grandchildID, childID))

	collector := NewCollector(store, SourceRoots{CodexSessions: sessionsRoot, CodexArchived: archiveRoot}, nil)
	mustNoError(t, collector.BackfillActive(context.Background()), "backfill root")
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("root sources=%+v err=%v", sources, err)
	}
	parentState := `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":["` + childID + `","` + childID + `","` + wrongID + `"]}}`
	if err := store.ApplyUsageChunk(context.Background(), sources[0].ID, 0, sources[0].UpdatedAt, domain.SourceCursorState{
		State:           domain.UsageSourceActive,
		ParserStateJSON: parentState,
		UpdatedAt:       time.Now().UTC(),
	}, nil); err != nil {
		t.Fatal(err)
	}

	restarted := NewCollector(store, SourceRoots{CodexSessions: sessionsRoot, CodexArchived: archiveRoot}, nil)
	mustNoError(t, restarted.ReconcileSources(context.Background(), -1), "reconcile persisted child")
	sources, err = store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources after child reconcile=%+v err=%v", sources, err)
	}
	var rootSource, childSource domain.UsageSourceRecord
	for _, source := range sources {
		switch source.NativeSessionID {
		case rootID:
			rootSource = source
		case childID:
			childSource = source
		}
	}
	if rootSource.ID == 0 || childSource.ID == 0 || childSource.SubagentID != childID ||
		rootSource.State == domain.UsageSourceComplete {
		t.Fatalf("parent/child sources=%+v", sources)
	}

	childState := `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":["` + grandchildID + `"]}}`
	if err := store.ApplyUsageChunk(context.Background(), childSource.ID, 0, childSource.UpdatedAt, domain.SourceCursorState{
		State:           domain.UsageSourceActive,
		ParserStateJSON: childState,
		UpdatedAt:       time.Now().UTC(),
	}, nil); err != nil {
		t.Fatal(err)
	}
	mustNoError(t, restarted.ReconcileSources(context.Background(), -1), "reconcile persisted grandchild")
	sources, err = store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 3 {
		t.Fatalf("recursive sources=%+v err=%v", sources, err)
	}
	foundGrandchild := false
	for _, source := range sources {
		if source.NativeSessionID == wrongID {
			t.Fatalf("registered child whose session parent was wrong: %+v", source)
		}
		if source.NativeSessionID == grandchildID {
			foundGrandchild = source.SubagentID == grandchildID && source.ArtifactPath == canonicalUsagePath(t, grandchildPath)
		}
	}
	if !foundGrandchild {
		t.Fatalf("archived grandchild missing from sources=%+v", sources)
	}
}

func TestCollectorIgnoresChildrenFromSupersededCodexGeneration(t *testing.T) {
	store := collectorTestStore(t)
	const (
		rootID  = "11111111-1111-4111-8111-111111111111"
		childID = "22222222-2222-4222-8222-222222222222"
	)
	session := collectorTestSession(t, store, domain.HarnessCodex, rootID, false)
	now := time.Now().UTC()
	binding := seedCollectorUsageBinding(t, store, session, rootID, domain.UsageBindingActive, now, "")
	root := filepath.Join(t.TempDir(), "sessions")
	childPath := filepath.Join(root, "2026", "07", "28", "rollout-"+childID+".jsonl")
	writeUsageFixture(t, childPath, codexSessionMetaFixture(t, childID, rootID))
	oldState := `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":["` + childID + `"]}}`
	emptyState := `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`
	for generation, state := range []string{oldState, emptyState} {
		if _, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
			BindingID:       binding.ID,
			Kind:            domain.UsageSourceCodexRollout,
			NativeSessionID: rootID,
			ArtifactPath:    filepath.Join(root, "rollout-root.jsonl"),
			FileIdentity:    fmt.Sprintf("root-%d", generation),
			Generation:      int64(generation),
			ParserStateJSON: state,
			State:           domain.UsageSourceComplete,
			UpdatedAt:       now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)
	mustNoError(t, collector.registerDiscoveredCodexChildren(context.Background(), binding, now), "register children")
	sources, err := store.ListUsageSourcesForBinding(context.Background(), binding.ID)
	mustNoError(t, err)
	for _, source := range sources {
		if source.NativeSessionID == childID {
			t.Fatalf("registered stale child from superseded generation: %+v", source)
		}
	}
}

func TestCollectorResumeKeepsDiscoveringAfterOnlyArchivedRolloutMatches(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-resume-late", false)
	sessionsRoot := filepath.Join(t.TempDir(), "sessions")
	archiveRoot := filepath.Join(t.TempDir(), "archived_sessions")
	archivedPath := filepath.Join(archiveRoot, "rollout-native-resume-late.jsonl")
	content := codexSessionMetaFixture(t, "native-resume-late", "")
	writeUsageFixture(t, archivedPath, content)
	now := time.Now().UTC()
	binding := seedCollectorUsageBinding(t, store, session, "native-resume-late", domain.UsageBindingComplete, now, "")
	identity, err := SourceIdentity(context.Background(), archivedPath)
	mustNoError(t, err)
	resolvedArchivedPath, err := filepath.EvalSymlinks(archivedPath)
	mustNoError(t, err)
	archivedSource, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: "native-resume-late",
		ArtifactPath:    resolvedArchivedPath,
		FileIdentity:    identity,
		State:           domain.UsageSourceComplete,
		UpdatedAt:       now,
	})
	mustNoError(t, err)
	collector := NewCollector(store, SourceRoots{CodexSessions: sessionsRoot, CodexArchived: archiveRoot}, nil)
	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Event:           "session-start",
		NativeSessionID: "native-resume-late",
	}); err != nil {
		t.Fatalf("resume start: %v", err)
	}
	resumedBinding, _, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, "native-resume-late")
	mustNoError(t, err)
	if resumedBinding.State != domain.UsageBindingActive ||
		resumedBinding.LastErrorCode != domain.UsageErrorSourceDiscoveryPending {
		t.Fatalf("resumed binding=%+v", resumedBinding)
	}

	activePath := filepath.Join(sessionsRoot, "2026", "07", "28", "rollout-native-resume-late.jsonl")
	writeUsageFixture(t, activePath, content)
	mustNoError(t, collector.ReconcileSources(context.Background(), 8), "reconcile active rollout")
	resumedBinding, _, _ = store.GetUsageBinding(context.Background(), session.ID, session.Harness, "native-resume-late")
	sources, err := store.ListUsageSourcesForBinding(context.Background(), binding.ID)
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	oldContext, _, _ := store.GetUsageSourceForIngestion(context.Background(), archivedSource.ID)
	if resumedBinding.LastErrorCode != "" || oldContext.Source.State != domain.UsageSourceComplete {
		t.Fatalf("binding/old source=%+v/%+v", resumedBinding, oldContext.Source)
	}
}

func TestCollectorDoesNotTransferCursorAcrossNativeSessions(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "root-native", false)
	now := time.Unix(1700000000, 0).UTC()
	binding := seedCollectorUsageBinding(t, store, session, "root-native", domain.UsageBindingActive, now, "")
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.jsonl")
	secondPath := filepath.Join(root, "second.jsonl")
	firstContent := codexSessionMetaFixture(t, "native-a", "") + strings.Repeat(" ", 256)
	mustNoError(t, os.WriteFile(firstPath, []byte(firstContent), 0o600))
	if err := os.Link(firstPath, secondPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)
	if _, err := collector.registerSource(
		context.Background(),
		binding,
		domain.UsageSourceCodexRollout,
		"native-a",
		"",
		firstPath,
		now,
		false,
	); err != nil {
		t.Fatal(err)
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), binding.ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	if err := store.ApplyUsageChunk(context.Background(), sources[0].ID, 0, sources[0].UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100,
		State:      domain.UsageSourceActive,
		UpdatedAt:  now,
	}, nil); err != nil {
		t.Fatal(err)
	}
	secondContent := codexSessionMetaFixture(t, "native-b", "") + strings.Repeat(" ", 256)
	mustNoError(t, rewriteCollectorFixture(secondPath, secondContent))
	if _, err := collector.registerSource(
		context.Background(),
		binding,
		domain.UsageSourceCodexRollout,
		"native-b",
		"",
		secondPath,
		now.Add(time.Second),
		false,
	); err != nil {
		t.Fatal(err)
	}
	sources, err = store.ListUsageSourcesForBinding(context.Background(), binding.ID)
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	if sources[1].NativeSessionID != "native-b" || sources[1].ByteOffset != 0 {
		t.Fatalf("new native source inherited an unrelated cursor: %+v", sources[1])
	}
}

func TestCollectorFinalizationReactivatesOnlyLatestCodexGenerationPerNativeSession(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, "native-relocated", false)
	now := time.Now().UTC()
	binding := seedCollectorUsageBinding(t, store, session, "native-relocated", domain.UsageBindingComplete, now, "")
	oldSource, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: "native-relocated",
		ArtifactPath:    "/tmp/codex/sessions/rollout-native-relocated.jsonl",
		FileIdentity:    "same-inode",
		Generation:      0,
		State:           domain.UsageSourceComplete,
		UpdatedAt:       now,
	})
	mustNoError(t, err)
	latestSource, err := store.InsertUsageSource(context.Background(), domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: "native-relocated",
		ArtifactPath:    "/tmp/codex/archived_sessions/rollout-native-relocated.jsonl",
		FileIdentity:    "same-inode",
		Generation:      1,
		State:           domain.UsageSourceComplete,
		UpdatedAt:       now,
	})
	mustNoError(t, err)

	collector := NewCollector(store, SourceRoots{}, nil)
	if err := collector.FinalizeSession(
		context.Background(),
		session.ID,
		session.Metadata.RuntimeLaunchID,
		session.UpdatedAt,
	); err != nil {
		t.Fatalf("finalize relocated rollout: %v", err)
	}
	oldContext, _, _ := store.GetUsageSourceForIngestion(context.Background(), oldSource.ID)
	latestContext, _, _ := store.GetUsageSourceForIngestion(context.Background(), latestSource.ID)
	if oldContext.Source.State != domain.UsageSourceComplete || latestContext.Source.State != domain.UsageSourceActive {
		t.Fatalf("old/latest relocated states = %s/%s", oldContext.Source.State, latestContext.Source.State)
	}
}

func TestCodexSessionMetaMatchesLargeRollout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-large.jsonl")
	content := codexSessionMetaFixture(t, "native-large", "") + strings.Repeat(`{"type":"event_msg"}`+"\n", 5000)
	writeUsageFixture(t, path, content)
	if !codexSessionMetaMatches(path, "native-large", "") {
		t.Fatal("valid first session_meta record was rejected because later rollout content exceeded the read bound")
	}
}

func TestCodexSessionMetaMatchesLargeFirstRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-large-meta.jsonl")
	line, err := json.Marshal(map[string]any{
		"type": "session_meta",
		"payload": map[string]any{
			"id":             "native-large-meta",
			"model_provider": "openai",
			"base_instructions": map[string]any{
				"text": strings.Repeat("x", 96<<10),
			},
		},
	})
	mustNoError(t, err)
	writeUsageFixture(t, path, string(line)+"\n")

	if !codexSessionMetaMatches(path, "native-large-meta", "") {
		t.Fatal("valid session_meta record with a switch-sized system prompt was rejected")
	}
}

func TestDiscoverCodexPathRequiresConfiguredRoots(t *testing.T) {
	const nativeID = "11111111-1111-4111-8111-111111111111"
	t.Chdir(t.TempDir())
	path := filepath.Join("2026", "07", "28", "rollout-"+nativeID+".jsonl")
	writeUsageFixture(t, path, codexSessionMetaFixture(t, nativeID, ""))

	collector := NewCollector(collectorTestStore(t), SourceRoots{}, nil)
	got, err := collector.discoverCodexPath(context.Background(), nativeID, "")
	mustNoError(t, err)
	if got != "" {
		t.Fatalf("unconfigured Codex roots discovered %q", got)
	}
}

func TestDefaultSourceRootsIncludesManagedKimiHome(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".ao", "data")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	got, err := DefaultSourceRoots(context.Background(), dataDir)
	mustNoError(t, err)
	if got.KimiHome != filepath.Join(dataDir, "kimi") {
		t.Fatalf("Kimi home = %q, want %q", got.KimiHome, filepath.Join(dataDir, "kimi"))
	}
}

func TestCollectorDiscoversKimiWireSourcesFromSessionIndex(t *testing.T) {
	const nativeID = "kimi-session-1"
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessKimi, nativeID, false)
	home := t.TempDir()
	sessionDir := filepath.Join(home, "sessions", "wd_agent-orchestrator", nativeID)
	mainPath := filepath.Join(sessionDir, "agents", "main", "wire.jsonl")
	childPath := filepath.Join(sessionDir, "agents", "researcher", "wire.jsonl")
	writeUsageFixture(t, mainPath, "{}\n")
	writeUsageFixture(t, childPath, "{}\n")
	writeUsageFixture(t, filepath.Join(home, "session_index.jsonl"),
		fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q,"workDir":"/repo"}`+"\n", nativeID, sessionDir))
	collector := NewCollector(store, SourceRoots{KimiHome: home}, nil)

	mustNoError(t, collector.RecordHook(context.Background(), session.ID, HookSignal{
		Harness: domain.HarnessKimi, Event: "session-start", NativeSessionID: nativeID,
	}))
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	pathsBySubagent := make(map[string]string, len(sources))
	for _, source := range sources {
		if source.Kind != domain.UsageSourceKimiWire || source.NativeSessionID != nativeID {
			t.Fatalf("source=%+v", source)
		}
		pathsBySubagent[source.SubagentID] = source.ArtifactPath
	}
	if pathsBySubagent[""] != canonicalUsagePath(t, mainPath) ||
		pathsBySubagent["researcher"] != canonicalUsagePath(t, childPath) {
		t.Fatalf("paths by subagent = %+v", pathsBySubagent)
	}
}

// TestCollectorReconcileDiscoversKimiChildCreatedAfterStart catches dropping
// child agents that appear after the initial Kimi source scan.
func TestCollectorReconcileDiscoversKimiChildCreatedAfterStart(t *testing.T) {
	const nativeID = "kimi-session-late-child"
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessKimi, nativeID, false)
	home := t.TempDir()
	sessionDir := filepath.Join(home, "sessions", "wd_agent-orchestrator", nativeID)
	mainPath := filepath.Join(sessionDir, "agents", "main", "wire.jsonl")
	childPath := filepath.Join(sessionDir, "agents", "researcher", "wire.jsonl")
	writeUsageFixture(t, mainPath, "{}\n")
	writeUsageFixture(t, filepath.Join(home, "session_index.jsonl"),
		fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q,"workDir":"/repo"}`+"\n", nativeID, sessionDir))
	collector := NewCollector(store, SourceRoots{KimiHome: home}, nil)

	mustNoError(t, collector.RecordHook(context.Background(), session.ID, HookSignal{
		Harness: domain.HarnessKimi, Event: "session-start", NativeSessionID: nativeID,
	}))
	writeUsageFixture(t, childPath, "{}\n")
	mustNoError(t, collector.ReconcileSources(context.Background(), 8))

	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v, want main and late child", sources, err)
	}
	subagents := map[string]bool{}
	for _, source := range sources {
		subagents[source.SubagentID] = true
	}
	if !subagents[""] || !subagents["researcher"] {
		t.Fatalf("sources=%+v, want main and researcher", sources)
	}
}

func TestDiscoverKimiPathRejectsUntrustedIndexRecords(t *testing.T) {
	const nativeID = "kimi-session-untrusted"
	tests := []struct {
		name  string
		setup func(t *testing.T, home string) string
	}{
		{
			name: "absolute path outside sessions root",
			setup: func(t *testing.T, home string) string {
				t.Helper()
				outside := filepath.Join(t.TempDir(), nativeID)
				writeUsageFixture(t, filepath.Join(outside, "agents", "main", "wire.jsonl"), "{}\n")
				return fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q}`+"\n", nativeID, outside)
			},
		},
		{
			name: "symlink escape",
			setup: func(t *testing.T, home string) string {
				t.Helper()
				outside := filepath.Join(t.TempDir(), nativeID)
				writeUsageFixture(t, filepath.Join(outside, "agents", "main", "wire.jsonl"), "{}\n")
				link := filepath.Join(home, "sessions", nativeID)
				mustNoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
				if err := os.Symlink(outside, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				return fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q}`+"\n", nativeID, link)
			},
		},
		{
			name: "deleted latest record",
			setup: func(t *testing.T, home string) string {
				t.Helper()
				sessionDir := filepath.Join(home, "sessions", nativeID)
				writeUsageFixture(t, filepath.Join(sessionDir, "agents", "main", "wire.jsonl"), "{}\n")
				return fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q}`+"\n"+`{"sessionId":%q,"sessionDir":%q,"deleted":true}`+"\n", nativeID, sessionDir, nativeID, sessionDir)
			},
		},
		{
			name: "malformed index",
			setup: func(t *testing.T, _ string) string {
				t.Helper()
				return `{"sessionId":` + "\n"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			writeUsageFixture(t, filepath.Join(home, "session_index.jsonl"), tt.setup(t, home))
			collector := NewCollector(collectorTestStore(t), SourceRoots{KimiHome: home}, nil)

			path, err := collector.discoverKimiPath(context.Background(), nativeID)
			mustNoError(t, err)
			if path != "" {
				t.Fatalf("untrusted index record discovered %q", path)
			}
		})
	}
}

func TestSourceKindForHarness(t *testing.T) {
	tests := []struct {
		harness domain.AgentHarness
		want    domain.UsageSourceKind
		ok      bool
	}{
		{harness: domain.HarnessClaudeCode, want: domain.UsageSourceClaudeMain, ok: true},
		{harness: domain.HarnessCodex, want: domain.UsageSourceCodexRollout, ok: true},
		{harness: domain.HarnessKimi, want: domain.UsageSourceKimiWire, ok: true},
		{harness: domain.HarnessAider, ok: false},
	}
	for _, tt := range tests {
		got, ok := sourceKindForHarness(tt.harness)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("sourceKindForHarness(%q) = %q, %v; want %q, %v", tt.harness, got, ok, tt.want, tt.ok)
		}
	}
}

func TestDiscoverClaudePathRejectsGlobMetadata(t *testing.T) {
	root := t.TempDir()
	writeUsageFixture(t, filepath.Join(root, "project", "native-session.jsonl"), "{}\n")
	collector := NewCollector(collectorTestStore(t), SourceRoots{ClaudeProjects: root}, nil)

	path, err := collector.discoverPath(context.Background(), domain.HarnessClaudeCode, "*")
	mustNoError(t, err)
	if path != "" {
		t.Fatalf("invalid Claude native ID discovered %q", path)
	}
}

func TestReconcileCodexRootAcceptsScalarSourceMetadata(t *testing.T) {
	const nativeID = "11111111-1111-4111-8111-111111111111"
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessCodex, nativeID, false)
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "08", "02", "rollout-"+nativeID+".jsonl")
	writeUsageFixture(t, path, `{"type":"session_meta","payload":{"id":"`+nativeID+`","source":"cli"}}`+"\n")
	now := time.Now().UTC()
	binding := seedCollectorUsageBinding(
		t, store, session, nativeID, domain.UsageBindingActive, now, domain.UsageErrorSourceDiscoveryPending,
	)

	collector := NewCollector(store, SourceRoots{CodexSessions: root}, nil)
	mustNoError(t, collector.ReconcileSources(context.Background(), 8), "reconcile scalar-source rollout")

	got, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, nativeID)
	if err != nil || !ok {
		t.Fatalf("get reconciled binding: ok=%v err=%v", ok, err)
	}
	if got.LastErrorCode != "" {
		t.Fatalf("binding error = %q, want cleared", got.LastErrorCode)
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), binding.ID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources = %+v, err=%v; want one registered rollout", sources, err)
	}
}

func TestSourceIdentityChangesWhenFileIsReplacedWithSameFirstRecord(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	previous := filepath.Join(root, "previous.jsonl")
	content := []byte(`{"type":"session_meta","payload":{"id":"same"}}` + "\n")
	mustNoError(t, os.WriteFile(path, content, 0o600))
	first, err := SourceIdentity(context.Background(), path)
	mustNoError(t, err)
	mustNoError(t, os.Rename(path, previous))
	mustNoError(t, os.WriteFile(path, content, 0o600))
	second, err := SourceIdentity(context.Background(), path)
	mustNoError(t, err)
	if first == second {
		t.Fatalf("replacement identity = %q, want a new file generation", second)
	}
}

func TestSourceIdentityDoesNotChangeAsFirstRecordIsWritten(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	mustNoError(t, os.WriteFile(path, nil, 0o600))
	emptyIdentity, err := SourceIdentity(context.Background(), path)
	mustNoError(t, err)
	mustNoError(t, os.WriteFile(path, []byte(`{"type":"session_meta"}`+"\n"), 0o600))
	writtenIdentity, err := SourceIdentity(context.Background(), path)
	mustNoError(t, err)
	if emptyIdentity != writtenIdentity {
		t.Fatalf("identity changed while first record was written: %q != %q", emptyIdentity, writtenIdentity)
	}
}

func collectorTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlitetest.Open(t.TempDir())
	mustNoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpsertProject(context.Background(), domain.ProjectRecord{
		ID:           "usage-test",
		Path:         t.TempDir(),
		RegisteredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func seedCollectorUsageBinding(
	t *testing.T,
	store *sqlite.Store,
	session domain.SessionRecord,
	nativeRootID string,
	state domain.UsageBindingState,
	at time.Time,
	lastErrorCode string,
) domain.UsageBindingRecord {
	t.Helper()
	binding, err := store.UpsertUsageBinding(context.Background(), domain.UsageBindingRecord{
		SessionID:     session.ID,
		Harness:       session.Harness,
		NativeRootID:  nativeRootID,
		State:         state,
		LastErrorCode: lastErrorCode,
		UpdatedAt:     at,
	})
	mustNoError(t, err)
	return binding
}

func assertNoUsageSourcesForSession(t *testing.T, store *sqlite.Store, sessionID domain.SessionID) {
	t.Helper()
	bindings, err := store.ListUsageBindingsForSession(context.Background(), sessionID)
	mustNoError(t, err)
	for _, binding := range bindings {
		sources, err := store.ListUsageSourcesForBinding(context.Background(), binding.ID)
		mustNoError(t, err)
		if len(sources) != 0 {
			t.Fatalf("unexpected usage sources: %+v", sources)
		}
	}
}

func collectorTestSession(t *testing.T, store *sqlite.Store, harness domain.AgentHarness, nativeID string, terminated bool) domain.SessionRecord {
	return collectorTestSessionWithActivity(t, store, harness, nativeID, terminated, domain.ActivityIdle)
}

func collectorTestChatSession(
	t *testing.T,
	store *sqlite.Store,
	harness domain.AgentHarness,
	providerConversationID string,
	terminated bool,
) domain.SessionRecord {
	t.Helper()
	now := time.Now().UTC()
	session, err := store.CreateSession(context.Background(), domain.SessionRecord{
		ProjectID:    "usage-test",
		Kind:         domain.KindWorker,
		Harness:      harness,
		Mode:         domain.SessionModeChat,
		Activity:     domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		IsTerminated: terminated,
		Metadata: domain.SessionMetadata{
			ProviderConversationID: providerConversationID,
		},
		CreatedAt: now,
		UpdatedAt: now,
	})
	mustNoError(t, err)
	return session
}

func collectorTestSessionWithActivity(
	t *testing.T,
	store *sqlite.Store,
	harness domain.AgentHarness,
	nativeID string,
	terminated bool,
	activity domain.ActivityState,
) domain.SessionRecord {
	t.Helper()
	now := time.Now().UTC()
	session, err := store.CreateSession(context.Background(), domain.SessionRecord{
		ProjectID:    "usage-test",
		Kind:         domain.KindWorker,
		Harness:      harness,
		Activity:     domain.Activity{State: activity, LastActivityAt: now},
		IsTerminated: terminated,
		Metadata: domain.SessionMetadata{
			AgentSessionID: nativeID,
		},
		CreatedAt: now,
		UpdatedAt: now,
	})
	mustNoError(t, err)
	return session
}

func writeUsageFixture(t *testing.T, path, content string) {
	t.Helper()
	mustNoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	mustNoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func rewriteCollectorFixture(path, content string) error {
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

func codexSessionMetaFixture(t *testing.T, id, parentID string) string {
	t.Helper()
	payload := map[string]any{"id": id, "model_provider": "openai"}
	if parentID != "" {
		payload["source"] = map[string]any{
			"subagent": map[string]any{
				"thread_spawn": map[string]any{"parent_thread_id": parentID},
			},
		}
	}
	line, err := json.Marshal(map[string]any{"type": "session_meta", "payload": payload})
	mustNoError(t, err)
	return string(line) + "\n"
}

func canonicalUsagePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	mustNoError(t, err)
	return resolved
}

func mustNoError(t testing.TB, err error, context ...string) {
	t.Helper()
	if err != nil {
		if len(context) > 0 {
			t.Fatalf("%s: %v", context[0], err)
		}
		t.Fatal(err)
	}
}
