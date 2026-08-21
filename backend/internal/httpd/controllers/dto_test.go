package controllers_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

func TestNewSessionPRSummaryMapsProviderReviewEntries(t *testing.T) {
	submitted := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	in := sessionsvc.PRSummary{
		URL: "https://github.com/o/r/pull/7",
		Review: sessionsvc.PRReviewSummary{
			Decision: domain.ReviewChangesRequest,
			UnresolvedBy: []sessionsvc.PRUnresolvedReviewer{{
				ReviewerID: "bob",
				Count:      1,
				Links:      []sessionsvc.PRReviewCommentLink{{URL: "comment-url", ReviewID: "4876751117", Body: "please fix this", AutoInjectReview: false}},
			}},
			Reviews: []sessionsvc.PRReviewEntry{{
				Reviewer:         "alice",
				Verdict:          domain.ReviewApproved,
				Body:             "looks good to me",
				URL:              "https://github.com/o/r/pull/7#pullrequestreview-1",
				SubmittedAt:      submitted,
				IsBot:            true,
				AutoInjectReview: false,
			}},
		},
	}

	got := controllers.NewSessionPRSummary(in)
	if len(got.Review.Reviews) != 1 {
		t.Fatalf("review entries = %+v, want 1", got.Review.Reviews)
	}
	entry := got.Review.Reviews[0]
	if entry.ReviewerID != "alice" {
		t.Fatalf("reviewerId = %q, want alice", entry.ReviewerID)
	}
	if entry.Verdict != domain.ReviewApproved {
		t.Fatalf("verdict = %q, want approved", entry.Verdict)
	}
	if entry.Body != "looks good to me" {
		t.Fatalf("body = %q", entry.Body)
	}
	if entry.ReviewURL != "https://github.com/o/r/pull/7#pullrequestreview-1" {
		t.Fatalf("reviewUrl = %q", entry.ReviewURL)
	}
	if !entry.SubmittedAt.Equal(submitted) {
		t.Fatalf("submittedAt = %v, want %v", entry.SubmittedAt, submitted)
	}
	if !entry.IsBot {
		t.Fatalf("isBot = false, want true")
	}
	if entry.AutoInjectReview {
		t.Fatal("autoInjectReview = true, want false")
	}
	if got.Review.UnresolvedBy[0].Links[0].ReviewID != "4876751117" {
		t.Fatalf("reviewId = %q, want 4876751117", got.Review.UnresolvedBy[0].Links[0].ReviewID)
	}
	if len(got.Review.UnresolvedBy) != 1 || len(got.Review.UnresolvedBy[0].Links) != 1 || got.Review.UnresolvedBy[0].Links[0].AutoInjectReview {
		t.Fatalf("unresolved comment links = %+v, want one not-injected link", got.Review.UnresolvedBy)
	}
	if got.Review.UnresolvedBy[0].Links[0].Body != "please fix this" {
		t.Fatalf("unresolved comment body = %q, want body text", got.Review.UnresolvedBy[0].Links[0].Body)
	}
}

func TestNewSessionPRSummaryExposesCIFailureInjectionPolicy(t *testing.T) {
	got := controllers.NewSessionPRSummary(sessionsvc.PRSummary{
		CI: sessionsvc.PRCISummary{State: domain.CIFailing, AutoInjectCI: false},
	})
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if got.CI.State != domain.CIFailing {
		t.Fatalf("CI state = %q, want failing", got.CI.State)
	}
	if !strings.Contains(string(payload), `"autoInjectCI":false`) {
		t.Fatalf("CI summary payload = %s, want explicit disabled injection policy", payload)
	}
}
