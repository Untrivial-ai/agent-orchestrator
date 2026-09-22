package githubapp

import (
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

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
