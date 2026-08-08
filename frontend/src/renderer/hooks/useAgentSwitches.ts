import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";

export type AgentSwitch = components["schemas"]["AgentSwitch"];

const terminalAgentSwitchStates = new Set<AgentSwitch["state"]>(["completed", "failed"]);

export const agentSwitchesQueryKey = (sessionId: string) => ["session-agent-switches", sessionId] as const;

export function isTerminalAgentSwitch(agentSwitch: AgentSwitch): boolean {
	return terminalAgentSwitchStates.has(agentSwitch.state);
}

export function findActiveAgentSwitch(agentSwitches: AgentSwitch[]): AgentSwitch | undefined {
	return agentSwitches.find((agentSwitch) => !isTerminalAgentSwitch(agentSwitch));
}

async function fetchAgentSwitches(sessionId: string): Promise<AgentSwitch[]> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/agent-switches", {
		params: { path: { sessionId } },
	});
	if (error) {
		throw new Error(apiErrorMessage(error, "Unable to load agent switch status"));
	}
	return data?.switches ?? [];
}

export function useAgentSwitches(sessionId: string) {
	return useQuery({
		queryKey: agentSwitchesQueryKey(sessionId),
		enabled: Boolean(sessionId),
		queryFn: () => (usesPreviewWorkspaceData ? Promise.resolve([]) : fetchAgentSwitches(sessionId)),
		// Once a durable saga is active, keep its phase fresh even if the CDC
		// connection is temporarily unavailable. An idle session does not poll.
		refetchInterval: (query) =>
			findActiveAgentSwitch((query.state.data as AgentSwitch[] | undefined) ?? []) ? 1_000 : false,
		retry: 1,
	});
}
