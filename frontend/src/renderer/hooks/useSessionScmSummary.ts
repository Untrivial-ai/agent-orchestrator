import { type QueryClient, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import type { CloudCpPullRequestSummary } from "../lib/cloud-cp";
import type { WorkspaceSession } from "../types/workspace";
import { createRendererCloudCpClient } from "./useCloudCp";
import { settingsQueryKey, type Settings } from "./useSettings";

export type SessionPRSummary = components["schemas"]["SessionPRSummary"];

export const sessionScmSummaryQueryKey = (sessionId?: string) =>
	sessionId ? (["session-scm-summary", sessionId] as const) : (["session-scm-summary"] as const);

// Cloud PR changes arrive without a daemon event stream to invalidate on, so
// poll on the same cadence as the local workspace list.
const CLOUD_SCM_SUMMARY_REFETCH_MS = 15_000;

export async function fetchSessionScmSummary(sessionId: string): Promise<SessionPRSummary[]> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr", {
		params: { path: { sessionId } },
	});
	if (error) throw error;
	return data?.prs ?? [];
}

/**
 * Maps the control plane's pull request summary onto the daemon's shape. The
 * control plane does not track review threads, failing-check logs, or
 * auto-inject toggles yet, so those take their empty defaults.
 */
export function toSessionPRSummaryFromCloud(pr: CloudCpPullRequestSummary): SessionPRSummary {
	return {
		url: pr.url,
		htmlUrl: pr.htmlUrl,
		number: pr.number,
		title: pr.title,
		state: pr.state,
		provider: pr.provider === "gitlab" ? "gitlab" : "github",
		repo: pr.repository,
		author: pr.author,
		sourceBranch: pr.sourceBranch,
		targetBranch: pr.targetBranch,
		headSha: pr.headSha,
		additions: pr.additions,
		deletions: pr.deletions,
		changedFiles: pr.changedFiles,
		ci: {
			state: pr.ci.state as SessionPRSummary["ci"]["state"],
			autoInjectCI: false,
			failingChecks: pr.ci.failingChecks.map((check) => ({
				name: check.name,
				status: check.conclusion === "cancelled" ? "cancelled" : "failed",
				conclusion: check.conclusion,
				url: check.url || undefined,
			})),
		},
		review: {
			decision: pr.review.decision as SessionPRSummary["review"]["decision"],
			hasUnresolvedHumanComments: pr.review.hasUnresolvedHumanComments,
			unresolvedBy: [],
		},
		mergeability: {
			state: pr.mergeability.state as SessionPRSummary["mergeability"]["state"],
			reasons: pr.mergeability.reasons,
			prUrl: pr.mergeability.pullRequestUrl,
		},
		stateChangedAt: pr.stateChangedAt,
		createdAt: pr.createdAt,
		updatedAt: pr.updatedAt,
		observedAt: pr.observedAt,
		ciObservedAt: pr.ciObservedAt,
		reviewObservedAt: pr.reviewObservedAt,
	};
}

// A cloud session only reaches the UI through a signed-in control-plane list,
// so the base URL is resolved lazily from settings, as terminate does, instead
// of subscribing every local card to the cloud auth hooks.
async function fetchCloudSessionScmSummary(
	queryClient: QueryClient,
	orgId: string,
	sessionId: string,
): Promise<SessionPRSummary[]> {
	const baseUrl = queryClient.getQueryData<Settings>(settingsQueryKey)?.cloudControlPlaneUrl ?? "";
	if (baseUrl === "") throw new Error("The cloud control plane is not configured.");
	const response = await createRendererCloudCpClient(baseUrl).listSessionPullRequests(orgId, sessionId);
	return response.pullRequests.map(toSessionPRSummaryFromCloud);
}

export function sessionScmSummaryQueryOptions(sessionId: string) {
	return {
		queryKey: sessionScmSummaryQueryKey(sessionId),
		enabled: Boolean(sessionId),
		queryFn: () => fetchSessionScmSummary(sessionId),
		retry: 1,
	};
}

/**
 * PR detail for one session. Local sessions read the daemon; cloud sessions
 * read their control plane, so the inspector shows the same PR cards for both.
 */
export function useSessionScmSummary(session?: Pick<WorkspaceSession, "id" | "cloud">) {
	const queryClient = useQueryClient();
	const sessionId = session?.id;
	const cloudOrgId = session?.cloud?.orgId;
	return useQuery({
		queryKey: sessionScmSummaryQueryKey(sessionId),
		enabled: Boolean(sessionId),
		queryFn: () =>
			cloudOrgId === undefined
				? fetchSessionScmSummary(sessionId!)
				: fetchCloudSessionScmSummary(queryClient, cloudOrgId, sessionId!),
		refetchInterval: cloudOrgId === undefined ? undefined : CLOUD_SCM_SUMMARY_REFETCH_MS,
		retry: 1,
	});
}
