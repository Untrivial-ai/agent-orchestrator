import type { AgentInfo } from "../lib/agent-select-options";
import { resolveAgentRecoveryAction } from "../lib/agent-recovery";
import { useUiStore } from "../stores/ui-store";
import { AgentRecoveryAction } from "./AgentRecoveryAction";

export function AgentSelectorRecoveryAction({
	agentId,
	agents,
	isLoading,
	variant,
}: {
	agentId: string;
	agents?: AgentInfo[];
	isLoading: boolean;
	variant: "explanatory" | "compact";
}) {
	const openGlobalSettings = useUiStore((state) => state.openGlobalSettings);
	if (!agentId) return null;

	const readiness = agents?.find((agent) => agent.id === agentId);
	const action = resolveAgentRecoveryAction({ readiness, isLoading });
	return (
		<AgentRecoveryAction
			action={action}
			agentLabel={readiness?.label ?? agentId}
			onAction={() => openGlobalSettings("harness", { focusAgentId: agentId })}
			variant={variant}
		/>
	);
}
