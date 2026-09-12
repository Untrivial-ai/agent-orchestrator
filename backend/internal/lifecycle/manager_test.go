package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/ownership"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var ctx = context.Background()

type fakeStore struct {
	sessions   map[domain.SessionID]domain.SessionRecord
	projects   map[string]domain.ProjectRecord
	prs        map[domain.SessionID][]domain.PullRequest
	reviews    map[string][]domain.PullRequestReview
	comments   map[string][]domain.PullRequestComment
	prPolicies map[string]bool
	signatures map[string]string

	listPRsErr        error
	listReviewsErr    error
	signatureWriteErr error
	signatureWrites   int
	chatSpawnErr      error
	chatSpawnCalls    []domain.ConversationBranch
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sessions:   map[domain.SessionID]domain.SessionRecord{},
		projects:   map[string]domain.ProjectRecord{},
		prs:        map[domain.SessionID][]domain.PullRequest{},
		reviews:    map[string][]domain.PullRequestReview{},
		comments:   map[string][]domain.PullRequestComment{},
		prPolicies: map[string]bool{},
		signatures: map[string]string{},
	}
}

func (f *fakeStore) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	r, ok := f.sessions[id]
	return r, ok, nil
}

func (f *fakeStore) ListPRsBySession(_ context.Context, id domain.SessionID) ([]domain.PullRequest, error) {
	if f.listPRsErr != nil {
		return nil, f.listPRsErr
	}
	return f.prs[id], nil
}

func (f *fakeStore) GetPR(_ context.Context, prURL string) (domain.PullRequest, bool, error) {
	for _, prs := range f.prs {
		for _, pr := range prs {
			if pr.URL != prURL {
				continue
			}
			if policy, ok := f.prPolicies[prURL]; ok {
				pr.AutoInjectCI = policy
			} else {
				pr.AutoInjectCI = true
			}
			return pr, true, nil
		}
	}
	policy, explicitlySet := f.prPolicies[prURL]
	if !explicitlySet {
		policy = true
	}
	return domain.PullRequest{URL: prURL, AutoInjectCI: policy}, true, nil
}

func (f *fakeStore) ListPRReviews(_ context.Context, prURL string) ([]domain.PullRequestReview, error) {
	if f.listReviewsErr != nil {
		return nil, f.listReviewsErr
	}
	return append([]domain.PullRequestReview(nil), f.reviews[prURL]...), nil
}

func (f *fakeStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	rec, ok := f.projects[id]
	return rec, ok, nil
}

func (f *fakeStore) ListPRComments(_ context.Context, prURL string) ([]domain.PullRequestComment, error) {
	return append([]domain.PullRequestComment(nil), f.comments[prURL]...), nil
}

func (f *fakeStore) ListSessions(_ context.Context, project domain.ProjectID) ([]domain.SessionRecord, error) {
	var out []domain.SessionRecord
	for _, rec := range f.sessions {
		if rec.ProjectID == project {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateSession(_ context.Context, rec domain.SessionRecord) error {
	f.sessions[rec.ID] = rec
	return nil
}

func (f *fakeStore) UpdateSessionFromActivitySignal(_ context.Context, rec domain.SessionRecord) (bool, error) {
	f.sessions[rec.ID] = rec
	return true, nil
}

func (f *fakeStore) CommitChatSpawn(
	_ context.Context,
	rec domain.SessionRecord,
	boundary domain.ConversationBranch,
) error {
	if f.chatSpawnErr != nil {
		return f.chatSpawnErr
	}
	f.chatSpawnCalls = append(f.chatSpawnCalls, boundary)
	f.sessions[rec.ID] = rec
	return nil
}

func (f *fakeStore) CommitSessionControllerEpoch(
	_ context.Context,
	id domain.SessionID,
	source, target domain.SessionMode,
	nativeConversationID string,
	now time.Time,
) (bool, error) {
	rec, ok := f.sessions[id]
	if !ok || rec.IsTerminated || domain.NormalizeSessionMode(rec.Mode) != source {
		return false, nil
	}
	rec.Mode = target
	rec.Metadata.RuntimeHandleID = ""
	rec.Metadata.RuntimeLaunchID = ""
	rec.Metadata.AgentSessionID = nativeConversationID
	rec.Metadata.AgentSessionIDLaunchID = ""
	rec.Metadata.ProviderConversationID = nativeConversationID
	rec.Metadata.ControllerGeneration = ""
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
	rec.UpdatedAt = now
	f.sessions[id] = rec
	return true, nil
}

func (f *fakeStore) GetPRLastNudgeSignature(_ context.Context, prURL string) (string, error) {
	return f.signatures[prURL], nil
}

func (f *fakeStore) UpdatePRLastNudgeSignature(_ context.Context, prURL, payload string) error {
	if f.signatureWriteErr != nil {
		return f.signatureWriteErr
	}
	if f.signatures == nil {
		f.signatures = map[string]string{}
	}
	f.signatures[prURL] = payload
	f.signatureWrites++
	return nil
}

type targetAcknowledgementCall struct {
	switchID   domain.AgentSwitchID
	sessionID  domain.SessionID
	generation domain.AgentGenerationID
	at         time.Time
}

// fakeAgentSwitchLifecycleStore embeds the full port only to inherit methods
// these focused lifecycle tests never call. The methods lifecycle does consume
// are implemented below, with one mutex covering both session ownership and
// switch state so ActivateAgentSwitchTarget models the production transaction.
type fakeAgentSwitchLifecycleStore struct {
	*fakeStore
	ports.AgentSwitchStore

	mu                   sync.Mutex
	activeSwitch         domain.AgentSwitch
	hasActiveSwitch      bool
	native               map[domain.AgentNativeSessionID]domain.AgentNativeSession
	acknowledgementCalls []targetAcknowledgementCall
	ackForceNoChange     bool
	ackErr               error
	getSwitchErr         error
}

func (f *fakeAgentSwitchLifecycleStore) GetAgentSwitch(_ context.Context, id domain.AgentSwitchID) (domain.AgentSwitch, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getSwitchErr != nil {
		return domain.AgentSwitch{}, false, f.getSwitchErr
	}
	if !f.hasActiveSwitch || f.activeSwitch.ID != id {
		return domain.AgentSwitch{}, false, nil
	}
	return f.activeSwitch, true, nil
}

func newFakeAgentSwitchLifecycleStore() *fakeAgentSwitchLifecycleStore {
	return &fakeAgentSwitchLifecycleStore{fakeStore: newFakeStore(), native: map[domain.AgentNativeSessionID]domain.AgentNativeSession{}}
}

func (f *fakeAgentSwitchLifecycleStore) GetAgentNativeSession(_ context.Context, id domain.AgentNativeSessionID) (domain.AgentNativeSession, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.native[id]
	return rec, ok, nil
}

func (f *fakeAgentSwitchLifecycleStore) UpdateAgentNativeSession(_ context.Context, rec domain.AgentNativeSession, expected domain.AgentGenerationID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.native[rec.ID]
	if !ok || current.LastGenerationID != expected {
		return false, nil
	}
	f.native[rec.ID] = rec
	return true, nil
}

func (f *fakeAgentSwitchLifecycleStore) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.sessions[id]
	return rec, ok, nil
}

func (f *fakeAgentSwitchLifecycleStore) UpdateSession(_ context.Context, rec domain.SessionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[rec.ID] = rec
	return nil
}

func (f *fakeAgentSwitchLifecycleStore) UpdateSessionFromActivitySignal(_ context.Context, rec domain.SessionRecord) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.sessions[rec.ID]
	if !ok || current.IsTerminated || current.Harness != rec.Harness ||
		current.Metadata.RuntimeLaunchID != rec.Metadata.RuntimeLaunchID {
		return false, nil
	}
	if f.hasActiveSwitch && f.activeSwitch.SessionID == rec.ID &&
		f.activeSwitch.FromHarness == current.Harness &&
		(f.activeSwitch.State == domain.AgentSwitchStoppingSource ||
			f.activeSwitch.State == domain.AgentSwitchSourceStopped ||
			f.activeSwitch.State == domain.AgentSwitchStartingTarget) {
		return false, nil
	}
	current.Activity = rec.Activity
	current.FirstSignalAt = rec.FirstSignalAt
	current.Metadata.AgentSessionID = rec.Metadata.AgentSessionID
	current.Metadata.AgentSessionIDLaunchID = rec.Metadata.AgentSessionIDLaunchID
	current.Metadata.LatestUserPrompt = rec.Metadata.LatestUserPrompt
	current.Metadata.LatestUserPromptAt = rec.Metadata.LatestUserPromptAt
	current.Metadata.LatestAssistantUpdate = rec.Metadata.LatestAssistantUpdate
	current.Metadata.NativeTranscriptPath = rec.Metadata.NativeTranscriptPath
	current.UpdatedAt = rec.UpdatedAt
	f.sessions[rec.ID] = current
	return true, nil
}

func (f *fakeAgentSwitchLifecycleStore) GetActiveAgentSwitch(_ context.Context, id domain.SessionID) (domain.AgentSwitch, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasActiveSwitch || f.activeSwitch.SessionID != id || f.activeSwitch.State.Terminal() {
		return domain.AgentSwitch{}, false, nil
	}
	return f.activeSwitch, true, nil
}

func (f *fakeAgentSwitchLifecycleStore) AcknowledgeAgentSwitchTarget(_ context.Context, switchID domain.AgentSwitchID, sessionID domain.SessionID, generation domain.AgentGenerationID, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acknowledgementCalls = append(f.acknowledgementCalls, targetAcknowledgementCall{
		switchID: switchID, sessionID: sessionID, generation: generation, at: at,
	})
	if f.ackErr != nil {
		return false, f.ackErr
	}
	if f.ackForceNoChange {
		return false, nil
	}
	if !f.hasActiveSwitch || f.activeSwitch.ID != switchID || f.activeSwitch.SessionID != sessionID ||
		f.activeSwitch.State != domain.AgentSwitchDelivering || f.activeSwitch.TargetGenerationID != generation ||
		f.activeSwitch.TargetAcknowledgedAt != nil {
		return false, nil
	}
	acknowledgedAt := at
	f.activeSwitch.TargetAcknowledgedAt = &acknowledgedAt
	f.activeSwitch.UpdatedAt = at
	return true, nil
}

func (f *fakeAgentSwitchLifecycleStore) ActivateAgentSwitchTarget(_ context.Context, activation domain.AgentSwitchTargetActivation) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasActiveSwitch || f.activeSwitch.ID != activation.SwitchID || f.activeSwitch.SessionID != activation.SessionID ||
		f.activeSwitch.State != domain.AgentSwitchStartingTarget || f.activeSwitch.FromHarness != activation.SourceHarness ||
		f.activeSwitch.TargetHarness != activation.TargetHarness || f.activeSwitch.SourceGenerationID != activation.SourceGenerationID ||
		f.activeSwitch.TargetGenerationID != activation.TargetGenerationID {
		return false, nil
	}
	rec, ok := f.sessions[activation.SessionID]
	if !ok || rec.IsTerminated || rec.Activity.State != domain.ActivityExited || rec.Harness != activation.SourceHarness ||
		rec.Metadata.RuntimeLaunchID != activation.ExpectedSourceRuntimeLaunchID {
		return false, nil
	}
	rec.Harness = activation.TargetHarness
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: activation.ActivatedAt}
	rec.FirstSignalAt = time.Time{}
	rec.Metadata.RuntimeHandleID = activation.RuntimeHandleID
	rec.Metadata.RuntimeLaunchID = string(activation.TargetGenerationID)
	rec.Metadata.AgentSessionIDLaunchID = string(activation.TargetGenerationID)
	rec.UpdatedAt = activation.ActivatedAt
	f.sessions[activation.SessionID] = rec
	f.activeSwitch.State = domain.AgentSwitchTargetReady
	f.activeSwitch.UpdatedAt = activation.ActivatedAt
	return true, nil
}

func (f *fakeAgentSwitchLifecycleStore) setActiveSwitch(sw domain.AgentSwitch) {
	f.mu.Lock()
	f.activeSwitch = sw
	f.hasActiveSwitch = true
	f.mu.Unlock()
}

func (f *fakeAgentSwitchLifecycleStore) setSession(rec domain.SessionRecord) {
	f.mu.Lock()
	f.sessions[rec.ID] = rec
	f.mu.Unlock()
}

func (f *fakeAgentSwitchLifecycleStore) session(id domain.SessionID) domain.SessionRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[id]
}

func (f *fakeAgentSwitchLifecycleStore) acknowledgements() []targetAcknowledgementCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]targetAcknowledgementCall(nil), f.acknowledgementCalls...)
}

func (f *fakeAgentSwitchLifecycleStore) active() domain.AgentSwitch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeSwitch
}

type fakeMessenger struct {
	msgs []string
	ids  []domain.SessionID
	err  error
}

type fakeCompletionTerminator struct {
	calls int
	err   error
}

func (f *fakeCompletionTerminator) Kill(_ context.Context, _ domain.SessionID) (bool, error) {
	f.calls++
	return true, f.err
}

type fixedSessionOperationGate bool

func (g fixedSessionOperationGate) SessionMutationInProgress(domain.SessionID) bool {
	return bool(g)
}

type fixedLifecycleInputLease bool

func (l fixedLifecycleInputLease) AcquireSessionInput(domain.SessionID) (func(), bool) {
	if !l {
		return nil, false
	}
	return func() {}, true
}

type telemetrySink struct {
	events []ports.TelemetryEvent
}

func (s *telemetrySink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	s.events = append(s.events, ev)
}

func (*telemetrySink) Close(context.Context) error { return nil }

func (f *fakeMessenger) Send(_ context.Context, id domain.SessionID, msg string) error {
	if f.err != nil {
		return f.err
	}
	f.msgs = append(f.msgs, msg)
	f.ids = append(f.ids, id)
	return nil
}

func newManager(opts ...Option) (*Manager, *fakeStore, *fakeMessenger) {
	st := newFakeStore()
	msg := &fakeMessenger{}
	return New(st, msg, opts...), st, msg
}

func working(id domain.SessionID) domain.SessionRecord {
	return domain.SessionRecord{
		ID: id, ProjectID: "mer",
		Activity:         domain.Activity{State: domain.ActivityActive, LastActivityAt: time.Now()},
		AutoInjectReview: true,
		FirstSignalAt:    time.Now(),
	}
}

func TestRuntimeObservation_ConfirmedRuntimeDeathTerminates(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
	st.sessions["mer-1"] = rec
	if err := m.ApplyRuntimeObservation(ctx, "mer-1", ports.RuntimeFacts{Runtime: ports.ProbeDead, Workload: ports.ProbeFailed}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if !got.IsTerminated || got.Activity.State != domain.ActivityExited {
		t.Fatalf("want terminated/exited, got %+v", got)
	}
}

func TestRuntimeObservation_CrashFinalizesUsageBeforeTermination(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
	rec.Metadata.RuntimeLaunchID = "launch-1"
	rec.UpdatedAt = time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)
	st.sessions[rec.ID] = rec
	finalizer := &fakeUsageFinalizer{store: st}
	m.SetUsageFinalizer(finalizer)

	if err := m.ApplyRuntimeObservation(ctx, rec.ID, ports.RuntimeFacts{
		Runtime:  ports.ProbeDead,
		Workload: ports.ProbeFailed,
		LaunchID: "launch-1",
	}); err != nil {
		t.Fatal(err)
	}
	if finalizer.calls != 1 || finalizer.sawTerminated {
		t.Fatalf("finalizer calls=%d sawTerminated=%v, want 1/false", finalizer.calls, finalizer.sawTerminated)
	}
	if finalizer.launchID != "launch-1" {
		t.Fatalf("finalizer launch id=%q, want launch-1", finalizer.launchID)
	}
	if !finalizer.sessionRevision.Equal(rec.UpdatedAt) {
		t.Fatalf("finalizer session revision=%s, want %s", finalizer.sessionRevision, rec.UpdatedAt)
	}
	if !st.sessions[rec.ID].IsTerminated {
		t.Fatal("crashed session was not terminated")
	}
}

func TestRuntimeObservation_FinalizerErrorIsLoggedAndDoesNotBlockTermination(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
	st.sessions[rec.ID] = rec
	finalizer := &fakeUsageFinalizer{store: st, err: errors.New("usage unavailable")}
	m.SetUsageFinalizer(finalizer)

	if err := m.ApplyRuntimeObservation(ctx, rec.ID, ports.RuntimeFacts{Runtime: ports.ProbeDead}); err != nil {
		t.Fatal(err)
	}
	if !st.sessions[rec.ID].IsTerminated {
		t.Fatal("finalizer error prevented crash termination")
	}
	if got := logs.String(); !strings.Contains(got, "lifecycle: finalize session usage before termination") || !strings.Contains(got, "usage unavailable") {
		t.Fatalf("finalizer error log = %q", got)
	}
}

func TestRuntimeObservation_DoesNotFinalizeRejectedObservations(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	oldActivity := domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-2 * time.Minute)}
	tests := []struct {
		name  string
		rec   domain.SessionRecord
		facts ports.RuntimeFacts
	}{
		{
			name:  "probe failed",
			rec:   domain.SessionRecord{ID: "mer-1", Activity: oldActivity},
			facts: ports.RuntimeFacts{Runtime: ports.ProbeFailed, ObservedAt: now},
		},
		{
			name: "stale launch",
			rec: domain.SessionRecord{
				ID:       "mer-1",
				Activity: oldActivity,
				Metadata: domain.SessionMetadata{RuntimeLaunchID: "launch-2"},
			},
			facts: ports.RuntimeFacts{Runtime: ports.ProbeDead, LaunchID: "launch-1", ObservedAt: now},
		},
		{
			name: "workload dead while runtime alive",
			rec: domain.SessionRecord{
				ID:       "mer-1",
				Activity: oldActivity,
				Metadata: domain.SessionMetadata{RuntimeLaunchID: "launch-1"},
			},
			facts: ports.RuntimeFacts{Runtime: ports.ProbeAlive, Workload: ports.ProbeDead, LaunchID: "launch-1", ObservedAt: now},
		},
		{
			name:  "already terminated",
			rec:   domain.SessionRecord{ID: "mer-1", IsTerminated: true, Activity: domain.Activity{State: domain.ActivityExited}},
			facts: ports.RuntimeFacts{Runtime: ports.ProbeDead, ObservedAt: now},
		},
		{
			name:  "recent activity",
			rec:   domain.SessionRecord{ID: "mer-1", Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}},
			facts: ports.RuntimeFacts{Runtime: ports.ProbeDead, ObservedAt: now},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, st, _ := newManager()
			m.clock = func() time.Time { return now }
			st.sessions[tt.rec.ID] = tt.rec
			finalizer := &fakeUsageFinalizer{store: st}
			m.SetUsageFinalizer(finalizer)

			if err := m.ApplyRuntimeObservation(ctx, tt.rec.ID, tt.facts); err != nil {
				t.Fatal(err)
			}
			if finalizer.calls != 0 {
				t.Fatalf("finalizer calls=%d, want 0", finalizer.calls)
			}
		})
	}
}

