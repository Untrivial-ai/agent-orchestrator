package contract_test

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

// deriveColumn asserts only the stage-one placement. The column is derived from
// lifecycle facts alone, so it needs neither worker activity nor the clock.
func deriveColumn(session contract.KanbanSessionFacts, prs []contract.KanbanPRFacts) contract.KanbanColumn {
	return contract.DeriveKanbanPresentation(session, prs, time.Time{}, 0).Column
}

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
			session: contract.KanbanSessionFacts{SessionFacts: contract.SessionFacts{IsTerminated: true}},
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
			if got := deriveColumn(tc.session, tc.prs); got != tc.want {
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
			name: "closed draft is still ready",
			pr:   contract.KanbanPRFacts{URL: "pr/1", Closed: true, Draft: true},
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
			name:    "human changes request stays person-owned even with auto-inject on",
			session: contract.KanbanSessionFacts{AutoInjectReview: true},
			pr: contract.KanbanPRFacts{
				URL:            "pr/1",
				Review:         contract.ReviewChangesRequest,
				ExternalReview: contract.KanbanExternalReviewFacts{ChangesRequested: true},
				ReviewRun:      contract.KanbanReviewRunFacts{Present: true},
			},
			want: contract.KanbanNeedsReview,
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
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Outcome: true},
			},
			want: contract.KanbanNeedsReview,
		},
		{
			name:    "auto review keeps a changes-requested head, even without auto-inject",
			session: contract.KanbanSessionFacts{AutoReview: true},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Outcome: true, ChangesRequested: true},
			},
			want: contract.KanbanValidating,
		},
		{
			name: "auto-inject still keeps the loop moving on a changes-requested head",
			session: contract.KanbanSessionFacts{
				AutoReview:       true,
				AutoInjectReview: true,
			},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Outcome: true, ChangesRequested: true},
			},
			want: contract.KanbanValidating,
		},
		{
			name:    "without auto review, a changes-requested pass hands off to a person",
			session: contract.KanbanSessionFacts{},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Outcome: true, ChangesRequested: true},
			},
			want: contract.KanbanNeedsReview,
		},
		{
			name:    "auto review does not keep a mergeable pr in validating over a stale changes request",
			session: contract.KanbanSessionFacts{AutoReview: true},
			pr: contract.KanbanPRFacts{
				URL: "pr/1", Mergeability: contract.MergeMergeable,
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Outcome: true, ChangesRequested: true},
			},
			want: contract.KanbanReady,
		},
		{
			name:    "auto review still owns a head whose pass failed",
			session: contract.KanbanSessionFacts{AutoReview: true},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Failed: true},
			},
			want: contract.KanbanValidating,
		},
		{
			name:    "auto review still owns a head whose pass was cancelled",
			session: contract.KanbanSessionFacts{AutoReview: true},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Cancelled: true},
			},
			want: contract.KanbanValidating,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := deriveColumn(tc.session, []contract.KanbanPRFacts{tc.pr})
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
	if got := deriveColumn(session, []contract.KanbanPRFacts{pr}); got != contract.KanbanValidating {
		t.Fatalf("auto review on: column = %q, want %q", got, contract.KanbanValidating)
	}
	if got := deriveColumn(contract.KanbanSessionFacts{}, []contract.KanbanPRFacts{pr}); got != contract.KanbanNeedsReview {
		t.Fatalf("auto review off: column = %q, want %q", got, contract.KanbanNeedsReview)
	}
}

