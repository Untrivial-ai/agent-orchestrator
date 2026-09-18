import { describe, expect, it } from "vitest";
import type { DashboardSession, ProjectInfo } from "./api";
import { projectSummaries, projectWorkers } from "./projects-view";

const projects: ProjectInfo[] = [
	{ id: "alpha", name: "Alpha", kind: "single_repo" },
	{ id: "beta", name: "Beta", kind: "workspace" },
	{ id: "empty", name: "Empty", kind: "scratch" },
];

function session(id: string, projectId: string, overrides: Partial<DashboardSession> = {}): DashboardSession {
	return {
		id,
		projectId,
		status: "working",
		mode: "chat",
		branch: null,
		issueId: null,
		issueTitle: null,
		userPrompt: null,
		displayName: id,
		summary: null,
		createdAt: "2026-08-01T00:00:00Z",
		lastActivityAt: "2026-08-01T00:00:00Z",
		...overrides,
	};
}

describe("projectSummaries", () => {
	it("derives active work, attention, de-duplicated open PRs, failures, and latest activity", () => {
		const sessions = [
			session("alpha-live", "alpha", {
				status: "needs_input",
				lastActivityAt: "2026-08-04T00:00:00Z",
				pr: { number: 7, url: "https://example.com/alpha/pull/7", state: "open", ciStatus: "failing" },
			}),
			session("alpha-same-pr", "alpha", {
				lastActivityAt: "2026-08-03T00:00:00Z",
				pr: { number: 7, url: "https://example.com/alpha/pull/7", state: "open", ciStatus: "passing" },
			}),
			session("alpha-dead", "alpha", {
				status: "terminated",
				isTerminated: true,
				lastActivityAt: "2026-08-05T00:00:00Z",
			}),
			session("beta-live", "beta", { lastActivityAt: "2026-08-02T00:00:00Z" }),
		];

		expect(projectSummaries(projects, sessions).map((summary) => ({
			id: summary.project.id,
			active: summary.activeWorkers,
			attention: summary.needsAttention,
			open: summary.openPRs,
			failing: summary.failingPRs,
			latest: summary.lastActivityAt,
		}))).toEqual([
			{ id: "alpha", active: 2, attention: 1, open: 1, failing: 1, latest: "2026-08-05T00:00:00Z" },
			{ id: "beta", active: 1, attention: 0, open: 0, failing: 0, latest: "2026-08-02T00:00:00Z" },
			{ id: "empty", active: 0, attention: 0, open: 0, failing: 0, latest: null },
		]);
	});
});

describe("projectWorkers", () => {
	it("returns only live project workers with pinned workers first, then newest activity", () => {
		const sessions = [
			session("other", "beta", { isPinned: true, lastActivityAt: "2026-08-06T00:00:00Z" }),
			session("newest", "alpha", { lastActivityAt: "2026-08-05T00:00:00Z" }),
			session("pinned", "alpha", { isPinned: true, lastActivityAt: "2026-08-02T00:00:00Z" }),
			session("dead", "alpha", { isTerminated: true, lastActivityAt: "2026-08-07T00:00:00Z" }),
		];

		expect(projectWorkers("alpha", sessions).map(({ id }) => id)).toEqual(["pinned", "newest"]);
	});
});
