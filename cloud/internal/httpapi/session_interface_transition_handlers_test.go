package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/go-chi/chi/v5"
)

type interfaceTransitionHTTPStore struct {
	Store
	session      domain.Session
	transition   domain.SessionInterfaceTransition
	found        bool
	startCalls   int
	advanceCalls int
	ackCalls     int
	err          error
	startErr     error
}

func (f *interfaceTransitionHTTPStore) GetSession(
	context.Context, domain.Principal, string, string,
) (domain.Session, error) {
	return f.session, f.err
}

func (f *interfaceTransitionHTTPStore) GetLatestRelevantSessionInterfaceTransition(
	context.Context, domain.Principal, string, string,
) (domain.SessionInterfaceTransition, bool, error) {
	return f.transition, f.found, f.err
}

func (f *interfaceTransitionHTTPStore) StartSessionInterfaceTransition(
	context.Context, domain.Principal, string, string, domain.SessionInterface,
	domain.SessionInterface, domain.SessionInterfaceTransitionPolicy, string,
) (domain.SessionInterfaceTransition, error) {
	f.startCalls++
	return f.transition, f.startErr
}

func (f *interfaceTransitionHTTPStore) GetActiveSessionInterfaceTransition(context.Context, domain.Principal, string, string) (domain.SessionInterfaceTransition, bool, error) {
	return f.transition, f.found, f.err
}

func (f *interfaceTransitionHTTPStore) AdvanceSessionInterfaceTransition(context.Context, domain.Principal, string, string, domain.SessionInterfaceTransitionPhase, domain.SessionInterfaceTransitionPhase, string, string, string) error {
	f.advanceCalls++
	return f.err
}

func (f *interfaceTransitionHTTPStore) AcknowledgeSessionInterfaceTransitionNotice(context.Context, domain.Principal, string, string, string) error {
	f.ackCalls++
	return f.err
}

func transitionRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("orgId", "00000000-0000-0000-0000-000000000001")
	ctx.URLParams.Add("sessionId", "00000000-0000-0000-0000-000000000002")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
	return r
}

func TestSessionInterfaceTransitionHandlersCoverSuccessAndErrors(t *testing.T) {
	active := domain.SessionInterfaceTransition{ID: "00000000-0000-0000-0000-000000000003", SessionID: "00000000-0000-0000-0000-000000000002", SourceInterface: domain.SessionInterfaceTUI, TargetInterface: domain.SessionInterfaceChat, Policy: domain.SessionInterfaceTransitionDrain, Phase: domain.SessionInterfaceTransitionDraining}
	store := &interfaceTransitionHTTPStore{session: domain.Session{Harness: "codex", Interface: domain.SessionInterfaceTUI}, transition: active, found: true}
	server := &Server{store: store}

	// Direct start success.
	recorder := httptest.NewRecorder()
	server.startSessionInterfaceTransition(recorder, transitionRequest(http.MethodPost, "/", `{"targetMode":"chat","policy":"drain"}`))
	if recorder.Code != http.StatusAccepted || store.startCalls != 1 {
		t.Fatalf("start = %d calls=%d, want 202/1", recorder.Code, store.startCalls)
	}
	// Malformed JSON is rejected before a store call.
	recorder = httptest.NewRecorder()
	server.startSessionInterfaceTransition(recorder, transitionRequest(http.MethodPost, "/", `{`))
	if recorder.Code != http.StatusBadRequest || store.startCalls != 1 {
		t.Fatalf("malformed = %d calls=%d, want 400/1", recorder.Code, store.startCalls)
	}
	// Store failures retain the common error envelope.
	store.err = postgres.ErrNotFound
	recorder = httptest.NewRecorder()
	server.getSessionInterfaceTransition(recorder, transitionRequest(http.MethodGet, "/", ""))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("store error = %d, want 404", recorder.Code)
	}
	store.err = nil

	// Cancel and acknowledgement exercise their terminal handler paths.
	recorder = httptest.NewRecorder()
	server.cancelSessionInterfaceTransition(recorder, transitionRequest(http.MethodDelete, "/", ""))
	if recorder.Code != http.StatusOK || store.advanceCalls != 1 {
		t.Fatalf("cancel = %d calls=%d, want 200/1", recorder.Code, store.advanceCalls)
	}
	recorder = httptest.NewRecorder()
	request := transitionRequest(http.MethodPut, "/", "")
	ctx := chi.RouteContext(request.Context())
	ctx.URLParams.Add("transitionId", "00000000-0000-0000-0000-000000000003")
	server.acknowledgeSessionInterfaceTransitionNotice(recorder, request)
	if recorder.Code != http.StatusOK || store.ackCalls != 1 {
		t.Fatalf("ack = %d calls=%d, want 200/1", recorder.Code, store.ackCalls)
	}
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

func TestStartSessionInterfaceTransitionBothDirectionsAndConflict(t *testing.T) {
	for _, tc := range []struct {
		name, source, target string
	}{
		{"tui to chat", "tui", "chat"},
		{"chat to tui", "chat", "tui"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &interfaceTransitionHTTPStore{session: domain.Session{Harness: "codex", Interface: domain.SessionInterface(tc.source)}}
			server := &Server{store: store}
			recorder := httptest.NewRecorder()
			server.startSessionInterfaceTransition(recorder, transitionRequest(http.MethodPost, "/", `{"targetMode":"`+tc.target+`","policy":"drain"}`))
			if recorder.Code != http.StatusAccepted || store.startCalls != 1 {
				t.Fatalf("start = %d calls=%d, want 202/1", recorder.Code, store.startCalls)
			}
			store.startErr = postgres.ErrTransitionInProgress
			recorder = httptest.NewRecorder()
			server.startSessionInterfaceTransition(recorder, transitionRequest(http.MethodPost, "/", `{"targetMode":"`+tc.target+`","policy":"drain"}`))
			if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "INTERFACE_TRANSITION_IN_PROGRESS") {
				t.Fatalf("conflict = %d %s, want 409 transition conflict", recorder.Code, recorder.Body.String())
			}
		})
	}
}
