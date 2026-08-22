package contract_test

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func TestDeriveKanbanColumnSessionLevelRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		session contract.KanbanSessionFacts
		prs     []contract.KanbanPRFacts
		want    contract.KanbanColumn
	}{
		{
			name:    "terminated archives even with a live pr",
			session: contract.KanbanSessionFacts{IsTerminated: true},
			prs:     []contract.KanbanPRFacts{{URL: "pr/1"}},
			want:    contract.KanbanArchive,
		},
		{
			name:    "no pr is still building",
			session: contract.KanbanSessionFacts{},
			want:    contract.KanbanBuilding,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := contract.DeriveKanbanColumn(tc.session, tc.prs); got != tc.want {
				t.Fatalf("column = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeriveKanbanColumnSinglePR(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		session contract.KanbanSessionFacts
		pr      contract.KanbanPRFacts
		want    contract.KanbanColumn
	}{
		{
			name: "draft is ao validation work",
			pr:   contract.KanbanPRFacts{URL: "pr/1", Draft: true},
			want: contract.KanbanValidating,
		},
		{
			name: "merged is ready",
			pr:   contract.KanbanPRFacts{URL: "pr/1", Merged: true},
			want: contract.KanbanReady,
		},
		{
			name: "closed without merge is ready",
			pr:   contract.KanbanPRFacts{URL: "pr/1", Closed: true},
			want: contract.KanbanReady,
		},
		{
			name: "mergeable is ready",
			pr:   contract.KanbanPRFacts{URL: "pr/1", Mergeability: contract.MergeMergeable},
			want: contract.KanbanReady,
		},
		{
			name: "human approval is ready",
			pr: contract.KanbanPRFacts{
				URL:            "pr/1",
				Review:         contract.ReviewApproved,
				Mergeability:   contract.MergeBlocked,
				ExternalReview: contract.KanbanExternalReviewFacts{Approved: true},
			},
			want: contract.KanbanReady,
		},
		{
			name: "ao's own approval alone is not ready",
			pr: contract.KanbanPRFacts{
				URL:          "pr/1",
				Review:       contract.ReviewApproved,
				Mergeability: contract.MergeBlocked,
				ReviewRun:    contract.KanbanReviewRunFacts{Present: true},
			},
			want: contract.KanbanNeedsReview,
		},
		{
			name: "review pass on the current head is validating",
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Running: true},
			},
			want: contract.KanbanValidating,
		},
		{
			name:    "ao addressing its own changes request is validating",
			session: contract.KanbanSessionFacts{AutoInjectReview: true},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, ChangesRequested: true},
			},
			want: contract.KanbanValidating,
		},
		{
			name:    "ao addressing human changes request is validating",
			session: contract.KanbanSessionFacts{AutoInjectReview: true},
			pr: contract.KanbanPRFacts{
				URL:            "pr/1",
				Review:         contract.ReviewChangesRequest,
				ExternalReview: contract.KanbanExternalReviewFacts{ChangesRequested: true},
				ReviewRun:      contract.KanbanReviewRunFacts{Present: true},
			},
			want: contract.KanbanValidating,
		},
		{
			name:    "ao fixing ci is validating",
			session: contract.KanbanSessionFacts{AutoInjectCI: true},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				CI:        contract.CIFailing,
				ReviewRun: contract.KanbanReviewRunFacts{Present: true},
			},
			want: contract.KanbanValidating,
		},
		{
			name:    "failing ci with injection off hands the loop to a person",
			session: contract.KanbanSessionFacts{},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				CI:        contract.CIFailing,
				ReviewRun: contract.KanbanReviewRunFacts{Present: true},
			},
			want: contract.KanbanNeedsReview,
		},
		{
			name:    "auto review owns an unreviewed head",
			session: contract.KanbanSessionFacts{AutoReview: true},
			pr:      contract.KanbanPRFacts{URL: "pr/1"},
			want:    contract.KanbanValidating,
		},
		{
			name:    "auto review off hands an unreviewed head to a person",
			session: contract.KanbanSessionFacts{},
			pr:      contract.KanbanPRFacts{URL: "pr/1"},
			want:    contract.KanbanNeedsReview,
		},
		{
			name:    "auto review hands the loop over once its pass approved this head",
			session: contract.KanbanSessionFacts{AutoReview: true},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true},
			},
			want: contract.KanbanNeedsReview,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := contract.DeriveKanbanColumn(tc.session, []contract.KanbanPRFacts{tc.pr})
			if got != tc.want {
				t.Fatalf("column = %q, want %q", got, tc.want)
			}
		})
	}
}

