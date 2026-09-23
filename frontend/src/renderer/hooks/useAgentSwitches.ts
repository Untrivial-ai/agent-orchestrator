import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import type { AgentSwitchSummary } from "../types/workspace";
import { agentSwitchVisibility } from "../lib/agent-switch-visibility";

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
		agentSwitchNeedsSourceRecovery(agentSwitch)
	);
}

export function agentSwitchNeedsSourceRecovery(agentSwitch: AgentSwitch): boolean {
	return agentSwitchNeedsSourceStopRecovery(agentSwitch) || agentSwitchNeedsSourceRestore(agentSwitch);
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

// Selects the durable switch that owns the session interaction lock. The
// workspace snapshot is fastest, while the switch-history query survives a
// renderer reload and carries recovery details that the compact summary may not
// have observed yet.
export function selectDurableAgentSwitch(
	sessionAgentSwitch: AgentSwitchSummary | undefined,
	agentSwitches: AgentSwitch[],
): AgentSwitch | undefined {
	const detailed = sessionAgentSwitch
		? agentSwitches.find((entry) => entry.id === sessionAgentSwitch.id)
		: undefined;
	return (
		detailed ??
		sessionAgentSwitch ??
		findRecoveryRequiredAgentSwitch(agentSwitches) ??
		findActiveAgentSwitch(agentSwitches)
	);
}

export function agentSwitchesRefetchInterval(agentSwitches: AgentSwitch[]): 1_000 | false {
	return findActiveAgentSwitch(agentSwitches) || agentSwitches.some(agentSwitchNeedsRecovery)
		? 1_000
		: false;
}

async function fetchAgentSwitches(sessionId: string, signal?: AbortSignal): Promise<AgentSwitch[]> {
	const localSourceKey = `switch-history:${sessionId}`;
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/agent-switches", {
		params: { path: { sessionId } },
		signal,
	});
	if (error) {
		agentSwitchVisibility.setQueryHealthy("active", false, localSourceKey);
		agentSwitchVisibility.setQueryHealthy("history", false, localSourceKey);
		throw new Error(apiErrorMessage(error, "Unable to load agent switch status"));
	}
	agentSwitchVisibility.setQueryHealthy("active", true, localSourceKey);
	agentSwitchVisibility.setQueryHealthy("history", true, localSourceKey);
	return data?.switches ?? [];
}

export function useAgentSwitches(sessionId: string) {
	useEffect(() => () => agentSwitchVisibility.clearQuerySource(`switch-history:${sessionId}`), [sessionId]);
	return useQuery({
		queryKey: agentSwitchesQueryKey(sessionId),
		enabled: Boolean(sessionId),
		queryFn: ({ signal }) => (usesPreviewWorkspaceData ? Promise.resolve([]) : fetchAgentSwitches(sessionId, signal)),
		// Keep active sagas fresh even if the CDC connection is temporarily
		// unavailable. Source-recovery endpoints accept work asynchronously, so
		// those recovery rows must also poll until their worker settles.
		refetchInterval: (query) =>
			agentSwitchesRefetchInterval((query.state.data as AgentSwitch[] | undefined) ?? []),
		retry: 1,
	});
}
