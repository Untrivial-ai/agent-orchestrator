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
	switchDelay   time.Duration
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
	timer := time.NewTimer(s.switchDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return domain.AgentSwitch{}, ctx.Err()
	}

	now := time.Now().UTC()
	return domain.AgentSwitch{
		ID:            "switch-timeout-probe",
		SessionID:     id,
		TargetHarness: in.TargetHarness,
		State:         domain.AgentSwitchCompleted,
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

func TestSwitchAgentRouteOutlivesGenericRequestTimeout(t *testing.T) {
	const (
		genericTimeout = 20 * time.Millisecond
		switchDelay    = 75 * time.Millisecond
	)

	svc := &timeoutProbeSessionService{
		genericBudget: make(chan time.Duration, 1),
		switchBudget:  make(chan time.Duration, 1),
		switchDelay:   switchDelay,
	}
	router := NewRouterWithControl(
		config.Config{RequestTimeout: genericTimeout},
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
	if genericBudget <= 0 || genericBudget > 2*genericTimeout {
		t.Fatalf("generic request budget = %s, want approximately %s", genericBudget, genericTimeout)
	}

	started := time.Now()
	switchRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/sessions/ao-1/switch-agent",
		bytes.NewBufferString(`{"targetHarness":"codex"}`),
	)
	switchRequest.Header.Set("Content-Type", "application/json")
	switchResponse := httptest.NewRecorder()
	router.ServeHTTP(switchResponse, switchRequest)
	if switchResponse.Code != http.StatusOK {
		t.Fatalf("POST switch-agent status = %d, want 200; body=%s", switchResponse.Code, switchResponse.Body.String())
	}
	if elapsed := time.Since(started); elapsed < switchDelay {
		t.Fatalf("POST switch-agent completed in %s, want at least %s", elapsed, switchDelay)
	}
	switchBudget := <-svc.switchBudget
	if switchBudget < minimumSwitchAgentRequestTimeout-time.Second {
		t.Fatalf("switch request budget = %s, want at least %s", switchBudget, minimumSwitchAgentRequestTimeout)
	}
}

func TestSwitchAgentRoutePreservesLongerConfiguredTimeout(t *testing.T) {
	configuredTimeout := 8 * time.Minute
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

	switchRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/sessions/ao-1/switch-agent",
		bytes.NewBufferString(`{"targetHarness":"codex"}`),
	)
	switchRequest.Header.Set("Content-Type", "application/json")
	switchResponse := httptest.NewRecorder()
	router.ServeHTTP(switchResponse, switchRequest)
	if switchResponse.Code != http.StatusOK {
		t.Fatalf("POST switch-agent status = %d, want 200; body=%s", switchResponse.Code, switchResponse.Body.String())
	}
	if switchBudget := <-svc.switchBudget; switchBudget < configuredTimeout-time.Second {
		t.Fatalf("switch request budget = %s, want approximately %s", switchBudget, configuredTimeout)
	}
}
