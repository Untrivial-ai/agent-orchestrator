package workertransport

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

type supervisorControlStub struct {
	claimTurnCalls int
	turn           *worker.Turn
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
