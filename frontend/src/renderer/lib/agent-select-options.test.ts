import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import { buildRankedAgentOptions, defaultAuthorizedAgentForRole, type RoleSession } from "./agent-select-options";

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

describe("defaultAuthorizedAgentForRole", () => {
	const agents = [agent("claude-code"), agent("codex")];

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

	it("infers each role from its own history", () => {
		const sessions = [
			session("codex", "worker", hoursAgo(5)),
			session("codex", "worker", hoursAgo(4)),
			session("claude-code", "worker", hoursAgo(3)),
			session("claude-code", "orchestrator", hoursAgo(2)),
		];

		expect(defaultAuthorizedAgentForRole(agents, sessions, "worker")).toBe("codex");
		expect(defaultAuthorizedAgentForRole(agents, sessions, "orchestrator")).toBe("claude-code");
	});

	it("breaks equal counts by the newest session", () => {
		const sessions = [session("claude-code", "worker", hoursAgo(2)), session("codex", "worker", hoursAgo(1))];

		expect(defaultAuthorizedAgentForRole(agents, sessions, "worker")).toBe("codex");
	});

	it("ignores sessions older than 48 hours", () => {
		const sessions = [
			session("codex", "worker", hoursAgo(72)),
			session("codex", "worker", hoursAgo(100)),
			session("codex", "worker", hoursAgo(200)),
			session("claude-code", "worker", hoursAgo(1)),
		];

		expect(defaultAuthorizedAgentForRole(agents, sessions, "worker")).toBe("claude-code");
		expect(defaultAuthorizedAgentForRole(agents, [session("codex", "worker", hoursAgo(72))], "worker")).toBe(
			"claude-code",
		);
	});

	it("ignores sessions without timestamps", () => {
		expect(
			defaultAuthorizedAgentForRole(agents, [{ id: "w1", provider: "codex", kind: "worker" }], "worker"),
		).toBe("claude-code");
	});

	it("counts legacy orchestrator ids and kind-less sessions like the board does", () => {
		expect(
			defaultAuthorizedAgentForRole(
				agents,
				[{ id: "abc-orchestrator", provider: "codex", createdAt: hoursAgo(2) }],
				"orchestrator",
			),
		).toBe("codex");
		expect(
			defaultAuthorizedAgentForRole(
				agents,
				[session("codex", undefined, hoursAgo(2)), session("codex", undefined, hoursAgo(1))],
				"worker",
			),
		).toBe("codex");
	});

	it("skips unavailable historical winners and falls back to Claude Code", () => {
		const sessions = [
			session("goose", "worker", hoursAgo(3)),
			session("goose", "worker", hoursAgo(2)),
			session("codex", "worker", hoursAgo(1)),
		];

		expect(defaultAuthorizedAgentForRole(agents, sessions, "worker")).toBe("codex");
		expect(defaultAuthorizedAgentForRole(agents, [], "worker")).toBe("claude-code");
	});

	it("stays usable when Claude Code is unavailable", () => {
		expect(defaultAuthorizedAgentForRole([agent("codex")], [], "worker")).toBe("codex");
		expect(defaultAuthorizedAgentForRole([], [], "worker")).toBe("");
	});
});
