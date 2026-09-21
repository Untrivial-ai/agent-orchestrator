package notification

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestBuildNotificationMapsAgentEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, eventType, payload, wantTitle, wantKey string
		epoch                                        int64
	}{
		{name: "needs input", eventType: "needs_input", payload: `{"activityId":"tool-7","message":"Choose a database"}`, wantTitle: "Agent needs input", wantKey: "needs-input:session-1:tool-7", epoch: 9},
		{name: "failed", eventType: "agent_failed", payload: `{"message":"command failed"}`, wantTitle: "Agent stopped with an error", wantKey: "agent-failed:session-1:9", epoch: 9},
		{name: "completed", eventType: "agent_completed", payload: `{"activityId":"turn-3"}`, wantTitle: "Agent completed its work", wantKey: "agent-completed:session-1:9:turn-3", epoch: 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := BuildNotification(domain.NotificationIngress{
				OrgID: "org-1", ProjectID: "project-1", SessionID: "session-1",
				RecipientUserID: "user-1", WorkerEpoch: tt.epoch,
				Event: domain.AgentNotificationEvent{EventID: "evt-1", Type: domain.NotificationType(tt.eventType), Payload: json.RawMessage(tt.payload)},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != tt.wantTitle || got.DedupeKey != tt.wantKey || got.Source != "cloud" || got.Status != "unread" {
				t.Fatalf("BuildNotification() = %+v", got)
			}
		})
	}
}

func TestBuildNotificationRejectsMalformedOrIncompletePayload(t *testing.T) {
	t.Parallel()

	for _, ingress := range []domain.NotificationIngress{
		{SessionID: "session", WorkerEpoch: 1, Event: domain.AgentNotificationEvent{Type: domain.NotificationTypeNeedsInput, Payload: json.RawMessage(`{`)}},
		{SessionID: "session", WorkerEpoch: 1, Event: domain.AgentNotificationEvent{Type: domain.NotificationTypeNeedsInput, Payload: json.RawMessage(`{}`)}},
		{SessionID: "session", WorkerEpoch: 1, Event: domain.AgentNotificationEvent{Type: domain.NotificationTypeAgentCompleted, Payload: json.RawMessage(`{"activityId":""}`)}},
		{SessionID: "session", WorkerEpoch: 1, Event: domain.AgentNotificationEvent{Type: "pull_request_created", Payload: json.RawMessage(`{}`)}},
	} {
		if _, err := BuildNotification(ingress); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("BuildNotification(%q, %s) error = %v, want ErrInvalidPayload", ingress.Event.Type, ingress.Event.Payload, err)
		}
	}
}

func TestBuildNotificationUsesSemanticKeyNotTransportEventID(t *testing.T) {
	t.Parallel()

	base := domain.NotificationIngress{
		SessionID: "session-1", WorkerEpoch: 2,
		Event: domain.AgentNotificationEvent{EventID: "evt-a", Type: domain.NotificationTypeNeedsInput, Payload: json.RawMessage(`{"activityId":"tool-1"}`)},
	}
	first, err := BuildNotification(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Event.EventID = "evt-b"
	second, err := BuildNotification(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.DedupeKey != second.DedupeKey {
		t.Fatalf("semantic keys differ: %q and %q", first.DedupeKey, second.DedupeKey)
	}
}

func TestProcessOneCompletesIngressWithoutRecipient(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claim: domain.NotificationIngress{ID: "ingress-1", OrgID: "org-1", LeaseOwner: "processor"}, found: true}
	service := NewService(store, Config{Owner: "processor"})

	err := service.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if store.completed != 1 || store.created != 0 || store.retried != 0 {
		t.Fatalf("completed=%d created=%d retried=%d", store.completed, store.created, store.retried)
	}
}

func TestProcessOneRetriesTransientFailure(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		claim: validIngress(3), found: true, createErr: errors.New("database unavailable"),
	}
	service := NewService(store, Config{Owner: "processor", Now: func() time.Time { return time.Unix(100, 0) }})

	err := service.ProcessOne(context.Background())
	if err == nil {
		t.Fatal("ProcessOne() error = nil, want transient failure")
	}
	if store.retried != 1 || store.retryTerminal || !store.retryAt.After(time.Unix(100, 0)) {
		t.Fatalf("retry count=%d terminal=%v at=%v", store.retried, store.retryTerminal, store.retryAt)
	}
}

