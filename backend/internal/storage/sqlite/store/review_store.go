package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// UpsertReview inserts the per-worker, per-harness review row, or reuses the
// existing one by refreshing its handle/native-session state.
func (s *Store) UpsertReview(ctx context.Context, r domain.Review) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.UpsertReview(ctx, gen.UpsertReviewParams{
		ID:               r.ID,
		SessionID:        r.SessionID,
		ProjectID:        r.ProjectID,
		Harness:          r.Harness,
		PRURL:            r.PRURL,
		ReviewerHandleID: r.ReviewerHandleID,
		AgentSessionID:   r.AgentSessionID,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	})
}

// GetReviewBySession returns the latest review row for a worker session,
// ok=false if none.
func (s *Store) GetReviewBySession(ctx context.Context, id domain.SessionID) (domain.Review, bool, error) {
	row, err := s.qr.GetReviewBySession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Review{}, false, nil
	}
	if err != nil {
		return domain.Review{}, false, fmt.Errorf("get review by session %s: %w", id, err)
	}
	return reviewFromGetReviewBySessionRow(row), true, nil
}

// GetReviewBySessionAndHarness returns the review row for one reviewer harness
// on a worker session, ok=false if none.
func (s *Store) GetReviewBySessionAndHarness(ctx context.Context, id domain.SessionID, harness domain.ReviewerHarness) (domain.Review, bool, error) {
	row, err := s.qr.GetReviewBySessionAndHarness(ctx, gen.GetReviewBySessionAndHarnessParams{SessionID: id, Harness: harness})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Review{}, false, nil
	}
	if err != nil {
		return domain.Review{}, false, fmt.Errorf("get review by session %s harness %s: %w", id, harness, err)
	}
	return reviewFromGetReviewBySessionAndHarnessRow(row), true, nil
}

// GetReviewByID returns one reviewer session row by its stable review id.
func (s *Store) GetReviewByID(ctx context.Context, id string) (domain.Review, bool, error) {
	row, err := s.qr.GetReviewByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Review{}, false, nil
	}
	if err != nil {
		return domain.Review{}, false, fmt.Errorf("get review %s: %w", id, err)
	}
	return reviewFromReview(row), true, nil
}

// ListReviewsBySession returns every harness-specific review row for a worker
// session, newest first.
func (s *Store) ListReviewsBySession(ctx context.Context, id domain.SessionID) ([]domain.Review, error) {
	rows, err := s.qr.ListReviewsBySession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list reviews for session %s: %w", id, err)
	}
	out := make([]domain.Review, 0, len(rows))
	for _, row := range rows {
		out = append(out, reviewFromListReviewsBySessionRow(row))
	}
	return out, nil
}

// ClearReviewerHandle removes all persisted terminal handles for a worker
// after a hard reviewer pane teardown.
func (s *Store) ClearReviewerHandle(ctx context.Context, id domain.SessionID) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.ClearReviewerHandle(ctx, id)
}

// ClearReviewerHandleByHarness removes only the runtime handle for one
// reviewer harness, preserving its native agent session id for later restore.
func (s *Store) ClearReviewerHandleByHarness(ctx context.Context, id domain.SessionID, harness domain.ReviewerHarness) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.ClearReviewerHandleByHarness(ctx, gen.ClearReviewerHandleByHarnessParams{SessionID: id, Harness: harness})
}

// UpdateReviewAgentSessionID records the native reviewer conversation id
// reported by the reviewer harness hooks.
func (s *Store) UpdateReviewAgentSessionID(ctx context.Context, id, agentSessionID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.UpdateReviewAgentSessionID(ctx, gen.UpdateReviewAgentSessionIDParams{ID: id, AgentSessionID: agentSessionID})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// InsertReviewRun records a new review pass. A unique-constraint hit on the
// (session_id, pr_url, target_sha) index (migration 0020) is surfaced as the sentinel
// domain.ErrDuplicateReviewRun so the engine can fall back to the existing run.
func (s *Store) InsertReviewRun(ctx context.Context, r domain.ReviewRun) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if r.TriggerSource == "" {
		r.TriggerSource = domain.ReviewTriggerManual
	}
	err := s.qw.InsertReviewRun(ctx, gen.InsertReviewRunParams{
		ID:               r.ID,
		ReviewID:         r.ReviewID,
		SessionID:        r.SessionID,
		BatchID:          r.BatchID,
		Harness:          r.Harness,
		TriggerSource:    r.TriggerSource,
		PRURL:            r.PRURL,
		TargetSha:        r.TargetSHA,
		Status:           r.Status,
		Verdict:          r.Verdict,
		Body:             r.Body,
		GithubReviewID:   r.GithubReviewID,
		CreatedAt:        r.CreatedAt,
		AutoInjectReview: r.AutoInjectReview,
	})
	if isSQLiteUnique(err) {
		return fmt.Errorf("insert review run for session %s pr %s sha %s: %w", r.SessionID, r.PRURL, r.TargetSHA, domain.ErrDuplicateReviewRun)
	}
	return err
}

