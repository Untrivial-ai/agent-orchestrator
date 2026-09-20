package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestRunHookDoesNotPublishReviewerActivity(t *testing.T) {
	t.Setenv(worker.ReviewTerminalEnv, "1")

	err := runHook(
		context.Background(),
		nil,
		[]string{"codex", "user-prompt-submit"},
		strings.NewReader(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
}
