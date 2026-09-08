package interfacereconcile

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

func testTransition(phase domain.SessionInterfaceTransitionPhase) postgres.CoordinatedInterfaceTransition {
	return postgres.CoordinatedInterfaceTransition{
		SessionInterfaceTransition: domain.SessionInterfaceTransition{
			ID:                   "transition-1",
			OrgID:                "org-1",
			SessionID:            "session-1",
			SourceInterface:      domain.SessionInterfaceTUI,
			TargetInterface:      domain.SessionInterfaceChat,
			Policy:               domain.SessionInterfaceTransitionDrain,
			Phase:                phase,
			NativeConversationID: "native-1",
		},
		Harness: "claude-code",
	}
}

type fakeStore struct {
	transitions []postgres.CoordinatedInterfaceTransition
	committed   domain.SessionInterface
	commitCalls int
	advances    []domain.SessionInterfaceTransitionPhase
	claimErr    error
	advanceErr  error
	commitErr   error
}

func (f *fakeStore) ClaimCoordinatedInterfaceTransitions(context.Context, string, int, time.Duration) ([]postgres.CoordinatedInterfaceTransition, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	return f.transitions, nil
}
func (f *fakeStore) RenewCoordinatedInterfaceClaim(ctx context.Context, owner, transitionID string, lease time.Duration) error {
	return nil
}
func (f *fakeStore) AdvanceCoordinatedInterfaceTransition(ctx context.Context, owner, transitionID string, from, to domain.SessionInterfaceTransitionPhase, nativeConversationID, errorCode, errorDetail string) error {
	if f.advanceErr != nil {
		return f.advanceErr
	}
	f.advances = append(f.advances, to)
	for index := range f.transitions {
		if f.transitions[index].ID != transitionID {
			continue
		}
		if f.transitions[index].Phase != from {
			return postgres.ErrTransitionStale
		}
		f.transitions[index].Phase = to
		if nativeConversationID != "" {
			f.transitions[index].NativeConversationID = nativeConversationID
		}
		break
	}
	return nil
}
func (f *fakeStore) CommitCoordinatedSessionInterface(ctx context.Context, owner, orgID, transitionID string, v domain.SessionInterface) (bool, error) {
	if f.commitErr != nil {
		return false, f.commitErr
	}
	f.committed = v
	f.commitCalls++
	return true, nil
}
func (f *fakeStore) ReleaseCoordinatedInterfaceClaim(ctx context.Context, owner, transitionID string) error {
	return nil
}
func (f *fakeStore) EnqueueSessionInterfaceTransitionMessage(ctx context.Context, transitionID, clientMessageID, message string) error {
	return nil
}

type fakeDriver struct {
	Inspection  SourceInspection
	inspectErr  error
	stopErr     error
	startErr    error
	preflight   error
	interrupt   bool
	nativeID    string
	nativeIDErr error

	preflightCalls int
	inspectCalls   int
	interruptCalls int
	stopCalls      int
	nativeIDCalls  int
	startCalls     int

	startedWithNativeID string
}

func (f *fakeDriver) PreflightTarget(context.Context, postgres.CoordinatedInterfaceTransition) error {
	f.preflightCalls++
	return f.preflight
}
func (f *fakeDriver) InspectSource(context.Context, postgres.CoordinatedInterfaceTransition) (SourceInspection, error) {
	f.inspectCalls++
	return f.Inspection, f.inspectErr
}
func (f *fakeDriver) InterruptSource(context.Context, postgres.CoordinatedInterfaceTransition) error {
	f.interrupt = true
	f.interruptCalls++
	return nil
}
func (f *fakeDriver) StopSource(context.Context, postgres.CoordinatedInterfaceTransition) error {
	f.stopCalls++
	return f.stopErr
}
func (f *fakeDriver) ResolveNativeConversationID(context.Context, postgres.CoordinatedInterfaceTransition) (string, error) {
	f.nativeIDCalls++
	if f.nativeIDErr != nil {
		return "", f.nativeIDErr
	}
	return f.nativeID, nil
}
func (f *fakeDriver) StartTarget(_ context.Context, _ postgres.CoordinatedInterfaceTransition, nativeID string) error {
	f.startCalls++
	f.startedWithNativeID = nativeID
	return f.startErr
}

