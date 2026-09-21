package postgres

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestCIFailureApplicationKeyIsStablePerHead(t *testing.T) {
	pr := domain.PullRequest{ID: "pr-1", HeadSHA: "abc", CIState: contract.CIFailing}
	if got := ciFailureApplicationKey(pr); got != "ci-failure:pr-1:abc" {
		t.Fatalf("key = %q", got)
	}
}

func TestShouldCreateCIFailureEffectOnlyOnTransition(t *testing.T) {
	passing := domain.PullRequest{CIState: contract.CIPassing}
	failing := domain.PullRequest{CIState: contract.CIFailing}
	if !shouldCreateCIFailureEffect(passing, failing) {
		t.Fatal("passing to failing should create an effect")
	}
	if shouldCreateCIFailureEffect(failing, failing) {
		t.Fatal("unchanged failure must not create another effect")
	}
}

func TestShouldResolveCIFailureEffectOnRecovery(t *testing.T) {
	failing := domain.PullRequest{CIState: contract.CIFailing}
	passing := domain.PullRequest{CIState: contract.CIPassing}
	if !shouldResolveCIFailureEffect(failing, passing) {
		t.Fatal("failing to passing should resolve the active notification")
	}
	if shouldResolveCIFailureEffect(passing, passing) {
		t.Fatal("unchanged passing state must not emit a resolution")
	}
}