func TestDeriveKanbanColumnMultiplePRs(t *testing.T) {
	t.Parallel()
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	t.Run("a merged pr never hides a live one", func(t *testing.T) {
		t.Parallel()
		got := deriveColumn(contract.KanbanSessionFacts{AutoReview: true}, []contract.KanbanPRFacts{
			{URL: "pr/merged", Merged: true, UpdatedAt: newer},
			{URL: "pr/live", UpdatedAt: older},
		})
		if got != contract.KanbanValidating {
			t.Fatalf("column = %q, want %q", got, contract.KanbanValidating)
		}
	})

	t.Run("terminal prs decide once nothing is live", func(t *testing.T) {
		t.Parallel()
		got := deriveColumn(contract.KanbanSessionFacts{}, []contract.KanbanPRFacts{
			{URL: "pr/merged", Merged: true, UpdatedAt: newer},
			{URL: "pr/closed", Closed: true, UpdatedAt: older},
		})
		if got != contract.KanbanReady {
			t.Fatalf("column = %q, want %q", got, contract.KanbanReady)
		}
	})

	t.Run("the most actionable live pr wins", func(t *testing.T) {
		t.Parallel()
		got := deriveColumn(contract.KanbanSessionFacts{}, []contract.KanbanPRFacts{
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
		first := deriveColumn(contract.KanbanSessionFacts{}, prs)
		second := deriveColumn(contract.KanbanSessionFacts{}, []contract.KanbanPRFacts{prs[1], prs[0]})
		if first != second || first != contract.KanbanValidating {
			t.Fatalf("columns = %q/%q, want both %q", first, second, contract.KanbanValidating)
		}
	})
}

const testGrace = 90 * time.Second

func sessionAt(activity contract.ActivityState) contract.KanbanSessionFacts {
	return contract.KanbanSessionFacts{
		SessionFacts: contract.SessionFacts{Activity: activity},
	}
}

func TestDeriveKanbanPresentationBuilding(t *testing.T) {
	t.Parallel()
	silent := contract.SessionFacts{
		HasSignal:      false,
		SignalExpected: true,
		LastActivityAt: time.Unix(0, 0),
	}
	tests := []struct {
		name    string
		session contract.KanbanSessionFacts
		want    contract.DisplayStatus
	}{
		{"active worker is working", sessionAt(contract.ActivityActive), contract.DisplayWorking},
		{"blocked worker is blocked", sessionAt(contract.ActivityBlocked), contract.DisplayBlocked},
		{"a worker waiting on input is blocked", sessionAt(contract.ActivityWaitingInput), contract.DisplayBlocked},
		{"exited worker has exited", sessionAt(contract.ActivityExited), contract.DisplayExited},
		{
			name:    "a silent worker past the grace period has no signal",
			session: contract.KanbanSessionFacts{SessionFacts: silent},
			want:    contract.DisplayNoSignal,
		},
		{"an idle worker is awaiting its pr", sessionAt(contract.ActivityIdle), contract.DisplayAwaitingPR},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := contract.DeriveKanbanPresentation(tc.session, nil, time.Unix(3600, 0), testGrace)
			if got.Column != contract.KanbanBuilding {
				t.Fatalf("column = %q, want %q", got.Column, contract.KanbanBuilding)
			}
			if got.DisplayStatus != tc.want {
				t.Fatalf("display status = %q, want %q", got.DisplayStatus, tc.want)
			}
		})
	}
}

