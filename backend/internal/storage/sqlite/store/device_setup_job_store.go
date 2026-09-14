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

// UpsertDeviceSetupJob persists the latest durable state for one platform setup.
func (s *Store) UpsertDeviceSetupJob(ctx context.Context, job ports.DeviceSetupJobRecord) error {
	finishedAt := sql.NullTime{}
	if job.FinishedAt != nil {
		finishedAt = sql.NullTime{Time: job.FinishedAt.UTC(), Valid: true}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.UpsertDeviceSetupJob(ctx, gen.UpsertDeviceSetupJobParams{
		Platform: job.Platform, State: job.State, Stage: job.Stage, Message: job.Message,
		Progress: int64(job.Progress), DownloadedBytes: job.DownloadedBytes, TotalBytes: job.TotalBytes,
		RequiredBytes: job.RequiredBytes, AvailableBytes: job.AvailableBytes, LicenseURL: job.LicenseURL,
		LicenseAccepted: boolInt(job.LicenseAccepted), ActionURL: job.ActionURL, ErrorCode: job.ErrorCode,
		Error: job.Error, InstalledVersion: job.InstalledVersion, StartedAt: job.StartedAt.UTC(),
		FinishedAt: finishedAt, UpdatedAt: job.UpdatedAt.UTC(),
	}); err != nil {
		return fmt.Errorf("upsert device setup job: %w", err)
	}
	return nil
}

// GetDeviceSetupJob returns the durable setup state for one platform.
func (s *Store) GetDeviceSetupJob(ctx context.Context, platform string) (ports.DeviceSetupJobRecord, bool, error) {
	row, err := s.qr.GetDeviceSetupJob(ctx, platform)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.DeviceSetupJobRecord{}, false, nil
	}
	if err != nil {
		return ports.DeviceSetupJobRecord{}, false, fmt.Errorf("get device setup job: %w", err)
	}
	record := ports.DeviceSetupJobRecord{
		Platform: row.Platform, State: row.State, Stage: row.Stage, Message: row.Message,
		Progress: int(row.Progress), DownloadedBytes: row.DownloadedBytes, TotalBytes: row.TotalBytes,
		RequiredBytes: row.RequiredBytes, AvailableBytes: row.AvailableBytes, LicenseURL: row.LicenseURL,
		LicenseAccepted: row.LicenseAccepted != 0, ActionURL: row.ActionURL, ErrorCode: row.ErrorCode,
		Error: row.Error, InstalledVersion: row.InstalledVersion, StartedAt: row.StartedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if row.FinishedAt.Valid {
		finishedAt := row.FinishedAt.Time.UTC()
		record.FinishedAt = &finishedAt
	}
	return record, true, nil
}

// InterruptActiveDeviceSetupJobs marks setup abandoned by a daemon restart as resumable.
func (s *Store) InterruptActiveDeviceSetupJobs(ctx context.Context, interruptedAt time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.InterruptActiveDeviceSetupJobs(ctx, gen.InterruptActiveDeviceSetupJobsParams{
		FinishedAt: sql.NullTime{Time: interruptedAt.UTC(), Valid: true}, UpdatedAt: interruptedAt.UTC(),
	}); err != nil {
		return fmt.Errorf("interrupt active device setup jobs: %w", err)
	}
	return nil
}
