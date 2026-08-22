package session

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSessionListDerivesKanbanColumn(t *testing.T) {
	for _, tt := range []struct {
		name    string
		record  domain.SessionRecord
		pr      *domain.PRFacts
		runs    []domain.CurrentHeadReviewRun
		want    domain.KanbanColumn
		wantPRs bool
	}{
		{
			name:   "no pr is building",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer"},
			want:   domain.KanbanBuilding,
		},
		{
			name:   "terminated archives",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer", IsTerminated: true},
			pr:     &domain.PRFacts{URL: "pr1", HeadSHA: "head1"},
			want:   domain.KanbanArchive,
		},
		{
			name:   "a running current-head pass is ao validation work",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer"},
			pr:     &domain.PRFacts{URL: "pr1", HeadSHA: "head1"},
			runs:   []domain.CurrentHeadReviewRun{{PRURL: "pr1", Status: domain.ReviewRunRunning}},
			want:   domain.KanbanValidating,
		},
		{
			name:   "a finished pass with auto review off hands over to a person",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer"},
			pr:     &domain.PRFacts{URL: "pr1", HeadSHA: "head1"},
			runs: []domain.CurrentHeadReviewRun{
				{PRURL: "pr1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved},
			},
			want: domain.KanbanNeedsReview,
		},
		{
			name:   "a human approval on a blocked pr is ready",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer"},
			pr: &domain.PRFacts{
				URL: "pr1", HeadSHA: "head1", Review: domain.ReviewApproved,
				Mergeability: domain.MergeBlocked, ExternalApproved: true,
			},
			want: domain.KanbanReady,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeStore()
			st.sessions[tt.record.ID] = tt.record
			if tt.pr != nil {
				st.pr[tt.record.ID] = *tt.pr
			}
			st.reviewRuns[tt.record.ID] = tt.runs

			list, err := (&Service{store: st}).List(context.Background(), ListFilter{ProjectID: "mer"})
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].KanbanColumn != tt.want {
				t.Fatalf("kanban column = %+v, want %q", list, tt.want)
			}
		})
	}
}

// A review pass recorded against an older commit is filtered out in SQL, so the
// service must not treat it as current-head validation work.
func TestSessionKanbanIgnoresPassesForAnEarlierHead(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", AutoReviewEnabled: true}
	st.pr["mer-1"] = domain.PRFacts{URL: "pr1", HeadSHA: "head2"}
	// The store returns nothing: the stale head1 pass never reaches the reducer.
	st.reviewRuns["mer-1"] = nil

	got, err := (&Service{store: st}).Get(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.KanbanColumn != domain.KanbanValidating {
		t.Fatalf("kanban column = %q, want %q for a fresh unreviewed head", got.KanbanColumn, domain.KanbanValidating)
	}
}
