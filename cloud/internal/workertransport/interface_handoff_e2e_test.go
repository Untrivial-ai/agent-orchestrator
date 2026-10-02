package workertransport

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/interfacereconcile"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

type handoffPathStore struct {
	transition  postgres.CoordinatedInterfaceTransition
	supervisor  *Supervisor
	workerCtx   context.Context
	committed   domain.SessionInterface
	requests    []string
	stopStarted chan struct{}
	sourceDone  <-chan struct{}
}

func (s *handoffPathStore) ClaimCoordinatedInterfaceTransitions(_ context.Context, _ string, _ int, _ time.Duration) ([]postgres.CoordinatedInterfaceTransition, error) {
	if s.transition.Phase == domain.SessionInterfaceTransitionCompleted {
		return nil, nil
	}
	return []postgres.CoordinatedInterfaceTransition{s.transition}, nil
}

func (s *handoffPathStore) RenewCoordinatedInterfaceClaim(context.Context, string, string, time.Duration) error {
	return nil
}

func (s *handoffPathStore) AdvanceCoordinatedInterfaceTransition(_ context.Context, _, _ string, from, to domain.SessionInterfaceTransitionPhase, nativeID, _, _ string, _ bool) error {
	if s.transition.Phase != from {
		return postgres.ErrTransitionStale
	}
	s.transition.Phase = to
	if nativeID != "" {
		s.transition.NativeConversationID = nativeID
	}
	return nil
}

func (s *handoffPathStore) CommitCoordinatedSessionInterface(_ context.Context, _, _, _ string, target domain.SessionInterface) (bool, error) {
	s.committed = target
	return true, nil
}

func (s *handoffPathStore) RollbackCoordinatedSessionInterface(context.Context, string, string, string) error {
	return errors.New("unexpected rollback")
}

func (s *handoffPathStore) CompleteCoordinatedInterfaceTransition(context.Context, string, string) error {
	s.transition.Phase = domain.SessionInterfaceTransitionCompleted
	return nil
}

func (s *handoffPathStore) ReleaseCoordinatedInterfaceClaim(context.Context, string, string) error {
	return nil
}

func (s *handoffPathStore) CreateCoordinatedInterfaceRequest(_ context.Context, _, _, kind string, payload json.RawMessage) (domain.WorkerRequest, error) {
	s.requests = append(s.requests, kind)
	if kind == "interface.start" && s.sourceDone != nil {
		select {
		case <-s.sourceDone:
		default:
			return domain.WorkerRequest{}, errors.New("target start dispatched before source process exited")
		}
	}
	if kind == "interface.stop" && s.stopStarted != nil {
		close(s.stopStarted)
	}
	var input interfacePayload
	if err := json.Unmarshal(payload, &input); err != nil {
		return domain.WorkerRequest{}, err
	}
	response, err := s.supervisor.handleInterface(s.workerCtx, input, kind)
	if err != nil {
		return domain.WorkerRequest{}, err
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return domain.WorkerRequest{}, err
	}
	return domain.WorkerRequest{Kind: kind, Status: "succeeded", Response: raw}, nil
}

type claimDuringHandoffControl struct {
	started   chan struct{}
	release   chan struct{}
	completed chan bool
}

func (c *claimDuringHandoffControl) ClaimTurn(context.Context) (*worker.Turn, error) {
	close(c.started)
	<-c.release
	return &worker.Turn{ID: "turn-1", Attempt: 1}, nil
}

func (c *claimDuringHandoffControl) Credential(context.Context) (worker.CredentialResponse, error) {
	return worker.CredentialResponse{}, errors.New("interrupted turn must not request credentials")
}

func (c *claimDuringHandoffControl) PublishOutput(context.Context, worker.OutputEvent) error {
	return nil
}

func (c *claimDuringHandoffControl) CancellationRequested(context.Context, string, int) (bool, error) {
	return false, nil
}

func (c *claimDuringHandoffControl) CompleteTurn(ctx context.Context, _ string, _ int, cancelled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.completed <- cancelled
	return nil
}

func (c *claimDuringHandoffControl) FailTurn(context.Context, string, int, string) error {
	return errors.New("interrupted turn must be completed, not failed")
}

type unusedHandoffBuilder struct{}

func (unusedHandoffBuilder) Build(context.Context, worker.Turn, worker.CredentialResponse, string) (workerexec.Command, error) {
	return workerexec.Command{}, errors.New("interrupted turn must not launch a provider")
}

func (s *handoffPathStore) GetCoordinatedInterfaceRequestResult(context.Context, string, string, string) (domain.WorkerRequest, error) {
	return domain.WorkerRequest{}, errors.New("worker request completed synchronously")
}

