package controllers_test

import (
	"bytes"
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
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type editQueuedStub struct {
	*fakeConversationService
	session domain.SessionID
	turnID  string
	text    string
	edit    chatsvc.QueuedMessageEdit
	err     error
}

func (s *editQueuedStub) EditQueuedTurn(
	_ context.Context,
	session domain.SessionID,
	turnID string,
	edit chatsvc.QueuedMessageEdit,
) error {
	s.session, s.turnID, s.text = session, turnID, edit.Text
	s.edit = edit
	return s.err
}

func postEditQueuedTurn(t *testing.T, svc *editQueuedStub, turnID string, request map[string]any) int {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Sessions:      newFakeSessionService(),
		Conversations: svc,
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	resp, err := http.Post(
		srv.URL+"/api/v1/sessions/p1-1/conversation/turns/"+turnID+"/queue/edit",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST queue/edit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestEditQueuedTurnRoute(t *testing.T) {
	svc := &editQueuedStub{fakeConversationService: &fakeConversationService{}}
	if status := postEditQueuedTurn(t, svc, "turn-queued", map[string]any{"text": "updated text"}); status != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", status, http.StatusNoContent)
	}
	if svc.session != "p1-1" || svc.turnID != "turn-queued" || svc.text != "updated text" {
		t.Fatalf("svc saw session=%q turn=%q text=%q", svc.session, svc.turnID, svc.text)
	}
}

func TestEditQueuedTurnRouteForwardsImageChanges(t *testing.T) {
	svc := &editQueuedStub{fakeConversationService: &fakeConversationService{}}
	status := postEditQueuedTurn(t, svc, "turn-queued", map[string]any{
		"text": "", "retainedContent": []int{}, "expectedRevision": 3,
		"attachments": []map[string]string{{"mimeType": "image/png", "data": "aGVsbG8="}},
	})
	if status != http.StatusNoContent {
		t.Fatalf("status = %d", status)
	}
	if svc.edit.RetainedContent == nil || len(*svc.edit.RetainedContent) != 0 ||
		svc.edit.ExpectedRevision == nil || *svc.edit.ExpectedRevision != 3 ||
		len(svc.edit.Content) != 1 || svc.edit.Content[0].Data != "aGVsbG8=" {
		t.Fatalf("edit = %+v", svc.edit)
	}
}

func TestEditQueuedTurnRouteRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"empty", chatsvc.ErrQueuedTurnTextRequired, http.StatusBadRequest},
		{"invalid content", chatsvc.ErrQueuedContentInvalid, http.StatusBadRequest},
		{"stale revision", chatsvc.ErrQueuedEditConflict, http.StatusConflict},
		{"already dispatched", store.ErrQueuedTurnNotAvailable, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &editQueuedStub{fakeConversationService: &fakeConversationService{}, err: tc.err}
			if status := postEditQueuedTurn(t, svc, "turn-queued", map[string]any{"text": "update"}); status != tc.status {
				t.Fatalf("status = %d, want %d", status, tc.status)
			}
		})
	}
}

func TestEditQueuedTurnRouteRejectsMalformedImageBeforeService(t *testing.T) {
	svc := &editQueuedStub{fakeConversationService: &fakeConversationService{}}
	status := postEditQueuedTurn(t, svc, "turn-queued", map[string]any{
		"text": "update", "attachments": []map[string]string{{"mimeType": "image/png", "data": "not base64"}},
	})
	if status != http.StatusBadRequest || svc.turnID != "" {
		t.Fatalf("status=%d, called turn=%q", status, svc.turnID)
	}
}

type cancelQueuedStub struct {
	*fakeConversationService
	session domain.SessionID
	turnID  string
	err     error
}

func (s *cancelQueuedStub) CancelQueuedTurn(
	_ context.Context,
	session domain.SessionID,
	turnID string,
) error {
	s.session, s.turnID = session, turnID
	return s.err
}

func postCancelQueuedTurn(t *testing.T, svc *cancelQueuedStub, turnID string) int {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Sessions:      newFakeSessionService(),
		Conversations: svc,
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(
		srv.URL+"/api/v1/sessions/p1-1/conversation/turns/"+turnID+"/cancel",
		"application/json",
		nil,
	)
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestCancelQueuedTurnRoute(t *testing.T) {
	svc := &cancelQueuedStub{fakeConversationService: &fakeConversationService{}}
	if status := postCancelQueuedTurn(t, svc, "turn-queued"); status != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", status, http.StatusNoContent)
	}
	if svc.session != "p1-1" || svc.turnID != "turn-queued" {
		t.Fatalf("svc saw session=%q turn=%q", svc.session, svc.turnID)
	}
}

type reorderQueuedStub struct {
	*fakeConversationService
	session domain.SessionID
	turnIDs []string
	err     error
}

func (s *reorderQueuedStub) ReorderQueuedTurns(
	_ context.Context,
	session domain.SessionID,
	turnIDs []string,
) error {
	s.session, s.turnIDs = session, append([]string(nil), turnIDs...)
	return s.err
}

func postReorderQueuedTurns(t *testing.T, svc *reorderQueuedStub, turnIDs []string) int {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Sessions:      newFakeSessionService(),
		Conversations: svc,
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	var body []byte
	var err error
	if turnIDs != nil {
		body, err = json.Marshal(map[string][]string{"turnIds": turnIDs})
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}

	resp, err := http.Post(
		srv.URL+"/api/v1/sessions/p1-1/conversation/queue/reorder",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST queue/reorder: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestReorderQueuedTurnsRoute(t *testing.T) {
	svc := &reorderQueuedStub{fakeConversationService: &fakeConversationService{}}
	if status := postReorderQueuedTurns(t, svc, []string{"queued-2", "queued-1", "queued-3"}); status != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", status, http.StatusNoContent)
	}
	if svc.session != "p1-1" || len(svc.turnIDs) != 3 || svc.turnIDs[0] != "queued-2" {
		t.Fatalf("svc saw session=%q turnIDs=%v", svc.session, svc.turnIDs)
	}
}

func TestReorderQueuedTurnsRouteRejectsEmptyOrder(t *testing.T) {
	svc := &reorderQueuedStub{fakeConversationService: &fakeConversationService{}}
	if status := postReorderQueuedTurns(t, svc, []string{}); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if svc.turnIDs != nil {
		t.Fatalf("service should not be called for empty order, got turnIDs=%v", svc.turnIDs)
	}
}

func TestReorderQueuedTurnsRouteRejectsInvalidOrder(t *testing.T) {
	svc := &reorderQueuedStub{
		fakeConversationService: &fakeConversationService{},
		err:                     chatsvc.ErrInvalidQueuedTurnOrder,
	}
	if status := postReorderQueuedTurns(t, svc, []string{"queued-1", "missing"}); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestReorderQueuedTurnsRouteRejectsUnavailableTurn(t *testing.T) {
	svc := &reorderQueuedStub{
		fakeConversationService: &fakeConversationService{},
		err:                     store.ErrQueuedTurnNotAvailable,
	}
	if status := postReorderQueuedTurns(t, svc, []string{"queued-2", "queued-1"}); status != http.StatusConflict {
		t.Fatalf("status = %d, want %d", status, http.StatusConflict)
	}
}