func TestRuntimeObservation_DoesNotTerminateNewRuntimeGenerationAfterFinalization(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
	rec.Metadata.RuntimeLaunchID = "launch-old"
	st.sessions[rec.ID] = rec
	finalizer := &fakeUsageFinalizer{store: st}
	finalizer.onFinalize = func(id domain.SessionID, _ string, _ time.Time) error {
		return m.MarkSpawned(ctx, id, domain.SessionMetadata{RuntimeLaunchID: "launch-new"})
	}
	m.SetUsageFinalizer(finalizer)

	done := make(chan error, 1)
	go func() {
		done <- m.ApplyRuntimeObservation(ctx, rec.ID, ports.RuntimeFacts{
			Runtime:  ports.ProbeDead,
			LaunchID: "launch-old",
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ApplyRuntimeObservation deadlocked while finalizing usage")
	}

	got := st.sessions[rec.ID]
	if got.IsTerminated || got.Metadata.RuntimeLaunchID != "launch-new" {
		t.Fatalf("stale runtime observation changed new generation: %+v", got)
	}
}

func TestRuntimeObservation_DoesNotTerminateAfterActivityDuringFinalization(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	m, st, _ := newManager()
	m.clock = func() time.Time { return now }
	rec := domain.SessionRecord{
		ID:       "mer-1",
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-2 * time.Minute)},
		Metadata: domain.SessionMetadata{RuntimeLaunchID: "launch-1"},
	}
	st.sessions[rec.ID] = rec
	finalizer := &fakeUsageFinalizer{store: st}
	finalizer.onFinalize = func(id domain.SessionID, _ string, _ time.Time) error {
		return m.ApplyActivitySignal(ctx, id, ports.ActivitySignal{
			Valid:     true,
			State:     domain.ActivityIdle,
			Timestamp: now,
			LaunchID:  "launch-1",
		})
	}
	m.SetUsageFinalizer(finalizer)

	if err := m.ApplyRuntimeObservation(ctx, rec.ID, ports.RuntimeFacts{
		Runtime:    ports.ProbeDead,
		LaunchID:   "launch-1",
		ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions[rec.ID]
	if got.IsTerminated || !got.Activity.LastActivityAt.Equal(now) {
		t.Fatalf("runtime observation overrode activity recorded during finalization: %+v", got)
	}
}

func TestRuntimeObservation_RetriesAfterRevisionChangesDuringFinalization(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	m, st, _ := newManager()
	m.clock = func() time.Time { return now }
	rec := domain.SessionRecord{
		ID:        "mer-1",
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-2 * time.Minute)},
		UpdatedAt: now.Add(-2 * time.Minute),
		Metadata:  domain.SessionMetadata{RuntimeLaunchID: "launch-1"},
	}
	st.sessions[rec.ID] = rec
	finalized := 0
	var revisions []time.Time
	finalizer := &fakeUsageFinalizer{store: st}
	finalizer.onFinalize = func(id domain.SessionID, launchID string, sessionRevision time.Time) error {
		revisions = append(revisions, sessionRevision)
		if finalizer.calls == 1 {
			if err := m.ApplyActivitySignal(ctx, id, ports.ActivitySignal{
				Valid:     true,
				State:     domain.ActivityExited,
				Timestamp: now,
				Event:     "process-exited",
				LaunchID:  launchID,
			}); err != nil {
				return err
			}
		}
		current := st.sessions[id]
		if !current.IsTerminated &&
			current.Metadata.RuntimeLaunchID == launchID &&
			current.UpdatedAt.Equal(sessionRevision) {
			finalized++
		}
		return nil
	}
	m.SetUsageFinalizer(finalizer)
	facts := ports.RuntimeFacts{
		Runtime:    ports.ProbeDead,
		LaunchID:   "launch-1",
		ObservedAt: now,
	}

	if err := m.ApplyRuntimeObservation(ctx, rec.ID, facts); err != nil {
		t.Fatal(err)
	}
	got := st.sessions[rec.ID]
	if finalized != 0 || got.IsTerminated || got.Activity.State != domain.ActivityExited || !got.UpdatedAt.Equal(now) {
		t.Fatalf("first pass finalized=%d session=%+v, want no finalization and live exited revision", finalized, got)
	}

	if err := m.ApplyRuntimeObservation(ctx, rec.ID, facts); err != nil {
		t.Fatal(err)
	}
	got = st.sessions[rec.ID]
	if finalizer.calls != 2 || finalized != 1 || !got.IsTerminated {
		t.Fatalf("second pass finalizer calls=%d finalized=%d session=%+v", finalizer.calls, finalized, got)
	}
	if len(revisions) != 2 || !revisions[0].Equal(rec.UpdatedAt) || !revisions[1].Equal(now) {
		t.Fatalf("finalizer revisions=%v, want [%s %s]", revisions, rec.UpdatedAt, now)
	}
}

func TestRuntimeObservation_ConfirmedDeathIsSuppressedDuringSessionMutation(t *testing.T) {
	m, st, _ := newManager()
	m.SetSessionOperationGate(fixedSessionOperationGate(true))
	rec := working("mer-1")
	rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
	st.sessions["mer-1"] = rec

	if err := m.ApplyRuntimeObservation(ctx, "mer-1", ports.RuntimeFacts{Runtime: ports.ProbeDead, Workload: ports.ProbeFailed}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"]; got != rec {
		t.Fatalf("runtime observation mutated session during exclusive operation: got %+v, want %+v", got, rec)
	}
}

func TestRuntimeObservation_FailedProbeDoesNotMutate(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = working("mer-1")
	before := st.sessions["mer-1"]
	if err := m.ApplyRuntimeObservation(ctx, "mer-1", ports.RuntimeFacts{Runtime: ports.ProbeFailed, Workload: ports.ProbeFailed}); err != nil {
		t.Fatal(err)
	}
	if st.sessions["mer-1"] != before {
		t.Fatalf("failed probe should not persist a state, got %+v", st.sessions["mer-1"])
	}
}

func TestRuntimeObservation_ExitedWorkloadKeepsSessionLive(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch-1"
	st.sessions["mer-1"] = rec
	if err := m.ApplyRuntimeObservation(ctx, "mer-1", ports.RuntimeFacts{Runtime: ports.ProbeAlive, Workload: ports.ProbeDead, LaunchID: "launch-1"}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if got.IsTerminated || got.Activity.State != domain.ActivityExited {
		t.Fatalf("want live/exited, got %+v", got)
	}
}

func TestRuntimeObservation_AliveWorkloadCannotResurrectExitedSession(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.State = domain.ActivityExited
	rec.Metadata.RuntimeLaunchID = "launch-1"
	st.sessions["mer-1"] = rec
	before := st.sessions["mer-1"]

	if err := m.ApplyRuntimeObservation(ctx, "mer-1", ports.RuntimeFacts{
		Runtime:  ports.ProbeAlive,
		Workload: ports.ProbeAlive,
		LaunchID: "launch-1",
	}); err != nil {
		t.Fatal(err)
	}
	if st.sessions["mer-1"] != before {
		t.Fatalf("original supervisor observation resurrected exited session: %+v", st.sessions["mer-1"])
	}
}

func TestRuntimeObservation_StaleLaunchIsIgnored(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch-2"
	st.sessions["mer-1"] = rec
	before := st.sessions["mer-1"]
	if err := m.ApplyRuntimeObservation(ctx, "mer-1", ports.RuntimeFacts{Runtime: ports.ProbeAlive, Workload: ports.ProbeDead, LaunchID: "launch-1"}); err != nil {
		t.Fatal(err)
	}
	if st.sessions["mer-1"] != before {
		t.Fatalf("stale launch observation mutated session: %+v", st.sessions["mer-1"])
	}
}

func TestActivity_InvalidIsIgnored(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = working("mer-1")
	before := st.sessions["mer-1"]
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: false, State: domain.ActivityIdle}); err != nil {
		t.Fatal(err)
	}
	if st.sessions["mer-1"] != before {
		t.Fatal("invalid signal must not mutate")
	}
}

func TestActivity_MetadataOnlyStoresAgentSessionIDWithoutChangingActivity(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch-1"
	rec.FirstSignalAt = time.Now().Add(-time.Minute)
	st.sessions["mer-1"] = rec

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
		LaunchID: "launch-1", AgentSessionID: "native-session-1",
	}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if got.Metadata.AgentSessionID != "native-session-1" {
		t.Fatalf("AgentSessionID = %q, want native-session-1", got.Metadata.AgentSessionID)
	}
	if got.Metadata.AgentSessionIDLaunchID != "launch-1" {
		t.Fatalf("AgentSessionIDLaunchID = %q, want launch-1", got.Metadata.AgentSessionIDLaunchID)
	}
	if got.Activity != rec.Activity {
		t.Fatalf("metadata-only hook changed activity: got %+v, want %+v", got.Activity, rec.Activity)
	}
	if !got.FirstSignalAt.Equal(rec.FirstSignalAt) {
		t.Fatalf("metadata-only hook changed FirstSignalAt: got %v, want %v", got.FirstSignalAt, rec.FirstSignalAt)
	}
}

func TestActivity_UserPromptStoresItsSignalTimestamp(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch-1"
	rec.FirstSignalAt = time.Now().Add(-time.Minute)
	st.sessions[rec.ID] = rec
	signalAt := time.Unix(456, 0).UTC()

	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		LaunchID: "launch-1", LatestUserPrompt: "keep the row compact", Timestamp: signalAt,
	}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions[rec.ID]
	if got.Metadata.LatestUserPrompt != "keep the row compact" || !got.Metadata.LatestUserPromptAt.Equal(signalAt) {
		t.Fatalf("latest user prompt = %q at %s", got.Metadata.LatestUserPrompt, got.Metadata.LatestUserPromptAt)
	}

	repeatedAt := signalAt.Add(time.Minute)
	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid: true, State: got.Activity.State, Event: "user-prompt-submit", LaunchID: "launch-1",
		LatestUserPrompt: "keep the row compact", Timestamp: repeatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	got = st.sessions[rec.ID]
	if !got.Metadata.LatestUserPromptAt.Equal(repeatedAt) {
		t.Fatalf("repeated user prompt timestamp = %s, want %s", got.Metadata.LatestUserPromptAt, repeatedAt)
	}
}

func TestActivity_MetadataOnlyConfirmsIdentityWithoutCreatingActivityReceipt(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch-1"
	rec.FirstSignalAt = time.Time{}
	st.sessions["mer-1"] = rec

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
		Event: "session-start", LaunchID: "launch-1", AgentSessionID: "native-session-1",
	}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if got.Metadata.AgentSessionID != "native-session-1" || got.Metadata.AgentSessionIDLaunchID != "launch-1" {
		t.Fatalf("metadata-only hook did not confirm current native identity: %+v", got.Metadata)
	}
	if !got.FirstSignalAt.IsZero() {
		t.Fatalf("metadata-only hook created an activity receipt: %v", got.FirstSignalAt)
	}
	if got.Activity != rec.Activity {
		t.Fatalf("metadata-only hook changed activity: got %+v, want %+v", got.Activity, rec.Activity)
	}
}

func TestActivity_SameStateSignalStillStoresAgentSessionID(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch-1"
	rec.FirstSignalAt = time.Now().Add(-time.Minute)
	st.sessions["mer-1"] = rec

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
		Valid:          true,
		State:          rec.Activity.State,
		LaunchID:       "launch-1",
		AgentSessionID: "native-session-1",
	}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if got.Metadata.AgentSessionID != "native-session-1" {
		t.Fatalf("AgentSessionID = %q, want native-session-1", got.Metadata.AgentSessionID)
	}
	if got.Metadata.AgentSessionIDLaunchID != "launch-1" {
		t.Fatalf("AgentSessionIDLaunchID = %q, want launch-1", got.Metadata.AgentSessionIDLaunchID)
	}
	if got.Activity != rec.Activity {
		t.Fatalf("same-state metadata signal changed activity: got %+v, want %+v", got.Activity, rec.Activity)
	}
}

func TestActivity_TerminalReconciliationRequiresUnchangedSnapshot(t *testing.T) {
	m, st, _ := newManager()
	updatedAt := time.Unix(100, 0).UTC()
	rec := working("mer-1")
	rec.Kind = domain.KindWorker
	rec.FirstSignalAt = updatedAt
	rec.UpdatedAt = updatedAt
	st.sessions[rec.ID] = rec

	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid:             true,
		State:             domain.ActivityIdle,
		Event:             "terminal-idle",
		ExpectedUpdatedAt: updatedAt.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions[rec.ID].Activity.State; got != domain.ActivityActive {
		t.Fatalf("stale reconciliation changed activity to %q", got)
	}

	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid:             true,
		State:             domain.ActivityIdle,
		Event:             "terminal-idle",
		ExpectedUpdatedAt: updatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions[rec.ID].Activity.State; got != domain.ActivityIdle {
		t.Fatalf("current reconciliation left activity %q", got)
	}

	idleUpdatedAt := st.sessions[rec.ID].UpdatedAt
	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid:             true,
		State:             domain.ActivityActive,
		Event:             "terminal-active",
		ExpectedUpdatedAt: idleUpdatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions[rec.ID].Activity.State; got != domain.ActivityActive {
		t.Fatalf("current idle snapshot did not reconcile to active: %q", got)
	}
}

func TestActivity_RepeatedUserPromptFencesTerminalReconciliation(t *testing.T) {
	m, st, _ := newManager()
	before := time.Unix(100, 0).UTC()
	after := time.Unix(200, 0).UTC()
	m.clock = func() time.Time { return after }
	rec := working("mer-1")
	rec.FirstSignalAt = before
	rec.UpdatedAt = before
	st.sessions[rec.ID] = rec

	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid: true,
		State: domain.ActivityActive,
		Event: "user-prompt-submit",
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions[rec.ID].UpdatedAt; !got.Equal(after) {
		t.Fatalf("updated at = %v, want %v", got, after)
	}

	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid:             true,
		State:             domain.ActivityIdle,
		Event:             "terminal-idle",
		ExpectedUpdatedAt: before,
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions[rec.ID].Activity.State; got != domain.ActivityActive {
		t.Fatalf("fresh prompt was overwritten with %q", got)
	}
}

func TestActivity_BlankAgentSessionIDDoesNotOverwriteMetadata(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.AgentSessionID = "existing-native-1"
	st.sessions["mer-1"] = rec

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
		Valid:          true,
		State:          domain.ActivityActive,
		AgentSessionID: "   ",
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"].Metadata.AgentSessionID; got != "existing-native-1" {
		t.Fatalf("AgentSessionID = %q, want existing-native-1", got)
	}
}

func TestActivity_MissingSessionReturnsNotFound(t *testing.T) {
	m, _, _ := newManager()
	err := m.ApplyActivitySignal(ctx, "missing-1", ports.ActivitySignal{Valid: true, State: domain.ActivityWaitingInput})
	if !errors.Is(err, ports.ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestActivity_ExitedPreservesLiveSessionAndRejectsDelayedHooks(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.FirstSignalAt = time.Now().Add(-time.Minute)
	st.sessions["mer-1"] = rec
	m.flights["mer-1"] = &toolFlight{inflight: map[string]string{"tool-1": "Bash"}, blockedCandidate: "tool-1"}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityExited}); err != nil {
		t.Fatal(err)
	}
	exited := st.sessions["mer-1"]
	if exited.IsTerminated || exited.Activity.State != domain.ActivityExited {
		t.Fatalf("agent exit should preserve live session, got %+v", exited)
	}
	if _, ok := m.flights["mer-1"]; ok {
		t.Fatal("tool-flight state leaked after agent exit")
	}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive}); err != nil {
		t.Fatal(err)
	}
	stillExited := st.sessions["mer-1"]
	if stillExited.IsTerminated || stillExited.Activity.State != domain.ActivityExited {
		t.Fatalf("delayed active signal resurrected exited launch: %+v", stillExited)
	}
}

func TestActivity_UserPromptResumesExitedWorkload(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.State = domain.ActivityExited
	rec.Metadata.RuntimeLaunchID = "launch-2"
	st.sessions["mer-1"] = rec
	signalAt := time.Unix(123, 0).UTC()

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
		Valid:     true,
		State:     domain.ActivityActive,
		Event:     "user-prompt-submit",
		Timestamp: signalAt,
		LaunchID:  "launch-2",
	}); err != nil {
		t.Fatal(err)
	}

	got := st.sessions["mer-1"]
	if got.IsTerminated || got.Activity.State != domain.ActivityActive {
		t.Fatalf("valid prompt did not resume exited workload: %+v", got)
	}
	if !got.Activity.LastActivityAt.Equal(signalAt) {
		t.Fatalf("last activity = %v, want %v", got.Activity.LastActivityAt, signalAt)
	}
}

func TestActivity_StaleUserPromptDoesNotResumeExitedWorkload(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.State = domain.ActivityExited
	rec.Metadata.RuntimeLaunchID = "launch-2"
	st.sessions["mer-1"] = rec

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
		Valid:    true,
		State:    domain.ActivityActive,
		Event:    "user-prompt-submit",
		LaunchID: "launch-1",
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"]; got != rec {
		t.Fatalf("stale prompt resumed exited workload: %+v", got)
	}
}

func TestActivity_StaleLaunchSignalIsIgnored(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch-2"
	st.sessions["mer-1"] = rec
	before := st.sessions["mer-1"]
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityExited, LaunchID: "launch-1"}); err != nil {
		t.Fatal(err)
	}
	if st.sessions["mer-1"] != before {
		t.Fatalf("stale process exit mutated session: %+v", st.sessions["mer-1"])
	}
}

func TestActivity_NewLaunchSignalWaitsForMarkSpawned(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.State = domain.ActivityExited
	rec.Metadata.RuntimeLaunchID = "launch-old"
	st.sessions["mer-1"] = rec

	if err := m.PrepareLaunch("mer-1", "launch-new"); err != nil {
		t.Fatal(err)
	}
	signalAt := time.Unix(123, 0).UTC()
	signalDone := make(chan error, 1)
	go func() {
		signalDone <- m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
			Valid:     true,
			State:     domain.ActivityActive,
			Timestamp: signalAt,
			LaunchID:  "launch-new",
		})
	}()

	select {
	case err := <-signalDone:
		t.Fatalf("new-generation signal completed before MarkSpawned: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	if err := m.MarkSpawned(ctx, "mer-1", domain.SessionMetadata{
		RuntimeHandleID: "tmux-mer-1",
		RuntimeLaunchID: "launch-new",
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-signalDone; err != nil {
		t.Fatal(err)
	}

	got := st.sessions["mer-1"]
	if got.IsTerminated || got.Activity.State != domain.ActivityActive {
		t.Fatalf("early signal was not applied after spawn commit: %+v", got)
	}
	if got.Metadata.RuntimeLaunchID != "launch-new" {
		t.Fatalf("runtime launch id = %q, want launch-new", got.Metadata.RuntimeLaunchID)
	}
	if !got.FirstSignalAt.Equal(signalAt) {
		t.Fatalf("first signal at = %v, want %v", got.FirstSignalAt, signalAt)
	}
}

func TestActivity_CancelledLaunchReleasesAndRejectsEarlySignal(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.State = domain.ActivityExited
	rec.Metadata.RuntimeLaunchID = "launch-old"
	st.sessions["mer-1"] = rec

	if err := m.PrepareLaunch("mer-1", "launch-new"); err != nil {
		t.Fatal(err)
	}
	signalDone := make(chan error, 1)
	go func() {
		signalDone <- m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
			Valid:    true,
			State:    domain.ActivityActive,
			LaunchID: "launch-new",
		})
	}()

	m.CancelLaunch("mer-1", "launch-new")
	if err := <-signalDone; err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"]; got != rec {
		t.Fatalf("cancelled launch signal mutated durable state: %+v", got)
	}
}

func TestPrepareLaunchRejectsOverlappingGeneration(t *testing.T) {
	m, _, _ := newManager()
	if err := m.PrepareLaunch("mer-1", "launch-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.PrepareLaunch("mer-1", "launch-1"); err != nil {
		t.Fatalf("same generation should be idempotent: %v", err)
	}
	if err := m.PrepareLaunch("mer-1", "launch-2"); err == nil {
		t.Fatal("overlapping generation was accepted")
	}
	m.CancelLaunch("mer-1", "launch-1")
}

func TestActivity_TargetPromptSubmitAcknowledgesDeliveringAgentSwitch(t *testing.T) {
	now := time.Unix(1_700_000_100, 0).UTC()
	store := newFakeAgentSwitchLifecycleStore()
	rec := working("mer-1")
	rec.Harness = domain.HarnessCodex
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Second)}
	rec.Metadata.RuntimeLaunchID = "target-generation"
	store.setSession(rec)
	store.setActiveSwitch(domain.AgentSwitch{
		ID: "switch-1", SessionID: rec.ID, FromHarness: domain.HarnessClaudeCode,
		TargetHarness: domain.HarnessCodex, State: domain.AgentSwitchDelivering,
		SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
	})
	m := New(store, &fakeMessenger{})
	m.clock = func() time.Time { return now }

	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit", LaunchID: "target-generation",
	}); err != nil {
		t.Fatalf("ApplyActivitySignal: %v", err)
	}
	calls := store.acknowledgements()
	if len(calls) != 1 {
		t.Fatalf("acknowledgement calls = %d, want 1", len(calls))
	}
	if call := calls[0]; call.switchID != "switch-1" || call.sessionID != rec.ID || call.generation != "target-generation" || !call.at.Equal(now) {
		t.Fatalf("acknowledgement = %+v, want exact target generation at %v", call, now)
	}
	acknowledged := store.active().TargetAcknowledgedAt
	if acknowledged == nil || !acknowledged.Equal(now) {
		t.Fatalf("target acknowledgement timestamp = %v, want %v", acknowledged, now)
	}
}

func TestAcknowledgeAgentSwitchTargetReadsBackChangedFalseOutcomes(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	base := domain.AgentSwitch{
		ID: "switch-ack-readback", SessionID: "session-ack-readback",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchDelivering, SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
	}
	signal := ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit", LaunchID: "target-generation"}

	t.Run("duplicate acknowledgement is suppressed", func(t *testing.T) {
		store := &fakeAgentSwitchLifecycleStore{fakeStore: newFakeStore(), hasActiveSwitch: true, activeSwitch: base}
		acknowledgedAt := now.Add(-time.Second)
		store.activeSwitch.TargetAcknowledgedAt = &acknowledgedAt
		manager := New(store, nil)
		if err := manager.acknowledgeAgentSwitchTarget(context.Background(), base.SessionID, signal, now); err != nil {
			t.Fatalf("duplicate acknowledgement: %v", err)
		}
	})

	t.Run("impossible unchanged current predicate is surfaced", func(t *testing.T) {
		store := &fakeAgentSwitchLifecycleStore{fakeStore: newFakeStore(), hasActiveSwitch: true, activeSwitch: base, ackForceNoChange: true}
		manager := New(store, nil)
		if err := manager.acknowledgeAgentSwitchTarget(context.Background(), base.SessionID, signal, now); err == nil {
			t.Fatal("unchanged exact acknowledgement predicate returned nil")
		}
	})
}