// UpdateReviewRunResult sets the status/verdict/body and the GitHub review id of
// a running review pass.
func (s *Store) UpdateReviewRunResult(ctx context.Context, id string, status domain.ReviewRunStatus, verdict domain.ReviewVerdict, body, githubReviewID string, autoInjectReview bool) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.UpdateReviewRunResult(ctx, gen.UpdateReviewRunResultParams{
		Status:           status,
		Verdict:          verdict,
		Body:             body,
		GithubReviewID:   githubReviewID,
		AutoInjectReview: autoInjectReview,
		ID:               id,
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SupersedeStaleRunningReviewRuns marks older running unverdicted passes for a
// worker failed before starting a review for a newer commit.
func (s *Store) SupersedeStaleRunningReviewRuns(ctx context.Context, sessionID domain.SessionID, prURL, targetSHA, body string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.SupersedeStaleRunningReviewRuns(ctx, gen.SupersedeStaleRunningReviewRunsParams{
		Body:      body,
		SessionID: sessionID,
		PRURL:     prURL,
		TargetSha: targetSHA,
	})
}

// CancelRunningReviewRunsBySession marks all currently running review passes
// for a worker cancelled.
func (s *Store) CancelRunningReviewRunsBySession(ctx context.Context, sessionID domain.SessionID, body string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.CancelRunningReviewRunsBySession(ctx, gen.CancelRunningReviewRunsBySessionParams{
		Body:      body,
		SessionID: sessionID,
	})
}

// CancelRunningReviewRunsBySessionAndHarness marks currently running review
// passes for a single reviewer harness as cancelled.
func (s *Store) CancelRunningReviewRunsBySessionAndHarness(ctx context.Context, sessionID domain.SessionID, harness domain.ReviewerHarness, body string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.CancelRunningReviewRunsBySessionAndHarness(ctx, gen.CancelRunningReviewRunsBySessionAndHarnessParams{
		Body:      body,
		SessionID: sessionID,
		Harness:   harness,
	})
}

// MarkReviewRunDelivered records that lifecycle delivered the worker nudge for
// a completed AO-internal review pass.
func (s *Store) MarkReviewRunDelivered(ctx context.Context, id string, deliveredAt time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.MarkReviewRunDelivered(ctx, gen.MarkReviewRunDeliveredParams{
		DeliveredAt: sql.NullTime{Time: deliveredAt, Valid: true},
		ID:          id,
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetReviewRun returns one review pass by id.
func (s *Store) GetReviewRun(ctx context.Context, id string) (domain.ReviewRun, bool, error) {
	row, err := s.qr.GetReviewRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReviewRun{}, false, nil
	}
	if err != nil {
		return domain.ReviewRun{}, false, fmt.Errorf("get review run %s: %w", id, err)
	}
	return reviewRunFromRow(row), true, nil
}

// GetReviewRunBySessionPRAndSHA returns the most recent review pass for a
// worker session, PR, and commit, ok=false if none. It lets a repeat trigger for
// the same PR head short-circuit to the existing run without colliding with
// another PR that happens to share the same head commit.
func (s *Store) GetReviewRunBySessionPRAndSHA(ctx context.Context, id domain.SessionID, prURL, targetSHA string) (domain.ReviewRun, bool, error) {
	row, err := s.qr.GetReviewRunBySessionPRAndSHA(ctx, gen.GetReviewRunBySessionPRAndSHAParams{SessionID: id, PRURL: prURL, TargetSha: targetSHA})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReviewRun{}, false, nil
	}
	if err != nil {
		return domain.ReviewRun{}, false, fmt.Errorf("get review run for session %s pr %s sha %s: %w", id, prURL, targetSHA, err)
	}
	return reviewRunFromRow(row), true, nil
}

// GetReviewRunBySessionPRSHAAndHarness returns the most recent review pass for
// a worker session, PR, commit, and reviewer harness, ok=false if none.
func (s *Store) GetReviewRunBySessionPRSHAAndHarness(ctx context.Context, id domain.SessionID, prURL, targetSHA string, harness domain.ReviewerHarness) (domain.ReviewRun, bool, error) {
	row, err := s.qr.GetReviewRunBySessionPRSHAAndHarness(ctx, gen.GetReviewRunBySessionPRSHAAndHarnessParams{SessionID: id, PRURL: prURL, TargetSha: targetSHA, Harness: harness})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReviewRun{}, false, nil
	}
	if err != nil {
		return domain.ReviewRun{}, false, fmt.Errorf("get review run for session %s pr %s sha %s harness %s: %w", id, prURL, targetSHA, harness, err)
	}
	return reviewRunFromRow(row), true, nil
}

// ListReviewRunsBySession returns all review passes for a worker session, newest first.
func (s *Store) ListReviewRunsBySession(ctx context.Context, id domain.SessionID) ([]domain.ReviewRun, error) {
	rows, err := s.qr.ListReviewRunsBySession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list review runs for session %s: %w", id, err)
	}
	out := make([]domain.ReviewRun, 0, len(rows))
	for _, row := range rows {
		out = append(out, reviewRunFromRow(row))
	}
	return out, nil
}

// ListRunningReviewRunsBySession returns only currently running unverdicted
// review passes for a worker session, newest first.
func (s *Store) ListRunningReviewRunsBySession(ctx context.Context, id domain.SessionID) ([]domain.ReviewRun, error) {
	rows, err := s.qr.ListRunningReviewRunsBySession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list running review runs for session %s: %w", id, err)
	}
	out := make([]domain.ReviewRun, 0, len(rows))
	for _, row := range rows {
		out = append(out, reviewRunFromRow(row))
	}
	return out, nil
}

