package workertransport

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

func TestReservedAgentTerminalBuffersEarlyInputAndResize(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	supervisor := &Supervisor{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		terminals: map[string]*terminalProcess{},
	}
	supervisor.HoldAgentInputUntilWorkspaceReady()
	if err := supervisor.ConfigureAgent(workerexec.Command{}, "agent-1"); err != nil {
		t.Fatalf("configure agent: %v", err)
	}
	if err := supervisor.writeTerminal(worker.TerminalCommand{
		TerminalID: "agent-1", Data: []byte("queued input"),
	}); err != nil {
		t.Fatalf("queue early input: %v", err)
	}
	if err := supervisor.resizeTerminal(worker.TerminalCommand{
		TerminalID: "agent-1", Columns: 120, Rows: 40,
	}); err != nil {
		t.Fatalf("queue early resize: %v", err)
	}

	// Checkout can finish before StartAgent has opened the PTY. That must retain
	// the buffered terminal requests rather than silently dropping them.
	supervisor.MarkWorkspaceReady()
	if got := len(supervisor.pendingAgentTerminalData); got != 1 {
		t.Fatalf("pending inputs after checkout = %d, want 1", got)
	}
	if got := supervisor.pendingAgentTerminalSize; got == nil || got.Columns != 120 || got.Rows != 40 {
		t.Fatalf("pending resize after checkout = %+v, want 120x40", got)
	}

	supervisor.mu.Lock()
	supervisor.terminals["agent-1"] = &terminalProcess{pty: writer, cancel: func() {}, cleanup: func() {}}
	supervisor.agentStarting = false
	supervisor.agentStarted = true
	supervisor.mu.Unlock()
	supervisor.flushReadyAgentTerminal()

	buffer := make([]byte, len("queued input"))
	if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatalf("read flushed input: %v", err)
	}
	if got := string(buffer); got != "queued input" {
		t.Fatalf("flushed input = %q, want %q", got, "queued input")
	}
	if supervisor.pendingAgentTerminalSize != nil || len(supervisor.pendingAgentTerminalData) != 0 {
		t.Fatalf("pending agent terminal work was not drained")
	}
}

type turnClaimSpy struct {
	Control
	claims int
}

func (s *turnClaimSpy) ClaimTurn(context.Context) (*worker.Turn, error) {
	s.claims++
	return nil, nil
}

func TestForwardTurnWaitsForReservedAgentPTY(t *testing.T) {
	t.Parallel()
	control := &turnClaimSpy{}
	supervisor := &Supervisor{
		Control:         control,
		AgentTerminalID: "agent-1",
		workspaceReady:  true,
		agentStarting:   true,
		holdAgentInput:  true,
	}

	handled, err := supervisor.forwardTurn(context.Background())
	if err != nil || handled {
		t.Fatalf("forward turn while agent starts = (%v, %v), want (false, nil)", handled, err)
	}
	if control.claims != 0 {
		t.Fatalf("claimed %d turns before the agent PTY started", control.claims)
	}

	supervisor.agentStarting = false
	supervisor.agentStarted = true
	handled, err = supervisor.forwardTurn(context.Background())
	if err != nil || handled {
		t.Fatalf("forward turn after agent start = (%v, %v), want (false, nil)", handled, err)
	}
	if control.claims != 1 {
		t.Fatalf("claimed %d turns after the agent PTY started, want 1", control.claims)
	}
}
