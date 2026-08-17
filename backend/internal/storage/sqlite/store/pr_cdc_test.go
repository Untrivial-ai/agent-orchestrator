package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// A check can change status on the same commit (in_progress -> failed) via
// UpsertPRCheck's ON CONFLICT DO UPDATE. CDC must emit on that transition, not
// only on the first insert — otherwise live clients never see the status change.
func TestPRChecksCDC_EmitsOnInsertAndStatusUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatal(err)
	}
	url := "https://example/pr/1"
	now := time.Now()
	mustCheck := func(status domain.PRCheckStatus) {
		if err := s.WritePR(ctx, domain.PullRequest{URL: url, SessionID: rec.ID, Number: 1, UpdatedAt: now}, []domain.PullRequestCheck{{Name: "build", CommitHash: "c1", Status: status, CreatedAt: now}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	mustCheck("in_progress") // insert  -> event
	mustCheck("failed")      // status change on same commit (update) -> event
	mustCheck("failed")      // no-op re-poll (status unchanged) -> NO event

	rows, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var checkEvents []cdc.Event
	for _, r := range rows {
		if r.Type == "pr_check_recorded" {
			checkEvents = append(checkEvents, r)
		}
	}
	if len(checkEvents) != 2 {
		t.Fatalf("want 2 check CDC events (insert + status change, no-op suppressed), got %d", len(checkEvents))
	}
	if !strings.Contains(string(checkEvents[1].Payload), `"status":"failed"`) {
		t.Fatalf("the update event should carry the new status, got %q", checkEvents[1].Payload)
	}
}

func TestPRReviewThreadsCDC_EmitsOnInsertAndResolvedTransition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	pr := domain.PullRequest{URL: "https://example/pr/9", SessionID: rec.ID, Number: 9, UpdatedAt: now}

	if err := s.WriteSCMObservation(ctx, pr, nil, nil, []domain.PullRequestReviewThread{{
		ThreadID: "t1", Path: "main.go", Line: 7, IsBot: true, SemanticHash: "v1", UpdatedAt: now,
	}}, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, []domain.PullRequestReviewThread{{
		ThreadID: "t1", Path: "main.go", Line: 8, Resolved: true, IsBot: true, SemanticHash: "v2", UpdatedAt: now.Add(time.Second),
	}}, nil, ports.ReviewWriteMerge); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, []domain.PullRequestReviewThread{{
		ThreadID: "t1", Path: "main.go", Line: 9, Resolved: true, IsBot: true, SemanticHash: "v3", UpdatedAt: now.Add(2 * time.Second),
	}}, nil, ports.ReviewWriteMerge); err != nil {
		t.Fatal(err)
	}

	rows, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var added, resolved []cdc.Event
	for _, r := range rows {
		switch r.Type {
		case cdc.EventPRReviewThreadAdded:
			added = append(added, r)
		case cdc.EventPRReviewThreadResolved:
			resolved = append(resolved, r)
		}
	}
	if len(added) != 1 {
		t.Fatalf("want 1 review-thread added CDC event, got %d", len(added))
	}
	if len(resolved) != 1 {
		t.Fatalf("want 1 review-thread resolved CDC event (resolved transition only), got %d", len(resolved))
	}

	var addPayload map[string]any
	if err := json.Unmarshal(added[0].Payload, &addPayload); err != nil {
		t.Fatalf("added payload JSON: %v", err)
	}
	if addPayload["thread"] != "t1" || addPayload["isBot"] != true || addPayload["resolved"] != false {
		t.Fatalf("added payload = %#v", addPayload)
	}
	var resolvedPayload map[string]any
	if err := json.Unmarshal(resolved[0].Payload, &resolvedPayload); err != nil {
		t.Fatalf("resolved payload JSON: %v", err)
	}
	if resolvedPayload["thread"] != "t1" || resolvedPayload["line"] != float64(8) || resolvedPayload["resolved"] != true {
		t.Fatalf("resolved payload = %#v", resolvedPayload)
	}
}

