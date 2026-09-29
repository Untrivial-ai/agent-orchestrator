package httpapi

import "testing"

func TestSupportedGitHubWebhookEvent(t *testing.T) {
	for _, event := range []string{
		"installation", "installation_repositories", "pull_request",
		"check_suite", "check_run", "pull_request_review",
		"pull_request_review_comment", "pull_request_review_thread",
		"status", "push",
	} {
		if !supportedGitHubWebhookEvent(event) {
			t.Errorf("event %q should be queued", event)
		}
	}
	if supportedGitHubWebhookEvent("fork") {
		t.Fatal("unrelated repository events must remain ignored")
	}
}

func TestGitHubWebhookPullRequestNumber(t *testing.T) {
	tests := []struct {
		payload string
		want    int
	}{
		{`{"pull_request":{"number":17}}`, 17},
		{`{"check_run":{"pull_requests":[{"number":18}]}}`, 18},
		{`{"check_suite":{"pull_requests":[{"number":19}]}}`, 19},
		{`{"pull_request":{"number":20},"comment":{"id":1}}`, 20},
		{`{"pull_request":{"number":21},"thread":{"id":"PRRT_1"}}`, 21},
		{`{"sha":"abc123"}`, 0},
		{`{"before":"old","after":"new"}`, 0},
		{`{"repository":{"id":1}}`, 0},
	}
	for _, test := range tests {
		if got := githubWebhookPullRequestNumber([]byte(test.payload)); got != test.want {
			t.Errorf("payload %s: got %d want %d", test.payload, got, test.want)
		}
	}
}
