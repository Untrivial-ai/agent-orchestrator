package usage

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestDefaultSourceRootsReturnsNoProviderRoots(t *testing.T) {
	roots, err := DefaultSourceRoots(context.Background(), t.TempDir())
	mustNoError(t, err)
	if roots != (SourceRoots{}) {
		t.Fatalf("DefaultSourceRoots = %+v, want no provider roots", roots)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DefaultSourceRoots(cancelled, t.TempDir()); err == nil {
		t.Fatal("cancelled context was accepted")
	}
}

func TestCollectorHookLifecycleTransitions(t *testing.T) {
	tests := []struct {
		name, event, hookLaunch string
		activity                domain.ActivityState
		binding                 domain.UsageBindingState
		wantBinding             domain.UsageBindingState
	}{
		{"current activity reactivates", "post-tool-use", "launch-current", domain.ActivityIdle, domain.UsageBindingFinalizing, domain.UsageBindingActive},
		{"terminal event finalizes", "process-exited", "launch-current", domain.ActivityIdle, domain.UsageBindingActive, domain.UsageBindingFinalizing},
		{"exited session ignores activity", "post-tool-use", "launch-current", domain.ActivityExited, domain.UsageBindingFinalizing, domain.UsageBindingFinalizing},
		{"stale launch ignores activity", "post-tool-use", "launch-stale", domain.ActivityIdle, domain.UsageBindingFinalizing, domain.UsageBindingFinalizing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := collectorTestStore(t)
			nativeID := "native-lifecycle"
			session := collectorTestSessionWithActivity(t, store, domain.HarnessOpenCode, nativeID, false, test.activity)
			session.Metadata.RuntimeLaunchID = "launch-current"
			mustNoError(t, store.UpdateSession(context.Background(), session))
			seedCollectorUsageBinding(t, store, session, nativeID, test.binding, time.Now().UTC(), "")

			collector := NewCollector(store, SourceRoots{}, nil)
			mustNoError(t, collector.RecordHook(context.Background(), session.ID, HookSignal{
				Event: test.event, LaunchID: test.hookLaunch, NativeSessionID: nativeID,
			}))
			got, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, nativeID)
			if err != nil || !ok || got.State != test.wantBinding {
				t.Fatalf("binding = %+v, ok=%v err=%v; want %s", got, ok, err, test.wantBinding)
			}
			// opencode has no certified transcript pipeline: hooks maintain the
			// binding lifecycle and must never register usage sources.
			assertNoUsageSourcesForSession(t, store, session.ID)
		})
	}
}

func TestCollectorIgnoresIneligibleHooks(t *testing.T) {
	tests := []struct {
		name, event string
		harness     domain.AgentHarness
		activity    domain.ActivityState
		terminated  bool
	}{
		{"terminated session", "process-exited", domain.HarnessOpenCode, domain.ActivityIdle, true},
		{"unsupported harness", "post-tool-use", domain.AgentHarness("aider"), domain.ActivityIdle, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := collectorTestStore(t)
			session := collectorTestSessionWithActivity(t, store, test.harness, "native-ignored", test.terminated, test.activity)
			session.Metadata.RuntimeLaunchID = "launch-current"
			mustNoError(t, store.UpdateSession(context.Background(), session))
			signal := HookSignal{Event: test.event, LaunchID: "launch-current", NativeSessionID: "native-ignored"}
			mustNoError(t, NewCollector(store, SourceRoots{}, nil).RecordHook(context.Background(), session.ID, signal))
			bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
			if err != nil || len(bindings) != 0 {
				t.Fatalf("bindings = %+v, err=%v; want none", bindings, err)
			}
		})
	}
}

// opencode has no transcript sources, so a terminal hook on an already-exited
// session still records the billable route: the binding is created and driven
// straight to finalizing.
func TestCollectorTerminalExitRecordsFinalizingBinding(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSessionWithActivity(
		t, store, domain.HarnessOpenCode, "native-exit", false, domain.ActivityExited,
	)
	session.Metadata.RuntimeLaunchID = "launch-current"
	mustNoError(t, store.UpdateSession(context.Background(), session))

	mustNoError(t, NewCollector(store, SourceRoots{}, nil).RecordHook(context.Background(), session.ID, HookSignal{
		Event:           "process-exited",
		LaunchID:        "launch-current",
		NativeSessionID: "native-exit",
	}))
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != domain.UsageBindingFinalizing {
		t.Fatalf("terminal exit bindings=%+v err=%v, want one finalizing", bindings, err)
	}
	assertNoUsageSourcesForSession(t, store, session.ID)
}

