package lifecycle

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReviewBatchSuppressesDuplicateSCMReviewNudge(t *testing.T) {
	m, st, msg := newManager()

	workerID := domain.SessionID("mer-review-dedup")
	prURL := "https://github.com/acme/repo/pull/7"
	st.sessions[workerID] = working(workerID)

	outcome, err := m.ApplyReviewBatch(
		context.Background(),
		workerID,
		"batch-1",
		[]ReviewResult{{
			RunID:          "run-1",
			BatchID:        "batch-1",
			WorkerID:       workerID,
			PRURL:          prURL,
			TargetSHA:      "deadbeef",
			Verdict:        domain.VerdictChangesRequested,
			Body:           "Remove extra.txt",
			GithubReviewID: "4949207965",
		}},
	)
	if err != nil {
		t.Fatalf("ApplyReviewBatch: %v", err)
	}
	if outcome != ReviewDeliverySent {
		t.Fatalf("ApplyReviewBatch outcome = %q, want %q", outcome, ReviewDeliverySent)
	}
	if got := len(msg.msgs); got != 1 {
		t.Fatalf("internal review should wake worker exactly once; got %d messages: %#v", got, msg.msgs)
	}

	// The SCM observer sees the same GitHub review through GraphQL, where the
	// review ID is an opaque node ID but the review URL still carries the
	// numeric GitHub review/database ID reported by AO's reviewer.
	st.reviews[prURL] = []domain.PullRequestReview{{
		ID:               "PRR_kwDOopaque",
		Author:           "reviewer",
		State:            domain.ReviewChangesRequest,
		URL:              prURL + "#pullrequestreview-4949207965",
		Body:             "Remove extra.txt",
		TargetSHA:        "deadbeef",
		AutoInjectReview: true,
	}}

	err = m.ApplyPRObservation(
		context.Background(),
		workerID,
		ports.PRObservation{
			Fetched: true,
			URL:     prURL,
			Review:  domain.ReviewChangesRequest,
		},
	)
	if err != nil {
		t.Fatalf("ApplyPRObservation: %v", err)
	}

	if got := len(msg.msgs); got != 1 {
		t.Fatalf(
			"SCM observation of the already-delivered GitHub review must not wake worker again; got %d messages: %#v",
			got,
			msg.msgs,
		)
	}
}

func TestPRObservation_WorkerReplyInSameReviewThreadDoesNotRenudge(t *testing.T) {
	m, st, msg := newManager()

	workerID := domain.SessionID("mer-thread-dedup")
	prURL := "https://github.com/acme/repo/pull/8"
	st.sessions[workerID] = working(workerID)

	// First observation: the reviewer leaves one actionable finding.
	st.comments[prURL] = []domain.PullRequestComment{
		{
			ThreadID:         "thread-1",
			ID:               "review-comment-1",
			Author:           "reviewer",
			File:             "extra.txt",
			Line:             1,
			Body:             "Remove this out-of-scope file.",
			Resolved:         false,
			AutoInjectReview: true,
		},
	}

	err := m.ApplyPRObservation(
		context.Background(),
		workerID,
		ports.PRObservation{
			Fetched: true,
			URL:     prURL,
		},
	)
	if err != nil {
		t.Fatalf("first ApplyPRObservation: %v", err)
	}

	if got := len(msg.msgs); got != 1 {
		t.Fatalf(
			"reviewer comment should wake worker exactly once; got %d messages: %#v",
			got,
			msg.msgs,
		)
	}

	// Second observation: after addressing the finding, the worker replies in
	// the SAME review thread. GitHub assigns the reply a different comment ID,
	// but it is not a new actionable review finding and must not wake the worker
	// again merely because the thread snapshot changed.
	st.comments[prURL] = append(
		st.comments[prURL],
		domain.PullRequestComment{
			ThreadID:         "thread-1",
			ID:               "worker-reply-1",
			Author:           "worker",
			File:             "extra.txt",
			Line:             1,
			Body:             "Removed in the latest commit.",
			Resolved:         false,
			AutoInjectReview: true,
		},
	)

	err = m.ApplyPRObservation(
		context.Background(),
		workerID,
		ports.PRObservation{
			Fetched: true,
			URL:     prURL,
		},
	)
	if err != nil {
		t.Fatalf("second ApplyPRObservation: %v", err)
	}

	if got := len(msg.msgs); got != 1 {
		t.Fatalf(
			"worker reply in an already-notified review thread must not wake worker again; got %d messages: %#v",
			got,
			msg.msgs,
		)
	}
}

