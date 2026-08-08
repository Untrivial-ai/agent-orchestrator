package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestInsertReviewRunDuplicatePRSHAMapsToSentinel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerClaudeCode, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	run := domain.ReviewRun{
		ID: "run-1", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerClaudeCode,
		PRURL: "https://example/pr/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone, CreatedAt: now,
	}
	if err := s.InsertReviewRun(ctx, run); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// A second run for the same (session_id, pr_url, target_sha, harness) hits the
	// partial unique index (migration 0041) and must surface as the sentinel so
	// the engine can fall back to the existing run.
	dup := run
	dup.ID = "run-2"
	if err := s.InsertReviewRun(ctx, dup); !errors.Is(err, domain.ErrDuplicateReviewRun) {
		t.Fatalf("duplicate insert err = %v, want ErrDuplicateReviewRun", err)
	}

	otherPR := run
	otherPR.ID = "run-other-pr"
	otherPR.PRURL = "https://example/pr/2"
	if err := s.InsertReviewRun(ctx, otherPR); err != nil {
		t.Fatalf("same sha on different PR should insert: %v", err)
	}

	if ok, err := s.UpdateReviewRunResult(ctx, "run-1", domain.ReviewRunFailed, domain.VerdictNone, "claude: not found", "", true); err != nil {
		t.Fatalf("mark failed: %v", err)
	} else if !ok {
		t.Fatal("mark failed: got ok=false")
	}
	if err := s.InsertReviewRun(ctx, dup); err != nil {
		t.Fatalf("retry after failed insert: %v", err)
	}

	// An empty target_sha is excluded from the index, so two are allowed.
	for _, id := range []string{"run-empty-1", "run-empty-2"} {
		r := run
		r.ID, r.TargetSHA = id, ""
		if err := s.InsertReviewRun(ctx, r); err != nil {
			t.Fatalf("empty-sha insert %s: %v", id, err)
		}
	}
}

// Harness is part of the idempotency key, so a different reviewer on the same
// commit is a second opinion rather than a duplicate. Without this the reviewer
// picker is inert on an already-reviewed commit.
func TestInsertReviewRunAllowsADifferentHarnessForTheSameCommit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerClaudeCode, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	first := domain.ReviewRun{
		ID: "run-1", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerClaudeCode,
		PRURL: "https://example/pr/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone, CreatedAt: now,
	}
	if err := s.InsertReviewRun(ctx, first); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	other := first
	other.ID = "run-other-harness"
	other.Harness = domain.ReviewerCodex
	if err := s.InsertReviewRun(ctx, other); err != nil {
		t.Fatalf("a different harness on the same commit should insert: %v", err)
	}

	// ...but the same harness twice is still a duplicate.
	same := first
	same.ID = "run-same-harness"
	if err := s.InsertReviewRun(ctx, same); !errors.Is(err, domain.ErrDuplicateReviewRun) {
		t.Fatalf("same harness duplicate err = %v, want ErrDuplicateReviewRun", err)
	}
}

func TestInsertReviewRunAllowsRerunAfterChangesRequested(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerClaudeCode, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	run := domain.ReviewRun{
		ID: "run-1", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerClaudeCode,
		PRURL: "https://example/pr/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone, CreatedAt: now,
	}
	if err := s.InsertReviewRun(ctx, run); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if ok, err := s.UpdateReviewRunResult(ctx, "run-1", domain.ReviewRunComplete, domain.VerdictChangesRequested, "please fix", "rev-1", true); err != nil {
		t.Fatalf("mark changes requested: %v", err)
	} else if !ok {
		t.Fatal("mark changes requested: got ok=false")
	}

	rerun := run
	rerun.ID = "run-2"
	rerun.CreatedAt = now.Add(time.Second)
	if err := s.InsertReviewRun(ctx, rerun); err != nil {
		t.Fatalf("rerun after changes_requested insert: %v", err)
	}
}

func TestInsertReviewRunAllowsRerunAfterTerminalEmptyVerdict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerClaudeCode, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	run := domain.ReviewRun{
		ID: "run-1", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerClaudeCode,
		PRURL: "https://example/pr/1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictNone, CreatedAt: now,
	}
	if err := s.InsertReviewRun(ctx, run); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	rerun := run
	rerun.ID = "run-2"
	rerun.Status = domain.ReviewRunRunning
	rerun.CreatedAt = now.Add(time.Second)
	if err := s.InsertReviewRun(ctx, rerun); err != nil {
		t.Fatalf("rerun after terminal empty-verdict insert: %v", err)
	}
}

func TestReviewUpsertReusesRowAndRunRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)

	// First upsert creates the review row.
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerClaudeCode, PRURL: "https://example/pr/1",
		ReviewerHandleID: "review-mer-1",
		CreatedAt:        now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	// A different harness gets its own row for the same worker session.
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-2", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerHarness("greptile"), PRURL: "https://example/pr/2",
		ReviewerHandleID: "review-mer-1b",
		CreatedAt:        now, UpdatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("upsert review (second harness): %v", err)
	}
	got, ok, err := s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerClaudeCode)
	if err != nil || !ok {
		t.Fatalf("get claude review: ok=%v err=%v", ok, err)
	}
	if got.ID != "rev-1" {
		t.Fatalf("claude review id = %q, want rev-1", got.ID)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get greptile review: ok=%v err=%v", ok, err)
	}
	if got.ID != "rev-2" {
		t.Fatalf("greptile review id = %q, want rev-2", got.ID)
	}
	if got.Harness != domain.ReviewerHarness("greptile") || got.PRURL != "https://example/pr/2" || got.ReviewerHandleID != "review-mer-1b" {
		t.Fatalf("second harness fields: %+v", got)
	}
	// A same-harness upsert reuses that row and records the native session id.
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-3", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerHarness("greptile"), PRURL: "https://example/pr/2",
		ReviewerHandleID: "review-mer-1b",
		AgentSessionID:   "reviewer-native-1",
		CreatedAt:        now, UpdatedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("upsert review (agent session): %v", err)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get review after agent session update: ok=%v err=%v", ok, err)
	}
	if got.ID != "rev-2" {
		t.Fatalf("same-harness upsert changed row id = %q, want rev-2", got.ID)
	}
	if got.AgentSessionID != "reviewer-native-1" {
		t.Fatalf("agent session id = %q, want reviewer-native-1", got.AgentSessionID)
	}
	updated, err := s.UpdateReviewAgentSessionID(ctx, "rev-1", "claude-native-1")
	if err != nil || !updated {
		t.Fatalf("update claude native session: updated=%v err=%v", updated, err)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerClaudeCode)
	if err != nil || !ok {
		t.Fatalf("get claude review after native update: ok=%v err=%v", ok, err)
	}
	if got.AgentSessionID != "claude-native-1" {
		t.Fatalf("claude agent session id = %q, want claude-native-1", got.AgentSessionID)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get greptile review after claude update: ok=%v err=%v", ok, err)
	}
	if got.AgentSessionID != "reviewer-native-1" {
		t.Fatalf("greptile agent session id = %q, want reviewer-native-1", got.AgentSessionID)
	}
	if err := s.ClearReviewerHandle(ctx, rec.ID); err != nil {
		t.Fatalf("clear reviewer handle: %v", err)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get review after clear: ok=%v err=%v", ok, err)
	}
	if got.ReviewerHandleID != "" {
		t.Fatalf("reviewer handle after clear = %q, want empty", got.ReviewerHandleID)
	}

	// A run inserts running and updates to complete/changes_requested.
	if err := s.InsertReviewRun(ctx, domain.ReviewRun{
		ID: "run-1", ReviewID: got.ID, SessionID: rec.ID, BatchID: "batch-1", Harness: domain.ReviewerHarness("greptile"),
		PRURL: got.PRURL, TargetSHA: "sha1", Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if ok, err := s.UpdateReviewRunResult(ctx, "run-1", domain.ReviewRunComplete, domain.VerdictChangesRequested, "please fix", "rev-987", false); err != nil {
		t.Fatalf("update run: %v", err)
	} else if !ok {
		t.Fatal("update run: got ok=false")
	}

	gotRun, ok, err := s.GetReviewRun(ctx, "run-1")
	if err != nil || !ok {
		t.Fatalf("get run: ok=%v err=%v", ok, err)
	}
	if gotRun.ID != "run-1" || gotRun.SessionID != rec.ID || gotRun.BatchID != "batch-1" || gotRun.TargetSHA != "sha1" {
		t.Fatalf("get run = %+v", gotRun)
	}

	bySHA, ok, err := s.GetReviewRunBySessionPRAndSHA(ctx, rec.ID, got.PRURL, "sha1")
	if err != nil || !ok {
		t.Fatalf("by sha: ok=%v err=%v", ok, err)
	}
	if bySHA.Status != domain.ReviewRunComplete || bySHA.Verdict != domain.VerdictChangesRequested || bySHA.Body != "please fix" || bySHA.GithubReviewID != "rev-987" {
		t.Fatalf("run result not persisted: %+v", bySHA)
	}
	byHarness, ok, err := s.GetReviewRunBySessionPRSHAAndHarness(ctx, rec.ID, got.PRURL, "sha1", domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("by harness: ok=%v err=%v", ok, err)
	}
	if byHarness.ID != "run-1" {
		t.Fatalf("by harness = %+v, want run-1", byHarness)
	}
	if _, ok, _ := s.GetReviewRunBySessionPRSHAAndHarness(ctx, rec.ID, got.PRURL, "sha1", domain.ReviewerCodex); ok {
		t.Fatal("unexpected run for a different harness")
	}
	if _, ok, _ := s.GetReviewRunBySessionPRAndSHA(ctx, rec.ID, got.PRURL, "other"); ok {
		t.Fatal("unexpected run for a different sha")
	}

	runs, err := s.ListReviewRunsBySession(ctx, rec.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-1" || runs[0].AutoInjectReview {
		t.Fatalf("list runs = %+v", runs)
	}
	batchRuns, err := s.ListReviewRunsByBatch(ctx, rec.ID, "batch-1")
	if err != nil {
		t.Fatalf("list batch runs: %v", err)
	}
	if len(batchRuns) != 1 || batchRuns[0].ID != "run-1" || batchRuns[0].BatchID != "batch-1" {
		t.Fatalf("batch runs = %+v", batchRuns)
	}

	if ok, err := s.UpdateReviewRunResult(ctx, "run-1", domain.ReviewRunComplete, domain.VerdictApproved, "again", "", true); err != nil {
		t.Fatalf("second update: %v", err)
	} else if ok {
		t.Fatal("second update completed an already-complete run")
	}
}

func TestCancelRunningReviewRunsBySession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	for _, run := range []domain.ReviewRun{
		{ID: "run-1", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerCodex, PRURL: "https://example/pr/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning, CreatedAt: now},
		{ID: "run-2", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerCodex, PRURL: "https://example/pr/2", TargetSHA: "sha2", Status: domain.ReviewRunRunning, CreatedAt: now.Add(time.Second)},
		{ID: "run-3", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerCodex, PRURL: "https://example/pr/3", TargetSHA: "sha3", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved, CreatedAt: now.Add(2 * time.Second)},
	} {
		if err := s.InsertReviewRun(ctx, run); err != nil {
			t.Fatalf("insert %s: %v", run.ID, err)
		}
	}
	running, err := s.ListRunningReviewRunsBySession(ctx, rec.ID)
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if len(running) != 2 || running[0].ID != "run-2" || running[1].ID != "run-1" {
		t.Fatalf("running = %+v", running)
	}
	n, err := s.CancelRunningReviewRunsBySession(ctx, rec.ID, "cancelled by user")
	if err != nil {
		t.Fatalf("cancel running: %v", err)
	}
	if n != 2 {
		t.Fatalf("cancelled rows = %d, want 2", n)
	}
	runs, err := s.ListReviewRunsBySession(ctx, rec.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	byID := map[string]domain.ReviewRun{}
	for _, run := range runs {
		byID[run.ID] = run
	}
	if byID["run-1"].Status != domain.ReviewRunCancelled || byID["run-2"].Status != domain.ReviewRunCancelled {
		t.Fatalf("running runs not cancelled: %+v", byID)
	}
	if byID["run-3"].Status != domain.ReviewRunComplete {
		t.Fatalf("complete run changed: %+v", byID["run-3"])
	}
}

func TestReviewGettersMissing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, ok, err := s.GetReviewBySession(ctx, "mer-1"); err != nil || ok {
		t.Fatalf("missing review: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetReviewRunBySessionPRAndSHA(ctx, "mer-1", "pr1", "sha1"); err != nil || ok {
		t.Fatalf("missing run: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetReviewRun(ctx, "run-missing"); err != nil || ok {
		t.Fatalf("missing run by id: ok=%v err=%v", ok, err)
	}
}
