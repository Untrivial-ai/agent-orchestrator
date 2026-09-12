import { describe, expect, it } from "vitest";
import { AGENT_OPTIONS } from "@aoagents/product-ui";
import {
	attentionZone,
	canonicalTrackerIssueId,
	findProjectOrchestrator,
	newestActiveOrchestrator,
	orchestratorHealth,
	sessionIsActive,
	sessionNeedsAttention,
	toAgentProvider,
	toSessionActivity,
	toSessionStatus,
	openPRs,
	mergedPRCount,
	primaryPR,
	sortedPRs,
	type AttentionZone,
	type PRState,
	type PullRequestFacts,
	type SessionStatus,
	type WorkspaceSession,
	type WorkspaceSummary,
} from "./workspace";

describe("canonicalTrackerIssueId", () => {
	it("keeps provider-prefixed intake ids and rejects manual task titles", () => {
		expect(canonicalTrackerIssueId("github:acme/project#42")).toBe("github:acme/project#42");
		expect(canonicalTrackerIssueId("Fix fallback renderer")).toBeUndefined();
		expect(canonicalTrackerIssueId(undefined)).toBeUndefined();
	});
});

function sessionWith(overrides: Partial<WorkspaceSession>): WorkspaceSession {
	return {
		id: "sess-1",
		workspaceId: "ws-1",
		workspaceName: "my-app",
		title: "fix-bug",
		provider: "claude-code",
		branch: "feat/x",
		status: "working",
		updatedAt: "2026-01-01T00:00:00Z",
		prs: [],
		...overrides,
	};
}

const pr = (overrides: Partial<PullRequestFacts> & { number: number; state: PRState }): PullRequestFacts => ({
	url: `https://example.com/pr/${overrides.number}`,
	ci: "passing",
	review: "approved",
	mergeability: "mergeable",
	reviewComments: false,
	updatedAt: "2026-01-01T00:00:00Z",
	...overrides,
});

describe("toSessionStatus", () => {
	it("passes through a known status", () => {
		expect(toSessionStatus("mergeable")).toBe("mergeable");
		expect(toSessionStatus("no_signal")).toBe("no_signal");
		expect(toSessionStatus("exited")).toBe("exited");
	});

	it("keeps a backend merged status even when the session is terminated", () => {
		expect(toSessionStatus("merged", true)).toBe("merged");
	});

	it("uses terminated only as a fallback when a terminated session has no known status", () => {
		expect(toSessionStatus(undefined, true)).toBe("terminated");
	});

	it("falls back to unknown for an unknown live status", () => {
		expect(toSessionStatus("bogus")).toBe("unknown");
		expect(toSessionStatus(undefined)).toBe("unknown");
	});
});

describe("toSessionActivity", () => {
	it.each(["active", "idle", "waiting_input", "blocked", "exited"] as const)(
		"passes through the known state %s",
		(state) => {
			expect(toSessionActivity({ state })?.state).toBe(state);
		},
	);

	it("falls back to unknown for an unrecognized state", () => {
		expect(toSessionActivity({ state: "bogus" })?.state).toBe("unknown");
	});

	it("returns undefined for a missing activity", () => {
		expect(toSessionActivity(undefined)).toBeUndefined();
		expect(toSessionActivity(null)).toBeUndefined();
	});
});

describe("sessionIsActive", () => {
	it("keeps a merged session active until its runtime is terminated", () => {
		expect(sessionIsActive(sessionWith({ status: "merged", isTerminated: false }))).toBe(true);
		expect(sessionIsActive(sessionWith({ status: "merged", isTerminated: true }))).toBe(false);
	});

	it("is false for a terminated session", () => {
		expect(sessionIsActive(sessionWith({ status: "terminated" }))).toBe(false);
	});

	it("is true for in-progress statuses", () => {
		expect(sessionIsActive(sessionWith({ status: "working" }))).toBe(true);
		expect(sessionIsActive(sessionWith({ status: "pr_open" }))).toBe(true);
		expect(sessionIsActive(sessionWith({ status: "exited" }))).toBe(true);
	});
});

