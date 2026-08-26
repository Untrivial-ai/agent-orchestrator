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

func (s *Store) CreateReport(ctx context.Context, rec domain.ReportRecord) (domain.ReportRecord, error) {
	if err := rec.Validate(); err != nil {
		return domain.ReportRecord{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.CreateReport(ctx, gen.CreateReportParams{ID: rec.ID, SessionID: string(rec.SessionID), ProjectID: string(rec.ProjectID), Type: string(rec.Type), Note: rec.Note, CreatedAt: rec.CreatedAt, AvailableAt: rec.AvailableAt})
	if err != nil {
		return domain.ReportRecord{}, fmt.Errorf("create report %s: %w", rec.ID, err)
	}
	return reportFromGen(row), nil
}

func (s *Store) GetReport(ctx context.Context, id string) (domain.ReportRecord, bool, error) {
	row, err := s.qr.GetReport(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReportRecord{}, false, nil
	}
	if err != nil {
		return domain.ReportRecord{}, false, fmt.Errorf("get report %s: %w", id, err)
	}
	return reportFromGen(row), true, nil
}

func (s *Store) ListReportsBySession(ctx context.Context, id domain.SessionID) ([]domain.ReportRecord, error) {
	rows, err := s.qr.ListReportsBySession(ctx, string(id))
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	return reportsFromGen(rows), nil
}

func (s *Store) ListPendingReports(ctx context.Context, at time.Time, limit int64) ([]domain.ReportRecord, error) {
	rows, err := s.qr.ListPendingReports(ctx, gen.ListPendingReportsParams{AvailableAt: at, Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("list pending reports: %w", err)
	}
	return reportsFromGen(rows), nil
}

func reportFromGen(r gen.Report) domain.ReportRecord {
	return domain.ReportRecord{ID: r.ID, SessionID: domain.SessionID(r.SessionID), ProjectID: domain.ProjectID(r.ProjectID), Type: domain.ReportType(r.Type), Note: r.Note, CreatedAt: r.CreatedAt, DeliveryState: domain.ReportDeliveryState(r.DeliveryState), AvailableAt: r.AvailableAt, ClaimToken: r.ClaimToken, ClaimedAt: timeFromNull(r.ClaimedAt), DeliveryAttempts: r.DeliveryAttempts, AcknowledgedAt: timeFromNull(r.AcknowledgedAt), LastError: r.LastError}
}
func reportsFromGen(rows []gen.Report) []domain.ReportRecord {
	out := make([]domain.ReportRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, reportFromGen(r))
	}
	return out
}
