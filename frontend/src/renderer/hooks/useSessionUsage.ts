import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { clientForSessionHost } from "../lib/host-clients";
import { sessionUsageQueryRoot } from "./useSessionUsageSummaries";

export type SessionUsage = components["schemas"]["SessionUsageResponse"];

export const sessionUsageDetailQueryKey = (sessionId: string, hostId?: string) =>
	hostId ? [...sessionUsageQueryRoot, "detail", hostId, sessionId] as const : [...sessionUsageQueryRoot, "detail", sessionId] as const;

export async function fetchSessionUsage(sessionId: string, hostId?: string): Promise<SessionUsage> {
	const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/usage/sessions/{sessionId}", {
		params: { path: { sessionId } },
	});
	if (error) throw error;
	return data;
}

export function useSessionUsage(sessionId: string, enabled = true, hostId?: string) {
	return useQuery({
		queryKey: sessionUsageDetailQueryKey(sessionId, hostId),
		queryFn: () => fetchSessionUsage(sessionId, hostId),
		enabled: enabled && Boolean(sessionId),
		retry: 1,
	});
}
