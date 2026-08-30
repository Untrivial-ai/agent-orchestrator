package interfacereconcile

import (
	"context"
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
	advances    []domain.SessionInterfaceTransitionPhase
	claimErr    error
	advanceErr  error
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
	_ = from
	return nil
}
func (f *fakeStore) CommitCoordinatedSessionInterface(ctx context.Context, owner, orgID, sessionID string, v domain.SessionInterface) (bool, error) {
	f.committed = v
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
}

func (f *fakeDriver) PreflightTarget(context.Context, postgres.CoordinatedInterfaceTransition) error {
	return f.preflight
}
func (f *fakeDriver) InspectSource(context.Context, postgres.CoordinatedInterfaceTransition) (SourceInspection, error) {
	return f.Inspection, f.inspectErr
}
func (f *fakeDriver) InterruptSource(context.Context, postgres.CoordinatedInterfaceTransition) error {
	f.interrupt = true
	return nil
}
func (f *fakeDriver) StopSource(context.Context, postgres.CoordinatedInterfaceTransition) error {
	return f.stopErr
}
func (f *fakeDriver) ResolveNativeConversationID(context.Context, postgres.CoordinatedInterfaceTransition) (string, error) {
	if f.nativeIDErr != nil {
		return "", f.nativeIDErr
	}
	return f.nativeID, nil
}
func (f *fakeDriver) StartTarget(context.Context, postgres.CoordinatedInterfaceTransition, string) error {
	return f.startErr
}

func newCoordinator(store *fakeStore, driver *fakeDriver) *Coordinator {
	return New(store, driver, Options{Interval: time.Millisecond, Logger: slog.New(slog.DiscardHandler)})
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
