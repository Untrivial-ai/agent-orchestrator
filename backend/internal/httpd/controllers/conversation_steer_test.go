package controllers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// The steer route's wire contract, asserted through the real router.
//
// Every refusal here is a code a client renders differently, so the codes are the
// test: a steer that arrives a moment too late has to be distinguishable from an
// agent that cannot steer at all, or the UI has nothing to say.

// Steer satisfies the controller's contract for the doubles the sibling tests in
// this package construct directly, so adding the route did not have to reach into
// their files. Both default to a plain acceptance; the scenarios below state their
// own outcome.
func (f *fakeConversationService) Steer(
	context.Context,
	domain.SessionID,
	ports.ChatUserMessage,
) (chatsvc.SteerResult, error) {
	return chatsvc.SteerResult{}, nil
}

func (f *fakeChatService) Steer(
	context.Context,
	domain.SessionID,
	ports.ChatUserMessage,
) (chatsvc.SteerResult, error) {
	return chatsvc.SteerResult{}, nil
}

func (f *fakeConversationService) PromoteQueuedTurn(
	context.Context,
	domain.SessionID,
	string,
) (chatsvc.PromoteQueuedTurnResult, error) {
	return chatsvc.PromoteQueuedTurnResult{}, nil
}

func (f *fakeChatService) PromoteQueuedTurn(
	context.Context,
	domain.SessionID,
	string,
) (chatsvc.PromoteQueuedTurnResult, error) {
	return chatsvc.PromoteQueuedTurnResult{}, nil
}

type promoteQueuedStub struct {
	*fakeConversationService
	result  chatsvc.PromoteQueuedTurnResult
	err     error
	session domain.SessionID
	turnID  string
}

func (s *promoteQueuedStub) PromoteQueuedTurn(
	_ context.Context,
	session domain.SessionID,
	turnID string,
) (chatsvc.PromoteQueuedTurnResult, error) {
	s.session, s.turnID = session, turnID
	return s.result, s.err
}

// steerStub overrides only the steer answer, so a scenario states the outcome it is
// about and inherits everything else.
type steerStub struct {
	*fakeConversationService

	result chatsvc.SteerResult
	err    error
	seen   []ports.ChatUserMessage
}

func (s *steerStub) Steer(
	_ context.Context,
	_ domain.SessionID,
	msg ports.ChatUserMessage,
) (chatsvc.SteerResult, error) {
	s.seen = append(s.seen, msg)
	return s.result, s.err
}

func steerRouter(t *testing.T, svc *steerStub) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Sessions:      newFakeSessionService(),
		Conversations: svc,
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

