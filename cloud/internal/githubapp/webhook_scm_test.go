package githubapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

type scheduledPullRequestRefresh struct {
	orgID, pullRequestID string
	reason               domain.PullRequestRefreshReason
	dueAt                time.Time
	message              string
}

type scmRefreshStore struct {
	Store
	byNumber                   map[int]domain.PullRequest
	byHead                     map[string]domain.PullRequest
	byRepo                     []domain.PullRequest
	routeErr                   error
	scheduled                  []scheduledPullRequestRefresh
	requireLiveScheduleContext bool
}

func (s *scmRefreshStore) PullRequestByGitHubReference(_ context.Context, _ string, _ int64, number int) (domain.PullRequest, error) {
	pr, ok := s.byNumber[number]
	if !ok {
		return domain.PullRequest{}, errWebhookPRNotFound
	}
	return pr, nil
}

func (s *scmRefreshStore) PullRequestByGitHubHead(_ context.Context, _ string, _ int64, headSHA string) (domain.PullRequest, error) {
	pr, ok := s.byHead[headSHA]
	if !ok {
		return domain.PullRequest{}, errWebhookPRNotFound
	}
	return pr, nil
}

func (s *scmRefreshStore) PullRequestsByGitHubRepository(context.Context, string, int64) ([]domain.PullRequest, error) {
	return s.byRepo, nil
}

func (s *scmRefreshStore) SchedulePullRequestRefresh(ctx context.Context, orgID, pullRequestID string, reason domain.PullRequestRefreshReason, dueAt time.Time, message string) error {
	if s.requireLiveScheduleContext && ctx.Err() != nil {
		return ctx.Err()
	}
	s.scheduled = append(s.scheduled, scheduledPullRequestRefresh{orgID: orgID, pullRequestID: pullRequestID, reason: reason, dueAt: dueAt, message: message})
	return nil
}

func (s *scmRefreshStore) GitHubInstallationRoute(context.Context, int64) (string, string, error) {
	return "", "", s.routeErr
}

var errWebhookPRNotFound = postgres.ErrNotFound

func webhookTestPullRequest(id string, number int) domain.PullRequest {
	return domain.PullRequest{ID: id, OrgID: "org-1", Provider: "github", Repository: "acme/widgets", Number: number}
}

func TestSCMWebhookSuccessUsesWebhookSourceWithoutSchedulingFallback(t *testing.T) {
	pr := webhookTestPullRequest("pr-1", 7)
	store := &scmRefreshStore{byNumber: map[int]domain.PullRequest{7: pr}}
	var gotRef domain.PullRequestRef
	var gotRefresh domain.PullRequestRefreshContext
	service := &Service{
		store: store,
		refreshPullRequestStatus: func(_ context.Context, ref domain.PullRequestRef, refresh domain.PullRequestRefreshContext) (domain.PullRequest, error) {
			gotRef, gotRefresh = ref, refresh
			return pr, nil
		},
	}
	delivery := domain.GitHubWebhookDelivery{DeliveryID: "delivery-ok", Event: "pull_request", GitHubRepositoryID: 99, Payload: []byte(`{"pull_request":{"number":7}}`)}
	if err := service.processSCMWebhook(context.Background(), "org-1", delivery); err != nil {
		t.Fatal(err)
	}
	if gotRef.ID != pr.ID || gotRefresh.Source != domain.PullRequestRefreshWebhook || gotRefresh.LeaseOwner != "" {
		t.Fatalf("refresh ref = %+v, context = %+v", gotRef, gotRefresh)
	}
	if len(store.scheduled) != 0 {
		t.Fatalf("scheduled fallback = %+v, want none", store.scheduled)
	}
}

func TestSCMWebhookRefreshFailureSchedulesOnlyResolvedPullRequest(t *testing.T) {
	pr := webhookTestPullRequest("pr-1", 7)
	store := &scmRefreshStore{byNumber: map[int]domain.PullRequest{7: pr}}
	refreshErr := errors.New("snapshot unavailable")
	service := &Service{
		store: store,
		refreshPullRequestStatus: func(context.Context, domain.PullRequestRef, domain.PullRequestRefreshContext) (domain.PullRequest, error) {
			return domain.PullRequest{}, refreshErr
		},
	}
	delivery := domain.GitHubWebhookDelivery{DeliveryID: "delivery-fail", Event: "pull_request", GitHubRepositoryID: 99, Payload: []byte(`{"pull_request":{"number":7}}`)}
	before := time.Now().UTC()
	err := service.processSCMWebhook(context.Background(), "org-1", delivery)
	after := time.Now().UTC()
	if !errors.Is(err, refreshErr) {
		t.Fatalf("error = %v, want original refresh error", err)
	}
	if len(store.scheduled) != 1 {
		t.Fatalf("scheduled = %+v", store.scheduled)
	}
	got := store.scheduled[0]
	if got.orgID != pr.OrgID || got.pullRequestID != pr.ID || got.reason != domain.PullRequestRefreshWebhookFailed || got.message != refreshErr.Error() || got.dueAt.Before(before) || got.dueAt.After(after) {
		t.Fatalf("scheduled fallback = %+v", got)
	}
}

