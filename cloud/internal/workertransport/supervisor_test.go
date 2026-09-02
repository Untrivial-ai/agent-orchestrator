package workertransport

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

type supervisorControlStub struct {
	claimTurnCalls int
	turn           *worker.Turn
	agentSessionID string
}

type chatRunnerStub struct{ idle bool }

func (s chatRunnerStub) Run(context.Context) error { return nil }
func (s chatRunnerStub) Idle() bool                { return s.idle }

type blockingChatRunner struct{ started chan struct{} }

func (r blockingChatRunner) Run(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	return nil
}

type delayedStoppingChatRunner struct {
	started chan struct{}
	stopped chan struct{}
	release chan struct{}
}

func (r delayedStoppingChatRunner) Run(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	close(r.stopped)
	<-r.release
	return nil
}

func (s *supervisorControlStub) ClaimTransport(context.Context) (*worker.TransportRequest, error) {
	return nil, nil
}

func (s *supervisorControlStub) ClaimTurn(context.Context) (*worker.Turn, error) {
	s.claimTurnCalls++
	return s.turn, nil
}

func (s *supervisorControlStub) CompleteTurn(context.Context, string, int, bool) error { return nil }
func (s *supervisorControlStub) FailTurn(context.Context, string, int, string) error   { return nil }
func (s *supervisorControlStub) CompleteTransport(context.Context, string, int, any) error {
	return nil
}
func (s *supervisorControlStub) FailTransport(context.Context, string, int, string, string) error {
	return nil
}
func (s *supervisorControlStub) PublishTerminalOutput(context.Context, string, []byte) error {
	return nil
}
func (s *supervisorControlStub) PublishTerminalExit(context.Context, string, int, bool) error {
	return nil
}
func (s *supervisorControlStub) AgentSessionID(context.Context) (string, error) {
	return s.agentSessionID, nil
}

func TestRefreshAgentCommandUsesLatestConversationID(t *testing.T) {
	var gotID string
	supervisor := &Supervisor{
		AgentCommand: workerexec.Command{Path: "stale-codex"},
		AgentCommandFactory: func(_ context.Context, nativeConversationID string) (workerexec.Command, error) {
			gotID = nativeConversationID
			return workerexec.Command{Path: "codex", Args: []string{"resume", nativeConversationID}}, nil
		},
	}
	if err := supervisor.refreshAgentCommand(context.Background(), "native-chat"); err != nil {
		t.Fatalf("refresh agent command: %v", err)
	}
	if gotID != "native-chat" {
		t.Fatalf("factory native conversation id = %q, want native-chat", gotID)
	}
	if supervisor.AgentCommand.Path != "codex" {
		t.Fatalf("refreshed command path = %q, want codex", supervisor.AgentCommand.Path)
	}
	if len(supervisor.AgentCommand.Args) != 2 || supervisor.AgentCommand.Args[1] != "native-chat" {
		t.Fatalf("refreshed command args = %v, want resume native-chat", supervisor.AgentCommand.Args)
	}
}

