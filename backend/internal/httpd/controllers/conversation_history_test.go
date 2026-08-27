package controllers_test

import (
	"context"
	"fmt"
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

// The wire contract for the history operations.
//
// The point of every case below is that an ordinary refusal is a typed conflict a
// client can act on, never a 500: "this agent cannot undo" and "stop the agent first"
// are different instructions, and a generic internal error is neither.

type fakeChatService struct {
	discarded   int
	title       string
	rollback    error
	setTitle    error
	edit        chatsvc.EditMessageResult
	editErr     error
	activate    string
	activateErr error
	retryTurn   domain.ConversationTurn
	retryErr    error

	gotTurnID      string
	gotTitle       string
	gotEditTurnID  string
	gotEditMessage ports.ChatUserMessage
	gotBranchID    string
}

func (f *fakeChatService) EditMessage(_ context.Context, _ domain.SessionID, turnID string, message ports.ChatUserMessage) (chatsvc.EditMessageResult, error) {
	f.gotEditTurnID = turnID
	f.gotEditMessage = message
	return f.edit, f.editErr
}

func (f *fakeChatService) ActivateBranch(_ context.Context, _ domain.SessionID, branchID string) (string, error) {
	f.gotBranchID = branchID
	return f.activate, f.activateErr
}

func (f *fakeChatService) Snapshot(context.Context, domain.SessionID) (chatsvc.Snapshot, error) {
	return chatsvc.Snapshot{}, nil
}

func (f *fakeChatService) Send(context.Context, domain.SessionID, ports.ChatUserMessage) (domain.ConversationTurn, error) {
	return domain.ConversationTurn{}, nil
}

func (f *fakeChatService) Resolve(context.Context, domain.SessionID, string, ports.ChatDecision) error {
	return nil
}

func (f *fakeChatService) ResolveInput(context.Context, domain.SessionID, string, ports.ChatInputResponse) error {
	return nil
}

func (f *fakeChatService) Interrupt(context.Context, domain.SessionID) error { return nil }

func (f *fakeChatService) Models(context.Context, domain.SessionID) ([]ports.ChatModel, domain.ConversationSettings, error) {
	return nil, domain.ConversationSettings{}, nil
}

func (f *fakeChatService) ConfigOptions(context.Context, domain.SessionID) ([]ports.ChatConfigOption, error) {
	return nil, nil
}

func (f *fakeChatService) SetConfigOption(context.Context, domain.SessionID, string, ports.ChatConfigOptionValue) ([]ports.ChatConfigOption, error) {
	return nil, nil
}

func (f *fakeChatService) SetTurnSettings(context.Context, domain.SessionID, domain.ConversationSettings) (domain.ConversationSettings, error) {
	return domain.ConversationSettings{}, nil
}

func (f *fakeChatService) Rollback(_ context.Context, _ domain.SessionID, turnID string) (int, error) {
	f.gotTurnID = turnID
	if f.rollback != nil {
		return 0, f.rollback
	}
	return f.discarded, nil
}

func (f *fakeChatService) RetryTurn(_ context.Context, _ domain.SessionID, turnID string) (domain.ConversationTurn, error) {
	f.gotTurnID = turnID
	if f.retryErr != nil {
		return domain.ConversationTurn{}, f.retryErr
	}
	return f.retryTurn, nil
}

// Compaction belongs to a sibling slice; this fake only has to satisfy the
// controller's interface so the history routes can be exercised.
func (f *fakeChatService) Compact(context.Context, domain.SessionID) (ports.ChatCompactionResult, error) {
	return ports.ChatCompactionResult{}, nil
}

func (f *fakeChatService) ReloadMCPServers(
	context.Context,
	domain.SessionID,
) ([]domain.ConversationMCPServer, error) {
	return nil, nil
}

func (f *fakeChatService) SetTitle(_ context.Context, _ domain.SessionID, title string) (string, error) {
	f.gotTitle = title
	if f.setTitle != nil {
		return "", f.setTitle
	}
	return f.title, nil
}

// Skills is not what these tests exercise; present so the fake satisfies the
// controller's interface.
func (f *fakeChatService) Skills(context.Context, domain.SessionID) ([]ports.ChatSkill, error) {
	return nil, nil
}

func newChatTestServer(t *testing.T, svc *fakeChatService) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{}, log, nil, httpd.APIDeps{Conversations: svc}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRollbackRouteReportsWhatWasDiscarded(t *testing.T) {
	svc := &fakeChatService{discarded: 3}
	srv := newChatTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "POST",
		"/api/v1/sessions/ao-1/conversation/turns/turn-7/rollback", "")
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.gotTurnID != "turn-7" {
		t.Errorf("turn id = %q, want turn-7", svc.gotTurnID)
	}
	var got struct {
		TurnsDiscarded int `json:"turnsDiscarded"`
	}
	mustJSON(t, body, &got)
	if got.TurnsDiscarded != 3 {
		t.Errorf("turnsDiscarded = %d, want 3", got.TurnsDiscarded)
	}
}

