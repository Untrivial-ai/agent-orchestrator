import { describe, expect, it } from "vitest";
import type { PRReviewState, ReviewRun } from "./api";
import { reviewBatchAction, reviewerDestination, reviewForPullRequest, reviewPrimaryAction, reviewPrimaryActionLabel, reviewStatusLabel, reviewStatusVisual, reviewVerdictLabel, shortCommit } from "./reviewView";

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

	it("does not present pending or unavailable reviews as successful", () => {
		expect(reviewStatusVisual("needs_review")).toEqual({ icon: "clock", tone: "muted" });
		expect(reviewStatusVisual("ineligible")).toEqual({ icon: "alert-circle", tone: "muted" });
		expect(reviewStatusVisual("up_to_date")).toEqual({ icon: "check-circle", tone: "green" });
	});

	it("only offers actions supported by the current review state", () => {
		expect(reviewPrimaryAction(state({ status: "needs_review" }))).toBe("start");
		expect(reviewPrimaryAction(state({ status: "running" }))).toBe("cancel");
		expect(reviewPrimaryAction(state({ status: "up_to_date" }))).toBe("review_again");
		expect(reviewPrimaryAction(state({ status: "ineligible" }))).toBe("none");
		expect(reviewPrimaryActionLabel("review_again")).toBe("Review again");
		expect(reviewPrimaryActionLabel("cancel", true)).toBe("Cancel running reviews");
	});

	it("prioritizes cancelling an active session batch over starting another", () => {
		const selected = state({ status: "needs_review" });
		expect(reviewBatchAction(selected, [selected, state({ prUrl: "other", status: "running" })])).toBe("cancel");
	});

	it("routes each reviewer surface to its native mobile experience", () => {
		const review = state();
		const base = { reviewerHandleId: "fallback", reviews: [review], runs: [] };
		expect(reviewerDestination({ ...base, reviewerSurface: { mode: "chat", reviewId: "review-1", harness: "codex" } }, review, "worker-1")).toMatchObject({ pathname: "/reviewer/[reviewId]", params: { reviewId: "review-1" } });
		expect(reviewerDestination({ ...base, reviewerSurface: { mode: "tui", reviewId: "review-1", harness: "codex" } }, review, "worker-1")).toMatchObject({ pathname: "/shell/[handleId]", params: { handleId: "fallback" } });
		expect(reviewerDestination(base, review, "worker-1")).toBeUndefined();
	});
});
