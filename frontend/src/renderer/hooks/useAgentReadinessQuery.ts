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
	if (!data || !Array.isArray(data.agents)) throw new Error("Invalid agent readiness response");
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
	if (!data || !Array.isArray(data.agents)) throw new Error("Invalid agent readiness response");
	return data as AgentReadiness;
}

export function mergeAgentReadiness(
	current: AgentReadiness | undefined,
	next: AgentReadiness,
): AgentReadiness {
	if (!current || next.agents.length === 0) return next;
	const byID = new Map(current.agents.map((agent) => [agent.id, agent]));
	for (const agent of next.agents) {
		const previous = byID.get(agent.id);
		if (!previous) {
			byID.set(agent.id, agent);
			continue;
		}
		const installation = newestObservation(previous.installation, agent.installation);
		const authentication = newestObservation(previous.authentication, agent.authentication);
		const effectiveReadiness = installation.state === "not_installed" || (installation.state === "installed" && authentication.state === "unauthorized")
			? "not_ready"
			: installation.state === "installed" && (authentication.state === "authorized" || authentication.state === "not_applicable")
				? "ready"
				: "unknown";
		byID.set(agent.id, { ...agent, installation, authentication, effectiveReadiness });
	}
	return { agents: [...byID.values()].sort((a, b) => a.id.localeCompare(b.id)) };
}

// Installation and authentication can finish independently and arrive out of order.
function newestObservation<T extends { attemptedAt: string | null; checkedAt: string | null }>(previous: T, next: T): T {
	const previousTime = Date.parse(previous.attemptedAt ?? previous.checkedAt ?? "") || 0;
	const nextTime = Date.parse(next.attemptedAt ?? next.checkedAt ?? "") || 0;
	return previousTime > nextTime ? previous : next;
}

export function cacheAgentReadiness(queryClient: QueryClient, next: AgentReadiness): void {
	queryClient.setQueryData<AgentReadiness>(agentReadinessQueryKey, (current) =>
		mergeAgentReadiness(current, next),
	);
}

export const agentReadinessQueryOptions = {
	queryKey: agentReadinessQueryKey,
	queryFn: fetchAgentReadiness,
	structuralSharing: (previous: unknown, next: unknown) =>
		mergeAgentReadiness(previous as AgentReadiness | undefined, next as AgentReadiness),
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
		let timer: ReturnType<typeof setTimeout> | undefined;
		const refresh = async () => {
			let delay = 60_000;
			try {
				const next = await ensureAgentReadiness(normalizedIDs, purpose);
				if (!active) return;
				cacheAgentReadiness(queryClient, next);
				if (next.agents.some((agent) => agent.installation.freshness === "checking")) delay = 2_000;
			} catch {
				// The daemon owns probe caching/backoff; retry only while consumed.
			}
			if (active) timer = setTimeout(() => void refresh(), delay);
		};
		void refresh();
		return () => {
			active = false;
			clearTimeout(timer);
		};
	}, [enabled, normalizedIDs, purpose, queryClient]);
}
