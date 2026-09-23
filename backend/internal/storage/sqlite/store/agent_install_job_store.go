package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// UpsertAgentInstallJob persists the latest durable state for one harness installation.
func (s *Store) UpsertAgentInstallJob(ctx context.Context, job ports.AgentInstallJobRecord) error {
	finishedAt := sql.NullTime{}
	if job.FinishedAt != nil {
		finishedAt = sql.NullTime{Time: job.FinishedAt.UTC(), Valid: true}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.UpsertAgentInstallJob(ctx, gen.UpsertAgentInstallJobParams{
		Target: job.Target, Status: job.Status, Method: job.Method, Command: job.Command,
		ExpectedDestination: job.ExpectedDestination, Output: job.Output, Error: job.Error,
		StartedAt: job.StartedAt.UTC(), FinishedAt: finishedAt, UpdatedAt: job.UpdatedAt.UTC(),
	}); err != nil {
		return fmt.Errorf("upsert agent install job: %w", err)
	}
	return nil
}

// GetAgentInstallJob returns the durable installation state for one harness.
func (s *Store) GetAgentInstallJob(ctx context.Context, target string) (ports.AgentInstallJobRecord, bool, error) {
	row, err := s.qr.GetAgentInstallJob(ctx, target)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.AgentInstallJobRecord{}, false, nil
	}
	if err != nil {
		return ports.AgentInstallJobRecord{}, false, fmt.Errorf("get agent install job: %w", err)
	}
	return mapAgentInstallJob(row), true, nil
}

// ListAgentInstallJobs returns all durable harness installation states.
func (s *Store) ListAgentInstallJobs(ctx context.Context) ([]ports.AgentInstallJobRecord, error) {
	rows, err := s.qr.ListAgentInstallJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agent install jobs: %w", err)
	}
	out := make([]ports.AgentInstallJobRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapAgentInstallJob(row))
	}
	return out, nil
}

// InterruptActiveAgentInstallJobs marks work abandoned by a daemon restart as interrupted.
func (s *Store) InterruptActiveAgentInstallJobs(ctx context.Context, interruptedAt time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.InterruptActiveAgentInstallJobs(ctx, gen.InterruptActiveAgentInstallJobsParams{
		FinishedAt: sql.NullTime{Time: interruptedAt.UTC(), Valid: true},
		UpdatedAt:  interruptedAt.UTC(),
	}); err != nil {
		return fmt.Errorf("interrupt active agent install jobs: %w", err)
	}
	return nil
}

func mapAgentInstallJob(row gen.AgentInstallJob) ports.AgentInstallJobRecord {
	record := ports.AgentInstallJobRecord{
		Target: row.Target, Status: row.Status, Method: row.Method, Command: row.Command,
		ExpectedDestination: row.ExpectedDestination, Output: row.Output, Error: row.Error,
		StartedAt: row.StartedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if row.FinishedAt.Valid {
		finishedAt := row.FinishedAt.Time.UTC()
		record.FinishedAt = &finishedAt
	}
	return record
}
