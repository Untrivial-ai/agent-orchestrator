import {
	findActiveAgentSwitch,
	selectDurableAgentSwitch,
	useAgentSwitches,
} from "./useAgentSwitches";
import { useSwitchAgentState } from "./useSwitchAgent";
import {
	deriveAgentSwitchPresentation,
	type AgentSwitchPresentation,
} from "../lib/agent-switch-presentation";
import type { AgentSwitchSummary, WorkspaceSession } from "../types/workspace";

type UseSessionHandoffMenuOptions = {
	chatControllerReady?: boolean;
};

/** Read-only handoff state for the session overflow menu. Side effects stay in the active surface. */
export function useSessionHandoffMenu(
	session: WorkspaceSession | undefined,
	options: UseSessionHandoffMenuOptions = {},
) {
	const sessionId = session?.id ?? "";
	const agentSwitches = useAgentSwitches(sessionId).data ?? [];
	const switchMutation = useSwitchAgentState(sessionId);
	const selectedAgentSwitch = selectDurableAgentSwitch(session?.activeAgentSwitch, agentSwitches);
	const activeHistorySwitch = findActiveAgentSwitch(agentSwitches);
	const admissionAgentSwitch: AgentSwitchSummary | undefined =
		!selectedAgentSwitch && switchMutation.isPending && switchMutation.input
			? {
					agentHandoffStatus: "not_attempted",
					fromHarness: switchMutation.input.session.provider,
					id: `admission:${switchMutation.input.idempotencyKey}`,
					state: "preparing_handoff",
					targetHarness: switchMutation.input.targetHarness,
				}
			: undefined;
	const agentSwitch = selectedAgentSwitch ?? admissionAgentSwitch ?? activeHistorySwitch;
	const terminalHandleId =
		options.chatControllerReady !== undefined
			? options.chatControllerReady
				? "chat-controller"
				: undefined
			: session?.terminalHandleId;
	const presentation =
		agentSwitch && session
			? deriveAgentSwitchPresentation({
					agentSwitch,
					activityState: session.activity?.state,
					currentHarness: session.provider,
					isTerminated: Boolean(session.isTerminated),
					terminalHandleId,
				})
			: undefined;
	const switchControlPresentation: AgentSwitchPresentation | undefined =
		presentation?.outcome === "success" ? undefined : presentation;

	return {
		agentSwitch,
		switchControlPresentation,
		switchError: switchMutation.error,
	};
}
