package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ListPRFactsForSession is the real-SQLite batch read the multi-PR status
// aggregator builds stacks from: every owned PR returned newest-first with its
// state flags and branch pair projected (the stack model needs both).
//
// The branch pair is written via WriteSCMObservation (the observer path, the
// source of truth for tracked PRs). The other writer, WritePR, deliberately
// omits source/target branch (UpsertLegacyPR), so the stack model depends on the
// observer having populated the row.
func TestListPRFactsForSessionProjectsAllPRsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	// A stack: root (open) -> child targets the root branch (open) -> a merged
	// historical PR. Distinct updated_at so newest-first ordering is observable.
	write := func(pr domain.PullRequest) {
		t.Helper()
		if err := s.WriteSCMObservation(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
			t.Fatalf("write %s: %v", pr.URL, err)
		}
	}
	write(domain.PullRequest{URL: "root", SessionID: r.ID, Number: 1, CI: domain.CIPassing, SourceBranch: "feat/x", TargetBranch: "main", UpdatedAt: now, ObservedAt: now})
	write(domain.PullRequest{URL: "child", SessionID: r.ID, Number: 2, Draft: true, SourceBranch: "feat/x/child", TargetBranch: "feat/x", UpdatedAt: now.Add(time.Second), ObservedAt: now})
	write(domain.PullRequest{URL: "old", SessionID: r.ID, Number: 3, Merged: true, SourceBranch: "feat/old", TargetBranch: "main", UpdatedAt: now.Add(2 * time.Second), ObservedAt: now})

	facts, err := s.ListPRFactsForSession(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 3 {
		t.Fatalf("ListPRFactsForSession = %d, want 3", len(facts))
	}
	// Newest-first by updated_at: old, child, root.
	if facts[0].URL != "old" || facts[1].URL != "child" || facts[2].URL != "root" {
		t.Fatalf("order = [%s %s %s], want [old child root]", facts[0].URL, facts[1].URL, facts[2].URL)
	}
	byURL := map[string]domain.PRFacts{}
	for _, f := range facts {
		byURL[f.URL] = f
	}
	if !byURL["old"].Merged || byURL["old"].Closed || byURL["old"].Draft {
		t.Fatalf("merged PR flags wrong: %+v", byURL["old"])
	}
	if !byURL["child"].Draft || byURL["child"].Merged {
		t.Fatalf("draft child flags wrong: %+v", byURL["child"])
	}
	// The stack model is derived from the source/target branch pair, so it must
	// survive the projection.
	if byURL["child"].SourceBranch != "feat/x/child" || byURL["child"].TargetBranch != "feat/x" {
		t.Fatalf("child branch pair lost: %+v", byURL["child"])
	}
	if byURL["root"].SourceBranch != "feat/x" || byURL["root"].TargetBranch != "main" {
		t.Fatalf("root branch pair lost: %+v", byURL["root"])
	}
	if byURL["root"].CI != domain.CIPassing {
		t.Fatalf("root CI = %q, want passing", byURL["root"].CI)
	}

	// A session with no PRs returns an empty (non-nil) slice, never an error.
	empty, _ := s.CreateSession(ctx, sampleRecord("mer"))
	got, err := s.ListPRFactsForSession(ctx, empty.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("no-PR session = %d facts, want 0", len(got))
	}
}