func TestCoordinatorInterruptsBusyTUIBeforeStartingChat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a Unix PTY")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	workspace := t.TempDir()
	terminalID := "00000000-0000-0000-0000-000000000042"
	control := &outputSequenceControl{
		supervisorControlStub: supervisorControlStub{agentSessionID: "native-1"},
		output:                make(chan int64, 8),
	}
	chatStarted := make(chan struct{})
	supervisor := &Supervisor{
		Control: control, Workspace: workspace, DataDir: t.TempDir(),
		AgentCommand: workerexec.Command{
			Path: "/bin/sh", Args: []string{"-c", "printf running; sleep 30"}, Dir: workspace,
		},
		AgentTerminalID: terminalID,
		ChatRunner:      blockingChatRunner{started: chatStarted},
		terminals:       make(map[string]*terminalProcess),
	}
	supervisor.iface.current = InterfaceTUI
	defer supervisor.closeAllTerminals()
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = supervisor.stopChat(stopCtx)
	}()
	if err := supervisor.openTerminal(ctx, worker.TerminalCommand{TerminalID: terminalID, Kind: "agent"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-control.output:
	case <-ctx.Done():
		t.Fatal("busy TUI produced no output")
	}
	supervisor.mu.Lock()
	process := supervisor.terminals[terminalID]
	supervisor.mu.Unlock()
	if process == nil {
		t.Fatal("busy TUI process was not registered")
	}
	store := &handoffPathStore{
		transition: postgres.CoordinatedInterfaceTransition{
			SessionInterfaceTransition: domain.SessionInterfaceTransition{
				ID: "transition-1", OrgID: "org-1", SessionID: "session-1",
				SourceInterface: domain.SessionInterfaceTUI, TargetInterface: domain.SessionInterfaceChat,
				Policy: domain.SessionInterfaceTransitionInterrupt, Phase: domain.SessionInterfaceTransitionRequested,
			},
			Harness: "codex",
		},
		supervisor: supervisor,
		workerCtx:  workerCtx,
		sourceDone: process.done,
	}
	driver := interfacereconcile.NewTransportDriver(store, "owner-1", time.Second, nil)
	coordinator := interfacereconcile.New(store, driver, interfacereconcile.Options{
		StepTimeout: 5 * time.Second, Logger: slog.New(slog.DiscardHandler),
	})
	if err := coordinator.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if store.transition.Phase != domain.SessionInterfaceTransitionCompleted || store.committed != domain.SessionInterfaceChat {
		t.Fatalf("handoff phase=%q committed=%q, want completed Chat", store.transition.Phase, store.committed)
	}
	select {
	case <-process.done:
	default:
		t.Fatal("Chat started before the busy TUI process exited")
	}
	select {
	case <-chatStarted:
	case <-ctx.Done():
		t.Fatal("Chat controller did not start")
	}
	if ready := supervisor.controllerReady(InterfaceChat); !ready.Ready {
		t.Fatalf("Chat controller readiness = %+v, want ready", ready)
	}
	interrupt, stop, start := -1, -1, -1
	for index, kind := range store.requests {
		switch kind {
		case "interface.interrupt":
			interrupt = index
		case "interface.stop":
			stop = index
		case "interface.start":
			start = index
		}
	}
	if interrupt < 0 || stop <= interrupt || start <= stop {
		t.Fatalf("worker command order = %v, want interrupt then stop then start", store.requests)
	}
}

func TestCoordinatorSettlesClaimedChatTurnBeforeStartingTUI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a Unix PTY")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	claim := &claimDuringHandoffControl{
		started: make(chan struct{}), release: make(chan struct{}), completed: make(chan bool, 1),
	}
	chat := &workerexec.Supervisor{
		Control: claim, Builder: unusedHandoffBuilder{}, Runner: workerexec.OSRunner{},
		Workspace: t.TempDir(), PollInterval: time.Millisecond, CompletionRetry: time.Millisecond,
	}
	terminalID := "00000000-0000-0000-0000-000000000043"
	transportControl := &outputSequenceControl{
		supervisorControlStub: supervisorControlStub{agentSessionID: "native-1", agentTerminal: terminalID},
		output:                make(chan int64, 8),
	}
	transport := &Supervisor{
		Control: transportControl, Workspace: t.TempDir(), ChatRunner: chat,
		AgentCommand: workerexec.Command{Path: "/bin/sh", Args: []string{"-c", "printf tui; cat"}},
		terminals:    make(map[string]*terminalProcess),
	}
	defer transport.closeAllTerminals()
	if err := transport.startChat(workerCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-claim.started:
	case <-ctx.Done():
		t.Fatal("Chat claim did not start")
	}
	stopStarted := make(chan struct{})
	store := &handoffPathStore{
		transition: postgres.CoordinatedInterfaceTransition{
			SessionInterfaceTransition: domain.SessionInterfaceTransition{
				ID: "transition-2", OrgID: "org-1", SessionID: "session-1",
				SourceInterface: domain.SessionInterfaceChat, TargetInterface: domain.SessionInterfaceTUI,
				Policy: domain.SessionInterfaceTransitionInterrupt, Phase: domain.SessionInterfaceTransitionRequested,
			},
			Harness: "codex",
		},
		supervisor: transport, workerCtx: workerCtx, stopStarted: stopStarted,
	}
	driver := interfacereconcile.NewTransportDriver(store, "owner-2", time.Second, nil)
	coordinator := interfacereconcile.New(store, driver, interfacereconcile.Options{
		StepTimeout: 5 * time.Second, Logger: slog.New(slog.DiscardHandler),
	})
	done := make(chan error, 1)
	go func() { done <- coordinator.ReconcileOnce(ctx) }()
	select {
	case <-stopStarted:
	case <-ctx.Done():
		t.Fatal("Chat stop did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("handoff completed before claimed Chat turn settled: %v", err)
	default:
	}
	close(claim.release)
	select {
	case cancelled := <-claim.completed:
		if !cancelled {
			t.Fatal("claimed Chat turn was not interrupted")
		}
	case <-ctx.Done():
		t.Fatal("claimed Chat turn was not durably completed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("handoff did not finish after Chat turn completion")
	}
	if store.transition.Phase != domain.SessionInterfaceTransitionCompleted || store.committed != domain.SessionInterfaceTUI {
		t.Fatalf("handoff phase=%q committed=%q, want completed TUI", store.transition.Phase, store.committed)
	}
	select {
	case <-transportControl.output:
	case <-ctx.Done():
		t.Fatal("replacement TUI produced no output")
	}
	if ready := transport.controllerReady(InterfaceTUI); !ready.Ready {
		t.Fatalf("TUI controller readiness = %+v, want ready", ready)
	}
}
