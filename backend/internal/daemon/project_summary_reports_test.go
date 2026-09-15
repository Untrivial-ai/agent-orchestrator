package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type stubProjectReportLister struct {
	reports   []domain.ReportRecord
	err       error
	projectID domain.ProjectID
}

func (s *stubProjectReportLister) ListProject(_ context.Context, projectID domain.ProjectID) ([]domain.ReportRecord, error) {
	s.projectID = projectID
	return s.reports, s.err
}

func TestProjectSummaryReportReaderAdaptsReadOnlyFacts(t *testing.T) {
	createdAt := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	source := &stubProjectReportLister{reports: []domain.ReportRecord{{
		ID: "rpt-1", SessionID: "worker-1", ProjectID: "project-1",
		State: domain.ReportNeedsInput, Note: "Choose an API.", CreatedAt: createdAt,
		RepeatCount: 2, Outputs: []domain.ReportOutput{{Kind: domain.ReportOutputArtifact, Reference: "opaque", Label: "Design"}},
	}}}

	facts, err := (projectSummaryReportReader{reports: source}).ListProject(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if source.projectID != "project-1" || len(facts) != 1 {
		t.Fatalf("project = %q, facts = %d", source.projectID, len(facts))
	}
	fact := facts[0]
	if fact.ID != "rpt-1" || fact.SessionID != "worker-1" || fact.ProjectID != "project-1" || fact.State != "needs_input" || fact.Note != "Choose an API." || !fact.CreatedAt.Equal(createdAt) || fact.RepeatCount != 2 {
		t.Fatalf("fact = %#v", fact)
	}
	if len(fact.Outputs) != 1 || fact.Outputs[0].Kind != "artifact" || fact.Outputs[0].Reference != "opaque" || fact.Outputs[0].Label != "Design" {
		t.Fatalf("outputs = %#v", fact.Outputs)
	}
}

func TestProjectSummaryReportReaderReturnsListError(t *testing.T) {
	want := errors.New("list failed")
	_, got := (projectSummaryReportReader{reports: &stubProjectReportLister{err: want}}).ListProject(context.Background(), "project-1")
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}
