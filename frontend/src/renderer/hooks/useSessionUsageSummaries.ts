import { useQuery, type QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";

export type SessionUsageSummary = components["schemas"]["CompactSessionUsageResponse"];

export const sessionUsageQueryRoot = ["session-usage"] as const;
export const sessionUsageQueryKey = (projectId?: string) =>
	[...sessionUsageQueryRoot, projectId ?? "all"] as const;

export async function fetchSessionUsageSummaries(projectId?: string): Promise<SessionUsageSummary[]> {
	const { data, error } = await apiClient.GET("/api/v1/usage/sessions", {
		params: { query: projectId ? { projectId } : {} },
	});
	if (error) throw error;
	return data?.sessions ?? [];
}

export function sessionUsageQueryOptions(projectId?: string) {
	return {
		queryKey: sessionUsageQueryKey(projectId),
		queryFn: () => fetchSessionUsageSummaries(projectId),
		retry: 1,
		select: (items: SessionUsageSummary[]) =>
			new Map(items.map((item) => [item.sessionId, item] as const)),
	};
}

// Board route loaders prime the same cache that SessionsBoard observes. Keep
// failures non-blocking: usage is supplementary, so an unavailable summary
// endpoint must not prevent the Kanban itself from opening.
export async function preloadSessionUsageSummaries(
	queryClient: QueryClient,
	projectId?: string,
): Promise<void> {
	try {
		await queryClient.ensureQueryData({ ...sessionUsageQueryOptions(projectId), retry: false });
	} catch {
		// The mounted query retains its existing retry/error behavior.
	}
}

export function useSessionUsageSummaries(projectId?: string) {
	return useQuery(sessionUsageQueryOptions(projectId));
}
