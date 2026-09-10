package controllers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func TestSessionCISummaryCarriesBlockingReason(t *testing.T) {
	result := newSessionPRCISummary(sessionsvc.PRCISummary{
		State:         domain.CIUnknown,
		BlockedChecks: []contract.PullRequestBlockedCheck{{Name: "build", Reason: domain.CIBillingBlockedReason, URL: "https://checks.example/build"}},
	})
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"blockedChecks":[{"name":"build","reason":"`+domain.CIBillingBlockedReason+`","url":"https://checks.example/build"}]`) {
		t.Fatalf("blocking reason missing from wire response: %s", body)
	}
	if strings.Contains(string(body), "logTail") {
		t.Fatalf("raw logs entered summary: %s", body)
	}
}