func TestPRStateChangedAtPersistsOnlyOnStateTransitions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	createdAt := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 6, 4, 9, 30, 0, 0, time.UTC)
	readyAt := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	laterAt := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC)
	pr := domain.PullRequest{
		URL:               "https://github.com/acme/repo/pull/7",
		SessionID:         r.ID,
		Number:            7,
		Draft:             true,
		CreatedAtProvider: createdAt,
		UpdatedAtProvider: updatedAt,
		UpdatedAt:         updatedAt,
		ObservedAt:        updatedAt,
	}
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetPR(ctx, pr.URL)
	if err != nil || !ok {
		t.Fatalf("GetPR after draft write: ok=%v err=%v", ok, err)
	}
	if !got.StateChangedAt.Equal(createdAt) {
		t.Fatalf("draft stateChangedAt = %s, want created time %s", got.StateChangedAt, createdAt)
	}

	pr.Title = "metadata-only update"
	pr.UpdatedAtProvider = laterAt
	pr.UpdatedAt = laterAt
	pr.ObservedAt = laterAt
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.GetPR(ctx, pr.URL)
	if err != nil || !ok {
		t.Fatalf("GetPR after same-state write: ok=%v err=%v", ok, err)
	}
	if !got.StateChangedAt.Equal(createdAt) {
		t.Fatalf("same-state stateChangedAt = %s, want preserved %s", got.StateChangedAt, createdAt)
	}

	pr.Draft = false
	pr.UpdatedAtProvider = readyAt
	pr.UpdatedAt = readyAt
	pr.ObservedAt = readyAt
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.GetPR(ctx, pr.URL)
	if err != nil || !ok {
		t.Fatalf("GetPR after ready write: ok=%v err=%v", ok, err)
	}
	if !got.StateChangedAt.Equal(readyAt) {
		t.Fatalf("ready stateChangedAt = %s, want ready time %s", got.StateChangedAt, readyAt)
	}
}

func TestPRStateChangedAtFillsWhenProviderCreatedAtArrives(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	discoveredAt := time.Date(2026, 6, 4, 9, 30, 0, 0, time.UTC)
	createdAt := time.Date(2026, 6, 4, 8, 45, 0, 0, time.UTC)
	laterAt := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	pr := domain.PullRequest{
		URL:        "https://github.com/acme/repo/pull/8",
		SessionID:  r.ID,
		Number:     8,
		UpdatedAt:  discoveredAt,
		ObservedAt: discoveredAt,
	}
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetPR(ctx, pr.URL)
	if err != nil || !ok {
		t.Fatalf("GetPR after discovery write: ok=%v err=%v", ok, err)
	}
	if !got.StateChangedAt.IsZero() {
		t.Fatalf("discovery stateChangedAt = %s, want unset without provider creation time", got.StateChangedAt)
	}

	pr.CreatedAtProvider = createdAt
	pr.UpdatedAt = laterAt
	pr.ObservedAt = laterAt
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.GetPR(ctx, pr.URL)
	if err != nil || !ok {
		t.Fatalf("GetPR after provider-created write: ok=%v err=%v", ok, err)
	}
	if !got.StateChangedAt.Equal(createdAt) {
		t.Fatalf("same-state stateChangedAt = %s, want provider creation time %s", got.StateChangedAt, createdAt)
	}
}

func TestListPRFactsForSessionsBatchesBySession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	first, _ := s.CreateSession(ctx, sampleRecord("mer"))
	second, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	write := func(sessionID domain.SessionID, url string, updatedAt time.Time) {
		t.Helper()
		if err := s.WriteSCMObservation(ctx, domain.PullRequest{
			URL: url, SessionID: sessionID, Number: 1, HeadSHA: "head", UpdatedAt: updatedAt, ObservedAt: updatedAt,
		}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
			t.Fatalf("write %s: %v", url, err)
		}
	}
	write(first.ID, "pr/1a", now)
	write(first.ID, "pr/1b", now.Add(time.Second))
	write(second.ID, "pr/2a", now.Add(2*time.Second))

	got, err := s.ListPRFactsForSessions(ctx, []domain.SessionID{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[first.ID]) != 2 || len(got[second.ID]) != 1 {
		t.Fatalf("batched facts = %+v", got)
	}
	if got[first.ID][0].URL != "pr/1b" || got[first.ID][1].URL != "pr/1a" {
		t.Fatalf("first session order = %+v, want newest first", got[first.ID])
	}
	if got[second.ID][0].URL != "pr/2a" {
		t.Fatalf("second session facts = %+v", got[second.ID])
	}
}
