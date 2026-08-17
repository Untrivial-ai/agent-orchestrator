package httpd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

type timeoutProbeSessionService struct {
	controllers.SessionService
	genericBudget chan time.Duration
	switchBudget  chan time.Duration
}

func (s *timeoutProbeSessionService) List(ctx context.Context, _ sessionsvc.ListFilter) ([]domain.Session, error) {
	s.genericBudget <- remainingRequestBudget(ctx)
	return nil, nil
}

func (s *timeoutProbeSessionService) SwitchAgent(
	ctx context.Context,
	id domain.SessionID,
	in sessionsvc.SwitchAgentInput,
) (domain.AgentSwitch, error) {
	s.switchBudget <- remainingRequestBudget(ctx)
	now := time.Now().UTC()
	return domain.AgentSwitch{
		ID:            "switch-timeout-probe",
		SessionID:     id,
		TargetHarness: in.TargetHarness,
		State:         domain.AgentSwitchPreparingHandoff,
		RequestedAt:   now,
		UpdatedAt:     now,
	}, nil
}

func remainingRequestBudget(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	return time.Until(deadline)
}

func TestSwitchAgentRouteUsesOrdinaryTimeoutAndReturnsAccepted(t *testing.T) {
	const configuredTimeout = 250 * time.Millisecond
	svc := &timeoutProbeSessionService{
		genericBudget: make(chan time.Duration, 1),
		switchBudget:  make(chan time.Duration, 1),
	}
	router := NewRouterWithControl(
		config.Config{RequestTimeout: configuredTimeout},
		discardLogger(),
		nil,
		APIDeps{Sessions: svc},
		ControlDeps{},
	)

	genericResponse := httptest.NewRecorder()
	router.ServeHTTP(genericResponse, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if genericResponse.Code != http.StatusOK {
		t.Fatalf("GET sessions status = %d, want 200", genericResponse.Code)
	}
	genericBudget := <-svc.genericBudget

	switchRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/sessions/ao-1/switch-agent",
		bytes.NewBufferString(`{"targetHarness":"codex"}`),
	)
	switchRequest.Header.Set("Content-Type", "application/json")
	switchResponse := httptest.NewRecorder()
	router.ServeHTTP(switchResponse, switchRequest)
	if switchResponse.Code != http.StatusAccepted {
		t.Fatalf("POST switch-agent status = %d, want 202; body=%s", switchResponse.Code, switchResponse.Body.String())
	}
	switchBudget := <-svc.switchBudget
	for name, budget := range map[string]time.Duration{"generic": genericBudget, "switch": switchBudget} {
		if budget <= 0 || budget > configuredTimeout {
			t.Fatalf("%s request budget = %s, want ordinary timeout no greater than %s", name, budget, configuredTimeout)
		}
	}
	if delta := genericBudget - switchBudget; delta < -50*time.Millisecond || delta > 50*time.Millisecond {
		t.Fatalf("generic and switch budgets differ by %s: generic=%s switch=%s", delta, genericBudget, switchBudget)
	}
}
