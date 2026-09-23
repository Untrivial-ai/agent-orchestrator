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

/** Changing an active reviewer's harness or config replaces its pane and cancels its running pass. */
export function reviewerSwitchWarning(hasRunningReview: boolean): string | undefined {
	return hasRunningReview
		? "Changing reviewer settings now stops the active reviewer and cancels its running review before applying the selection."
		: undefined;
}
