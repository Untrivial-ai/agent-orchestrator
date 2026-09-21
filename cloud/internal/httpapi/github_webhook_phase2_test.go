package httpapi

import "testing"

func TestSupportedGitHubWebhookEvent(t *testing.T) {
	for _, event := range []string{"installation", "installation_repositories", "pull_request", "check_suite", "check_run", "pull_request_review"} {
		if !supportedGitHubWebhookEvent(event) {
			t.Errorf("event %q should be queued", event)
		}
	}
	if supportedGitHubWebhookEvent("push") {
		t.Fatal("push must remain ignored")
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
		{`{"repository":{"id":1}}`, 0},
	}
	for _, test := range tests {
		if got := githubWebhookPullRequestNumber([]byte(test.payload)); got != test.want {
			t.Errorf("payload %s: got %d want %d", test.payload, got, test.want)
		}
	}
}