func TestCollectorSerializesFinalizationAgainstEarlierHook(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessOpenCode, "native-serialized", false)

	collector := NewCollector(store, SourceRoots{}, nil)
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
		})
	}()
	<-entered

	finalDone := make(chan error, 1)
	go func() {
		finalDone <- collector.RecordHook(context.Background(), session.ID, HookSignal{
			Event:           "process-exited",
			NativeSessionID: "native-serialized",
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
	expectedSessionRevision int64,
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
	expectedSessionRevision int64,
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
	expectedSessionRevision int64,
) error {
	close(f.entered)
	<-f.release
	return f.collector.FinalizeSession(ctx, sessionID, expectedLaunchID, expectedSessionRevision)
}

func TestCollectorFinalizationSkipsRelaunchCommittedBeforeStorageFence(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessOpenCode, "native-relaunched", false)
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
	current, found, readErr := store.GetSession(context.Background(), session.ID)
	if readErr != nil || !found {
		t.Fatalf("reload session before finalization: %v %v", found, readErr)
	}
	session = current

	done := make(chan error, 1)
	go func() {
		done <- collector.FinalizeSession(context.Background(), session.ID, "launch-old", session.Revision)
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
	session := collectorTestSession(t, store, domain.HarnessOpenCode, "native-before-fence", false)
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
	session := collectorTestSession(t, store, domain.HarnessOpenCode, "native-reactivated", false)
	session.Metadata.RuntimeLaunchID = "launch-old"
	mustNoError(t, store.UpdateSession(context.Background(), session))
	collector := NewCollector(store, SourceRoots{}, nil)

	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Event:           "session-start",
		LaunchID:        "launch-old",
		NativeSessionID: "native-reactivated",
	}); err != nil {
		t.Fatal(err)
	}
	current, found, readErr := store.GetSession(context.Background(), session.ID)
	if readErr != nil || !found {
		t.Fatalf("reload session before finalization: %v %v", found, readErr)
	}
	session = current
	mustNoError(t, collector.FinalizeSession(context.Background(), session.ID, "launch-old", session.Revision))
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
	}); err != nil {
		t.Fatal(err)
	}
	bindings, err = store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != domain.UsageBindingActive {
		t.Fatalf("reactivated bindings=%+v err=%v", bindings, err)
	}
}

func TestCollectorReactivateSessionReactivatesBindingWithoutHook(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessOpenCode, "native-restored", false)
	session.Metadata.RuntimeLaunchID = "launch-new"
	mustNoError(t, store.UpdateSession(context.Background(), session))
	now := time.Unix(1700000000, 0).UTC()
	seedCollectorUsageBinding(t, store, session, "native-restored", domain.UsageBindingComplete, now, "")
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
	if wakes != 1 {
		t.Fatalf("reactivation notifications = %d, want 1", wakes)
	}
	assertNoUsageSourcesForSession(t, store, session.ID)
}

func TestCollectorRegistersChatUsageWhenLifecycleMarksControllerSpawned(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestChatSession(t, store, domain.HarnessOpenCode, "", false)

	collector := NewCollector(store, SourceRoots{}, nil)
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
}

func TestCollectorReactivateSessionRejectsStaleLaunch(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessOpenCode, "native-stale", false)
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

