package main

import (
	"fmt"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/google/uuid"
)

func reviewerLaunch(base worker.LaunchContext, input worker.TerminalCommand) (worker.LaunchContext, error) {
	if _, err := uuid.Parse(input.ReviewRunID); err != nil || input.Reviewer == nil {
		return worker.LaunchContext{}, fmt.Errorf("invalid reviewer launch context")
	}
	if err := domain.ValidateProjectAgent(input.Reviewer.Harness, input.Reviewer.AgentConfig); err != nil {
		return worker.LaunchContext{}, err
	}
	base.SessionID = input.ReviewRunID
	base.Kind = "reviewer"
	base.Harness = input.Reviewer.Harness
	base.AgentConfig = input.Reviewer.AgentConfig
	base.Model = input.Reviewer.AgentConfig.Model
	base.AgentSessionID = ""
	base.ParentSessionID = ""
	base.Prompt = ""
	base.SystemPrompt = ""
	return base, nil
}

func reviewHelp() string {
	return "curl --unix-socket $AO_REVIEW_SOCKET " +
		`-X POST http://localhost/review -H 'Content-Type: application/json' ` +
		`-d '{"reviewRunId":"<review run id from the prompt>","verdict":"approved|changes_requested","body":"<your findings>"}' ` +
		"to submit an AO-triggered review verdict."
}
