package github

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestBuildReviewThreadsQueryIncludesParentReviewURL(t *testing.T) {
	query := buildReviewThreadsQuery(
		ports.SCMPRRef{
			Repo: ports.SCMRepo{
				Owner: "acme",
				Name:  "repo",
			},
			Number: 7,
		},
		"",
		false,
	)

	if !strings.Contains(query, "pullRequestReview{ url }") {
		t.Fatalf(
			"review-thread query must request each comment's parent review URL; query:\n%s",
			query,
		)
	}
}

func TestSCMThreadFromGraphQLCarriesParentReviewURL(t *testing.T) {
	const reviewURL = "https://github.com/acme/repo/pull/7#pullrequestreview-4949207965"

	thread := scmThreadFromGraphQL(map[string]any{
		"id":         "THREAD_1",
		"isResolved": false,
		"path":       "extra.txt",
		"line":       float64(1),
		"comments": map[string]any{
			"nodes": []any{
				map[string]any{
					"id":   "COMMENT_1",
					"body": "Remove this file.",
					"url":  "https://github.com/acme/repo/pull/7#discussion_r1",
					"pullRequestReview": map[string]any{
						"url": reviewURL,
					},
					"author": map[string]any{
						"login":      "reviewer",
						"__typename": "User",
					},
				},
			},
		},
	})

	if len(thread.Comments) != 1 {
		t.Fatalf("comments = %#v; want exactly 1", thread.Comments)
	}

	// Reflection keeps this regression compilable before the DTO grows
	// the ReviewURL field. The expected RED is behavioral, not a build error.
	comment := reflect.ValueOf(thread.Comments[0])
	field := comment.FieldByName("ReviewURL")
	if !field.IsValid() {
		t.Fatalf("SCMReviewCommentObservation must expose ReviewURL")
	}
	if field.Kind() != reflect.String {
		t.Fatalf("ReviewURL kind = %s, want string", field.Kind())
	}
	if got := field.String(); got != reviewURL {
		t.Fatalf("ReviewURL = %q, want %q", got, reviewURL)
	}
}