func TestAcknowledgeAgentSwitchTargetOwnsPostAdmissionErrors(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	base := domain.AgentSwitch{
		ID: "switch-ack-owner", SessionID: "session-ack-owner",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchDelivering, SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
	}
	signal := ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit", LaunchID: "target-generation"}

	tests := []struct {
		name  string
		store *fakeAgentSwitchLifecycleStore
	}{
		{
			name: "acknowledgement failure",
			store: &fakeAgentSwitchLifecycleStore{
				fakeStore: newFakeStore(), hasActiveSwitch: true, activeSwitch: base,
				ackErr: errors.New("ack write failed"),
			},
		},
		{
			name: "read-back failure",
			store: &fakeAgentSwitchLifecycleStore{
				fakeStore: newFakeStore(), hasActiveSwitch: true, activeSwitch: base,
				ackForceNoChange: true, getSwitchErr: errors.New("read-back failed"),
			},
		},
		{
			name: "unchanged exact predicate",
			store: &fakeAgentSwitchLifecycleStore{
				fakeStore: newFakeStore(), hasActiveSwitch: true, activeSwitch: base,
				ackForceNoChange: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := New(tt.store, nil)
			err := manager.acknowledgeAgentSwitchTarget(context.Background(), base.SessionID, signal, now)
			if err == nil {
				t.Fatal("acknowledgement error = nil")
			}
			if got := ownership.OwnerOf(err); got != ownership.OwnerAgentSwitchSaga {
				t.Fatalf("acknowledgement owner = %q, want %q (error: %v)", got, ownership.OwnerAgentSwitchSaga, err)
			}
		})
	}
}

func TestActivity_InternalSourceHandoffUpdateNeverReplacesUserFacingAssistant(t *testing.T) {
	tests := []struct {
		name          string
		state         domain.AgentSwitchState
		handoffStatus domain.AgentHandoffStatus
	}{
		{name: "collecting", state: domain.AgentSwitchPreparingHandoff, handoffStatus: domain.AgentHandoffRequested},
		{name: "already received", state: domain.AgentSwitchPreparingHandoff, handoffStatus: domain.AgentHandoffReceived},
		{name: "source stop hook", state: domain.AgentSwitchStoppingSource, handoffStatus: domain.AgentHandoffReceived},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeAgentSwitchLifecycleStore()
			rec := working("mer-1")
			rec.Harness = domain.HarnessClaudeCode
			rec.Metadata.RuntimeLaunchID = "source-generation"
			rec.Metadata.LatestAssistantUpdate = "last real user-facing source update"
			store.setSession(rec)
			store.setActiveSwitch(domain.AgentSwitch{
				ID: "switch-1", SessionID: rec.ID, FromHarness: domain.HarnessClaudeCode,
				TargetHarness: domain.HarnessCodex, State: tt.state,
				AgentHandoffStatus: tt.handoffStatus, SourceGenerationID: "source-generation",
			})
			m := New(store, &fakeMessenger{})

			if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
				LaunchID: "source-generation", LatestAssistantUpdate: "internal handoff submitted",
			}); err != nil {
				t.Fatalf("ApplyActivitySignal: %v", err)
			}
			if got := store.session(rec.ID).Metadata.LatestAssistantUpdate; got != rec.Metadata.LatestAssistantUpdate {
				t.Fatalf("latest assistant update = %q, want preserved %q", got, rec.Metadata.LatestAssistantUpdate)
			}
		})
	}
}

func TestActivity_SourceUpdateBeforeInternalHandoffRequestRemainsUserFacing(t *testing.T) {
	for _, status := range []domain.AgentHandoffStatus{domain.AgentHandoffNotAttempted, domain.AgentHandoffUnavailable} {
		t.Run(string(status), func(t *testing.T) {
			store := newFakeAgentSwitchLifecycleStore()
			rec := working("mer-1")
			rec.Harness = domain.HarnessClaudeCode
			rec.Metadata.RuntimeLaunchID = "source-generation"
			rec.Metadata.LatestAssistantUpdate = "earlier source update"
			store.setSession(rec)
			store.setActiveSwitch(domain.AgentSwitch{
				ID: "switch-1", SessionID: rec.ID, FromHarness: domain.HarnessClaudeCode,
				TargetHarness: domain.HarnessCodex, State: domain.AgentSwitchPreparingHandoff,
				AgentHandoffStatus: status, SourceGenerationID: "source-generation",
			})
			m := New(store, &fakeMessenger{})

			if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
				LaunchID: "source-generation", LatestAssistantUpdate: "real turn completed before AO requested a handoff",
			}); err != nil {
				t.Fatalf("ApplyActivitySignal: %v", err)
			}
			if got := store.session(rec.ID).Metadata.LatestAssistantUpdate; got != "real turn completed before AO requested a handoff" {
				t.Fatalf("latest assistant update = %q, want legitimate pre-request update", got)
			}
		})
	}
}

func TestActivity_LateSourceSignalAfterStopConfirmationIsFenced(t *testing.T) {
	stoppedAt := time.Unix(1_700_000_150, 0).UTC()
	store := newFakeAgentSwitchLifecycleStore()
	rec := working("mer-1")
	rec.Harness = domain.HarnessClaudeCode
	rec.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: stoppedAt}
	rec.Metadata.RuntimeLaunchID = "source-generation"
	rec.Metadata.AgentSessionID = "source-native"
	rec.Metadata.LatestAssistantUpdate = "last real source update"
	rec.UpdatedAt = stoppedAt
	store.setSession(rec)
	store.setActiveSwitch(domain.AgentSwitch{
		ID: "switch-1", SessionID: rec.ID, FromHarness: domain.HarnessClaudeCode,
		TargetHarness: domain.HarnessCodex, State: domain.AgentSwitchSourceStopped,
		SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
	})
	m := New(store, &fakeMessenger{})
	m.clock = func() time.Time { return stoppedAt.Add(time.Minute) }

	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit",
		LaunchID: "source-generation", AgentSessionID: "late-source-native",
		LatestAssistantUpdate: "late source callback",
	}); err != nil {
		t.Fatalf("ApplyActivitySignal: %v", err)
	}
	if got := store.session(rec.ID); got != rec {
		t.Fatalf("late source signal mutated stopped session: got %+v, want %+v", got, rec)
	}
	if calls := store.acknowledgements(); len(calls) != 0 {
		t.Fatalf("late source signal produced target acknowledgements: %+v", calls)
	}
}

func TestActivity_LateSourceSignalAfterTargetActivationIsFenced(t *testing.T) {
	activatedAt := time.Unix(1_700_000_175, 0).UTC()
	store := newFakeAgentSwitchLifecycleStore()
	rec := working("mer-1")
	rec.Harness = domain.HarnessCodex
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: activatedAt}
	rec.Metadata.RuntimeLaunchID = "target-generation"
	rec.Metadata.AgentSessionID = "target-native"
	rec.UpdatedAt = activatedAt
	store.setSession(rec)
	store.setActiveSwitch(domain.AgentSwitch{
		ID: "switch-1", SessionID: rec.ID, FromHarness: domain.HarnessClaudeCode,
		TargetHarness: domain.HarnessCodex, State: domain.AgentSwitchTargetReady,
		SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
	})
	m := New(store, &fakeMessenger{})
	m.clock = func() time.Time { return activatedAt.Add(time.Minute) }

	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit",
		LaunchID: "source-generation", AgentSessionID: "late-source-native",
	}); err != nil {
		t.Fatalf("ApplyActivitySignal: %v", err)
	}
	if got := store.session(rec.ID); got != rec {
		t.Fatalf("late source signal mutated target owner: got %+v, want %+v", got, rec)
	}
	if calls := store.acknowledgements(); len(calls) != 0 {
		t.Fatalf("late source signal produced target acknowledgements: %+v", calls)
	}
}

func TestActivity_StaleGenerationPromptSubmitDoesNotAcknowledgeAgentSwitch(t *testing.T) {
	now := time.Unix(1_700_000_200, 0).UTC()
	store := newFakeAgentSwitchLifecycleStore()
	rec := working("mer-1")
	rec.Harness = domain.HarnessCodex
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Second)}
	rec.Metadata.RuntimeLaunchID = "target-generation"
	store.setSession(rec)
	store.setActiveSwitch(domain.AgentSwitch{
		ID: "switch-1", SessionID: rec.ID, FromHarness: domain.HarnessClaudeCode,
		TargetHarness: domain.HarnessCodex, State: domain.AgentSwitchDelivering,
		SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
	})
	m := New(store, &fakeMessenger{})
	m.clock = func() time.Time { return now }

	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit", LaunchID: "abandoned-generation",
	}); err != nil {
		t.Fatalf("ApplyActivitySignal: %v", err)
	}
	if calls := store.acknowledgements(); len(calls) != 0 {
		t.Fatalf("stale generation produced acknowledgement calls: %+v", calls)
	}
	if got := store.session(rec.ID); got != rec {
		t.Fatalf("stale generation mutated session: got %+v, want %+v", got, rec)
	}
}

func TestActivity_TargetPromptSubmitOutsideDeliveryDoesNotAcknowledgeAgentSwitch(t *testing.T) {
	now := time.Unix(1_700_000_300, 0).UTC()
	store := newFakeAgentSwitchLifecycleStore()
	rec := working("mer-1")
	rec.Harness = domain.HarnessCodex
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Second)}
	rec.Metadata.RuntimeLaunchID = "target-generation"
	store.setSession(rec)
	store.setActiveSwitch(domain.AgentSwitch{
		ID: "switch-1", SessionID: rec.ID, FromHarness: domain.HarnessClaudeCode,
		TargetHarness: domain.HarnessCodex, State: domain.AgentSwitchTargetReady,
		SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
	})
	m := New(store, &fakeMessenger{})
	m.clock = func() time.Time { return now }

	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit", LaunchID: "target-generation",
	}); err != nil {
		t.Fatalf("ApplyActivitySignal: %v", err)
	}
	if calls := store.acknowledgements(); len(calls) != 0 {
		t.Fatalf("non-delivery state produced acknowledgement calls: %+v", calls)
	}
	if got := store.session(rec.ID); got.Activity.State != domain.ActivityActive {
		t.Fatalf("valid target activity was not applied: %+v", got)
	}
}

func TestActivity_ReleaseLaunchUnblocksTargetHookAfterAtomicOwnershipTransfer(t *testing.T) {
	activatedAt := time.Unix(1_700_000_400, 0).UTC()
	store := newFakeAgentSwitchLifecycleStore()
	targetNativeRef := domain.AgentNativeSessionID("native-target")
	store.native[targetNativeRef] = domain.AgentNativeSession{
		ID: targetNativeRef, AOSessionID: "mer-1", Harness: domain.HarnessCodex,
		LastGenerationID: "target-generation",
		CreatedAt:        activatedAt.Add(-time.Second), LastUsedAt: activatedAt.Add(-time.Second),
	}
	rec := working("mer-1")
	rec.Harness = domain.HarnessClaudeCode
	rec.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: activatedAt.Add(-time.Second)}
	rec.Metadata.RuntimeHandleID = "runtime-1"
	rec.Metadata.RuntimeLaunchID = "source-generation"
	store.setSession(rec)
	store.setActiveSwitch(domain.AgentSwitch{
		ID: "switch-1", SessionID: rec.ID, FromHarness: domain.HarnessClaudeCode,
		TargetHarness: domain.HarnessCodex, State: domain.AgentSwitchStartingTarget,
		SourceGenerationID: "source-generation", TargetGenerationID: "target-generation",
		TargetNativeSessionRef: &targetNativeRef,
	})
	m := New(store, &fakeMessenger{})
	m.clock = func() time.Time { return activatedAt.Add(time.Second) }
	if err := m.PrepareLaunch(rec.ID, "target-generation"); err != nil {
		t.Fatalf("PrepareLaunch: %v", err)
	}

	signalDone := make(chan error, 1)
	go func() {
		signalDone <- m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
			Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit", LaunchID: "target-generation",
			AgentSessionID: "codex-native", TranscriptPath: "/provider/codex-native.jsonl",
		})
	}()
	select {
	case err := <-signalDone:
		t.Fatalf("target hook completed before ownership transfer and ReleaseLaunch: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	store.mu.Lock()
	stagedNative := store.native[targetNativeRef]
	stagedSource := store.sessions[rec.ID]
	store.mu.Unlock()
	if stagedNative.NativeSessionID != "codex-native" || stagedNative.TranscriptPath != "/provider/codex-native.jsonl" {
		t.Fatalf("pending target metadata was not staged durably: %+v", stagedNative)
	}
	if stagedSource.Harness != domain.HarnessClaudeCode || stagedSource.Metadata.AgentSessionID == "codex-native" {
		t.Fatalf("pending target hook mutated source-owned session: %+v", stagedSource)
	}

	activated, err := m.ActivateAgentSwitchTarget(ctx, domain.AgentSwitchTargetActivation{
		SwitchID: "switch-1", SessionID: rec.ID, SourceHarness: domain.HarnessClaudeCode,
		SourceGenerationID: "source-generation", ExpectedSourceRuntimeLaunchID: "source-generation",
		TargetHarness: domain.HarnessCodex, TargetGenerationID: "target-generation",
		RuntimeHandleID: "runtime-1", ActivatedAt: activatedAt,
	})
	if err != nil || !activated {
		t.Fatalf("ActivateAgentSwitchTarget = (%v, %v), want (true, nil)", activated, err)
	}
	m.ReleaseLaunch(rec.ID, "target-generation")
	select {
	case err := <-signalDone:
		if err != nil {
			t.Fatalf("released target hook: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReleaseLaunch did not unblock target hook")
	}

	got := store.session(rec.ID)
	if got.Harness != domain.HarnessCodex || got.Metadata.RuntimeLaunchID != "target-generation" || got.Metadata.AgentSessionID != "codex-native" || got.Activity.State != domain.ActivityActive {
		t.Fatalf("released hook did not apply to atomically transferred target owner: %+v", got)
	}
}

func TestMarkTerminated(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = working("mer-1")
	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if !got.IsTerminated || got.Activity.State != domain.ActivityExited {
		t.Fatalf("want terminated/exited, got %+v", got)
	}
}

type fakeUsageFinalizer struct {
	store           *fakeStore
	calls           int
	sawTerminated   bool
	launchID        string
	sessionRevision time.Time
	err             error
	onFinalize      func(domain.SessionID, string, time.Time) error
}

func (f *fakeUsageFinalizer) FinalizeSession(
	_ context.Context,
	id domain.SessionID,
	launchID string,
	sessionRevision time.Time,
) error {
	f.calls++
	f.sawTerminated = f.store.sessions[id].IsTerminated
	f.launchID = launchID
	f.sessionRevision = sessionRevision
	if f.onFinalize != nil {
		return f.onFinalize(id, launchID, sessionRevision)
	}
	return f.err
}

type fakeUsageLifecycle struct {
	fakeUsageFinalizer
	reactivateCalls  int
	reactivateID     domain.SessionID
	reactivateLaunch string
	sawLive          bool
}

func (f *fakeUsageLifecycle) ReactivateSession(
	_ context.Context,
	id domain.SessionID,
	launchID string,
) error {
	f.reactivateCalls++
	f.reactivateID = id
	f.reactivateLaunch = launchID
	f.sawLive = !f.store.sessions[id].IsTerminated
	return nil
}

func TestMarkSpawnedReactivatesUsageAfterLifecycleTransition(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID:           "mer-1",
		ProjectID:    "mer",
		IsTerminated: true,
		Activity:     domain.Activity{State: domain.ActivityExited},
		Metadata:     domain.SessionMetadata{RuntimeLaunchID: "launch-old"},
	}
	usage := &fakeUsageLifecycle{fakeUsageFinalizer: fakeUsageFinalizer{store: st}}
	m.SetUsageFinalizer(usage)

	if err := m.MarkSpawned(ctx, "mer-1", domain.SessionMetadata{RuntimeLaunchID: "launch-new"}); err != nil {
		t.Fatal(err)
	}
	if usage.reactivateCalls != 1 || usage.reactivateID != "mer-1" ||
		usage.reactivateLaunch != "launch-new" || !usage.sawLive {
		t.Fatalf("usage reactivation = calls:%d id:%q launch:%q live:%v",
			usage.reactivateCalls, usage.reactivateID, usage.reactivateLaunch, usage.sawLive)
	}
}

func TestMarkTerminatedFinalizesUsageBeforeLifecycleTransition(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.UpdatedAt = time.Date(2026, 8, 2, 11, 30, 0, 0, time.UTC)
	st.sessions[rec.ID] = rec
	finalizer := &fakeUsageFinalizer{store: st, err: errors.New("best effort failure")}
	m.SetUsageFinalizer(finalizer)

	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if finalizer.calls != 1 || finalizer.sawTerminated {
		t.Fatalf("finalizer calls=%d sawTerminated=%v, want 1/false", finalizer.calls, finalizer.sawTerminated)
	}
	if !finalizer.sessionRevision.Equal(rec.UpdatedAt) {
		t.Fatalf("finalizer session revision=%s, want %s", finalizer.sessionRevision, rec.UpdatedAt)
	}
	if !st.sessions["mer-1"].IsTerminated {
		t.Fatal("finalizer failure prevented session termination")
	}
	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if finalizer.calls != 1 {
		t.Fatalf("already terminated session finalized %d times, want once", finalizer.calls)
	}
}

func TestMarkTerminatedDoesNotTerminateNewRuntimeGeneration(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch-old"
	st.sessions[rec.ID] = rec
	finalizer := &fakeUsageFinalizer{store: st}
	finalizer.onFinalize = func(id domain.SessionID, _ string, _ time.Time) error {
		return m.MarkSpawned(ctx, id, domain.SessionMetadata{RuntimeLaunchID: "launch-new"})
	}
	m.SetUsageFinalizer(finalizer)

	if err := m.MarkTerminated(ctx, rec.ID); err == nil || !strings.Contains(err.Error(), "runtime launch changed") {
		t.Fatalf("MarkTerminated() error = %v, want runtime launch change", err)
	}
	if finalizer.launchID != "launch-old" {
		t.Fatalf("finalizer launch id=%q, want launch-old", finalizer.launchID)
	}
	got := st.sessions[rec.ID]
	if got.IsTerminated || got.Metadata.RuntimeLaunchID != "launch-new" {
		t.Fatalf("stale termination changed new runtime generation: %+v", got)
	}
}

func TestMarkTerminatedRetriesFinalizationAfterSameLaunchRevisionChange(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch-1"
	rec.UpdatedAt = time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	st.sessions[rec.ID] = rec
	var revisions []time.Time
	finalizer := &fakeUsageFinalizer{store: st}
	finalizer.onFinalize = func(id domain.SessionID, _ string, revision time.Time) error {
		revisions = append(revisions, revision)
		if len(revisions) == 1 {
			current := st.sessions[id]
			current.UpdatedAt = current.UpdatedAt.Add(time.Second)
			st.sessions[id] = current
		}
		return nil
	}
	m.SetUsageFinalizer(finalizer)

	if err := m.MarkTerminated(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || !revisions[0].Equal(rec.UpdatedAt) || !revisions[1].Equal(rec.UpdatedAt.Add(time.Second)) {
		t.Fatalf("finalization revisions = %v", revisions)
	}
	if !st.sessions[rec.ID].IsTerminated {
		t.Fatal("session was not terminated after revision-fenced retry")
	}
}

func TestMarkSpawnedStoresRuntimeMetadata(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", IsTerminated: true}
	metadata := domain.SessionMetadata{
		Branch:            "b",
		WorkspacePath:     "/ws",
		WorkspaceRepoPath: "/repos/mer",
		RuntimeHandleID:   "h1",
		AgentSessionID:    "agent",
		Prompt:            "prompt",
	}
	if err := m.MarkSpawned(ctx, "mer-1", metadata); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if got.IsTerminated || got.Activity.State != domain.ActivityIdle || got.Metadata.RuntimeHandleID != "h1" {
		t.Fatalf("spawn metadata wrong: %+v", got)
	}
	if got.Metadata.WorkspaceRepoPath != metadata.WorkspaceRepoPath {
		t.Fatalf("workspace repo path = %q, want %q", got.Metadata.WorkspaceRepoPath, metadata.WorkspaceRepoPath)
	}
}

func TestMarkSpawnedPersistsAndPreservesDiffBase(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", IsTerminated: true}

	wantSHA := "0123456789abcdef"
	wantRef := "refs/remotes/origin/main"
	if err := m.MarkSpawned(ctx, "mer-1", domain.SessionMetadata{
		WorkspacePath: "/ws",
		DiffBaseSHA:   wantSHA,
		DiffBaseRef:   wantRef,
	}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"].Metadata
	if got.DiffBaseSHA != wantSHA || got.DiffBaseRef != wantRef {
		t.Fatalf("spawn diff base = sha:%q ref:%q, want sha:%q ref:%q", got.DiffBaseSHA, got.DiffBaseRef, wantSHA, wantRef)
	}

	// Restore does not recompute the base. Empty incoming values must preserve
	// the durable comparison metadata recorded by the initial spawn.
	if err := m.MarkSpawned(ctx, "mer-1", domain.SessionMetadata{
		WorkspacePath:   "/ws",
		RuntimeHandleID: "h2",
	}); err != nil {
		t.Fatal(err)
	}
	got = st.sessions["mer-1"].Metadata
	if got.DiffBaseSHA != wantSHA || got.DiffBaseRef != wantRef {
		t.Fatalf("restored diff base = sha:%q ref:%q, want preserved sha:%q ref:%q", got.DiffBaseSHA, got.DiffBaseRef, wantSHA, wantRef)
	}
}

func TestCommitControllerEpochOwnsModeAndActivityFacts(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityWaitingInput, LastActivityAt: time.Unix(10, 0)},
		Metadata: domain.SessionMetadata{
			RuntimeHandleID: "runtime-1", RuntimeLaunchID: "launch-1",
			AgentSessionID: "native-1",
		},
	}

	changed, err := m.CommitControllerEpoch(
		ctx, "mer-1", domain.SessionModeTUI, domain.SessionModeChat, "native-1", false,
	)
	if err != nil || !changed {
		t.Fatalf("CommitControllerEpoch: changed=%v err=%v", changed, err)
	}
	got := st.sessions["mer-1"]
	if got.Mode != domain.SessionModeChat || got.Activity.State != domain.ActivityIdle {
		t.Fatalf("controller facts = mode:%q activity:%q", got.Mode, got.Activity.State)
	}
	if got.Metadata.RuntimeHandleID != "" || got.Metadata.RuntimeLaunchID != "" ||
		got.Metadata.AgentSessionID != "native-1" ||
		got.Metadata.AgentSessionIDLaunchID != "" ||
		got.Metadata.ProviderConversationID != "native-1" ||
		got.Metadata.ControllerGeneration != "" {
		t.Fatalf("controller metadata = %+v", got.Metadata)
	}
	changed, err = m.CommitControllerEpoch(
		ctx, "mer-1", domain.SessionModeTUI, domain.SessionModeChat, "native-1", false,
	)
	if err != nil || changed {
		t.Fatalf("stale controller epoch: changed=%v err=%v", changed, err)
	}
}

func TestCommitControllerEpochAllowsExplicitFreshHandoff(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityIdle},
		Metadata: domain.SessionMetadata{
			RuntimeHandleID: "runtime-1", RuntimeLaunchID: "launch-1",
			AgentSessionID: "reserved-but-empty",
		},
	}

	changed, err := m.CommitControllerEpoch(
		ctx, "mer-1", domain.SessionModeTUI, domain.SessionModeChat, "", true,
	)
	if err != nil || !changed {
		t.Fatalf("CommitControllerEpoch fresh: changed=%v err=%v", changed, err)
	}
	got := st.sessions["mer-1"]
	if got.Mode != domain.SessionModeChat || got.Metadata.AgentSessionID != "" ||
		got.Metadata.ProviderConversationID != "" {
		t.Fatalf("fresh controller facts = %+v", got)
	}

	if _, err := m.CommitControllerEpoch(
		ctx, "mer-1", domain.SessionModeChat, domain.SessionModeTUI, "", false,
	); err == nil {
		t.Fatal("blank native id without explicit fresh handoff was accepted")
	}
}

