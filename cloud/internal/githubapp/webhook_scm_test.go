package githubapp

import "testing"

func TestSCMWebhookPullRequestNumber(t *testing.T) {
	for _, test := range []struct {
		event   string
		payload string
		want    int
	}{
		{"pull_request", `{"pull_request":{"number":17}}`, 17},
		{"pull_request_review", `{"pull_request":{"number":18}}`, 18},
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
