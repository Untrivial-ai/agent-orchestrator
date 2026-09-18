package workertransport

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

func TestTerminalCommandStartsReviewWithInitialPrompt(t *testing.T) {
	supervisor := Supervisor{
		AgentCommand:  workerexec.Command{Path: "codex", Args: []string{"resume", "active-thread"}},
		ReviewCommand: workerexec.Command{Path: "codex", Args: []string{"--dangerously-bypass-approvals-and-sandbox"}},
	}

	command, _, err := supervisor.terminalCommand(context.Background(), worker.TerminalCommand{
		TerminalID: "review-terminal",
		Kind:       "agent",
		Review:     true,
		Data:       []byte("review this change"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "review this change"}
	if len(command.Args) != len(want) {
		t.Fatalf("review command args = %#v, want %#v", command.Args, want)
	}
	for index, arg := range want {
		if command.Args[index] != arg {
			t.Fatalf("review command args = %#v, want %#v", command.Args, want)
		}
	}
	if got := supervisor.ReviewCommand.Args; len(got) != 1 || got[0] != "--dangerously-bypass-approvals-and-sandbox" {
		t.Fatalf("review command config was mutated: %#v", got)
	}
}