func newCoordinator(store *fakeStore, driver *fakeDriver) *Coordinator {
	return New(store, driver, Options{Interval: time.Millisecond, Logger: slog.New(slog.DiscardHandler)})
}

// fakeRequestStore satisfies the TransportDriver's RequestStore for tests that
// never reach a dispatch (preflight fails before any worker request exists).
type fakeRequestStore struct{}

func (fakeRequestStore) CreateCoordinatedInterfaceRequest(context.Context, string, string, string, json.RawMessage) (domain.WorkerRequest, error) {
	return domain.WorkerRequest{}, errors.New("no requests expected")
}
func (fakeRequestStore) GetCoordinatedInterfaceRequestResult(context.Context, string, string, string) (domain.WorkerRequest, error) {
	return domain.WorkerRequest{}, errors.New("no requests expected")
}
func TestReconcileHappyPath(t *testing.T) {
	store := &fakeStore{transitions: []postgres.CoordinatedInterfaceTransition{testTransition(domain.SessionInterfaceTransitionRequested)}}
	driver := &fakeDriver{Inspection: SourceInspection{Idle: true}, nativeID: "native-1"}
	err := newCoordinator(store, driver).ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if store.committed != domain.SessionInterfaceChat {
		t.Fatalf("expected interface committed to chat, got %q", store.committed)
	}
	last := store.advances[len(store.advances)-1]
	if last != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("expected final phase completed, got %q", last)
	}
}

func TestReconcileTreatsStaleCommitAsCoordinationLoss(t *testing.T) {
	store := &fakeStore{
		transitions: []postgres.CoordinatedInterfaceTransition{testTransition(domain.SessionInterfaceTransitionSourceStopped)},
		commitErr:   postgres.ErrTransitionStale,
	}
	driver := &fakeDriver{nativeID: "native-1"}
	if err := newCoordinator(store, driver).ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("stale coordinator should stop without failing the transition: %v", err)
	}
	if store.commitCalls != 0 {
		t.Fatalf("stale commit should not be recorded as committed: %d calls", store.commitCalls)
	}
	if len(store.advances) != 0 {
		t.Fatalf("stale coordinator must not advance the transition: %v", store.advances)
	}
}

// Both handoff directions and every supported harness must converge through
// the same durable phase machine, commit the requested interface, and hand the
// shared native conversation identity to the target controller.
func TestReconcileEverySupportedHarnessBothDirections(t *testing.T) {
	for _, harness := range []string{"codex", "claude-code", "cursor"} {
		for _, direction := range []struct {
			name   string
			source domain.SessionInterface
			target domain.SessionInterface
		}{
			{name: "tui-to-chat", source: domain.SessionInterfaceTUI, target: domain.SessionInterfaceChat},
			{name: "chat-to-tui", source: domain.SessionInterfaceChat, target: domain.SessionInterfaceTUI},
		} {
			t.Run(harness+"/"+direction.name, func(t *testing.T) {
				transition := testTransition(domain.SessionInterfaceTransitionRequested)
				transition.Harness = harness
				transition.SourceInterface = direction.source
				transition.TargetInterface = direction.target
				store := &fakeStore{transitions: []postgres.CoordinatedInterfaceTransition{transition}}
				driver := &fakeDriver{Inspection: SourceInspection{Idle: true}, nativeID: "native-" + harness}
				err := newCoordinator(store, driver).ReconcileOnce(context.Background())
				if err != nil {
					t.Fatalf("reconcile: %v", err)
				}
				if store.committed != direction.target {
					t.Fatalf("expected interface committed to %s, got %q", direction.target, store.committed)
				}
				if driver.startedWithNativeID != "native-"+harness {
					t.Fatalf("target started with native id %q, want native-%s", driver.startedWithNativeID, harness)
				}
				if last := store.advances[len(store.advances)-1]; last != domain.SessionInterfaceTransitionCompleted {
					t.Fatalf("expected final phase completed, got %q", last)
				}
			})
		}
	}
}

