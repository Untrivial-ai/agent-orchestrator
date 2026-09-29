package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func TestPullRequestSnapshotQueryHasBalancedDelimiters(t *testing.T) {
	if err := validateGraphQLDelimiters(pullRequestSnapshotQuery); err != nil {
		t.Fatal(err)
	}
}

func TestMapRESTMergeability(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name      string
		mergeable *bool
		state     string
		want      contract.Mergeability
	}{
		{"clean+mergeable", &yes, "clean", contract.MergeMergeable},
		{"has_hooks+mergeable", &yes, "has_hooks", contract.MergeMergeable},
		{"dirty", &no, "dirty", contract.MergeConflicting},
		{"blocked", &yes, "blocked", contract.MergeBlocked},
		{"behind", &yes, "behind", contract.MergeBlocked},
		{"unstable", &yes, "unstable", contract.MergeUnstable},
		{"nil mergeable stays unknown", nil, "unknown", contract.MergeUnknown},
		{"clean but not mergeable stays unknown", &no, "clean", contract.MergeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapRESTMergeability(tc.mergeable, tc.state); got != tc.want {
				t.Fatalf("mapRESTMergeability(%v,%q) = %q, want %q", tc.mergeable, tc.state, got, tc.want)
			}
		})
	}
}

// When GraphQL reports mergeable=UNKNOWN for an open PR, the fetch must fall back
// to the REST pulls endpoint (which forces GitHub's computation) and adopt its
// mergeable_state — the fix for a PR stranded at mergeability=unknown forever.
func TestFetchPullRequestSnapshotRESTFallbackResolvesUnknownMergeability(t *testing.T) {
	var restHits int
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
				"number": 7, "id": "PR_7", "url": "https://github.com/acme/widgets/pull/7", "state": "OPEN",
				"mergeable": "UNKNOWN", "mergeStateStatus": "UNKNOWN", "reviewDecision": "REVIEW_REQUIRED",
				"headRefName": "feature", "headRefOid": "head123", "baseRefName": "main", "baseRefOid": "base123",
			}}}})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/7" {
			restHits++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 7, "html_url": "https://github.com/acme/widgets/pull/7", "state": "open",
				"mergeable": true, "mergeable_state": "clean",
				"head": map[string]any{"sha": "head123", "ref": "feature"}, "base": map[string]any{"ref": "main"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer gh.Close()

	client := NewRESTClient(gh.URL, gh.Client())
	snapshot, err := client.FetchPullRequestSnapshotWithToken(context.Background(), "token", "acme", "widgets", 7)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if restHits != 1 {
		t.Fatalf("REST pulls endpoint hit %d times, want 1 (fallback on GraphQL UNKNOWN)", restHits)
	}
	if snapshot.Observation.Mergeability != contract.MergeMergeable {
		t.Fatalf("mergeability = %q, want mergeable (resolved via REST fallback)", snapshot.Observation.Mergeability)
	}
}