func TestProcessOnePermanentlyFailsInvalidPayloadAndAttemptLimit(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		ingress  domain.NotificationIngress
		storeErr error
	}{
		{name: "invalid payload", ingress: domain.NotificationIngress{ID: "bad", OrgID: "org", RecipientUserID: "user", LeaseOwner: "processor", Event: domain.AgentNotificationEvent{Type: domain.NotificationTypeNeedsInput, Payload: json.RawMessage(`{}`)}}},
		{name: "attempt limit", ingress: validIngress(10), storeErr: errors.New("still unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{claim: test.ingress, found: true, createErr: test.storeErr}
			service := NewService(store, Config{Owner: "processor"})
			_ = service.ProcessOne(context.Background())
			if store.retried != 1 || !store.retryTerminal {
				t.Fatalf("retry count=%d terminal=%v", store.retried, store.retryTerminal)
			}
		})
	}
}

func TestProcessOneAcceptsReclaimedExpiredLeaseAndContextCancellation(t *testing.T) {
	t.Parallel()
	expired := time.Now().Add(-time.Minute)
	ingress := validIngress(2)
	ingress.LeaseUntil = &expired
	store := &fakeStore{claim: ingress, found: true}
	service := NewService(store, Config{Owner: "new-owner"})
	if err := service.ProcessOne(context.Background()); err != nil || store.created != 1 {
		t.Fatalf("reclaimed ProcessOne() error = %v; created=%d", err, store.created)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	store = &fakeStore{claimErr: context.Canceled}
	service = NewService(store, Config{Owner: "processor"})
	if err := service.ProcessOne(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ProcessOne error = %v", err)
	}
}

func TestWakeIsNonBlockingWhenAlreadyQueued(t *testing.T) {
	t.Parallel()
	service := NewService(&fakeStore{}, Config{})
	service.Wake()
	done := make(chan struct{})
	go func() {
		service.Wake()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second Wake blocked")
	}
}

func validIngress(attempt int) domain.NotificationIngress {
	return domain.NotificationIngress{
		ID: "ingress-1", OrgID: "org-1", ProjectID: "project-1", SessionID: "session-1",
		RecipientUserID: "user-1", WorkerID: "worker-1", WorkerEpoch: 4,
		Event:        domain.AgentNotificationEvent{EventID: "evt-1", Type: domain.NotificationTypeNeedsInput, Payload: json.RawMessage(`{"activityId":"tool-1"}`)},
		AttemptCount: attempt, LeaseOwner: "processor",
	}
}

type fakeStore struct {
	claim                       domain.NotificationIngress
	found                       bool
	claimErr, createErr         error
	completed, created, retried int
	retryTerminal               bool
	retryAt                     time.Time
}

func (s *fakeStore) ClaimNotificationEvent(context.Context, string, time.Duration) (domain.NotificationIngress, bool, error) {
	return s.claim, s.found, s.claimErr
}

func (s *fakeStore) CompleteNotificationEvent(context.Context, string, string, string) error {
	s.completed++
	return nil
}

func (s *fakeStore) RetryNotificationEvent(_ context.Context, _, _, _, _ string, retryAt time.Time, terminal bool) error {
	s.retried++
	s.retryAt = retryAt
	s.retryTerminal = terminal
	return nil
}

func (s *fakeStore) CreateNotificationFromIngress(context.Context, domain.NotificationIngress, domain.Notification) (domain.Notification, bool, error) {
	s.created++
	return domain.Notification{}, true, s.createErr
}
