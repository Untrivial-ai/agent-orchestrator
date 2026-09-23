import { describe, expect, it } from "vitest";
import type { ReviewRun } from "./api";
import { formatExternalReviewMessage, formatInlineReviewCommentMessage, formatReviewSummaryMessage, reviewRunsForPullRequest, reviewRunUrl } from "./reviewFeedback";

const run = (id: string, createdAt: string, prUrl = "https://github.com/ao/repo/pull/42"): ReviewRun => ({
	id, reviewId: "review", sessionId: "session", batchId: "batch", harness: "codex", triggerSource: "auto", prUrl,
	targetSha: "abcdef123456", status: "delivered", verdict: "approved", body: "Looks good.", githubReviewId: "99", createdAt,
	autoInjectReview: true,
});

describe("mobile review feedback", () => {
	it("returns every run for the selected PR in newest-first order", () => {
		expect(reviewRunsForPullRequest([run("old", "2026-01-01"), run("other", "2026-03-01", "https://example.com/2"), run("new", "2026-02-01")], "https://github.com/ao/repo/pull/42").map((item) => item.id)).toEqual(["new", "old"]);
	});

	it("builds the exact GitHub review URL and worker summary", () => {
		expect(reviewRunUrl(run("one", "2026-01-01"))).toBe("https://github.com/ao/repo/pull/42#pullrequestreview-99");
		expect(formatReviewSummaryMessage(run("one", "2026-01-01"))).toContain("Review URL: https://github.com/ao/repo/pull/42#pullrequestreview-99");
	});

	it("formats external summaries and inline comments for the worker", () => {
		expect(formatExternalReviewMessage({ reviewerId: "maya", verdict: "changes_requested", body: "Fix this.", submittedAt: "now", autoInjectReview: false }, "https://github.com/ao/repo/pull/42")).toContain("GitHub review from maya");
		expect(formatInlineReviewCommentMessage({ file: "app.ts", line: 12, body: "Cover this.", url: "https://example.com/c", autoInjectReview: false }, "maya")).toContain("app.ts:12");
	});
});
