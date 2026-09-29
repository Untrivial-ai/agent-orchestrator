package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	automationsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/automation"
)

type fakeAutomationService struct {
	created automationsvc.CreateInput
	items   []domain.Automation
	deleted domain.AutomationID
}

func (f *fakeAutomationService) Create(_ context.Context, input automationsvc.CreateInput) (domain.Automation, error) {
	f.created = input
	return domain.Automation{ID: "automation-1", ProjectID: input.ProjectID, DisplayName: input.DisplayName, Prompt: input.Prompt, Kind: input.Kind, Timezone: input.Timezone, Enabled: true, NextRunAt: time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeAutomationService) Get(_ context.Context, id domain.AutomationID) (domain.Automation, error) {
	return f.items[0], nil
}
func (f *fakeAutomationService) List(_ context.Context, filter domain.AutomationFilter) (domain.AutomationPage, error) {
	return domain.AutomationPage{Items: f.items, Total: int64(len(f.items))}, nil
}
func (f *fakeAutomationService) Update(_ context.Context, id domain.AutomationID, input automationsvc.UpdateInput) (domain.Automation, error) {
	return f.items[0], nil
}
func (f *fakeAutomationService) Delete(_ context.Context, id domain.AutomationID) error {
	f.deleted = id
	return nil
}
func (f *fakeAutomationService) Runs(_ context.Context, filter domain.AutomationRunFilter) (domain.AutomationRunPage, error) {
	return domain.AutomationRunPage{}, nil
}

func automationTestRouter(service AutomationService) http.Handler {
	r := chi.NewRouter()
	(&AutomationsController{Svc: service}).Register(r)
	return r
}

// Request decoding must preserve the schedule choice and route through the
// automation service rather than launching a session from the controller.
func TestAutomationsCreateReturnsDefinition(t *testing.T) {
	service := &fakeAutomationService{}
	body := `{"projectId":"demo","displayName":"Morning","prompt":"Review","kind":"worker","cron":"0 9 * * *","timezone":"Asia/Calcutta"}`
	req := httptest.NewRequest(http.MethodPost, "/automations", strings.NewReader(body))
	rec := httptest.NewRecorder()
	automationTestRouter(service).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.created.ProjectID != "demo" || service.created.Cron != "0 9 * * *" || service.created.Timezone != "Asia/Calcutta" {
		t.Fatalf("create input = %#v", service.created)
	}
	var response AutomationEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Automation.ID != "automation-1" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestAutomationsDeleteReturnsNoContent(t *testing.T) {
	service := &fakeAutomationService{}
	req := httptest.NewRequest(http.MethodDelete, "/automations/automation-1", nil)
	rec := httptest.NewRecorder()
	automationTestRouter(service).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || service.deleted != "automation-1" {
		t.Fatalf("status=%d deleted=%q body=%s", rec.Code, service.deleted, rec.Body.String())
	}
}
