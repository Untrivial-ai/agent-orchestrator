import { describe, expect, it } from "vitest";

import {
	AO_REVIEW_RUN_STATUSES,
	AO_REVIEW_STATES,
	AO_REVIEW_VERDICTS,
	CI_STATES,
	MERGEABILITY_STATES,
	PULL_REQUEST_CHECK_STATUSES,
	PULL_REQUEST_STATES,
	REVIEW_DECISIONS,
	type PullRequestSummary,
	type SessionReviewState,
} from "./scm-models";

describe("raw SCM models", () => {
	it("keeps the shared normalized vocabularies stable", () => {
		expect(PULL_REQUEST_STATES).toEqual(["draft", "open", "merged", "closed"]);
		expect(CI_STATES).toEqual(["unknown", "pending", "passing", "failing"]);
		expect(PULL_REQUEST_CHECK_STATUSES).toEqual([
			"unknown",
			"queued",
			"in_progress",
			"passed",
			"failed",
			"skipped",
			"cancelled",
		]);
		expect(REVIEW_DECISIONS).toEqual(["none", "approved", "changes_requested", "review_required"]);
		expect(MERGEABILITY_STATES).toEqual(["unknown", "mergeable", "conflicting", "blocked", "unstable"]);
		expect(AO_REVIEW_RUN_STATUSES).toEqual(["running", "complete", "delivered", "failed", "cancelled"]);
		expect(AO_REVIEW_VERDICTS).toEqual(["", "approved", "changes_requested"]);
		expect(AO_REVIEW_STATES).toEqual([
			"needs_review",
			"running",
			"up_to_date",
			"changes_requested",
			"ineligible",
		]);
	});

	it("represents provider observations separately from AO review execution", () => {
		const pullRequest = {
			url: "github://o/r/pull/7",
			number: 7,
			title: "Cloud contract",
			state: "open",
			provider: "github",
			repository: "o/r",
			author: "alice",
			sourceBranch: "feat/cloud",
			targetBranch: "main",
			headSha: "abc123",
			additions: 12,
			deletions: 3,
			changedFiles: 2,
			ci: { state: "failing", failingChecks: [] },
			review: {
				decision: "changes_requested",
				hasUnresolvedHumanComments: true,
				unresolvedBy: [],
				reviews: [],
			},
			mergeability: {
				state: "blocked",
				reasons: ["ci_failing"],
				pullRequestUrl: "https://github.com/o/r/pull/7",
				conflictFiles: [],
			},
			updatedAt: "2026-08-09T12:00:00Z",
			observedAt: "2026-08-09T12:00:01Z",
			ciObservedAt: "2026-08-09T12:00:02Z",
			reviewObservedAt: "2026-08-09T12:00:03Z",
		} satisfies PullRequestSummary;
		const reviewState = {
			sessionId: "session-1",
			reviews: [
				{
					pullRequestUrl: pullRequest.url,
					pullRequestNumber: pullRequest.number,
					title: pullRequest.title,
					targetSha: pullRequest.headSha,
					staleTargetSha: "previous123",
					status: "needs_review",
				},
			],
			runs: [],
		} satisfies SessionReviewState;

		expect(reviewState.reviews[0]?.staleTargetSha).toBe("previous123");
	});
});
