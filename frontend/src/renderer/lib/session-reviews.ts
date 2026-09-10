import { queryOptions } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { appI18n } from "../i18n";
import { sortedPRs, type WorkspaceSession } from "../types/workspace";
import { apiClient, apiErrorMessage } from "./api-client";
import { usesPreviewWorkspaceData as usePreviewData } from "./preview-mode";

export type PRReviewState = components["schemas"]["PRReviewState"];
export type ReviewsResponse = components["schemas"]["ListReviewsResponse"];
export type ReviewRunFacts = components["schemas"]["ReviewRun"];

/**
 * Shared query options for a session's AO review states. The query key is the
 * single source of truth for review data — the SessionInspector Reviews tab
 * and the command palette both subscribe through this, so React Query shares
 * one cache entry per session and one fetch path (including the preview mock).
 */
export function sessionReviewsQueryOptions(session: WorkspaceSession, enabled: boolean, staleTime?: number) {
	return queryOptions({
		queryKey: ["session-reviews", session.id] as const,
		enabled,
		...(staleTime !== undefined ? { staleTime } : {}),
		refetchInterval: (query) => {
			const data = query.state.data as ReviewsResponse | undefined;
			const reviews = data?.reviews ?? [];
			return reviews.some((review) => review.status === "running") ? 2500 : false;
		},
		queryFn: async () => {
			if (usePreviewData) return mockReviewsResponse(session);
			const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/reviews", {
				params: { path: { sessionId: session.id } },
			});
			if (error) throw new Error(apiErrorMessage(error, "Unable to load reviews"));
			return data ?? ({ reviewerHandleId: "", reviews: [], runs: [] } satisfies ReviewsResponse);
		},
	});
}

/** Review states for a session's active PRs. Open and draft PRs can be reviewed; merged/closed PRs cannot. */
export function openReviewStatesFor(session: WorkspaceSession, reviewStates: PRReviewState[]): PRReviewState[] {
	const openPRURLs = new Set(
		sortedPRs(session)
			.filter((pr) => pr.state === "open" || pr.state === "draft")
			.map((pr) => pr.url),
	);
	return reviewStates.filter((reviewState) => openPRURLs.has(reviewState.prUrl));
}

export function reviewIsRunning(openReviewStates: PRReviewState[]): boolean {
	return openReviewStates.some((reviewState) => reviewState.status === "running");
}

export function reviewRunDisabled(openReviewStates: PRReviewState[], isTriggering: boolean): boolean {
	return (
		isTriggering ||
		openReviewStates.length === 0 ||
		openReviewStates.every((reviewState) => reviewState.status === "ineligible")
	);
}

export function reviewSessionRunAction(reviewStates: PRReviewState[], isTriggering: boolean): string {
	if (isTriggering || reviewStates.some((reviewState) => reviewState.status === "running")) {
		return appI18n.t("inspector.review.reviewing");
	}
	if (reviewStates.some((reviewState) => reviewState.status === "needs_review")) {
		return appI18n.t("inspector.review.runLatest");
	}
	if (reviewStates.some((reviewState) => reviewState.status === "changes_requested" || reviewState.latestRun)) {
		return appI18n.t("inspector.review.rerun");
	}
	return appI18n.t("inspector.review.run");
}

// Preview-only pins so the reviews section can be seen mid-run and with a verdict
// left behind by an earlier commit — neither follows from a PR's review decision.
const MOCK_RUNNING_PR = 322;
const MOCK_STALE_PR = 324;

