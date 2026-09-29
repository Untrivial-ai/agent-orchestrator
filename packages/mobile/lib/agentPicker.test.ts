import { describe, expect, it } from "vitest";
import { availabilityOf, defaultAgent, isSelectable, rankAgents, statusLabel } from "./agentPicker";
import type { AgentCatalog, AgentInfo } from "./api";

const agent = (id: string, label = id): AgentInfo => ({ id, label });

const catalog = (over: Partial<AgentCatalog> = {}): AgentCatalog => ({
	supported: [],
	installed: [],
	authorized: [],
	...over,
});

describe("availabilityOf", () => {
	it("reports an agent that is installed and authorized", () => {
		const c = catalog({ installed: [agent("codex")], authorized: [agent("codex")] });
		expect(availabilityOf(agent("codex"), c)).toBe("authorized");
	});

	it("accepts authStatus as proof of authorization", () => {
		const c = catalog({ installed: [{ id: "codex", label: "Codex", authStatus: "authorized" }] });
		expect(availabilityOf(agent("codex"), c)).toBe("authorized");
	});

	it("reports a missing install", () => {
		expect(availabilityOf(agent("goose"), catalog())).toBe("needs-install");
	});

	it("reports an explicit refusal as needing auth", () => {
		const c = catalog({ installed: [{ id: "cursor", label: "Cursor", authStatus: "unauthorized" }] });
		expect(availabilityOf(agent("cursor"), c)).toBe("needs-auth");
	});

	// Absent authStatus means the daemon could not determine credential state,
	// which is not the same as knowing the agent is unusable.
	it("treats an absent or unknown authStatus as unknown, not unauthorized", () => {
		expect(availabilityOf(agent("amp"), catalog({ installed: [agent("amp")] }))).toBe("auth-unknown");
		const known = catalog({ installed: [{ id: "amp", label: "Amp", authStatus: "unknown" }] });
		expect(availabilityOf(agent("amp"), known)).toBe("auth-unknown");
	});

	it("treats configured credentials as unverified", () => {
		const c = catalog({ installed: [{ id: "claude-code", label: "Claude Code", authStatus: "configured" }] });
		expect(availabilityOf(agent("claude-code"), c)).toBe("auth-unknown");
	});
});

describe("isSelectable", () => {
	// The load-bearing case: refusing an agent because the daemon could not probe
	// it would block one that works.
	it("allows an agent whose auth state is unknown", () => {
		expect(isSelectable("auth-unknown")).toBe(true);
	});

	it("allows an authorized agent", () => {
		expect(isSelectable("authorized")).toBe(true);
	});

	it("refuses an uninstalled or explicitly unauthorized agent", () => {
		expect(isSelectable("needs-auth")).toBe(false);
		expect(isSelectable("needs-install")).toBe(false);
	});
});

describe("statusLabel", () => {
	// A badge on every row would make the healthy majority look like warnings.
	it("says nothing about a healthy agent", () => {
		expect(statusLabel("authorized")).toBe("");
	});

	it("names each problem", () => {
		expect(statusLabel("auth-unknown")).toBe("Auth unknown");
		expect(statusLabel("needs-auth")).toBe("Needs auth");
		expect(statusLabel("needs-install")).toBe("Needs install");
	});
});

describe("rankAgents", () => {
	it("orders usable agents above unusable ones", () => {
		const c = catalog({
			supported: [agent("goose"), agent("cursor"), agent("amp"), agent("codex")],
			installed: [
				agent("codex"),
				{ id: "cursor", label: "cursor", authStatus: "unauthorized" },
				agent("amp"),
			],
			authorized: [agent("codex")],
		});
		expect(rankAgents(c).map((a) => a.id)).toEqual(["codex", "amp", "cursor", "goose"]);
	});

	// Desktop breaks ties with DEFAULT_AGENT_PRIORITY before the label. Without
	// it the authorized group is alphabetical and Aider outranks Claude Code.
	it("breaks ties by priority, not alphabetically", () => {
		const ids = ["aider", "claude-code", "codex"];
		const c = catalog({
			supported: ids.map((id) => agent(id)),
			installed: ids.map((id) => agent(id)),
			authorized: ids.map((id) => agent(id)),
		});
		expect(rankAgents(c).map((a) => a.id)).toEqual(["claude-code", "codex", "aider"]);
	});

	it("falls back to the label for agents outside the priority list", () => {
		const ids = ["zed", "kiro", "droid"];
		const c = catalog({
			supported: ids.map((id) => agent(id)),
			installed: ids.map((id) => agent(id)),
			authorized: ids.map((id) => agent(id)),
		});
		expect(rankAgents(c).map((a) => a.id)).toEqual(["droid", "kiro", "zed"]);
	});

	it("carries the status and selectability onto each row", () => {
		const c = catalog({ supported: [agent("goose")] });
		expect(rankAgents(c)[0]).toMatchObject({ status: "Needs install", selectable: false });
	});

	it("returns nothing for an absent catalog", () => {
		expect(rankAgents(null)).toEqual([]);
		expect(rankAgents(undefined)).toEqual([]);
		expect(rankAgents(catalog())).toEqual([]);
	});
});

describe("defaultAgent", () => {
	it("preselects the best usable agent", () => {
		const c = catalog({
			supported: [agent("goose"), agent("codex")],
			installed: [agent("codex")],
			authorized: [agent("codex")],
		});
		expect(defaultAgent(rankAgents(c))).toBe("codex");
	});

	// Preselecting one would put the form in a state the user cannot submit.
	it("preselects nothing when no agent is usable", () => {
		const c = catalog({ supported: [agent("goose"), agent("aider")] });
		expect(defaultAgent(rankAgents(c))).toBeNull();
	});
});