describe("findProjectOrchestrator", () => {
	function workspaceWith(sessions: WorkspaceSession[]): WorkspaceSummary {
		return { id: "skills", name: "skills", path: "/tmp/skills", sessions };
	}

	it("skips a terminated orchestrator that precedes the live one", () => {
		// Regression: the daemon lists sessions by spawn number, so a dead
		// orchestrator (zellij session deleted) sorts before its live successor.
		// Picking it sent the Orchestrator button to an instant "[process exited]".
		const dead = sessionWith({ id: "skills-4", kind: "orchestrator", status: "terminated" });
		const live = sessionWith({ id: "skills-5", kind: "orchestrator", status: "needs_input" });
		const worker = sessionWith({ id: "skills-6", kind: "worker", status: "working" });
		expect(findProjectOrchestrator([workspaceWith([dead, live, worker])], "skills")).toBe(live);
	});

	it("prefers the newest live orchestrator when multiple replacements overlap", () => {
		const older = sessionWith({ id: "skills-4", kind: "orchestrator", status: "idle", provider: "claude-code" });
		const newer = sessionWith({ id: "skills-5", kind: "orchestrator", status: "working", provider: "codex" });
		expect(findProjectOrchestrator([workspaceWith([older, newer])], "skills")).toBe(newer);
	});

	it("returns undefined when every orchestrator is terminated", () => {
		const dead = sessionWith({ id: "skills-4", kind: "orchestrator", status: "terminated" });
		expect(findProjectOrchestrator([workspaceWith([dead])], "skills")).toBeUndefined();
	});

	it("ignores live workers when looking for an orchestrator", () => {
		const worker = sessionWith({ id: "skills-6", kind: "worker", status: "working" });
		expect(findProjectOrchestrator([workspaceWith([worker])], "skills")).toBeUndefined();
	});

	it("returns undefined for an unknown project", () => {
		const live = sessionWith({ id: "skills-5", kind: "orchestrator", status: "working" });
		expect(findProjectOrchestrator([workspaceWith([live])], "other")).toBeUndefined();
	});

	it("selects the newest active orchestrator, not the first active one", () => {
		const older = sessionWith({
			id: "skills-1",
			kind: "orchestrator",
			status: "working",
			createdAt: "2026-01-01T00:00:00Z",
			updatedAt: "2026-01-01T00:00:00Z",
		});
		const newer = sessionWith({
			id: "skills-2",
			kind: "orchestrator",
			status: "working",
			createdAt: "2026-01-02T00:00:00Z",
			updatedAt: "2026-01-02T00:00:00Z",
		});
		expect(findProjectOrchestrator([workspaceWith([older, newer])], "skills")).toBe(newer);
	});

	it("uses updatedAt and id as newest orchestrator tie breakers", () => {
		const oldUpdate = sessionWith({
			id: "skills-2",
			kind: "orchestrator",
			status: "working",
			createdAt: "2026-01-01T00:00:00Z",
			updatedAt: "2026-01-01T00:00:00Z",
		});
		const newUpdate = sessionWith({
			id: "skills-1",
			kind: "orchestrator",
			status: "working",
			createdAt: "2026-01-01T00:00:00Z",
			updatedAt: "2026-01-02T00:00:00Z",
		});
		const sameTimesHigherID = sessionWith({
			id: "skills-3",
			kind: "orchestrator",
			status: "working",
			createdAt: "2026-01-01T00:00:00Z",
			updatedAt: "2026-01-02T00:00:00Z",
		});
		expect(newestActiveOrchestrator([oldUpdate, newUpdate])).toBe(newUpdate);
		expect(newestActiveOrchestrator([newUpdate, sameTimesHigherID])).toBe(sameTimesHigherID);
	});
});

describe("sessionNeedsAttention", () => {
	it.each(["needs_input", "exited", "no_signal", "changes_requested", "ci_failed", "unknown"] as const)(
		"is true for %s",
		(status) => {
			expect(sessionNeedsAttention(sessionWith({ status }))).toBe(true);
		},
	);

	it("treats no_signal as needing attention", () => {
		expect(sessionNeedsAttention(sessionWith({ status: "no_signal" }))).toBe(true);
	});

	it("is false for statuses that don't need the user", () => {
		expect(sessionNeedsAttention(sessionWith({ status: "working" }))).toBe(false);
		expect(sessionNeedsAttention(sessionWith({ status: "mergeable" }))).toBe(false);
		expect(sessionNeedsAttention(sessionWith({ status: "review_pending" }))).toBe(false);
	});
});

