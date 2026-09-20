package prstatus

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

type automaticReviewStore struct {
	enabled bool
}

func (s automaticReviewStore) OpenPullRequestRefs(context.Context) ([]domain.PullRequestRef, error) {
	return []domain.PullRequestRef{{ID: "pr-1", OrgID: "org-1"}}, nil
}

func (s automaticReviewStore) AutomaticReviewSession(context.Context, string, string) (string, string, bool, error) {
	return "session-1", "codex", s.enabled, nil
}

func (s automaticReviewStore) ApplyPullRequestAutomation(context.Context, domain.PullRequest) error {
	return nil
}

type automaticReviewGitHub struct {
	triggered int
}

func (g *automaticReviewGitHub) RefreshPullRequestStatus(context.Context, domain.PullRequestRef) (domain.PullRequest, error) {
	return domain.PullRequest{ID: "pr-1", HeadSHA: "abc", State: contract.PRStateOpen}, nil
}

func (g *automaticReviewGitHub) TriggerAutomaticReview(context.Context, string, string, string, domain.PullRequest) (domain.ReviewRun, bool, error) {
	g.triggered++
	return domain.ReviewRun{}, true, nil
}

func TestScanOnceTriggersEnabledAutomaticReview(t *testing.T) {
	github := &automaticReviewGitHub{}
	scanner := New(automaticReviewStore{enabled: true}, github, Options{})
	if err := scanner.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if github.triggered != 1 {
		t.Fatalf("automatic review triggers = %d, want 1", github.triggered)
	}
}

func TestScanOnceSkipsDisabledAutomaticReview(t *testing.T) {
	github := &automaticReviewGitHub{}
	scanner := New(automaticReviewStore{}, github, Options{})
	if err := scanner.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if github.triggered != 0 {
		t.Fatalf("automatic review triggers = %d, want 0", github.triggered)
	}
}
