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

describe("configured but unverified credentials", () => {
	// The whole point of the "configured" state is that it must not look like
	// a working credential. Falling through to the known-good branch would
	// paint an unvalidated — possibly revoked — credential green.
	it("shows an unverified agent as neutral rather than known-good", () => {
		const [option] = buildRankedAgentOptions({
			agents: [agent("claude-code", "installed", "configured")],
			priorityRank,
			fallbackAgents: [],
		});
		expect(option.statusTone).not.toBe("success");
		expect(option.status).toBe("Unverified");
	});

	// I1: never block. An unverified credential is not a disproved one.
	it("keeps an unverified agent selectable and ranked with unknown, not below it", () => {
		const options = buildRankedAgentOptions({
			agents: [
				agent("claude-code", "installed", "configured"),
				agent("codex", "installed", "unknown"),
			],
			priorityRank,
			fallbackAgents: [],
		});
		const unverified = options.find((option) => option.id === "claude-code")!;
		const unknown = options.find((option) => option.id === "codex")!;
		expect(unverified.disabled).toBe(false);
		expect(unverified.rank).toBe(unknown.rank);
	});

	it("still disables an agent the provider definitely rejected", () => {
		const [option] = buildRankedAgentOptions({
			agents: [agent("claude-code", "installed", "unauthorized")],
			priorityRank,
			fallbackAgents: [],
		});
		expect(option.disabled).toBe(true);
	});
});
