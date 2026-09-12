import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";

export type SessionPRSummary = components["schemas"]["SessionPRSummary"];

export const sessionScmSummaryQueryKey = (sessionId?: string) =>
	sessionId ? (["session-scm-summary", sessionId] as const) : (["session-scm-summary"] as const);

export async function fetchSessionScmSummary(sessionId: string): Promise<SessionPRSummary[]> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr", {
		params: { path: { sessionId } },
	});
	if (error) throw error;
	return data?.prs ?? [];
}

export function sessionScmSummaryQueryOptions(sessionId: string) {
	return {
		queryKey: sessionScmSummaryQueryKey(sessionId),
		enabled: Boolean(sessionId),
		queryFn: () => fetchSessionScmSummary(sessionId),
		retry: 1,
	};
}

export function useSessionScmSummary(sessionId?: string) {
	return useQuery({
		queryKey: sessionScmSummaryQueryKey(sessionId),
		enabled: Boolean(sessionId),
		queryFn: () => fetchSessionScmSummary(sessionId!),
		retry: 1,
	});
}