func TestTransportDriverPreflightRejectsUnsupportedHarness(t *testing.T) {
	driver := NewTransportDriver(&fakeRequestStore{}, "owner", time.Millisecond, slog.New(slog.DiscardHandler))
	transition := testTransition(domain.SessionInterfaceTransitionRequested)
	for _, harness := range []string{"codex", "claude-code", "cursor"} {
		transition.Harness = harness
		if err := driver.PreflightTarget(context.Background(), transition); err != nil {
			t.Fatalf("preflight %s: %v", harness, err)
		}
	}
	transition.Harness = "unknown-harness"
	if err := driver.PreflightTarget(context.Background(), transition); err == nil {
		t.Fatal("expected an unsupported harness to fail preflight")
	}
}

func TestReconcileInterruptPolicy(t *testing.T) {
	store := &fakeStore{transitions: []postgres.CoordinatedInterfaceTransition{testTransition(domain.SessionInterfaceTransitionRequested)}}
	store.transitions[0].Policy = domain.SessionInterfaceTransitionInterrupt
	driver := &fakeDriver{Inspection: SourceInspection{Idle: true}}
	err := newCoordinator(store, driver).ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !driver.interrupt {
		t.Fatal("expected interrupt to be issued for interrupt policy")
	}
	if store.committed != domain.SessionInterfaceChat {
		t.Fatalf("expected interface committed to chat, got %q", store.committed)
	}
}

func TestReconcileResumesFromDurablePhaseWithoutReplayingCompletedWork(t *testing.T) {
	for _, test := range []struct {
		phase     domain.SessionInterfaceTransitionPhase
		preflight int
		inspect   int
		interrupt int
		stop      int
		nativeID  int
		commit    int
		start     int
	}{
		{phase: domain.SessionInterfaceTransitionRequested, preflight: 1, inspect: 1, interrupt: 1, stop: 1, nativeID: 1, commit: 1, start: 1},
		{phase: domain.SessionInterfaceTransitionPreflighting, preflight: 1, inspect: 1, interrupt: 1, stop: 1, nativeID: 1, commit: 1, start: 1},
		{phase: domain.SessionInterfaceTransitionDraining, inspect: 1, interrupt: 1, stop: 1, nativeID: 1, commit: 1, start: 1},
		{phase: domain.SessionInterfaceTransitionSourceStopping, stop: 1, nativeID: 1, commit: 1, start: 1},
		{phase: domain.SessionInterfaceTransitionSourceStopped, nativeID: 1, commit: 1, start: 1},
		{phase: domain.SessionInterfaceTransitionTargetStarting, start: 1},
		{phase: domain.SessionInterfaceTransitionActivating},
	} {
		t.Run(string(test.phase), func(t *testing.T) {
			transition := testTransition(test.phase)
			transition.Policy = domain.SessionInterfaceTransitionInterrupt
			store := &fakeStore{transitions: []postgres.CoordinatedInterfaceTransition{transition}}
			driver := &fakeDriver{Inspection: SourceInspection{Idle: true}, nativeID: "native-resumed"}

			if err := newCoordinator(store, driver).ReconcileOnce(context.Background()); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if got := store.transitions[0].Phase; got != domain.SessionInterfaceTransitionCompleted {
				t.Fatalf("phase = %q, want completed", got)
			}
			if driver.preflightCalls != test.preflight || driver.inspectCalls != test.inspect ||
				driver.interruptCalls != test.interrupt || driver.stopCalls != test.stop ||
				driver.nativeIDCalls != test.nativeID || store.commitCalls != test.commit ||
				driver.startCalls != test.start {
				t.Fatalf("calls = preflight:%d inspect:%d interrupt:%d stop:%d native:%d commit:%d start:%d",
					driver.preflightCalls, driver.inspectCalls, driver.interruptCalls,
					driver.stopCalls, driver.nativeIDCalls, store.commitCalls, driver.startCalls)
			}
		})
	}
}