// TestMarkSpawned_StampsUTCActivity locks the lifecycle clock to UTC so
// activity-driven timestamps match the session manager's spawn timestamps. A
// local clock here left `ao session get` showing created in UTC but updated in
// local time.
func TestMarkSpawned_StampsUTCActivity(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", IsTerminated: true}
	if err := m.MarkSpawned(ctx, "mer-1", domain.SessionMetadata{RuntimeHandleID: "h1"}); err != nil {
		t.Fatal(err)
	}
	if loc := st.sessions["mer-1"].Activity.LastActivityAt.Location(); loc != time.UTC {
		t.Fatalf("LastActivityAt location = %v, want UTC", loc)
	}
}

func TestActivity_WaitingInputEntryAndExitEmitTelemetry(t *testing.T) {
	st := newFakeStore()
	sink := &telemetrySink{}
	m := New(st, nil, WithTelemetry(sink))
	now := time.Unix(100, 0).UTC()
	m.clock = func() time.Time { return now }
	st.sessions["mer-1"] = domain.SessionRecord{
		ID:        "mer-1",
		ProjectID: "mer",
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)},
	}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityWaitingInput, Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Second)
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Timestamp: now}); err != nil {
		t.Fatal(err)
	}

	if len(sink.events) != 2 {
		t.Fatalf("events = %#v, want waiting_input entered/exited", sink.events)
	}
	if sink.events[0].Name != "ao.session.waiting_input_entered" || sink.events[1].Name != "ao.session.waiting_input_exited" {
		t.Fatalf("event names = %#v", []string{sink.events[0].Name, sink.events[1].Name})
	}
	if got := sink.events[1].Payload["dwell_ms"]; got != int64(3000) {
		t.Fatalf("dwell_ms = %#v, want 3000", got)
	}
}

func TestPRObservation_CIFailingNudgesAgentWithLogs(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	o := ports.PRObservation{Fetched: true, URL: "pr1", CI: domain.CIFailing, Checks: []ports.PRCheckObservation{
		{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, URL: "https://ci.example/build", LogTail: "boom"},
		{Name: "lint", CommitHash: "c1", Status: domain.PRCheckCancelled, URL: "https://ci.example/lint"},
	}}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("want one CI nudge with log tail, got %v", msg.msgs)
	}
	for _, want := range []string{
		"CI is failing on your PR.",
		"Failed: build (failed)",
		"Failure URL: https://ci.example/build",
		"Log tail (last 1 line):",
		"boom",
		"fetch full CI logs only if you need additional context",
	} {
		if !strings.Contains(msg.msgs[0], want) {
			t.Fatalf("CI nudge missing %q:\n%s", want, msg.msgs[0])
		}
	}
	if strings.Contains(msg.msgs[0], "lint") || strings.Contains(msg.msgs[0], "cancelled") {
		t.Fatalf("cancelled checks must not be included in CI nudge:\n%s", msg.msgs[0])
	}
}

func TestPRObservation_CancelledChecksDoNotNudge(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	o := ports.PRObservation{Fetched: true, URL: "pr1", CI: domain.CIFailing, Checks: []ports.PRCheckObservation{
		{Name: "lint", CommitHash: "c1", Status: domain.PRCheckCancelled, URL: "https://ci.example/lint"},
	}}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("cancelled-only checks must not nudge, got %v", msg.msgs)
	}
}

func TestPRObservation_CIFailureNotInjectedWhenPRPolicyDisabled(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.prPolicies["pr1"] = false
	o := ports.PRObservation{Fetched: true, URL: "pr1", CI: domain.CIFailing, Checks: []ports.PRCheckObservation{
		{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"},
	}}

	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("CI failure was injected with PR policy disabled: %v", msg.msgs)
	}
}

func TestReviewCommentsSignatureUsesStableIDs(t *testing.T) {
	original := []ports.PRCommentObservation{
		{ID: "c1", ThreadID: "t1", Author: "alice", File: "old.go", Line: 10, Body: "old", URL: "https://old"},
		{ID: "c2", ThreadID: "t2", Author: "bob", File: "old.go", Line: 20, Body: "old", URL: "https://old"},
	}
	editedAndReordered := []ports.PRCommentObservation{
		{ID: "c2", ThreadID: "t2", Author: "bob", File: "new.go", Line: 99, Body: "edited", URL: "https://new"},
		{ID: "c1", ThreadID: "t1", Author: "alice", File: "new.go", Line: 42, Body: "edited", URL: "https://new"},
	}
	if got, want := reviewCommentsSignature(editedAndReordered), reviewCommentsSignature(original); got != want {
		t.Fatalf("signature changed after edit/reorder\n got %q\nwant %q", got, want)
	}

	withNewComment := append([]ports.PRCommentObservation(nil), original...)
	withNewComment = append(withNewComment, ports.PRCommentObservation{ID: "c3", ThreadID: "t2", Body: "new comment in same thread"})
	if got, old := reviewCommentsSignature(withNewComment), reviewCommentsSignature(original); got == old {
		t.Fatalf("new comment id should change signature, got %q", got)
	}
}

func TestFormatCIFailureMessageUsesNonMutatingFence(t *testing.T) {
	logTail := "start\n```\ninner\n````\nend"
	msg := formatCIFailureMessage([]ports.PRCheckObservation{{
		Name: "build", Status: domain.PRCheckFailed, LogTail: logTail,
	}})
	if !strings.Contains(msg, logTail) {
		t.Fatalf("message should preserve log text without zero-width mutation:\n%s", msg)
	}
	if strings.Contains(msg, "\u200b") {
		t.Fatalf("message must not insert zero-width characters:\n%s", msg)
	}
	if !strings.Contains(msg, "`````\n"+logTail+"\n`````") {
		t.Fatalf("message should wrap log in a fence longer than embedded runs:\n%s", msg)
	}
}

func TestPRObservation_ReviewCommentsNudgeAgent(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.comments["pr1"] = []domain.PullRequestComment{
		{ID: "1", ThreadID: "T1", Author: "alice", File: "foo.go", Line: 12, Body: "fix this", URL: "https://github.com/o/r/pull/1#discussion_r1", AutoInjectReview: true},
		{ID: "2", Author: "bob", Body: "already handled", Resolved: true, AutoInjectReview: true},
	}
	o := ports.PRObservation{Fetched: true, URL: "pr1", Review: domain.ReviewChangesRequest}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("want review nudge, got %v", msg.msgs)
	}
	for _, want := range []string{
		"The following 1 unresolved review comment(s)",
		"foo.go:12 (@alice):",
		"fix this",
		"https://github.com/o/r/pull/1#discussion_r1",
		"Thread ID: T1",
		"re-fetch review data unless you need additional context",
	} {
		if !strings.Contains(msg.msgs[0], want) {
			t.Fatalf("review nudge missing %q:\n%s", want, msg.msgs[0])
		}
	}
	if strings.Contains(msg.msgs[0], "already handled") {
		t.Fatalf("review nudge included resolved comment:\n%s", msg.msgs[0])
	}
}

func TestPRObservation_ReviewFeedbackNotInjectedWhenDisabled(t *testing.T) {
	m, st, msg := newManager()
	rec := working("mer-1")
	st.sessions[rec.ID] = rec
	st.comments["pr1"] = []domain.PullRequestComment{{ID: "1", Author: "alice", Body: "fix this", AutoInjectReview: false}}
	st.reviews["pr1"] = []domain.PullRequestReview{{ID: "r1", Author: "alice", State: domain.ReviewChangesRequest, Body: "change this too", AutoInjectReview: false}}
	o := ports.PRObservation{
		Fetched: true,
		URL:     "pr1",
		CI:      domain.CIFailing,
		Checks:  []ports.PRCheckObservation{{Name: "build", Status: domain.PRCheckFailed, LogTail: "boom"}},
		Review:  domain.ReviewChangesRequest,
	}
	if err := m.ApplyPRObservation(ctx, rec.ID, o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 || !strings.Contains(msg.msgs[0], "boom") || strings.Contains(msg.msgs[0], "fix this") {
		t.Fatalf("messages = %v, want CI only while review feedback is not injected", msg.msgs)
	}
}

func TestPRObservation_MixedPersistedCommentDecisions(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.comments["pr1"] = []domain.PullRequestComment{
		{ID: "1", Author: "alice", Body: "captured while disabled", AutoInjectReview: false},
		{ID: "2", Author: "alice", Body: "captured while enabled", AutoInjectReview: true},
	}
	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Review: domain.ReviewChangesRequest}); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("want only the injectable comment to nudge, got %v", msg.msgs)
	}
	if !strings.Contains(msg.msgs[0], "captured while enabled") || strings.Contains(msg.msgs[0], "captured while disabled") {
		t.Fatalf("nudge did not honor per-comment persisted decisions: %q", msg.msgs[0])
	}
}

func TestPRObservation_CIFailingAndReviewBothNudge(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.comments["pr1"] = []domain.PullRequestComment{{ID: "1", Author: "alice", Body: "fix this", AutoInjectReview: true}}
	o := ports.PRObservation{
		Fetched: true,
		URL:     "pr1",
		CI:      domain.CIFailing,
		Checks:  []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}},
		Review:  domain.ReviewChangesRequest,
	}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	// Both actionable items fire — neither is suppressed by the other — in queue
	// order (CI first, then review).
	if len(msg.msgs) != 2 {
		t.Fatalf("want CI and review nudges, got %d: %v", len(msg.msgs), msg.msgs)
	}
	if !strings.Contains(msg.msgs[0], "boom") {
		t.Fatalf("first nudge should carry the CI failure, got %q", msg.msgs[0])
	}
	if !strings.Contains(msg.msgs[1], "fix this") {
		t.Fatalf("second nudge should carry the review feedback, got %q", msg.msgs[1])
	}
	// Re-observing the identical state re-nudges nothing: per-item dedup is intact.
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 2 {
		t.Fatalf("re-observation should not re-nudge, got %d: %v", len(msg.msgs), msg.msgs)
	}
}

// A failed parent-stack lookup (the store read behind the merge-conflict
// exemption) must not discard the CI/review nudges already queued for the same
// PR. Only the merge-conflict nudge is skipped this cycle; CI and review still
// send, the read error still surfaces so the observer logs and re-polls, and the
// merge-conflict nudge fires once the read recovers.
func TestPRObservation_MergeConflictReadErrorStillSendsCIAndReview(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.comments["pr1"] = []domain.PullRequestComment{{ID: "1", Author: "alice", Body: "fix this", AutoInjectReview: true}}
	st.listPRsErr = errors.New("transient store read failure")
	o := ports.PRObservation{
		Fetched:      true,
		URL:          "pr1",
		CI:           domain.CIFailing,
		Checks:       []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}},
		Review:       domain.ReviewChangesRequest,
		Mergeability: domain.MergeConflicting,
	}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err == nil {
		t.Fatal("want the deferred parent-stack read error surfaced")
	}
	// CI and review still fired despite the merge-conflict lookup failing; the
	// merge-conflict nudge itself is skipped this cycle.
	if len(msg.msgs) != 2 {
		t.Fatalf("want CI and review nudges sent, got %d: %v", len(msg.msgs), msg.msgs)
	}
	if !strings.Contains(msg.msgs[0], "boom") {
		t.Fatalf("first nudge should carry the CI failure, got %q", msg.msgs[0])
	}
	if !strings.Contains(msg.msgs[1], "fix this") {
		t.Fatalf("second nudge should carry the review feedback, got %q", msg.msgs[1])
	}
	for _, sent := range msg.msgs {
		if strings.Contains(sent, "merge conflicts") {
			t.Fatalf("merge-conflict nudge must be skipped on read error, got %q", sent)
		}
	}

	// Next poll, the read recovers: the merge-conflict nudge now fires while
	// CI/review stay deduped (their signatures persisted before the error).
	st.listPRsErr = nil
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 3 {
		t.Fatalf("want merge-conflict nudge on recovery with CI/review deduped, got %d: %v", len(msg.msgs), msg.msgs)
	}
	if !strings.Contains(msg.msgs[2], "merge conflicts") {
		t.Fatalf("third nudge should be the merge-conflict nudge, got %q", msg.msgs[2])
	}
}

func TestPRObservation_CINudgeSanitizesLogTailControlChars(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	// A CI log tail with an embedded ANSI escape sequence and a NUL byte; the
	// agent's pane must receive the visible text without the control bytes.
	o := ports.PRObservation{Fetched: true, URL: "pr1", CI: domain.CIFailing, Checks: []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "line1\x1b[2Jline2\x00\ttabbed"}}}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("want one CI nudge, got %v", msg.msgs)
	}
	got := msg.msgs[0]
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\x00') {
		t.Fatalf("nudge still carries control bytes: %q", got)
	}
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line2") || !strings.Contains(got, "\ttabbed") {
		t.Fatalf("nudge dropped visible text or tab: %q", got)
	}
}

func TestPRObservation_ReviewNudgeSanitizesCommentControlChars(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.comments["pr1"] = []domain.PullRequestComment{{ID: "1", Body: "please\x1b]0;pwned\afix this", AutoInjectReview: true}}
	o := ports.PRObservation{Fetched: true, URL: "pr1", Review: domain.ReviewChangesRequest}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("want one review nudge, got %v", msg.msgs)
	}
	got := msg.msgs[0]
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\a') {
		t.Fatalf("review nudge still carries control bytes: %q", got)
	}
	if !strings.Contains(got, "please") || !strings.Contains(got, "fix this") {
		t.Fatalf("review nudge dropped visible text: %q", got)
	}
}

func TestPRObservation_ChangesRequestedReviewUsesPersistedBodyAndDecision(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.reviews["pr1"] = []domain.PullRequestReview{
		{ID: "r1", Author: "alice\x1b]0;pwned\a", State: domain.ReviewChangesRequest, URL: "https://github.com/o/r/pull/1#pullrequestreview-1", Body: "please\x1b[2Jfix this", AutoInjectReview: true},
		{ID: "r2", Author: "bob", State: domain.ReviewApproved, Body: "approved", AutoInjectReview: true},
		{ID: "r3", Author: "carol", State: domain.ReviewChangesRequest, Body: "do not inject", AutoInjectReview: false},
	}
	o := ports.PRObservation{Fetched: true, URL: "pr1", Review: domain.ReviewChangesRequest}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("want one persisted review nudge, got %v", msg.msgs)
	}
	got := msg.msgs[0]
	for _, want := range []string{"@alice", "please", "fix this", "https://github.com/o/r/pull/1#pullrequestreview-1", "Review ID: r1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("review nudge missing %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\a') {
		t.Fatalf("review nudge still carries control bytes: %q", got)
	}
	if strings.Contains(got, "approved") || strings.Contains(got, "do not inject") {
		t.Fatalf("review nudge included an ineligible persisted review: %q", got)
	}
}

func TestSCMObservationProjectsToExistingPRReactions(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	o := ports.SCMObservation{
		Fetched: true,
		PR:      ports.SCMPRObservation{URL: "pr1", Number: 1},
		CI: ports.SCMCIObservation{
			Summary: string(domain.CIFailing),
			HeadSHA: "c1",
			FailedChecks: []ports.SCMCheckObservation{{
				Name: "build", Status: string(domain.PRCheckFailed), LogTail: "boom",
			}},
		},
	}
	if err := m.ApplySCMObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 || !strings.Contains(msg.msgs[0], "boom") {
		t.Fatalf("want SCM CI nudge with log tail, got %v", msg.msgs)
	}
}

func TestSCMObservation_MissingSessionIsIgnored(t *testing.T) {
	st := newFakeStore()
	m := New(st, nil)
	o := ports.SCMObservation{
		Fetched: true,
		PR:      ports.SCMPRObservation{URL: "pr1", Number: 1},
	}
	if err := m.ApplySCMObservation(ctx, "missing-1", o); err != nil {
		t.Fatalf("ApplySCMObservation missing session: %v", err)
	}
}

func TestSCMObservationUsesPRHeadWhenCIHeadMissing(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	o := ports.SCMObservation{
		Fetched: true,
		PR:      ports.SCMPRObservation{URL: "pr1", HeadSHA: "c1"},
		CI: ports.SCMCIObservation{
			Summary: string(domain.CIFailing),
			FailedChecks: []ports.SCMCheckObservation{{
				Name: "build", Status: string(domain.PRCheckFailed),
			}},
		},
	}
	if err := m.ApplySCMObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	o.PR.HeadSHA = "c2"
	if err := m.ApplySCMObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 2 {
		t.Fatalf("want separate CI nudges for distinct PR heads when CI head is absent, got %d: %v", len(msg.msgs), msg.msgs)
	}
}

func TestPRObservation_MergeConflictNudgesAgent(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	o := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 || !strings.Contains(msg.msgs[0], "merge conflicts") {
		t.Fatalf("want merge-conflict nudge, got %v", msg.msgs)
	}
}