// A run recorded for an earlier head is dropped before the reducer sees it, so
// the PR reads as an unreviewed head and the review-feedback loop restarts: AO
// takes the next turn with auto review on, a person takes it with it off.
func TestDeriveKanbanColumnStaleReviewRunStartsANewCycle(t *testing.T) {
	t.Parallel()
	session := contract.KanbanSessionFacts{AutoReview: true}
	pr := contract.KanbanPRFacts{URL: "pr/1"}
	if got := contract.DeriveKanbanColumn(session, []contract.KanbanPRFacts{pr}); got != contract.KanbanValidating {
		t.Fatalf("auto review on: column = %q, want %q", got, contract.KanbanValidating)
	}
	if got := contract.DeriveKanbanColumn(contract.KanbanSessionFacts{}, []contract.KanbanPRFacts{pr}); got != contract.KanbanNeedsReview {
		t.Fatalf("auto review off: column = %q, want %q", got, contract.KanbanNeedsReview)
	}
}

func TestDeriveKanbanColumnMultiplePRs(t *testing.T) {
	t.Parallel()
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	t.Run("a merged pr never hides a live one", func(t *testing.T) {
		t.Parallel()
		got := contract.DeriveKanbanColumn(contract.KanbanSessionFacts{AutoReview: true}, []contract.KanbanPRFacts{
			{URL: "pr/merged", Merged: true, UpdatedAt: newer},
			{URL: "pr/live", UpdatedAt: older},
		})
		if got != contract.KanbanValidating {
			t.Fatalf("column = %q, want %q", got, contract.KanbanValidating)
		}
	})

	t.Run("terminal prs decide once nothing is live", func(t *testing.T) {
		t.Parallel()
		got := contract.DeriveKanbanColumn(contract.KanbanSessionFacts{}, []contract.KanbanPRFacts{
			{URL: "pr/merged", Merged: true, UpdatedAt: newer},
			{URL: "pr/closed", Closed: true, UpdatedAt: older},
		})
		if got != contract.KanbanReady {
			t.Fatalf("column = %q, want %q", got, contract.KanbanReady)
		}
	})

	t.Run("the most actionable live pr wins", func(t *testing.T) {
		t.Parallel()
		got := contract.DeriveKanbanColumn(contract.KanbanSessionFacts{}, []contract.KanbanPRFacts{
			{URL: "pr/validating", Draft: true, UpdatedAt: newer},
			{URL: "pr/needs-review", UpdatedAt: older},
		})
		if got != contract.KanbanNeedsReview {
			t.Fatalf("column = %q, want %q", got, contract.KanbanNeedsReview)
		}
	})

	t.Run("ties break on the newest pr then on url", func(t *testing.T) {
		t.Parallel()
		prs := []contract.KanbanPRFacts{
			{URL: "pr/b", Draft: true, UpdatedAt: older},
			{URL: "pr/a", Draft: true, UpdatedAt: older},
		}
		first := contract.DeriveKanbanColumn(contract.KanbanSessionFacts{}, prs)
		second := contract.DeriveKanbanColumn(contract.KanbanSessionFacts{}, []contract.KanbanPRFacts{prs[1], prs[0]})
		if first != second || first != contract.KanbanValidating {
			t.Fatalf("columns = %q/%q, want both %q", first, second, contract.KanbanValidating)
		}
	})
}
