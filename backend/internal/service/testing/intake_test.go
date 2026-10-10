package testing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type snapshotFake struct {
	calls int
	fail  error
}

func (f *snapshotFake) Snapshot(context.Context, domain.ProjectRecord, string) (domain.TestPullRequestSnapshot, string, error) {
	f.calls++
	return domain.TestPullRequestSnapshot{URL: "https://github.com/owner/agent-orchestrator/pull/1", Title: "quoted", Body: "untrusted body", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), DiffPath: "/owned/full.diff", DiffSHA256: strings.Repeat("c", 64)}, "/owned/warm/checkout", f.fail
}
func TestPRIntakeCreatesTwoPinnedRunsFromOneSnapshot(t *testing.T) {
	intake := &snapshotFake{}
	f := newFixture(t, func(d *Deps) { d.PullRequests = intake })
	before := f.worker.binding.Attempt.ID
	runs, err := f.svc.CreatePullRequestRuns(context.Background(), CreatePullRequestRunsInput{ProjectID: "ao", PRURL: "https://github.com/owner/agent-orchestrator/pull/1", RecipeID: "native", Requester: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if intake.calls != 1 || runs.Base.CommitSHA != strings.Repeat("a", 40) || runs.Head.CommitSHA != strings.Repeat("b", 40) || runs.Head.LinkedRunID != runs.Base.ID || runs.Base.ID == runs.Head.ID {
		t.Fatalf("incorrect pinned pair: %+v calls=%d", runs, intake.calls)
	}
	if runs.Base.IssueSnapshot != runs.Head.IssueSnapshot || runs.Base.RecipeSnapshot != runs.Head.RecipeSnapshot || f.worker.binding.Attempt.ID != before {
		t.Fatal("snapshot changed or worker started during intake")
	}
	var recipe Recipe
	if err := json.Unmarshal([]byte(runs.Head.RecipeSnapshot), &recipe); err != nil {
		t.Fatal(err)
	}
	if recipe.PullRequest == nil || recipe.PullRequest.HeadSHA != runs.Head.CommitSHA || recipe.CheckoutPath != "/owned/warm/checkout" {
		t.Fatal("PR recipe snapshot missing", recipe)
	}
	for _, run := range []domain.TestRunRecord{runs.Base, runs.Head} {
		stored, found, err := f.store.GetTestRun(context.Background(), run.ID)
		if err != nil || !found || stored.RecipeSnapshot != run.RecipeSnapshot {
			t.Fatal("run snapshot not persisted", err)
		}
	}
}
func TestPRIntakeFailureKeepsOriginalCause(t *testing.T) {
	intake := &snapshotFake{fail: errors.New("gh: exit status 1: fork permission denied")}
	f := newFixture(t, func(d *Deps) { d.PullRequests = intake })
	_, err := f.svc.CreatePullRequestRuns(context.Background(), CreatePullRequestRunsInput{ProjectID: "ao", PRURL: "https://github.com/o/r/pull/1", RecipeID: "native", Requester: "worker"})
	if code(err) != "TEST_PR_INTAKE_FAILED" || !strings.Contains(err.Error(), "fork permission denied") {
		t.Fatal("provider cause masked", err)
	}
}
