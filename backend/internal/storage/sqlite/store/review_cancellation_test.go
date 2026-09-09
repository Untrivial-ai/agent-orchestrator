package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestCancelReviewRunsAndClearHandleIsAtomicAndScoped(t *testing.T) {
	for _, harness := range []domain.ReviewerHarness{domain.ReviewerCodex, ""} {
		name := string(harness)
		if name == "" {
			name = "all"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dataDir := t.TempDir()
			s := sqlitetest.MustOpenAt(t, dataDir)
			seedProject(t, s, "mer")
			session, err := s.CreateSession(ctx, sampleRecord("mer"))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			reviews := []domain.Review{
				{ID: "selected", SessionID: session.ID, ProjectID: session.ProjectID, Harness: domain.ReviewerCodex, ReviewerHandleID: "selected-pane", AgentSessionID: "selected-history", CreatedAt: now, UpdatedAt: now},
				{ID: "other", SessionID: session.ID, ProjectID: session.ProjectID, Harness: domain.ReviewerOpenCode, ReviewerHandleID: "other-pane", AgentSessionID: "other-history", CreatedAt: now, UpdatedAt: now},
			}
			for _, review := range reviews {
				if err := s.UpsertReview(ctx, review); err != nil {
					t.Fatal(err)
				}
				for _, status := range []domain.ReviewRunStatus{domain.ReviewRunRunning, domain.ReviewRunComplete} {
					run := domain.ReviewRun{
						ID: review.ID + "-" + string(status), ReviewID: review.ID, SessionID: session.ID, Harness: review.Harness,
						PRURL: "https://example.test/pr/1", TargetSHA: "head", Status: status, CreatedAt: now,
					}
					if status == domain.ReviewRunComplete {
						run.TargetSHA = "previous-head"
						run.Verdict, run.Body, run.GithubReviewID = domain.VerdictApproved, "completed result", "123"
					}
					if err := s.InsertReviewRun(ctx, run); err != nil {
						t.Fatal(err)
					}
				}
			}
			db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			// The second mutation fails after the transaction has cleared the
			// handle. Both mutations must roll back to retain a teardown retry.
			if _, err := db.Exec(`CREATE TRIGGER reject_review_cancellation
BEFORE UPDATE ON review_run WHEN NEW.status = 'cancelled'
BEGIN SELECT RAISE(ABORT, 'injected cancellation failure'); END`); err != nil {
				t.Fatal(err)
			}
			if n, err := s.CancelReviewRunsAndClearHandle(ctx, session.ID, harness, "cancelled by user"); err == nil || n != 0 {
				t.Fatalf("failed transaction = %d, %v", n, err)
			}
			for _, review := range reviews {
				got, ok, err := s.GetReviewBySessionAndHarness(ctx, session.ID, review.Harness)
				if err != nil || !ok || got.ReviewerHandleID != review.ReviewerHandleID {
					t.Fatalf("rollback lost reviewer identity: review=%+v ok=%v err=%v", got, ok, err)
				}
				run, ok, err := s.GetReviewRun(ctx, review.ID+"-running")
				if err != nil || !ok || run.Status != domain.ReviewRunRunning {
					t.Fatalf("rollback changed running review: run=%+v ok=%v err=%v", run, ok, err)
				}
			}
			if _, err := db.Exec(`DROP TRIGGER reject_review_cancellation`); err != nil {
				t.Fatal(err)
			}
			wantCancelled := int64(1)
			if harness == "" {
				wantCancelled = 2
			}
			if n, err := s.CancelReviewRunsAndClearHandle(ctx, session.ID, harness, "cancelled by user"); err != nil || n != wantCancelled {
				t.Fatalf("retry transaction = %d, %v, want %d cancelled", n, err, wantCancelled)
			}
			for _, review := range reviews {
				selected := harness == "" || review.Harness == harness
				wantHandle := review.ReviewerHandleID
				wantStatus := domain.ReviewRunRunning
				if selected {
					wantHandle, wantStatus = "", domain.ReviewRunCancelled
				}
				got, ok, err := s.GetReviewBySessionAndHarness(ctx, session.ID, review.Harness)
				if err != nil || !ok || got.ReviewerHandleID != wantHandle || got.AgentSessionID != review.AgentSessionID {
					t.Fatalf("review after cancellation = %+v, ok=%v err=%v", got, ok, err)
				}
				run, ok, err := s.GetReviewRun(ctx, review.ID+"-running")
				if err != nil || !ok || run.Status != wantStatus {
					t.Fatalf("run after cancellation = %+v, ok=%v err=%v", run, ok, err)
				}
				if selected {
					updated, err := s.UpdateReviewRunResult(ctx, run.ID, domain.ReviewRunComplete, domain.VerdictApproved, "late result", "456", true)
					if err != nil || updated {
						t.Fatalf("late result overwrote finalized cancellation: updated=%v err=%v", updated, err)
					}
				}
				history, ok, err := s.GetReviewRun(ctx, review.ID+"-complete")
				if err != nil || !ok || history.Status != domain.ReviewRunComplete || history.Verdict != domain.VerdictApproved || history.Body != "completed result" || history.GithubReviewID != "123" {
					t.Fatalf("completed history changed: run=%+v ok=%v err=%v", history, ok, err)
				}
			}
		})
	}
}
