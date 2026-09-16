package httpapi

import (
	"testing"
	"time"

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

// A cloud session with a PR presents the same PR list and SCM status the
// local daemon would give it.
func TestSessionChildResponseDerivesPRPresentation(t *testing.T) {
	pr := domain.PullRequest{
		URL: "https://github.com/octo/widgets/pull/7", Number: 7,
		State: contract.PRStateOpen, CIState: contract.CIFailing,
		ReviewState: contract.ReviewNone, Mergeability: contract.MergeBlocked,
		SourceBranch: "ao/session-1", TargetBranch: "main", UpdatedAt: time.Now(),
	}
	facts := []contract.PRFacts{{
		URL: pr.URL, CI: pr.CIState, Review: pr.ReviewState, Mergeability: pr.Mergeability,
		SourceBranch: pr.SourceBranch, TargetBranch: pr.TargetBranch,
	}}
	response := toSessionChildResponse(domain.Session{
		ID: "session-1", ActivityState: contract.ActivityIdle, UpdatedAt: time.Now(),
	}, facts, []domain.PullRequest{pr})
	if len(response.PRs) != 1 || response.PRs[0].Number != 7 || response.PRs[0].CI != "failing" {
		t.Fatalf("prs = %+v", response.PRs)
	}
	if response.SCMStatus != string(contract.StatusCIFailed) {
		t.Fatalf("scmStatus = %q, want %q", response.SCMStatus, contract.StatusCIFailed)
	}
}

func TestSessionChildResponseWithoutPRsHasNoSCMStatus(t *testing.T) {
	response := toSessionChildResponse(domain.Session{ID: "session-1", UpdatedAt: time.Now()}, nil, nil)
	if response.SCMStatus != "" || len(response.PRs) != 0 {
		t.Fatalf("response = %+v", response)
	}
}
