package lifecycle

import (
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestSCMToPRObservationPreservesParentReviewCorrelation(t *testing.T) {
	const (
		prURL     = "https://github.com/acme/repo/pull/7"
		reviewURL = "https://github.com/acme/repo/pull/7#pullrequestreview-4949207965"
	)

	got := scmToPRObservation(ports.SCMObservation{
		Fetched: true,
		PR: ports.SCMPRObservation{
			URL: prURL,
		},
		Review: ports.SCMReviewObservation{
			Threads: []ports.SCMReviewThreadObservation{
				{
					ID:   "thread-1",
					Path: "extra.txt",
					Line: 1,
					Comments: []ports.SCMReviewCommentObservation{
						{
							ID:        "comment-1",
							Author:    "reviewer",
							Body:      "Remove this file.",
							URL:       prURL + "#discussion_r1",
							ReviewURL: reviewURL,
						},
					},
				},
			},
		},
	})

	if len(got.Comments) != 1 {
		t.Fatalf(
			"scmToPRObservation dropped review comments needed for parent-review correlation; got %d comments",
			len(got.Comments),
		)
	}

	// Reflection keeps the regression compilable before PRCommentObservation
	// grows the transient ReviewURL correlation field.
	comment := reflect.ValueOf(got.Comments[0])
	field := comment.FieldByName("ReviewURL")
	if !field.IsValid() {
		t.Fatalf("PRCommentObservation must expose transient ReviewURL correlation")
	}
	if field.Kind() != reflect.String {
		t.Fatalf("ReviewURL kind = %s, want string", field.Kind())
	}
	if gotURL := field.String(); gotURL != reviewURL {
		t.Fatalf("ReviewURL = %q, want %q", gotURL, reviewURL)
	}
}
