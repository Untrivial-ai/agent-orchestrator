package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func testNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}
func testNullTime(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: value.UTC(), Valid: true}
}

// CreateTestRun stores an investigation independently of session status.
func (s *Store) CreateTestRun(ctx context.Context, r domain.TestRunRecord) error {
	if r.ID == "" || r.ProjectID == "" || r.CreatedAt.IsZero() {
		return fmt.Errorf("invalid test run")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.CreateTestRun(ctx, gen.CreateTestRunParams{ID: string(r.ID), LinkedRunID: testNullString(string(r.LinkedRunID)), ProjectID: string(r.ProjectID), IssueURL: r.IssueURL, IssueSnapshot: r.IssueSnapshot, CommitSha: r.CommitSHA, RecipeSnapshot: r.RecipeSnapshot, Requester: r.Requester, WorkerSessionID: testNullString(string(r.WorkerSessionID)), ReportEvidenceID: testNullString(r.ReportEvidenceID), CreatedAt: r.CreatedAt.UTC()})
}

// GetTestRun returns one durable investigation.
func (s *Store) GetTestRun(ctx context.Context, id domain.TestRunID) (domain.TestRunRecord, bool, error) {
	r, err := s.qr.GetTestRun(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TestRunRecord{}, false, nil
	}
	if err != nil {
		return domain.TestRunRecord{}, false, err
	}
	return domain.TestRunRecord{ID: domain.TestRunID(r.ID), LinkedRunID: domain.TestRunID(r.LinkedRunID.String), ProjectID: domain.ProjectID(r.ProjectID), IssueURL: r.IssueURL, IssueSnapshot: r.IssueSnapshot, CommitSHA: r.CommitSha, RecipeSnapshot: r.RecipeSnapshot, Requester: r.Requester, WorkerSessionID: domain.SessionID(r.WorkerSessionID.String), ReportEvidenceID: r.ReportEvidenceID.String, CreatedAt: r.CreatedAt}, true, nil
}
func testRows(n int64, err error) error {
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// SetTestRunWorker links the current visible investigator.
func (s *Store) SetTestRunWorker(ctx context.Context, id domain.TestRunID, session domain.SessionID) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.SetTestRunWorker(ctx, gen.SetTestRunWorkerParams{ID: string(id), WorkerSessionID: testNullString(string(session))})
	return testRows(n, err)
}

// SetTestRunReport links the saved investigator report receipt.
func (s *Store) SetTestRunReport(ctx context.Context, id domain.TestRunID, evidence string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.SetTestRunReport(ctx, gen.SetTestRunReportParams{ID: string(id), ReportEvidenceID: testNullString(evidence)})
	return testRows(n, err)
}

// CreateTestAttempt assigns the next attempt number and refuses a live overlap.
func (s *Store) CreateTestAttempt(ctx context.Context, r domain.TestAttemptRecord) (domain.TestAttemptRecord, error) {
	if r.ID == "" || r.RunID == "" || r.Deadline.IsZero() || r.CreatedAt.IsZero() {
		return domain.TestAttemptRecord{}, fmt.Errorf("invalid test attempt")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.CreateTestAttempt(ctx, gen.CreateTestAttemptParams{ID: string(r.ID), RunID: string(r.RunID), Deadline: r.Deadline.UTC(), RecordingGap: r.RecordingGap, CreatedAt: r.CreatedAt.UTC()})
	if err != nil {
		return domain.TestAttemptRecord{}, err
	}
	return testAttemptFromRow(row)
}
func testAttemptFromRow(r gen.TestAttempt) (domain.TestAttemptRecord, error) {
	out := domain.TestAttemptRecord{ID: domain.TestAttemptID(r.ID), RunID: domain.TestRunID(r.RunID), Number: r.Number, LeaseGeneration: r.LeaseGeneration, Phase: domain.TestAttemptPhase(r.Phase), Deadline: r.Deadline, Outcome: domain.TestOutcome(r.Outcome.String), CleanupState: domain.TestCleanupState(r.CleanupState), RecordingGap: r.RecordingGap, CancelledAt: nullTimeToTimePtr(r.CancelledAt), CreatedAt: r.CreatedAt, FinishedAt: nullTimeToTimePtr(r.FinishedAt)}
	if r.TargetIdentity.Valid {
		if err := json.Unmarshal([]byte(r.TargetIdentity.String), &out.Target); err != nil {
			return out, fmt.Errorf("decode test target: %w", err)
		}
	}
	return out, nil
}

// GetTestAttempt reads execution facts and the full target identity.
func (s *Store) GetTestAttempt(ctx context.Context, id domain.TestAttemptID) (domain.TestAttemptRecord, bool, error) {
	r, err := s.qr.GetTestAttempt(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TestAttemptRecord{}, false, nil
	}
	if err != nil {
		return domain.TestAttemptRecord{}, false, err
	}
	out, err := testAttemptFromRow(r)
	return out, err == nil, err
}

// UpdateTestAttempt stores execution progress without changing its deadline.
func (s *Store) UpdateTestAttempt(ctx context.Context, r domain.TestAttemptRecord) error {
	var target sql.NullString
	if r.Target.ID != "" {
		raw, err := json.Marshal(r.Target)
		if err != nil {
			return err
		}
		target = testNullString(string(raw))
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.UpdateTestAttempt(ctx, gen.UpdateTestAttemptParams{ID: string(r.ID), TargetIdentity: target, Phase: string(r.Phase), Outcome: testNullString(string(r.Outcome)), CleanupState: string(r.CleanupState), RecordingGap: r.RecordingGap, CancelledAt: testNullTime(r.CancelledAt), FinishedAt: testNullTime(r.FinishedAt)})
	return testRows(n, err)
}

// BindTestTools persists one current server-owned profile binding per session.
func (s *Store) BindTestTools(ctx context.Context, r domain.TestToolProfileLink) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.BindTestTools(ctx, gen.BindTestToolsParams{SessionID: string(r.SessionID), AttemptID: string(r.AttemptID), ProfileID: string(r.ProfileID)})
}

// GetTestToolBinding resolves the profile reference at worker spawn and restore.
func (s *Store) GetTestToolBinding(ctx context.Context, id domain.SessionID) (domain.TestToolProfileLink, bool, error) {
	r, err := s.qr.GetTestToolBinding(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TestToolProfileLink{}, false, nil
	}
	if err != nil {
		return domain.TestToolProfileLink{}, false, err
	}
	return domain.TestToolProfileLink{SessionID: domain.SessionID(r.SessionID), AttemptID: domain.TestAttemptID(r.AttemptID), ProfileID: domain.TestToolProfileID(r.ProfileID)}, true, nil
}

// CreateTestWorkerLegs stores the immutable revision pair before Chat starts.
func (s *Store) CreateTestWorkerLegs(ctx context.Context, r domain.TestWorkerLegs) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.CreateTestWorkerLegs(ctx, gen.CreateTestWorkerLegsParams{SessionID: string(r.SessionID), BaseRunID: string(r.BaseRunID), HeadRunID: string(r.HeadRunID), TimeoutSeconds: r.TimeoutSeconds})
}

// GetTestWorkerLegs resolves a comparison without relying on provider state.
func (s *Store) GetTestWorkerLegs(ctx context.Context, id domain.SessionID) (domain.TestWorkerLegs, bool, error) {
	r, err := s.qr.GetTestWorkerLegs(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TestWorkerLegs{}, false, nil
	}
	if err != nil {
		return domain.TestWorkerLegs{}, false, err
	}
	return domain.TestWorkerLegs{SessionID: domain.SessionID(r.SessionID), BaseRunID: domain.TestRunID(r.BaseRunID), HeadRunID: domain.TestRunID(r.HeadRunID), TimeoutSeconds: r.TimeoutSeconds}, true, nil
}
