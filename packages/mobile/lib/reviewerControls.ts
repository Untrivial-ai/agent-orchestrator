import type { AgentCatalog, ReviewerAgentConfig } from "./api";
import { rankAgents, type RankedAgent } from "./agentPicker";

export type ReviewerSelection = {
	harness?: string;
	agentConfig?: ReviewerAgentConfig;
};

export function reviewerChoices(catalog: AgentCatalog): RankedAgent[] {
	return rankAgents(catalog);
}

export function reviewerSwitchSelection(
	harness: string,
	config: ReviewerAgentConfig,
): ReviewerSelection {
	const agentConfig = Object.fromEntries(
		Object.entries(config).filter(([, value]) => typeof value === "string" && value.trim()),
	) as ReviewerAgentConfig;
	return {
		...(harness ? { harness } : {}),
		...(Object.keys(agentConfig).length ? { agentConfig } : {}),
	};
}

export function reviewerSelectionChanged(
	currentHarness: string,
	currentConfig: ReviewerAgentConfig,
	nextHarness: string,
	nextConfig: ReviewerAgentConfig,
): boolean {
	const current = reviewerSwitchSelection(currentHarness, currentConfig);
	const next = reviewerSwitchSelection(nextHarness, nextConfig);
	return current.harness !== next.harness
		|| current.agentConfig?.model !== next.agentConfig?.model
		|| current.agentConfig?.mode !== next.agentConfig?.mode
		|| current.agentConfig?.effort !== next.agentConfig?.effort
		|| current.agentConfig?.permissions !== next.agentConfig?.permissions;
}

/** Changing an active reviewer's harness or config replaces its pane and cancels its running pass. */
export function reviewerSwitchWarning(hasRunningReview: boolean): string | undefined {
	return hasRunningReview
		? "Changing reviewer settings now stops the active reviewer and cancels its running review before applying the selection."
		: undefined;
}
