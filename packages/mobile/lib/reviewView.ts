import type { DashboardSession, PRReviewState, ReviewRun, SessionReviews } from "./api";

export function reviewRouteForSession(session: DashboardSession) {
	const pr = session.prs?.[0] ?? session.pr;
	if (!pr) return undefined;
	return {
		pathname: "/review/[sessionId]" as const,
		params: { sessionId: session.id, prNumber: String(pr.number), prUrl: pr.url },
	};
}

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

export function reviewStatusVisual(status: PRReviewState["status"]): {
	icon: "alert-circle" | "check-circle" | "clock" | "loader";
	tone: "amber" | "blue" | "green" | "muted";
} {
	switch (status) {
		case "running": return { icon: "loader", tone: "blue" };
		case "up_to_date": return { icon: "check-circle", tone: "green" };
		case "changes_requested": return { icon: "alert-circle", tone: "amber" };
		case "ineligible": return { icon: "alert-circle", tone: "muted" };
		default: return { icon: "clock", tone: "muted" };
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

export type ReviewPrimaryAction = "start" | "cancel" | "review_again" | "none";

export function reviewPrimaryAction(review: PRReviewState): ReviewPrimaryAction {
	if (review.status === "running") return "cancel";
	if (review.status === "ineligible") return "none";
	if (review.status === "needs_review") return "start";
	return "review_again";
}

export function reviewBatchAction(review: PRReviewState, reviews: PRReviewState[]): ReviewPrimaryAction {
	if (reviews.some((candidate) => candidate.status === "running")) return "cancel";
	return reviewPrimaryAction(review);
}

export function reviewerDestination(data: SessionReviews, review: PRReviewState, sessionId: string) {
	const surface = data.reviewerSurface;
	if (!surface) return undefined;
	if (surface.mode === "chat") {
		return { pathname: "/reviewer/[reviewId]" as const, params: { reviewId: surface.reviewId, sessionId, title: review.title } };
	}
	const handleId = surface.handleId || data.reviewerHandleId;
	if (!handleId) return undefined;
	return { pathname: "/shell/[handleId]" as const, params: { handleId, sessionId, title: `Review · PR #${review.prNumber}` } };
}

export function reviewPrimaryActionLabel(action: ReviewPrimaryAction, multiple = false): string {
	switch (action) {
		case "start": return multiple ? "Start all reviews" : "Start review";
		case "cancel": return multiple ? "Cancel running reviews" : "Cancel review";
		case "review_again": return multiple ? "Review all again" : "Review again";
		default: return "";
	}
}