func TestPRObservation_NewReviewThreadStillNudges(t *testing.T) {
	m, st, msg := newManager()

	workerID := domain.SessionID("mer-new-thread")
	prURL := "https://github.com/acme/repo/pull/9"
	st.sessions[workerID] = working(workerID)

	st.comments[prURL] = []domain.PullRequestComment{
		{
			ThreadID:         "thread-1",
			ID:               "comment-1",
			Author:           "reviewer",
			File:             "a.go",
			Line:             10,
			Body:             "Fix finding one.",
			Resolved:         false,
			AutoInjectReview: true,
		},
	}

	err := m.ApplyPRObservation(
		context.Background(),
		workerID,
		ports.PRObservation{
			Fetched: true,
			URL:     prURL,
		},
	)
	if err != nil {
		t.Fatalf("first ApplyPRObservation: %v", err)
	}

	if got := len(msg.msgs); got != 1 {
		t.Fatalf("first review thread should wake worker once; got %d messages: %#v", got, msg.msgs)
	}

	st.comments[prURL] = append(
		st.comments[prURL],
		domain.PullRequestComment{
			ThreadID:         "thread-2",
			ID:               "comment-2",
			Author:           "reviewer",
			File:             "b.go",
			Line:             20,
			Body:             "Fix finding two.",
			Resolved:         false,
			AutoInjectReview: true,
		},
	)

	err = m.ApplyPRObservation(
		context.Background(),
		workerID,
		ports.PRObservation{
			Fetched: true,
			URL:     prURL,
		},
	)
	if err != nil {
		t.Fatalf("second ApplyPRObservation: %v", err)
	}

	if got := len(msg.msgs); got != 2 {
		t.Fatalf(
			"a genuinely new review thread must still wake worker; got %d messages: %#v",
			got,
			msg.msgs,
		)
	}
}

