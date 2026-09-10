export type AgentInfo = {
	authentication: {
		state: "authorized" | "unauthorized" | "unknown" | "not_applicable" | "configured";
		freshness: "fresh" | "stale" | "checking";
	};
	effectiveReadiness: "ready" | "not_ready" | "unknown";
	id: string;
	installation: {
		state: "installed" | "not_installed" | "unknown";
		freshness: "fresh" | "stale" | "checking";
	};
	label: string;
	lastUsedAt?: string | null;
	usageCount: number;
};

export const DEFAULT_AGENT_PRIORITY = ["claude-code", "codex", "cursor", "opencode", "aider"] as const;
export const DEFAULT_AGENT_PRIORITY_RANK = new Map<string, number>(
	DEFAULT_AGENT_PRIORITY.map((agent, index) => [agent, index]),
);

export type AgentStatusTone = "success" | "warning" | "muted";

export type RankedAgentOption = AgentInfo & {
	disabled: boolean;
	priorityRank: number;
	rank: number;
	status: string;
	statusTone: AgentStatusTone;
};

export function unknownAgentReadiness(id: string, label: string): AgentInfo {
	return {
		id,
		label,
		installation: {
			state: "unknown",
			freshness: "stale",
		},
		authentication: {
			state: "unknown",
			freshness: "stale",
		},
		effectiveReadiness: "unknown",
		usageCount: 0,
		lastUsedAt: null,
	};
}

export function agentLabelCompare(a: AgentInfo, b: AgentInfo): number {
	return a.label.localeCompare(b.label) || a.id.localeCompare(b.id);
}

export function agentUsageCompare(a: AgentInfo, b: AgentInfo): number {
	const byFrequency = (b.usageCount ?? 0) - (a.usageCount ?? 0);
	if (byFrequency !== 0) return byFrequency;
	const byRecency = (b.lastUsedAt ?? "").localeCompare(a.lastUsedAt ?? "");
	if (byRecency !== 0) return byRecency;
	return 0;
}

function agentStatus(agent: AgentInfo): Pick<RankedAgentOption, "status" | "statusTone"> {
	if (agent.installation.state === "not_installed") {
		return { status: "Needs install", statusTone: "muted" };
	}
	if (agent.authentication.state === "unauthorized") {
		return { status: "Needs auth", statusTone: "warning" };
	}
	if (agent.installation.state === "unknown") {
		return { status: "Install unknown", statusTone: "warning" };
	}
	if (agent.authentication.state === "unknown") {
		return { status: "Auth unknown", statusTone: "warning" };
	}
	// A credential exists but nothing proved it works. It must not fall through
	// to the known-good branch below: rendering an unverified credential green
	// is precisely how a revoked key came to look like a working agent.
	if (agent.authentication.state === "configured") {
		return { status: "Unverified", statusTone: "muted" };
	}
	// Known-good agents stay selectable even while stale or checking; freshness
	// is informative coordinator state, not a reason for the renderer to block.
	return { status: "", statusTone: "success" };
}

export function buildRankedAgentOptions({
	agents,
	priorityRank,
	fallbackAgents,
	filter,
}: {
	agents?: AgentInfo[];
	priorityRank: Map<string, number>;
	fallbackAgents: AgentInfo[];
	filter?: (agent: AgentInfo) => boolean;
}): RankedAgentOption[] {
	return (agents ?? fallbackAgents)
		.filter((agent) => (filter ? filter(agent) : true))
		.map((agent) => {
			const isInstallationUnknown = agent.installation.state === "unknown";
			// Configured ranks with unknown, not below it: a credential AO could
			// not validate is still more evidence than no observation at all,
			// and neither is a reason to make the agent unselectable.
			const isAuthUnknown =
				agent.authentication.state === "unknown" || agent.authentication.state === "configured";
			const isAuthorized =
				agent.authentication.state === "authorized" || agent.authentication.state === "not_applicable";
			const isDefinitelyUnavailable =
				agent.installation.state === "not_installed" || agent.authentication.state === "unauthorized";
			const isSelectable = !isDefinitelyUnavailable;
			const rank =
				isAuthorized && agent.installation.state === "installed"
					? 0
					: !isDefinitelyUnavailable && (isInstallationUnknown || isAuthUnknown)
						? 1
						: agent.installation.state === "installed"
							? 2
							: 3;
			return {
				...agent,
				disabled: !isSelectable,
				priorityRank: priorityRank.get(agent.id) ?? Number.MAX_SAFE_INTEGER,
				rank,
				...agentStatus(agent),
			};
		})
		.sort(
			(a, b) =>
				a.rank - b.rank ||
				agentUsageCompare(a, b) ||
				a.priorityRank - b.priorityRank ||
				agentLabelCompare(a, b),
		);
}
