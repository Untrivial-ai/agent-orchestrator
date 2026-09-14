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

// CreateReport atomically persists one validated pending report and all outputs.
func (s *Store) CreateReport(ctx context.Context, rec domain.ReportRecord) (domain.ReportRecord, error) {
	if err := rec.Validate(); err != nil {
		return domain.ReportRecord{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var row gen.Report
	err := s.inTx(ctx, "create report", func(q *gen.Queries) error {
		var err error
		row, err = q.CreateReport(ctx, gen.CreateReportParams{
			ID: rec.ID, SessionID: string(rec.SessionID), ProjectID: string(rec.ProjectID),
			State: string(rec.State), Note: rec.Note, Message: rec.Message,
			CreatedAt: rec.CreatedAt, AvailableAt: rec.AvailableAt,
			SettlementDeadline: nullTime(rec.SettlementDeadline), RepeatCount: rec.RepeatCount,
		})
		if err != nil {
			return err
		}
		for i, output := range rec.Outputs {
			if err := q.CreateReportOutput(ctx, gen.CreateReportOutputParams{
				ReportID: rec.ID, Position: int64(i), Kind: string(output.Kind),
				Reference: output.Reference, Label: output.Label,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.ReportRecord{}, fmt.Errorf("create report %s: %w", rec.ID, err)
	}
	created := reportFromGen(row)
	created.Outputs = append([]domain.ReportOutput(nil), rec.Outputs...)
	return created, nil
}

// GetReport returns one report by durable ID with its ordered outputs.
func (s *Store) GetReport(ctx context.Context, id string) (domain.ReportRecord, bool, error) {
	row, err := s.qr.GetReport(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReportRecord{}, false, nil
	}
	if err != nil {
		return domain.ReportRecord{}, false, fmt.Errorf("get report %s: %w", id, err)
	}
	rec, err := s.reportWithOutputs(ctx, row)
	if err != nil {
		return domain.ReportRecord{}, false, err
	}
	return rec, true, nil
}

// ListReportsBySession returns a worker's reports in creation order.
func (s *Store) ListReportsBySession(ctx context.Context, id domain.SessionID) ([]domain.ReportRecord, error) {
	rows, err := s.qr.ListReportsBySession(ctx, string(id))
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	return s.reportsWithOutputs(ctx, rows)
}

// ListPendingReports returns reports eligible for a later delivery batch.
func (s *Store) ListPendingReports(ctx context.Context, at time.Time, limit int64) ([]domain.ReportRecord, error) {
	rows, err := s.qr.ListPendingReports(ctx, gen.ListPendingReportsParams{AvailableAt: at, Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("list pending reports: %w", err)
	}
	return s.reportsWithOutputs(ctx, rows)
}

// ClaimReport atomically fences one pending report to a delivery attempt token.
func (s *Store) ClaimReport(ctx context.Context, id, token string, at time.Time) (domain.ReportRecord, bool, error) {
	return s.updateReportClaim(ctx, id, token, at, "claim", func() (gen.Report, error) {
		return s.qw.ClaimReport(ctx, gen.ClaimReportParams{ID: id, ClaimToken: token, ClaimedAt: nullTime(at)})
	})
}

// AcknowledgeReport completes only the delivery attempt holding the current token.
func (s *Store) AcknowledgeReport(ctx context.Context, id, token string, at time.Time) (domain.ReportRecord, bool, error) {
	return s.updateReportClaim(ctx, id, token, at, "acknowledge", func() (gen.Report, error) {
		return s.qw.AcknowledgeReport(ctx, gen.AcknowledgeReportParams{ID: id, ClaimToken: token, AcknowledgedAt: nullTime(at)})
	})
}

// ReleaseReport returns the matching token's claim to pending with retry metadata.
func (s *Store) ReleaseReport(ctx context.Context, id, token string, availableAt time.Time, lastError string) (domain.ReportRecord, bool, error) {
	return s.updateReportClaim(ctx, id, token, availableAt, "release", func() (gen.Report, error) {
		return s.qw.ReleaseReport(ctx, gen.ReleaseReportParams{ID: id, ClaimToken: token, AvailableAt: availableAt, LastError: lastError})
	})
}

func (s *Store) updateReportClaim(ctx context.Context, id, token string, at time.Time, action string, update func() (gen.Report, error)) (domain.ReportRecord, bool, error) {
	if id == "" || token == "" || at.IsZero() {
		return domain.ReportRecord{}, false, domain.ErrInvalidReport
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := update()
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReportRecord{}, false, nil
	}
	if err != nil {
		return domain.ReportRecord{}, false, fmt.Errorf("%s report %s: %w", action, id, err)
	}
	rec, err := s.reportWithOutputs(ctx, row)
	return rec, err == nil, err
}

// RequeueClaimedReports atomically recovers claims owned by a prior daemon process.
func (s *Store) RequeueClaimedReports(ctx context.Context) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	count, err := s.qw.RequeueClaimedReports(ctx)
	if err != nil {
		return 0, fmt.Errorf("requeue claimed reports: %w", err)
	}
	return count, nil
}

func (s *Store) reportWithOutputs(ctx context.Context, row gen.Report) (domain.ReportRecord, error) {
	outputs, err := s.qr.ListReportOutputs(ctx, row.ID)
	if err != nil {
		return domain.ReportRecord{}, fmt.Errorf("list report %s outputs: %w", row.ID, err)
	}
	rec := reportFromGen(row)
	rec.Outputs = make([]domain.ReportOutput, len(outputs))
	for i, output := range outputs {
		rec.Outputs[i] = domain.ReportOutput{Kind: domain.ReportOutputKind(output.Kind), Reference: output.Reference, Label: output.Label}
	}
	return rec, nil
}

func (s *Store) reportsWithOutputs(ctx context.Context, rows []gen.Report) ([]domain.ReportRecord, error) {
	out := make([]domain.ReportRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := s.reportWithOutputs(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func reportFromGen(r gen.Report) domain.ReportRecord {
	return domain.ReportRecord{
		ID: r.ID, SessionID: domain.SessionID(r.SessionID), ProjectID: domain.ProjectID(r.ProjectID),
		State: domain.ReportState(r.State), Note: r.Note, Message: r.Message, CreatedAt: r.CreatedAt,
		DeliveryState: domain.ReportDeliveryState(r.DeliveryState), AvailableAt: r.AvailableAt,
		SettlementDeadline: timeFromNull(r.SettlementDeadline), RepeatCount: r.RepeatCount,
		ClaimToken: r.ClaimToken, ClaimedAt: timeFromNull(r.ClaimedAt), DeliveryAttempts: r.DeliveryAttempts,
		AcknowledgedAt: timeFromNull(r.AcknowledgedAt), LastError: r.LastError,
	}
}
