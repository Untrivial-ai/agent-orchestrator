import { describe, expect, it } from "vitest";
import type { SessionPRSummary } from "../hooks/useSessionScmSummary";
import type { PullRequestFacts, WorkspaceSession } from "../types/workspace";
import {
	prBrowserUrl,
	prCardPresentation,
	prDiffSummary,
	prStatusRows,
	prSummaryParts,
	sessionPRDisplaySummaries,
} from "./pr-display";

const summary = (overrides: Partial<SessionPRSummary> = {}): SessionPRSummary => ({
	url: "https://github.com/acme/repo/pull/7",
	htmlUrl: "https://github.com/acme/repo/pull/7",
	number: 7,
	title: "Fix dashboard",
	state: "open",
	provider: "github",
	repo: "acme/repo",
	author: "ada",
	sourceBranch: "fix/dashboard",
	targetBranch: "main",
	headSha: "abc123",
	additions: 10,
	deletions: 3,
	changedFiles: 2,
	ci: { autoInjectCI: true, state: "passing", failingChecks: [] },
	review: { decision: "approved", hasUnresolvedHumanComments: false, unresolvedBy: [] },
	mergeability: { state: "mergeable", reasons: [], prUrl: "https://github.com/acme/repo/pull/7" },
	updatedAt: "2026-06-15T00:00:00Z",
	observedAt: "2026-06-15T00:00:00Z",
	ciObservedAt: "2026-06-15T00:00:00Z",
	reviewObservedAt: "2026-06-15T00:00:00Z",
	...overrides,
});

const session = (prs: WorkspaceSession["prs"]): WorkspaceSession => ({
	id: "sess-1",
	workspaceId: "ws-1",
	workspaceName: "repo",
	title: "Fix timing",
	provider: "codex",
	branch: "feat/timing",
	status: "review_pending",
	updatedAt: "2026-06-15T12:00:00Z",
	prs,
});

