package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/go-chi/chi/v5"
)

type interfaceTransitionHTTPStore struct {
	Store
	session    domain.Session
	transition domain.SessionInterfaceTransition
	found      bool
	startCalls int
}

func (f *interfaceTransitionHTTPStore) GetSession(
	context.Context, domain.Principal, string, string,
) (domain.Session, error) {
	return f.session, nil
}

func (f *interfaceTransitionHTTPStore) GetLatestRelevantSessionInterfaceTransition(
	context.Context, domain.Principal, string, string,
) (domain.SessionInterfaceTransition, bool, error) {
	return f.transition, f.found, nil
}

func (f *interfaceTransitionHTTPStore) StartSessionInterfaceTransition(
	context.Context, domain.Principal, string, string, domain.SessionInterface,
	domain.SessionInterface, domain.SessionInterfaceTransitionPolicy, string,
) (domain.SessionInterfaceTransition, error) {
	f.startCalls++
	return f.transition, nil
}

func transitionRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("orgId", "00000000-0000-0000-0000-000000000001")
	ctx.URLParams.Add("sessionId", "00000000-0000-0000-0000-000000000002")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
	return r
}

func TestGetSessionInterfaceTransitionMarksTerminatedSessionUnsupported(t *testing.T) {
	store := &interfaceTransitionHTTPStore{session: domain.Session{
		Harness:      "codex",
		Interface:    domain.SessionInterfaceTUI,
		IsTerminated: true,
	}}
	server := &Server{store: store}
	recorder := httptest.NewRecorder()
	server.getSessionInterfaceTransition(recorder, transitionRequest(http.MethodGet, "/", ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response interfaceTransitionStatusResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Supported {
		t.Fatal("terminated session must not advertise interface handoff support")
	}
	if response.ReasonCode != "SESSION_TERMINATED" {
		t.Fatalf("reason code = %q, want SESSION_TERMINATED", response.ReasonCode)
	}
}

func TestStartSessionInterfaceTransitionRejectsTerminatedSession(t *testing.T) {
	store := &interfaceTransitionHTTPStore{session: domain.Session{
		Harness:      "codex",
		Interface:    domain.SessionInterfaceTUI,
		IsTerminated: true,
	}}
	server := &Server{store: store}
	recorder := httptest.NewRecorder()
	server.startSessionInterfaceTransition(
		recorder,
		transitionRequest(http.MethodPost, "/", `{"targetMode":"chat","policy":"drain"}`),
	)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	var response errorEnvelope
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != "SESSION_TERMINATED" {
		t.Fatalf("error code = %q, want SESSION_TERMINATED", response.Code)
	}
	if store.startCalls != 0 {
		t.Fatal("terminated session must be rejected before creating a transition")
	}
}
