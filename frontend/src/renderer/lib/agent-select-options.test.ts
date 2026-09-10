import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import { buildRankedAgentOptions } from "./agent-select-options";

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
		label: id === "claude-code" ? "Claude Code" : "Codex",
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

const priorityRank = new Map([
	["claude-code", 0],
	["codex", 1],
]);

describe("buildRankedAgentOptions", () => {
	it("ranks selectable agents by frequency before the static cold-start priority", () => {
		const agents = [
			agent("claude-code", "installed", "authorized", 1),
			agent("codex", "installed", "authorized", 4),
		];

		const options = buildRankedAgentOptions({
			agents,
			priorityRank,
			fallbackAgents: [],
		});

		expect(options.map((agent) => agent.id)).toEqual(["codex", "claude-code"]);
	});

	it("uses most recent usage to break frequency ties", () => {
		const agents = [
			agent("claude-code", "installed", "authorized", 2, "2026-08-18T10:00:00Z"),
			agent("codex", "installed", "authorized", 2, "2026-08-19T10:00:00Z"),
		];

		const options = buildRankedAgentOptions({
			agents,
			priorityRank,
			fallbackAgents: [],
		});

		expect(options.map((agent) => agent.id)).toEqual(["codex", "claude-code"]);
	});

	it("keeps unavailable agents below selectable agents regardless of usage", () => {
		const agents = [
			agent("claude-code", "installed", "authorized", 1),
			agent("codex", "not_installed", "unknown", 10),
		];

		const options = buildRankedAgentOptions({
			agents,
			priorityRank,
			fallbackAgents: [],
		});

		expect(options.map((agent) => agent.id)).toEqual(["claude-code", "codex"]);
	});

	it("allows unknown observations with warnings and blocks definite failures", () => {
		const options = buildRankedAgentOptions({
			agents: [
				agent("claude-code", "unknown", "unknown"),
				agent("codex", "installed", "unauthorized"),
			],
			priorityRank,
			fallbackAgents: [],
		});

		expect(options[0]).toMatchObject({ id: "claude-code", disabled: false, status: "Install unknown" });
		expect(options[1]).toMatchObject({ id: "codex", disabled: true, status: "Needs auth" });
	});

	it("keeps stale known-good agents selectable while checking", () => {
		const knownGood = agent("codex");
		knownGood.installation.freshness = "checking";
		knownGood.authentication.freshness = "stale";

		const [option] = buildRankedAgentOptions({
			agents: [knownGood],
			priorityRank,
			fallbackAgents: [],
		});

		expect(option).toMatchObject({ disabled: false, status: "" });
	});
});
