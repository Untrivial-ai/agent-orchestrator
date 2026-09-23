import { useEffect, useMemo } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type AgentReadiness = components["schemas"]["AgentReadinessResponse"];
export type AgentReadinessSnapshot = components["schemas"]["AgentReadinessSnapshot"];
export type AgentReadinessPurpose = components["schemas"]["EnsureAgentReadinessRequest"]["purpose"];

export const agentReadinessQueryKey = ["agent-readiness"] as const;

async function fetchAgentReadiness(): Promise<AgentReadiness> {
	const { data, error } = await apiClient.GET("/api/v1/agents/readiness");
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgentReadiness;
}

export async function ensureAgentReadiness(
	agentIds: string[] = [],
	purpose: AgentReadinessPurpose = "display",
): Promise<AgentReadiness> {
	const { data, error } = await apiClient.POST("/api/v1/agents/readiness/ensure", {
		body: { agentIds, purpose },
	});
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgentReadiness;
}

export function mergeAgentReadiness(
	current: AgentReadiness | undefined,
	next: AgentReadiness,
): AgentReadiness {
	if (!current || next.agents.length === 0) return next;
	const byID = new Map(current.agents.map((agent) => [agent.id, agent]));
	for (const agent of next.agents) byID.set(agent.id, agent);
	return { agents: [...byID.values()].sort((a, b) => a.id.localeCompare(b.id)) };
}

export function cacheAgentReadiness(queryClient: QueryClient, next: AgentReadiness): void {
	queryClient.setQueryData<AgentReadiness>(agentReadinessQueryKey, (current) =>
		mergeAgentReadiness(current, next),
	);
}

export const agentReadinessQueryOptions = {
	queryKey: agentReadinessQueryKey,
	queryFn: fetchAgentReadiness,
	retry: 1,
	// Freshness belongs to the daemon coordinator. React Query only retains the
	// latest display copy and must never decide whether native work is required.
	staleTime: Number.POSITIVE_INFINITY,
};

export function useAgentReadinessQuery(enabled = true) {
	return useQuery({ ...agentReadinessQueryOptions, enabled });
}

export function useEnsureAgentReadiness({
	agentIds = [],
	enabled = true,
	purpose = "display",
}: {
	agentIds?: string[];
	enabled?: boolean;
	purpose?: AgentReadinessPurpose;
} = {}): void {
	const queryClient = useQueryClient();
	const agentIDsKey = [...new Set(agentIds.filter(Boolean))].sort().join("\u0000");
	const normalizedIDs = useMemo(
		() => (agentIDsKey === "" ? [] : agentIDsKey.split("\u0000")),
		[agentIDsKey],
	);

	useEffect(() => {
		if (!enabled) return;
		let active = true;
		void ensureAgentReadiness(normalizedIDs, purpose)
			.then((next) => {
				if (active) cacheAgentReadiness(queryClient, next);
			})
			.catch(() => {
				// Opportunistic: cached readiness remains useful and native launch is
				// still the authoritative validation path.
			});
		return () => {
			active = false;
		};
	}, [enabled, normalizedIDs, purpose, queryClient]);
}