func TestCollectorCurrentActivityReactivatesBindingDuringReaperFinalization(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessOpenCode, "native-race", false)
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
		Harness:   domain.HarnessOpenCode,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		Metadata: domain.SessionMetadata{
			AgentSessionID:  "native-fenced",
			RuntimeLaunchID: "launch-current",
		},
		CreatedAt: now,
		UpdatedAt: now,
	})
	mustNoError(t, err)
	collector := NewCollector(store, SourceRoots{}, nil)

	if err := collector.RecordHook(context.Background(), session.ID, HookSignal{
		Harness:         domain.HarnessOpenCode,
		Event:           "session-start",
		LaunchID:        "launch-current",
		NativeSessionID: "native-fenced",
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

func TestCollectorBackfillsOnlyNonTerminatedSupportedSessions(t *testing.T) {
	store := collectorTestStore(t)
	active := collectorTestSession(t, store, domain.HarnessOpenCode, "active-native", false)
	terminated := collectorTestSession(t, store, domain.HarnessOpenCode, "terminated-native", true)
	unsupported := collectorTestSession(t, store, domain.AgentHarness("aider"), "unsupported-native", false)

	collector := NewCollector(store, SourceRoots{}, nil)
	mustNoError(t, collector.BackfillActive(context.Background()), "backfill")
	for _, test := range []struct {
		name    string
		session domain.SessionRecord
		want    int
	}{
		{"active", active, 1},
		{"terminated", terminated, 0},
		{"unsupported harness", unsupported, 0},
	} {
		bindings, err := store.ListUsageBindingsForSession(context.Background(), test.session.ID)
		if err != nil || len(bindings) != test.want {
			t.Fatalf("%s bindings=%+v err=%v, want %d", test.name, bindings, err, test.want)
		}
	}
}

func TestCollectorBackfillsChatSessionFromProviderConversationID(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestChatSession(t, store, domain.HarnessOpenCode, "native-chat-backfill", false)
	collector := NewCollector(store, SourceRoots{}, nil)

	mustNoError(t, collector.BackfillActive(context.Background()), "backfill chat usage")
	binding, ok, err := store.GetUsageBinding(
		context.Background(), session.ID, session.Harness, "native-chat-backfill",
	)
	if err != nil || !ok || binding.State != domain.UsageBindingActive {
		t.Fatalf("chat backfill binding=%+v ok=%v err=%v", binding, ok, err)
	}
}

func TestCollectorBackfillPreservesCompletedExitedBinding(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSessionWithActivity(t, store, domain.HarnessOpenCode, "native-complete", false, domain.ActivityExited)
	seedCollectorUsageBinding(t, store, session, "native-complete", domain.UsageBindingComplete, time.Now().UTC(), "")

	mustNoError(t, NewCollector(store, SourceRoots{}, nil).BackfillActive(context.Background()), "backfill")
	gotBinding, _, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, "native-complete")
	mustNoError(t, err)
	if gotBinding.State != domain.UsageBindingComplete {
		t.Fatalf("backfill reopened completed exited binding: %s", gotBinding.State)
	}
}

func TestCollectorBackfillReactivatesLiveBinding(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessOpenCode, "native-live", false)
	seedCollectorUsageBinding(t, store, session, "native-live", domain.UsageBindingComplete, time.Now().UTC(), "")

	mustNoError(t, NewCollector(store, SourceRoots{}, nil).BackfillActive(context.Background()), "backfill")
	gotBinding, _, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, "native-live")
	mustNoError(t, err)
	if gotBinding.State != domain.UsageBindingActive {
		t.Fatalf("backfill did not reactivate live binding: %s", gotBinding.State)
	}
}

func TestCollectorReconcileSourcesResolvesLegacyDiscoveringBinding(t *testing.T) {
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessOpenCode, "native-discovering", false)
	seedCollectorUsageBinding(
		t, store, session, "native-discovering", domain.UsageBindingDiscovering, time.Now().UTC(),
		domain.UsageErrorSourceDiscoveryPending,
	)

	collector := NewCollector(store, SourceRoots{}, nil)
	mustNoError(t, collector.ReconcileSources(context.Background(), 8), "reconcile")
	got, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, "native-discovering")
	if err != nil || !ok || got.State != domain.UsageBindingActive || got.LastErrorCode != "" {
		t.Fatalf("reconciled binding=%+v ok=%v err=%v, want active with no error", got, ok, err)
	}
}

func TestCollectorReconcileBindingFinalizesNonLiveSessions(t *testing.T) {
	tests := []struct {
		name   string
		create func(t *testing.T, store *sqlite.Store) domain.SessionRecord
	}{
		{
			name: "exited activity",
			create: func(t *testing.T, store *sqlite.Store) domain.SessionRecord {
				return collectorTestSessionWithActivity(
					t, store, domain.HarnessOpenCode, "native-finalize", false, domain.ActivityExited,
				)
			},
		},
		{
			name: "terminated session",
			create: func(t *testing.T, store *sqlite.Store) domain.SessionRecord {
				return collectorTestSession(t, store, domain.HarnessOpenCode, "native-finalize", true)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := collectorTestStore(t)
			session := test.create(t, store)
			now := time.Now().UTC()
			binding := seedCollectorUsageBinding(
				t, store, session, "native-finalize", domain.UsageBindingActive, now, "",
			)

			collector := NewCollector(store, SourceRoots{}, nil)
			mustNoError(t, collector.reconcileBinding(context.Background(), binding, now))
			got, ok, err := store.GetUsageBinding(context.Background(), session.ID, session.Harness, "native-finalize")
			if err != nil || !ok || got.State != domain.UsageBindingFinalizing {
				// No transcript sources exist for opencode, so a settled
				// binding intentionally remains finalizing.
				t.Fatalf("reconciled binding=%+v ok=%v err=%v, want finalizing", got, ok, err)
			}
		})
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

func TestSourceIdentityHonorsCancelledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	mustNoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SourceIdentity(cancelled, path); err == nil {
		t.Fatal("cancelled context was accepted")
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

func mustNoError(t testing.TB, err error, context ...string) {
	t.Helper()
	if err != nil {
		if len(context) > 0 {
			t.Fatalf("%s: %v", context[0], err)
		}
		t.Fatal(err)
	}
}