// Regression for the bug where pr_review_thread_resolved never fired on the
// common Replace path. Real-world polls take ReviewWriteReplace whenever the
// upstream listing is not paginated (provider observer sets Partial=false).
// The previous implementation did DELETE-all + UPSERT inside the tx, so every
// upsert hit the INSERT trigger and the UPDATE trigger that emits
// pr_review_thread_resolved never saw the resolved flip. The fix is a set-diff
// delete: upsert observed threads first (so unchanged thread_ids hit ON
// CONFLICT DO UPDATE and the UPDATE trigger fires), then prune the rows whose
// thread_id is no longer in the observed set.
func TestPRReviewThreadsCDC_EmitsResolvedOnReplacePoll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	pr := domain.PullRequest{URL: "https://example/pr/55", SessionID: rec.ID, Number: 55, UpdatedAt: now}

	// First poll: seed via Replace (no Partial pagination). Same shape as the
	// real GitHub provider path when the review-thread listing fits in one page.
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, []domain.PullRequestReviewThread{{
		ThreadID: "t1", Path: "main.go", Line: 7, IsBot: true, SemanticHash: "v1", UpdatedAt: now,
	}}, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}

	// Second poll: same thread, resolved flipped to true, also via Replace.
	// On the buggy code this fired the INSERT trigger again (because the
	// DELETE-all removed the row first) and the UPDATE trigger never saw the
	// resolved transition.
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, []domain.PullRequestReviewThread{{
		ThreadID: "t1", Path: "main.go", Line: 7, Resolved: true, IsBot: true, SemanticHash: "v2", UpdatedAt: now.Add(time.Second),
	}}, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}

	rows, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var added, resolved []cdc.Event
	for _, r := range rows {
		switch r.Type {
		case cdc.EventPRReviewThreadAdded:
			added = append(added, r)
		case cdc.EventPRReviewThreadResolved:
			resolved = append(resolved, r)
		}
	}
	if len(added) != 1 {
		t.Fatalf("want 1 review-thread added CDC event (initial Replace insert), got %d", len(added))
	}
	if len(resolved) != 1 {
		t.Fatalf("want 1 review-thread resolved CDC event on the second Replace poll, got %d", len(resolved))
	}
	var payload map[string]any
	if err := json.Unmarshal(resolved[0].Payload, &payload); err != nil {
		t.Fatalf("resolved payload JSON: %v", err)
	}
	if payload["thread"] != "t1" || payload["resolved"] != true {
		t.Fatalf("resolved payload = %#v", payload)
	}
}

// Pruning regression: Replace must still drop threads that are no longer in
// the observed listing, otherwise stale rows accumulate. Seed two threads,
// then re-poll with only one; the missing thread must be gone, while the
// surviving thread still gets an UPDATE (not a fresh INSERT).
func TestPRReviewThreadsReplace_PrunesOrphansWithoutReinserting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	pr := domain.PullRequest{URL: "https://example/pr/56", SessionID: rec.ID, Number: 56, UpdatedAt: now}

	if err := s.WriteSCMObservation(ctx, pr, nil, nil, []domain.PullRequestReviewThread{
		{ThreadID: "keep", Path: "a.go", Line: 1, IsBot: true, SemanticHash: "k1", UpdatedAt: now},
		{ThreadID: "drop", Path: "b.go", Line: 2, IsBot: true, SemanticHash: "d1", UpdatedAt: now},
	}, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, []domain.PullRequestReviewThread{
		{ThreadID: "keep", Path: "a.go", Line: 1, Resolved: true, IsBot: true, SemanticHash: "k2", UpdatedAt: now.Add(time.Second)},
	}, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListPRReviewThreads(ctx, pr.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ThreadID != "keep" || !got[0].Resolved {
		t.Fatalf("after prune want one resolved \"keep\" row, got %+v", got)
	}
	rows, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var added, resolved int
	for _, r := range rows {
		if r.Type == cdc.EventPRReviewThreadAdded {
			added++
		}
		if r.Type == cdc.EventPRReviewThreadResolved {
			resolved++
		}
	}
	// Two adds from poll 1 ("keep" and "drop"), no extra add on poll 2
	// (the surviving row went through ON CONFLICT DO UPDATE, not a fresh
	// INSERT), and one resolved transition from the kept row's flip.
	if added != 2 {
		t.Fatalf("want 2 added events across both polls, got %d", added)
	}
	if resolved != 1 {
		t.Fatalf("want 1 resolved event from the surviving thread's flip, got %d", resolved)
	}
}

// WritePR persists scalar facts, checks, and comments in one tx; all three
// should be queryable afterward.
func TestWritePR_PersistsScalarsChecksAndComments(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatal(err)
	}
	url := "https://example/pr/7"
	now := time.Now()

	err = s.WritePR(ctx,
		domain.PullRequest{URL: url, SessionID: rec.ID, Number: 7, CI: domain.CIFailing, UpdatedAt: now},
		[]domain.PullRequestCheck{{Name: "build", CommitHash: "c1", Status: "failed", CreatedAt: now}},
		[]domain.PullRequestComment{{ID: "1", Author: "reviewer", Body: "use a const", CreatedAt: now}},
	)
	if err != nil {
		t.Fatal(err)
	}

	pr, ok, err := s.GetPR(ctx, url)
	if err != nil || !ok || pr.CI != domain.CIFailing {
		t.Fatalf("scalar facts not persisted: ok=%v ci=%q err=%v", ok, pr.CI, err)
	}
	if checks, _ := s.ListChecks(ctx, url); len(checks) != 1 || checks[0].Status != "failed" {
		t.Fatalf("check not persisted: %+v", checks)
	}
	if comments, _ := s.ListPRComments(ctx, url); len(comments) != 1 || comments[0].Body != "use a const" {
		t.Fatalf("comment not persisted: %+v", comments)
	}
}

