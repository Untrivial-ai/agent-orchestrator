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

func researchFromGen(row gen.ResearchRun) domain.ResearchRun {
	rec := domain.ResearchRun{
		ID: row.ID, ParentSessionID: domain.SessionID(row.ParentSessionID),
		ProjectID: domain.ProjectID(row.ProjectID), Prompt: row.Prompt,
		Harness: domain.AgentHarness(row.Harness),
		AgentConfig: domain.AgentConfig{Model: row.Model, Effort: row.Effort, Mode: row.Mode,
			Permissions: domain.PermissionMode(row.Permissions)},
		Status: row.Status, Result: row.Result,
		Error: row.Error, CreatedAt: row.CreatedAt,
	}
	if row.StartedAt.Valid {
		rec.StartedAt = &row.StartedAt.Time
	}
	if row.FinishedAt.Valid {
		rec.FinishedAt = &row.FinishedAt.Time
	}
	return rec
}

// CreateResearchRun persists a run when its orchestrator has no active run.
func (s *Store) CreateResearchRun(ctx context.Context, rec domain.ResearchRun) (domain.ResearchRun, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.qw.GetActiveResearchRunByParent(ctx, string(rec.ParentSessionID)); err == nil {
		return domain.ResearchRun{}, domain.ErrResearchAlreadyRunning
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.ResearchRun{}, fmt.Errorf("check active research: %w", err)
	}
	row, err := s.qw.CreateResearchRun(ctx, gen.CreateResearchRunParams{
		ID: rec.ID, ParentSessionID: string(rec.ParentSessionID), ProjectID: string(rec.ProjectID),
		Prompt: rec.Prompt, Harness: string(rec.Harness), Model: rec.AgentConfig.Model,
		Effort: rec.AgentConfig.Effort, Mode: rec.AgentConfig.Mode,
		Permissions: string(rec.AgentConfig.Permissions), CreatedAt: rec.CreatedAt,
	})
	if err != nil {
		return domain.ResearchRun{}, fmt.Errorf("create research: %w", err)
	}
	return researchFromGen(row), nil
}

// GetResearchRun looks up a run by ID.
func (s *Store) GetResearchRun(ctx context.Context, id string) (domain.ResearchRun, bool, error) {
	row, err := s.qr.GetResearchRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ResearchRun{}, false, nil
	}
	if err != nil {
		return domain.ResearchRun{}, false, fmt.Errorf("get research: %w", err)
	}
	return researchFromGen(row), true, nil
}

// ListResearchRunsByParent returns runs for one orchestrator.
func (s *Store) ListResearchRunsByParent(ctx context.Context, id domain.SessionID) ([]domain.ResearchRun, error) {
	rows, err := s.qr.ListResearchRunsByParent(ctx, string(id))
	if err != nil {
		return nil, fmt.Errorf("list research: %w", err)
	}
	out := make([]domain.ResearchRun, len(rows))
	for i, row := range rows {
		out[i] = researchFromGen(row)
	}
	return out, nil
}

// ListUnfinishedResearchRuns returns runs needing recovery after a restart.
func (s *Store) ListUnfinishedResearchRuns(ctx context.Context) ([]domain.ResearchRun, error) {
	rows, err := s.qr.ListUnfinishedResearchRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unfinished research: %w", err)
	}
	out := make([]domain.ResearchRun, len(rows))
	for i, row := range rows {
		out[i] = researchFromGen(row)
	}
	return out, nil
}

// MarkResearchRunning transitions a queued run to running.
func (s *Store) MarkResearchRunning(ctx context.Context, id string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.MarkResearchRunning(ctx, gen.MarkResearchRunningParams{ID: id, StartedAt: nullTime(at)})
	return n == 1, err
}

// FinishResearchRun stores the terminal status and result of a run.
func (s *Store) FinishResearchRun(ctx context.Context, id, status, result, message string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.FinishResearchRun(ctx, gen.FinishResearchRunParams{
		ID: id, Status: status, Result: result, Error: message, FinishedAt: nullTime(at),
	})
	return n == 1, err
}

// CancelResearchRun marks an active run cancelled.
func (s *Store) CancelResearchRun(ctx context.Context, id string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.CancelResearchRun(ctx, gen.CancelResearchRunParams{ID: id, FinishedAt: nullTime(at)})
	return n == 1, err
}

// InterruptResearchRun marks an orphaned run interrupted.
func (s *Store) InterruptResearchRun(ctx context.Context, id, message string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.InterruptResearchRun(ctx, gen.InterruptResearchRunParams{ID: id, Error: message, FinishedAt: nullTime(at)})
	return n == 1, err
}
