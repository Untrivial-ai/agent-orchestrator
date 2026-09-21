import { describe, expect, it } from "vitest";
import type { DashboardPR, DashboardSession } from "./api";
import {
	BOARD_ZONES,
	boardZoneOf,
	groupSessions,
	kanbanColumnOf,
	workerStatusGlyph,
	isArchived,
	prLine,
	showBranch,
	trackerIssueId,
	workerRowPresentation,
	zoneMeta,
} from "./agentsView";
import { darkTheme, lightTheme } from "./theme";

type SortableSession = Partial<DashboardSession> & { isPinned?: boolean; pinnedAt?: string | null };

const session = (over: SortableSession = {}): DashboardSession =>
	({ id: "proj-1", projectId: "proj", status: null, lastActivityAt: "", ...over }) as DashboardSession;

const pr = (over: Partial<DashboardPR> = {}): DashboardPR => ({ number: 1, url: "", state: "open", ...over });

describe("kanbanColumnOf", () => {
	// The daemon derives the column from facts the client cannot see — whether
	// AO's review pass is mid-run, whether auto-inject is configured — so its
	// placement wins over anything re-derived here.
	it("trusts the daemon's column over the local fallback", () => {
		expect(kanbanColumnOf(session({ status: "working", kanbanColumn: "needs_review" }))).toBe("needs_review");
		expect(kanbanColumnOf(session({ status: "mergeable", kanbanColumn: "validating" }))).toBe("validating");
	});

	// Only for a daemon too old to send the field.
	it("falls back to deriving from status", () => {
		expect(kanbanColumnOf(session({ status: "mergeable" }))).toBe("ready");
		expect(kanbanColumnOf(session({ status: "pr_open" }))).toBe("validating");
		expect(kanbanColumnOf(session({ status: "ci_failed" }))).toBe("needs_review");
		expect(kanbanColumnOf(session({ status: "working" }))).toBe("building");
		expect(kanbanColumnOf(session({ status: null }))).toBe("building");
	});
});

describe("boardZoneOf", () => {
	it("puts the sections a person owns ahead of the ones a machine owns", () => {
		expect(BOARD_ZONES).toEqual(["needs_you", "needs_review", "ready", "building", "validating"]);
	});

	// The one deliberate deviation from desktop. A worker blocked on a reply has
	// no PR, so desktop files it under Building with every other running agent.
	// That is right for a pipeline and wrong for a phone.
	it("lifts an agent blocked on a person above its delivery column", () => {
		expect(boardZoneOf(session({ status: "needs_input", kanbanColumn: "building" }))).toBe("needs_you");
		expect(boardZoneOf(session({ status: "stuck" }))).toBe("needs_you");
		expect(boardZoneOf(session({ status: "errored" }))).toBe("needs_you");
		expect(boardZoneOf(session({ status: "exited" }))).toBe("needs_you");
		expect(boardZoneOf(session({ status: "working", displayStatus: "Blocked" }))).toBe("needs_you");
	});

	// PR facts stay where the daemon put them: it already decided whether AO or a
	// person owns the next turn, and splitting one PR's lifecycle across two
	// sections would second-guess that.
	it("leaves PR-level states in their daemon column", () => {
		expect(boardZoneOf(session({ status: "ci_failed", kanbanColumn: "validating" }))).toBe("validating");
		expect(boardZoneOf(session({ status: "changes_requested", kanbanColumn: "needs_review" }))).toBe("needs_review");
		expect(boardZoneOf(session({ status: "mergeable", kanbanColumn: "ready" }))).toBe("ready");
	});

	// Terminated runtimes are routed to the archive strip before grouping, so the
	// column never has to render as a section.
	it("never yields an archive section", () => {
		expect(boardZoneOf(session({ status: "working", kanbanColumn: "archive" }))).toBe("building");
	});
});

