package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

type reviewerConversationStub struct {
	*fakeConversationService
	snapshot         chatsvc.Snapshot
	snapshotReviewID string
	snapshotBefore   int64
	snapshotLimit    int64
	snapshotErr      error
	sendOwner        domain.ConversationOwner
	sendMessage      ports.ChatUserMessage
	sendErr          error
	resolveOwner     domain.ConversationOwner
	resolveRequestID string
	resolveDecision  ports.ChatDecision
	resolveErr       error
	inputOwner       domain.ConversationOwner
	inputRequestID   string
	inputResponse    ports.ChatInputResponse
	inputErr         error
	interruptOwner   domain.ConversationOwner
	interruptErr     error
}

func newReviewerConversationStub() *reviewerConversationStub {
	return &reviewerConversationStub{fakeConversationService: &fakeConversationService{}}
}

func (s *reviewerConversationStub) SnapshotForReview(context.Context, string) (chatsvc.Snapshot, error) {
	return s.snapshot, s.snapshotErr
}

func (s *reviewerConversationStub) SnapshotPageForReview(_ context.Context, reviewID string, beforeSequence, limit int64) (chatsvc.Snapshot, error) {
	s.snapshotReviewID = reviewID
	s.snapshotBefore = beforeSequence
	s.snapshotLimit = limit
	return s.snapshot, s.snapshotErr
}

func (s *reviewerConversationStub) SendForOwner(_ context.Context, owner domain.ConversationOwner, message ports.ChatUserMessage) (domain.ConversationTurn, error) {
	s.sendOwner = owner
	s.sendMessage = message
	return domain.ConversationTurn{ID: "review-turn-1", ProviderTurnID: "provider-turn-1", State: domain.TurnStateRunning}, s.sendErr
}

func (s *reviewerConversationStub) ResolveForOwner(_ context.Context, owner domain.ConversationOwner, requestID string, decision ports.ChatDecision) error {
	s.resolveOwner = owner
	s.resolveRequestID = requestID
	s.resolveDecision = decision
	return s.resolveErr
}

func (s *reviewerConversationStub) ResolveInputForOwner(_ context.Context, owner domain.ConversationOwner, requestID string, response ports.ChatInputResponse) error {
	s.inputOwner = owner
	s.inputRequestID = requestID
	s.inputResponse = response
	return s.inputErr
}

func (s *reviewerConversationStub) InterruptForOwner(_ context.Context, owner domain.ConversationOwner) error {
	s.interruptOwner = owner
	return s.interruptErr
}

func reviewerConversationTestServer(t *testing.T, service *reviewerConversationStub) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Sessions: newFakeSessionService(), Conversations: service,
	}, httpd.ControlDeps{}))
	t.Cleanup(server.Close)
	return server
}

func TestReviewerConversationRoutesDispatchToReviewOwner(t *testing.T) {
	service := newReviewerConversationStub()
	service.snapshot = chatsvc.Snapshot{
		Conversation: domain.ConversationRecord{ID: "review-conversation-1", LatestSequence: 88},
		SessionID:    "worker-1", Mode: domain.SessionModeChat,
	}
	server := reviewerConversationTestServer(t, service)

	t.Run("snapshot page", func(t *testing.T) {
		body, status, _ := doRequest(t, server, http.MethodGet,
			"/api/v1/reviews/review-1/conversation?beforeSequence=42&limit=25", "")
		if status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", status, body)
		}
		var response struct {
			ConversationID string `json:"conversationId"`
		}
		mustJSON(t, body, &response)
		if response.ConversationID != "review-conversation-1" || service.snapshotReviewID != "review-1" || service.snapshotBefore != 42 || service.snapshotLimit != 25 {
			t.Fatalf("snapshot response=%+v call=(%q,%d,%d)", response, service.snapshotReviewID, service.snapshotBefore, service.snapshotLimit)
		}
	})

	t.Run("send", func(t *testing.T) {
		body, status, _ := doRequest(t, server, http.MethodPost,
			"/api/v1/reviews/review-1/conversation/messages",
			`{"text":"review again","clientMessageId":"client-1"}`)
		if status != http.StatusAccepted {
			t.Fatalf("status = %d, body = %s", status, body)
		}
		var response struct {
			TurnID string `json:"turnId"`
		}
		mustJSON(t, body, &response)
		if response.TurnID != "review-turn-1" || service.sendOwner != domain.ReviewConversationOwner("review-1") || service.sendMessage.Text != "review again" || service.sendMessage.ClientMessageID != "client-1" {
			t.Fatalf("send response=%+v owner=%+v message=%+v", response, service.sendOwner, service.sendMessage)
		}
	})

	t.Run("approval", func(t *testing.T) {
		body, status, _ := doRequest(t, server, http.MethodPost,
			"/api/v1/reviews/review-1/conversation/approvals/approval-1/resolve",
			`{"decisionId":"allow"}`)
		if status != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", status, body)
		}
		if service.resolveOwner != domain.ReviewConversationOwner("review-1") || service.resolveRequestID != "approval-1" || service.resolveDecision.ID != "allow" {
			t.Fatalf("resolve owner=%+v request=%q decision=%+v", service.resolveOwner, service.resolveRequestID, service.resolveDecision)
		}
	})

	t.Run("input", func(t *testing.T) {
		body, status, _ := doRequest(t, server, http.MethodPost,
			"/api/v1/reviews/review-1/conversation/inputs/input-1/resolve",
			`{"action":"accept","content":{"choice":"native"}}`)
		if status != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", status, body)
		}
		if service.inputOwner != domain.ReviewConversationOwner("review-1") || service.inputRequestID != "input-1" || service.inputResponse.Action != ports.ChatInputActionAccept || service.inputResponse.Content["choice"] != "native" {
			t.Fatalf("input owner=%+v request=%q response=%+v", service.inputOwner, service.inputRequestID, service.inputResponse)
		}
	})

	t.Run("interrupt", func(t *testing.T) {
		body, status, _ := doRequest(t, server, http.MethodPost,
			"/api/v1/reviews/review-1/conversation/interrupt", "")
		if status != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", status, body)
		}
		if service.interruptOwner != domain.ReviewConversationOwner("review-1") {
			t.Fatalf("interrupt owner = %+v", service.interruptOwner)
		}
	})
}

