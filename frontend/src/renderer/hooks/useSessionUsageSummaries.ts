import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";

export type SessionUsageSummary = components["schemas"]["CompactSessionUsageResponse"];
export type SessionUsageDetails = components["schemas"]["SessionUsageResponse"];

export const sessionUsageQueryRoot = ["session-usage"] as const;
export const sessionUsageQueryKey = (projectId?: string) =>
	[...sessionUsageQueryRoot, projectId ?? "all"] as const;
export const sessionUsageDetailQueryKey = (sessionId: string) =>
	[...sessionUsageQueryRoot, "detail", sessionId] as const;

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

export function useSessionUsageSummaries(projectId?: string) {
	return useQuery(sessionUsageQueryOptions(projectId));
}

export async function fetchSessionUsage(sessionId: string): Promise<SessionUsageDetails> {
	const { data, error } = await apiClient.GET("/api/v1/usage/sessions/{sessionId}", {
		params: { path: { sessionId } },
	});
	if (error) throw error;
	if (!data) throw new Error("Session usage response was empty");
	return data;
}

export function sessionUsageDetailQueryOptions(sessionId: string) {
	return {
		enabled: sessionId.length > 0,
		queryFn: () => fetchSessionUsage(sessionId),
		queryKey: sessionUsageDetailQueryKey(sessionId),
		retry: false,
	};
}

export function useSessionUsage(sessionId: string) {
	return useQuery(sessionUsageDetailQueryOptions(sessionId));
}