func TestOpenAgentTerminalStartsWithoutDeadlocking(t *testing.T) {
	workspace := t.TempDir()
	supervisor := &Supervisor{
		Control:   &supervisorControlStub{},
		Workspace: workspace,
		AgentCommand: workerexec.Command{
			Path: "/bin/sh",
			Args: []string{"-c", "sleep 30"},
			Dir:  workspace,
		},
		terminals: make(map[string]*terminalProcess),
	}

	done := make(chan error, 1)
	go func() {
		done <- supervisor.openTerminal(context.Background(), worker.TerminalCommand{
			TerminalID: "00000000-0000-0000-0000-000000000001",
			Kind:       "agent",
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("open agent terminal: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("open agent terminal deadlocked before starting the PTY")
	}

	supervisor.closeAllTerminals()
}

func TestStopInterfaceWaitsForInteractiveProcessExit(t *testing.T) {
	workspace := t.TempDir()
	supervisor := &Supervisor{
		Control:   &supervisorControlStub{},
		Workspace: workspace,
		AgentCommand: workerexec.Command{
			Path: "/bin/sh",
			Args: []string{"-c", "sleep 30"},
			Dir:  workspace,
		},
		AgentTerminalID: "00000000-0000-0000-0000-000000000002",
		terminals:       make(map[string]*terminalProcess),
	}
	supervisor.iface.current = InterfaceTUI

	if err := supervisor.openTerminal(context.Background(), worker.TerminalCommand{
		TerminalID: supervisor.AgentTerminalID,
		Kind:       "agent",
	}); err != nil {
		t.Fatalf("open agent terminal: %v", err)
	}
	supervisor.mu.Lock()
	process := supervisor.terminals[supervisor.AgentTerminalID]
	supervisor.mu.Unlock()
	if process == nil {
		t.Fatal("agent terminal was not registered")
	}

	if err := supervisor.stopInterface(context.Background()); err != nil {
		t.Fatalf("stop interface: %v", err)
	}
	select {
	case <-process.done:
	default:
		t.Fatal("stop interface returned before the interactive process exited")
	}
}

func TestStopInterfaceWaitsForChatProcessExit(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	release := make(chan struct{})
	supervisor := &Supervisor{
		ChatRunner: delayedStoppingChatRunner{
			started: started, stopped: stopped, release: release,
		},
	}
	supervisor.iface.current = InterfaceChat
	if err := supervisor.startChat(context.Background()); err != nil {
		t.Fatalf("start chat: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("chat controller did not start")
	}

	done := make(chan error, 1)
	go func() { done <- supervisor.stopInterface(context.Background()) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("chat controller did not receive stop")
	}
	select {
	case err := <-done:
		t.Fatalf("stop interface returned before chat process exit: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stop interface: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop interface did not wait for chat process exit")
	}
}

func TestNativeConversationIDRefreshesFromControlPlane(t *testing.T) {
	supervisor := &Supervisor{Control: &supervisorControlStub{agentSessionID: "native-chat"}}
	got := supervisor.nativeConversationID(context.Background(), interfacePayload{})
	if got != "native-chat" {
		t.Fatalf("native conversation id = %q, want native-chat", got)
	}
}

func TestNativeConversationIDPrefersRefreshedControlPlaneID(t *testing.T) {
	supervisor := &Supervisor{
		Control:        &supervisorControlStub{agentSessionID: "native-chat"},
		AgentSessionID: "stale-bootstrap-id",
	}
	if got := supervisor.nativeConversationID(context.Background(), interfacePayload{}); got != "native-chat" {
		t.Fatalf("native conversation id = %q, want refreshed control-plane id", got)
	}
}

func TestForwardTurnLeavesQueueToChatController(t *testing.T) {
	control := &supervisorControlStub{turn: &worker.Turn{ID: "turn-1", Attempt: 1}}
	supervisor := &Supervisor{Control: control}
	supervisor.iface.current = InterfaceChat

	handled, err := supervisor.forwardTurn(context.Background())
	if err != nil {
		t.Fatalf("forward turn: %v", err)
	}
	if handled {
		t.Fatal("expected transport supervisor not to handle turns in Chat mode")
	}
	if control.claimTurnCalls != 0 {
		t.Fatalf("expected Chat controller to own the queue, got %d claims", control.claimTurnCalls)
	}
}

func TestInspectInterfaceDrainsOnlyActiveChatWork(t *testing.T) {
	tests := []struct {
		name string
		idle bool
		want bool
	}{
		{name: "idle chat controller", idle: true, want: true},
		{name: "running chat turn", idle: false, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			supervisor := &Supervisor{ChatRunner: chatRunnerStub{idle: test.idle}}
			supervisor.iface.current = InterfaceChat

			result, err := supervisor.inspectInterface()
			if err != nil {
				t.Fatalf("inspect interface: %v", err)
			}
			inspection := result.(interfaceInspectResult)
			if inspection.Idle != test.want {
				t.Fatalf("idle = %v, want %v", inspection.Idle, test.want)
			}
		})
	}
}

func TestRunRestartsChatControllerForChatSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan error, 1)
	runnerStarted := make(chan struct{})
	supervisor := &Supervisor{
		Control:          &supervisorControlStub{},
		Workspace:        t.TempDir(),
		Started:          started,
		InitialInterface: InterfaceChat,
		ChatRunner:       blockingChatRunner{started: runnerStarted},
		PollInterval:     time.Millisecond,
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	if err := <-started; err != nil {
		t.Fatalf("start chat worker: %v", err)
	}
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("chat controller was not started for a Chat session")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("transport supervisor did not stop")
	}
}
