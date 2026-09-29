package httpapi

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestSessionResponseIncludesSandboxLifecycleContract(t *testing.T) {
	response := toSessionResponse(domain.Session{
		ID: "session-1", SandboxProvider: "coder",
		DesiredState: "paused", ObservedState: "stopped",
	}, nil)
	if response.SandboxProvider != "coder" ||
		response.DesiredState != "paused" ||
		response.ObservedState != "stopped" {
		t.Fatalf("lifecycle response = %+v", response)
	}
}

func TestSessionPRFactsResponseIncludesUnresolvedReviewComments(t *testing.T) {
	responses := toSessionPRFactsResponses(
		[]domain.PullRequest{{URL: "https://github.test/octo/widgets/pull/7", Number: 7}},
		[]contract.PRFacts{{URL: "https://github.test/octo/widgets/pull/7", ReviewComments: true}},
	)
	if len(responses) != 1 || !responses[0].ReviewComments {
		t.Fatalf("responses = %+v, want unresolved review comments", responses)
	}
}