// TestPRObservation_MergeConflictReArmsAfterConflictClears is the regression
// test for #4528: AO notified on the first conflict but never again, because
// the "merge-conflict:<url>" = "conflicting" signature sendOnce persists was
// never cleared. A PR rebased clean and then made conflicting again by a
// base-branch advance was silently swallowed as a duplicate.
func TestPRObservation_MergeConflictReArmsAfterConflictClears(t *testing.T) {
	// Every state that definitively rules a conflict out must re-arm; `blocked`
	// is deliberately excluded because the observer synthesizes it from
	// draft/CI/review facts even while provider mergeability is unknown.
	for _, clear := range []domain.Mergeability{domain.MergeMergeable, domain.MergeUnstable} {
		t.Run(string(clear), func(t *testing.T) {
			m, st, msg := newManager()
			st.sessions["mer-1"] = working("mer-1")
			apply := func(state domain.Mergeability) {
				t.Helper()
				if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: state}); err != nil {
					t.Fatal(err)
				}
			}

			apply(clear)
			apply(domain.MergeConflicting)
			if len(msg.msgs) != 1 {
				t.Fatalf("first conflict should nudge once, got %v", msg.msgs)
			}
			apply(clear)
			apply(domain.MergeConflicting)
			if len(msg.msgs) != 2 {
				t.Fatalf("conflict returning after %s should nudge again, got %d nudges: %v", clear, len(msg.msgs), msg.msgs)
			}
		})
	}
}

// TestPRObservation_ConsecutiveMergeConflictsStayDeduplicated pins the other
// half of #4528: the re-arm must not weaken the dedup that stops an unchanged
// conflict from re-nudging on every poll.
func TestPRObservation_ConsecutiveMergeConflictsStayDeduplicated(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	for range 3 {
		if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}); err != nil {
			t.Fatal(err)
		}
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("identical consecutive conflicts should nudge once, got %d: %v", len(msg.msgs), msg.msgs)
	}
}

// TestPRObservation_UnknownMergeabilityDoesNotReArm keeps the re-arm off the
// transient GitHub reports while it recomputes mergeability after a push or a
// retarget. Treating unknown as "conflict resolved" would re-nudge on every
// unknown → conflicting flap of a conflict that never went away.
func TestPRObservation_UnknownMergeabilityDoesNotReArm(t *testing.T) {
	for _, transient := range []domain.Mergeability{domain.MergeUnknown, domain.MergeBlocked, ""} {
		t.Run(string(transient), func(t *testing.T) {
			m, st, msg := newManager()
			st.sessions["mer-1"] = working("mer-1")
			for _, state := range []domain.Mergeability{domain.MergeConflicting, transient, domain.MergeConflicting} {
				if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: state}); err != nil {
					t.Fatal(err)
				}
			}
			if len(msg.msgs) != 1 {
				t.Fatalf("%q must not re-arm the conflict nudge, got %d nudges: %v", transient, len(msg.msgs), msg.msgs)
			}
		})
	}
}

// TestPRObservation_MergeConflictDedupSurvivesRestart covers both persistence
// directions of #4528 across a daemon restart, simulated by a fresh Manager
// over the same store: an unchanged conflict must stay quiet, and a conflict
// that cleared before the restart must be free to nudge again.
func TestPRObservation_MergeConflictDedupSurvivesRestart(t *testing.T) {
	conflicting := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}

	t.Run("unchanged conflict stays deduplicated", func(t *testing.T) {
		st := newFakeStore()
		st.sessions["mer-1"] = working("mer-1")
		first := New(st, &fakeMessenger{})
		if err := first.ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
			t.Fatal(err)
		}

		restarted := &fakeMessenger{}
		if err := New(st, restarted).ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
			t.Fatal(err)
		}
		if len(restarted.msgs) != 0 {
			t.Fatalf("restart must not replay an already-sent conflict nudge, got %v", restarted.msgs)
		}
	})

	t.Run("cleared conflict re-arms across restart", func(t *testing.T) {
		st := newFakeStore()
		st.sessions["mer-1"] = working("mer-1")
		first := New(st, &fakeMessenger{})
		if err := first.ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
			t.Fatal(err)
		}
		if err := first.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeMergeable}); err != nil {
			t.Fatal(err)
		}

		restarted := &fakeMessenger{}
		if err := New(st, restarted).ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
			t.Fatal(err)
		}
		if len(restarted.msgs) != 1 {
			t.Fatalf("a conflict returning after a restart should nudge, got %v", restarted.msgs)
		}
	})
}

// TestPRObservation_ReArmLeavesOtherDedupEntriesIntact guards the blast radius
// of the re-arm: it must delete only this PR's merge-conflict key, so a
// resolved conflict cannot replay the CI-failure nudge the same PR row holds.
func TestPRObservation_ReArmLeavesOtherDedupEntriesIntact(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	o := ports.PRObservation{
		Fetched:      true,
		URL:          "pr1",
		CI:           domain.CIFailing,
		Checks:       []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}},
		Mergeability: domain.MergeConflicting,
	}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 2 {
		t.Fatalf("want a CI and a merge-conflict nudge, got %d: %v", len(msg.msgs), msg.msgs)
	}

	o.Mergeability = domain.MergeMergeable
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 2 {
		t.Fatalf("re-arming the conflict must not replay the unchanged CI nudge, got %d: %v", len(msg.msgs), msg.msgs)
	}
}

// TestPRObservation_ReArmSkipsStoreWriteWhenNothingArmed keeps a steadily
// mergeable PR off the write path: the re-arm persists only when it actually
// cleared an entry, so routine polls do not rewrite an unchanged payload.
func TestPRObservation_ReArmSkipsStoreWriteWhenNothingArmed(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = working("mer-1")
	for range 2 {
		if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeMergeable}); err != nil {
			t.Fatal(err)
		}
	}
	if st.signatureWrites != 0 {
		t.Fatalf("mergeable polls with nothing armed wrote the signature payload %d times, want 0", st.signatureWrites)
	}
}

// TestPRObservation_ReArmRetriesAfterFailedPersist covers the failure ->
// successful observation -> restart -> conflict path. The re-arm's in-memory
// delete stands even when the durable write fails, so a naive implementation
// would see nothing armed on the next mergeable observation, take the early
// return, and never retry — leaving the stale "conflicting" signature on disk
// to suppress the nudge after a restart, recreating #4528.
func TestPRObservation_ReArmRetriesAfterFailedPersist(t *testing.T) {
	conflicting := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}
	mergeable := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeMergeable}

	st := newFakeStore()
	st.sessions["mer-1"] = working("mer-1")
	m := New(st, &fakeMessenger{})
	if err := m.ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
		t.Fatal(err)
	}

	st.signatureWriteErr = errors.New("db locked")
	if err := m.ApplyPRObservation(ctx, "mer-1", mergeable); err == nil {
		t.Fatal("a failed re-arm persist should surface to the observer")
	}

	// Persistence recovers. The next non-conflicting observation must retry the
	// durable write rather than short-circuit on the now-empty in-memory maps.
	st.signatureWriteErr = nil
	if err := m.ApplyPRObservation(ctx, "mer-1", mergeable); err != nil {
		t.Fatal(err)
	}

	restarted := &fakeMessenger{}
	if err := New(st, restarted).ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
		t.Fatal(err)
	}
	if len(restarted.msgs) != 1 {
		t.Fatalf("a re-arm that retried its persist must survive a restart, got %v", restarted.msgs)
	}
}

// TestPRObservation_ReArmStillNudgesInProcessAfterFailedPersist pins the other
// half of the retry contract: the failed persist must not roll the in-memory
// re-arm back, or a conflict returning before the write succeeds would be
// deduplicated away inside the running daemon.
func TestPRObservation_ReArmStillNudgesInProcessAfterFailedPersist(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	conflicting := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}
	if err := m.ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
		t.Fatal(err)
	}

	st.signatureWriteErr = errors.New("db locked")
	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeMergeable}); err == nil {
		t.Fatal("a failed re-arm persist should surface to the observer")
	}
	st.signatureWriteErr = nil

	if err := m.ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 2 {
		t.Fatalf("a conflict returning after a failed re-arm persist should still nudge, got %d: %v", len(msg.msgs), msg.msgs)
	}
}

// TestPRObservation_ReArmStoreFailureSurfacesWithoutLosingNudges checks the
// deferred-error contract: a failed re-arm persist is reported, but only after
// the independent CI nudge queued in the same observation has been sent.
func TestPRObservation_ReArmStoreFailureSurfacesWithoutLosingNudges(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}); err != nil {
		t.Fatal(err)
	}

	st.signatureWriteErr = errors.New("db locked")
	err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{
		Fetched:      true,
		URL:          "pr1",
		CI:           domain.CIFailing,
		Checks:       []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}},
		Mergeability: domain.MergeMergeable,
	})
	if err == nil {
		t.Fatal("a failed re-arm persist should surface to the observer")
	}
	if len(msg.msgs) != 2 {
		t.Fatalf("the CI nudge must still be sent before the deferred re-arm error, got %d: %v", len(msg.msgs), msg.msgs)
	}
}

func TestPRObservation_ExitedAgentIsNotNudged(t *testing.T) {
	m, st, msg := newManager()
	rec := working("mer-1")
	rec.Activity.State = domain.ActivityExited
	st.sessions["mer-1"] = rec

	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("exited agent should not receive reaction nudges, got %v", msg.msgs)
	}
}

// TestPRObservation_ReArmClearsConflictWhileSessionExited is the regression
// test for the delivery-gate finding: a definitively-cleared observation must
// re-arm the merge-conflict dedup even when it arrives while the session is
// exited. Exited sessions are still polled, so the sequence
// conflicting -> exit -> mergeable -> restore -> conflicting must nudge on the
// final conflict; a re-arm placed behind the dead-session return would skip the
// clear and let the stale "conflicting" signature swallow the recurrence.
func TestPRObservation_ReArmClearsConflictWhileSessionExited(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	conflicting := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}
	mergeable := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeMergeable}

	if err := m.ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("first conflict should nudge, got %v", msg.msgs)
	}

	// The agent exits, then the PR is observed mergeable while exited. No nudge
	// is delivered (nowhere to write), but the re-arm bookkeeping must still run.
	exited := working("mer-1")
	exited.Activity.State = domain.ActivityExited
	st.sessions["mer-1"] = exited
	if err := m.ApplyPRObservation(ctx, "mer-1", mergeable); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("mergeable-while-exited must deliver nothing, got %v", msg.msgs)
	}

	// The agent is restored and the base advances into a fresh conflict. The
	// re-arm during the exited window must let this recurrence nudge again.
	st.sessions["mer-1"] = working("mer-1")
	if err := m.ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 2 {
		t.Fatalf("a conflict returning after a clear-while-exited must nudge again, got %d: %v", len(msg.msgs), msg.msgs)
	}
}

// TestPRObservation_ReArmClearsConflictSurvivesTerminatedRestore covers the
// terminated variant of the same hazard: a terminated session is excluded from
// polling, so the clear typically arrives on the first observation after
// restore. The re-arm still runs ahead of the dead-session return, so even a
// clear observed in the terminated window (before restore is reflected) drops
// the stale signature and a later conflict nudges again.
func TestPRObservation_ReArmClearsConflictSurvivesTerminatedRestore(t *testing.T) {
	conflicting := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}
	mergeable := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeMergeable}

	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	if err := m.ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
		t.Fatal(err)
	}

	// Session terminates; a mergeable observation lands before restore. It must
	// re-arm despite the terminated gate returning before any delivery.
	terminated := working("mer-1")
	terminated.IsTerminated = true
	st.sessions["mer-1"] = terminated
	if err := m.ApplyPRObservation(ctx, "mer-1", mergeable); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("mergeable-while-terminated must deliver nothing, got %v", msg.msgs)
	}

	// Restored, then a fresh conflict: the re-arm during the terminated window
	// must let it nudge again rather than dedup against the stale signature.
	st.sessions["mer-1"] = working("mer-1")
	if err := m.ApplyPRObservation(ctx, "mer-1", conflicting); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 2 {
		t.Fatalf("a conflict after a clear-while-terminated must nudge again, got %d: %v", len(msg.msgs), msg.msgs)
	}
}

// TestPRObservation_MergeConflictReachesNeedsInputSession is the regression
// test for #2173: a session parked awaiting the human (waiting_input) must
// still receive a merge-conflict nudge, because the human sitting at that
// prompt may be exactly who needs to act (rebase it themselves, or redirect
// the agent) — unlike CI-failure/review-feedback nudges, which only the agent
// can act on and which correctly wait for it to resume (see the sibling test
// below). Two states are still refused: a session blocked on a live permission
// dialog, and a waiting_input session on a harness that cannot prove that
// prompt is a genuine idle composer rather than a masked permission decision
// (codex maps permission-request to waiting_input) — the harness-aware gate the
// urgent route consults, so an unsolicited paste never answers a hidden dialog.
func TestPRObservation_MergeConflictReachesNeedsInputSession(t *testing.T) {
	const safeHarness = domain.AgentHarness("claude-code")
	const ambiguousHarness = domain.AgentHarness("codex")
	urgentGate := func(h domain.AgentHarness) bool { return h == safeHarness }
	cases := []struct {
		name      string
		state     domain.ActivityState
		harness   domain.AgentHarness
		wantNudge bool
	}{
		{"waiting_input on a blocked-signalling harness reaches the agent", domain.ActivityWaitingInput, safeHarness, true},
		{"waiting_input on an ambiguous harness stays suppressed", domain.ActivityWaitingInput, ambiguousHarness, false},
		{"blocked stays suppressed", domain.ActivityBlocked, safeHarness, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, st, msg := newManager(WithUrgentNudgeGate(urgentGate))
			rec := working("mer-1")
			rec.Activity.State = tc.state
			rec.Harness = tc.harness
			st.sessions["mer-1"] = rec

			o := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}
			if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
				t.Fatal(err)
			}
			gotNudge := len(msg.msgs) == 1
			if gotNudge != tc.wantNudge {
				t.Fatalf("nudge sent = %v (msgs=%v), want %v", gotNudge, msg.msgs, tc.wantNudge)
			}
			if gotNudge && !strings.Contains(msg.msgs[0], "merge conflicts") {
				t.Fatalf("want merge-conflict nudge, got %q", msg.msgs[0])
			}
		})
	}
}

// TestPRObservation_NeedsInputSessionStillWithholdsOtherNudges is the
// regression guard: the needs-input gate must keep suppressing CI-failure and
// review-feedback nudges exactly as before. Only the merge-conflict nudge
// gets the carve-out in TestPRObservation_MergeConflictReachesNeedsInputSession.
func TestPRObservation_NeedsInputSessionStillWithholdsOtherNudges(t *testing.T) {
	for _, state := range []domain.ActivityState{domain.ActivityWaitingInput, domain.ActivityBlocked} {
		t.Run(string(state), func(t *testing.T) {
			m, st, msg := newManager()
			rec := working("mer-1")
			rec.Activity.State = state
			st.sessions["mer-1"] = rec
			st.comments["pr1"] = []domain.PullRequestComment{{ID: "1", Author: "alice", Body: "fix this", AutoInjectReview: true}}

			o := ports.PRObservation{
				Fetched: true,
				URL:     "pr1",
				CI:      domain.CIFailing,
				Checks:  []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}},
				Review:  domain.ReviewChangesRequest,
			}
			if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
				t.Fatal(err)
			}
			if len(msg.msgs) != 0 {
				t.Fatalf("needs-input session should not receive CI/review nudges, got %v", msg.msgs)
			}
		})
	}
}

// TestPRObservation_DeadSessionGetsNoNudgesOfAnyKind sanity-checks that the
// merge-conflict carve-out did not overreach: a genuinely dead session
// (terminated, or its pane already exited to a shell) still gets nothing at
// all, conflict included, since there is nowhere to deliver it.
func TestPRObservation_DeadSessionGetsNoNudgesOfAnyKind(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*domain.SessionRecord)
	}{
		{"terminated", func(r *domain.SessionRecord) { r.IsTerminated = true }},
		{"exited", func(r *domain.SessionRecord) { r.Activity.State = domain.ActivityExited }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, st, msg := newManager()
			rec := working("mer-1")
			tc.mut(&rec)
			st.sessions["mer-1"] = rec
			st.comments["pr1"] = []domain.PullRequestComment{{ID: "1", Author: "alice", Body: "fix this", AutoInjectReview: true}}

			o := ports.PRObservation{
				Fetched:      true,
				URL:          "pr1",
				CI:           domain.CIFailing,
				Checks:       []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}},
				Review:       domain.ReviewChangesRequest,
				Mergeability: domain.MergeConflicting,
			}
			if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
				t.Fatal(err)
			}
			if len(msg.msgs) != 0 {
				t.Fatalf("%s session should receive no nudges at all, including for a conflict, got %v", tc.name, msg.msgs)
			}
		})
	}
}

func TestPRObservation_NudgeIncludesPRIdentity(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	o := ports.PRObservation{
		Fetched:      true,
		URL:          "https://github.com/o/r/pull/7",
		Number:       7,
		Title:        "Add auth",
		SourceBranch: "feat/x/auth",
		TargetBranch: "feat/x",
		CI:           domain.CIFailing,
		Checks:       []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}},
	}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("want one CI nudge, got %d: %v", len(msg.msgs), msg.msgs)
	}
	got := msg.msgs[0]
	if !strings.Contains(got, `PR #7 "Add auth" (feat/x/auth → feat/x)`) {
		t.Fatalf("nudge missing PR identity: %q", got)
	}
	if !strings.Contains(got, "PR: https://github.com/o/r/pull/7") {
		t.Fatalf("nudge missing PR URL: %q", got)
	}
}

func TestPRObservation_MergedStaysLiveWhenAutoTerminateDisabled(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.prs["mer-1"] = []domain.PullRequest{{URL: "pr1", Merged: true}}
	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Merged: true}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if got.IsTerminated || got.Activity.State == domain.ActivityExited {
		t.Fatalf("merged PR should stay live when auto-terminate is disabled, got %+v", got)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("merged PR should not send nudge, got %v", msg.msgs)
	}
}

func TestPRObservation_MergedUsesConfiguredTerminator(t *testing.T) {
	m, st, _ := newManager()
	terminator := &fakeCompletionTerminator{}
	m.SetCompletionTerminator(terminator)
	rec := working("mer-1")
	rec.TerminateOnPRMerge = true
	st.sessions["mer-1"] = rec
	st.prs["mer-1"] = []domain.PullRequest{{URL: "pr1", Merged: true}}

	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Merged: true}); err != nil {
		t.Fatal(err)
	}
	if terminator.calls != 1 {
		t.Fatalf("terminator calls = %d, want 1", terminator.calls)
	}
}

func TestPRObservation_MergedTerminationIsSuppressedDuringSessionMutation(t *testing.T) {
	m, st, _ := newManager()
	terminator := &fakeCompletionTerminator{}
	m.SetCompletionTerminator(terminator)
	m.SetSessionOperationGate(fixedSessionOperationGate(true))
	rec := working("mer-1")
	rec.TerminateOnPRMerge = true
	st.sessions["mer-1"] = rec
	st.prs["mer-1"] = []domain.PullRequest{{URL: "pr1", Merged: true}}

	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Merged: true}); err != nil {
		t.Fatal(err)
	}
	if terminator.calls != 0 {
		t.Fatalf("terminator calls = %d, want 0 during session mutation", terminator.calls)
	}
}

func TestPRObservation_MergedTeardownFailureStaysLiveForRetry(t *testing.T) {
	m, st, _ := newManager()
	terminator := &fakeCompletionTerminator{err: errors.New("transient teardown failure")}
	m.SetCompletionTerminator(terminator)
	rec := working("mer-1")
	rec.TerminateOnPRMerge = true
	st.sessions["mer-1"] = rec
	st.prs["mer-1"] = []domain.PullRequest{{URL: "pr1", Merged: true}}

	err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Merged: true})
	if err == nil || !strings.Contains(err.Error(), "transient teardown failure") {
		t.Fatalf("ApplyPRObservation err = %v, want teardown failure", err)
	}
	if st.sessions["mer-1"].IsTerminated {
		t.Fatalf("failed teardown must not hide a live session: %+v", st.sessions["mer-1"])
	}
}

func TestPRObservation_MergedRequiresConfiguredTerminator(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.TerminateOnPRMerge = true
	st.sessions["mer-1"] = rec
	st.prs["mer-1"] = []domain.PullRequest{{URL: "pr1", Merged: true}}

	err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Merged: true})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("ApplyPRObservation err = %v, want configuration error", err)
	}
	if st.sessions["mer-1"].IsTerminated {
		t.Fatalf("missing terminator must not mark the session terminated: %+v", st.sessions["mer-1"])
	}
}

// A session with one merged PR and one still-open PR must NOT terminate: the
// completion bar is "no open PR remains AND at least one merged".
func TestPRObservation_MergedWithOpenSiblingDoesNotTerminate(t *testing.T) {
	m, st, _ := newManager()
	terminator := &fakeCompletionTerminator{}
	m.SetCompletionTerminator(terminator)
	rec := working("mer-1")
	rec.TerminateOnPRMerge = true
	st.sessions["mer-1"] = rec
	st.prs["mer-1"] = []domain.PullRequest{
		{URL: "pr1", Merged: true},
		{URL: "pr2"},
	}
	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Merged: true}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"]; got.IsTerminated {
		t.Fatalf("session with an open sibling PR must stay alive, got %+v", got)
	}
	if terminator.calls != 0 {
		t.Fatalf("terminator calls = %d, want 0", terminator.calls)
	}
}

