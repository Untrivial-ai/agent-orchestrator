// Ranking and availability rules for the agent catalog. Pure — no React Native
// or Expo imports — so the states and ordering are unit-testable, the same split
// as prView.ts / agentsView.ts.
//
// Mirrors the desktop's frontend/src/renderer/lib/agent-select-options.ts, which
// is the only place either app decides what "available" means.
import type { AgentCatalog, AgentInfo } from "./api";

/** What the row says about an agent, and whether it can be chosen. */
export type AgentAvailability = "authorized" | "auth-unknown" | "needs-auth" | "needs-install";

export type RankedAgent = AgentInfo & {
	availability: AgentAvailability;
	/** Short status for the row. Empty for a healthy agent — see below. */
	status: string;
	selectable: boolean;
	rank: number;
};

// Desktop's DEFAULT_AGENT_PRIORITY. Ties inside a rank break by this before
// falling back to the label — without it the authorized group is alphabetical
// and Aider sorts above Claude Code.
const PRIORITY = ["claude-code", "codex", "cursor", "opencode", "aider"];
const priorityOf = (id: string) => {
	const i = PRIORITY.indexOf(id);
	return i === -1 ? Number.MAX_SAFE_INTEGER : i;
};

export function availabilityOf(agent: AgentInfo, catalog: AgentCatalog): AgentAvailability {
	const installed = catalog.installed.find((a) => a.id === agent.id);
	if (!installed) return "needs-install";
	const authorized =
		catalog.authorized.some((a) => a.id === agent.id) ||
		installed.authStatus === "authorized";
	if (authorized) return "authorized";
	// Absent is treated as unknown: the daemon could not determine credential
	// state, which is not the same as knowing the agent is unusable.
	return installed.authStatus === "unauthorized" ? "needs-auth" : "auth-unknown";
}

/**
 * Whether an agent can be picked.
 *
 * `auth-unknown` stays selectable on purpose, matching desktop: the daemon
 * failed to determine credential state, and refusing on that basis would block
 * a perfectly working agent. Only an explicit "unauthorized" or a missing
 * install is disqualifying.
 */
export function isSelectable(availability: AgentAvailability): boolean {
	return availability === "authorized" || availability === "auth-unknown";
}

/** Row status text. Healthy agents get none — only problems are worth saying. */
export function statusLabel(availability: AgentAvailability): string {
	switch (availability) {
		case "auth-unknown":
			return "Auth unknown";
		case "needs-auth":
			return "Needs auth";
		case "needs-install":
			return "Needs install";
		default:
			return "";
	}
}

const RANK: Record<AgentAvailability, number> = {
	authorized: 0,
	"auth-unknown": 1,
	"needs-auth": 2,
	"needs-install": 3,
};

/** The catalog as a picker list: usable agents first, then by priority, then label. */
export function rankAgents(catalog: AgentCatalog | null | undefined): RankedAgent[] {
	if (!catalog) return [];
	return catalog.supported
		.map((agent) => {
			const availability = availabilityOf(agent, catalog);
			return {
				...agent,
				availability,
				status: statusLabel(availability),
				selectable: isSelectable(availability),
				rank: RANK[availability],
			};
		})
		.sort(
			(a, b) =>
				a.rank - b.rank ||
				priorityOf(a.id) - priorityOf(b.id) ||
				a.label.localeCompare(b.label),
		);
}

/** The agent to preselect: the best usable one, else nothing. */
export function defaultAgent(ranked: RankedAgent[]): string | null {
	return ranked.find((a) => a.selectable)?.id ?? null;
}