func TestDeriveKanbanPresentationSinglePR(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		session    contract.KanbanSessionFacts
		pr         contract.KanbanPRFacts
		wantColumn contract.KanbanColumn
		want       contract.DisplayStatus
	}{
		// Validating: the AO-driven loop.
		{
			name:       "a draft with nothing else to say is a draft",
			pr:         contract.KanbanPRFacts{URL: "pr/1", Draft: true},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayDraft,
		},
		{
			name: "a blocked worker outranks the loop it was running",
			session: contract.KanbanSessionFacts{
				SessionFacts: contract.SessionFacts{Activity: contract.ActivityBlocked},
				AutoInjectCI: true,
			},
			pr:         contract.KanbanPRFacts{URL: "pr/1", CI: contract.CIFailing},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayBlocked,
		},
		{
			name: "an exited worker outranks the loop it was running",
			session: contract.KanbanSessionFacts{
				SessionFacts: contract.SessionFacts{Activity: contract.ActivityExited},
				AutoInjectCI: true,
			},
			pr:         contract.KanbanPRFacts{URL: "pr/1", CI: contract.CIFailing},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayExited,
		},
		{
			name:       "failing ci with auto-fix on is being fixed",
			session:    contract.KanbanSessionFacts{AutoInjectCI: true},
			pr:         contract.KanbanPRFacts{URL: "pr/1", CI: contract.CIFailing},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayFixingCI,
		},
		{
			name:       "failing ci on a draft with auto-fix off says ci failing",
			pr:         contract.KanbanPRFacts{URL: "pr/1", Draft: true, CI: contract.CIFailing},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayCIFailing,
		},
		{
			name:    "an ao changes request with auto-inject on is being addressed",
			session: contract.KanbanSessionFacts{AutoInjectReview: true},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, ChangesRequested: true},
			},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayAddressingComments,
		},
		{
			name: "an ao changes request with auto-inject off needs review",
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				Draft:     true,
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, ChangesRequested: true},
			},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayNeedsReview,
		},
		{
			name:       "auto review with no pass on this head has one scheduled",
			session:    contract.KanbanSessionFacts{AutoReview: true},
			pr:         contract.KanbanPRFacts{URL: "pr/1"},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayReviewScheduled,
		},
		{
			name: "a pass in flight is reviewing",
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Running: true},
			},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayReviewing,
		},
		{
			name:    "a failed pass needs a review nobody produced",
			session: contract.KanbanSessionFacts{AutoReview: true},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Failed: true},
			},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayNeedsReview,
		},
		{
			name:    "a cancelled pass leaves the review pending",
			session: contract.KanbanSessionFacts{AutoReview: true},
			pr: contract.KanbanPRFacts{
				URL:       "pr/1",
				ReviewRun: contract.KanbanReviewRunFacts{Present: true, Cancelled: true},
			},
			wantColumn: contract.KanbanValidating,
			want:       contract.DisplayReviewPending,
		},

		// In review: the loop seen from the person's side.
		{
			name:       "an open pr nobody is handling needs a human review",
			pr:         contract.KanbanPRFacts{URL: "pr/1"},
			wantColumn: contract.KanbanNeedsReview,
			want:       contract.DisplayNeedsHumanReview,
		},
		{
			name:       "failing ci nobody is fixing says ci failing",
			pr:         contract.KanbanPRFacts{URL: "pr/1", CI: contract.CIFailing},
			wantColumn: contract.KanbanNeedsReview,
			want:       contract.DisplayCIFailing,
		},
		{
			name: "external comments with auto-inject off are commented",
			pr: contract.KanbanPRFacts{
				URL:            "pr/1",
				ExternalReview: contract.KanbanExternalReviewFacts{Comments: true},
			},
			wantColumn: contract.KanbanNeedsReview,
			want:       contract.DisplayCommented,
		},
		{
			name:    "external comments with auto-inject on are being addressed",
			session: contract.KanbanSessionFacts{AutoInjectReview: true},
			pr: contract.KanbanPRFacts{
				URL:            "pr/1",
				ExternalReview: contract.KanbanExternalReviewFacts{Comments: true},
			},
			wantColumn: contract.KanbanNeedsReview,
			want:       contract.DisplayAddressingComments,
		},
		{
			name: "an external changes request nobody is addressing requests changes",
			pr: contract.KanbanPRFacts{
				URL:            "pr/1",
				Review:         contract.ReviewChangesRequest,
				ExternalReview: contract.KanbanExternalReviewFacts{ChangesRequested: true},
			},
			wantColumn: contract.KanbanNeedsReview,
			want:       contract.DisplayChangesRequested,
		},
		{
			name:    "an external changes request with auto-inject on stays in review while being addressed",
			session: contract.KanbanSessionFacts{AutoInjectReview: true},
			pr: contract.KanbanPRFacts{
				URL:            "pr/1",
				Review:         contract.ReviewChangesRequest,
				ExternalReview: contract.KanbanExternalReviewFacts{ChangesRequested: true},
			},
			wantColumn: contract.KanbanNeedsReview,
			want:       contract.DisplayAddressingComments,
		},

		// Ready: how the pr landed, or what stands between it and the button.
		{
			name:       "a mergeable pr is mergeable",
			pr:         contract.KanbanPRFacts{URL: "pr/1", Mergeability: contract.MergeMergeable},
			wantColumn: contract.KanbanReady,
			want:       contract.DisplayMergeable,
		},
		{
			name: "an approved pr that is not mergeable is approved",
			pr: contract.KanbanPRFacts{
				URL:            "pr/1",
				Review:         contract.ReviewApproved,
				Mergeability:   contract.MergeBlocked,
				ExternalReview: contract.KanbanExternalReviewFacts{Approved: true},
			},
			wantColumn: contract.KanbanReady,
			want:       contract.DisplayApproved,
		},
		{
			name: "an approved pr blocked by checks says ci failing",
			pr: contract.KanbanPRFacts{
				URL:            "pr/1",
				Review:         contract.ReviewApproved,
				Mergeability:   contract.MergeBlocked,
				CI:             contract.CIFailing,
				ExternalReview: contract.KanbanExternalReviewFacts{Approved: true},
			},
			wantColumn: contract.KanbanReady,
			want:       contract.DisplayCIFailing,
		},
		{
			name:       "a merged pr is merged",
			pr:         contract.KanbanPRFacts{URL: "pr/1", Merged: true},
			wantColumn: contract.KanbanReady,
			want:       contract.DisplayMerged,
		},
		{
			name:       "a closed pr closed without merging",
			pr:         contract.KanbanPRFacts{URL: "pr/1", Closed: true},
			wantColumn: contract.KanbanReady,
			want:       contract.DisplayClosed,
		},
		{
			name:       "a merged pr reports the merge, not its stale merge readiness",
			pr:         contract.KanbanPRFacts{URL: "pr/1", Merged: true, Mergeability: contract.MergeMergeable},
			wantColumn: contract.KanbanReady,
			want:       contract.DisplayMerged,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := contract.DeriveKanbanPresentation(
				tc.session, []contract.KanbanPRFacts{tc.pr}, time.Unix(3600, 0), testGrace,
			)
			if got.Column != tc.wantColumn {
				t.Fatalf("column = %q, want %q", got.Column, tc.wantColumn)
			}
			if got.DisplayStatus != tc.want {
				t.Fatalf("display status = %q, want %q", got.DisplayStatus, tc.want)
			}
		})
	}
}

