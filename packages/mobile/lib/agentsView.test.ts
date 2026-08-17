import { describe, expect, it } from "vitest";
import type { DashboardPR, DashboardSession } from "./api";
import {
	BOARD_ZONES,
	boardZoneOf,
	groupSessions,
	isArchived,
	prLine,
	showBranch,
	trackerIssueId,
	zoneMeta,
} from "./agentsView";
import { darkTheme, lightTheme } from "./theme";

type SortableSession = Partial<DashboardSession> & { isPinned?: boolean; pinnedAt?: string | null };

const session = (over: SortableSession = {}): DashboardSession =>
	({ id: "proj-1", projectId: "proj", status: null, lastActivityAt: "", ...over }) as DashboardSession;

const pr = (over: Partial<DashboardPR> = {}): DashboardPR => ({ number: 1, url: "", state: "open", ...over });

describe("boardZoneOf", () => {
	it("puts user-action sections ahead of passive work", () => {
		expect(BOARD_ZONES).toEqual(["action", "merge", "working", "pending"]);
	});

	// Desktop files ci_failed and changes_requested under "Needs you" rather than
	// giving review its own column; mobile used to split them.
	it("folds review and respond into Needs you", () => {
		expect(boardZoneOf(session({ status: "needs_input" }))).toBe("action");
		expect(boardZoneOf(session({ status: "stuck" }))).toBe("action");
		expect(boardZoneOf(session({ status: "ci_failed" }))).toBe("action");
		expect(boardZoneOf(session({ status: "changes_requested" }))).toBe("action");
	});

	it("maps the remaining zones straight through", () => {
		expect(boardZoneOf(session({ status: "mergeable" }))).toBe("merge");
		expect(boardZoneOf(session({ status: "approved" }))).toBe("merge");
		expect(boardZoneOf(session({ status: "pr_open" }))).toBe("pending");
		expect(boardZoneOf(session({ status: "review_pending" }))).toBe("pending");
		expect(boardZoneOf(session({ status: "working" }))).toBe("working");
		expect(boardZoneOf(session({ status: "idle" }))).toBe("working");
	});
});

describe("zoneMeta", () => {
	it("uses the mobile priority order and desktop labels", () => {
		expect(BOARD_ZONES.map((z) => zoneMeta(darkTheme, z).label)).toEqual([
			"Needs you",
			"Ready to merge",
			"Working",
			"In review",
		]);
	});

	it("takes its colours from the passed theme", () => {
		expect(zoneMeta(lightTheme, "merge").color).not.toBe(zoneMeta(darkTheme, "merge").color);
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
	it("splits the board from the archive", () => {
		const { sections, archived } = groupSessions(darkTheme, [
			session({ id: "a", status: "working" }),
			session({ id: "b", status: "needs_input" }),
			session({ id: "z", isTerminated: true }),
		]);
		expect(sections.map((s) => s.zone)).toEqual(["action", "working"]);
		expect(archived.map((s) => s.id)).toEqual(["z"]);
	});

	// A run of empty section titles is most of a phone screen.
	it("drops empty zones rather than rendering empty headers", () => {
		const { sections } = groupSessions(darkTheme, [session({ status: "working" })]);
		expect(sections).toHaveLength(1);
		expect(sections[0].label).toBe("Working");
	});

	it("keeps sections in action-first order regardless of input order", () => {
		const { sections } = groupSessions(darkTheme, [
			session({ id: "m", status: "mergeable" }),
			session({ id: "w", status: "working" }),
			session({ id: "p", status: "pr_open" }),
		]);
		expect(sections.map((s) => s.zone)).toEqual(["merge", "working", "pending"]);
	});

	it("puts pinned sessions first within their section", () => {
		const { sections } = groupSessions(darkTheme, [
			session({ id: "recent", status: "working", lastActivityAt: "2026-08-09T10:00:00Z" }),
			session({ id: "pinned", status: "working", isPinned: true, lastActivityAt: "2026-01-01T00:00:00Z" }),
		]);
		expect(sections[0].data.map((s) => s.id)).toEqual(["pinned", "recent"]);
	});

	it.each([
		["Needs you", "needs_input", ["old", "new"]],
		["Ready to merge", "mergeable", ["old", "new"]],
		["Working", "working", ["new", "old"]],
		["In review", "pr_open", ["old", "new"]],
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
		expect(groupSessions(darkTheme, [])).toEqual({ sections: [], archived: [] });
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
