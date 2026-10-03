import { describe, expect, it } from "vitest";
import type { DashboardSession } from "./api";
import { attentionOf, isTerminalStatus, sessionTitle } from "./sessionStatus";

// Minimal session; each test overrides only the fields it is about.
const session = (over: Partial<DashboardSession> = {}): DashboardSession =>
	({
		id: "proj-7",
		projectId: "proj",
		displayName: null,
		status: null,
		branch: null,
		issueId: null,
		issueTitle: null,
		userPrompt: null,
		summary: null,
		prs: [],
		pr: null,
		...over,
	}) as DashboardSession;

describe("sessionTitle", () => {
	it("prefers displayName, then issueId", () => {
		expect(sessionTitle(session({ displayName: "Fix auth", issueId: "AO-12" }))).toBe("Fix auth");
		expect(sessionTitle(session({ issueId: "AO-12" }))).toBe("AO-12");
	});

	// The id is the only field guaranteed to be present, so it is what a session
	// with nothing else must fall back to.
	it("falls back to the id when nothing is named", () => {
		expect(sessionTitle(session())).toBe("proj-7");
	});

	// The bug this function was rewritten for: " " is truthy, so a `||` chain
	// stopped on it and rendered a card with no title at all.
	it("treats a whitespace-only name as absent", () => {
		expect(sessionTitle(session({ displayName: "   " }))).toBe("proj-7");
		expect(sessionTitle(session({ displayName: "\t\n" , issueId: "AO-12" }))).toBe("AO-12");
	});

	it("trims a name that has content", () => {
		expect(sessionTitle(session({ displayName: "  Fix auth  " }))).toBe("Fix auth");
	});

	// Whatever happens, a card never renders an empty string.
	it("never returns blank", () => {
		for (const s of [
			session(),
			session({ displayName: " " }),
			session({ displayName: " ", issueId: " ", summary: " " }),
		]) {
			expect(sessionTitle(s).length).toBeGreaterThan(0);
		}
	});
});

describe("isTerminalStatus", () => {
	it("recognises the terminal set", () => {
		for (const s of ["killed", "terminated", "done", "cleanup", "errored", "merged"]) {
			expect(isTerminalStatus(s)).toBe(true);
		}
	});

	it("is false for live and missing statuses", () => {
		expect(isTerminalStatus("working")).toBe(false);
		expect(isTerminalStatus(null)).toBe(false);
		expect(isTerminalStatus(undefined)).toBe(false);
		expect(isTerminalStatus("")).toBe(false);
	});
});

describe("attentionOf", () => {
	// The server's own bucket wins whenever it sent one; the rest of this
	// function is only a fallback for daemons that didn't compute it.
	it("trusts a server-provided attentionLevel", () => {
		expect(attentionOf(session({ attentionLevel: "merge", status: "working" }))).toBe("merge");
	});

	it("maps terminal statuses to done", () => {
		expect(attentionOf(session({ status: "merged" }))).toBe("done");
		expect(attentionOf(session({ status: "killed" }))).toBe("done");
	});

	it("maps a mergeable PR to merge", () => {
		expect(attentionOf(session({ prs: [{ number: 1, url: "u", mergeability: { mergeable: true } }] as never }))).toBe(
			"merge",
		);
	});

	it("maps blocked statuses to respond", () => {
		expect(attentionOf(session({ status: "needs_input" }))).toBe("respond");
		expect(attentionOf(session({ status: "stuck" }))).toBe("respond");
	});

	it("maps failing CI and requested changes to review", () => {
		expect(attentionOf(session({ status: "ci_failed" }))).toBe("review");
		expect(attentionOf(session({ prs: [{ number: 1, url: "u", ciStatus: "failing" }] as never }))).toBe("review");
	});

	// An unresolved comment from a non-blocking review must still flag the
	// session as needing attention, same as a formal changes-requested
	// decision -- see agent-orchestrator#5765.
	it("maps unresolved comments to review", () => {
		expect(attentionOf(session({ status: "commented" }))).toBe("review");
	});

	it("defaults to working", () => {
		expect(attentionOf(session())).toBe("working");
	});
});
