import type { AgentCatalog, ReviewerAgentConfig } from "./api";
import { rankAgents, type RankedAgent } from "./agentPicker";

export type ReviewerSelection = {
	harness?: string;
	agentConfig?: ReviewerAgentConfig;
};

export function reviewerChoices(catalog: AgentCatalog): RankedAgent[] {
	return rankAgents(catalog);
}

// Mirrors desktop's WORKER_DEFAULT_REVIEWERS (frontend/src/renderer/lib/reviewer-harnesses.ts).
const WORKER_DEFAULT_REVIEWERS: Readonly<Record<string, string>> = {
	"claude-code": "claude-code",
	codex: "codex",
	opencode: "opencode",
	muse: "muse",
	kimchi: "kimchi",
};

/**
 * The harness "Project default" resolves to, as desktop resolves it: the
 * project's first configured reviewer, else the worker harness's default.
 * Never the daemon's reported reviewerHarness, which names whichever reviewer
 * last ran and lags a switch until the new reviewer has run once.
 */
export function defaultReviewerHarness(projectReviewers: readonly { harness?: string }[] | undefined, workerHarness: string | undefined): string {
	return projectReviewers?.[0]?.harness || (workerHarness ? WORKER_DEFAULT_REVIEWERS[workerHarness] : undefined) || "claude-code";
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