func TestClaimPR_CreatesMovesAndGuardsActiveOwner(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	first, _ := s.CreateSession(ctx, sampleRecord("mer"))
	second, _ := s.CreateSession(ctx, sampleRecord("mer"))
	url := "https://github.com/acme/repo/pull/42"
	pr := domain.PullRequest{URL: url, SessionID: first.ID, Number: 42, CI: domain.CIPassing, Mergeability: domain.MergeMergeable, UpdatedAt: time.Now().UTC()}

	out, err := s.ClaimPR(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve, true)
	if err != nil {
		t.Fatalf("initial claim: %v", err)
	}
	if out.PreviousOwner != "" {
		t.Fatalf("new claim previous owner = %q", out.PreviousOwner)
	}
	got, ok, err := s.GetPR(ctx, url)
	if err != nil || !ok || got.SessionID != first.ID || got.Number != 42 {
		t.Fatalf("claimed row = %+v ok=%v err=%v", got, ok, err)
	}

	pr.SessionID = second.ID
	if _, err := s.ClaimPR(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve, false); !errors.Is(err, ports.ErrPRClaimedByActiveSession) {
		t.Fatalf("no-takeover err = %v, want ErrPRClaimedByActiveSession", err)
	}
	got, _, _ = s.GetPR(ctx, url)
	if got.SessionID != first.ID {
		t.Fatalf("active-owner refusal moved row to %s", got.SessionID)
	}

	out, err = s.ClaimPR(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve, true)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if out.PreviousOwner != first.ID || out.OwnerTerminated {
		t.Fatalf("takeover outcome = %+v", out)
	}
	got, _, _ = s.GetPR(ctx, url)
	if got.SessionID != second.ID {
		t.Fatalf("takeover row owner = %s, want %s", got.SessionID, second.ID)
	}
}

func TestClaimPRSnapshotsDisabledSessionAutoInjectCI(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	record := sampleRecord("mer")
	record.AutoInjectCI = false
	owner, err := s.CreateSession(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	pr := domain.PullRequest{
		URL:       "https://github.com/acme/repo/pull/124",
		SessionID: owner.ID,
		Number:    124,
		CI:        domain.CIFailing,
		UpdatedAt: time.Now().UTC(),
	}

	if _, err := s.ClaimPR(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve, true); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetPR(ctx, pr.URL)
	if err != nil || !ok {
		t.Fatalf("get claimed PR: ok=%v err=%v", ok, err)
	}
	if got.AutoInjectCI {
		t.Fatal("claimed PR did not snapshot the session's disabled CI injection policy")
	}
}

func TestClaimPRCreatedCDCUsesClaimReviewDecision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/acme/repo/pull/123"
	pr := domain.PullRequest{
		URL:       url,
		SessionID: rec.ID,
		Number:    123,
		Review:    domain.ReviewChangesRequest,
		UpdatedAt: time.Now().UTC(),
	}
	if _, err := s.ClaimPR(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve, true); err != nil {
		t.Fatal(err)
	}

	events, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type != cdc.EventPRCreated {
			continue
		}
		if !strings.Contains(string(ev.Payload), `"review":"changes_requested"`) {
			t.Fatalf("pr_created payload review not from claim: %s", ev.Payload)
		}
		return
	}
	t.Fatalf("no pr_created event found; events=%v", events)
}

func TestClaimPR_TakesOverTerminatedOwnerAndEmitsSessionChangedCDC(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	first, _ := s.CreateSession(ctx, sampleRecord("mer"))
	second, _ := s.CreateSession(ctx, sampleRecord("mer"))
	url := "https://github.com/acme/repo/pull/99"
	pr := domain.PullRequest{URL: url, SessionID: first.ID, Number: 99, CI: domain.CIPassing, UpdatedAt: time.Now().UTC()}
	if _, err := s.ClaimPR(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve, true); err != nil {
		t.Fatal(err)
	}
	first.IsTerminated = true
	first.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	if err := s.UpdateSession(ctx, first); err != nil {
		t.Fatal(err)
	}

	pr.SessionID = second.ID
	out, err := s.ClaimPR(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve, false)
	if err != nil {
		t.Fatalf("terminated takeover: %v", err)
	}
	if out.PreviousOwner != first.ID || !out.OwnerTerminated {
		t.Fatalf("terminated outcome = %+v", out)
	}

	events, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var changed []cdc.Event
	for _, ev := range events {
		if ev.Type == "pr_session_changed" {
			changed = append(changed, ev)
		}
	}
	if len(changed) != 1 {
		t.Fatalf("pr_session_changed events = %d, want 1; all=%v", len(changed), events)
	}
	if changed[0].SessionID != string(second.ID) || !strings.Contains(string(changed[0].Payload), `"fromSession":"`+string(first.ID)+`"`) || !strings.Contains(string(changed[0].Payload), `"toSession":"`+string(second.ID)+`"`) {
		t.Fatalf("bad change event: %+v", changed[0])
	}
}
