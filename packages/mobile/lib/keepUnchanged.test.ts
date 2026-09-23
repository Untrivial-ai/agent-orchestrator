import { describe, expect, it } from "vitest";

// Type-only, so this test pulls in no React Native module at runtime.
import type { DashboardSession } from "./api";
import { keepUnchanged, sameJson } from "./keepUnchanged";

// A session shaped like the poll actually returns one: nested PR, optional
// fields present and absent, so the walk is exercised on the real payload.
const session = (over: Partial<DashboardSession> = {}): DashboardSession => ({
	id: "ao-1",
	projectId: "ao",
	status: "running",
	mode: "chat",
	branch: "ao/ao-1/root",
	issueId: null,
	issueTitle: null,
	userPrompt: "check the readme",
	displayName: "flow-check",
	summary: null,
	isTerminated: false,
	createdAt: "2026-09-03T09:00:00Z",
	lastActivityAt: "2026-09-03T09:04:12Z",
	pr: { number: 7, url: "https://github.com/o/r/pull/7", ciStatus: "passing", mergeability: { mergeable: true, blockers: [] } },
	...over,
});

describe("sameJson", () => {
	it("treats a re-mapped but identical fleet as unchanged", () => {
		expect(sameJson([session(), session({ id: "ao-2" })], [session(), session({ id: "ao-2" })])).toBe(true);
	});

	it.each([
		["a status", { status: "waiting_input" }],
		["an activity timestamp", { lastActivityAt: "2026-09-03T09:05:00Z" }],
		["a nulled field", { branch: null }],
		["termination", { isTerminated: true }],
	])("sees %s change", (_name, over) => {
		expect(sameJson(session(), session(over))).toBe(false);
	});

	it("sees a change nested inside the PR", () => {
		const before = session();
		const after = session({ pr: { ...before.pr!, ciStatus: "failing" } });
		expect(sameJson(before, after)).toBe(false);
	});

	it("sees a change nested two levels down, inside mergeability", () => {
		const before = session();
		const after = session({ pr: { ...before.pr!, mergeability: { mergeable: false, blockers: ["conflicts"] } } });
		expect(sameJson(before, after)).toBe(false);
	});

	it("sees a session appear and disappear", () => {
		expect(sameJson([session()], [session(), session({ id: "ao-2" })])).toBe(false);
		expect(sameJson([session(), session({ id: "ao-2" })], [session()])).toBe(false);
	});

	// The daemon orders the board; a reorder is a real change to render.
	it("sees a reorder", () => {
		expect(sameJson([session(), session({ id: "ao-2" })], [session({ id: "ao-2" }), session()])).toBe(false);
	});

	// A hand-written field list would go blind to a field added later. This is
	// the property that makes the deep compare the safe choice.
	it("sees a field the comparison was never told about", () => {
		expect(sameJson(session(), { ...session(), somethingAddedLater: true })).toBe(false);
	});

	it("does not confuse an empty array with an empty object", () => {
		expect(sameJson([], {})).toBe(false);
	});

	it("distinguishes null, undefined and absent", () => {
		expect(sameJson({ pr: null }, { pr: undefined })).toBe(false);
		expect(sameJson({ pr: undefined }, {})).toBe(false);
	});

});

describe("keepUnchanged", () => {
	// The point of the whole module: an unchanged poll tick must hand React back
	// the array it already holds, or it re-renders every card on the board.
	it("returns the previous array when the tick brought nothing new", () => {
		const prev = [session()];
		expect(keepUnchanged(prev, [session()])).toBe(prev);
	});

	it("returns the new array when something moved", () => {
		const prev = [session()];
		const next = [session({ status: "done" })];
		expect(keepUnchanged(prev, next)).toBe(next);
	});

	it("holds for the stats object too", () => {
		const prev = { totalSessions: 3, workingSessions: 1 };
		expect(keepUnchanged(prev, { totalSessions: 3, workingSessions: 1 })).toBe(prev);
		expect(keepUnchanged(prev, { totalSessions: 3, workingSessions: 2 })).not.toBe(prev);
	});

	it("starts from empty without pretending an arriving fleet is unchanged", () => {
		const empty: DashboardSession[] = [];
		expect(keepUnchanged(empty, [])).toBe(empty);
		expect(keepUnchanged(empty, [session()])).not.toBe(empty);
	});
});