// Once the last open PR merges (all PRs now merged), the session terminates.
func TestPRObservation_LastMergeTerminatesSession(t *testing.T) {
	m, st, _ := newManager()
	terminator := &fakeCompletionTerminator{}
	m.SetCompletionTerminator(terminator)
	rec := working("mer-1")
	rec.TerminateOnPRMerge = true
	st.sessions["mer-1"] = rec
	st.prs["mer-1"] = []domain.PullRequest{
		{URL: "pr1", Merged: true},
		{URL: "pr2", Merged: true},
	}
	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr2", Merged: true}); err != nil {
		t.Fatal(err)
	}
	if terminator.calls != 1 {
		t.Fatalf("terminator calls = %d, want 1", terminator.calls)
	}
}

// A closed PR that leaves the session with an open sibling and no merge does not
// terminate; closing the last PR with no merge also does not terminate (nothing
// shipped).
func TestPRObservation_ClosedWithoutMergeDoesNotTerminate(t *testing.T) {
	m, st, _ := newManager()
	terminator := &fakeCompletionTerminator{}
	m.SetCompletionTerminator(terminator)
	rec := working("mer-1")
	rec.TerminateOnPRMerge = true
	st.sessions["mer-1"] = rec
	st.prs["mer-1"] = []domain.PullRequest{{URL: "pr1", Closed: true}}
	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1", Closed: true}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"]; got.IsTerminated {
		t.Fatalf("a closed-without-merge PR must not terminate the session, got %+v", got)
	}
	if terminator.calls != 0 {
		t.Fatalf("terminator calls = %d, want 0", terminator.calls)
	}
}

// A PR stacked on an open parent (its target branch is the parent's source
// branch) is exempt from the merge-conflict nudge: conflicts there are expected
// until the parent merges.
func TestPRObservation_StackedChildConflictSuppressed(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.prs["mer-1"] = []domain.PullRequest{
		{URL: "parent", SourceBranch: "ao/x", TargetBranch: "main"},
		{URL: "child", SourceBranch: "ao/x/auth", TargetBranch: "ao/x"},
	}
	o := ports.PRObservation{Fetched: true, URL: "child", Mergeability: domain.MergeConflicting}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("stacked child conflict should be suppressed, got %v", msg.msgs)
	}
}

// The bottom-of-stack PR (not stacked on any open parent) still gets the
// merge-conflict nudge even when it has open stacked children.
func TestPRObservation_BottomOfStackConflictNudges(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.prs["mer-1"] = []domain.PullRequest{
		{URL: "parent", SourceBranch: "ao/x", TargetBranch: "main"},
		{URL: "child", SourceBranch: "ao/x/auth", TargetBranch: "ao/x"},
	}
	o := ports.PRObservation{Fetched: true, URL: "parent", Mergeability: domain.MergeConflicting}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 || !strings.Contains(msg.msgs[0], "merge conflicts") {
		t.Fatalf("bottom-of-stack conflict should nudge, got %v", msg.msgs)
	}
}

// TestPRObservation_DedupSurvivesManagerRestart simulates a daemon restart by
// constructing a second Manager over the same store and asserts that an
// identical PR observation does not re-fire the nudge — the dedup signature
// must survive process restart, not just live in the Manager's maps.
func TestPRObservation_DedupSurvivesManagerRestart(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = working("mer-1")

	o := ports.PRObservation{
		Fetched: true,
		URL:     "https://github.com/o/r/pull/1",
		CI:      domain.CIFailing,
		Checks:  []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}},
	}

	first := &fakeMessenger{}
	m1 := New(st, first)
	if err := m1.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatalf("first ApplyPRObservation: %v", err)
	}
	if len(first.msgs) != 1 {
		t.Fatalf("first manager: want 1 nudge, got %d", len(first.msgs))
	}
	if got := st.signatures[o.URL]; got == "" {
		t.Fatalf("signature was not persisted; want a non-empty JSON payload for %q", o.URL)
	}

	// Simulate daemon restart: the second Manager has no in-memory state but
	// shares the same store, so it should hydrate seen/attempts from the
	// persisted payload and suppress the re-send.
	second := &fakeMessenger{}
	m2 := New(st, second)
	if err := m2.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatalf("second ApplyPRObservation: %v", err)
	}
	if len(second.msgs) != 0 {
		t.Fatalf("post-restart manager re-nudged on identical observation, got %d msgs: %v", len(second.msgs), second.msgs)
	}

	// And a genuinely new signature (different log tail) still fires — proving
	// the persisted state is per-signature, not a blanket "this PR was nudged".
	o2 := o
	o2.Checks = []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "different boom"}}
	if err := m2.ApplyPRObservation(ctx, "mer-1", o2); err != nil {
		t.Fatalf("third ApplyPRObservation: %v", err)
	}
	if len(second.msgs) != 1 {
		t.Fatalf("new signature should send, got %d msgs", len(second.msgs))
	}
}

func TestPRObservation_DedupPersistsAcrossPRs(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = working("mer-1")
	msg := &fakeMessenger{}
	m := New(st, msg)

	for _, url := range []string{"https://github.com/o/r/pull/1", "https://github.com/o/r/pull/2"} {
		o := ports.PRObservation{
			Fetched: true, URL: url, CI: domain.CIFailing,
			Checks: []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}},
		}
		if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
			t.Fatalf("ApplyPRObservation for %s: %v", url, err)
		}
	}
	if len(msg.msgs) != 2 {
		t.Fatalf("distinct PRs should each get one nudge, got %d", len(msg.msgs))
	}
	if _, ok := st.signatures["https://github.com/o/r/pull/1"]; !ok {
		t.Fatal("missing persisted signature for PR 1")
	}
	if _, ok := st.signatures["https://github.com/o/r/pull/2"]; !ok {
		t.Fatal("missing persisted signature for PR 2")
	}
}

func TestApplyReviewBatchSuppressedByJITGuardIsNotDelivered(t *testing.T) {
	// The worker is working at ApplyReviewBatch's entry guard (read #1) but a
	// permission dialog stores blocked before sendOnce's just-in-time re-read
	// (read #2). The nudge must be SUPPRESSED, and the outcome must be
	// ReviewDeliveryNoop — NOT Sent — so the caller does not stamp the run
	// delivered and the changes-requested feedback re-fires once unblocked.
	st := newFakeStore()
	st.sessions["mer-1"] = working("mer-1")
	bst := &blockOnNthGetStore{fakeStore: st, id: "mer-1", flipAt: 2}
	msg := &fakeMessenger{}
	m := New(bst, msg)
	result := ReviewResult{
		RunID: "run-1", BatchID: "batch-1", WorkerID: "mer-1", PRURL: "https://github.com/o/r/pull/1",
		TargetSHA: "sha1", Verdict: domain.VerdictChangesRequested, Body: "fix the bug",
	}

	outcome, err := m.ApplyReviewBatch(ctx, "mer-1", "batch-1", []ReviewResult{result})
	if err != nil {
		t.Fatalf("ApplyReviewBatch: %v", err)
	}
	if outcome != ReviewDeliveryNoop {
		t.Fatalf("outcome = %q, want no_op (suppressed nudge must not be stamped delivered)", outcome)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("nudge pasted into a session that went blocked before send: %v", msg.msgs)
	}
	if st.signatures[result.PRURL] != "" {
		t.Fatal("suppressed nudge must not persist a sendOnce signature (it re-fires next observation)")
	}
}

func TestApplyReviewBatchSendsCombinedAndDedups(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = working("mer-1")
	msg := &fakeMessenger{}
	m := New(st, msg)
	results := []ReviewResult{
		{RunID: "run-2", BatchID: "batch-1", WorkerID: "mer-1", PRURL: "https://github.com/o/r/pull/2", TargetSHA: "sha2", Verdict: domain.VerdictChangesRequested, Body: "fix tests", GithubReviewID: "102"},
		{RunID: "run-1", BatchID: "batch-1", WorkerID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Verdict: domain.VerdictChangesRequested, Body: "fix auth", GithubReviewID: "101"},
	}

	outcome, err := m.ApplyReviewBatch(ctx, "mer-1", "batch-1", results)
	if err != nil {
		t.Fatalf("ApplyReviewBatch: %v", err)
	}
	if outcome != ReviewDeliverySent || len(msg.msgs) != 1 {
		t.Fatalf("outcome/messages = %q/%v, want sent once", outcome, msg.msgs)
	}
	got := msg.msgs[0]
	for _, want := range []string{
		"submitted 2 review(s) requesting changes",
		"PR: https://github.com/o/r/pull/1",
		"GitHub review: 101",
		"Review body:\nfix auth",
		"PR: https://github.com/o/r/pull/2",
		"GitHub review: 102",
		"Review body:\nfix tests",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("batch nudge missing %q: %q", want, got)
		}
	}
	if st.signatures["https://github.com/o/r/pull/1"] == "" {
		t.Fatal("batch nudge did not persist signature on anchor PR")
	}

	outcome, err = m.ApplyReviewBatch(ctx, "mer-1", "batch-1", results)
	if err != nil {
		t.Fatalf("repeat ApplyReviewBatch: %v", err)
	}
	if outcome != ReviewDeliverySent || len(msg.msgs) != 1 {
		t.Fatalf("repeat should suppress duplicate send, outcome=%q msgs=%v", outcome, msg.msgs)
	}
}

func TestApplyReviewBatchNoopsWithoutDeliverableResults(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = working("mer-1")
	msg := &fakeMessenger{}
	m := New(st, msg)

	outcome, err := m.ApplyReviewBatch(ctx, "mer-1", "batch-1", nil)
	if err != nil {
		t.Fatalf("ApplyReviewBatch: %v", err)
	}
	if outcome != ReviewDeliveryNoop || len(msg.msgs) != 0 || st.signatureWrites != 0 {
		t.Fatalf("empty batch should no-op, outcome=%q msgs=%v signatureWrites=%d", outcome, msg.msgs, st.signatureWrites)
	}
}

func TestApplyReviewBatchNoopsWhenWorkerCannotBeNudged(t *testing.T) {
	tests := []struct {
		name   string
		result ReviewResult
		rec    domain.SessionRecord
	}{
		{
			name:   "terminated worker",
			result: ReviewResult{RunID: "run-1", PRURL: "pr1", Verdict: domain.VerdictChangesRequested},
			rec:    func() domain.SessionRecord { r := working("mer-1"); r.IsTerminated = true; return r }(),
		},
		{
			name:   "worker waiting input",
			result: ReviewResult{RunID: "run-1", PRURL: "pr1", Verdict: domain.VerdictChangesRequested},
			rec: func() domain.SessionRecord {
				r := working("mer-1")
				r.Activity.State = domain.ActivityWaitingInput
				return r
			}(),
		},
		{
			name:   "worker agent exited",
			result: ReviewResult{RunID: "run-1", PRURL: "pr1", Verdict: domain.VerdictChangesRequested},
			rec:    func() domain.SessionRecord { r := working("mer-1"); r.Activity.State = domain.ActivityExited; return r }(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, st, msg := newManager()
			st.sessions["mer-1"] = tt.rec
			outcome, err := m.ApplyReviewBatch(ctx, "mer-1", "batch-1", []ReviewResult{tt.result})
			if err != nil {
				t.Fatalf("ApplyReviewBatch: %v", err)
			}
			if outcome != ReviewDeliveryNoop || len(msg.msgs) != 0 || st.signatureWrites != 0 {
				t.Fatalf("non-nudgeable worker should no-op, outcome=%q msgs=%v signatureWrites=%d", outcome, msg.msgs, st.signatureWrites)
			}
		})
	}
}

func TestApplyTrackerFacts_TerminalStateMarksTerminated(t *testing.T) {
	for _, state := range []domain.NormalizedIssueState{domain.IssueDone, domain.IssueCancelled} {
		t.Run(string(state), func(t *testing.T) {
			m, st, msg := newManager()
			st.sessions["mer-1"] = working("mer-1")
			o := ports.TrackerObservation{
				Fetched: true,
				Issue:   ports.TrackerIssueObservation{URL: "https://github.com/o/r/issues/1", State: state},
			}
			if err := m.ApplyTrackerFacts(ctx, "mer-1", o); err != nil {
				t.Fatalf("ApplyTrackerFacts: %v", err)
			}
			got := st.sessions["mer-1"]
			if !got.IsTerminated || got.Activity.State != domain.ActivityExited {
				t.Fatalf("want terminated/exited for state %q, got %+v", state, got)
			}
			if len(msg.msgs) != 0 {
				t.Fatalf("terminal state should not nudge, got %v", msg.msgs)
			}
		})
	}
}

func TestApplyTrackerFacts_TerminalStateIsSuppressedDuringSessionMutation(t *testing.T) {
	m, st, _ := newManager()
	m.SetSessionOperationGate(fixedSessionOperationGate(true))
	rec := working("mer-1")
	st.sessions["mer-1"] = rec
	o := ports.TrackerObservation{
		Fetched: true,
		Issue:   ports.TrackerIssueObservation{URL: "https://github.com/o/r/issues/1", State: domain.IssueDone},
	}

	if err := m.ApplyTrackerFacts(ctx, "mer-1", o); err != nil {
		t.Fatalf("ApplyTrackerFacts: %v", err)
	}
	if got := st.sessions["mer-1"]; got != rec {
		t.Fatalf("tracker observation mutated session during exclusive operation: got %+v, want %+v", got, rec)
	}
}

func TestLifecycleNudgeUsesLateBoundSessionInputLease(t *testing.T) {
	m, st, msg := newManager()
	m.SetSessionInputLease(fixedLifecycleInputLease(false))
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", Activity: domain.Activity{State: domain.ActivityIdle}}

	outcome, err := m.sendOnce(ctx, "mer-1", "", "tracker-comment:1", "1", "review this", 0, false)
	if err != nil {
		t.Fatalf("sendOnce: %v", err)
	}
	if outcome != sendOnceSuppressed {
		t.Fatalf("sendOnce outcome = %v, want suppressed", outcome)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("lifecycle nudge bypassed closed input lease: %v", msg.msgs)
	}
}

func TestLifecycleNudgeStartupGateUsesAdapterCapability(t *testing.T) {
	tests := []struct {
		name     string
		harness  domain.AgentHarness
		gate     bool
		wantSent bool
	}{
		{name: "startup-signaling adapter is gated", harness: domain.HarnessCursor, gate: true, wantSent: false},
		{name: "hookless adapter remains deliverable", harness: domain.HarnessAider, gate: false, wantSent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeStore()
			msg := &fakeMessenger{}
			m := New(st, msg, WithStartupSignalGate(func(harness domain.AgentHarness) bool {
				return harness == tt.harness && tt.gate
			}))
			st.sessions["mer-1"] = domain.SessionRecord{
				ID: "mer-1", Harness: tt.harness, Mode: domain.SessionModeTUI,
				Activity: domain.Activity{State: domain.ActivityIdle},
			}

			outcome, err := m.sendOnce(ctx, "mer-1", "", "tracker-comment:1", "1", "review this", 0, false)
			if err != nil {
				t.Fatalf("sendOnce: %v", err)
			}
			if got := len(msg.msgs) == 1; got != tt.wantSent {
				t.Fatalf("sent = %v, want %v (outcome %v)", got, tt.wantSent, outcome)
			}
		})
	}
}

func TestApplyTrackerFacts_AssigneeChangedIsLogOnly(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	before := st.sessions["mer-1"]
	o := ports.TrackerObservation{
		Fetched: true,
		Issue:   ports.TrackerIssueObservation{URL: "https://github.com/o/r/issues/1", State: domain.IssueOpen, Assignee: "someone-else"},
		Changed: ports.TrackerChanged{Assignee: true},
	}
	if err := m.ApplyTrackerFacts(ctx, "mer-1", o); err != nil {
		t.Fatalf("ApplyTrackerFacts: %v", err)
	}
	if st.sessions["mer-1"] != before {
		t.Fatalf("assignee-only change must not mutate the session row, got %+v", st.sessions["mer-1"])
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("assignee-only change must not nudge, got %v", msg.msgs)
	}
}

func TestApplyTrackerFacts_NewBotCommentNudges(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	o := ports.TrackerObservation{
		Fetched: true,
		Issue:   ports.TrackerIssueObservation{URL: "https://github.com/o/r/issues/1", State: domain.IssueOpen},
		Comments: []ports.TrackerCommentObservation{
			{ID: "human-1", Author: "alice", Body: "human chime-in, must NOT nudge", IsBot: false},
			{ID: "bot-1", Author: "ci-bot[bot]", Body: "please rerun the migration", IsBot: true},
		},
		Changed: ports.TrackerChanged{Comments: true},
	}
	if err := m.ApplyTrackerFacts(ctx, "mer-1", o); err != nil {
		t.Fatalf("ApplyTrackerFacts: %v", err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("want one bot-mention nudge, got %d: %v", len(msg.msgs), msg.msgs)
	}
	if !strings.Contains(msg.msgs[0], "please rerun the migration") {
		t.Fatalf("nudge should include the bot comment body, got %q", msg.msgs[0])
	}
	if strings.Contains(msg.msgs[0], "human chime-in") {
		t.Fatalf("nudge must not include human comments, got %q", msg.msgs[0])
	}
}

func TestApplyTrackerFacts_NudgeSuppressedOnRepeat(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	o := ports.TrackerObservation{
		Fetched: true,
		Issue:   ports.TrackerIssueObservation{URL: "https://github.com/o/r/issues/1", State: domain.IssueOpen},
		Comments: []ports.TrackerCommentObservation{
			{ID: "bot-1", Author: "ci-bot[bot]", Body: "please rerun the migration", IsBot: true},
		},
		Changed: ports.TrackerChanged{Comments: true},
	}
	if err := m.ApplyTrackerFacts(ctx, "mer-1", o); err != nil {
		t.Fatalf("first ApplyTrackerFacts: %v", err)
	}
	if err := m.ApplyTrackerFacts(ctx, "mer-1", o); err != nil {
		t.Fatalf("second ApplyTrackerFacts: %v", err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("repeat observation must dedup; got %d nudges: %v", len(msg.msgs), msg.msgs)
	}

	// A genuinely new bot comment still fires.
	o.Comments = append(o.Comments, ports.TrackerCommentObservation{ID: "bot-2", Author: "ci-bot[bot]", Body: "now check the seed", IsBot: true})
	if err := m.ApplyTrackerFacts(ctx, "mer-1", o); err != nil {
		t.Fatalf("third ApplyTrackerFacts: %v", err)
	}
	if len(msg.msgs) != 2 {
		t.Fatalf("new bot comment id should re-fire, got %d: %v", len(msg.msgs), msg.msgs)
	}
}

func TestApplyTrackerFacts_BotCommentWithEmptyIDIsIgnored(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	// Bot comment lacks an ID — without one we cannot dedup, and the
	// zero-value signature collides with m.react.seen's empty default and
	// would silently suppress every future nudge for this issue. The
	// reducer must skip it entirely.
	o := ports.TrackerObservation{
		Fetched: true,
		Issue:   ports.TrackerIssueObservation{URL: "https://github.com/o/r/issues/1", State: domain.IssueOpen},
		Comments: []ports.TrackerCommentObservation{
			{ID: "", Author: "ci-bot[bot]", Body: "no id, must be skipped", IsBot: true},
		},
		Changed: ports.TrackerChanged{Comments: true},
	}
	if err := m.ApplyTrackerFacts(ctx, "mer-1", o); err != nil {
		t.Fatalf("ApplyTrackerFacts: %v", err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("bot comment with empty ID must not nudge, got %v", msg.msgs)
	}
	// A subsequent, properly-formed bot comment must still nudge — the
	// earlier empty-ID entry must not have polluted the dedup signature.
	o.Comments = []ports.TrackerCommentObservation{
		{ID: "bot-1", Author: "ci-bot[bot]", Body: "now with an id", IsBot: true},
	}
	if err := m.ApplyTrackerFacts(ctx, "mer-1", o); err != nil {
		t.Fatalf("second ApplyTrackerFacts: %v", err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("follow-up bot comment with real ID should nudge, got %d: %v", len(msg.msgs), msg.msgs)
	}
}

func TestApplyTrackerFacts_NotFetchedIsNoop(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	before := st.sessions["mer-1"]
	if err := m.ApplyTrackerFacts(ctx, "mer-1", ports.TrackerObservation{Fetched: false}); err != nil {
		t.Fatalf("ApplyTrackerFacts: %v", err)
	}
	if st.sessions["mer-1"] != before {
		t.Fatalf("not-fetched observation must not mutate state")
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("not-fetched observation must not nudge")
	}
}

func TestApplyTrackerFacts_TerminatedSessionDoesNotRefireOrNudge(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", IsTerminated: true, Activity: domain.Activity{State: domain.ActivityExited}}
	o := ports.TrackerObservation{
		Fetched: true,
		Issue:   ports.TrackerIssueObservation{URL: "https://github.com/o/r/issues/1", State: domain.IssueOpen},
		Comments: []ports.TrackerCommentObservation{
			{ID: "bot-1", Body: "x", IsBot: true},
		},
		Changed: ports.TrackerChanged{Comments: true},
	}
	if err := m.ApplyTrackerFacts(ctx, "mer-1", o); err != nil {
		t.Fatalf("ApplyTrackerFacts: %v", err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("terminated session must not receive nudges, got %v", msg.msgs)
	}
}

func TestPRObservation_RetriesAfterMessengerFailure(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	o := ports.PRObservation{Fetched: true, URL: "pr1", Mergeability: domain.MergeConflicting}
	msg.err = errors.New("temporary send failure")
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err == nil {
		t.Fatal("want send error")
	}
	msg.err = nil
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("want retry to send once, got %v", msg.msgs)
	}
}

func TestActivity_FirstSignalStampsReceipt(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()}}
	// A same-state repeat (idle on an idle-seeded row) must still write: the
	// receipt itself is the durable fact that clears no_signal.
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityIdle}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if got.FirstSignalAt.IsZero() {
		t.Fatalf("first signal not stamped: %+v", got)
	}
	stamped := got.FirstSignalAt
	// Later signals must not move the receipt.
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Timestamp: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"]; !got.FirstSignalAt.Equal(stamped) {
		t.Fatalf("first signal moved: %v -> %v", stamped, got.FirstSignalAt)
	}
}

