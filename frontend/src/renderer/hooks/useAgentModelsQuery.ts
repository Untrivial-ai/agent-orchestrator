import { queryOptions } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { clientForHost } from "../lib/host-clients";

export type AgentModelCatalog = components["schemas"]["AgentModelsResponse"];

const MODEL_CATALOG_VALIDATION_INTERVAL_MS = 10 * 60 * 1_000;

export const agentModelsQueryPrefix = (agentId: string) =>
	["agent-models", agentId] as const;

export const agentModelsQueryKey = (agentId: string, projectId: string, hostId?: string) =>
	hostId ? ["agent-models", hostId, agentId, projectId] as const : [...agentModelsQueryPrefix(agentId), projectId] as const;

async function requestAgentModels(
	agentId: string,
	projectId: string,
	mode: "cached" | "refresh" | "revalidate",
	hostId?: string,
): Promise<AgentModelCatalog> {
	const client = hostId ? clientForHost(hostId) : apiClient;
	const path = { agent: agentId };
	const result =
		mode === "cached"
			? await client.GET("/api/v1/agents/{agent}/models", {
					params: { path, query: { projectId: projectId || undefined } },
				})
			: await client.POST("/api/v1/agents/{agent}/models/refresh", {
					params: {
						path,
						query: { projectId: projectId || undefined, revalidate: mode === "revalidate" || undefined },
					},
				});
	if (result.error) throw new Error(apiErrorMessage(result.error));
	return result.data as AgentModelCatalog;
}

export function agentModelsQueryOptions(agentId: string, projectId: string, hostId?: string) {
	return queryOptions({
		queryKey: agentModelsQueryKey(agentId, projectId, hostId),
		queryFn: () => requestAgentModels(agentId, projectId, "cached", hostId),
		enabled: agentId !== "",
		staleTime: MODEL_CATALOG_VALIDATION_INTERVAL_MS,
	});
}

export function refreshAgentModels(agentId: string, projectId: string, hostId?: string) {
	return requestAgentModels(agentId, projectId, "refresh", hostId);
}

export function revalidateAgentModels(agentId: string, projectId: string, hostId?: string) {
	return requestAgentModels(agentId, projectId, "revalidate", hostId);
}