func TestReconcilePendingStopResumesAtSourceStopping(t *testing.T) {
	transition := testTransition(domain.SessionInterfaceTransitionSourceStopping)
	transition.Policy = domain.SessionInterfaceTransitionInterrupt
	store := &fakeStore{transitions: []postgres.CoordinatedInterfaceTransition{transition}}
	driver := &fakeDriver{
		Inspection: SourceInspection{Idle: true},
		nativeID:   "native-resumed",
		stopErr:    errPendingWorkerCommand,
	}
	coordinator := newCoordinator(store, driver)
	if err := coordinator.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if got := store.transitions[0].Phase; got != domain.SessionInterfaceTransitionSourceStopping {
		t.Fatalf("phase after pending stop = %q, want source_stopping", got)
	}
	driver.stopErr = nil
	if err := coordinator.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if driver.inspectCalls != 0 || driver.interruptCalls != 0 {
		t.Fatalf("completed drain was replayed: inspect=%d interrupt=%d", driver.inspectCalls, driver.interruptCalls)
	}
	if driver.stopCalls != 2 || driver.nativeIDCalls != 1 || store.commitCalls != 1 || driver.startCalls != 1 {
		t.Fatalf("calls = stop:%d native:%d commit:%d start:%d", driver.stopCalls, driver.nativeIDCalls, store.commitCalls, driver.startCalls)
	}
}

func TestReconcileDrainDecisionPendingFails(t *testing.T) {
	store := &fakeStore{transitions: []postgres.CoordinatedInterfaceTransition{testTransition(domain.SessionInterfaceTransitionRequested)}}
	driver := &fakeDriver{Inspection: SourceInspection{DecisionPending: true}}
	err := newCoordinator(store, driver).ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("expected decision-pending to be surfaced as a recovered failure, got err: %v", err)
	}
	if store.committed != "" {
		t.Fatalf("no session interface should be committed on drain failure, got %q", store.committed)
	}
	if last := store.advances[len(store.advances)-1]; last != domain.SessionInterfaceTransitionFailed {
		t.Fatalf("expected terminal phase failed, got %q", last)
	}
}

func TestReconcileTargetStartFailureRecovers(t *testing.T) {
	store := &fakeStore{transitions: []postgres.CoordinatedInterfaceTransition{testTransition(domain.SessionInterfaceTransitionRequested)}}
	driver := &fakeDriver{Inspection: SourceInspection{Idle: true}, startErr: errors.New("harness unavailable")}
	err := newCoordinator(store, driver).ReconcileOnce(context.Background())
	if err != nil && !errors.Is(err, errCoordinationLost) {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if store.committed != domain.SessionInterfaceChat {
		t.Fatalf("expected interface committed to chat before target start, got %q", store.committed)
	}
	last := store.advances[len(store.advances)-1]
	if last != domain.SessionInterfaceTransitionRecovery {
		t.Fatalf("expected terminal phase recovery_required, got %q", last)
	}
}

func TestReconcilePreflightFailure(t *testing.T) {
	store := &fakeStore{transitions: []postgres.CoordinatedInterfaceTransition{testTransition(domain.SessionInterfaceTransitionRequested)}}
	driver := &fakeDriver{preflight: errors.New("chat unsupported")}
	err := newCoordinator(store, driver).ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("expected preflight failure surfaced as recovered failure, got err: %v", err)
	}
	if store.committed != "" {
		t.Fatalf("no session interface should be committed on preflight failure, got %q", store.committed)
	}
}

func TestReconcilePendingWorkerCommandRecovers(t *testing.T) {
	store := &fakeStore{transitions: []postgres.CoordinatedInterfaceTransition{testTransition(domain.SessionInterfaceTransitionRequested)}}
	driver := &fakeDriver{
		Inspection: SourceInspection{Idle: true},
		// The worker never completes the native-conversation-id resolver, so
		// every run reports a pending retryable command before commit.
		nativeIDErr: errPendingWorkerCommand,
	}
	coordinator := newCoordinator(store, driver)
	for attempt := 0; attempt < defaultMaxRetries+2; attempt++ {
		_ = coordinator.ReconcileOnce(context.Background())
	}
	// The pending command is resolved before the session interface is committed,
	// so no commit happens; the transition fails closed after retry exhaustion.
	if store.committed != "" {
		t.Fatalf("expected no commit while native id is pending, got %q", store.committed)
	}
	for _, phase := range store.advances {
		if phase == domain.SessionInterfaceTransitionFailed {
			return
		}
	}
	t.Fatalf("expected terminal phase failed after pending retries, got %v", store.advances)
}