func TestReviewBatchSuppressesInlineCommentFromSameSCMReview(t *testing.T) {
	m, st, msg := newManager()

	workerID := domain.SessionID("mer-inline-cross-lane")
	prURL := "https://github.com/acme/repo/pull/10"
	const reviewID = "4949207965"
	reviewURL := prURL + "#pullrequestreview-" + reviewID

	st.sessions[workerID] = working(workerID)

	outcome, err := m.ApplyReviewBatch(
		context.Background(),
		workerID,
		"batch-inline-cross-lane",
		[]ReviewResult{
			{
				RunID:          "run-inline-cross-lane",
				BatchID:        "batch-inline-cross-lane",
				WorkerID:       workerID,
				PRURL:          prURL,
				TargetSHA:      "deadbeef",
				Verdict:        domain.VerdictChangesRequested,
				Body:           "Remove extra.txt",
				GithubReviewID: reviewID,
			},
		},
	)
	if err != nil {
		t.Fatalf("ApplyReviewBatch: %v", err)
	}
	if outcome != ReviewDeliverySent {
		t.Fatalf("ApplyReviewBatch outcome = %q, want %q", outcome, ReviewDeliverySent)
	}
	if got := len(msg.msgs); got != 1 {
		t.Fatalf(
			"internal reviewer should wake worker exactly once; got %d messages: %#v",
			got,
			msg.msgs,
		)
	}

	// The SCM observer has already persisted the durable comment before
	// lifecycle runs. Durable comments intentionally do not carry ReviewURL.
	st.comments[prURL] = []domain.PullRequestComment{
		{
			ThreadID:         "thread-1",
			ID:               "comment-1",
			Author:           "reviewer",
			File:             "extra.txt",
			Line:             1,
			Body:             "Remove this file.",
			URL:              prURL + "#discussion_r1",
			Resolved:         false,
			AutoInjectReview: true,
		},
	}

	// The live SCM observation DOES know which submitted review owns the
	// comment. Lifecycle must use that transient correlation even though it
	// reloads the durable comment rows before reacting.
	err = m.ApplySCMObservation(
		context.Background(),
		workerID,
		ports.SCMObservation{
			Fetched: true,
			PR: ports.SCMPRObservation{
				URL: prURL,
			},
			Review: ports.SCMReviewObservation{
				Decision: string(domain.ReviewChangesRequest),
				Threads: []ports.SCMReviewThreadObservation{
					{
						ID:       "thread-1",
						Path:     "extra.txt",
						Line:     1,
						Resolved: false,
						Comments: []ports.SCMReviewCommentObservation{
							{
								ID:        "comment-1",
								Author:    "reviewer",
								Body:      "Remove this file.",
								URL:       prURL + "#discussion_r1",
								ReviewURL: reviewURL,
							},
						},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("ApplySCMObservation: %v", err)
	}

	if got := len(msg.msgs); got != 1 {
		t.Fatalf(
			"inline comment from a GitHub review already delivered by the internal reviewer must not wake worker again; got %d messages: %#v",
			got,
			msg.msgs,
		)
	}
}

func TestInlineReviewCoverageSuppressesSameThreadReplyAfterRestart(t *testing.T) {
	m1, st, msg := newManager()

	workerID := domain.SessionID("mer-inline-restart")
	prURL := "https://github.com/acme/repo/pull/11"
	const reviewID = "4949207965"
	reviewURL := prURL + "#pullrequestreview-" + reviewID

	st.sessions[workerID] = working(workerID)

	// AO's internal reviewer delivers the finding first.
	outcome, err := m1.ApplyReviewBatch(
		context.Background(),
		workerID,
		"batch-inline-restart",
		[]ReviewResult{
			{
				RunID:          "run-inline-restart",
				BatchID:        "batch-inline-restart",
				WorkerID:       workerID,
				PRURL:          prURL,
				TargetSHA:      "deadbeef",
				Verdict:        domain.VerdictChangesRequested,
				Body:           "Remove extra.txt",
				GithubReviewID: reviewID,
			},
		},
	)
	if err != nil {
		t.Fatalf("ApplyReviewBatch: %v", err)
	}
	if outcome != ReviewDeliverySent {
		t.Fatalf("ApplyReviewBatch outcome = %q, want %q", outcome, ReviewDeliverySent)
	}
	if got := len(msg.msgs); got != 1 {
		t.Fatalf("internal review messages = %d, want 1", got)
	}

	reviewerComment := domain.PullRequestComment{
		ThreadID:         "thread-1",
		ID:               "comment-reviewer",
		Author:           "reviewer",
		File:             "extra.txt",
		Line:             1,
		Body:             "Remove this file.",
		URL:              prURL + "#discussion_r1",
		Resolved:         false,
		AutoInjectReview: true,
	}

	// SCM later observes the inline comment belonging to that same review.
	// It must be covered by the already-delivered internal review.
	st.comments[prURL] = []domain.PullRequestComment{reviewerComment}

	err = m1.ApplySCMObservation(
		context.Background(),
		workerID,
		ports.SCMObservation{
			Fetched: true,
			PR: ports.SCMPRObservation{
				URL: prURL,
			},
			Review: ports.SCMReviewObservation{
				Decision: string(domain.ReviewChangesRequest),
				Threads: []ports.SCMReviewThreadObservation{
					{
						ID:       "thread-1",
						Path:     "extra.txt",
						Line:     1,
						Resolved: false,
						Comments: []ports.SCMReviewCommentObservation{
							{
								ID:        "comment-reviewer",
								Author:    "reviewer",
								Body:      "Remove this file.",
								URL:       reviewerComment.URL,
								ReviewURL: reviewURL,
							},
						},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("initial ApplySCMObservation: %v", err)
	}
	if got := len(msg.msgs); got != 1 {
		t.Fatalf(
			"covered inline comment re-woke worker before restart; got %d messages: %#v",
			got,
			msg.msgs,
		)
	}

	// Simulate a daemon restart: same durable store, fresh Manager/reaction state.
	m2 := New(st, msg)

	workerReply := domain.PullRequestComment{
		ThreadID:         "thread-1",
		ID:               "comment-worker-reply",
		Author:           "worker",
		File:             "extra.txt",
		Line:             1,
		Body:             "Addressed in deadbeef.",
		URL:              prURL + "#discussion_r2",
		Resolved:         false,
		AutoInjectReview: true,
	}

	st.comments[prURL] = []domain.PullRequestComment{
		reviewerComment,
		workerReply,
	}

	// The reviewer comment still carries its parent review correlation. The
	// worker reply deliberately does not: suppression of that reply must come
	// from the persisted thread-level dedup marker.
	err = m2.ApplySCMObservation(
		context.Background(),
		workerID,
		ports.SCMObservation{
			Fetched: true,
			PR: ports.SCMPRObservation{
				URL: prURL,
			},
			Review: ports.SCMReviewObservation{
				Decision: string(domain.ReviewChangesRequest),
				Threads: []ports.SCMReviewThreadObservation{
					{
						ID:       "thread-1",
						Path:     "extra.txt",
						Line:     1,
						Resolved: false,
						Comments: []ports.SCMReviewCommentObservation{
							{
								ID:        "comment-reviewer",
								Author:    "reviewer",
								Body:      "Remove this file.",
								URL:       reviewerComment.URL,
								ReviewURL: reviewURL,
							},
							{
								ID:     "comment-worker-reply",
								Author: "worker",
								Body:   "Addressed in deadbeef.",
								URL:    workerReply.URL,
							},
						},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("post-restart ApplySCMObservation: %v", err)
	}

	if got := len(msg.msgs); got != 1 {
		t.Fatalf(
			"same-thread worker reply after restart must remain covered; got %d messages: %#v",
			got,
			msg.msgs,
		)
	}
}