describe("workerStatusGlyph", () => {
	// The row tints its status label by colour alone, which a colour-blind reader
	// cannot use. The glyph is a second channel for the same fact.
	it("gives distinct shapes to the states a person acts on", () => {
		expect(workerStatusGlyph("needs_input")).toBe("message-square");
		expect(workerStatusGlyph("ci_failed")).toBe("x-octagon");
		expect(workerStatusGlyph("stuck")).toBe("alert-circle");
		expect(workerStatusGlyph("mergeable")).toBe("check-circle");
	});

	it("separates blocked-on-you from broken", () => {
		expect(workerStatusGlyph("needs_input")).not.toBe(workerStatusGlyph("errored"));
	});

	it("marks quiet and busy states differently", () => {
		expect(workerStatusGlyph("working")).toBe("loader");
		expect(workerStatusGlyph("idle")).toBe("moon");
	});

	// A generic glyph on an unknown status is noise pretending to be signal.
	it("returns nothing when the status says nothing specific", () => {
		expect(workerStatusGlyph(null)).toBeNull();
		expect(workerStatusGlyph(undefined)).toBeNull();
		expect(workerStatusGlyph("unknown")).toBeNull();
		expect(workerStatusGlyph("no_signal")).toBeNull();
	});
});

describe("zoneMeta", () => {
	it("uses mobile's order with desktop's column labels", () => {
		expect(BOARD_ZONES.map((z) => zoneMeta(darkTheme, z).label)).toEqual([
			"Needs you",
			"In review",
			"Ready",
			"Building",
			"Validating",
		]);
	});

	it("takes its colours from the passed theme", () => {
		expect(zoneMeta(lightTheme, "ready").color).not.toBe(zoneMeta(darkTheme, "ready").color);
	});
});

describe("isArchived", () => {
	// Desktop's rule is about a dead runtime, not a finished outcome.
	it("archives a terminated runtime", () => {
		expect(isArchived(session({ isTerminated: true }))).toBe(true);
		expect(isArchived(session({ status: "terminated" }))).toBe(true);
	});

	// The distinction that makes this worth its own function: `attentionOf`
	// buckets a merged session as "done", but if its agent is still running it
	// belongs on the board, not in the archive.
	it("keeps a merged session whose runtime is still alive", () => {
		expect(isArchived(session({ status: "merged" }))).toBe(false);
		expect(isArchived(session({ status: "done" }))).toBe(false);
	});

	it("keeps ordinary live sessions", () => {
		expect(isArchived(session({ status: "working" }))).toBe(false);
		expect(isArchived(session())).toBe(false);
	});
});

