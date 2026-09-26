package review

import (
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReviewTextsSinglePRPrompt(t *testing.T) {
	prompt, system := reviewTexts(launchSpec())
	for _, want := range []string{
		"Review the requested pull request(s) for worker session mer-1.",
		"* 1. https://github.com/o/r/pull/1 (head commit sha1, run run-1)",
		"Complete every review task in the queue autonomously",
		"gh pr view <url> --json title,body,baseRefName,headRefOid,files",
		"gh pr diff <url>",
		"Use changes_requested only when at least one finding would block a merge",
		"Verdict: changes requested",
		"\"line\" must be a line on the new side of the PR diff",
		"retry once without \"comments\"",
		"JSON-escape the review text",
		"do not use a heredoc",
		"printf '%s'",
		"ao review submit --session mer-1 --reviews -",
		`"reviews": [`,
		"still include that run with an empty githubReviewId",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{
		"AO created",
		"Do not ask the user whether to continue to the next PR",
		"For each PR below",
		"Earlier review of",
		"\t",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("single-PR prompt should not contain %q:\n%s", unwanted, prompt)
		}
	}
	// The failure rule is stated once, in the bookkeeping step.
	if got := strings.Count(prompt, "empty githubReviewId"); got != 1 {
		t.Fatalf("failure rule stated %d times, want 1:\n%s", got, prompt)
	}
	for _, want := range []string{
		"Code reviewer role",
		"Prefer a few high-confidence findings over nitpicks",
		"untrusted evidence",
		"This is a read-only role",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, system)
		}
	}
	// Posting mechanics live in the task prompt only.
	if strings.Contains(system, "Post your review") {
		t.Fatalf("system prompt duplicates the posting step:\n%s", system)
	}
}

func TestReviewTextsIncludesMultiPRQueue(t *testing.T) {
	spec := launchSpec()
	spec.RunID = "run-2"
	spec.PRURL = "https://github.com/o/r/pull/2"
	spec.TargetSHA = "sha2"
	spec.ReviewIndex = 1
	spec.ReviewQueue = []ports.ReviewTask{
		{RunID: "run-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1"},
		{RunID: "run-2", PRURL: "https://github.com/o/r/pull/2", TargetSHA: "sha2"},
	}

	prompt, _ := reviewTexts(spec)
	for _, want := range []string{
		"AO created 2 review tasks",
		"Review every queued PR, then submit all results together",
		"Complete every review task in the queue autonomously",
		"Do not ask the user whether to continue to the next PR",
		"* 1. https://github.com/o/r/pull/1 (head commit sha1, run run-1)",
		"* 2. https://github.com/o/r/pull/2 (head commit sha2, run run-2)",
		"Record AO's bookkeeping for every PR in the queue with one command",
		"printf '%s'",
		"do not use a heredoc",
		"ao review submit --session mer-1 --reviews -",
		`"reviews": [`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestReviewTextsSurfacesLatestPriorVerdictForRereview(t *testing.T) {
	spec := launchSpec()
	spec.TargetSHA = "sha3"
	older := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	spec.PreviousRuns = []domain.ReviewRun{
		// Newest completed verdict on an earlier commit: the one to surface.
		{PRURL: spec.PRURL, TargetSHA: "sha2", Status: domain.ReviewRunComplete, Verdict: domain.VerdictChangesRequested, Body: "Verdict: changes requested\n1. auth.go:12 blocking: nil deref", CreatedAt: older.Add(2 * time.Hour)},
		{PRURL: spec.PRURL, TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved, Body: "first pass", CreatedAt: older},
		// Same commit as the one under review: not a prior review.
		{PRURL: spec.PRURL, TargetSHA: "sha3", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved, Body: "same commit", CreatedAt: older.Add(3 * time.Hour)},
		// Never produced a verdict.
		{PRURL: spec.PRURL, TargetSHA: "sha2", Status: domain.ReviewRunFailed, Verdict: domain.VerdictNone, Body: "launch failed", CreatedAt: older.Add(4 * time.Hour)},
		// Another PR.
		{PRURL: "https://github.com/o/r/pull/9", TargetSHA: "x", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved, Body: "other pr", CreatedAt: older.Add(5 * time.Hour)},
	}

	prompt, _ := reviewTexts(spec)
	for _, want := range []string{
		"Earlier review of https://github.com/o/r/pull/1 at commit sha2 recorded verdict changes_requested.",
		"  Body: Verdict: changes requested 1. auth.go:12 blocking: nil deref",
		"State which of those findings the new commit addresses and which remain",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"first pass", "same commit", "launch failed", "other pr"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt should not surface %q:\n%s", unwanted, prompt)
		}
	}
	// The history block sits with the queue, before the numbered steps.
	if strings.Index(prompt, "Earlier review of") > strings.Index(prompt, "Do these steps in order") {
		t.Fatalf("prior review text should precede the steps:\n%s", prompt)
	}
}

func TestReviewTextsOmitsPriorVerdictWithoutCompletedRun(t *testing.T) {
	spec := launchSpec()
	spec.PreviousRuns = []domain.ReviewRun{
		{PRURL: spec.PRURL, TargetSHA: "sha0", Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone},
	}
	prompt, _ := reviewTexts(spec)
	if strings.Contains(prompt, "Earlier review of") {
		t.Fatalf("prompt should not surface an unfinished run:\n%s", prompt)
	}
}