func TestSCMWebhookTimeoutSchedulesFallbackWithFreshBoundedContext(t *testing.T) {
	pr := webhookTestPullRequest("pr-timeout", 9)
	store := &scmRefreshStore{
		byNumber:                   map[int]domain.PullRequest{9: pr},
		requireLiveScheduleContext: true,
	}
	service := &Service{
		store: store,
		refreshPullRequestStatus: func(context.Context, domain.PullRequestRef, domain.PullRequestRefreshContext) (domain.PullRequest, error) {
			return domain.PullRequest{}, context.DeadlineExceeded
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	delivery := domain.GitHubWebhookDelivery{DeliveryID: "delivery-timeout", Event: "pull_request", GitHubRepositoryID: 99, Payload: []byte(`{"pull_request":{"number":9}}`)}
	err := service.processSCMWebhook(ctx, "org-1", delivery)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want original deadline error", err)
	}
	if len(store.scheduled) != 1 || store.scheduled[0].pullRequestID != pr.ID {
		t.Fatalf("scheduled = %+v, want timeout fallback for %s", store.scheduled, pr.ID)
	}
}

func TestSCMWebhookUnresolvedOrMalformedEventSchedulesNothing(t *testing.T) {
	tests := []struct {
		name     string
		service  *Service
		delivery domain.GitHubWebhookDelivery
		call     func(*Service, domain.GitHubWebhookDelivery) error
	}{
		{
			name:     "no durable pull request",
			service:  &Service{store: &scmRefreshStore{byNumber: map[int]domain.PullRequest{}}},
			delivery: domain.GitHubWebhookDelivery{Event: "pull_request", GitHubRepositoryID: 99, Payload: []byte(`{"pull_request":{"number":7}}`)},
			call: func(service *Service, delivery domain.GitHubWebhookDelivery) error {
				return service.processSCMWebhook(context.Background(), "org-1", delivery)
			},
		},
		{
			name:     "malformed JSON",
			service:  &Service{store: &scmRefreshStore{}},
			delivery: domain.GitHubWebhookDelivery{Event: "pull_request", GitHubRepositoryID: 99, Payload: []byte(`{`)},
			call: func(service *Service, delivery domain.GitHubWebhookDelivery) error {
				return service.processSCMWebhook(context.Background(), "org-1", delivery)
			},
		},
		{
			name:     "missing installation route",
			service:  &Service{store: &scmRefreshStore{routeErr: errors.New("installation route unavailable")}},
			delivery: domain.GitHubWebhookDelivery{GitHubInstallationID: 123, Event: "pull_request", GitHubRepositoryID: 99, Payload: []byte(`{"pull_request":{"number":7}}`)},
			call: func(service *Service, delivery domain.GitHubWebhookDelivery) error {
				return service.processWebhook(context.Background(), delivery)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.call(tt.service, tt.delivery)
			store := tt.service.store.(*scmRefreshStore)
			if len(store.scheduled) != 0 {
				t.Fatalf("scheduled fallback = %+v, want none", store.scheduled)
			}
		})
	}
}

func TestSCMRepositoryWideWebhookSchedulesOnlyFailedPullRequest(t *testing.T) {
	first := webhookTestPullRequest("pr-1", 7)
	second := webhookTestPullRequest("pr-2", 8)
	store := &scmRefreshStore{byRepo: []domain.PullRequest{first, second}}
	refreshErr := errors.New("second refresh failed")
	service := &Service{
		store: store,
		refreshPullRequestStatus: func(_ context.Context, ref domain.PullRequestRef, refresh domain.PullRequestRefreshContext) (domain.PullRequest, error) {
			if refresh.Source != domain.PullRequestRefreshWebhook {
				t.Fatalf("refresh source = %q", refresh.Source)
			}
			if ref.ID == second.ID {
				return domain.PullRequest{}, refreshErr
			}
			return first, nil
		},
	}
	delivery := domain.GitHubWebhookDelivery{DeliveryID: "delivery-push", Event: "push", GitHubRepositoryID: 99, Payload: []byte(`{"before":"old","after":"new"}`)}
	if err := service.processSCMWebhook(context.Background(), "org-1", delivery); !errors.Is(err, refreshErr) {
		t.Fatalf("error = %v, want refresh error", err)
	}
	if len(store.scheduled) != 1 || store.scheduled[0].pullRequestID != second.ID {
		t.Fatalf("scheduled = %+v, want only %s", store.scheduled, second.ID)
	}
}

func TestSCMRepositoryWideWebhookContinuesAfterOneRefreshFailure(t *testing.T) {
	first := webhookTestPullRequest("pr-1", 7)
	second := webhookTestPullRequest("pr-2", 8)
	store := &scmRefreshStore{byRepo: []domain.PullRequest{first, second}}
	refreshErr := errors.New("first refresh failed")
	var refreshed []string
	service := &Service{
		store: store,
		refreshPullRequestStatus: func(_ context.Context, ref domain.PullRequestRef, _ domain.PullRequestRefreshContext) (domain.PullRequest, error) {
			refreshed = append(refreshed, ref.ID)
			if ref.ID == first.ID {
				return domain.PullRequest{}, refreshErr
			}
			return second, nil
		},
	}
	delivery := domain.GitHubWebhookDelivery{DeliveryID: "delivery-push-first-fails", Event: "push", GitHubRepositoryID: 99, Payload: []byte(`{"before":"old","after":"new"}`)}
	if err := service.processSCMWebhook(context.Background(), "org-1", delivery); !errors.Is(err, refreshErr) {
		t.Fatalf("error = %v, want refresh error", err)
	}
	if len(refreshed) != 2 || refreshed[0] != first.ID || refreshed[1] != second.ID {
		t.Fatalf("refreshed = %v, want both pull requests", refreshed)
	}
	if len(store.scheduled) != 1 || store.scheduled[0].pullRequestID != first.ID {
		t.Fatalf("scheduled = %+v, want only %s", store.scheduled, first.ID)
	}
}

type scmNotificationStore struct {
	Store
	notifications int
}

func (s *scmNotificationStore) PullRequestByGitHubReference(
	context.Context, string, int64, int,
) (domain.PullRequest, error) {
	return domain.PullRequest{
		ID: "pr-1", OrgID: "org-1", SessionID: "session-1",
		Provider: "github", Repository: "invalid", Number: 7,
	}, nil
}

func (s *scmNotificationStore) RecordPullRequestOpened(
	context.Context, string, domain.PullRequest, string,
) error {
	s.notifications++
	return nil
}

func TestOpenedPullRequestWebhookDoesNotCreateBellNotification(t *testing.T) {
	t.Parallel()
	store := &scmNotificationStore{}
	service := &Service{
		store: store,
		refreshPullRequestStatus: func(context.Context, domain.PullRequestRef, domain.PullRequestRefreshContext) (domain.PullRequest, error) {
			return domain.PullRequest{}, nil
		},
	}
	delivery := domain.GitHubWebhookDelivery{
		DeliveryID: "delivery-1", Event: "pull_request", Action: "opened",
		GitHubRepositoryID: 99, Payload: []byte(`{"pull_request":{"number":7}}`),
	}

	_ = service.processSCMWebhook(context.Background(), "org-1", delivery)

	if store.notifications != 0 {
		t.Fatalf("opened PR notifications = %d, want 0 for local parity", store.notifications)
	}
}

func TestSCMWebhookPullRequestNumber(t *testing.T) {
	for _, test := range []struct {
		event   string
		payload string
		want    int
	}{
		{"pull_request", `{"pull_request":{"number":17}}`, 17},
		{"pull_request_review", `{"pull_request":{"number":18}}`, 18},
		{"pull_request_review_comment", `{"pull_request":{"number":21}}`, 21},
		{"pull_request_review_thread", `{"pull_request":{"number":22}}`, 22},
		{"check_run", `{"check_run":{"pull_requests":[{"number":19}]}}`, 19},
		{"check_suite", `{"check_suite":{"pull_requests":[{"number":20}]}}`, 20},
		{"check_run", `{"check_run":{"pull_requests":[]}}`, 0},
	} {
		target, err := scmWebhookTargets(test.event, []byte(test.payload))
		if err != nil {
			t.Fatalf("%s: %v", test.event, err)
		}
		if target.PullRequestNumber != test.want {
			t.Errorf("%s: got %d want %d", test.event, target.PullRequestNumber, test.want)
		}
	}
}

func TestSCMWebhookTargetsStatusAndPush(t *testing.T) {
	tests := []struct {
		event   string
		payload string
		want    scmWebhookTargetSet
	}{
		{
			event:   "status",
			payload: `{"sha":"abc123"}`,
			want:    scmWebhookTargetSet{HeadSHA: "abc123"},
		},
		{
			event:   "push",
			payload: `{"before":"old123","after":"new456"}`,
			want:    scmWebhookTargetSet{RepositoryWide: true},
		},
	}
	for _, test := range tests {
		got, err := scmWebhookTargets(test.event, []byte(test.payload))
		if err != nil {
			t.Fatalf("%s: %v", test.event, err)
		}
		if got != test.want {
			t.Errorf("%s target = %#v, want %#v", test.event, got, test.want)
		}
	}
}

func TestSCMWebhookTargetsRejectMalformedJSON(t *testing.T) {
	if _, err := scmWebhookTargets("pull_request", []byte(`{`)); err == nil {
		t.Fatal("malformed payload error = nil")
	}
}

func TestSCMWebhookTargetsFallBackToCheckHeadSHA(t *testing.T) {
	target, err := scmWebhookTargets(
		"check_run",
		[]byte(`{"check_run":{"head_sha":"abc123","pull_requests":[]}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if target.PullRequestNumber != 0 || target.HeadSHA != "abc123" {
		t.Fatalf("target = %#v", target)
	}
}
