package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/go-chi/chi/v5"
)

type messageSequenceStore struct {
	Store
	called         int
	clientSequence int64
}

func (s *messageSequenceStore) SendMessage(
	_ context.Context,
	_ domain.Principal,
	_, _, _, _ string,
	clientSequence int64,
) (domain.ClientEvent, error) {
	s.called++
	s.clientSequence = clientSequence
	return domain.ClientEvent{
		SessionID: testOrchestratorID,
		Sequence:  2,
		Type:      "chat.user_message",
		Payload:   []byte(`{"text":"hello","clientSequence":7}`),
		CreatedAt: time.Now(),
	}, nil
}

func userMessageRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "message-key")
	route := chi.NewRouteContext()
	route.URLParams.Add("orgId", testOrgID)
	route.URLParams.Add("sessionId", testOrchestratorID)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
}

func TestSendMessageAcceptsPositiveClientSequence(t *testing.T) {
	store := &messageSequenceStore{}
	response := httptest.NewRecorder()
	testServer(store).sendMessage(
		response,
		userMessageRequest(`{"text":"hello","clientSequence":7}`),
	)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if store.called != 1 || store.clientSequence != 7 {
		t.Fatalf("store calls = %d, client sequence = %d", store.called, store.clientSequence)
	}
}

func TestSendMessageAcceptsLegacyPayload(t *testing.T) {
	store := &messageSequenceStore{}
	response := httptest.NewRecorder()
	testServer(store).sendMessage(response, userMessageRequest(`{"text":"hello"}`))
	if response.Code != http.StatusAccepted || store.called != 1 || store.clientSequence != 0 {
		t.Fatalf("status=%d calls=%d sequence=%d", response.Code, store.called, store.clientSequence)
	}
}

func TestSendMessageRejectsMalformedClientSequence(t *testing.T) {
	for _, sequence := range []string{`1.5`, `"7"`, `true`} {
		store := &messageSequenceStore{}
		response := httptest.NewRecorder()
		testServer(store).sendMessage(response, userMessageRequest(`{"text":"hello","clientSequence":`+sequence+`}`))
		if response.Code != http.StatusBadRequest || store.called != 0 {
			t.Fatalf("sequence=%s status=%d calls=%d", sequence, response.Code, store.called)
		}
	}
}

func TestSendMessageRejectsInvalidClientSequence(t *testing.T) {
	for _, body := range []string{
		`{"text":"hello","clientSequence":null}`,
		`{"text":"hello","clientSequence":0}`,
		`{"text":"hello","clientSequence":-1}`,
		`{"text":"hello","clientSequence":9007199254740992}`,
	} {
		store := &messageSequenceStore{}
		response := httptest.NewRecorder()
		testServer(store).sendMessage(response, userMessageRequest(body))
		if response.Code != http.StatusUnprocessableEntity || store.called != 0 {
			t.Fatalf("body %s produced status %d and %d store calls", body, response.Code, store.called)
		}
	}
}
