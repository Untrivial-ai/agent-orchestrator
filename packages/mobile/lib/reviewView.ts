import type { PRReviewState, ReviewRun } from "./api";

export function reviewForPullRequest(
	reviews: PRReviewState[],
	prUrl: string | undefined,
	prNumber: number | undefined,
): PRReviewState | undefined {
	if (prUrl) {
		const exact = reviews.find((review) => review.prUrl === prUrl);
		if (exact) return exact;
	}
	return prNumber ? reviews.find((review) => review.prNumber === prNumber) : undefined;
}

export function reviewStatusLabel(status: PRReviewState["status"]): string {
	switch (status) {
		case "running": return "Review in progress";
		case "up_to_date": return "Reviewed";
		case "changes_requested": return "Changes requested";
		case "ineligible": return "Review unavailable";
		default: return "Needs review";
	}
}

export function reviewVerdictLabel(run: ReviewRun): string {
	if (run.status === "running") return "Reviewing";
	if (run.status === "failed") return "Review failed";
	if (run.status === "cancelled") return "Review cancelled";
	if (run.verdict === "approved") return "Approved";
	if (run.verdict === "changes_requested") return "Changes requested";
	return "No verdict";
}

export function shortCommit(sha: string): string {
	return sha.slice(0, 8);
}
