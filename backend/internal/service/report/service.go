package report

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Store is the report service's persistence and ownership boundary.
type Store interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	CreateReport(context.Context, domain.ReportRecord) (domain.ReportRecord, error)
}

// CreateInput is the validated caller payload before AO-derived ownership and deadlines.
type CreateInput struct {
	SessionID domain.SessionID
	State     domain.ReportState
	Note      string
	Message   string
	Outputs   []domain.ReportOutput
}

// Service validates and creates durable worker reports.
type Service struct {
	store     Store
	now       func() time.Time
	newID     func() string
	onCreated func(domain.ReportRecord)
}

// Deps configures a Service.
type Deps struct {
	Store     Store
	Now       func() time.Time
	NewID     func() string
	OnCreated func(domain.ReportRecord)
}

// New constructs a report Service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.NewID == nil {
		d.NewID = func() string { return "rpt_" + uuid.NewString() }
	}
	return &Service{store: d.Store, now: d.Now, newID: d.NewID, onCreated: d.OnCreated}
}

// ListProject returns persisted report facts ordered by created_at then id,
// with each report's outputs in persisted position order. It never claims,
// acknowledges, schedules, or otherwise mutates report delivery state.
func (s *Service) ListProject(ctx context.Context, projectID domain.ProjectID) ([]domain.ReportRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("report: store is required")
	}
	if projectID == "" {
		return nil, apierr.Invalid("REPORT_PROJECT_REQUIRED", "Project id is required", nil)
	}
	reader, ok := s.store.(interface {
		ListReportsByProject(context.Context, domain.ProjectID) ([]domain.ReportRecord, error)
	})
	if !ok {
		return nil, errors.New("report: project reader is required")
	}
	reports, err := reader.ListReportsByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(reports, func(i, j int) bool {
		if reports[i].CreatedAt.Equal(reports[j].CreatedAt) {
			return reports[i].ID < reports[j].ID
		}
		return reports[i].CreatedAt.Before(reports[j].CreatedAt)
	})
	return reports, nil
}

// Create validates ownership and persists one pending report. Reports are
// coordination claims and never mutate authoritative worker lifecycle state.
func (s *Service) Create(ctx context.Context, input CreateInput) (domain.ReportRecord, error) {
	if s == nil || s.store == nil {
		return domain.ReportRecord{}, errors.New("report: store is required")
	}
	if input.SessionID == "" {
		return domain.ReportRecord{}, apierr.Invalid("INVALID_REPORT_SESSION", "Worker session id is required", nil)
	}
	if err := domain.ValidateReportContent(input.State, input.Note, input.Message, input.Outputs); err != nil {
		return domain.ReportRecord{}, apierr.Invalid("INVALID_REPORT", "Report state, note, message, or outputs are invalid", nil)
	}
	session, ok, err := s.store.GetSession(ctx, input.SessionID)
	if err != nil {
		return domain.ReportRecord{}, err
	}
	if !ok {
		return domain.ReportRecord{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	if session.Kind != domain.KindWorker {
		return domain.ReportRecord{}, apierr.Invalid("REPORT_WORKER_REQUIRED", "Reports can only be created by worker sessions", nil)
	}

	now := s.now().UTC()
	var availableAt time.Time
	var settlementDeadline time.Time
	switch input.State {
	case domain.ReportDone:
		settlementDeadline = now.Add(domain.ReportSettlementWindow)
		availableAt = settlementDeadline
	case domain.ReportNeedsInput, domain.ReportStuck:
		availableAt = now
	default:
		availableAt = now.Add(domain.ReportBatchFallback)
	}
	rec := domain.ReportRecord{
		ID: s.newID(), SessionID: session.ID, ProjectID: session.ProjectID,
		State: input.State, Note: input.Note, Message: input.Message, Outputs: input.Outputs,
		CreatedAt: now, DeliveryState: domain.ReportPending, AvailableAt: availableAt,
		SettlementDeadline: settlementDeadline, RepeatCount: 1,
	}
	created, err := s.store.CreateReport(ctx, rec)
	if err == nil && s.onCreated != nil {
		s.onCreated(created)
	}
	return created, err
}
