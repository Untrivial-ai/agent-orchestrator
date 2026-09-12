package session

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
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
			name:   "auto review keeps a changes-requested head validating, even without auto-inject",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer", AutoReviewEnabled: true},
			pr:     &domain.PRFacts{URL: "pr1", HeadSHA: "head1"},
			runs: []domain.CurrentHeadReviewRun{
				{PRURL: "pr1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictChangesRequested},
			},
			want: domain.KanbanValidating,
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

func TestSessionListDerivesDisplayStatus(t *testing.T) {
	for _, tt := range []struct {
		name   string
		record domain.SessionRecord
		pr     *domain.PRFacts
		runs   []domain.CurrentHeadReviewRun
		want   domain.DisplayStatus
	}{
		{
			name: "a working session with no pr reports the worker",
			record: domain.SessionRecord{
				ID: "mer-1", ProjectID: "mer",
				Activity: domain.Activity{State: domain.ActivityActive},
			},
			want: contract.DisplayWorking,
		},
		{
			name:   "terminated is archived",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer", IsTerminated: true},
			pr:     &domain.PRFacts{URL: "pr1", HeadSHA: "head1"},
			want:   contract.DisplayTerminated,
		},
		{
			name:   "a pass in flight is reviewing",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer"},
			pr:     &domain.PRFacts{URL: "pr1", HeadSHA: "head1"},
			runs:   []domain.CurrentHeadReviewRun{{PRURL: "pr1", Status: domain.ReviewRunRunning}},
			want:   contract.DisplayReviewing,
		},
		{
			name:   "a cancelled pass leaves the review pending",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer", AutoReviewEnabled: true},
			pr:     &domain.PRFacts{URL: "pr1", HeadSHA: "head1"},
			runs:   []domain.CurrentHeadReviewRun{{PRURL: "pr1", Status: domain.ReviewRunCancelled}},
			want:   contract.DisplayReviewPending,
		},
		{
			name:   "failing checks on the current head are being fixed",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer", AutoInjectCI: true},
			pr: &domain.PRFacts{
				URL: "pr1", HeadSHA: "head1",
				CI: domain.CIFailing,
			},
			want: contract.DisplayFixingCI,
		},
		{
			name:   "a merged pr reports the merge",
			record: domain.SessionRecord{ID: "mer-1", ProjectID: "mer"},
			pr:     &domain.PRFacts{URL: "pr1", HeadSHA: "head1", Merged: true},
			want:   contract.DisplayMerged,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeStore()
			st.sessions[tt.record.ID] = tt.record
			if tt.pr != nil {
				st.pr[tt.record.ID] = *tt.pr
			}
			st.reviewRuns[tt.record.ID] = tt.runs

			got, err := (&Service{store: st}).Get(context.Background(), tt.record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.DisplayStatus != tt.want {
				t.Fatalf("display status = %q, want %q", got.DisplayStatus, tt.want)
			}
		})
	}
}

func TestSessionKanbanExternalChangesStayPersonOwned(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", AutoInjectReview: true,
	}
	st.pr["mer-1"] = domain.PRFacts{
		URL: "pr1", HeadSHA: "head1", Review: domain.ReviewChangesRequest, ExternalChangesRequested: true,
	}

	got, err := (&Service{store: st}).Get(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.KanbanColumn != domain.KanbanNeedsReview {
		t.Fatalf("kanban column = %q, want %q", got.KanbanColumn, domain.KanbanNeedsReview)
	}
	if got.DisplayStatus != contract.DisplayAddressingComments {
		t.Fatalf("display status = %q, want %q", got.DisplayStatus, contract.DisplayAddressingComments)
	}
}

func TestSessionKanbanExternalCommentsDriveNeedsReviewDisplayStatus(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer",
	}
	st.pr["mer-1"] = domain.PRFacts{
		URL: "pr1", HeadSHA: "head1", ReviewComments: true, ExternalComments: true,
	}

	got, err := (&Service{store: st}).Get(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.KanbanColumn != domain.KanbanNeedsReview {
		t.Fatalf("kanban column = %q, want %q", got.KanbanColumn, domain.KanbanNeedsReview)
	}
	if got.DisplayStatus != contract.DisplayCommented {
		t.Fatalf("display status = %q, want %q", got.DisplayStatus, contract.DisplayCommented)
	}
}

func TestSessionGetUsesLatestCurrentHeadRunPerHarness(t *testing.T) {
	st := newSQLiteKanbanTestStore(t)
	ctx := context.Background()
	seedKanbanTestProject(t, st, "mer")
	now := time.Now().UTC().Truncate(time.Second)

	rec, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID:         "mer",
		Kind:              domain.KindWorker,
		Harness:           domain.HarnessClaudeCode,
		AutoReviewEnabled: true,
		AutoInjectReview:  true,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteSCMObservation(ctx, domain.PullRequest{
		URL: "pr/1", SessionID: rec.ID, Number: 1, HeadSHA: "head1", UpdatedAt: now, ObservedAt: now,
	}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertReview(ctx, domain.Review{
		ID: "rev", SessionID: rec.ID, ProjectID: "mer", Harness: "claude-code",
		PRURL: "pr/1", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	insert := func(id string, createdAt time.Time, verdict domain.ReviewVerdict) {
		t.Helper()
		if err := st.InsertReviewRun(ctx, domain.ReviewRun{
			ID: id, ReviewID: "rev", SessionID: rec.ID, Harness: "claude-code",
			PRURL: "pr/1", TargetSHA: "head1", Status: domain.ReviewRunComplete,
			Verdict: verdict, CreatedAt: createdAt, AutoInjectReview: true,
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("old", now, domain.VerdictChangesRequested)
	insert("new", now.Add(time.Second), domain.VerdictApproved)

	got, err := (&Service{store: st}).Get(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.KanbanColumn != domain.KanbanNeedsReview {
		t.Fatalf("kanban column = %q, want %q after approved rerun on same head", got.KanbanColumn, domain.KanbanNeedsReview)
	}
	if got.DisplayStatus != contract.DisplayNeedsHumanReview {
		t.Fatalf("display status = %q, want %q after approved rerun on same head", got.DisplayStatus, contract.DisplayNeedsHumanReview)
	}
}

func newSQLiteKanbanTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	return sqlitetest.MustOpen(t)
}

func seedKanbanTestProject(t *testing.T, st *sqlite.Store, id string) {
	t.Helper()
	if err := st.UpsertProject(context.Background(), domain.ProjectRecord{
		ID: id, Path: "/tmp/" + id, RegisteredAt: time.Now().UTC().Truncate(time.Second),
	}); err != nil {
		t.Fatalf("seed project %s: %v", id, err)
	}
}
