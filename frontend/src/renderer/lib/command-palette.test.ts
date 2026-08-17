import { describe, expect, it } from "vitest";
import {
	buildCommands,
	buildSessionActions,
	filterCommands,
	groupCommands,
	displayGroups,
	findSession,
	visibleForQuery,
	MAX_ATTENTION_SEARCH_RESULTS,
	MAX_ITEMS_PER_GROUP,
	MAX_SEARCH_RESULTS,
	type CommandItem,
} from "./command-palette";
import type { PRReviewState } from "./session-reviews";
import type { PullRequestFacts, WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { appI18n } from "../i18n";

function session(overrides: Partial<WorkspaceSession> & { id: string }): WorkspaceSession {
	return {
		workspaceId: "proj-1",
		workspaceName: "app",
		title: overrides.id,
		provider: "codex",
		kind: "worker",
		branch: `feature/${overrides.id}`,
		status: "working",
		updatedAt: "2026-06-10T00:00:00Z",
		prs: [],
		...overrides,
	};
}

const pr = (number: number, url = `https://github.com/o/r/pull/${number}`): PullRequestFacts => ({
	url,
	number,
	state: "open",
	ci: "passing",
	review: "pending",
	mergeability: "clean",
	reviewComments: false,
	updatedAt: "2026-06-10T00:00:00Z",
});

function workspaces(): WorkspaceSummary[] {
	return [
		{
			id: "proj-1",
			name: "app",
			path: "/repos/app",
			type: "main",
			sessions: [
				session({ id: "w-working", title: "refactor loader", status: "working" }),
				session({ id: "w-merge", title: "ship banner", status: "mergeable" }),
				session({ id: "w-action", title: "fix flake", status: "needs_input" }),
				session({ id: "w-pr", title: "add cache", status: "pr_open", prs: [pr(42)] }),
				session({ id: "w-synthetic", title: "scratch", branch: "session/w-synthetic" }),
				session({ id: "orch", title: "orchestrate", kind: "orchestrator" }),
			],
		},
	];
}

const byId = (items: CommandItem[]) => new Map(items.map((item) => [item.id, item]));

describe("findSession", () => {
	it("returns the workspace and session together", () => {
		const result = findSession(workspaces(), "w-pr");

		expect(result?.workspace.id).toBe("proj-1");
		expect(result?.session.id).toBe("w-pr");
		expect(findSession(workspaces(), "missing")).toBeUndefined();
	});
});

describe("buildCommands grouping", () => {
	it("uses the translator supplied by the reactive caller", () => {
		const items = buildCommands(
			{ workspaces: workspaces(), currentProjectId: "proj-1" },
			appI18n.getFixedT("zh-CN"),
		);
		expect(byId(items).get("current-new-task")?.title).toBe("新建任务");
	});

	it("puts current-scoped actions in the Current group when the project is valid", () => {
		const items = buildCommands({ workspaces: workspaces(), currentProjectId: "proj-1", currentSessionId: "w-pr" });
		const map = byId(items);
		expect(map.get("current-new-task")?.group).toBe("current");
		expect(map.get("current-open-orchestrator")?.group).toBe("current");
		expect(map.get("current-project-settings")?.group).toBe("current");
		expect(map.get("current-copy-branch")?.group).toBe("current");
		expect(map.get("current-copy-branch")?.action).toEqual({ kind: "copy-branch", branch: "feature/w-pr" });
	});

	it("disables New task with a reason when the route project is absent from workspaces", () => {
		const items = buildCommands({ workspaces: workspaces(), currentProjectId: "missing", currentSessionId: undefined });
		const newTask = byId(items).get("current-new-task");
		expect(newTask?.disabled).toBe(true);
		expect(newTask?.disabledReason).toBe("No current project");
		expect(newTask?.action).toBeUndefined();
		expect(byId(items).has("current-open-orchestrator")).toBe(false);
		expect(byId(items).has("current-project-settings")).toBe(false);
	});

	it("disables New task and Open orchestrator while the project orchestrator is restarting", () => {
		const items = buildCommands({
			workspaces: workspaces(),
			currentProjectId: "proj-1",
			restartingProjectIds: new Set(["proj-1"]),
		});
		const map = byId(items);
		expect(map.get("current-new-task")?.disabled).toBe(true);
		expect(map.get("current-new-task")?.disabledReason).toBe("Orchestrator restarting");
		expect(map.get("current-open-orchestrator")?.disabled).toBe(true);
		expect(map.get("current-open-orchestrator")?.disabledReason).toBe("Orchestrator restarting");
		expect(map.get("current-project-settings")?.disabled).toBeFalsy();
	});

	it("omits Copy branch for a synthetic (session/<id>) branch and for orchestrators", () => {
		const synthetic = buildCommands({ workspaces: workspaces(), currentSessionId: "w-synthetic" });
		expect(byId(synthetic).has("current-copy-branch")).toBe(false);
		const orch = buildCommands({ workspaces: workspaces(), currentSessionId: "orch" });
		expect(byId(orch).has("current-copy-branch")).toBe(false);
	});

	it("recognises an orchestrator by its id suffix, not just its kind", () => {
		const rows = workspaces();
		rows[0].sessions.push(session({ id: "proj-1-orchestrator", title: "legacy orch", branch: "main" }));
		const items = buildCommands({ workspaces: rows, currentSessionId: "proj-1-orchestrator" });
		expect(byId(items).has("current-copy-branch")).toBe(false);
	});
});

describe("buildCommands attention", () => {
	it("includes ready-to-merge AND attention-needing sessions, ordered merge-first", () => {
		const items = buildCommands({ workspaces: workspaces() });
		const attention = items.filter((item) => item.group === "attention");
		const ids = attention.map((item) => item.id);
		expect(ids).toContain("attention:w-merge");
		expect(ids).toContain("attention:w-action");
		expect(ids).not.toContain("attention:w-working");
		expect(ids.indexOf("attention:w-merge")).toBeLessThan(ids.indexOf("attention:w-action"));
	});

	it("omits the current session from Needs attention (already in view)", () => {
		const items = buildCommands({ workspaces: workspaces(), currentSessionId: "w-merge" });
		const ids = new Set(items.map((item) => item.id));
		expect(ids.has("attention:w-merge")).toBe(false);
		expect(ids.has("attention:w-action")).toBe(true);
	});
});

describe("buildCommands sessions", () => {
	it("does not repeat attention or current sessions in the flat Sessions list", () => {
		const items = buildCommands({ workspaces: workspaces(), currentSessionId: "w-working" });
		const ids = new Set(items.map((item) => item.id));
		expect(ids.has("attention:w-merge")).toBe(true);
		expect(ids.has("session:w-merge")).toBe(false);
		expect(ids.has("attention:w-action")).toBe(true);
		expect(ids.has("session:w-action")).toBe(false);
		expect(ids.has("session:w-working")).toBe(false);
		expect(ids.has("session:w-synthetic")).toBe(true);
	});
});

describe("buildCommands pull requests", () => {
	it("creates a per-PR item searchable by number, #number, url, branch and project", () => {
		const items = buildCommands({ workspaces: workspaces() });
		const prItem = byId(items).get("pr:w-pr:42");
		expect(prItem?.group).toBe("prs");
		expect(prItem?.title).toBe("#42");
		const keywords = prItem?.keywords ?? [];
		expect(keywords).toContain("#42");
		expect(keywords).toContain("42");
		expect(keywords).toContain("https://github.com/o/r/pull/42");
		expect(keywords).toContain("feature/w-pr");
		expect(keywords).toContain("app");
	});

	it("excludes merged and closed PRs (open/draft only)", () => {
		const merged: PullRequestFacts = { ...pr(7), state: "merged" };
		const closed: PullRequestFacts = { ...pr(8), state: "closed" };
		const ws: WorkspaceSummary[] = [
			{
				id: "proj-1",
				name: "app",
				path: "/repos/app",
				type: "main",
				sessions: [session({ id: "w-mix", title: "mixed prs", prs: [merged, closed, pr(9)] })],
			},
		];
		const ids = new Set(buildCommands({ workspaces: ws }).map((item) => item.id));
		expect(ids.has("pr:w-mix:9")).toBe(true);
		expect(ids.has("pr:w-mix:7")).toBe(false);
		expect(ids.has("pr:w-mix:8")).toBe(false);
	});
});

type ReviewRun = NonNullable<PRReviewState["latestRun"]>;

const reviewRun = (prNumber: number): ReviewRun => ({
	autoInjectReview: false,
	batchId: "batch-1",
	triggerSource: "manual" as const,
	body: "review body",
	createdAt: "2026-06-10T00:00:00Z",
	githubReviewId: `${prNumber}01`,
	harness: "codex",
	id: `run-${prNumber}`,
	prUrl: `https://github.com/o/r/pull/${prNumber}`,
	reviewId: `review-${prNumber}`,
	sessionId: "w-pr",
	status: "delivered",
	targetSha: "sha",
	verdict: "approved",
});

const reviewState = (
	prNumber: number,
	status: PRReviewState["status"],
	overrides: Partial<PRReviewState> = {},
): PRReviewState => ({
	prNumber,
	prUrl: `https://github.com/o/r/pull/${prNumber}`,
	status,
	targetSha: "sha",
	title: `PR ${prNumber}`,
	...overrides,
});

describe("buildCommands PR actions", () => {
	it("creates Open PR and Copy PR URL items per open PR", () => {
		const items = buildCommands({ workspaces: workspaces() });
		const map = byId(items);
		const open = map.get("pr-open:w-pr:42");
		expect(open?.group).toBe("prs");
		expect(open?.title).toBe("Open PR #42");
		expect(open?.action).toEqual({ kind: "open-pr", url: "https://github.com/o/r/pull/42" });
		expect(open?.searchOnly).toBe(true);
		const copy = map.get("pr-copy:w-pr:42");
		expect(copy?.title).toBe("Copy PR URL #42");
		expect(copy?.action).toEqual({ kind: "copy-pr-url", url: "https://github.com/o/r/pull/42" });
		expect(copy?.searchOnly).toBe(true);
	});

	it("creates an enabled Run review item per open PR when review data has not loaded", () => {
		const review = byId(buildCommands({ workspaces: workspaces() })).get("pr-review:w-pr:42");
		expect(review?.group).toBe("prs");
		expect(review?.title).toBe("Run review #42");
		expect(review?.action).toEqual({ kind: "trigger-review", sessionId: "w-pr" });
		expect(review?.disabled).toBeFalsy();
		expect(review?.disabledReason).toBeUndefined();
		expect(review?.searchOnly).toBe(true);
	});

	it("omits the review item while a session's review data has not yet loaded, but keeps Open PR / Copy PR URL", () => {
		const map = byId(
			buildCommands({
				workspaces: workspaces(),
				reviewStatesBySessionId: {},
			}),
		);
		expect(map.has("pr-review:w-pr:42")).toBe(false);
		expect(map.get("pr-open:w-pr:42")).toBeTruthy();
		expect(map.get("pr-copy:w-pr:42")).toBeTruthy();
	});

	it("labels the review item Re-run review for changes requested or a completed run", () => {
		const changesRequested = buildCommands({
			workspaces: workspaces(),
			reviewStatesBySessionId: { "w-pr": [reviewState(42, "changes_requested")] },
		});
		expect(byId(changesRequested).get("pr-review:w-pr:42")?.title).toBe("Re-run review #42");

		const upToDate = buildCommands({
			workspaces: workspaces(),
			reviewStatesBySessionId: { "w-pr": [reviewState(42, "up_to_date", { latestRun: reviewRun(42) })] },
		});
		const item = byId(upToDate).get("pr-review:w-pr:42");
		expect(item?.title).toBe("Re-run review #42");
		expect(item?.disabled).toBeFalsy();
	});

	it("disables the review item with Review already running when a session review is running", () => {
		const items = buildCommands({
			workspaces: workspaces(),
			reviewStatesBySessionId: { "w-pr": [reviewState(42, "running")] },
		});
		const review = byId(items).get("pr-review:w-pr:42");
		expect(review?.disabled).toBe(true);
		expect(review?.disabledReason).toBe("Review already running");
	});

	it("disables the review item with Not eligible for review for ineligible or draft PRs", () => {
		const ineligible = buildCommands({
			workspaces: workspaces(),
			reviewStatesBySessionId: { "w-pr": [reviewState(42, "ineligible")] },
		});
		const ineligibleItem = byId(ineligible).get("pr-review:w-pr:42");
		expect(ineligibleItem?.disabled).toBe(true);
		expect(ineligibleItem?.disabledReason).toBe("Not eligible for review");

		const draftWorkspaces: WorkspaceSummary[] = [
			{
				id: "proj-1",
				name: "app",
				path: "/repos/app",
				type: "main",
				sessions: [session({ id: "w-draft", title: "draft work", prs: [{ ...pr(5), state: "draft" }] })],
			},
		];
		const draft = buildCommands({
			workspaces: draftWorkspaces,
			reviewStatesBySessionId: { "w-draft": [reviewState(5, "ineligible")] },
		});
		const draftItem = byId(draft).get("pr-review:w-draft:5");
		expect(draftItem?.disabled).toBe(true);
		expect(draftItem?.disabledReason).toBe("Not eligible for review");
	});

	it("emits per-PR action items for every open PR of a multi-PR session, all triggering the session", () => {
		const multiWorkspaces: WorkspaceSummary[] = [
			{
				id: "proj-1",
				name: "app",
				path: "/repos/app",
				type: "main",
				sessions: [session({ id: "w-multi", title: "stacked", prs: [pr(1), pr(2)] })],
			},
		];
		const map = byId(buildCommands({ workspaces: multiWorkspaces }));
		expect(map.get("pr-open:w-multi:1")?.action).toEqual({ kind: "open-pr", url: "https://github.com/o/r/pull/1" });
		expect(map.get("pr-open:w-multi:2")?.action).toEqual({ kind: "open-pr", url: "https://github.com/o/r/pull/2" });
		expect(map.get("pr-review:w-multi:1")?.action).toEqual({ kind: "trigger-review", sessionId: "w-multi" });
		expect(map.get("pr-review:w-multi:2")?.action).toEqual({ kind: "trigger-review", sessionId: "w-multi" });
	});

	it("does not create action items for merged or closed PRs", () => {
		const merged: PullRequestFacts = { ...pr(7), state: "merged" };
		const closed: PullRequestFacts = { ...pr(8), state: "closed" };
		const ws: WorkspaceSummary[] = [
			{
				id: "proj-1",
				name: "app",
				path: "/repos/app",
				type: "main",
				sessions: [session({ id: "w-mix", title: "mixed prs", prs: [merged, closed, pr(9)] })],
			},
		];
		const ids = new Set(buildCommands({ workspaces: ws }).map((item) => item.id));
		expect(ids.has("pr-open:w-mix:9")).toBe(true);
		expect(ids.has("pr-copy:w-mix:9")).toBe(true);
		expect(ids.has("pr-review:w-mix:9")).toBe(true);
		expect(ids.has("pr-open:w-mix:7")).toBe(false);
		expect(ids.has("pr-copy:w-mix:8")).toBe(false);
		expect(ids.has("pr-review:w-mix:7")).toBe(false);
	});

	it("surfaces the action items for open pr / copy pr / review queries but hides them untyped", () => {
		const items = buildCommands({ workspaces: workspaces() });
		expect(filterCommands(items, "open pr").some((item) => item.id === "pr-open:w-pr:42")).toBe(true);
		expect(filterCommands(items, "copy pr").some((item) => item.id === "pr-copy:w-pr:42")).toBe(true);
		expect(filterCommands(items, "review").some((item) => item.id === "pr-review:w-pr:42")).toBe(true);
		expect(filterCommands(items, "").some((item) => item.id === "pr-open:w-pr:42")).toBe(false);
		expect(filterCommands(items, "").some((item) => item.id === "pr-review:w-pr:42")).toBe(false);
	});

	it("discovers Copy branch name via git and copy keywords", () => {
		const items = buildCommands({ workspaces: workspaces(), currentProjectId: "proj-1", currentSessionId: "w-pr" });
		const keywords = byId(items).get("current-copy-branch")?.keywords ?? [];
		expect(keywords).toContain("copy");
		expect(keywords).toContain("git");
	});
});

describe("buildCommands finished sessions", () => {
	function withFinished(): WorkspaceSummary[] {
		return [
			{
				id: "proj-1",
				name: "app",
				path: "/repos/app",
				type: "main",
				sessions: [
					session({ id: "w-live", title: "live one", status: "working" }),
					session({ id: "w-done", title: "archived cleanup", status: "terminated" }),
					session({ id: "w-merged", title: "old merge", status: "merged" }),
				],
			},
		];
	}

	it("indexes merged/terminated sessions as search-only (hidden until typed, then findable)", () => {
		const items = buildCommands({ workspaces: withFinished() });
		const done = byId(items).get("session:w-done");
		expect(done?.searchOnly).toBe(true);
		expect(byId(items).get("session:w-live")?.searchOnly).toBeFalsy();

		const suggested = filterCommands(items, "");
		expect(suggested.some((item) => item.id === "session:w-done")).toBe(false);
		expect(suggested.some((item) => item.id === "session:w-live")).toBe(true);

		const searched = filterCommands(items, "archived");
		expect(searched.some((item) => item.id === "session:w-done")).toBe(true);
	});
});

describe("result caps", () => {
	const manyProjects = (n: number): WorkspaceSummary[] =>
		Array.from({ length: n }, (_, i) => ({
			id: `p${i}`,
			name: `project-${i}`,
			path: `/repos/p${i}`,
			type: "main" as const,
			sessions: [],
		}));

	it("caps the pre-typing suggestion view per group so huge installs never render every row", () => {
		const grouped = displayGroups(buildCommands({ workspaces: manyProjects(50) }), "");
		expect(grouped.find((g) => g.id === "projects")?.items.length).toBe(MAX_ITEMS_PER_GROUP);
		expect(grouped.find((g) => g.id === "global")?.items.length).toBeGreaterThan(0);
	});

	it("renders a typed search under category headings, capped to MAX_SEARCH_RESULTS overall", () => {
		const groups = displayGroups(buildCommands({ workspaces: manyProjects(50) }), "project");
		expect(groups.every((g) => g.id !== "results")).toBe(true);
		expect(groups.some((g) => g.id === "projects")).toBe(true);
		const total = groups.reduce((sum, g) => sum + g.items.length, 0);
		expect(total).toBe(MAX_SEARCH_RESULTS);
	});

	it("keeps the combined attention + results count within MAX_SEARCH_RESULTS", () => {
		const attentionSessions = Array.from({ length: 25 }, (_, i) =>
			session({ id: `deploy-hot-${i}`, title: `deploy hot ${i}`, status: "needs_input" }),
		);
		const workspaces: WorkspaceSummary[] = [
			{ id: "deploy", name: "deploy proj", path: "/repos/deploy", type: "main", sessions: attentionSessions },
		];
		const groups = displayGroups(buildCommands({ workspaces }), "deploy");
		const total = groups.reduce((n, g) => n + g.items.length, 0);
		expect(total).toBeLessThanOrEqual(MAX_SEARCH_RESULTS);
	});

	it("keeps search hits under category headings, best-matching category first", () => {
		const workspaces: WorkspaceSummary[] = [
			{
				id: "alpha",
				name: "alpha",
				path: "/repos/alpha",
				type: "main",
				sessions: [session({ id: "s-attn", title: "fix alpha bug", status: "needs_input" })],
			},
		];
		const groups = displayGroups(buildCommands({ workspaces }), "alpha");
		const ids = groups.map((g) => g.id);
		expect(ids).toContain("attention");
		expect(ids).toContain("projects");
		// Projects outranks its default position ahead of Needs attention because
		// "alpha" is an exact project title but only a fuzzy hit on the session.
		expect(ids.indexOf("projects")).toBeLessThan(ids.indexOf("attention"));
		expect(groups.find((g) => g.id === "attention")?.items.some((item) => item.id === "attention:s-attn")).toBe(
			true,
		);
		// Enter targets the first item in render order, so that must be the top match.
		const rendered = groups.flatMap((g) => g.items);
		expect(rendered[0]?.id).toBe("project:alpha");
		expect(rendered.length).toBeLessThanOrEqual(MAX_SEARCH_RESULTS);
	});

	it("floats attention matches into their own category during search", () => {
		const workspaces: WorkspaceSummary[] = [
			{
				id: "alpha",
				name: "alpha",
				path: "/repos/alpha",
				type: "main",
				sessions: [session({ id: "s-attn", title: "fix alpha bug", status: "needs_input" })],
			},
		];
		const groups = displayGroups(buildCommands({ workspaces }), "alpha");
		expect(groups.map((g) => g.id)).toContain("attention");
		expect(groups.find((g) => g.id === "attention")?.items.map((item) => item.id)).toContain("attention:s-attn");
	});

	it("falls back to the default category order when categories match equally well", () => {
		// "app" is the project title (1000) and a keyword-only hit (500) on the
		// attention, session and PR rows, so those three keep their declared order.
		const ids = displayGroups(buildCommands({ workspaces: workspaces() }), "app").map((g) => g.id);
		expect(ids[0]).toBe("projects");
		expect(ids.indexOf("attention")).toBeLessThan(ids.indexOf("sessions"));
		expect(ids.indexOf("sessions")).toBeLessThan(ids.indexOf("prs"));
	});

	it("keeps a lower-scoring attention match visible past the ordinary-result cap", () => {
		const manyPrs = Array.from({ length: 30 }, (_, i) => pr(i + 1));
		const workspaces: WorkspaceSummary[] = [
			{
				id: "proj-deploy",
				name: "proj-deploy",
				path: "/repos/deploy",
				type: "main",
				sessions: [
					session({ id: "hot", title: "zzz deploy", status: "needs_input" }),
					session({ id: "pr-host", title: "deploy stack", status: "pr_open", prs: manyPrs }),
				],
			},
		];
		const groups = displayGroups(buildCommands({ workspaces }), "deploy");
		const attention = groups.find((g) => g.id === "attention");
		expect(attention?.items.map((item) => item.id)).toContain("attention:hot");
	});

	it("never lets a flood of attention matches crowd out an exact non-attention match", () => {
		const floodedWorkspace: WorkspaceSummary = {
			id: "proj-deploy",
			name: "proj-deploy",
			path: "/repos/deploy",
			type: "main",
			sessions: Array.from({ length: MAX_SEARCH_RESULTS }, (_, i) =>
				session({ id: `hot-${i}`, title: `zzz deploy ${i}`, status: "needs_input" }),
			),
		};
		const exactMatchWorkspace: WorkspaceSummary = {
			id: "deploy",
			name: "deploy",
			path: "/repos/deploy-exact",
			type: "main",
			sessions: [],
		};
		const items = buildCommands({ workspaces: [floodedWorkspace, exactMatchWorkspace] });
		const attentionMatches = items.filter(
			(item) => item.group === "attention" && item.title.toLowerCase().includes("deploy"),
		);
		expect(attentionMatches.length).toBeGreaterThanOrEqual(MAX_SEARCH_RESULTS);

		const visible = visibleForQuery(items, "deploy");
		expect(visible.slice(0, MAX_ATTENTION_SEARCH_RESULTS).every(isAttentionInZone)).toBe(true);
		expect(visible).toContainEqual(expect.objectContaining({ id: "project:deploy" }));
		expect(visible.findIndex((item) => item.id === "project:deploy")).toBeLessThan(MAX_SEARCH_RESULTS);
	});

	it("still surfaces the exact match when every attention title also prefix-matches (tied score)", () => {
		const floodedWorkspace: WorkspaceSummary = {
			id: "proj-1",
			name: "proj-1",
			path: "/repos/proj-1",
			type: "main",
			sessions: Array.from({ length: MAX_SEARCH_RESULTS }, (_, i) =>
				session({ id: `hot-${i}`, title: `deploy pipeline ${i}`, status: "needs_input" }),
			),
		};
		const exactMatchWorkspace: WorkspaceSummary = {
			id: "deploy",
			name: "deploy",
			path: "/repos/deploy-exact",
			type: "main",
			sessions: [],
		};
		const items = buildCommands({ workspaces: [floodedWorkspace, exactMatchWorkspace] });
		const visible = visibleForQuery(items, "deploy");
		expect(visible).toContainEqual(expect.objectContaining({ id: "project:deploy" }));
		expect(visible.filter((item) => !isAttentionInZone(item))).not.toHaveLength(0);
	});
});

function isAttentionInZone(item: CommandItem): boolean {
	return item.zone === "action" || item.zone === "merge";
}

describe("filterCommands / matchScore", () => {
	it("ranks a title prefix above a keyword-only hit", () => {
		const items = buildCommands({ workspaces: workspaces() });
		const results = filterCommands(items, "app");
		expect(results[0]?.id).toBe("project:proj-1");
	});

	it("matches a PR by its #number", () => {
		const items = buildCommands({ workspaces: workspaces() });
		const results = filterCommands(items, "#42");
		expect(results.some((item) => item.id === "pr:w-pr:42")).toBe(true);
	});
});

describe("groupCommands", () => {
	it("skips empty groups and preserves group order", () => {
		const items = buildCommands({ workspaces: workspaces(), currentProjectId: "proj-1" });
		const grouped = groupCommands(items);
		const order = grouped.map((g) => g.id);
		expect(order).toEqual(["current", "attention", "projects", "sessions", "prs", "global"]);
		expect(grouped.every((g) => g.items.length > 0)).toBe(true);
	});
});

describe("session rows open the actions panel", () => {
	it("emits open-session-actions for session rows while PR rows stay navigate", () => {
		const items = buildCommands({ workspaces: workspaces() });
		const map = byId(items);
		expect(map.get("attention:w-merge")?.action).toEqual({ kind: "open-session-actions", sessionId: "w-merge" });
		expect(map.get("pr:w-pr:42")?.action?.kind).toBe("navigate");
		expect(map.get("project:proj-1")?.action?.kind).toBe("navigate");
	});
});

const workspace: WorkspaceSummary = { id: "proj-1", name: "app", path: "/repos/app", type: "main", sessions: [] };
const actionKinds = (items: CommandItem[]) => items.map((item) => item.action?.kind ?? "none");

describe("buildSessionActions", () => {
	it("offers Jump then Copy branch for a live worker", () => {
		const items = buildSessionActions(workspace, session({ id: "live", status: "working" }));
		expect(items.map((i) => i.title)).toEqual(["Jump to session", "Copy branch name"]);
		expect(items[0].action).toEqual({
			kind: "navigate",
			target: { to: "/projects/$projectId/sessions/$sessionId", params: { projectId: "proj-1", sessionId: "live" } },
		});
	});

	it("adds Resume only for a terminated worker", () => {
		const terminated = buildSessionActions(workspace, session({ id: "gone", status: "terminated" }));
		expect(terminated.find((i) => i.action?.kind === "resume-session")?.action).toEqual({
			kind: "resume-session",
			projectId: "proj-1",
			sessionId: "gone",
		});
		for (const status of ["working", "needs_input", "no_signal", "mergeable"] as const) {
			const items = buildSessionActions(workspace, session({ id: `s-${status}`, status }));
			expect(items.some((i) => i.action?.kind === "resume-session")).toBe(false);
		}
	});

	it("offers Resume for a durably terminated session whose derived status is not 'terminated'", () => {
		const items = buildSessionActions(
			workspace,
			session({ id: "archived-merged", status: "merged", isTerminated: true }),
		);
		expect(items.some((i) => i.action?.kind === "resume-session")).toBe(true);
	});

	it("never offers Resume or Copy branch for an orchestrator", () => {
		const items = buildSessionActions(
			workspace,
			session({ id: "proj-1-orchestrator", kind: "orchestrator", status: "terminated", branch: "main" }),
		);
		expect(actionKinds(items)).toEqual(["navigate"]);
	});

	it("omits Copy branch for a synthetic branch", () => {
		const items = buildSessionActions(workspace, session({ id: "syn", branch: "session/syn" }));
		expect(items.some((i) => i.action?.kind === "copy-branch")).toBe(false);
	});
});