func TestActivity_SameStateRepeatAfterReceiptIsNoOp(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.FirstSignalAt = time.Now()
	st.sessions["mer-1"] = rec
	before := st.sessions["mer-1"]
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive}); err != nil {
		t.Fatal(err)
	}
	if st.sessions["mer-1"] != before {
		t.Fatalf("same-state repeat after receipt must not rewrite: %+v", st.sessions["mer-1"])
	}
}

func TestMarkSpawnedClearsFirstSignalAndLeavesResumeIdentityUnverified(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.FirstSignalAt = time.Now().Add(-time.Hour)
	rec.Metadata.RuntimeLaunchID = "launch-old"
	rec.Metadata.AgentSessionID = "native-1"
	rec.Metadata.AgentSessionIDLaunchID = "launch-old"
	st.sessions["mer-1"] = rec
	if err := m.MarkSpawned(ctx, "mer-1", domain.SessionMetadata{RuntimeLaunchID: "launch-new"}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if !got.FirstSignalAt.IsZero() {
		t.Fatalf("spawn/restore must clear the receipt, got %+v", got)
	}
	if got.Metadata.AgentSessionID != "native-1" || got.Metadata.AgentSessionIDLaunchID != "launch-old" {
		t.Fatalf("spawn/restore must retain the resume hint without re-proving it, got %+v", got.Metadata)
	}
	if got.Metadata.AgentSessionIDLaunchID == got.Metadata.RuntimeLaunchID {
		t.Fatalf("new launch unexpectedly inherited native identity proof: %+v", got.Metadata)
	}
}

type fakeNotificationSink struct {
	intents     []ports.NotificationIntent
	resolutions []ports.NotificationResolution
	err         error
}

func (f *fakeNotificationSink) Notify(_ context.Context, intent ports.NotificationIntent) error {
	f.intents = append(f.intents, intent)
	return f.err
}

func (f *fakeNotificationSink) Resolve(_ context.Context, res ports.NotificationResolution) error {
	f.resolutions = append(f.resolutions, res)
	return f.err
}

func TestActivity_WaitingInputTransitionEmitsNotification(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", DisplayName: "checkout-flow", Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now.Add(-time.Minute)}, FirstSignalAt: now.Add(-time.Minute)}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityWaitingInput}); err != nil {
		t.Fatal(err)
	}
	if len(sink.intents) != 1 {
		t.Fatalf("intents = %d, want 1", len(sink.intents))
	}
	intent := sink.intents[0]
	if intent.Type != domain.NotificationNeedsInput || intent.SessionID != "mer-1" || intent.ProjectID != "mer" || intent.SessionDisplayName != "checkout-flow" {
		t.Fatalf("intent = %+v", intent)
	}
}

// The user answering the agent is what resolves a needs-input notification —
// there is no manual acknowledgement anywhere in the flow.
func TestActivity_LeavingNeedsInputResolvesNotification(t *testing.T) {
	for _, tt := range []struct {
		name string
		next domain.ActivityState
	}{
		{name: "answered", next: domain.ActivityActive},
		{name: "went idle", next: domain.ActivityIdle},
		{name: "agent exited", next: domain.ActivityExited},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeStore()
			sink := &fakeNotificationSink{}
			m := New(st, nil, WithNotificationSink(sink))
			now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
			m.clock = func() time.Time { return now }
			st.sessions["mer-1"] = domain.SessionRecord{
				ID: "mer-1", ProjectID: "mer",
				Activity:      domain.Activity{State: domain.ActivityWaitingInput, LastActivityAt: now.Add(-time.Minute)},
				FirstSignalAt: now.Add(-time.Minute),
			}

			if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: tt.next}); err != nil {
				t.Fatal(err)
			}
			if len(sink.resolutions) != 1 {
				t.Fatalf("resolutions = %+v, want 1", sink.resolutions)
			}
			got := sink.resolutions[0]
			if got.Type != domain.NotificationNeedsInput || got.SessionID != "mer-1" || !got.ResolvedAt.Equal(now) {
				t.Fatalf("resolution = %+v", got)
			}
		})
	}
}

// An in-family escalation is still the same pause: nothing was answered, so
// there is nothing to resolve.
func TestActivity_WaitingInputToBlockedDoesNotResolve(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Now()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer",
		Activity:      domain.Activity{State: domain.ActivityWaitingInput, LastActivityAt: now},
		FirstSignalAt: now,
	}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityBlocked}); err != nil {
		t.Fatal(err)
	}
	if len(sink.resolutions) != 0 {
		t.Fatalf("resolutions = %+v, want none", sink.resolutions)
	}
}

// Terminating a paused session also clears its ping: nobody is waiting on the
// user any more.
func TestMarkTerminated_ResolvesNeedsInputNotification(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Now()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer",
		Activity: domain.Activity{State: domain.ActivityBlocked, LastActivityAt: now},
	}

	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if len(sink.resolutions) != 1 || sink.resolutions[0].Type != domain.NotificationNeedsInput {
		t.Fatalf("resolutions = %+v", sink.resolutions)
	}
}

func TestActivity_WaitingInputSameStateDoesNotEmitNotification(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Now()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Activity: domain.Activity{State: domain.ActivityWaitingInput, LastActivityAt: now}, FirstSignalAt: now}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityWaitingInput}); err != nil {
		t.Fatal(err)
	}
	if len(sink.intents) != 0 {
		t.Fatalf("same-state waiting_input emitted %+v", sink.intents)
	}
}

func TestActivity_BlockedTransitionEmitsNotification(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", DisplayName: "checkout-flow", Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now.Add(-time.Minute)}, FirstSignalAt: now.Add(-time.Minute)}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityBlocked}); err != nil {
		t.Fatal(err)
	}
	if len(sink.intents) != 1 {
		t.Fatalf("intents = %d, want 1 (blocked is a needs-input entry)", len(sink.intents))
	}
	if sink.intents[0].Type != domain.NotificationNeedsInput {
		t.Fatalf("intent type = %q, want needs_input", sink.intents[0].Type)
	}
}

func TestActivity_WaitingInputToBlockedDoesNotReNotify(t *testing.T) {
	// waiting_input -> blocked is an in-family escalation: the user was already
	// pinged once for this pause, so no second notification and no telemetry
	// entry/exit pair.
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	tele := &telemetrySink{}
	m := New(st, nil, WithNotificationSink(sink), WithTelemetry(tele))
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Activity: domain.Activity{State: domain.ActivityWaitingInput, LastActivityAt: now.Add(-time.Minute)}, FirstSignalAt: now.Add(-time.Minute)}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityBlocked}); err != nil {
		t.Fatal(err)
	}
	if len(sink.intents) != 0 {
		t.Fatalf("in-family escalation emitted notification: %+v", sink.intents)
	}
	if len(tele.events) != 0 {
		t.Fatalf("in-family escalation emitted telemetry: %+v", tele.events)
	}
}

func TestActivity_BlockedEntryAndExitEmitTelemetry(t *testing.T) {
	st := newFakeStore()
	sink := &telemetrySink{}
	m := New(st, nil, WithTelemetry(sink))
	now := time.Unix(100, 0).UTC()
	m.clock = func() time.Time { return now }
	st.sessions["mer-1"] = domain.SessionRecord{
		ID:        "mer-1",
		ProjectID: "mer",
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)},
	}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityBlocked, Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Timestamp: now}); err != nil {
		t.Fatal(err)
	}

	if len(sink.events) != 2 {
		t.Fatalf("events = %#v, want entered/exited", sink.events)
	}
	if sink.events[0].Name != "ao.session.waiting_input_entered" || sink.events[1].Name != "ao.session.waiting_input_exited" {
		t.Fatalf("event names = %#v (family events keep the waiting_input_* names)", []string{sink.events[0].Name, sink.events[1].Name})
	}
	if got := sink.events[0].Payload["state"]; got != "blocked" {
		t.Fatalf("entered payload state = %#v, want blocked", got)
	}
}

func TestSCMObservation_ReadyToMergeSuppressedWhileBlocked(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	rec := working("mer-1")
	rec.Activity.State = domain.ActivityBlocked
	st.sessions["mer-1"] = rec
	obs := ports.SCMObservation{
		Fetched:      true,
		PR:           ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", Number: 1},
		CI:           ports.SCMCIObservation{Summary: string(domain.CIPassing)},
		Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable)},
	}
	if err := m.ApplySCMObservation(ctx, "mer-1", obs); err != nil {
		t.Fatal(err)
	}
	if len(sink.intents) != 0 {
		t.Fatalf("blocked session emitted ready notification: %+v", sink.intents)
	}
}

// blockOnNthGetStore wraps fakeStore and flips a session to ActivityBlocked on
// the Nth GetSession call, reproducing the reactions TOCTOU: the handler's
// entry guard (1st read) sees the session working, but a permission hook stores
// blocked before sendOnce's just-in-time re-read (2nd read).
type blockOnNthGetStore struct {
	*fakeStore
	id     domain.SessionID
	reads  int
	flipAt int
}

func (s *blockOnNthGetStore) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	s.reads++
	if s.reads == s.flipAt {
		if rec, ok := s.sessions[s.id]; ok {
			rec.Activity.State = domain.ActivityBlocked
			s.sessions[s.id] = rec
		}
	}
	return s.fakeStore.GetSession(ctx, id)
}

func TestSendOnce_NoNudgeWhenBlockedAppearsBeforeSend(t *testing.T) {
	// The entry guard in ApplyPRObservation reads the session working (read #1);
	// a permission dialog then stores blocked before sendOnce's just-in-time
	// re-read (read #2), which must suppress the paste+Enter into the dialog.
	st := newFakeStore()
	st.sessions["mer-1"] = working("mer-1")
	bst := &blockOnNthGetStore{fakeStore: st, id: "mer-1", flipAt: 2}
	msg := &fakeMessenger{}
	m := New(bst, msg)
	o := ports.PRObservation{Fetched: true, URL: "pr1", CI: domain.CIFailing, Checks: []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}}}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("nudge sent into a session that went blocked before send: %v", msg.msgs)
	}
}

func TestPRObservation_NudgesSuppressedWhileBlocked(t *testing.T) {
	// A blocked session must not receive automated CI/review nudges: injected
	// text could interact with the pending permission dialog.
	m, st, msg := newManager()
	rec := working("mer-1")
	rec.Activity.State = domain.ActivityBlocked
	st.sessions["mer-1"] = rec
	o := ports.PRObservation{Fetched: true, URL: "pr1", CI: domain.CIFailing, Checks: []ports.PRCheckObservation{{Name: "build", CommitHash: "c1", Status: domain.PRCheckFailed, LogTail: "boom"}}}
	if err := m.ApplyPRObservation(ctx, "mer-1", o); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("blocked session got nudged: %v", msg.msgs)
	}
}

func TestActivity_TerminatedSessionDoesNotEmitNotification(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", IsTerminated: true, Activity: domain.Activity{State: domain.ActivityExited}}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityWaitingInput}); err != nil {
		t.Fatal(err)
	}
	if len(sink.intents) != 0 {
		t.Fatalf("terminated session emitted %+v", sink.intents)
	}
}

func TestSCMObservation_Notifications(t *testing.T) {
	for _, tc := range []struct {
		name string
		obs  ports.SCMObservation
		want domain.NotificationType
	}{
		{
			name: "ready",
			obs:  ports.SCMObservation{Fetched: true, PR: ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", Number: 1, Title: "checkout"}, CI: ports.SCMCIObservation{Summary: string(domain.CIPassing)}, Review: ports.SCMReviewObservation{Decision: string(domain.ReviewApproved)}, Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable)}},
			want: domain.NotificationReadyToMerge,
		},
		{
			name: "merged",
			obs:  ports.SCMObservation{Fetched: true, PR: ports.SCMPRObservation{URL: "https://github.com/o/r/pull/2", Number: 2, Merged: true}},
			want: domain.NotificationPRMerged,
		},
		{
			name: "closed",
			obs:  ports.SCMObservation{Fetched: true, PR: ports.SCMPRObservation{URL: "https://github.com/o/r/pull/3", Number: 3, Closed: true}},
			want: domain.NotificationPRClosedUnmerged,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			sink := &fakeNotificationSink{}
			m := New(st, nil, WithNotificationSink(sink))
			st.sessions["mer-1"] = working("mer-1")
			if err := m.ApplySCMObservation(ctx, "mer-1", tc.obs); err != nil {
				t.Fatal(err)
			}
			if len(sink.intents) != 1 {
				t.Fatalf("intents = %d, want 1", len(sink.intents))
			}
			if got := sink.intents[0]; got.Type != tc.want || got.PRURL != tc.obs.PR.URL || got.PRNumber != tc.obs.PR.Number {
				t.Fatalf("intent = %+v, want type %s", got, tc.want)
			}
		})
	}
}

// Merging the PR is what resolves a ready-to-merge ping. So is the PR ceasing
// to be mergeable — either way there is nothing left for the user to merge.
func TestSCMObservation_ResolvesReadyToMergeWhenNoLongerReady(t *testing.T) {
	ready := ports.SCMObservation{
		Fetched:      true,
		PR:           ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", Number: 1},
		CI:           ports.SCMCIObservation{Summary: string(domain.CIPassing)},
		Review:       ports.SCMReviewObservation{Decision: string(domain.ReviewApproved)},
		Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable)},
	}
	merged := ready
	merged.PR.Merged = true
	ciBroke := ready
	ciBroke.CI.Summary = string(domain.CIFailing)

	for _, tt := range []struct {
		name string
		obs  ports.SCMObservation
		want int
	}{
		{name: "still ready", obs: ready, want: 0},
		{name: "merged", obs: merged, want: 1},
		{name: "ci went red", obs: ciBroke, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeStore()
			sink := &fakeNotificationSink{}
			m := New(st, nil, WithNotificationSink(sink))
			st.sessions["mer-1"] = working("mer-1")

			if err := m.ApplySCMObservation(ctx, "mer-1", tt.obs); err != nil {
				t.Fatal(err)
			}
			if len(sink.resolutions) != tt.want {
				t.Fatalf("resolutions = %+v, want %d", sink.resolutions, tt.want)
			}
			if tt.want == 0 {
				return
			}
			got := sink.resolutions[0]
			if got.Type != domain.NotificationReadyToMerge || got.PRURL != tt.obs.PR.URL {
				t.Fatalf("resolution = %+v", got)
			}
		})
	}
}

func TestSCMObservation_NotReadyWhenCIOrReviewBlocks(t *testing.T) {
	for _, obs := range []ports.SCMObservation{
		{Fetched: true, PR: ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", Number: 1}, CI: ports.SCMCIObservation{Summary: string(domain.CIFailing)}, Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable)}},
		{Fetched: true, PR: ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", Number: 1}, CI: ports.SCMCIObservation{Summary: string(domain.CIPending)}, Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable)}},
		{Fetched: true, PR: ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", Number: 1}, CI: ports.SCMCIObservation{Summary: string(domain.CIUnknown)}, Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable)}},
		{Fetched: true, PR: ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", Number: 1}, Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable)}},
		{Fetched: true, PR: ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", Number: 1}, CI: ports.SCMCIObservation{Summary: string(domain.CIPassing)}, Review: ports.SCMReviewObservation{Decision: string(domain.ReviewChangesRequest)}, Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable)}},
	} {
		st := newFakeStore()
		sink := &fakeNotificationSink{}
		m := New(st, nil, WithNotificationSink(sink))
		st.sessions["mer-1"] = working("mer-1")
		if err := m.ApplySCMObservation(ctx, "mer-1", obs); err != nil {
			t.Fatal(err)
		}
		if len(sink.intents) != 0 {
			t.Fatalf("blocked PR emitted %+v", sink.intents)
		}
	}
}

func TestSCMObservation_ReadyToMergeSuppressedWhileWaitingInput(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	rec := working("mer-1")
	rec.Activity.State = domain.ActivityWaitingInput
	st.sessions["mer-1"] = rec
	obs := ports.SCMObservation{
		Fetched:      true,
		PR:           ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", Number: 1},
		CI:           ports.SCMCIObservation{Summary: string(domain.CIPassing)},
		Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable)},
	}
	if err := m.ApplySCMObservation(ctx, "mer-1", obs); err != nil {
		t.Fatal(err)
	}
	if len(sink.intents) != 0 {
		t.Fatalf("waiting-input session emitted ready notification: %+v", sink.intents)
	}
}

func TestActivity_WorkerIdleDoesNotNudgeOrchestrator(t *testing.T) {
	m, st, msg := newManager()
	now := time.Now()
	st.sessions["mer-orch"] = domain.SessionRecord{ID: "mer-orch", ProjectID: "mer", Kind: domain.KindOrchestrator, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}, FirstSignalAt: now}
	st.sessions["mer-8"] = domain.SessionRecord{ID: "mer-8", ProjectID: "mer", Kind: domain.KindWorker, DisplayName: "husky-setup", Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now}, FirstSignalAt: now}

	if err := m.ApplyActivitySignal(ctx, "mer-8", ports.ActivitySignal{Valid: true, State: domain.ActivityIdle}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-8"].Activity.State; got != domain.ActivityIdle {
		t.Fatalf("worker activity = %q, want idle", got)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("orchestrator nudges = %d, want 0", len(msg.msgs))
	}
}

func TestActivity_OrchestratorIdleDoesNotNudge(t *testing.T) {
	m, st, msg := newManager()
	now := time.Now()
	st.sessions["mer-orch"] = domain.SessionRecord{ID: "mer-orch", ProjectID: "mer", Kind: domain.KindOrchestrator, Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now}, FirstSignalAt: now}

	if err := m.ApplyActivitySignal(ctx, "mer-orch", ports.ActivitySignal{Valid: true, State: domain.ActivityIdle}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-orch"].Activity.State; got != domain.ActivityIdle {
		t.Fatalf("orchestrator activity = %q, want idle", got)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("orchestrator idle self-nudged: %d, want 0", len(msg.msgs))
	}
}

func TestActivity_WorkerWaitingToIdleDoesNotNudge(t *testing.T) {
	m, st, msg := newManager()
	now := time.Now()
	st.sessions["mer-orch"] = domain.SessionRecord{ID: "mer-orch", ProjectID: "mer", Kind: domain.KindOrchestrator, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}, FirstSignalAt: now}
	st.sessions["mer-8"] = domain.SessionRecord{ID: "mer-8", ProjectID: "mer", Kind: domain.KindWorker, Activity: domain.Activity{State: domain.ActivityWaitingInput, LastActivityAt: now}, FirstSignalAt: now}

	if err := m.ApplyActivitySignal(ctx, "mer-8", ports.ActivitySignal{Valid: true, State: domain.ActivityIdle}); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("waiting_input->idle nudged: %d, want 0", len(msg.msgs))
	}
}

func TestActivity_WorkerIdleOrchestratorBlockedSuppressed(t *testing.T) {
	m, st, msg := newManager()
	now := time.Now()
	st.sessions["mer-orch"] = domain.SessionRecord{ID: "mer-orch", ProjectID: "mer", Kind: domain.KindOrchestrator, Activity: domain.Activity{State: domain.ActivityBlocked, LastActivityAt: now}, FirstSignalAt: now}
	st.sessions["mer-8"] = domain.SessionRecord{ID: "mer-8", ProjectID: "mer", Kind: domain.KindWorker, Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now}, FirstSignalAt: now}

	if err := m.ApplyActivitySignal(ctx, "mer-8", ports.ActivitySignal{Valid: true, State: domain.ActivityIdle}); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("nudged a blocked orchestrator: %d, want 0", len(msg.msgs))
	}
}

