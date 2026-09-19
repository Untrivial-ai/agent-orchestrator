import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { cloudPullRequestToSessionSummary } from "../lib/cloud-inspector-adapters";
import type { CloudInspectorTarget } from "../lib/cloud-inspector-target";
import { createRendererCloudCpClient } from "./useCloudCp";

export type SessionPRSummary = components["schemas"]["SessionPRSummary"];

export const sessionScmSummaryQueryKey = (sessionId?: string, cloudOrgId?: string) =>
	sessionId
		? (["session-scm-summary", sessionId, cloudOrgId ?? "local"] as const)
		: (["session-scm-summary"] as const);

export async function fetchSessionScmSummary(
	sessionId: string,
	cloud?: CloudInspectorTarget,
): Promise<SessionPRSummary[]> {
	if (cloud) {
		const client = createRendererCloudCpClient(cloud.baseUrl);
		const response = await client.listSessionPullRequests(cloud.orgId, sessionId);
		return (response.pullRequests ?? []).map(cloudPullRequestToSessionSummary);
	}
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr", {
		params: { path: { sessionId } },
	});
	if (error) throw error;
	return data?.prs ?? [];
}

export function sessionScmSummaryQueryOptions(sessionId: string, cloud?: CloudInspectorTarget) {
	return {
		queryKey: sessionScmSummaryQueryKey(sessionId, cloud?.orgId),
		enabled: Boolean(sessionId),
		queryFn: () => fetchSessionScmSummary(sessionId, cloud),
		retry: 1,
		refetchInterval: (cloud ? 15_000 : false) as number | false,
	};
}

export function useSessionScmSummary(sessionId?: string, cloud?: CloudInspectorTarget) {
	return useQuery({
		...sessionScmSummaryQueryOptions(sessionId ?? "", cloud),
		enabled: Boolean(sessionId),
	});
}