func TestRetryTurnRouteDispatchesANewTurn(t *testing.T) {
	svc := &fakeChatService{retryTurn: domain.ConversationTurn{
		ID: "turn-retry", ProviderTurnID: "provider-retry", State: domain.TurnStateRunning,
	}}
	srv := newChatTestServer(t, svc)

	body, status, headers := doRequest(t, srv, http.MethodPost,
		"/api/v1/sessions/ao-1/conversation/turns/turn-failed/retry", "")
	assertJSON(t, headers)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", status, body)
	}
	if svc.gotTurnID != "turn-failed" {
		t.Fatalf("turn id = %q, want turn-failed", svc.gotTurnID)
	}
	var got struct {
		TurnID         string           `json:"turnId"`
		ProviderTurnID string           `json:"providerTurnId"`
		State          domain.TurnState `json:"state"`
	}
	mustJSON(t, body, &got)
	if got.TurnID != "turn-retry" || got.ProviderTurnID != "provider-retry" || got.State != domain.TurnStateRunning {
		t.Fatalf("response = %+v", got)
	}
}

func TestRetryTurnRouteReturnsAnExistingRecoveredAttempt(t *testing.T) {
	svc := &fakeChatService{retryTurn: domain.ConversationTurn{
		ID: "turn-retry", ProviderTurnID: "provider-retry", State: domain.TurnStateRecovered,
	}}
	srv := newChatTestServer(t, svc)

	body, status, headers := doRequest(t, srv, http.MethodPost,
		"/api/v1/sessions/ao-1/conversation/turns/turn-failed/retry", "")
	assertJSON(t, headers)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", status, body)
	}
	var got struct {
		TurnID string           `json:"turnId"`
		State  domain.TurnState `json:"state"`
	}
	mustJSON(t, body, &got)
	if got.TurnID != "turn-retry" || got.State != domain.TurnStateRecovered {
		t.Fatalf("response = %+v, want recovered replay", got)
	}
}

func TestRetryTurnRouteRefusalsAreTyped(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not retryable", chatsvc.ErrTurnNotRetryable, http.StatusConflict, "CHAT_RETRY_NOT_RETRYABLE"},
		{"delivery uncertain", chatsvc.ErrRetryDeliveryUncertain, http.StatusConflict, "CHAT_RETRY_DELIVERY_UNCERTAIN"},
		{"stale branch", chatsvc.ErrRetryStaleBranch, http.StatusConflict, "CHAT_RETRY_STALE_BRANCH"},
		{"invalid content", chatsvc.ErrRetryContentInvalid, http.StatusBadRequest, "CHAT_RETRY_CONTENT_INVALID"},
		{"unsupported content", chatsvc.ErrRetryUnsupported, http.StatusConflict, "CHAT_RETRY_UNSUPPORTED"},
		{"busy", chatsvc.ErrTurnRunning, http.StatusConflict, "CHAT_RETRY_BUSY"},
		{"missing turn", domain.ErrNoConversationTurn, http.StatusNotFound, "CHAT_RETRY_TURN_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newChatTestServer(t, &fakeChatService{retryErr: tc.err})
			body, status, headers := doRequest(t, srv, http.MethodPost,
				"/api/v1/sessions/ao-1/conversation/turns/turn-failed/retry", "")
			assertJSON(t, headers)
			assertErrorCode(t, body, status, tc.wantStatus, tc.wantCode)
		})
	}
}