func TestActivity_WorkerIdleOrchestratorActiveDefersNoNudge(t *testing.T) {
	m, st, msg := newManager()
	now := time.Now()
	st.sessions["mer-orch"] = domain.SessionRecord{ID: "mer-orch", ProjectID: "mer", Kind: domain.KindOrchestrator, Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now}, FirstSignalAt: now}
	st.sessions["mer-8"] = domain.SessionRecord{ID: "mer-8", ProjectID: "mer", Kind: domain.KindWorker, Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now}, FirstSignalAt: now}

	if err := m.ApplyActivitySignal(ctx, "mer-8", ports.ActivitySignal{Valid: true, State: domain.ActivityIdle}); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("nudged a busy orchestrator: %d, want 0", len(msg.msgs))
	}
}

// fakeLifecycleContainerReaper is a minimal ports.ContainerReaper test double.
type fakeLifecycleContainerReaper struct {
	sessions []domain.SessionID
	removed  int
	err      error
}

func (f *fakeLifecycleContainerReaper) ReapSessionContainers(_ context.Context, id domain.SessionID) (int, error) {
	f.sessions = append(f.sessions, id)
	return f.removed, f.err
}

// fakeProjectConfigLoader is a minimal projectConfigLoader test double.
type fakeProjectConfigLoader struct {
	projects map[string]domain.ProjectRecord
	err      error
}

func (f *fakeProjectConfigLoader) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	if f.err != nil {
		return domain.ProjectRecord{}, false, f.err
	}
	rec, ok := f.projects[id]
	return rec, ok, nil
}

func newManagerWithContainerReaper(cr ports.ContainerReaper, pl projectConfigLoader) (*Manager, *fakeStore, *fakeMessenger) {
	st := newFakeStore()
	msg := &fakeMessenger{}
	m := New(st, msg, WithContainerReaper(cr, pl))
	return m, st, msg
}

// TestMarkTerminated_ReapsContainers is the #2652 regression for hooking the
// shared teardown path: MarkTerminated must reap the terminated session's
// containers, covering every terminal-state path (Kill, daemon shutdown,
// Cleanup, RetireForReplacement, tracker-driven termination) through this one
// choke point rather than only explicit ao session kill.
func TestMarkTerminated_ReapsContainers(t *testing.T) {
	cr := &fakeLifecycleContainerReaper{removed: 2}
	pl := &fakeProjectConfigLoader{projects: map[string]domain.ProjectRecord{
		"mer": {ID: "mer", Config: domain.ProjectConfig{}},
	}}
	m, st, _ := newManagerWithContainerReaper(cr, pl)
	st.sessions["mer-1"] = working("mer-1")

	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if len(cr.sessions) != 1 || cr.sessions[0] != "mer-1" {
		t.Fatalf("expected container reap for mer-1, got %v", cr.sessions)
	}
}

func TestMarkTerminated_ReapsContainersAgainWhenAlreadyTerminated(t *testing.T) {
	cr := &fakeLifecycleContainerReaper{}
	pl := &fakeProjectConfigLoader{projects: map[string]domain.ProjectRecord{
		"mer": {ID: "mer", Config: domain.ProjectConfig{}},
	}}
	m, st, _ := newManagerWithContainerReaper(cr, pl)
	st.sessions["mer-1"] = working("mer-1")

	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if len(cr.sessions) != 2 {
		t.Fatalf("container reap calls = %v, want retry on repeated termination", cr.sessions)
	}
}

// TestMarkTerminated_ContainerReapFailureDoesNotFailTermination asserts the
// best-effort contract: a container reaper error must never fail
// MarkTerminated, matching every other best-effort teardown step in AO.
func TestMarkTerminated_ContainerReapFailureDoesNotFailTermination(t *testing.T) {
	cr := &fakeLifecycleContainerReaper{err: errors.New("docker rm: permission denied")}
	pl := &fakeProjectConfigLoader{projects: map[string]domain.ProjectRecord{
		"mer": {ID: "mer", Config: domain.ProjectConfig{}},
	}}
	m, st, _ := newManagerWithContainerReaper(cr, pl)
	st.sessions["mer-1"] = working("mer-1")

	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatalf("a container reap failure must not fail MarkTerminated: %v", err)
	}
	got := st.sessions["mer-1"]
	if !got.IsTerminated {
		t.Fatal("session must still be marked terminated despite the reap failure")
	}
	if len(cr.sessions) != 1 {
		t.Fatalf("expected container reap to still be attempted, got %v", cr.sessions)
	}
}

// TestMarkTerminated_SkipsReapWhenProjectDisables covers the project-level
// opt-out: ContainerReap.Disabled must suppress the reap without affecting
// termination.
func TestMarkTerminated_SkipsReapWhenProjectDisables(t *testing.T) {
	cr := &fakeLifecycleContainerReaper{}
	pl := &fakeProjectConfigLoader{projects: map[string]domain.ProjectRecord{
		"mer": {ID: "mer", Config: domain.ProjectConfig{ContainerReap: domain.ContainerReapConfig{Disabled: true}}},
	}}
	m, st, _ := newManagerWithContainerReaper(cr, pl)
	st.sessions["mer-1"] = working("mer-1")

	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if len(cr.sessions) != 0 {
		t.Fatalf("expected no reap call when project disables container reap, got %v", cr.sessions)
	}
	if !st.sessions["mer-1"].IsTerminated {
		t.Fatal("session must still be marked terminated when reap is disabled")
	}
}

// TestMarkTerminated_ProjectLoadErrorSkipsRatherThanReaps is the regression
// for failing open: a project-config load error must skip reaping rather than
// guess and reap anyway. This package's stated bias throughout is to spare on
// ambiguity, never to reap on it.
func TestMarkTerminated_ProjectLoadErrorSkipsRatherThanReaps(t *testing.T) {
	cr := &fakeLifecycleContainerReaper{}
	pl := &fakeProjectConfigLoader{err: errors.New("db unavailable")}
	m, st, _ := newManagerWithContainerReaper(cr, pl)
	st.sessions["mer-1"] = working("mer-1")

	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if len(cr.sessions) != 0 {
		t.Fatalf("a project-load error must skip reaping (spare on ambiguity), got calls: %v", cr.sessions)
	}
	if !st.sessions["mer-1"].IsTerminated {
		t.Fatal("session must still be marked terminated when the project lookup fails")
	}
}

// TestMarkTerminated_NilReaperSkipsWithoutProjectLookup confirms nil wiring
// (the common case — most AO installs run without Docker) skips reaping
// cleanly without even attempting a project lookup.
func TestMarkTerminated_NilReaperSkipsWithoutProjectLookup(t *testing.T) {
	m, st, _ := newManager() // newManager wires no container reaper at all
	st.sessions["mer-1"] = working("mer-1")

	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if !st.sessions["mer-1"].IsTerminated {
		t.Fatal("session must still be marked terminated with no reaper wired")
	}
}

// TestMarkTerminated_MissingProjectSkipsRatherThanReaps is the regression for
// failing open on a missing project record: GetProject returning ok=false,
// err=nil is ambiguity (AO cannot know whether ContainerReap.Disabled would
// have applied), not a green light to reap. Must be treated the same as the
// error path.
func TestMarkTerminated_MissingProjectSkipsRatherThanReaps(t *testing.T) {
	cr := &fakeLifecycleContainerReaper{}
	pl := &fakeProjectConfigLoader{projects: map[string]domain.ProjectRecord{}} // no "mer" entry: ok=false, err=nil
	m, st, _ := newManagerWithContainerReaper(cr, pl)
	st.sessions["mer-1"] = working("mer-1")

	if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if len(cr.sessions) != 0 {
		t.Fatalf("a missing project record must skip reaping (spare on ambiguity), got calls: %v", cr.sessions)
	}
	if !st.sessions["mer-1"].IsTerminated {
		t.Fatal("session must still be marked terminated when the project record is missing")
	}
}

// TestRuntimeObservation_ConfirmedDeathReapsContainers is the regression for
// the review finding that ApplyRuntimeObservation's reaper-driven terminal
// transition (crash/SIGKILL detected by the runtime reaper) bypassed
// MarkTerminated entirely and left containers unreaped. This confirms the
// container leg of #2652 now fires on this path too, not just explicit kill.
func TestRuntimeObservation_ConfirmedDeathReapsContainers(t *testing.T) {
	cr := &fakeLifecycleContainerReaper{removed: 1}
	pl := &fakeProjectConfigLoader{projects: map[string]domain.ProjectRecord{
		"mer": {ID: "mer", Config: domain.ProjectConfig{}},
	}}
	m, st, _ := newManagerWithContainerReaper(cr, pl)
	rec := working("mer-1")
	rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
	st.sessions["mer-1"] = rec

	if err := m.ApplyRuntimeObservation(ctx, "mer-1", ports.RuntimeFacts{Runtime: ports.ProbeDead, Workload: ports.ProbeFailed}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if !got.IsTerminated || got.Activity.State != domain.ActivityExited {
		t.Fatalf("want terminated/exited, got %+v", got)
	}
	if len(cr.sessions) != 1 || cr.sessions[0] != "mer-1" {
		t.Fatalf("expected container reap for mer-1 on reaper-observed death, got %v", cr.sessions)
	}
}

// TestRuntimeObservation_WorkloadDeathAloneDoesNotReap confirms the
// non-terminal workload-dead branch (runtime alive, workload dead) does NOT
// trigger a container reap — only a confirmed session termination should.
func TestRuntimeObservation_WorkloadDeathAloneDoesNotReap(t *testing.T) {
	cr := &fakeLifecycleContainerReaper{}
	pl := &fakeProjectConfigLoader{projects: map[string]domain.ProjectRecord{
		"mer": {ID: "mer", Config: domain.ProjectConfig{}},
	}}
	m, st, _ := newManagerWithContainerReaper(cr, pl)
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch-1"
	st.sessions["mer-1"] = rec

	if err := m.ApplyRuntimeObservation(ctx, "mer-1", ports.RuntimeFacts{LaunchID: "launch-1", Runtime: ports.ProbeAlive, Workload: ports.ProbeDead}); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if got.IsTerminated {
		t.Fatal("workload death alone must not terminate the session")
	}
	if len(cr.sessions) != 0 {
		t.Fatalf("expected no reap call for a non-terminal transition, got %v", cr.sessions)
	}
}

// mergeMetadata is an explicit allowlist, so a field added to SessionMetadata
// without a line here is silently dropped on every spawn and restore. That
// happened to the chat resume handle: the provider still held the conversation,
// but AO forgot its id, so no restart could ever resume it — and nothing failed
// loudly, the column was just empty.
func TestMarkSpawnedPersistsChatControllerFacts(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Mode: domain.SessionModeChat,
		Metadata: domain.SessionMetadata{
			RuntimeHandleID: "stale-tui-runtime",
			RuntimeLaunchID: "stale-tui-generation",
		},
	}
	m := New(st, nil)

	if err := m.MarkSpawned(ctx, "mer-1", domain.SessionMetadata{
		WorkspacePath:          "/ws",
		ProviderConversationID: "thread-abc",
		ControllerGeneration:   "gen-1",
	}); err != nil {
		t.Fatalf("MarkSpawned: %v", err)
	}

	got, _, err := st.GetSession(ctx, "mer-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Metadata.ProviderConversationID != "thread-abc" {
		t.Fatalf("provider conversation id = %q; without it a restart cannot resume",
			got.Metadata.ProviderConversationID)
	}
	if got.Metadata.ControllerGeneration != "gen-1" {
		t.Fatalf("controller generation = %q", got.Metadata.ControllerGeneration)
	}
	if got.Metadata.RuntimeHandleID != "" || got.Metadata.RuntimeLaunchID != "" {
		t.Fatalf("Chat spawn retained terminal ownership metadata: %+v", got.Metadata)
	}

	// A relaunch rotates the generation: the new value must replace the old, or
	// events from the superseded controller could not be told apart.
	if err := m.MarkSpawned(ctx, "mer-1", domain.SessionMetadata{
		WorkspacePath:          "/ws",
		ProviderConversationID: "thread-abc",
		ControllerGeneration:   "gen-2",
	}); err != nil {
		t.Fatalf("second MarkSpawned: %v", err)
	}
	got, _, _ = st.GetSession(ctx, "mer-1")
	if got.Metadata.ControllerGeneration != "gen-2" {
		t.Fatalf("generation = %q after relaunch, want it rotated to gen-2", got.Metadata.ControllerGeneration)
	}
}

// Model is the same allowlist omission as the chat resume handle above, one
// field over: it is declared on SessionMetadata, has its own sessions.model
// column, and is read back by the API — but mergeMetadata never copied it, so
// every `ao spawn --model X` persisted an empty model and the session reported
// no model at all.
func TestMarkSpawnedPersistsResolvedModel(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer"}
	m := New(st, nil)

	if err := m.MarkSpawned(ctx, "mer-1", domain.SessionMetadata{
		WorkspacePath: "/ws",
		Model:         "sonnet",
	}); err != nil {
		t.Fatalf("MarkSpawned: %v", err)
	}

	got, _, err := st.GetSession(ctx, "mer-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Metadata.Model != "sonnet" {
		t.Fatalf("model = %q, want %q; a spawn's resolved model must survive the merge",
			got.Metadata.Model, "sonnet")
	}

	// Merged rather than assigned: a relaunch that resolves no explicit model
	// must leave the recorded one alone instead of blanking it.
	if err := m.MarkSpawned(ctx, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws"}); err != nil {
		t.Fatalf("second MarkSpawned: %v", err)
	}
	got, _, _ = st.GetSession(ctx, "mer-1")
	if got.Metadata.Model != "sonnet" {
		t.Fatalf("model = %q after a relaunch that resolved none, want it preserved", got.Metadata.Model)
	}
}

func TestMarkChatSpawnedKeepsPreviousOwnerWhenAtomicBoundaryCommitFails(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	previous := domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Mode: domain.SessionModeChat,
		Metadata: domain.SessionMetadata{
			ProviderConversationID: "thread-source",
			ControllerGeneration:   "generation-source",
		},
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Unix(1, 0)},
	}
	st.sessions[previous.ID] = previous
	commitErr := errors.New("provider boundary transaction failed")
	st.chatSpawnErr = commitErr
	m := New(st, nil)
	boundary := domain.ConversationBranch{
		ID: "fresh-provider-boundary", ConversationID: "conversation-1", SessionID: previous.ID,
		ProviderConversationID: "thread-fresh", ParentBranchID: "source-provider-boundary",
		ProviderScopeID: "fresh-provider-boundary", CreatedAt: time.Unix(2, 0),
	}

	err := m.MarkChatSpawned(ctx, previous.ID, domain.SessionMetadata{
		ProviderConversationID: "thread-fresh",
		ControllerGeneration:   "generation-fresh",
	}, boundary)
	if !errors.Is(err, commitErr) {
		t.Fatalf("MarkChatSpawned error = %v, want atomic commit failure", err)
	}
	got := st.sessions[previous.ID]
	if got.Metadata.ProviderConversationID != previous.Metadata.ProviderConversationID ||
		got.Metadata.ControllerGeneration != previous.Metadata.ControllerGeneration {
		t.Fatalf("owner after failed boundary commit = handle %q generation %q, want %q/%q",
			got.Metadata.ProviderConversationID, got.Metadata.ControllerGeneration,
			previous.Metadata.ProviderConversationID, previous.Metadata.ControllerGeneration)
	}
	if len(st.chatSpawnCalls) != 0 {
		t.Fatalf("committed Chat boundaries after failure = %+v", st.chatSpawnCalls)
	}
}

func TestMarkChatSpawnedCommitsReservedBoundaryWithLifecycleOwner(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Mode: domain.SessionModeChat, IsTerminated: true,
	}
	m := New(st, nil)
	boundary := domain.ConversationBranch{
		ID: "fresh-provider-boundary", ConversationID: "conversation-1", SessionID: "mer-1",
		ProviderConversationID: "thread-fresh", ParentBranchID: "source-provider-boundary",
		ProviderScopeID: "fresh-provider-boundary", CreatedAt: time.Unix(2, 0),
	}

	if err := m.MarkChatSpawned(ctx, "mer-1", domain.SessionMetadata{
		ProviderConversationID: "thread-fresh",
		ControllerGeneration:   "generation-fresh",
	}, boundary); err != nil {
		t.Fatalf("MarkChatSpawned: %v", err)
	}
	if len(st.chatSpawnCalls) != 1 || st.chatSpawnCalls[0].ID != boundary.ID {
		t.Fatalf("committed Chat boundaries = %+v, want %q", st.chatSpawnCalls, boundary.ID)
	}
	got := st.sessions["mer-1"]
	if got.IsTerminated || got.Activity.State != domain.ActivityIdle ||
		got.Metadata.ProviderConversationID != "thread-fresh" ||
		got.Metadata.ControllerGeneration != "generation-fresh" {
		t.Fatalf("committed Chat owner = %+v", got)
	}
}

func TestActivitySignalRejectsStaleChatControllerGenerationAcrossHandoff(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Mode: domain.SessionModeChat,
		Metadata: domain.SessionMetadata{ControllerGeneration: "chat-generation-2"},
		Activity: domain.Activity{State: domain.ActivityIdle},
	}
	m := New(st, nil)

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, ControllerGeneration: "chat-generation-1",
	}); err != nil {
		t.Fatalf("stale signal: %v", err)
	}
	if got := st.sessions["mer-1"].Activity.State; got != domain.ActivityIdle {
		t.Fatalf("stale generation changed activity to %q", got)
	}

	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, ControllerGeneration: "chat-generation-2",
	}); err != nil {
		t.Fatalf("current signal: %v", err)
	}
	if got := st.sessions["mer-1"].Activity.State; got != domain.ActivityActive {
		t.Fatalf("current generation left activity at %q", got)
	}

	rec := st.sessions["mer-1"]
	rec.Mode = domain.SessionModeTUI
	rec.Activity.State = domain.ActivityIdle
	st.sessions["mer-1"] = rec
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, ControllerGeneration: "chat-generation-2",
	}); err != nil {
		t.Fatalf("post-handoff stale signal: %v", err)
	}
	if got := st.sessions["mer-1"].Activity.State; got != domain.ActivityIdle {
		t.Fatalf("old Chat controller changed TUI activity to %q", got)
	}
}

func TestActivitySignalFencesChatByControllerGenerationDespiteStaleRuntimeMetadata(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Mode: domain.SessionModeChat,
		Metadata: domain.SessionMetadata{
			RuntimeHandleID:      "stale-tui-runtime",
			RuntimeLaunchID:      "stale-tui-generation",
			ControllerGeneration: "chat-generation",
		},
		Activity: domain.Activity{State: domain.ActivityIdle},
	}
	m := New(st, nil)

	for _, signal := range []ports.ActivitySignal{
		{Valid: true, State: domain.ActivityActive, LaunchID: "stale-tui-generation"},
		{Valid: true, State: domain.ActivityActive, ControllerGeneration: "foreign-generation"},
	} {
		if err := m.ApplyActivitySignal(ctx, "mer-1", signal); err != nil {
			t.Fatalf("reject non-owner signal: %v", err)
		}
		if got := st.sessions["mer-1"].Activity.State; got != domain.ActivityIdle {
			t.Fatalf("non-owner signal changed activity to %q", got)
		}
	}

	for _, state := range []domain.ActivityState{domain.ActivityActive, domain.ActivityIdle} {
		if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
			Valid: true, State: state, ControllerGeneration: "chat-generation",
		}); err != nil {
			t.Fatalf("apply current Chat signal %q: %v", state, err)
		}
		if got := st.sessions["mer-1"].Activity.State; got != state {
			t.Fatalf("current Chat signal left activity at %q, want %q", got, state)
		}
	}
}

// TestEmitTelemetryStampsRequestID covers the shared lifecycle emit path: an
// HTTP-driven activity signal must tag its events with the request id so the
// lifecycle rows join to the request, while reaper/poller ticks (a plain
// background context) must keep an empty request id.
func TestEmitTelemetryStampsRequestID(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{
			name: "request scoped",
			ctx:  context.WithValue(context.Background(), middleware.RequestIDKey, "req-1"),
			want: "req-1",
		},
		{name: "background context", ctx: context.Background(), want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			sink := &telemetrySink{}
			m := New(st, nil, WithTelemetry(sink))
			now := time.Unix(100, 0).UTC()
			m.clock = func() time.Time { return now }
			st.sessions["mer-1"] = domain.SessionRecord{
				ID:        "mer-1",
				ProjectID: "mer",
				Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)},
			}

			signal := ports.ActivitySignal{Valid: true, State: domain.ActivityWaitingInput, Timestamp: now}
			if err := m.ApplyActivitySignal(tc.ctx, "mer-1", signal); err != nil {
				t.Fatal(err)
			}
			if len(sink.events) == 0 {
				t.Fatal("no telemetry events emitted")
			}
			for _, ev := range sink.events {
				if ev.RequestID != tc.want {
					t.Fatalf("%s RequestID = %q, want %q", ev.Name, ev.RequestID, tc.want)
				}
			}
		})
	}
}
