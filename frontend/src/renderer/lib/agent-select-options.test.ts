import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import { buildRankedAgentOptions, defaultAuthorizedAgentForRole, DEFAULT_AGENT_PRIORITY_RANK, type RoleSession } from "./agent-select-options";

type Agent = components["schemas"]["AgentReadinessSnapshot"];

function agent(
	id: string,
	installation: Agent["installation"]["state"] = "installed",
	authentication: Agent["authentication"]["state"] = "authorized",
	usageCount = 0,
	lastUsedAt?: string,
): Agent {
	return {
		id,
		label: id === "opencode" ? "OpenCode" : id,
		installation: {
			state: installation,
			freshness: "fresh",
			checkedAt: null,
			attemptedAt: null,
			reasonCode: "test",
			reason: "test",
		},
		authentication: {
			state: authentication,
			freshness: "fresh",
			checkedAt: null,
			attemptedAt: null,
			reasonCode: "test",
			reason: "test",
		},
		effectiveReadiness: installation === "installed" && authentication === "authorized" ? "ready" : "unknown",
		usageCount,
		lastUsedAt,
	};
}

const openCode = agent("opencode");

describe("buildRankedAgentOptions", () => {
	it("keeps the ready opencode agent selectable at the top of the ranking", () => {
		const options = buildRankedAgentOptions({
			agents: [openCode],
			priorityRank: DEFAULT_AGENT_PRIORITY_RANK,
			fallbackAgents: [],
		});

		expect(options).toHaveLength(1);
		expect(options[0]).toMatchObject({ id: "opencode", disabled: false, rank: 0, priorityRank: 0, status: "" });
	});

	it("allows unknown observations with warnings and blocks definite failures", () => {
		const options = buildRankedAgentOptions({
			agents: [agent("opencode", "unknown", "unknown"), agent("opencode", "installed", "unauthorized")],
			priorityRank: DEFAULT_AGENT_PRIORITY_RANK,
			fallbackAgents: [],
		});

		expect(options[0]).toMatchObject({ id: "opencode", disabled: false, status: "Install unknown" });
		expect(options[1]).toMatchObject({ id: "opencode", disabled: true, status: "Needs auth" });
	});

	it("keeps stale known-good agents selectable while checking", () => {
		const knownGood = agent("opencode");
		knownGood.installation.freshness = "checking";
		knownGood.authentication.freshness = "stale";

		const [option] = buildRankedAgentOptions({
			agents: [knownGood],
			priorityRank: DEFAULT_AGENT_PRIORITY_RANK,
			fallbackAgents: [],
		});

		expect(option).toMatchObject({ disabled: false, status: "" });
	});

	it("falls back to the fallback catalog when no observed agents exist", () => {
		const options = buildRankedAgentOptions({
			agents: undefined,
			priorityRank: DEFAULT_AGENT_PRIORITY_RANK,
			fallbackAgents: [openCode],
		});

		expect(options.map((option) => option.id)).toEqual(["opencode"]);
	});
});

describe("defaultAuthorizedAgentForRole", () => {
	const agents = [openCode];

	function hoursAgo(hours: number): string {
		return new Date(Date.now() - hours * 60 * 60 * 1000).toISOString();
	}

	function session(
		provider: RoleSession["provider"],
		kind: "worker" | "orchestrator" | undefined,
		createdAt: string,
		id = `${provider}-${kind ?? "unknown"}-${createdAt}`,
	): RoleSession {
		return { id, provider, kind, createdAt };
	}

	it("counts worker history for the worker role and returns opencode", () => {
		const sessions = [session("opencode", "worker", hoursAgo(5)), session("opencode", "worker", hoursAgo(4))];

		expect(defaultAuthorizedAgentForRole(agents, sessions, "worker")).toBe("opencode");
	});

	it("returns opencode from an empty history when it is the only authorized agent", () => {
		expect(defaultAuthorizedAgentForRole(agents, [], "worker")).toBe("opencode");
		expect(defaultAuthorizedAgentForRole(agents, [], "orchestrator")).toBe("opencode");
	});

	it("ignores sessions older than 48 hours without losing the default", () => {
		const sessions = [session("opencode", "worker", hoursAgo(72)), session("opencode", "worker", hoursAgo(100))];

		expect(defaultAuthorizedAgentForRole(agents, sessions, "worker")).toBe("opencode");
	});

	it("returns an empty default when no agent is authorized", () => {
		expect(defaultAuthorizedAgentForRole([], [], "worker")).toBe("");
	});
});