func TestEditConversationRouteAcceptsReplacement(t *testing.T) {
	svc := &fakeChatService{edit: chatsvc.EditMessageResult{
		SourceBranchID: "branch-root", ActiveBranchID: "branch-child",
		Turn: domain.ConversationTurn{ID: "turn-edited", ProviderTurnID: "provider-edited", State: domain.TurnStateRunning},
	}}
	srv := newChatTestServer(t, svc)
	body, status, headers := doRequest(t, srv, http.MethodPost,
		"/api/v1/sessions/ao-1/conversation/turns/turn-2/edit",
		`{"text":"edited prompt","clientMessageId":"edit-1"}`)
	assertJSON(t, headers)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if svc.gotEditTurnID != "turn-2" || svc.gotEditMessage.Text != "edited prompt" || svc.gotEditMessage.ClientMessageID != "edit-1" {
		t.Fatalf("edit call = turn %q message %#v", svc.gotEditTurnID, svc.gotEditMessage)
	}
	var got struct {
		SourceBranchID string `json:"sourceBranchId"`
		ActiveBranchID string `json:"activeBranchId"`
		TurnID         string `json:"turnId"`
	}
	mustJSON(t, body, &got)
	if got.SourceBranchID != "branch-root" || got.ActiveBranchID != "branch-child" || got.TurnID != "turn-edited" {
		t.Fatalf("response = %+v", got)
	}
}

func TestEditConversationRouteRefusalsUseEditCodes(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"blank", nil, http.StatusBadRequest, "CHAT_EDIT_TURN_INVALID"},
		{"unsupported", chatsvc.ErrForkUnsupported, http.StatusConflict, "CHAT_EDIT_UNSUPPORTED"},
		{"busy", chatsvc.ErrTurnRunning, http.StatusConflict, "CHAT_EDIT_BUSY"},
		{"missing turn", fmt.Errorf("%w: %w", chatsvc.ErrEditTurnInvalid, domain.ErrNoConversationTurn), http.StatusNotFound, "CHAT_EDIT_TURN_INVALID"},
		{"invalid stored content", chatsvc.ErrEditTurnInvalid, http.StatusBadRequest, "CHAT_EDIT_TURN_INVALID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeChatService{editErr: tc.err}
			srv := newChatTestServer(t, svc)
			request := `{"text":"edited"}`
			if tc.name == "blank" {
				request = `{"text":"   "}`
			}
			body, status, _ := doRequest(t, srv, http.MethodPost,
				"/api/v1/sessions/ao-1/conversation/turns/turn-2/edit", request)
			assertErrorCode(t, body, status, tc.wantStatus, tc.wantCode)
		})
	}
}

func TestActivateConversationBranchRouteResumesWithoutBody(t *testing.T) {
	svc := &fakeChatService{activate: "branch-root"}
	srv := newChatTestServer(t, svc)
	body, status, _ := doRequest(t, srv, http.MethodPost,
		"/api/v1/sessions/ao-1/conversation/branches/branch-root/activate", "")
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if svc.gotBranchID != "branch-root" {
		t.Fatalf("branch id = %q", svc.gotBranchID)
	}
}

func TestActivateConversationBranchRouteDecodesEscapedBranchID(t *testing.T) {
	svc := &fakeChatService{activate: "conversation-1:root"}
	srv := newChatTestServer(t, svc)
	body, status, _ := doRequest(t, srv, http.MethodPost,
		"/api/v1/sessions/ao-1/conversation/branches/conversation-1%3Aroot/activate", "")
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if svc.gotBranchID != "conversation-1:root" {
		t.Fatalf("branch id = %q, want decoded root id", svc.gotBranchID)
	}
}

func TestActivateConversationBranchRouteReportsMissingBranch(t *testing.T) {
	svc := &fakeChatService{activateErr: domain.ErrNoConversationBranch}
	srv := newChatTestServer(t, svc)
	body, status, _ := doRequest(t, srv, http.MethodPost,
		"/api/v1/sessions/ao-1/conversation/branches/missing/activate", "")
	assertErrorCode(t, body, status, http.StatusNotFound, "CHAT_BRANCH_NOT_FOUND")
}

func TestActivateConversationBranchRouteRejectsPreviousAgentProvider(t *testing.T) {
	svc := &fakeChatService{activateErr: chatsvc.ErrBranchProviderMismatch}
	srv := newChatTestServer(t, svc)
	body, status, _ := doRequest(t, srv, http.MethodPost,
		"/api/v1/sessions/ao-1/conversation/branches/source-agent-branch/activate", "")
	assertErrorCode(t, body, status, http.StatusConflict, "CHAT_BRANCH_PROVIDER_MISMATCH")
}

