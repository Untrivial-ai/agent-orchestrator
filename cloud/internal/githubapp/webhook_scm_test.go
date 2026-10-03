package githubapp

import (
	"context"
	"errors"
	"log/slog"
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
	matchingSessionID          string
	claimed                    *domain.PullRequest
	openedNotifications        int
	recentOpen                 []domain.GitHubWebhookDelivery
}

func (s *scmRefreshStore) RecentOpenedPullRequestWebhooks(context.Context, int64, time.Time, int) ([]domain.GitHubWebhookDelivery, error) {
	return s.recentOpen, nil
}

func (s *scmRefreshStore) SessionForGitHubPullRequestHead(context.Context, string, int64, string, string) (string, error) {
	if s.matchingSessionID == "" {
		return "", postgres.ErrNotFound
	}
	return s.matchingSessionID, nil
}

func (s *scmRefreshStore) ClaimPullRequestRecord(_ context.Context, orgID, sessionID string, input domain.PullRequest) (domain.PullRequest, error) {
	input.ID, input.OrgID, input.SessionID = "claimed-pr", orgID, sessionID
	s.claimed = &input
	s.byNumber[input.Number] = input
	return input, nil
}

func (s *scmRefreshStore) RecordPullRequestOpened(context.Context, string, domain.PullRequest, string) error {
	s.openedNotifications++
	return nil
}

func (s *scmRefreshStore) CreateReviewRun(context.Context, string, string, string, string) (domain.ReviewRun, bool, error) {
	return domain.ReviewRun{}, false, postgres.ErrNotFound
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

func (s *scmRefreshStore) GitHubInstallationRoutes(context.Context, int64) ([]domain.GitHubInstallationRoute, error) {
	if s.routeErr != nil {
		return nil, s.routeErr
	}
	return []domain.GitHubInstallationRoute{{OrgID: "org-1", InstallationID: "install-1"}}, nil
}

var errWebhookPRNotFound = postgres.ErrNotFound

func TestOpenedPRWebhookClaimsSessionBranchAndCreatesCard(t *testing.T) {
	store := &scmRefreshStore{byNumber: map[int]domain.PullRequest{}, matchingSessionID: "session-1"}
	service := &Service{store: store, logger: slog.Default(), refreshPullRequestStatus: func(_ context.Context, ref domain.PullRequestRef, _ domain.PullRequestRefreshContext) (domain.PullRequest, error) {
		return store.byNumber[ref.Number], nil
	}}
	delivery := domain.GitHubWebhookDelivery{DeliveryID: "opened-7", Event: "pull_request", Action: "opened", GitHubRepositoryID: 99,
		Payload: []byte(`{"action":"opened","pull_request":{"number":7,"html_url":"https://github.com/acme/widgets/pull/7","title":"Add docs","state":"open","head":{"ref":"ao/session-1","sha":"abc123"},"base":{"ref":"main"},"user":{"login":"octocat"}},"repository":{"full_name":"acme/widgets"}}`)}
	if err := service.processSCMWebhook(context.Background(), "org-1", delivery); err != nil {
		t.Fatal(err)
	}
	if store.claimed == nil || store.claimed.SessionID != "session-1" || store.claimed.Number != 7 {
		t.Fatalf("claimed = %+v, want PR #7 in session-1", store.claimed)
	}
	if store.openedNotifications != 1 {
		t.Fatalf("opened notifications = %d, want 1", store.openedNotifications)
	}
}

func TestWorkerRefReportReplaysEarlierOpenedWebhook(t *testing.T) {
	delivery := domain.GitHubWebhookDelivery{DeliveryID: "earlier-opened", Event: "pull_request", Action: "opened", GitHubRepositoryID: 99,
		Payload: []byte(`{"pull_request":{"number":8,"html_url":"https://github.com/acme/widgets/pull/8","title":"Add docs","state":"open","head":{"ref":"docs/custom","sha":"abc123"},"base":{"ref":"main"}},"repository":{"full_name":"acme/widgets"}}`)}
	store := &scmRefreshStore{byNumber: map[int]domain.PullRequest{}, matchingSessionID: "session-1", recentOpen: []domain.GitHubWebhookDelivery{delivery}}
	service := &Service{store: store, logger: slog.Default(), refreshPullRequestStatus: func(_ context.Context, ref domain.PullRequestRef, _ domain.PullRequestRefreshContext) (domain.PullRequest, error) {
		return store.byNumber[ref.Number], nil
	}}
	if err := service.ReconcileWorkerGitRefs(context.Background(), "org-1", 99, []domain.WorkerGitRef{{Branch: "docs/custom", SHA: "abc123"}}); err != nil {
		t.Fatal(err)
	}
	if store.claimed == nil || store.claimed.SessionID != "session-1" || store.openedNotifications != 1 {
		t.Fatalf("claim = %+v, notifications = %d", store.claimed, store.openedNotifications)
	}
}

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

func TestSCMWebhookRefreshFailureReturnsErrorForDeliveryRetry(t *testing.T) {
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
	err := service.processSCMWebhook(context.Background(), "org-1", delivery)
	if !errors.Is(err, refreshErr) {
		t.Fatalf("error = %v, want original refresh error", err)
	}
	if len(store.scheduled) != 0 {
		t.Fatalf("scheduled fallback = %+v, want none", store.scheduled)
	}
}

func TestSCMWebhookTimeoutReturnsErrorForDeliveryRetry(t *testing.T) {
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
	if len(store.scheduled) != 0 {
		t.Fatalf("scheduled fallback = %+v, want none", store.scheduled)
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

func TestSCMRepositoryWideWebhookReturnsFailureForDeliveryRetry(t *testing.T) {
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
	if len(store.scheduled) != 0 {
		t.Fatalf("scheduled fallback = %+v, want none", store.scheduled)
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
	if len(store.scheduled) != 0 {
		t.Fatalf("scheduled fallback = %+v, want none", store.scheduled)
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

func TestOpenedPullRequestWebhookCreatesBellNotification(t *testing.T) {
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

	if store.notifications != 1 {
		t.Fatalf("opened PR notifications = %d, want 1", store.notifications)
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