function mockReviewsResponse(session: WorkspaceSession): ReviewsResponse {
	const states: PRReviewState[] = sortedPRs(session).map((pr, index) => {
		const targetSha = `demo${pr.number}${index}`;
		const reviewedAt = new Date(Date.now() - (index + 1) * 11 * 60 * 1000).toISOString();
		const latestRun =
			pr.review === "approved" || pr.review === "changes_requested"
				? {
						autoInjectReview: session.autoInjectReview ?? true,
						batchId: `demo-batch-${session.id}`,
						body:
							pr.review === "approved"
								? "Demo review **approved** the README screenshot flow.\n\n- Layout is stable\n- Browser preview opens cleanly"
								: "Demo review found **polish feedback** for the terminal presentation.\n\n- Tighten toolbar density\n- Recheck contrast",
						createdAt: reviewedAt,
						githubReviewId: `${pr.number}01`,
						harness: "codex",
						id: `demo-review-run-${pr.number}`,
						prUrl: pr.url,
						reviewId: `demo-review-${pr.number}`,
						sessionId: session.id,
						status: "delivered",
						targetSha,
						triggerSource: "manual" as const,
						verdict: pr.review === "approved" ? "approved" : "changes_requested",
					}
				: undefined;
	const run = (over: Record<string, unknown>) => ({
		autoInjectReview: session.autoInjectReview ?? true,
		batchId: `demo-batch-${session.id}`,
		body: "",
		triggerSource: "manual" as const,
			createdAt: reviewedAt,
			githubReviewId: "",
			harness: "codex",
			id: `demo-review-run-${pr.number}`,
			prUrl: pr.url,
			reviewId: `demo-review-${pr.number}`,
			sessionId: session.id,
			status: "complete",
			targetSha,
			verdict: "",
			...over,
		});
		// A couple of PRs are pinned to states the review decision alone cannot
		// produce, so the preview shows every shape the panel can render.
		if (pr.number === MOCK_RUNNING_PR) {
			return {
				latestRun: run({ status: "running", id: `demo-review-run-${pr.number}-live` }),
				prNumber: pr.number,
				prUrl: pr.url,
				status: "running",
				targetSha,
				title: mockReviewTitle(pr.number),
			};
		}
		if (pr.number === MOCK_STALE_PR) {
			// Reviewed, then a new commit landed: the verdict is about code that
			// has since changed, so the panel demotes it to "Previous".
			return {
				previousRun: run({
					status: "delivered",
					verdict: "changes_requested",
					githubReviewId: `${pr.number}09`,
					body: "Demo review asked for a tighter activity sample before the last commit.",
					targetSha: `${targetSha}-old`,
				}),
				prNumber: pr.number,
				prUrl: pr.url,
				status: "needs_review",
				targetSha,
				title: mockReviewTitle(pr.number),
			};
		}
		return {
			latestRun,
			prNumber: pr.number,
			prUrl: pr.url,
			status:
				pr.review === "approved"
					? "up_to_date"
					: pr.review === "changes_requested"
						? "changes_requested"
						: pr.state === "draft"
							? "ineligible"
							: "needs_review",
			targetSha,
			title: mockReviewTitle(pr.number),
		};
	});
	// Earlier passes, so the history control has something to open. Two reviewers
	// on the same PR is the case the control exists for.
	const runs: ReviewRunFacts[] = states.flatMap((state) => {
		const base = {
			autoInjectReview: session.autoInjectReview ?? true,
			batchId: `demo-batch-${session.id}`,
			githubReviewId: "",
			prUrl: state.prUrl,
			triggerSource: "manual" as const,
			reviewId: `demo-review-${state.prNumber}`,
			sessionId: session.id,
			status: "delivered",
			targetSha: state.targetSha,
		};
		return [
			...(state.latestRun?.body?.trim() ? [state.latestRun] : []),
			{
				...base,
				id: `demo-hist-${state.prNumber}-a`,
				harness: "codex",
				verdict: "changes_requested",
				body: "Earlier codex pass asked for tests around the discount edge cases.",
				createdAt: new Date(Date.now() - 55 * 60 * 1000).toISOString(),
			},
			{
				...base,
				id: `demo-hist-${state.prNumber}-b`,
				harness: "claude-code",
				verdict: "approved",
				body: "Earlier claude-code pass found nothing blocking.",
				createdAt: new Date(Date.now() - 95 * 60 * 1000).toISOString(),
			},
		];
	});
	return { reviewerHandleId: `${session.id}-reviewer`, reviews: states, runs };
}

function mockReviewTitle(prNumber: number): string {
	switch (prNumber) {
		case 319:
			return "Browser preview rail renders inside AO";
		case 320:
			return "Review tab keeps stacked PR rows visible";
		case 321:
			return "Draft child PR waits for parent review";
		case 318:
			return "Terminal polish feedback";
		case 323:
			return "README screenshot assets ready";
		default:
			return `Demo pull request ${prNumber}`;
	}
}