func validateGraphQLDelimiters(document string) error {
	openers := map[rune]rune{'}': '{', ')': '(', ']': '['}
	var stack []rune
	for offset, current := range document {
		switch current {
		case '{', '(', '[':
			stack = append(stack, current)
		case '}', ')', ']':
			if len(stack) == 0 || stack[len(stack)-1] != openers[current] {
				return fmt.Errorf("GraphQL query has unmatched %q at byte %d", current, offset)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		return fmt.Errorf("GraphQL query has unclosed %q", stack[len(stack)-1])
	}
	return nil
}

func TestNormalizePullRequestSnapshotIncludesReviewsThreadsAndStatusContexts(t *testing.T) {
	var response githubPullRequestSnapshotResponse
	if err := json.Unmarshal([]byte(`{
		"data":{"repository":{"pullRequest":{
			"number":7,"id":"PR_7","url":"https://github.com/acme/widgets/pull/7",
			"state":"OPEN","isDraft":false,"merged":false,"closed":false,
			"title":"Webhook parity","additions":12,"deletions":3,"changedFiles":2,
			"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"CHANGES_REQUESTED",
			"headRefName":"feature","headRefOid":"head123","baseRefName":"main","baseRefOid":"base123",
			"author":{"login":"owner","avatarUrl":"https://avatars.example/owner"},
			"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE","contexts":{"nodes":[
				{"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://ci/test","databaseId":11},
				{"__typename":"StatusContext","context":"lint","state":"SUCCESS","targetUrl":"https://ci/lint"}
			]}}}}]},
			"reviews":{"nodes":[
				{"id":"R1","databaseId":101,"state":"COMMENTED","url":"https://github.com/r1","body":"looks good with one note","submittedAt":"2026-09-22T00:00:00Z","author":{"login":"mohak","__typename":"User"}},
				{"id":"R2","databaseId":102,"state":"CHANGES_REQUESTED","url":"https://github.com/r2","body":"fix this","submittedAt":"2026-09-22T00:01:00Z","author":{"login":"reviewer","__typename":"User"}}
			],"pageInfo":{"hasNextPage":false}},
			"reviewThreads":{"nodes":[
				{"id":"T1","isResolved":false,"isOutdated":false,"path":"main.go","line":12,"comments":{"nodes":[
					{"id":"C1","databaseId":201,"body":"rename this","url":"https://github.com/c1","author":{"login":"reviewer","__typename":"User"},"pullRequestReview":{"databaseId":102}}
				]}},
				{"id":"T2","isResolved":true,"isOutdated":false,"path":"bot.go","line":4,"comments":{"nodes":[
					{"id":"C2","databaseId":202,"body":"automated note","url":"https://github.com/c2","author":{"login":"lint-bot","__typename":"Bot"},"pullRequestReview":{"databaseId":101}}
				]}}
			],"pageInfo":{"hasNextPage":false}}
		}}}}
	`), &response); err != nil {
		t.Fatal(err)
	}

	got, err := normalizePullRequestSnapshot(response)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation.CIState != contract.CIFailing || len(got.Checks) != 2 {
		t.Fatalf("checks = %+v, state = %q", got.Checks, got.Observation.CIState)
	}
	if len(got.Reviews) != 2 || got.Reviews[0].State != contract.ReviewNone || got.Reviews[0].Body != "looks good with one note" {
		t.Fatalf("reviews = %+v", got.Reviews)
	}
	if len(got.Threads) != 2 || len(got.Comments) != 2 || got.Comments[1].IsBot != true {
		t.Fatalf("threads = %+v comments = %+v", got.Threads, got.Comments)
	}
	if got.Observation.Mergeability != contract.MergeMergeable || got.AuthorAvatarURL == "" {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestNormalizePullRequestSnapshotMarksPartialReviewWindow(t *testing.T) {
	response := githubPullRequestSnapshotResponse{}
	response.Data.Repository.PullRequest.Number = 1
	response.Data.Repository.PullRequest.URL = "https://github.com/acme/widgets/pull/1"
	response.Data.Repository.PullRequest.Reviews.PageInfo.HasNextPage = true
	response.Data.Repository.PullRequest.ReviewThreads.PageInfo.HasNextPage = true
	got, err := normalizePullRequestSnapshot(response)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ReviewsPartial {
		t.Fatal("ReviewsPartial = false, want true")
	}
}

func TestNormalizePullRequestSnapshotEncodesNoChecksAsArray(t *testing.T) {
	tests := []struct {
		name       string
		withRollup bool
	}{
		{name: "missing rollup"},
		{name: "empty rollup", withRollup: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var response githubPullRequestSnapshotResponse
			response.Data.Repository.PullRequest.Number = 1
			response.Data.Repository.PullRequest.URL = "https://github.com/acme/widgets/pull/1"
			if tt.withRollup {
				response.Data.Repository.PullRequest.Commits.Nodes = append(
					response.Data.Repository.PullRequest.Commits.Nodes,
					struct {
						Commit struct {
							StatusCheckRollup *struct {
								State    string
								Contexts struct {
									Nodes []struct {
										Type                                 string `json:"__typename"`
										Name, Status, Conclusion, DetailsURL string
										DatabaseID                           int64
										Context, State, TargetURL            string
									} `json:"nodes"`
									PageInfo githubPageInfo `json:"pageInfo"`
								} `json:"contexts"`
							} `json:"statusCheckRollup"`
						} `json:"commit"`
					}{},
				)
				response.Data.Repository.PullRequest.Commits.Nodes[0].Commit.StatusCheckRollup = &struct {
					State    string
					Contexts struct {
						Nodes []struct {
							Type                                 string `json:"__typename"`
							Name, Status, Conclusion, DetailsURL string
							DatabaseID                           int64
							Context, State, TargetURL            string
						} `json:"nodes"`
						PageInfo githubPageInfo `json:"pageInfo"`
					} `json:"contexts"`
				}{}
			}

			snapshot, err := normalizePullRequestSnapshot(response)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(snapshot.Observation.Checks); got != "[]" {
				t.Fatalf("checks = %s, want []", got)
			}
		})
	}
}
