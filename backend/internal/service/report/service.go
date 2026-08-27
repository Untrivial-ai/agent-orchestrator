package report

import (
	"context"
	"errors"
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

// Service validates and creates durable worker reports.
type Service struct {
	store Store
	now   func() time.Time
	newID func() string
}

// Deps configures a Service.
type Deps struct {
	Store Store
	Now   func() time.Time
	NewID func() string
}

// New constructs a report Service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.NewID == nil {
		d.NewID = func() string { return "rpt_" + uuid.NewString() }
	}
	return &Service{store: d.Store, now: d.Now, newID: d.NewID}
}

// Create validates ownership and persists one pending report.
func (s *Service) Create(ctx context.Context, sessionID domain.SessionID, typ domain.ReportType, note string) (domain.ReportRecord, error) {
	if s == nil || s.store == nil {
		return domain.ReportRecord{}, errors.New("report: store is required")
	}
	if sessionID == "" {
		return domain.ReportRecord{}, apierr.Invalid("INVALID_REPORT_SESSION", "Worker session id is required", nil)
	}
	if err := domain.ValidateReportNote(typ, note); err != nil {
		message := "Report type and note are invalid"
		code := "INVALID_REPORT"
		if typ == domain.ReportPRCreated {
			message, code = "PR-created reports require an HTTP(S) GitHub pull-request URL", "INVALID_PR_URL"
		}
		return domain.ReportRecord{}, apierr.Invalid(code, message, nil)
	}
	session, ok, err := s.store.GetSession(ctx, sessionID)
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
	rec := domain.ReportRecord{ID: s.newID(), SessionID: session.ID, ProjectID: session.ProjectID, Type: typ, Note: note, CreatedAt: now, DeliveryState: domain.ReportPending, AvailableAt: now}
	return s.store.CreateReport(ctx, rec)
}
