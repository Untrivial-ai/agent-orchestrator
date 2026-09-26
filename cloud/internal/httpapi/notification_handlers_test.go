package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

func TestWorkerNotificationEventRequiresScopeAndValidatesEnvelope(t *testing.T) {
	t.Parallel()
	valid := `{"eventId":"evt-1","type":"needs_input","occurredAt":"2026-09-21T10:00:00Z","payload":{"activityId":"tool-1"}}`
	tests := []struct {
		name, body string
		scopes     []string
		want       int
	}{
		{name: "missing scope", body: valid, scopes: []string{"worker:event"}, want: http.StatusForbidden},
		{name: "unknown field", body: strings.TrimSuffix(valid, "}") + `,"orgId":"forged"}`, scopes: []string{"worker:notification"}, want: http.StatusBadRequest},
		{name: "unsupported type", body: strings.Replace(valid, "needs_input", "pull_request_created", 1), scopes: []string{"worker:notification"}, want: http.StatusUnprocessableEntity},
		{name: "empty event id", body: strings.Replace(valid, "evt-1", "", 1), scopes: []string{"worker:notification"}, want: http.StatusUnprocessableEntity},
		{name: "non object payload", body: strings.Replace(valid, `{"activityId":"tool-1"}`, `[]`, 1), scopes: []string{"worker:notification"}, want: http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &notificationHandlerStore{}
			srv := New(Options{Store: store})
			req := notificationWorkerRequest(tt.body, tt.scopes...)
			recorder := httptest.NewRecorder()
			srv.workerNotificationEvent(recorder, req)
			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.want, recorder.Body.String())
			}
			if store.accepted != 0 {
				t.Fatalf("store accepted %d invalid events", store.accepted)
			}
		})
	}
}

func TestWorkerNotificationEventReturnsDurableAcceptance(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		acceptance domain.NotificationAcceptance
		storeErr   error
		want       int
	}{
		{name: "new", acceptance: domain.NotificationAcceptance{IngressID: "ingress", EventID: "evt-1"}, want: http.StatusAccepted},
		{name: "duplicate", acceptance: domain.NotificationAcceptance{IngressID: "ingress", EventID: "evt-1", Duplicate: true}, want: http.StatusOK},
		{name: "stale", storeErr: postgres.ErrStaleWorker, want: http.StatusUnauthorized},
		{name: "mismatch", storeErr: postgres.ErrIdempotencyMismatch, want: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			wakes := 0
			store := &notificationHandlerStore{acceptance: test.acceptance, acceptErr: test.storeErr}
			srv := New(Options{Store: store, NotificationWake: func() { wakes++ }})
			recorder := httptest.NewRecorder()
			srv.workerNotificationEvent(recorder, notificationWorkerRequest(
				`{"eventId":"evt-1","type":"needs_input","occurredAt":"2026-09-21T10:00:00Z","payload":{"activityId":"tool-1"}}`,
				"worker:notification",
			))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.want, recorder.Body.String())
			}
			if test.storeErr == nil {
				var response struct {
					Accepted, Duplicate bool
					EventID             string `json:"eventId"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if !response.Accepted || response.EventID != "evt-1" || response.Duplicate != test.acceptance.Duplicate || wakes != 1 {
					t.Fatalf("response=%+v wakes=%d", response, wakes)
				}
			} else if wakes != 0 {
				t.Fatalf("failed acceptance woke processor %d times", wakes)
			}
		})
	}
}

func TestListNotificationsPassesRecipientAndFilterAndReturnsCloudPage(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	store := &notificationHandlerStore{page: domain.NotificationPage{
		Items:   []domain.Notification{{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Source: "cloud", Status: "unread", CreatedAt: createdAt}},
		HasMore: true, UnreadCount: 3, LatestSequence: 9,
		NextCursor: &domain.NotificationCursor{CreatedAt: createdAt, ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	}}
	srv := New(Options{Store: store})
	req := notificationUserRequest(http.MethodGet, "/notifications?status=unread&limit=1", "org-1", "user-1", nil)
	recorder := httptest.NewRecorder()
	srv.listNotifications(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if store.listPrincipal.UserID != "user-1" || store.listOrg != "org-1" || store.listFilter.Status != domain.NotificationStatusUnread || store.listFilter.Limit != 1 {
		t.Fatalf("list args principal=%+v org=%q filter=%+v", store.listPrincipal, store.listOrg, store.listFilter)
	}
	var response struct {
		Items []domain.Notification `json:"items"`
		Page  struct {
			HasMore    bool   `json:"hasMore"`
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
		UnreadCount    int   `json:"unreadCount"`
		LatestSequence int64 `json:"latestSequence"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].Source != "cloud" || !response.Page.HasMore || response.Page.NextCursor == "" || response.UnreadCount != 3 || response.LatestSequence != 9 {
		t.Fatalf("response=%+v", response)
	}
}

