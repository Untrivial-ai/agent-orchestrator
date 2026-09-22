package githubapp

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

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
	service := &Service{store: store}
	delivery := domain.GitHubWebhookDelivery{
		DeliveryID: "delivery-1", Event: "pull_request", Action: "opened",
		GitHubRepositoryID: 99, Payload: []byte(`{"pull_request":{"number":7}}`),
	}

	_ = service.processSCMWebhook(context.Background(), "org-1", delivery)

	if store.notifications != 0 {
		t.Fatalf("opened PR notifications = %d, want 0 for local parity", store.notifications)
	}
}

func TestAggregateCIStateTreatsNoChecksAsPassing(t *testing.T) {
	t.Parallel()
	if got := aggregateCIState(nil); got != contract.CIPassing {
		t.Fatalf("aggregateCIState(nil) = %q, want %q", got, contract.CIPassing)
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
		got, err := scmWebhookPullRequestNumber(test.event, []byte(test.payload))
		if err != nil {
			t.Fatalf("%s: %v", test.event, err)
		}
		if got != test.want {
			t.Errorf("%s: got %d want %d", test.event, got, test.want)
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
			want: scmWebhookTargetSet{
				BeforeSHA: "old123", AfterSHA: "new456", RepositoryWide: true,
			},
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

func TestSCMWebhookReferenceFallsBackToCheckHeadSHA(t *testing.T) {
	ref, err := scmWebhookPullRequestReference(
		"check_run",
		[]byte(`{"check_run":{"head_sha":"abc123","pull_requests":[]}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Number != 0 || ref.HeadSHA != "abc123" {
		t.Fatalf("reference = %#v", ref)
	}
}