describe("orchestratorHealth", () => {
	it("reports restart_needed when the configured orchestrator agent differs from the newest active orchestrator", () => {
		const older = sessionWith({
			id: "skills-1",
			kind: "orchestrator",
			provider: "codex",
			status: "working",
			createdAt: "2026-01-01T00:00:00Z",
			updatedAt: "2026-01-01T00:00:00Z",
		});
		const newest = sessionWith({
			id: "skills-2",
			kind: "orchestrator",
			provider: "claude-code",
			status: "working",
			createdAt: "2026-01-02T00:00:00Z",
			updatedAt: "2026-01-02T00:00:00Z",
		});

		expect(
			orchestratorHealth({
				id: "skills",
				name: "skills",
				path: "/tmp/skills",
				orchestratorAgent: "codex",
				sessions: [older, newest],
			}),
		).toEqual({
			state: "duplicates",
			message:
				"Multiple orchestrators are active. The newest one is used; stale ones will be cleaned up on daemon reconcile.",
		});

		expect(
			orchestratorHealth({
				id: "skills",
				name: "skills",
				path: "/tmp/skills",
				orchestratorAgent: "codex",
				sessions: [newest],
			}).state,
		).toBe("restart_needed");
	});
});

describe("toAgentProvider", () => {
	it.each(AGENT_OPTIONS)("passes through the shared provider %s", (provider) => {
		expect(toAgentProvider(provider)).toBe(provider);
	});
	it("passes through Prime Agent", () => {
		expect(toAgentProvider("prime-agent")).toBe("prime-agent");
	});

	it("defaults unknown and undefined providers to codex", () => {
		expect(toAgentProvider("totally-unknown")).toBe("codex");
		expect(toAgentProvider(undefined)).toBe("codex");
	});

	it("keeps the fake harness (test sessions must not normalize to codex)", () => {
		expect(toAgentProvider("fake")).toBe("fake");
	});
});

describe("PR helpers", () => {
	const session = sessionWith({
		prs: [
			pr({ number: 41, state: "open" }),
			pr({ number: 42, state: "draft" }),
			pr({ number: 40, state: "merged" }),
			pr({ number: 39, state: "closed" }),
		],
	});

	it("sortedPRs orders open, draft, merged, closed then by number", () => {
		expect(sortedPRs(session).map((p) => p.number)).toEqual([41, 42, 40, 39]);
	});

	it("openPRs returns open and draft only", () => {
		expect(
			openPRs(session)
				.map((p) => p.number)
				.sort(),
		).toEqual([41, 42]);
	});

	it("mergedPRCount counts merged PRs", () => {
		expect(mergedPRCount(session)).toBe(1);
	});

	it("primaryPR is the highest-priority PR (open before merged)", () => {
		expect(primaryPR(session)?.number).toBe(41);
	});

	it("primaryPR is undefined when there are no PRs", () => {
		expect(primaryPR(sessionWith({ prs: [] }))).toBeUndefined();
	});
});

describe("attentionZone", () => {
	const cases: Array<[SessionStatus, AttentionZone]> = [
		["mergeable", "merge"],
		["approved", "merge"],
		["needs_input", "action"],
		["exited", "action"],
		["no_signal", "action"],
		["ci_failed", "action"],
		["changes_requested", "action"],
		["unknown", "action"],
		["review_pending", "pending"],
		["pr_open", "pending"],
		["draft", "pending"],
		["working", "working"],
		["idle", "working"],
		["merged", "merge"],
		["terminated", "done"],
	];

	it.each(cases)("buckets %s into the %s zone", (status, zone) => {
		expect(attentionZone(sessionWith({ status }))).toBe(zone);
	});

	it("prioritizes merge as the highest-ROI zone", () => {
		// merge is checked before action/pending so an approved PR always surfaces.
		expect(attentionZone(sessionWith({ status: "approved" }))).toBe("merge");
	});
});
