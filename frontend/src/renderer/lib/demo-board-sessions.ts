import type { WorkspaceSession } from "../types/workspace";

const demoUpdatedAt = "2026-08-26T08:00:00Z";

/** Preview-only sessions used to exercise every board lane without a daemon. */
export function demoBoardSessions(workspaceId: string): WorkspaceSession[] {
	const base = {
		activity: { state: "idle" as const, lastActivityAt: demoUpdatedAt },
		createdAt: "2026-08-25T08:00:00Z",
		isTerminated: false,
		provider: "claude-code" as const,
		prs: [],
		updatedAt: demoUpdatedAt,
		workspaceId,
		workspaceName: "Demo repository",
	};

	return [
		{
			...base,
			activity: { state: "active", lastActivityAt: demoUpdatedAt },
			branch: "ao/demo-building",
			id: "demo-building",
			kanbanColumn: "building",
			status: "working",
			title: "Implement auth flow",
		},
		{
			...base,
			branch: "ao/demo-ci-failing",
			displayStatus: "Fixing CI failures",
			id: "demo-ci-failing",
			kanbanColumn: "validating",
			prs: [
				{
					ci: "failing",
					number: 142,
					review: "approved",
					mergeability: "blocked",
					reviewComments: false,
					state: "open",
					updatedAt: demoUpdatedAt,
					url: "https://github.com/example/demo/pull/142",
				},
			],
			status: "ci_failed",
			title: "Fix checkout regression",
		},
		{
			...base,
			branch: "ao/demo-review",
			displayStatus: "Needs human review",
			id: "demo-needs-review",
			kanbanColumn: "needs_review",
			prs: [
				{
					ci: "passing",
					number: 143,
					review: "review_required",
					mergeability: "mergeable",
					reviewComments: false,
					state: "open",
					updatedAt: demoUpdatedAt,
					url: "https://github.com/example/demo/pull/143",
				},
			],
			status: "review_pending",
			title: "Review notification copy",
		},
		{
			...base,
			branch: "ao/demo-ready",
			displayStatus: "Ready to merge",
			id: "demo-ready",
			kanbanColumn: "ready",
			prs: [
				{
					ci: "passing",
					number: 144,
					review: "approved",
					mergeability: "mergeable",
					reviewComments: false,
					state: "open",
					updatedAt: demoUpdatedAt,
					url: "https://github.com/example/demo/pull/144",
				},
			],
			status: "approved",
			title: "Add account export",
		},
		{
			...base,
			activity: { state: "blocked", lastActivityAt: demoUpdatedAt },
			branch: "ao/demo-blocked",
			displayStatus: "Blocked",
			id: "demo-blocked",
			kanbanColumn: "building",
			status: "idle",
			title: "Waiting on environment access",
		},
	];
}