describe("groupSessions", () => {
	it("lifts pinned live workers into a dedicated top section without duplicating them", () => {
		const { pinned, sections } = groupSessions(darkTheme, [
			session({ id: "pinned", status: "working", isPinned: true }),
			session({ id: "working", status: "working" }),
		]);
		expect(pinned.map((item) => item.id)).toEqual(["pinned"]);
		expect(sections.flatMap((section) => section.data).map((item) => item.id)).toEqual(["working"]);
	});

	it("splits the board from the archive", () => {
		const { sections, archived } = groupSessions(darkTheme, [
			session({ id: "a", status: "working" }),
			session({ id: "b", status: "needs_input" }),
			session({ id: "z", isTerminated: true }),
		]);
		expect(sections.map((s) => s.zone)).toEqual(["needs_you", "building"]);
		expect(archived.map((s) => s.id)).toEqual(["z"]);
	});

	// A run of empty section titles is most of a phone screen.
	it("drops empty zones rather than rendering empty headers", () => {
		const { sections } = groupSessions(darkTheme, [session({ status: "idle" })]);
		expect(sections).toHaveLength(1);
		// Desktop folds idle and working into one lane; the distinction survives on
		// the row's own status text rather than as a section of its own.
		expect(sections[0].label).toBe("Building");
	});

	it("keeps sections in board order regardless of input order", () => {
		const { sections } = groupSessions(darkTheme, [
			session({ id: "m", status: "mergeable" }),
			session({ id: "w", status: "working" }),
			session({ id: "p", status: "pr_open" }),
		]);
		expect(sections.map((s) => s.zone)).toEqual(["ready", "building", "validating"]);
	});

	it("separates pinned sessions from their normal section", () => {
		const { pinned, sections } = groupSessions(darkTheme, [
			session({ id: "recent", status: "working", lastActivityAt: "2026-08-09T10:00:00Z" }),
			session({ id: "pinned", status: "working", isPinned: true, lastActivityAt: "2026-01-01T00:00:00Z" }),
		]);
		expect(pinned.map((s) => s.id)).toEqual(["pinned"]);
		expect(sections[0].data.map((s) => s.id)).toEqual(["recent"]);
	});

	// Sections a person owns read oldest-first, so whatever has waited longest is
	// at the top. Sections a machine is turning read newest-first, because there
	// the interesting one is whatever just moved.
	it.each([
		["Needs you", "needs_input", ["old", "new"]],
		["Ready", "mergeable", ["old", "new"]],
		["Building", "working", ["new", "old"]],
		["Validating", "pr_open", ["new", "old"]],
		["Building (idle)", "idle", ["new", "old"]],
	] as const)("orders %s sessions by the useful activity direction", (_label, status, expected) => {
		const { sections } = groupSessions(darkTheme, [
			session({ id: "new", status, lastActivityAt: "2026-08-09T10:00:00Z" }),
			session({ id: "old", status, lastActivityAt: "2026-01-01T00:00:00Z" }),
		]);
		expect(sections[0].data.map((s) => s.id)).toEqual(expected);
	});

	it("preserves daemon session-number order when priority and activity tie", () => {
		const { sections } = groupSessions(darkTheme, [
			session({ id: "worker-2", status: "working", lastActivityAt: "2026-08-09T10:00:00Z" }),
			session({ id: "worker-10", status: "working", lastActivityAt: "2026-08-09T10:00:00Z" }),
		]);
		expect(sections[0].data.map((s) => s.id)).toEqual(["worker-2", "worker-10"]);
	});

	it("sorts the archive newest first", () => {
		const { archived } = groupSessions(darkTheme, [
			session({ id: "old", isTerminated: true, lastActivityAt: "2026-01-01T00:00:00Z" }),
			session({ id: "new", isTerminated: true, lastActivityAt: "2026-07-01T00:00:00Z" }),
		]);
		expect(archived.map((s) => s.id)).toEqual(["new", "old"]);
	});

	it("puts pinned archive sessions before newer unpinned history", () => {
		const { archived } = groupSessions(darkTheme, [
			session({ id: "new", isTerminated: true, lastActivityAt: "2026-07-01T00:00:00Z" }),
			session({ id: "pinned", isTerminated: true, isPinned: true, lastActivityAt: "2026-01-01T00:00:00Z" }),
		]);
		expect(archived.map((s) => s.id)).toEqual(["pinned", "new"]);
	});

	it("returns nothing for an empty board", () => {
		expect(groupSessions(darkTheme, [])).toEqual({ pinned: [], sections: [], archived: [] });
	});
});

describe("workerRowPresentation", () => {
	it("uses a live status for active work and keeps branch and project metadata compact", () => {
		const row = workerRowPresentation(
			darkTheme,
			session({
				id: "worker-7",
				status: "working",
				displayName: "Make remote coding feel local",
				branch: "feat/remote-command-center",
				lastActivityAt: "2026-09-02T10:55:00Z",
			}),
			"Moonbase Terminal",
			Date.parse("2026-09-02T11:00:00Z"),
		);

		expect(row).toEqual({
			title: "Make remote coding feel local",
			project: "Moonbase Terminal",
			branch: "feat/remote-command-center",
			trailing: "Working",
			trailingKind: "status",
		});
	});

	// A standalone agent session omits projectId on the wire. The missing value
	// reached `.length` inside render and crashed the entire board, so this is a
	// regression guard, not a cosmetic assertion.
	it("labels a session with no project rather than throwing", () => {
		const row = workerRowPresentation(
			darkTheme,
			session({ id: "worker-9", projectId: "", status: "working", displayName: "No project here" }),
			undefined,
			Date.parse("2026-09-02T11:00:00Z"),
		);

		expect(row.project).toBe("Standalone");
	});

	it("uses elapsed time for an idle worker and falls back to the compact project id", () => {
		const row = workerRowPresentation(
			darkTheme,
			session({
				id: "worker-8",
				projectId: "agent-orchestrator-mobile_98d163a851",
				status: "idle",
				displayName: "Polish the handoff",
				branch: null,
				lastActivityAt: "2026-09-02T10:18:00Z",
			}),
			undefined,
			Date.parse("2026-09-02T11:00:00Z"),
		);

		expect(row).toEqual({
			title: "Polish the handoff",
			project: "agent-orch…8d163a851",
			branch: null,
			trailing: "42m",
			trailingKind: "time",
		});
	});
});