func TestDeriveKanbanPresentationTerminatedArchives(t *testing.T) {
	t.Parallel()
	got := contract.DeriveKanbanPresentation(
		contract.KanbanSessionFacts{
			SessionFacts: contract.SessionFacts{IsTerminated: true, Activity: contract.ActivityActive},
		},
		[]contract.KanbanPRFacts{{URL: "pr/1", Merged: true}},
		time.Unix(3600, 0), testGrace,
	)
	want := contract.KanbanPresentation{
		Column:        contract.KanbanArchive,
		DisplayStatus: contract.DisplayTerminated,
	}
	if got != want {
		t.Fatalf("presentation = %+v, want %+v", got, want)
	}
}

// A pass recorded for an earlier commit is filtered out before the reducer
// sees it, so the head it left behind reads as unreviewed rather than as a
// finished review.
func TestDeriveKanbanPresentationIgnoresAPassForAnEarlierHead(t *testing.T) {
	t.Parallel()
	got := contract.DeriveKanbanPresentation(
		contract.KanbanSessionFacts{AutoReview: true},
		[]contract.KanbanPRFacts{{URL: "pr/1"}},
		time.Unix(3600, 0), testGrace,
	)
	if got.DisplayStatus != contract.DisplayReviewScheduled {
		t.Fatalf("display status = %q, want %q", got.DisplayStatus, contract.DisplayReviewScheduled)
	}
}

// The display status must describe the PR the column was chosen from, so a
// landed PR cannot speak for a session whose other PR still needs work.
func TestDeriveKanbanPresentationSpeaksForTheChosenPR(t *testing.T) {
	t.Parallel()
	older := time.Unix(100, 0)
	newer := time.Unix(200, 0)

	t.Run("a merged pr does not hide live work", func(t *testing.T) {
		t.Parallel()
		got := contract.DeriveKanbanPresentation(
			contract.KanbanSessionFacts{AutoInjectCI: true},
			[]contract.KanbanPRFacts{
				{URL: "pr/1", Merged: true, UpdatedAt: newer},
				{URL: "pr/2", CI: contract.CIFailing, UpdatedAt: older},
			},
			time.Unix(3600, 0), testGrace,
		)
		want := contract.KanbanPresentation{
			Column:        contract.KanbanValidating,
			DisplayStatus: contract.DisplayFixingCI,
		}
		if got != want {
			t.Fatalf("presentation = %+v, want %+v", got, want)
		}
	})

	t.Run("only terminal prs report the best landing", func(t *testing.T) {
		t.Parallel()
		got := contract.DeriveKanbanPresentation(
			contract.KanbanSessionFacts{},
			[]contract.KanbanPRFacts{
				{URL: "pr/1", Closed: true, UpdatedAt: older},
				{URL: "pr/2", Merged: true, UpdatedAt: newer},
			},
			time.Unix(3600, 0), testGrace,
		)
		want := contract.KanbanPresentation{
			Column:        contract.KanbanReady,
			DisplayStatus: contract.DisplayMerged,
		}
		if got != want {
			t.Fatalf("presentation = %+v, want %+v", got, want)
		}
	})

	t.Run("a pr keeps the card out of building whatever the worker is doing", func(t *testing.T) {
		t.Parallel()
		got := contract.DeriveKanbanPresentation(
			sessionAt(contract.ActivityActive),
			[]contract.KanbanPRFacts{{URL: "pr/1"}},
			time.Unix(3600, 0), testGrace,
		)
		if got.Column == contract.KanbanBuilding || got.DisplayStatus == contract.DisplayWorking {
			t.Fatalf("presentation = %+v, want a pr-driven placement", got)
		}
	})
}
