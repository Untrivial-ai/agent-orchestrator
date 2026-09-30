package workertransport

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

func TestReviewTerminalResolvesIndependentCommand(t *testing.T) {
	supervisor := Supervisor{AgentCommand: workerexec.Command{Path: "/bin/sh", Args: []string{"-c", "printf worker"}, Env: map[string]string{"ROLE": "worker"}}}
	supervisor.ReviewCommand = func(_ context.Context, input worker.TerminalCommand) (workerexec.Command, error) {
		if input.Reviewer.Harness != "claude-code" || input.ReviewRunID != "review" {
			t.Fatalf("review settings=%+v", input)
		}
		return workerexec.Command{Path: "/bin/sh", Args: []string{"-c", "printf reviewer"}, Env: map[string]string{"ROLE": "reviewer"}}, nil
	}
	command, cleanup, err := supervisor.terminalCommand(context.Background(), worker.TerminalCommand{Kind: "reviewer", ReviewRunID: "review", Reviewer: &domain.ProjectReviewer{Harness: "claude-code"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	output, err := command.Output()
	if err != nil || string(output) != "reviewer" || supervisor.AgentCommand.Args[1] != "printf worker" {
		t.Fatalf("review output=%q err=%v worker=%+v", output, err, supervisor.AgentCommand)
	}
	supervisor.ReviewCommand = nil
	if _, _, err := supervisor.terminalCommand(context.Background(), worker.TerminalCommand{Kind: "reviewer"}); err == nil {
		t.Fatal("missing review configuration fell back to worker command")
	}
}