function sessionWith(overrides: Partial<WorkspaceSession> = {}): WorkspaceSession {
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

describe("sessionPRDisplaySummaries", () => {
	it("keeps GitHub PR #7 and GitLab MR #7 distinct in the same session", () => {
		// Both providers have a PR/MR numbered 7. Keying by number alone collapses
		// them into one summary (the second wins), hiding the other. Keying by the
		// canonical web URL keeps both visible.
		const githubPR: PullRequestFacts = {
			url: "https://github.com/acme/repo/pull/7",
			number: 7,
			state: "open",
			ci: "passing",
			review: "approved",
			mergeability: "mergeable",
			reviewComments: false,
			updatedAt: "2026-06-15T00:00:00Z",
		};
		const gitlabMR: PullRequestFacts = {
			url: "https://gitlab.com/acme/repo/-/merge_requests/7",
			number: 7,
			state: "open",
			ci: "passing",
			review: "approved",
			mergeability: "mergeable",
			reviewComments: false,
			updatedAt: "2026-06-15T00:00:00Z",
		};
		const session = sessionWith({ prs: [githubPR, gitlabMR] });
		const githubSummary = summary({
			url: "https://github.com/acme/repo/pull/7",
			htmlUrl: "https://github.com/acme/repo/pull/7",
			number: 7,
			provider: "github",
			repo: "acme/repo",
			mergeability: {
				state: "mergeable",
				reasons: [],
				prUrl: "https://github.com/acme/repo/pull/7",
			},
		});
		const gitlabSummary = summary({
			url: "https://gitlab.com/acme/repo/-/merge_requests/7",
			htmlUrl: "https://gitlab.com/acme/repo/-/merge_requests/7",
			number: 7,
			provider: "gitlab",
			repo: "acme/repo",
			mergeability: {
				state: "mergeable",
				reasons: [],
				prUrl: "https://gitlab.com/acme/repo/-/merge_requests/7",
			},
		});

		const result = sessionPRDisplaySummaries(session, [githubSummary, gitlabSummary]);

		// Both summaries appear — not deduped to one.
		expect(result).toHaveLength(2);
		const urls = result.map((r) => r.url).sort();
		expect(urls).toEqual(["https://github.com/acme/repo/pull/7", "https://gitlab.com/acme/repo/-/merge_requests/7"]);
		// Each retains its own provider/repo, no summary is hidden.
		const github = result.find((r) => r.url === "https://github.com/acme/repo/pull/7");
		const gitlab = result.find((r) => r.url === "https://gitlab.com/acme/repo/-/merge_requests/7");
		expect(github?.provider).toBe("github");
		expect(gitlab?.provider).toBe("gitlab");
	});

	it("does not drop summaries whose url is empty, falling back to number:${number}", () => {
		// Two facts with empty urls and distinct numbers must both surface. The
		// fallback key `number:${number}` keeps them distinct within one provider
		// even when the canonical URL is not yet populated.
		const session = sessionWith({
			prs: [
				{
					url: "",
					number: 7,
					state: "open",
					ci: "passing",
					review: "approved",
					mergeability: "mergeable",
					reviewComments: false,
					updatedAt: "2026-06-15T00:00:00Z",
				},
				{
					url: "",
					number: 8,
					state: "open",
					ci: "passing",
					review: "approved",
					mergeability: "mergeable",
					reviewComments: false,
					updatedAt: "2026-06-15T00:00:00Z",
				},
			],
		});
		const summaries: SessionPRSummary[] = [
			summary({
				url: "",
				htmlUrl: undefined,
				number: 7,
			}),
			summary({
				url: "",
				htmlUrl: undefined,
				number: 8,
			}),
		];

		const result = sessionPRDisplaySummaries(session, summaries);

		expect(result.map((r) => r.number).sort((a, b) => a - b)).toEqual([7, 8]);
	});
});

describe("prStatusRows", () => {
	it("formats the three PR states without exposing raw unknown", () => {
		const rows = prStatusRows(
			summary({
				ci: { autoInjectCI: true, state: "unknown", failingChecks: [] },
				review: { decision: "none", hasUnresolvedHumanComments: false, unresolvedBy: [] },
				mergeability: { state: "unknown", reasons: [], prUrl: "https://github.com/acme/repo/pull/7" },
			}),
		);

		expect(rows.map((row) => `${row.label}:${row.value}`)).toEqual(["CI:Checking", "Merge:Checking", "Review:None"]);
	});

	it("includes minimal diff detail on the merge row", () => {
		const rows = prStatusRows(summary({ changedFiles: 4, additions: 25, deletions: 2 }));
		expect(rows.find((row) => row.key === "merge")?.detail).toBe("4 files");
	});
});

describe("prDiffSummary", () => {
	it("formats file and line delta metadata", () => {
		expect(prDiffSummary(summary({ changedFiles: 6, additions: 42, deletions: 8 }))).toBe("6 files · +42 -8");
	});

	it("omits the diff label when no diff metadata is available", () => {
		expect(prDiffSummary(summary({ changedFiles: 0, additions: 0, deletions: 0 }))).toBeUndefined();
	});
});

describe("prCardPresentation", () => {
	const priorityCases: Array<[string, Partial<SessionPRSummary>, string]> = [
		["conflict + passing + approval", { mergeability: { state: "conflicting", reasons: [], prUrl: "" } }, "Not mergeable yet"],
		["clean + passing + approval", { mergeability: { state: "mergeable", reasons: [], prUrl: "" } }, "Mergeable"],
		["clean + failing + approval", { ci: { autoInjectCI: true, state: "failing", failingChecks: [] }, mergeability: { state: "mergeable", reasons: [], prUrl: "" } }, "Not mergeable yet"],
	];
	it.each(priorityCases)("renders the priority stack for %s", (_name, overrides, readiness) => {
		const presentation = prCardPresentation(summary(overrides));
		expect(presentation.statusRows?.map((status) => status.label)).toEqual(
			readiness === "Mergeable" ? ["Checks passing", "Review status"] : overrides.mergeability?.state === "conflicting" ? ["Merge conflict", "Checks passing", "Review status"] : ["Checks failing", "Review status"],
		);
		expect(presentation.readiness?.label).toBe(readiness);
	});

	it("shows a required review once instead of repeating it as a merge blocker", () => {
		const presentation = prCardPresentation(
			summary({
				review: { decision: "review_required", hasUnresolvedHumanComments: false, unresolvedBy: [] },
				mergeability: {
					state: "blocked",
					reasons: ["review_required"],
					prUrl: "https://github.com/acme/repo/pull/7",
				},
			}),
		);

		expect(presentation.primary).toMatchObject({
			key: "review",
			label: "Review required",
			detail: "Merge blocked until a required review is submitted.",
			tone: "review",
		});
		expect(presentation.supporting.map((status) => status.label)).toEqual(["Checks passing"]);
	});

	it("shows checking merge readiness while provider state is pending", () => {
		const presentation = prCardPresentation(
			summary({
				ci: { autoInjectCI: true, state: "pending", failingChecks: [] },
				mergeability: { state: "unknown", reasons: [], prUrl: "https://github.com/acme/repo/pull/7" },
			}),
		);

		expect(presentation.readiness?.label).toBe("Checking merge readiness");
		expect(presentation.readiness?.detail).toBe("Waiting for the latest checks and review state.");
	});

	it("prioritizes failing checks over lower-priority review and merge facts", () => {
		const presentation = prCardPresentation(
			summary({
				ci: {
					autoInjectCI: true,
					state: "failing",
					failingChecks: [{ name: "unit", status: "failed", conclusion: "failure", url: "https://ci/unit" }],
				},
				review: { decision: "review_required", hasUnresolvedHumanComments: false, unresolvedBy: [] },
				mergeability: {
					state: "blocked",
					reasons: ["review_required"],
					prUrl: "https://github.com/acme/repo/pull/7",
				},
			}),
		);

		expect(presentation.primary.label).toBe("Checks failing");
		expect(presentation.primary.links[0]).toMatchObject({ label: "unit", href: "https://ci/unit" });
		expect(presentation.supporting).toEqual([]);
	});

	it("retains running checks as linked supporting state when review is the primary action", () => {
		const presentation = prCardPresentation(
			summary({
				ci: { autoInjectCI: true, state: "pending", failingChecks: [] },
				review: { decision: "review_required", hasUnresolvedHumanComments: false, unresolvedBy: [] },
				mergeability: {
					state: "blocked",
					reasons: ["review_required"],
					prUrl: "https://github.com/acme/repo/pull/7",
				},
			}),
		);

		expect(presentation.primary.label).toBe("Review required");
		expect(presentation.supporting[0]).toMatchObject({
			label: "Checks running",
			href: "https://github.com/acme/repo/pull/7/checks",
			breathe: true,
		});
	});

	it("shows running checks instead of an internal provider blocker", () => {
		const presentation = prCardPresentation(
			summary({
				ci: { autoInjectCI: true, state: "pending", failingChecks: [] },
				mergeability: {
					state: "unstable",
					reasons: ["blocked_by_provider"],
					prUrl: "https://github.com/acme/repo/pull/7",
				},
			}),
		);

		expect(presentation.primary).toMatchObject({
			key: "ci",
			label: "Checks running",
			href: "https://github.com/acme/repo/pull/7/checks",
			breathe: true,
		});
		expect(presentation.supporting).toEqual([]);
	});

	it("uses customer-facing copy for an unexplained merge blocker", () => {
		const presentation = prCardPresentation(
			summary({
				mergeability: {
					state: "blocked",
					reasons: ["blocked_by_provider"],
					prUrl: "https://github.com/acme/repo/pull/7",
				},
			}),
		);

		expect(presentation.primary).toMatchObject({
			key: "merge",
			label: "Merge unavailable",
			detail: "GitHub currently reports this pull request can't be merged.",
			links: [],
		});
	});
});

describe("prBrowserUrl", () => {
	it("normalizes issue-shaped GitHub PR URLs to the pull request page", () => {
		expect(
			prBrowserUrl(
				summary({
					url: "https://github.com/acme/repo/issues/7",
					htmlUrl: "https://github.com/acme/repo/issues/7",
				}),
			),
		).toBe("https://github.com/acme/repo/pull/7");
	});

	it("normalizes GitLab merge request URLs", () => {
		expect(
			prBrowserUrl(
				summary({
					provider: "gitlab",
					url: "https://gitlab.com/acme/repo/-/merge_requests/7",
					htmlUrl: "https://gitlab.com/acme/repo/-/merge_requests/7",
					mergeability: {
						state: "mergeable",
						reasons: [],
						prUrl: "https://gitlab.com/acme/repo/-/merge_requests/7",
					},
				}),
			),
		).toBe("https://gitlab.com/acme/repo/-/merge_requests/7");
	});

	it("strips query params and fragments from GitLab MR URLs", () => {
		expect(
			prBrowserUrl(
				summary({
					provider: "gitlab",
					url: "https://gitlab.com/acme/repo/-/merge_requests/7/diffs?view=inline#note_123",
					htmlUrl: "https://gitlab.com/acme/repo/-/merge_requests/7/diffs?view=inline#note_123",
					mergeability: {
						state: "mergeable",
						reasons: [],
						prUrl: "https://gitlab.com/acme/repo/-/merge_requests/7",
					},
				}),
			),
		).toBe("https://gitlab.com/acme/repo/-/merge_requests/7");
	});
});

describe("sessionPRDisplaySummaries", () => {
	it("leaves lifecycle timing absent for fallback PR facts until provider times arrive", () => {
		const [fallback] = sessionPRDisplaySummaries(
			session([
				{
					url: "https://github.com/acme/repo/pull/7",
					number: 7,
					state: "open",
					ci: "passing",
					review: "none",
					mergeability: "mergeable",
					reviewComments: false,
					updatedAt: "2026-06-15T11:00:00Z",
				},
			]),
		);

		expect(fallback.updatedAt).toBe("2026-06-15T11:00:00Z");
		expect(fallback.createdAt).toBeUndefined();
		expect(fallback.stateChangedAt).toBeUndefined();
	});

	it("deduplicates repeated API summaries before rendering", () => {
		const got = sessionPRDisplaySummaries(session([]), [
			summary({ number: 7, title: "first observed PR #7" }),
			summary({ number: 7, title: "duplicate PR #7" }),
			summary({ number: 8, title: "PR #8", url: "https://github.com/acme/repo/pull/8", htmlUrl: "https://github.com/acme/repo/pull/8" }),
		]);

		expect(got.map((pr) => pr.number)).toEqual([7, 8]);
		expect(got[0].title).toBe("first observed PR #7");
	});

	it("keeps same-number PRs from different repositories distinct", () => {
		const got = sessionPRDisplaySummaries(session([]), [
			summary({
				url: "https://github.com/acme/api/pull/7",
				htmlUrl: "https://github.com/acme/api/pull/7",
				repo: "acme/api",
				number: 7,
				title: "API PR #7",
			}),
			summary({
				url: "https://github.com/acme/web/pull/7",
				htmlUrl: "https://github.com/acme/web/pull/7",
				repo: "acme/web",
				number: 7,
				title: "Web PR #7",
			}),
		]);

		expect(got.map((pr) => pr.title)).toEqual(["API PR #7", "Web PR #7"]);
	});

	it("keeps same-number PRs with the same repository basename from different owners distinct", () => {
		const got = sessionPRDisplaySummaries(session([]), [
			summary({
				url: "https://github.com/acme/repo/pull/7",
				htmlUrl: "https://github.com/acme/repo/pull/7",
				repo: "acme/repo",
				number: 7,
				title: "Acme PR #7",
				headSha: "acme-head",
			}),
			summary({
				url: "https://github.com/other/repo/pull/7",
				htmlUrl: "https://github.com/other/repo/pull/7",
				repo: "other/repo",
				number: 7,
				title: "Other PR #7",
				headSha: "other-head",
			}),
		]);

		expect(got.map((pr) => pr.title)).toEqual(["Acme PR #7", "Other PR #7"]);
	});

	it("uses the enriched summary for a unique session fact when URL identities differ", () => {
		const got = sessionPRDisplaySummaries(
			session([
				{
					url: "https://example.com/pr/8",
					number: 8,
					state: "draft",
					ci: "pending",
					review: "none",
					mergeability: "unknown",
					reviewComments: false,
					updatedAt: "2026-06-15T10:00:00Z",
				},
			]),
			[
				summary({
					number: 8,
					url: "https://api.github.com/repos/acme/repo/pulls/8",
					htmlUrl: "https://github.com/acme/repo/pull/8",
					title: "enriched PR #8",
				}),
			],
		);

		expect(got).toHaveLength(1);
		expect(got[0]).toMatchObject({
			number: 8,
			title: "enriched PR #8",
			htmlUrl: "https://github.com/acme/repo/pull/8",
		});
	});

	it("keeps a same-number summary for another repository after an exact fact match", () => {
		const got = sessionPRDisplaySummaries(
			session([
				{
					url: "https://github.com/acme/api/pull/7",
					number: 7,
					state: "open",
					ci: "passing",
					review: "approved",
					mergeability: "mergeable",
					reviewComments: false,
					updatedAt: "2026-06-15T10:00:00Z",
				},
			]),
			[
				summary({
					url: "https://github.com/acme/api/pull/7",
					htmlUrl: "https://github.com/acme/api/pull/7",
					repo: "acme/api",
					number: 7,
					title: "API PR #7",
				}),
				summary({
					url: "https://github.com/acme/web/pull/7",
					htmlUrl: "https://github.com/acme/web/pull/7",
					repo: "acme/web",
					number: 7,
					title: "Web PR #7",
				}),
			],
		);

		expect(got.map((pr) => pr.title)).toEqual(["API PR #7", "Web PR #7"]);
	});

	it("deduplicates repeated session facts and uses the enriched summary for the surviving card", () => {
		const got = sessionPRDisplaySummaries(
			session([
				{
					url: "https://github.com/acme/repo/pull/7",
					number: 7,
					state: "open",
					ci: "pending",
					review: "none",
					mergeability: "unknown",
					reviewComments: false,
					updatedAt: "2026-06-15T10:00:00Z",
				},
				{
					url: "https://github.com/acme/repo/issues/7",
					number: 7,
					state: "open",
					ci: "passing",
					review: "approved",
					mergeability: "mergeable",
					reviewComments: false,
					updatedAt: "2026-06-15T11:00:00Z",
				},
			]),
			[
				summary({ number: 7, title: "enriched PR #7", ci: { autoInjectCI: true, state: "passing", failingChecks: [] } }),
				summary({ number: 7, title: "duplicate enriched PR #7" }),
			],
		);

		expect(got).toHaveLength(1);
		expect(got[0]).toMatchObject({ number: 7, title: "enriched PR #7", ci: { state: "passing" } });
	});

	it("keeps the next selected session's PR list independent after a duplicated list", () => {
		const firstSelection = sessionPRDisplaySummaries(
			session([
				{
					url: "https://github.com/acme/repo/pull/7",
					number: 7,
					state: "open",
					ci: "passing",
					review: "approved",
					mergeability: "mergeable",
					reviewComments: false,
					updatedAt: "2026-06-15T10:00:00Z",
				},
				{
					url: "https://github.com/acme/repo/issues/7",
					number: 7,
					state: "open",
					ci: "passing",
					review: "approved",
					mergeability: "mergeable",
					reviewComments: false,
					updatedAt: "2026-06-15T11:00:00Z",
				},
			]),
			[summary({ number: 7 }), summary({ number: 7 })],
		);
		const nextSelection = sessionPRDisplaySummaries(
			session([
				{
					url: "https://github.com/acme/repo/pull/9",
					number: 9,
					state: "open",
					ci: "passing",
					review: "approved",
					mergeability: "mergeable",
					reviewComments: false,
					updatedAt: "2026-06-15T12:00:00Z",
				},
			]),
		);

		expect(firstSelection.map((pr) => pr.number)).toEqual([7]);
		expect(nextSelection.map((pr) => pr.number)).toEqual([9]);
	});
});

describe("prSummaryParts", () => {
	it("always returns CI, Merge, and Review parts", () => {
		expect(prSummaryParts(summary()).map((part) => part.label)).toEqual(["CI", "Merge", "Review"]);
	});

	it("details active CI, merge, and review blockers under their parts", () => {
		const parts = prSummaryParts(
			summary({
				ci: {
					autoInjectCI: true,
					state: "failing",
					failingChecks: [
						{ name: "copy-check", status: "failed", conclusion: "failure", url: "https://checks.example/copy" },
					],
				},
				review: {
					decision: "changes_requested",
					hasUnresolvedHumanComments: true,
					unresolvedBy: [
						{
							reviewerId: "alice",
							count: 6,
							links: [
								{
									url: "https://github.com/acme/repo/pull/7#discussion_r1",
									file: "main.go",
									line: 12,
									autoInjectReview: true,
								},
							],
						},
					],
				},
				mergeability: {
					state: "blocked",
					reasons: ["behind_base"],
					prUrl: "https://github.com/acme/repo/pull/7",
				},
			}),
		);

		expect(parts.map((part) => part.key)).toEqual(["ci", "merge", "review"]);
		expect(parts.find((part) => part.key === "ci")).toMatchObject({
			status: "Failing",
			summary: undefined,
			tone: "error",
		});
		expect(parts.find((part) => part.key === "ci")?.links[0]).toMatchObject({
			label: "copy-check",
			href: "https://checks.example/copy",
		});
		expect(parts.find((part) => part.key === "merge")).toMatchObject({
			status: "Blocked",
			summary: undefined,
			tone: "warning",
		});
		expect(parts.find((part) => part.key === "review")).toMatchObject({
			status: "Changes requested",
			summary: undefined,
			tone: "warning",
		});
		expect(parts.find((part) => part.key === "review")?.links[0]).toMatchObject({
			label: "alice +5",
			href: "https://github.com/acme/repo/pull/7#discussion_r1",
		});
	});

	it("links failing CI checks to their provider URLs", () => {
		const parts = prSummaryParts(
			summary({
				ci: {
					autoInjectCI: true,
					state: "failing",
					failingChecks: [
						{ name: "unit", status: "failed", conclusion: "failure", url: "https://checks.example/unit" },
						{ name: "lint", status: "failed", conclusion: "failure", url: "https://checks.example/lint" },
						{ name: "build", status: "failed", conclusion: "failure", url: "https://checks.example/build" },
						{ name: "types", status: "failed", conclusion: "failure", url: "https://checks.example/types" },
					],
				},
			}),
		);

		const ciPart = parts.find((part) => part.key === "ci");
		expect(ciPart?.links).toEqual([
			{ label: "unit", href: "https://checks.example/unit", title: "failure" },
			{ label: "lint", href: "https://checks.example/lint", title: "failure" },
			{ label: "build", href: "https://checks.example/build", title: "failure" },
		]);
		expect(ciPart?.overflowLabel).toBe("+1 check");
	});

	it("prefers the submitted review summary over inline comments", () => {
		const parts = prSummaryParts(
			summary({
				review: {
					decision: "changes_requested",
					hasUnresolvedHumanComments: true,
					unresolvedBy: [
						{
							reviewerId: "alice",
							count: 2,
							reviewUrl: "https://github.com/acme/repo/pull/7#pullrequestreview-1",
							links: [
								{
									url: "https://github.com/acme/repo/pull/7#discussion_r1",
									file: "main.go",
									line: 12,
									autoInjectReview: true,
								},
								{
									url: "https://github.com/acme/repo/pull/7#discussion_r2",
									file: "test.go",
									line: 20,
									autoInjectReview: true,
								},
							],
						},
					],
				},
			}),
		);

		expect(parts.find((part) => part.key === "review")?.links[0]).toMatchObject({
			label: "alice +1",
			href: "https://github.com/acme/repo/pull/7#pullrequestreview-1",
			title: "Open requested-changes review from alice",
		});
	});

	it("falls back to the first inline comment when no review summary exists", () => {
		const parts = prSummaryParts(
			summary({
				review: {
					decision: "changes_requested",
					hasUnresolvedHumanComments: true,
					unresolvedBy: [
						{
							reviewerId: "alice",
							count: 2,
							links: [
								{
									url: "https://github.com/acme/repo/pull/7#discussion_r1",
									file: "main.go",
									line: 12,
									autoInjectReview: true,
								},
								{
									url: "https://github.com/acme/repo/pull/7#discussion_r2",
									file: "test.go",
									line: 20,
									autoInjectReview: true,
								},
							],
						},
					],
				},
			}),
		);

		expect(parts.find((part) => part.key === "review")?.links[0]).toMatchObject({
			label: "alice +1",
			href: "https://github.com/acme/repo/pull/7#discussion_r1",
			title: "2 unresolved comments from alice",
		});
	});

	it("falls back to the PR page when review summary and inline comment URLs are missing", () => {
		const parts = prSummaryParts(
			summary({
				url: "https://github.com/acme/repo/issues/7",
				htmlUrl: "https://github.com/acme/repo/issues/7",
				review: {
					decision: "changes_requested",
					hasUnresolvedHumanComments: true,
					unresolvedBy: [{ reviewerId: "alice", count: 1, links: [] }],
				},
			}),
		);

		expect(parts.find((part) => part.key === "review")?.links[0]).toMatchObject({
			label: "alice",
			href: "https://github.com/acme/repo/pull/7",
			title: "Open pull request for alice",
		});
	});

	it("shows bot reviewers with a bot label", () => {
		const parts = prSummaryParts(
			summary({
				review: {
					decision: "changes_requested",
					hasUnresolvedHumanComments: false,
					unresolvedBy: [
						{
							reviewerId: "copilot",
							count: 0,
							reviewUrl: "https://github.com/acme/repo/pull/7#pullrequestreview-2",
							isBot: true,
							links: [],
						},
					],
				},
			}),
		);

		expect(parts.find((part) => part.key === "review")?.links[0]).toMatchObject({
			label: "copilot bot",
			href: "https://github.com/acme/repo/pull/7#pullrequestreview-2",
			title: "Open requested-changes review from copilot bot",
		});
	});

	it("links merge conflicts to GitHub's conflict resolution page", () => {
		const parts = prSummaryParts(
			summary({
				url: "https://github.com/acme/repo/issues/7",
				htmlUrl: "https://github.com/acme/repo/issues/7",
				mergeability: {
					state: "conflicting",
					reasons: [],
					prUrl: "https://github.com/acme/repo/issues/7",
				},
			}),
		);

		expect(parts.find((part) => part.key === "merge")).toMatchObject({
			status: "Conflict",
			summary: undefined,
		});
		expect(parts.find((part) => part.key === "merge")?.links[0]).toMatchObject({
			label: "conflicts",
			href: "https://github.com/acme/repo/pull/7/conflicts",
		});
	});

	it("keeps closed or merged PR summaries to the three status parts", () => {
		const parts = prSummaryParts(
			summary({
				state: "merged",
				ci: {
					autoInjectCI: true,
					state: "failing",
					failingChecks: [{ name: "unit", status: "failed", conclusion: "failure" }],
				},
				review: { decision: "changes_requested", hasUnresolvedHumanComments: true, unresolvedBy: [] },
				mergeability: { state: "conflicting", reasons: ["conflicts"], prUrl: "https://github.com/acme/repo/pull/7" },
			}),
		);

		expect(parts).toHaveLength(3);
		expect(parts.find((part) => part.key === "merge")?.links).toEqual([]);
		expect(parts.find((part) => part.key === "review")?.links).toEqual([]);
	});

	it("puts draft readiness under Review", () => {
		const parts = prSummaryParts(
			summary({ state: "draft", review: { decision: "none", hasUnresolvedHumanComments: false, unresolvedBy: [] } }),
		);

		expect(parts.find((part) => part.key === "review")).toMatchObject({
			status: "None",
			summary: "Draft PR · Not ready for review",
		});
	});
});
