package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestFetchPullRequestsLargeCheckSets(t *testing.T) {
	for _, tt := range []struct {
		total int
		last  map[string]any
		want  domain.CIState
	}{
		{201, map[string]any{"__typename": "CheckRun", "name": "last", "conclusion": "SUCCESS"}, domain.CIPassing},
		{256, map[string]any{"__typename": "CheckRun", "name": "last", "conclusion": "FAILURE"}, domain.CIFailing},
		{920, billingBlockedCheck(), domain.CIUnknown},
	} {
		t.Run(fmt.Sprintf("%d checks", tt.total), func(t *testing.T) {
			fake := newFakeGH(t)
			next := 0
			fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Query string }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				limit, alias := 20, "pr0"
				if next > 0 {
					limit, alias = 100, "repo"
					if !strings.Contains(request.Query, fmt.Sprintf(`contexts(first:100, after:"cursor-%d")`, next)) {
						t.Fatalf("follow-up must fetch a full page after the last cursor: %s", request.Query)
					}
				}
				var checks []any
				end := min(next+limit, tt.total)
				for ; next < end; next++ {
					check := map[string]any{"__typename": "CheckRun", "name": fmt.Sprintf("matrix-%d", next), "conclusion": "SUCCESS"}
					if next == tt.total-1 {
						check = tt.last
					}
					checks = append(checks, check)
				}
				pr := checkFixture(checks...)
				statusContexts(pr)["pageInfo"] = map[string]any{"hasNextPage": next < tt.total, "endCursor": fmt.Sprintf("cursor-%d", next)}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{alias: map[string]any{"pullRequest": pr}}})
			})
			obs, err := newProviderForTest(t, fake).FetchPullRequests(ctx(), []ports.SCMPRRef{{Number: 42}})
			if err != nil {
				t.Fatal(err)
			}
			if len(obs) != 1 || !obs[0].Fetched || obs[0].CI.Summary != string(tt.want) || len(obs[0].CI.Checks) != tt.total {
				t.Fatalf("incomplete or incorrectly classified check set: %#v", obs)
			}
			if got, want := fake.callsTo(http.MethodPost, "/graphql"), 1+(tt.total-20+99)/100; got != want {
				t.Fatalf("requests = %d, want %d", got, want)
			}
		})
	}
}

func TestFetchPullRequestsCancellationDuringCheckPaging(t *testing.T) {
	fake := newFakeGH(t)
	callCtx, cancel := context.WithCancel(ctx())
	defer cancel()
	fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, _ *http.Request) {
		if fake.callsTo(http.MethodPost, "/graphql") > 1 {
			cancel()
			http.Error(w, "cancelled", http.StatusServiceUnavailable)
			return
		}
		pr := checkFixture(billingBlockedCheck())
		statusContexts(pr)["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "next"}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"pr0": map[string]any{"pullRequest": pr}}})
	})
	obs, err := newProviderForTest(t, fake).FetchPullRequests(callCtx, []ports.SCMPRRef{{Number: 42}})
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("cancelled fetch = %#v, %v", obs, err)
	}
}
