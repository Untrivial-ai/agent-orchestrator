import { describe, expect, it } from "vitest";
import type { DashboardSession, OrchestratorLink, ProjectInfo } from "./api";
import {
	launchIntent,
	orchestratorProjectSections,
	orchestratorRowAccessibilityLabel,
	orchestratorState,
	orchestratorStatus,
	orchestratorWorkerAccessibilityLabel,
	orchestratorWorkerPreviews,
	orchestratorButtonCopy,
	projectBlockerLine,
	projectCardSummary,
	projectDetailSessions,
	projectPageStats,
	projectRailTone,
	projectRowChips,
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

const project = (id: string): ProjectInfo => ({ id, name: id });

const rowByProject = (
	sections: ReturnType<typeof orchestratorProjectSections>,
	projectId: string,
) => {
	const row = sections.flatMap((section) => section.data).find((candidate) => candidate.project.id === projectId);
	if (!row) throw new Error(`Missing project row ${projectId}`);
	return row;
};

describe("projectRowChips", () => {
	const rowFor = (sessions: DashboardSession[]) =>
		rowByProject(orchestratorProjectSections([project("proj")], sessions, [link()]), "proj");

	it("promotes the counts that detailFor spends on prose", () => {
		const chips = projectRowChips(
			rowFor([
				session({ id: "a", status: "needs_input" }),
				session({ id: "b", status: "needs_input" }),
				session({ id: "c", status: "ci_failed" }),
			]),
		);
		expect(chips.map((chip) => chip.id)).toEqual(["needs-you", "failing"]);
		expect(chips[0]).toEqual({ id: "needs-you", label: "2 need you", tone: "attention" });
	});

	it("says 'needs' for one and 'need' for many", () => {
		const one = projectRowChips(rowFor([session({ id: "a", status: "needs_input" })]));
		expect(one[0].label).toBe("1 needs you");
	});

	// A row should carry only facts that are true of it — "0 failing" is noise.
	it("drops zero counts rather than rendering them", () => {
		const chips = projectRowChips(rowFor([session({ id: "a", status: "running" })]));
		expect(chips.every((chip) => !chip.label.startsWith("0"))).toBe(true);
	});

	it("never returns more than three, keeping the most urgent", () => {
		const chips = projectRowChips(
			rowFor([
				session({ id: "a", status: "needs_input" }),
				session({ id: "b", status: "ci_failed" }),
				session({ id: "c", status: "mergeable" }),
				session({ id: "d", status: "running" }),
			]),
		);
		expect(chips.length).toBeLessThanOrEqual(3);
		expect(chips[0].id).toBe("needs-you");
	});

	it("shows nothing for a project whose orchestrator is not running", () => {
		const rows = orchestratorProjectSections([project("proj")], [], []);
		expect(projectRowChips(rowByProject(rows, "proj"))).toEqual([]);
	});
});

describe("projectBlockerLine", () => {
	const now = Date.parse("2026-01-01T12:00:00.000Z");
	const ago = (mins: number) => new Date(now - mins * 60_000).toISOString();

	// The bug this function exists for: the attention section suppressed `detail`,
	// so the most urgent rows were the ones saying the least.
	it("names the worker, why it is blocked, and for how long", () => {
		const rows = orchestratorProjectSections(
			[project("proj")],
			[session({ id: "auth-refactor", displayName: "auth-refactor", status: "needs_input", lastActivityAt: ago(12) })],
			[link()],
		);
		expect(projectBlockerLine(rowByProject(rows, "proj"), now)).toEqual({
			worker: "auth-refactor",
			reason: "waiting on your reply",
			age: "12m",
		});
	});

	it("names the worker that has been blocked longest", () => {
		const rows = orchestratorProjectSections(
			[project("proj")],
			[
				session({ id: "new", displayName: "new", status: "needs_input", lastActivityAt: ago(2) }),
				session({ id: "old", displayName: "old", status: "needs_input", lastActivityAt: ago(90) }),
			],
			[link()],
		);
		expect(projectBlockerLine(rowByProject(rows, "proj"), now)?.worker).toBe("old");
	});

	it("stays silent outside the attention section", () => {
		const rows = orchestratorProjectSections([project("proj")], [session({ id: "a", status: "running" })], [link()]);
		expect(projectBlockerLine(rowByProject(rows, "proj"), now)).toBeNull();
	});
});

describe("projectRailTone", () => {
	const toneFor = (sessions: DashboardSession[], links: OrchestratorLink[] = [link()]) =>
		projectRailTone(rowByProject(orchestratorProjectSections([project("proj")], sessions, links), "proj"));

	it("reports stopped when nothing is running", () => {
		expect(toneFor([], [])).toBe("stopped");
	});

	it("puts a blocked worker ahead of a busy one", () => {
		expect(toneFor([session({ id: "a", status: "running" }), session({ id: "b", status: "needs_input" })]))
			.toBe("attention");
	});

	it("reports review when checks are failing", () => {
		expect(toneFor([session({ id: "a", status: "ci_failed" })])).toBe("review");
	});

	it("reports working when work is merely in flight", () => {
		expect(toneFor([session({ id: "a", status: "running" })])).toBe("working");
	});

	it("reports idle for a running orchestrator with nothing to do", () => {
		expect(toneFor([])).toBe("idle");
	});
});

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

	it("shows a live orchestrator as Online when only its activity signal is unavailable", () => {
		const status = orchestratorStatus(darkTheme, link({ status: "no_signal", hasRuntime: true }));
		expect(status).toMatchObject({ label: "Online", color: darkTheme.green, breathing: false });
	});

	it("uses the healthy green treatment when a live orchestrator has no status yet", () => {
		expect(orchestratorStatus(darkTheme, link())).toMatchObject({
			label: "Online",
			color: darkTheme.green,
		});
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

describe("orchestratorProjectSections", () => {
	it("places every project once and derives open, start, and resume actions", () => {
		const projects = [project("attention"), project("coordinating"), project("missing"), project("stopped")];
		const orchestrators = [
			link({ id: "attention-orch", projectId: "attention", projectName: "attention", status: "needs_input" }),
			link({ id: "coordinating-orch", projectId: "coordinating", projectName: "coordinating", status: "working" }),
			link({ id: "stopped-orch", projectId: "stopped", projectName: "stopped", isTerminal: true }),
		];

		const sections = orchestratorProjectSections(projects, [], orchestrators);

		expect(sections.map((section) => section.title)).toEqual(["Needs Attention", "Coordinating", "Not Running"]);
		expect(sections.flatMap((section) => section.data).map((row) => row.project.id).sort()).toEqual([
			"attention",
			"coordinating",
			"missing",
			"stopped",
		]);
		expect(rowByProject(sections, "attention").action).toBe("open");
		expect(rowByProject(sections, "coordinating").action).toBe("open");
		expect(rowByProject(sections, "missing").action).toBe("start");
		expect(rowByProject(sections, "stopped").action).toBe("resume");
	});

	it("uses truthful attention facts with correct singular and plural copy", () => {
		const projects = [project("many"), project("single"), project("healthy"), project("missing")];
		const orchestrators = projects.slice(0, 3).map((p) =>
			link({ id: `${p.id}-orch`, projectId: p.id, projectName: p.name, status: "working" }),
		);
		const readyPR = {
			number: 17,
			url: "https://example.test/pull/17",
			state: "open" as const,
			ciStatus: "passing" as const,
			reviewDecision: "approved" as const,
			mergeability: { mergeable: true },
		};
		const sessions = [
			session({ id: "many-1", projectId: "many", status: "needs_input" }),
			session({ id: "many-2", projectId: "many", status: "needs_input" }),
			session({ id: "many-pr", projectId: "many", status: "mergeable", pr: readyPR }),
			session({ id: "single-1", projectId: "single", status: "needs_input" }),
			session({ id: "healthy-1", projectId: "healthy", status: "working" }),
			session({ id: "healthy-2", projectId: "healthy", status: "working" }),
			session({ id: "healthy-3", projectId: "healthy", status: "working" }),
		];

		const sections = orchestratorProjectSections(projects, sessions, orchestrators);

		expect(rowByProject(sections, "many").detail).toBe("2 workers need input · 1 pull request is ready");
		expect(rowByProject(sections, "single").detail).toBe("1 worker needs input");
		expect(rowByProject(sections, "healthy").detail).toBe("3 active workers");
		expect(rowByProject(sections, "missing").detail).toBe("Start one to coordinate work for this project");
	});

	it("does not count archived workers as current project activity", () => {
		const sections = orchestratorProjectSections(
			[project("proj")],
			[
				session({ id: "dead", status: "needs_input", isTerminated: true, lastActivityAt: "2026-09-04T08:00:00Z" }),
				session({ id: "live", status: "working", lastActivityAt: "2026-09-04T09:00:00Z" }),
			],
			[link({ status: "working", updatedAt: "2026-09-04T10:00:00Z" })],
		);

		expect(sections.map((section) => section.title)).toEqual(["Coordinating"]);
		expect(rowByProject(sections, "proj")).toMatchObject({
			detail: "1 active worker",
			activityAt: "2026-09-04T10:00:00Z",
		});
	});

	it("orders attention by urgency then oldest unresolved activity", () => {
		const projects = [project("merge"), project("new-input"), project("old-input"), project("crashed")];
		const orchestrators = projects.map((p) =>
			link({
				id: `${p.id}-orch`,
				projectId: p.id,
				projectName: p.name,
				status: p.id === "crashed" ? "errored" : "working",
				updatedAt: "2026-09-04T10:00:00Z",
			}),
		);
		const sessions = [
			session({ id: "merge-worker", projectId: "merge", status: "mergeable", lastActivityAt: "2026-09-04T07:00:00Z" }),
			session({ id: "new-worker", projectId: "new-input", status: "needs_input", lastActivityAt: "2026-09-04T09:00:00Z" }),
			session({ id: "old-worker", projectId: "old-input", status: "needs_input", lastActivityAt: "2026-09-04T08:00:00Z" }),
		];

		const [attention] = orchestratorProjectSections(projects, sessions, orchestrators);

		expect(attention.data.map((row) => row.project.id)).toEqual(["crashed", "old-input", "new-input", "merge"]);
	});

	it("orders healthy projects by latest activity with project order as the stable tie-breaker", () => {
		const projects = [project("first"), project("second"), project("newest")];
		const orchestrators = [
			link({ id: "first-orch", projectId: "first", projectName: "first", status: "working", updatedAt: "2026-09-04T08:00:00Z" }),
			link({ id: "second-orch", projectId: "second", projectName: "second", status: "working", updatedAt: "2026-09-04T08:00:00Z" }),
			link({ id: "newest-orch", projectId: "newest", projectName: "newest", status: "working", updatedAt: "2026-09-04T09:00:00Z" }),
		];

		const [coordinating] = orchestratorProjectSections(projects, [], orchestrators);

		expect(coordinating.data.map((row) => row.project.id)).toEqual(["newest", "first", "second"]);
	});
});

describe("orchestratorRowAccessibilityLabel", () => {
	it("describes the action available for each project row", () => {
		expect(orchestratorRowAccessibilityLabel("agent-orchestrator", "Needs input", "open")).toBe(
			"Open orchestrator for agent-orchestrator, Needs input",
		);
		expect(orchestratorRowAccessibilityLabel("landing-page", "Not started", "start")).toBe(
			"Start orchestrator for landing-page",
		);
		expect(orchestratorRowAccessibilityLabel("meetyou", "Stopped", "resume")).toBe(
			"Resume orchestrator for meetyou",
		);
	});
});

describe("orchestratorWorkerPreviews", () => {
	it("shows the three most recently active live workers with their meaningful status", () => {
		const previews = orchestratorWorkerPreviews([
			session({ id: "older", displayName: "Older audit", status: "idle", lastActivityAt: "2026-09-04T08:00:00Z" }),
			session({ id: "newest", displayName: "Fix search", status: "working", lastActivityAt: "2026-09-04T12:00:00Z" }),
			session({ id: "middle", issueId: "Review PR 4854", status: "needs_input", lastActivityAt: "2026-09-04T10:00:00Z" }),
			session({ id: "second", userPrompt: "Polish workers", status: "starting", lastActivityAt: "2026-09-04T11:00:00Z" }),
			session({ id: "archived", displayName: "Old worker", status: "working", isTerminated: true, lastActivityAt: "2026-09-04T13:00:00Z" }),
		]);

		expect(previews).toEqual([
			{ id: "newest", name: "Fix search", status: "working" },
			{ id: "second", name: "Polish workers", status: "spawning" },
			{ id: "middle", name: "Review PR 4854", status: "needs_input" },
		]);
	});

	it("describes a worker preview as a direct navigation action", () => {
		expect(orchestratorWorkerAccessibilityLabel({ id: "worker-1", name: "Fix search", status: "working" }, "Working")).toBe(
			"Open worker Fix search, Working",
		);
		expect(orchestratorWorkerAccessibilityLabel({ id: "worker-2", name: "Review PR", status: "needs_input" }, "Needs input")).toBe(
			"Open worker Review PR, Needs input",
		);
	});
});

describe("projectCardSummary", () => {
	const rowFor = (sessions: DashboardSession[]) =>
		rowByProject(orchestratorProjectSections([project("proj")], sessions, [link()]), "proj");

	it("counts the project's workers in plain words", () => {
		expect(projectCardSummary(rowFor([session({ id: "proj-1" }), session({ id: "proj-2" })])).workers).toBe("2 workers");
		expect(projectCardSummary(rowFor([session({ id: "proj-1" })])).workers).toBe("1 worker");
	});

	it("says so plainly when there are none", () => {
		expect(projectCardSummary(rowFor([])).workers).toBe("No workers");
	});

	// The one count worth colour, reported apart so the card can tint it alone.
	it("reports work waiting on a person separately", () => {
		const summary = projectCardSummary(rowFor([
			session({ id: "proj-1", status: "needs_input" }),
			session({ id: "proj-2", status: "working" }),
		]));
		expect(summary.needsYou).toBe(1);
	});
});

describe("orchestratorButtonCopy", () => {
	const rowWith = (action: "open" | "start" | "resume") => ({
		...rowByProject(orchestratorProjectSections([project("proj")], [], [link()]), "proj"),
		action,
	});

	it("names what the button will do", () => {
		expect(orchestratorButtonCopy(rowWith("open"), false)).toEqual({ label: "Open orchestrator", short: "Orchestrator", running: true });
		expect(orchestratorButtonCopy(rowWith("start"), false)).toEqual({ label: "Start orchestrator", short: "Start", running: false });
		expect(orchestratorButtonCopy(rowWith("resume"), false)).toEqual({ label: "Resume orchestrator", short: "Resume", running: false });
	});

	it("says it is working while a launch is in flight", () => {
		expect(orchestratorButtonCopy(rowWith("start"), true).label).toBe("Starting…");
		expect(orchestratorButtonCopy(rowWith("resume"), true).label).toBe("Resuming…");
	});
});

describe("projectDetailSessions", () => {
	// A project's own page is where its history belongs, so finished work stays.
	it("keeps archived sessions alongside live ones", () => {
		const sessions = [
			session({ id: "a", projectId: "proj", status: "working" }),
			session({ id: "b", projectId: "proj", status: "terminated", isTerminated: true }),
			session({ id: "c", projectId: "other" }),
		];
		expect(projectDetailSessions("proj", sessions).map((item) => item.id)).toEqual(["a", "b"]);
	});
});

describe("projectPageStats", () => {
	it("counts live workers by board zone and archived ones apart", () => {
		const stats = projectPageStats([
			session({ id: "a", status: "working" }),
			session({ id: "b", status: "needs_input" }),
			session({ id: "c", status: "terminated", isTerminated: true }),
		]);
		expect(stats.workers).toBe(2);
		expect(stats.archived).toBe(1);
		expect(stats.needsYou).toBe(1);
	});

	it("counts an orchestrator that is waiting on you", () => {
		const sessions = [session({ id: "a", status: "needs_input" })];
		expect(projectPageStats(sessions, link({ status: "needs_input" })).needsYou).toBe(2);
		expect(projectPageStats(sessions, link({ status: "working" })).needsYou).toBe(1);
	});
});
