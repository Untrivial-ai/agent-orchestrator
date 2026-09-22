import { describe, expect, it } from "vitest";
import type { PRReviewState, ReviewRun } from "./api";
import { reviewForPullRequest, reviewStatusLabel, reviewVerdictLabel, shortCommit } from "./reviewView";

const run = (over: Partial<ReviewRun> = {}): ReviewRun => ({
	id: "run-1", reviewId: "review-1", sessionId: "worker-1", batchId: "", harness: "codex",
	triggerSource: "manual", prUrl: "https://github.com/acme/repo/pull/12", targetSha: "abcdef123456",
	status: "complete", verdict: "approved", body: "Looks good", githubReviewId: "", createdAt: "2026-09-22T00:00:00Z",
	autoInjectReview: true, ...over,
});

const state = (over: Partial<PRReviewState> = {}): PRReviewState => ({
	prUrl: "https://github.com/acme/repo/pull/12", prNumber: 12, title: "Improve mobile review",
	targetSha: "new-head", status: "needs_review", previousRun: run({ targetSha: "old-head" }), ...over,
});

describe("mobile review presentation", () => {
	it("matches the exact PR URL before falling back to its number", () => {
		const reviews = [state({ prUrl: "other", prNumber: 12 }), state()];
		expect(reviewForPullRequest(reviews, state().prUrl, 12)?.prUrl).toBe(state().prUrl);
	});

	it("keeps an earlier verdict in previousRun instead of treating it as current", () => {
		const review = state();
		expect(review.latestRun).toBeUndefined();
		expect(review.previousRun?.verdict).toBe("approved");
		expect(reviewStatusLabel(review.status)).toBe("Needs review");
	});

	it("labels terminal outcomes and shortens commit ids", () => {
		expect(reviewVerdictLabel(run({ verdict: "changes_requested" }))).toBe("Changes requested");
		expect(reviewVerdictLabel(run({ status: "cancelled", verdict: "" }))).toBe("Review cancelled");
		expect(shortCommit("abcdef123456")).toBe("abcdef12");
	});
});
