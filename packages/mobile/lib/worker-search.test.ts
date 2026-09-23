import { describe, expect, it } from "vitest";
import type { DashboardSession } from "./api";
import { filterWorkerSessions, workerSearchClearState } from "./worker-search";

function session(id: string, overrides: Partial<DashboardSession> = {}): DashboardSession {
	return {
		id,
		projectId: "alpha",
		status: "working",
		mode: "chat",
		branch: null,
		issueId: null,
		issueTitle: null,
		userPrompt: null,
		displayName: null,
		summary: null,
		createdAt: "2026-08-01T00:00:00Z",
		lastActivityAt: "2026-08-01T00:00:00Z",
		...overrides,
	};
}

const projectNameFor = (projectId: string) => ({ alpha: "Agent Orchestrator", beta: "Mobile App" })[projectId] ?? projectId;
const statusLabelFor = (status: string | null) => (status === "needs_input" ? "Needs input" : status ?? "Unknown");

describe("filterWorkerSessions", () => {
	const sessions = [
		session("audit", { displayName: "Mobile audit", projectId: "alpha", branch: "feat/native-controls" }),
		session("review", { displayName: "Review release", projectId: "beta", status: "needs_input" }),
		session("archived", { displayName: "Old migration", status: "terminated", isTerminated: true }),
	];

	it("keeps every worker when the query is blank", () => {
		expect(filterWorkerSessions(sessions, "  ", projectNameFor, statusLabelFor).map(({ id }) => id)).toEqual([
			"audit",
			"review",
			"archived",
		]);
	});

	it.each([
		["MOBILE AUDIT", ["audit"]],
		["mobile app", ["review"]],
		["native-controls", ["audit"]],
		["needs INPUT", ["review"]],
		["old migration", ["archived"]],
	])("matches %s across worker fields", (query, expected) => {
		expect(filterWorkerSessions(sessions, query, projectNameFor, statusLabelFor).map(({ id }) => id)).toEqual(expected);
	});
});

describe("workerSearchClearState", () => {
	it("keeps the inset clear control non-interactive until text exists", () => {
		expect(workerSearchClearState("")).toEqual({ disabled: true, opacity: 0, scale: 0.82 });
		expect(workerSearchClearState("audit")).toEqual({ disabled: false, opacity: 1, scale: 1 });
	});
});
