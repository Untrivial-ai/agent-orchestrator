import type { PRReviewCommentLink, PRReviewEntry, ReviewRun } from "./api";

export function reviewRunsForPullRequest(runs: ReviewRun[], prUrl: string): ReviewRun[] {
	const byId = new Map<string, ReviewRun>();
	for (const run of runs) if (run.prUrl === prUrl && !byId.has(run.id)) byId.set(run.id, run);
	return [...byId.values()].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
}

export function reviewRunUrl(run: ReviewRun): string | undefined {
	if (!run.githubReviewId || !run.prUrl) return undefined;
	return `${run.prUrl}#pullrequestreview-${run.githubReviewId}`;
}

export function formatReviewSummaryMessage(run: ReviewRun): string {
	const url = reviewRunUrl(run);
	return [
		`Review summary (${run.harness || "reviewer"}, commit ${run.targetSha.slice(0, 8)}):`,
		run.body?.trim() || "No written findings.",
		url ? `Review URL: ${url}` : undefined,
	].filter(Boolean).join("\n\n");
}

export function formatExternalReviewMessage(review: PRReviewEntry, prUrl: string): string {
	return [
		`GitHub review from ${review.reviewerId} (${review.verdict.replaceAll("_", " ")}):`,
		review.body?.trim() || "No written summary.",
		`Review URL: ${review.reviewUrl || prUrl}`,
	].join("\n\n");
}

export function formatInlineReviewCommentMessage(comment: PRReviewCommentLink, reviewerId?: string): string {
	const location = comment.file ? `${comment.file}${comment.line ? `:${comment.line}` : ""}` : "inline review comment";
	return [
		`Review comment${reviewerId ? ` from ${reviewerId}` : ""} (${location}):`,
		comment.body?.trim() || "No comment text.",
		comment.url ? `Comment URL: ${comment.url}` : undefined,
	].filter(Boolean).join("\n\n");
}
