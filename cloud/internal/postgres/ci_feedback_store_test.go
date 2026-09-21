package postgres

import (
	"encoding/json"
	"strings"
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

func TestCIFailureMessageNamesFailingChecks(t *testing.T) {
	pr := domain.PullRequest{
		Repository: "ao/repo",
		Number:     42,
		Checks: json.RawMessage(`[
			{"name":"unit tests","conclusion":"failure","html_url":"https://example.test/unit"},
			{"name":"lint","conclusion":"success"}
		]`),
	}
	message := ciFailureMessage(pr)
	if !strings.Contains(message, "unit tests") || !strings.Contains(message, "https://example.test/unit") {
		t.Fatalf("message missing failing check details: %q", message)
	}
	if strings.Contains(message, "lint") {
		t.Fatalf("message includes passing check: %q", message)
	}
}

func TestShouldCreateCIFailureEffectOnlyOnTransition(t *testing.T) {
	passing := domain.PullRequest{HeadSHA: "sha-1", CIState: contract.CIPassing}
	failing := domain.PullRequest{HeadSHA: "sha-1", CIState: contract.CIFailing}
	if !shouldCreateCIFailureEffect(passing, failing) {
		t.Fatal("passing to failing should create an effect")
	}
	if shouldCreateCIFailureEffect(failing, failing) {
		t.Fatal("unchanged failure must not create another effect")
	}
	nextHeadFailing := domain.PullRequest{HeadSHA: "sha-2", CIState: contract.CIFailing}
	if !shouldCreateCIFailureEffect(failing, nextHeadFailing) {
		t.Fatal("a new failing head should create a new effect")
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
