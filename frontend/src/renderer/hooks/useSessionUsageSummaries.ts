import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { clientForHost } from "../lib/host-clients";
import { LOCAL_HOST } from "../lib/hosts";

export type SessionUsageSummary = components["schemas"]["CompactSessionUsageResponse"];

export const sessionUsageQueryRoot = ["session-usage"] as const;
export const sessionUsageQueryKey = (projectId?: string, hostId?: string) =>
	[...sessionUsageQueryRoot, hostId ?? LOCAL_HOST, projectId ?? "all"] as const;

export async function fetchSessionUsageSummaries(projectId?: string, hostId?: string): Promise<SessionUsageSummary[]> {
	const { data, error } = await (hostId ? clientForHost(hostId) : apiClient).GET("/api/v1/usage/sessions", {
		params: { query: projectId ? { projectId } : {} },
	});
	if (error) throw error;
	return data?.sessions ?? [];
}

export function sessionUsageQueryOptions(projectId?: string, hostId?: string) {
	return {
		queryKey: sessionUsageQueryKey(projectId, hostId),
		queryFn: () => fetchSessionUsageSummaries(projectId, hostId),
		retry: 1,
		...(hostId ? { refetchInterval: 15_000 } : {}),
		select: (items: SessionUsageSummary[]) =>
			new Map(items.map((item) => [item.sessionId, item] as const)),
	};
}

export function useSessionUsageSummaries(projectId?: string, hostId?: string) {
	return useQuery(sessionUsageQueryOptions(projectId, hostId));
}
