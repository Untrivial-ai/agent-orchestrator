package daemon

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	projectsummarysvc "github.com/aoagents/agent-orchestrator/backend/internal/service/projectsummary"
)

type projectReportLister interface {
	ListProject(context.Context, domain.ProjectID) ([]domain.ReportRecord, error)
}

type projectSummaryReportReader struct {
	reports projectReportLister
}

func (r projectSummaryReportReader) ListProject(ctx context.Context, projectID domain.ProjectID) ([]projectsummarysvc.ReportFact, error) {
	reports, err := r.reports.ListProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	facts := make([]projectsummarysvc.ReportFact, 0, len(reports))
	for _, report := range reports {
		outputs := make([]projectsummarysvc.ReportOutputFact, 0, len(report.Outputs))
		for _, output := range report.Outputs {
			outputs = append(outputs, projectsummarysvc.ReportOutputFact{
				Kind: string(output.Kind), Reference: output.Reference, Label: output.Label,
			})
		}
		facts = append(facts, projectsummarysvc.ReportFact{
			ID: report.ID, SessionID: report.SessionID, ProjectID: report.ProjectID,
			State: string(report.State), Note: report.Note, Message: report.Message,
			Outputs: outputs, CreatedAt: report.CreatedAt, RepeatCount: report.RepeatCount,
		})
	}
	return facts, nil
}
