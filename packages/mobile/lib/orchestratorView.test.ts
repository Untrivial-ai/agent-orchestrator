import { describe, expect, it } from "vitest";
import type { DashboardSession, OrchestratorLink } from "./api";
import {
	launchIntent,
	orchestratorState,
	orchestratorStatus,
	workersOf,
	zoneCounts,
} from "./orchestratorView";
import { darkTheme, lightTheme } from "./theme";

const link = (over: Partial<OrchestratorLink> = {}): OrchestratorLink => ({
	id: "proj-orchestrator",
	projectId: "proj",
	projectName: "proj",
	mode: "chat",
	...over,
});

const session = (over: Partial<DashboardSession> = {}): DashboardSession =>
	({ id: "proj-1", projectId: "proj", status: null, ...over }) as DashboardSession;

describe("orchestratorState", () => {
	it("reports missing when there is no link at all", () => {
		expect(orchestratorState(null)).toBe("missing");
		expect(orchestratorState(undefined)).toBe("missing");
		expect(orchestratorState(link({ id: "" }))).toBe("missing");
	});

	it("reports stopped only when explicitly flagged", () => {
		expect(orchestratorState(link({ hasRuntime: false }))).toBe("stopped");
		expect(orchestratorState(link({ isTerminal: true }))).toBe("stopped");
	});

	// The daemon derives both flags from one `isTerminated` boolean, and a build
	// that omits them must not read as dead — that would show "Start" beside a
	// running orchestrator and offer to replace it.
	it("treats a link with neither flag as running", () => {
		expect(orchestratorState(link())).toBe("running");
	});
});

describe("launchIntent", () => {
	// The regression this file exists for. SpawnOrchestrator treats clean:false
	// as an idempotent ensure — `if len(existing) > 0 { return newestSession }` —
	// so "Restart" on a live orchestrator sent clean:false and did nothing.
	it("sends clean:true when restarting a running orchestrator", () => {
		const intent = launchIntent("running");
		expect(intent.clean).toBe(true);
		expect(intent.label).toMatch(/restart/i);
	});

	// clean:true retires every live orchestrator with a notice telling it to stop
	// coordinating work. One unguarded tap should not do that.
	it("requires confirmation for the destructive path, and only that path", () => {
		expect(launchIntent("running").confirm).toBe(true);
		expect(launchIntent("stopped").confirm).toBe(false);
		expect(launchIntent("missing").confirm).toBe(false);
	});

	it("uses the cheap ensure when there is nothing live to retire", () => {
		for (const state of ["missing", "stopped"] as const) {
			expect(launchIntent(state).clean, state).toBe(false);
		}
	});

	// The card only ever offers a launch when the orchestrator is not running
	// (running shows Open alone), so these two labels are the only ones a user
	// can actually tap — and they must not both read "Start". An orchestrator
	// that exited needs restarting; one that never existed needs starting.
	// Asserted exactly, not with /start/i: "Restart" contains "start", so a
	// loose match passes whichever label is wrong.
	it("says Start only when there is no orchestrator, and Restart when one exited", () => {
		expect(launchIntent("missing").label).toBe("Start orchestrator");
		expect(launchIntent("stopped").label).toBe("Restart orchestrator");
	});
});

describe("orchestratorStatus", () => {
	it("names the two non-running states without inventing a status", () => {
		expect(orchestratorStatus(darkTheme, null).label).toBe("Not started");
		expect(orchestratorStatus(darkTheme, link({ isTerminal: true })).label).toBe("Stopped");
	});

	it("defers to the shared status vocabulary while running", () => {
		expect(orchestratorStatus(darkTheme, link({ status: "working" })).label).toBe("Working");
		expect(orchestratorStatus(darkTheme, link({ status: "needs_input" })).label).toBe("Needs input");
	});

	it("falls back to Online when the daemon sent no status", () => {
		expect(orchestratorStatus(darkTheme, link()).label).toBe("Online");
	});

	it("takes its colours from the passed theme", () => {
		const a = orchestratorStatus(lightTheme, link({ status: "working" }));
		const b = orchestratorStatus(darkTheme, link({ status: "working" }));
		expect(a.color).not.toBe(b.color);
	});

	it("only breathes for a live, working orchestrator", () => {
		expect(orchestratorStatus(darkTheme, link({ status: "working" })).breathing).toBe(true);
		expect(orchestratorStatus(darkTheme, link({ status: "idle" })).breathing).toBe(false);
		expect(orchestratorStatus(darkTheme, null).breathing).toBe(false);
	});
});

describe("workersOf", () => {
	// There is no parentId on the wire — sessions relate to an orchestrator only
	// by sharing a project.
	it("takes every session in the project", () => {
		const all = [session({ id: "a" }), session({ id: "b" }), session({ id: "x", projectId: "other" })];
		expect(workersOf(all, "proj", null).map((s) => s.id)).toEqual(["a", "b"]);
	});

	it("never counts the orchestrator as one of its own workers", () => {
		const all = [session({ id: "proj-orchestrator" }), session({ id: "a" })];
		expect(workersOf(all, "proj", link()).map((s) => s.id)).toEqual(["a"]);
	});
});

describe("zoneCounts", () => {
	it("buckets by attention zone", () => {
		const counts = zoneCounts([
			session({ status: "working" }),
			session({ status: "working" }),
			session({ status: "needs_input" }),
		]);
		expect(counts.working).toBe(2);
		expect(counts.respond).toBe(1);
	});

	it("is empty for no sessions", () => {
		expect(zoneCounts([])).toEqual({});
	});
});
