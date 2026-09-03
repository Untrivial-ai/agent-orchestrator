package trackerintake

// Regression tests for #2921: after #2777 excluded terminated sessions from the
// seen set, intake would spawn a duplicate worker (and a duplicate PR) for an
// open issue whose worker was terminated after opening a PR. A terminated
// session that left an open or merged PR now keeps its issue claimed, while a
// terminated session that produced no PR (or only a closed, unmerged one) still
// frees its issue for re-spawn (#2746).

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func intakeProject() domain.ProjectRecord {
	return domain.ProjectRecord{
		ID:            "demo",
		RepoOriginURL: "https://github.com/acme/demo.git",
		Config:        domain.ProjectConfig{TrackerIntake: domain.TrackerIntakeConfig{Enabled: true, Assignee: "alice"}},
	}
}

func openIssue12() *fakeTracker {
	return &fakeTracker{issues: []domain.Issue{{
		ID:        domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "acme/demo#12"},
		Title:     "Implement feature X",
		State:     domain.IssueOpen,
		Assignees: []string{"alice"},
	}}}
}

// terminatedSessionForIssue12 stands in for a worker that opened its PR and was
// then killed or crashed: the session row (and its PR rows) persist with
// IsTerminated=true.
func terminatedSessionForIssue12() domain.SessionRecord {
	return domain.SessionRecord{ID: "demo-1", ProjectID: "demo", IssueID: "github:acme/demo#12", IsTerminated: true}
}

func TestPollDoesNotRespawnIssueWithOpenPRFromTerminatedSession(t *testing.T) {
	store := &fakeStore{
		projects: []domain.ProjectRecord{intakeProject()},
		sessions: []domain.SessionRecord{terminatedSessionForIssue12()},
		prsBySession: map[domain.SessionID][]domain.PRFacts{
			"demo-1": {{Number: 100, Merged: false, Closed: false}}, // PR #100 still open
		},
	}
	spawner := &fakeSpawner{}

	if err := New(singleResolver(openIssue12()), store, spawner, Config{Logger: discardLogger()}).Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("spawn calls = %+v, want 0: an open PR from the terminated worker must not be duplicated (#2921)", spawner.calls)
	}
}

func TestPollDoesNotRespawnIssueWithMergedPRFromTerminatedSession(t *testing.T) {
	store := &fakeStore{
		projects: []domain.ProjectRecord{intakeProject()},
		sessions: []domain.SessionRecord{terminatedSessionForIssue12()},
		prsBySession: map[domain.SessionID][]domain.PRFacts{
			"demo-1": {{Number: 100, Merged: true, Closed: true}}, // merged (GitHub marks a merged PR closed)
		},
	}
	spawner := &fakeSpawner{}

	if err := New(singleResolver(openIssue12()), store, spawner, Config{Logger: discardLogger()}).Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("spawn calls = %+v, want 0: a merged PR means the work was done; do not redo it", spawner.calls)
	}
}

func TestPollRespawnsIssueWhenTerminatedSessionPRClosedUnmerged(t *testing.T) {
	store := &fakeStore{
		projects: []domain.ProjectRecord{intakeProject()},
		sessions: []domain.SessionRecord{terminatedSessionForIssue12()},
		prsBySession: map[domain.SessionID][]domain.PRFacts{
			"demo-1": {{Number: 100, Merged: false, Closed: true}}, // rejected / abandoned
		},
	}
	spawner := &fakeSpawner{}

	if err := New(singleResolver(openIssue12()), store, spawner, Config{Logger: discardLogger()}).Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(spawner.calls) != 1 || spawner.calls[0].IssueID != "github:acme/demo#12" {
		t.Fatalf("spawn calls = %+v, want one respawn: a closed, unmerged PR should be retried fresh", spawner.calls)
	}
}

func TestPollTreatsIssueClaimedWhenPRLookupFails(t *testing.T) {
	store := &fakeStore{
		projects: []domain.ProjectRecord{intakeProject()},
		sessions: []domain.SessionRecord{terminatedSessionForIssue12()},
		prsErr:   map[domain.SessionID]error{"demo-1": errors.New("db unavailable")},
	}
	spawner := &fakeSpawner{}

	if err := New(singleResolver(openIssue12()), store, spawner, Config{Logger: discardLogger()}).Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("spawn calls = %+v, want 0: a PR-lookup failure must fail safe and not spawn a possible duplicate", spawner.calls)
	}
}

// A live session claims its issue outright and its PRs are never queried — a
// healthy project (one live worker per issue) triggers no PR lookups.
func TestPollLiveSessionClaimsWithoutPRLookup(t *testing.T) {
	store := &fakeStore{
		projects: []domain.ProjectRecord{intakeProject()},
		sessions: []domain.SessionRecord{{ID: "demo-1", ProjectID: "demo", IssueID: "github:acme/demo#12"}},
		// prsErr would blow up if the live session were queried; it must not be.
		prsErr: map[domain.SessionID]error{"demo-1": errors.New("PRs must not be queried for a live session")},
	}
	spawner := &fakeSpawner{}

	if err := New(singleResolver(openIssue12()), store, spawner, Config{Logger: discardLogger()}).Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("spawn calls = %+v, want 0: a live session already claims the issue", spawner.calls)
	}
}