// steerErrorBody is the daemon's locked error envelope: `error` is the kind, with
// the stable code alongside it.
type steerErrorBody struct {
	Kind    string `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// postSteer calls the route the way a client does and returns the status with both
// possible bodies decoded.
func postSteer(t *testing.T, svc *steerStub, body any) (int, map[string]any, steerErrorBody) {
	t.Helper()
	srv := steerRouter(t, svc)

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	resp, err := http.Post(srv.URL+"/api/v1/sessions/p1-1/conversation/steer",
		"application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("POST steer: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var ok map[string]any
	var failure steerErrorBody
	_ = json.Unmarshal(raw, &ok)
	_ = json.Unmarshal(raw, &failure)
	return resp.StatusCode, ok, failure
}

// 202, not 200: the provider takes the guidance and acts on it at its next model
// boundary. The body names the turn it joined so a client can attach it.
func TestSteerRouteAcceptsGuidanceAndNamesTheTurn(t *testing.T) {
	svc := &steerStub{
		fakeConversationService: &fakeConversationService{},
		result: chatsvc.SteerResult{
			ProviderTurnID: "provider-turn-1",
			ActivityID:     "act-9",
		},
	}
	status, body, _ := postSteer(t, svc, map[string]any{
		"text": "actually, just summarize", "clientMessageId": "steer-1",
	})

	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %#v", status, body)
	}
	if body["providerTurnId"] != "provider-turn-1" {
		t.Errorf("providerTurnId = %#v", body["providerTurnId"])
	}
	if body["activityId"] != "act-9" {
		t.Errorf("activityId = %#v", body["activityId"])
	}

	if len(svc.seen) != 1 {
		t.Fatalf("service saw %d steers, want 1", len(svc.seen))
	}
	if svc.seen[0].Text != "actually, just summarize" {
		t.Errorf("text = %q", svc.seen[0].Text)
	}
	if svc.seen[0].ClientMessageID != "steer-1" {
		t.Errorf("clientMessageId = %q", svc.seen[0].ClientMessageID)
	}
	// A steer typed by a person is the person's, and the timeline attributes it that
	// way rather than as something the daemon said.
	if svc.seen[0].Origin != domain.MessageOriginHuman {
		t.Errorf("origin = %q, want human", svc.seen[0].Origin)
	}
}

// Every refusal, with the code a client keys its message off. Table-driven because
// the mapping IS the contract: a wrong status turns an ordinary outcome into
// something that looks like a server fault.
func TestSteerRouteRefusalsAreTypedAndCoded(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		// wantInMessage keeps a refusal's advice honest where two routes share a
		// sentinel and must not share its wording.
		wantInMessage string
	}{
		{
			name: "no turn in flight", err: chatsvc.ErrNoActiveTurn,
			wantStatus: http.StatusConflict, wantCode: "CHAT_NO_ACTIVE_TURN",
			wantInMessage: "steer",
		},
		{
			name:       "driver cannot steer",
			err:        chatsvc.ErrSteerUnsupported,
			wantStatus: http.StatusConflict, wantCode: "CHAT_STEER_UNSUPPORTED",
		},
		{
			name:       "turn kind cannot take guidance",
			err:        fmt.Errorf("%w: the provider is running a compact turn", chatsvc.ErrTurnNotSteerable),
			wantStatus: http.StatusConflict, wantCode: "CHAT_TURN_NOT_STEERABLE",
			// The provider's own explanation names the turn kind, which is the part a
			// user can act on.
			wantInMessage: "compact",
		},
		{
			name:       "empty guidance",
			err:        chatsvc.ErrSteerTextRequired,
			wantStatus: http.StatusBadRequest, wantCode: "CHAT_STEER_TEXT_REQUIRED",
		},
		{
			name:       "delivery outcome uncertain",
			err:        chatsvc.ErrSteerDeliveryUncertain,
			wantStatus: http.StatusConflict, wantCode: "CHAT_STEER_UNCERTAIN",
		},
		{
			name:       "client handle reused for different guidance",
			err:        chatsvc.ErrSteerIdempotencyConflict,
			wantStatus: http.StatusConflict, wantCode: "CHAT_STEER_IDEMPOTENCY_CONFLICT",
		},
		{
			name:       "interface transition",
			err:        chatsvc.ErrControllerHandoff,
			wantStatus: http.StatusConflict, wantCode: "CHAT_INTERFACE_TRANSITION",
		},
		{
			name:       "terminal-mode session",
			err:        chatsvc.ErrNotChatMode,
			wantStatus: http.StatusConflict, wantCode: "SESSION_MODE_MISMATCH",
		},
		{
			name:       "controller not running",
			err:        chatsvc.ErrNoController,
			wantStatus: http.StatusConflict, wantCode: "CHAT_CONTROLLER_NOT_READY",
		},
		{
			name:       "unknown session",
			err:        ports.ErrSessionNotFound,
			wantStatus: http.StatusNotFound, wantCode: "SESSION_NOT_FOUND",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &steerStub{fakeConversationService: &fakeConversationService{}, err: tc.err}
			status, _, failure := postSteer(t, svc, map[string]any{"text": "guidance"})

			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			if failure.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", failure.Code, tc.wantCode)
			}
			if tc.wantInMessage != "" &&
				!strings.Contains(strings.ToLower(failure.Message), tc.wantInMessage) {
				t.Errorf("message %q does not mention %q", failure.Message, tc.wantInMessage)
			}
		})
	}
}

// A refusal must never arrive as a 500: the conversation is healthy, and a server
// fault would send a client into retry-and-report instead of telling the user what
// to do next.
func TestSteerRouteReportsAnUnknownFailureAsAServerError(t *testing.T) {
	svc := &steerStub{
		fakeConversationService: &fakeConversationService{},
		err:                     errors.New("the pipe broke"),
	}
	status, _, _ := postSteer(t, svc, map[string]any{"text": "guidance"})
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
}

func TestSteerRouteRejectsUnparseableBody(t *testing.T) {
	srv := steerRouter(t, &steerStub{fakeConversationService: &fakeConversationService{}})
	resp, err := http.Post(srv.URL+"/api/v1/sessions/p1-1/conversation/steer",
		"application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatalf("POST steer: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// With no Chat service wired the route answers the locked 501 envelope rather than
// panicking on a nil service.
func TestSteerRouteIsNotImplementedWithoutAChatService(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Sessions: newFakeSessionService(),
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/v1/sessions/p1-1/conversation/steer",
		"application/json", strings.NewReader(`{"text":"guidance"}`))
	if err != nil {
		t.Fatalf("POST steer: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

// The turn-scoped route identifies durable content; it carries no request body
// that could replace what AO already queued.
func TestQueuedTurnSteerRoutePromotesTheSelectedTurn(t *testing.T) {
	svc := &promoteQueuedStub{
		fakeConversationService: &fakeConversationService{},
		result: chatsvc.PromoteQueuedTurnResult{
			SourceTurnID: "queued-2", ProviderTurnID: "provider-running", ActivityID: "activity-steer",
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Sessions: newFakeSessionService(), Conversations: svc,
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(
		srv.URL+"/api/v1/sessions/p1-1/conversation/turns/queued-2/steer",
		"application/json", nil)
	if err != nil {
		t.Fatalf("POST queued steer: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 202: %s", resp.StatusCode, body)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if svc.session != "p1-1" || svc.turnID != "queued-2" {
		t.Fatalf("service target = (%s, %s)", svc.session, svc.turnID)
	}
	if body["sourceTurnId"] != "queued-2" || body["providerTurnId"] != "provider-running" || body["activityId"] != "activity-steer" {
		t.Fatalf("response = %#v", body)
	}
}
