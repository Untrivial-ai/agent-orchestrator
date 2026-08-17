import { useQuery } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import type { AgentSwitchSummary } from "../types/workspace";

export type AgentSwitch = AgentSwitchSummary;

const terminalAgentSwitchStates = new Set<AgentSwitch["state"]>(["completed", "failed"]);

export const agentSwitchesQueryRoot = ["session-agent-switches"] as const;
export const agentSwitchesQueryKey = (sessionId: string) => [...agentSwitchesQueryRoot, sessionId] as const;

export function isTerminalAgentSwitch(agentSwitch: AgentSwitch): boolean {
	return terminalAgentSwitchStates.has(agentSwitch.state);
}

export function agentSwitchNeedsRecovery(agentSwitch: AgentSwitch): boolean {
	return (
		agentSwitchNeedsTargetStartRecovery(agentSwitch) ||
		agentSwitchNeedsSourceStopRecovery(agentSwitch) ||
		agentSwitchNeedsSourceRestore(agentSwitch)
	);
}

export function agentSwitchNeedsTargetStartRecovery(agentSwitch: AgentSwitch): boolean {
	return agentSwitch.state === "starting_target" && agentSwitch.errorCode === "target_start_unconfirmed";
}

export function agentSwitchNeedsSourceStopRecovery(agentSwitch: AgentSwitch): boolean {
	return agentSwitch.state === "stopping_source" && agentSwitch.errorCode === "source_stop_unconfirmed";
}

export function agentSwitchNeedsSourceRestore(agentSwitch: AgentSwitch): boolean {
	return (
		(agentSwitch.state === "source_stopped" || agentSwitch.state === "starting_target") &&
		agentSwitch.errorCode === "source_restore_unconfirmed"
	);
}

export function findActiveAgentSwitch(agentSwitches: AgentSwitch[]): AgentSwitch | undefined {
	return agentSwitches.find(
		(agentSwitch) => !isTerminalAgentSwitch(agentSwitch) && !agentSwitchNeedsRecovery(agentSwitch),
	);
}

export function findRecoveryRequiredAgentSwitch(agentSwitches: AgentSwitch[]): AgentSwitch | undefined {
	return agentSwitches.find(agentSwitchNeedsRecovery);
}

export function agentSwitchesRefetchInterval(agentSwitches: AgentSwitch[]): 1_000 | false {
	return findActiveAgentSwitch(agentSwitches) ? 1_000 : false;
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
		// connection is temporarily unavailable. Recovery-required records are
		// intentionally static until an external recovery changes them.
		refetchInterval: (query) =>
			agentSwitchesRefetchInterval((query.state.data as AgentSwitch[] | undefined) ?? []),
		retry: 1,
	});
}