describe("showBranch", () => {
	it("shows a branch that adds information", () => {
		expect(showBranch("fix/auth-timeouts", "Fix auth timeouts on refresh")).toBe(true);
	});

	it("hides a branch that merely repeats the title", () => {
		expect(showBranch("fix/auth-timeouts", "auth timeouts")).toBe(false);
		expect(showBranch("feat/add-login", "Add Login")).toBe(false);
	});

	// The rule this was narrowed to, twice. AO names worktree branches
	// `ao/<session-id>/<slug>`; earlier versions normalised that scaffolding away
	// and so hid the branch on every unnamed session. But it IS the worktree, the
	// card names it nowhere else, and desktop's sameLabel has no knowledge of
	// `ao/` so desktop shows it.
	it("keeps AO worktree branches, named session or not", () => {
		expect(showBranch("ao/agent-orchestrator-mo-17/root", "mobile-ui-revamp")).toBe(true);
		expect(showBranch("ao/meetyou-2/chat-experience", "chat-ux")).toBe(true);
		expect(showBranch("ao/meetyou-7/root", "meetyou-7")).toBe(true);
		expect(showBranch("ao/precision-market-19/root", "precision-market-19")).toBe(true);
	});

	it("hides an absent branch", () => {
		expect(showBranch(null, "t")).toBe(false);
		expect(showBranch("  ", "t")).toBe(false);
	});
});

describe("prLine", () => {
	it("renders nothing when there is no PR", () => {
		expect(prLine(session())).toBeNull();
		expect(prLine(session({ prs: [] }))).toBeNull();
	});

	it("ignores placeholder PRs with no real number", () => {
		expect(prLine(session({ prs: [pr({ number: 0 })] }))).toBeNull();
	});

	it("groups by lifecycle, the way the desktop board card does", () => {
		const line = prLine(session({ prs: [pr({ number: 12 }), pr({ number: 13 })] }));
		expect(line?.text).toBe("PR #12, #13 open");
	});

	it("keeps separate lifecycles apart", () => {
		const line = prLine(session({ prs: [pr({ number: 12 }), pr({ number: 9, state: "merged" })] }));
		expect(line?.text).toBe("PR #12 open · #9 merged");
	});

	it("falls back to the single `pr` field", () => {
		expect(prLine(session({ pr: pr({ number: 4 }) }))?.text).toBe("PR #4 open");
	});

	it("takes its tone from the worst lifecycle present", () => {
		expect(prLine(session({ prs: [pr({ number: 1, state: "closed" })] }))?.tone).toBe("error");
		expect(prLine(session({ prs: [pr({ number: 1 })] }))?.tone).toBe("success");
	});
});

describe("trackerIssueId", () => {
	it("keeps a provider-prefixed tracker reference", () => {
		expect(trackerIssueId("github:123")).toBe("github:123");
	});

	// The daemon stores issueId verbatim from whatever spawn passed — no
	// validation — so a hand-created session carries the task name typed at
	// spawn. Rendering that in a 10pt mono chip put a sentence in the smallest
	// text on the card; desktop hides it, and so do we.
	it("rejects the free text a manually created session carries", () => {
		expect(trackerIssueId("onboarding")).toBeNull();
		expect(trackerIssueId("say hi back to me")).toBeNull();
	});

	it("rejects an absent or blank id", () => {
		expect(trackerIssueId(null)).toBeNull();
		expect(trackerIssueId(undefined)).toBeNull();
		expect(trackerIssueId("   ")).toBeNull();
	});
});