// ListReviewRunsByBatch returns all passes in one trigger-created batch, oldest first.
func (s *Store) ListReviewRunsByBatch(ctx context.Context, id domain.SessionID, batchID string) ([]domain.ReviewRun, error) {
	rows, err := s.qr.ListReviewRunsByBatch(ctx, gen.ListReviewRunsByBatchParams{SessionID: id, BatchID: batchID})
	if err != nil {
		return nil, fmt.Errorf("list review runs for session %s batch %s: %w", id, batchID, err)
	}
	out := make([]domain.ReviewRun, 0, len(rows))
	for _, row := range rows {
		out = append(out, reviewRunFromRow(row))
	}
	return out, nil
}

func reviewFromGetReviewBySessionRow(r gen.Review) domain.Review {
	return reviewFromReview(r)
}

func reviewFromGetReviewBySessionAndHarnessRow(r gen.Review) domain.Review {
	return reviewFromReview(r)
}

func reviewFromListReviewsBySessionRow(r gen.Review) domain.Review {
	return reviewFromReview(r)
}

func reviewFromReview(r gen.Review) domain.Review {
	return domain.Review{
		ID:               r.ID,
		SessionID:        r.SessionID,
		ProjectID:        r.ProjectID,
		Harness:          r.Harness,
		PRURL:            r.PRURL,
		ReviewerHandleID: r.ReviewerHandleID,
		AgentSessionID:   r.AgentSessionID,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
}

func reviewRunFromRow(r gen.ReviewRun) domain.ReviewRun {
	var deliveredAt *time.Time
	if r.DeliveredAt.Valid {
		t := r.DeliveredAt.Time
		deliveredAt = &t
	}
	return domain.ReviewRun{
		ID:               r.ID,
		ReviewID:         r.ReviewID,
		SessionID:        r.SessionID,
		BatchID:          r.BatchID,
		Harness:          r.Harness,
		TriggerSource:    r.TriggerSource,
		PRURL:            r.PRURL,
		TargetSHA:        r.TargetSha,
		Status:           r.Status,
		Verdict:          r.Verdict,
		Body:             r.Body,
		GithubReviewID:   r.GithubReviewID,
		CreatedAt:        r.CreatedAt,
		DeliveredAt:      deliveredAt,
		AutoInjectReview: r.AutoInjectReview,
	}
}