func TestReviewerConversationRouteValidation(t *testing.T) {
	server := reviewerConversationTestServer(t, newReviewerConversationStub())
	tests := []struct {
		name, method, path, body, code string
	}{
		{name: "cursor", method: http.MethodGet, path: "/api/v1/reviews/review-1/conversation?beforeSequence=0", code: "CONVERSATION_CURSOR_INVALID"},
		{name: "limit", method: http.MethodGet, path: "/api/v1/reviews/review-1/conversation?limit=501", code: "CONVERSATION_LIMIT_INVALID"},
		{name: "empty message", method: http.MethodPost, path: "/api/v1/reviews/review-1/conversation/messages", body: `{}`, code: "CHAT_MESSAGE_EMPTY"},
		{name: "missing decision", method: http.MethodPost, path: "/api/v1/reviews/review-1/conversation/approvals/request-1/resolve", body: `{}`, code: "CHAT_DECISION_REQUIRED"},
		{name: "invalid input action", method: http.MethodPost, path: "/api/v1/reviews/review-1/conversation/inputs/request-1/resolve", body: `{"action":"retry"}`, code: "CHAT_INPUT_ACTION_INVALID"},
		{name: "content on decline", method: http.MethodPost, path: "/api/v1/reviews/review-1/conversation/inputs/request-1/resolve", body: `{"action":"decline","content":{"choice":"native"}}`, code: "CHAT_INPUT_CONTENT_INVALID"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, status, _ := doRequest(t, server, tc.method, tc.path, tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", status, body)
			}
			var response struct {
				Code      string `json:"code"`
				RequestID string `json:"requestId"`
			}
			mustJSON(t, body, &response)
			if response.Code != tc.code || response.RequestID == "" {
				t.Fatalf("error = %+v, want code %q and requestId", response, tc.code)
			}
		})
	}
}

func TestReviewerConversationRoutesMapServiceErrors(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		setError                 func(*reviewerConversationStub)
	}{
		{name: "snapshot", method: http.MethodGet, path: "/api/v1/reviews/review-1/conversation", setError: func(s *reviewerConversationStub) { s.snapshotErr = ports.ErrSessionNotFound }},
		{name: "send", method: http.MethodPost, path: "/api/v1/reviews/review-1/conversation/messages", body: `{"text":"review"}`, setError: func(s *reviewerConversationStub) { s.sendErr = ports.ErrSessionNotFound }},
		{name: "approval", method: http.MethodPost, path: "/api/v1/reviews/review-1/conversation/approvals/request-1/resolve", body: `{"decisionId":"allow"}`, setError: func(s *reviewerConversationStub) { s.resolveErr = ports.ErrSessionNotFound }},
		{name: "input", method: http.MethodPost, path: "/api/v1/reviews/review-1/conversation/inputs/request-1/resolve", body: `{"action":"cancel"}`, setError: func(s *reviewerConversationStub) { s.inputErr = ports.ErrSessionNotFound }},
		{name: "interrupt", method: http.MethodPost, path: "/api/v1/reviews/review-1/conversation/interrupt", setError: func(s *reviewerConversationStub) { s.interruptErr = ports.ErrSessionNotFound }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newReviewerConversationStub()
			tc.setError(service)
			server := reviewerConversationTestServer(t, service)
			body, status, _ := doRequest(t, server, tc.method, tc.path, tc.body)
			if status != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s", status, body)
			}
			var response struct {
				Error     string `json:"error"`
				Code      string `json:"code"`
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if response.Error != "not_found" || response.Code != "SESSION_NOT_FOUND" || response.RequestID == "" {
				t.Fatalf("error response = %+v", response)
			}
		})
	}
}
