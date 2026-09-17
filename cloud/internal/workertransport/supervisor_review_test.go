package workertransport

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

func TestTerminalCommandUsesFreshReviewCommand(t *testing.T) {
	supervisor := Supervisor{
		AgentCommand:  workerexec.Command{Path: "codex", Args: []string{"resume", "active-thread"}},
		ReviewCommand: workerexec.Command{Path: "codex", Args: []string{"--dangerously-bypass-approvals-and-sandbox"}},
	}

	command, _, err := supervisor.terminalCommand(context.Background(), worker.TerminalCommand{
		TerminalID: "review-terminal",
		Kind:       "agent",
		Review:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(command.Args) != 2 || command.Args[0] != "codex" || command.Args[1] != "--dangerously-bypass-approvals-and-sandbox" {
		t.Fatalf("review command args = %#v, want fresh review command", command.Args)
	}
}