func TestNotificationReadHandlersKeepUserAndOrgScope(t *testing.T) {
	t.Parallel()
	store := &notificationHandlerStore{}
	srv := New(Options{Store: store})

	recorder := httptest.NewRecorder()
	const notificationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	req := notificationUserRequest(http.MethodPatch, "/notifications/"+notificationID, "org-1", "user-1", bytes.NewBufferString(`{"status":"read"}`))
	req = withNotificationParam(req, "notificationId", notificationID)
	srv.markNotificationRead(recorder, req)
	if recorder.Code != http.StatusOK || len(store.readIDs) != 1 || store.readIDs[0] != notificationID {
		t.Fatalf("single read status=%d ids=%v body=%s", recorder.Code, store.readIDs, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	req = notificationUserRequest(http.MethodPost, "/notifications/read-all", "org-1", "user-1", nil)
	srv.markAllNotificationsRead(recorder, req)
	if recorder.Code != http.StatusOK || store.readIDs != nil {
		t.Fatalf("read all status=%d ids=%v body=%s", recorder.Code, store.readIDs, recorder.Body.String())
	}
}

func notificationWorkerRequest(body string, scopes ...string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/worker/notification-events", strings.NewReader(body))
	claims := worker.Claims{OrgID: "org-1", SessionID: "session-1", WorkerID: "worker-1", Epoch: 4, Scopes: scopes}
	return req.WithContext(context.WithValue(req.Context(), workerContextKey{}, claims))
}

func notificationUserRequest(method, target, orgID, userID string, body *bytes.Buffer) *http.Request {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body.Bytes())
	}
	req := httptest.NewRequest(method, target, reader)
	req = req.WithContext(context.WithValue(req.Context(), principalKey, domain.Principal{UserID: userID, Provider: "local"}))
	return withNotificationParam(req, "orgId", orgID)
}

func withNotificationParam(req *http.Request, key, value string) *http.Request {
	ctx := chi.NewRouteContext()
	if existing := chi.RouteContext(req.Context()); existing != nil {
		ctx.URLParams = existing.URLParams
	}
	ctx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
}

type notificationHandlerStore struct {
	Store
	acceptance    domain.NotificationAcceptance
	acceptErr     error
	accepted      int
	page          domain.NotificationPage
	listPrincipal domain.Principal
	listOrg       string
	listFilter    domain.NotificationFilter
	readIDs       []string
	readErr       error
}

func (s *notificationHandlerStore) AcceptNotificationEvent(_ context.Context, _, _, _ string, _ int64, _ domain.AgentNotificationEvent) (domain.NotificationAcceptance, error) {
	s.accepted++
	return s.acceptance, s.acceptErr
}

func (s *notificationHandlerStore) ListNotifications(_ context.Context, principal domain.Principal, orgID string, filter domain.NotificationFilter) (domain.NotificationPage, error) {
	s.listPrincipal, s.listOrg, s.listFilter = principal, orgID, filter
	return s.page, nil
}

func (s *notificationHandlerStore) ListNotificationEvents(context.Context, domain.Principal, string, int64, int) ([]domain.NotificationEvent, bool, error) {
	return nil, false, nil
}

func (s *notificationHandlerStore) MarkNotificationsRead(_ context.Context, _ domain.Principal, _ string, ids []string) (int64, error) {
	s.readIDs = ids
	if s.readErr != nil {
		return 0, s.readErr
	}
	return int64(len(ids)), nil
}
