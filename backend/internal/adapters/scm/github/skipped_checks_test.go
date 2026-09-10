package github

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestFetchPullRequestsSkippedAndStaleChecks(t *testing.T) {
	completed := func(conclusion string) map[string]any {
		return map[string]any{"__typename": "CheckRun", "name": "check-" + conclusion, "status": "COMPLETED", "conclusion": conclusion}
	}
	skipped := completed("SKIPPED")
	for _, tt := range []struct {
		name   string
		checks []any
		rollup string
		want   domain.CIState
	}{
		{"skipped only", []any{skipped}, "SUCCESS", domain.CIPassing},
		{"skipped and successful", []any{skipped, completed("SUCCESS")}, "SUCCESS", domain.CIPassing},
		{"skipped and failed", []any{skipped, completed("FAILURE")}, "FAILURE", domain.CIFailing},
		{"skipped and pending", []any{skipped, map[string]any{"__typename": "CheckRun", "name": "pending", "status": "IN_PROGRESS"}}, "PENDING", domain.CIPending},
		{"skipped and unknown", []any{skipped, completed("")}, "PENDING", domain.CIUnknown},
		{"skipped and billing blocked", []any{skipped, billingBlockedCheck()}, "FAILURE", domain.CIUnknown},
		{"skipped and stale", []any{skipped, completed("STALE")}, "FAILURE", domain.CIUnknown},
		{"stale only", []any{completed("STALE")}, "FAILURE", domain.CIUnknown},
		{"stale and successful", []any{completed("STALE"), completed("SUCCESS")}, "FAILURE", domain.CIUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeGH(t)
			pr := checkFixture(tt.checks...)
			statusRollup(pr)["state"] = tt.rollup
			fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"pr0": map[string]any{"pullRequest": pr}}})
			})
			obs, err := newProviderForTest(t, fake).FetchPullRequests(ctx(), []ports.SCMPRRef{{Number: 42}})
			if err != nil {
				t.Fatal(err)
			}
			if len(obs) != 1 || !obs[0].Fetched || obs[0].CI.Summary != string(tt.want) {
				t.Fatalf("observations = %#v, want %s", obs, tt.want)
			}
		})
	}
}
