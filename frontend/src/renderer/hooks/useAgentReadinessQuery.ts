import { useEffect, useMemo } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type AgentReadiness = components["schemas"]["AgentReadinessResponse"];
export type AgentReadinessSnapshot = components["schemas"]["AgentReadinessSnapshot"];
export type AgentReadinessPurpose = components["schemas"]["EnsureAgentReadinessRequest"]["purpose"];

export const agentReadinessQueryKey = ["agent-readiness"] as const;
const settingsReadinessPollQueryKey = ["agent-readiness-ensure", "settings"] as const;
export const SETTINGS_READINESS_POLL_INTERVAL_MS = 15_000;

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
	if (!current) return next;
	if (next.agents.length === 0) return current;
	const byID = new Map(current.agents.map((agent) => [agent.id, agent]));
	for (const agent of next.agents) {
		const existing = byID.get(agent.id);
		if (!existing) {
			byID.set(agent.id, agent);
			continue;
		}
		const installation = newestObservation(existing.installation, agent.installation);
		const authentication = newestObservation(existing.authentication, agent.authentication);
		byID.set(agent.id, {
			...agent,
			installation,
			authentication,
			effectiveReadiness: effectiveReadiness(installation.state, authentication.state),
		});
	}
	return { agents: [...byID.values()].sort((a, b) => a.id.localeCompare(b.id)) };
}

type ReadinessObservation = AgentReadinessSnapshot["installation"] | AgentReadinessSnapshot["authentication"];

function observationTime(observation: ReadinessObservation): number {
	const value = observation.attemptedAt ?? observation.checkedAt;
	if (!value) return Number.NEGATIVE_INFINITY;
	const parsed = Date.parse(value);
	return Number.isNaN(parsed) ? Number.NEGATIVE_INFINITY : parsed;
}

function newestObservation<T extends ReadinessObservation>(current: T, next: T): T {
	return observationTime(next) >= observationTime(current) ? next : current;
}

function effectiveReadiness(
	installation: AgentReadinessSnapshot["installation"]["state"],
	authentication: AgentReadinessSnapshot["authentication"]["state"],
): AgentReadinessSnapshot["effectiveReadiness"] {
	if (installation === "not_installed" || (installation === "installed" && authentication === "unauthorized")) return "not_ready";
	if (installation === "installed" && (authentication === "authorized" || authentication === "not_applicable")) return "ready";
	return "unknown";
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

export function useSettingsAgentReadinessPolling({
	agentIds,
	enabled = true,
}: {
	agentIds: string[];
	enabled?: boolean;
}) {
	const queryClient = useQueryClient();
	const agentIDsKey = [...new Set(agentIds.filter(Boolean))].sort().join("\u0000");
	const normalizedIDs = useMemo(
		() => (agentIDsKey === "" ? [] : agentIDsKey.split("\u0000")),
		[agentIDsKey],
	);

	return useQuery({
		queryKey: [...settingsReadinessPollQueryKey, agentIDsKey],
		queryFn: async () => {
			const next = await ensureAgentReadiness(normalizedIDs, "settings");
			cacheAgentReadiness(queryClient, next);
			return next;
		},
		enabled: enabled && normalizedIDs.length > 0,
		refetchInterval: SETTINGS_READINESS_POLL_INTERVAL_MS,
		refetchIntervalInBackground: false,
		retry: 1,
	});
}