// Every refusal maps to a stable code and a status the client can branch on. A 500
// anywhere in this table would mean AO reported its own bug for someone else's
// ordinary situation.
func TestConversationHistoryRefusalsAreTypedNeverInternalErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"turn running", chatsvc.ErrTurnRunning, http.StatusConflict, "CHAT_TURN_RUNNING"},
		{"unknown turn", fmt.Errorf("look up: %w", domain.ErrNoConversationTurn), http.StatusNotFound, "CHAT_TURN_NOT_FOUND"},
		{"never dispatched", chatsvc.ErrTurnNotRollbackable, http.StatusConflict, "CHAT_TURN_NOT_ROLLBACKABLE"},
		{"previous agent provider", chatsvc.ErrTurnProviderMismatch, http.StatusConflict, "CHAT_TURN_PROVIDER_MISMATCH"},
		{"unsupported", chatsvc.ErrRollbackUnsupported, http.StatusConflict, "CHAT_ROLLBACK_UNSUPPORTED"},
		{"provider refused", fmt.Errorf("%w: no", chatsvc.ErrProviderRefused), http.StatusConflict, "CHAT_PROVIDER_REFUSED"},
		{"no controller", chatsvc.ErrNoController, http.StatusConflict, "CHAT_CONTROLLER_NOT_READY"},
		{"tui session", chatsvc.ErrNotChatMode, http.StatusConflict, "SESSION_MODE_MISMATCH"},
		{"missing session", ports.ErrSessionNotFound, http.StatusNotFound, "SESSION_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newChatTestServer(t, &fakeChatService{rollback: tc.err})
			body, status, headers := doRequest(t, srv, "POST",
				"/api/v1/sessions/ao-1/conversation/turns/turn-1/rollback", "")
			assertJSON(t, headers)
			assertErrorCode(t, body, status, tc.wantStatus, tc.wantCode)
		})
	}
}

// 202, not 200: the provider confirms the name and then reports it back, and that
// report is what moves the session label. Claiming 200 would promise a change that
// has not happened.
func TestSetTitleRouteAcceptsAndEchoesTheNormalizedTitle(t *testing.T) {
	svc := &fakeChatService{title: "Fix OAuth Return URL Loss"}
	srv := newChatTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "PUT",
		"/api/v1/sessions/ao-1/conversation/title",
		`{"title":"## Fix OAuth Return URL Loss."}`)
	assertJSON(t, headers)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", status, body)
	}
	if svc.gotTitle != "## Fix OAuth Return URL Loss." {
		t.Errorf("service received %q, want the raw title (the service normalizes)", svc.gotTitle)
	}
	var got struct {
		Title string `json:"title"`
	}
	mustJSON(t, body, &got)
	if got.Title != "Fix OAuth Return URL Loss" {
		t.Errorf("title = %q, want the normalized form", got.Title)
	}
}

func TestSetTitleRouteRefusesABlankTitle(t *testing.T) {
	srv := newChatTestServer(t, &fakeChatService{setTitle: chatsvc.ErrTitleRequired})
	body, status, headers := doRequest(t, srv, "PUT",
		"/api/v1/sessions/ao-1/conversation/title", `{"title":"   "}`)
	assertJSON(t, headers)
	assertErrorCode(t, body, status, http.StatusBadRequest, "CHAT_TITLE_REQUIRED")
}

func TestSetTitleRouteReportsAnUnsupportedProvider(t *testing.T) {
	srv := newChatTestServer(t, &fakeChatService{setTitle: chatsvc.ErrRenameUnsupported})
	body, status, headers := doRequest(t, srv, "PUT",
		"/api/v1/sessions/ao-1/conversation/title", `{"title":"A Name"}`)
	assertJSON(t, headers)
	assertErrorCode(t, body, status, http.StatusConflict, "CHAT_RENAME_UNSUPPORTED")
}

// Without a chat driver wired the routes must say "not implemented" rather than
// panicking on a nil service.
func TestConversationHistoryRoutesStubWithoutAService(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{}, log, nil, httpd.APIDeps{}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/sessions/ao-1/conversation/turns/turn-1/rollback", ""},
		{"POST", "/api/v1/sessions/ao-1/conversation/turns/turn-1/retry", ""},
		{"PUT", "/api/v1/sessions/ao-1/conversation/title", `{"title":"A Name"}`},
	} {
		body, status, headers := doRequest(t, srv, tc.method, tc.path, tc.body)
		assertJSON(t, headers)
		assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
	}
}
