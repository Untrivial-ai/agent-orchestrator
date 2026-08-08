import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionInspector } from "./SessionInspector";
import { TooltipProvider } from "./ui/tooltip";
import type { SessionPRSummary } from "../hooks/useSessionScmSummary";
import { sessionScmSummaryQueryKey } from "../hooks/useSessionScmSummary";
import { sessionUsageDetailQueryKey, type SessionUsage } from "../hooks/useSessionUsage";
import { sessionWorkspaceFilesQueryKey } from "../hooks/useSessionWorkspaceFiles";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { useUiStore } from "../stores/ui-store";
import type { PRState, PullRequestFacts, WorkspaceSession, WorkspaceSummary } from "../types/workspace";

const { getMock, navigateMock, patchMock, putMock, postMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	navigateMock: vi.fn(),
	patchMock: vi.fn(),
	putMock: vi.fn(),
	postMock: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
}));

vi.mock("../lib/preview-mode", () => ({
	usesPreviewWorkspaceData: false,
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: getMock,
		PATCH: patchMock,
		POST: postMock,
		PUT: putMock,
	},
	getApiBaseUrl: () => "http://127.0.0.1:3001",
	hasTrustedApiBaseUrl: () => false,
	subscribeApiBaseUrl: () => () => {},
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		if (typeof error === "object" && error !== null && "message" in error) {
			return String((error as { message: unknown }).message);
		}
		return fallback;
	},
}));

const pr = (n: number, state: PRState, overrides: Partial<PullRequestFacts> = {}): PullRequestFacts => ({
	url: `https://example.com/pr/${n}`,
	number: n,
	state,
	ci: "passing",
	review: "approved",
	mergeability: "mergeable",
	reviewComments: false,
	updatedAt: "2026-06-15T00:00:00Z",
	...overrides,
});

const session = (prs: PullRequestFacts[], overrides: Partial<WorkspaceSession> = {}): WorkspaceSession => ({
	id: "sess-1",
	workspaceId: "ws-1",
	workspaceName: "my-app",
	title: "do the thing",
	provider: "claude-code",
	kind: "worker",
	branch: "feat/ns",
	status: "review_pending",
	updatedAt: "2026-06-15T00:00:00Z",
	autoInjectReview: true,
	prs,
	...overrides,
});

const sessionWithProvider = (prs: PullRequestFacts[], provider: WorkspaceSession["provider"]): WorkspaceSession => ({
	...session(prs),
	provider,
});

const prSummary = (
	number: number,
	state: SessionPRSummary["state"],
	overrides: Partial<SessionPRSummary> = {},
): SessionPRSummary => {
	const url = `https://github.com/acme/repo/pull/${number}`;
	return {
		url: `https://api.github.com/repos/acme/repo/pulls/${number}`,
		htmlUrl: url,
		number,
		title: `PR ${number}`,
		state,
		provider: "github",
		repo: "acme/repo",
		author: "ada",
		sourceBranch: `feat/${number}`,
		targetBranch: "main",
		headSha: `sha-${number}`,
		additions: 4,
		deletions: 1,
		changedFiles: 2,
		ci: { state: "passing", failingChecks: [] },
		review: { decision: "none", hasUnresolvedHumanComments: false, unresolvedBy: [] },
		mergeability: { state: "mergeable", reasons: [], prUrl: url, conflictFiles: [] },
		updatedAt: "2026-06-15T12:00:00Z",
		...overrides,
	};
};

const usageTelemetry = (overrides: Partial<SessionUsage> = {}): SessionUsage => ({
	sessionId: "sess-1",
	incomplete: false,
	totals: {
		inputTokens: 1000,
		uncachedInputTokens: 600,
		cacheReadTokens: 400,
		cacheWriteTokens: 0,
		outputTokens: 200,
		reasoningTokens: 40,
	},
	harnesses: [
		{
			harness: "codex",
			totals: {
				inputTokens: 1000,
				uncachedInputTokens: 600,
				cacheReadTokens: 400,
				cacheWriteTokens: 0,
				outputTokens: 200,
				reasoningTokens: 40,
			},
			models: [
				{
					modelId: "gpt-5.6",
					totals: {
						inputTokens: 1000,
						uncachedInputTokens: 600,
						cacheReadTokens: 400,
						cacheWriteTokens: 0,
						outputTokens: 200,
						reasoningTokens: 40,
					},
				},
			],
		},
	],
	...overrides,
});

function renderWithQuery(children: ReactNode, workspaces?: WorkspaceSummary[], seed?: (client: QueryClient) => void) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	if (workspaces) client.setQueryData(workspaceQueryKey, workspaces);
	seed?.(client);
	return {
		...render(
			<QueryClientProvider client={client}>
				<TooltipProvider>{children}</TooltipProvider>
			</QueryClientProvider>,
		),
		queryClient: client,
	};
}

function mockCommonGets(_unusedRuns: unknown[] = [], reviewerHandleId = "", reviews: unknown[] = []) {
	getMock.mockImplementation(async (path: string) => {
		if (path === "/api/v1/usage/sessions/{sessionId}") {
			return { data: usageTelemetry(), error: undefined };
		}
		if (path === "/api/v1/agents") {
			const agents = ["claude-code", "codex", "opencode"].map((id) => ({ id, label: id }));
			return { data: { supported: agents, installed: agents, authorized: agents } };
		}
		if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
			return { data: { sessionId: "sess-1", files: [], truncated: false }, error: undefined };
		}
		if (path === "/api/v1/sessions/{sessionId}/reviews") {
			return { data: { reviewerHandleId, reviews } };
		}
		if (path === "/api/v1/projects/{id}") {
			return {
				data: {
					status: "ok",
					project: {
						id: "ws-1",
						kind: "git",
						name: "my-app",
						path: "/repo",
						repo: "my-app",
						defaultBranch: "main",
						config: { reviewers: [{ harness: "codex" }] },
					},
				},
			};
		}
		return { data: undefined };
	});
}

const approvedReview = {
	id: "run-1",
	reviewId: "review-1",
	sessionId: "sess-1",
	harness: "codex",
	status: "complete",
	verdict: "approved",
	body: "Looks good.",
	prUrl: "https://example.com/pr/3",
	targetSha: "abc123",
	createdAt: "2026-06-16T10:06:00Z",
	autoInjectReview: true,
};

const failedReview = {
	...approvedReview,
	id: "run-failed",
	status: "failed",
	verdict: "",
	body: "reviewer crashed",
};

const reviewState = (n: number, status: string, targetSha = `sha-${n}`) => ({
	prUrl: `https://example.com/pr/${n}`,
	prNumber: n,
	title: `Reviewable change ${n}`,
	targetSha,
	status,
	latestRun:
		status === "up_to_date" ? { ...approvedReview, prUrl: `https://example.com/pr/${n}`, targetSha } : undefined,
});

beforeEach(() => {
	useUiStore.getState().setDeveloperMode(false);
	getMock.mockReset();
	navigateMock.mockReset();
	patchMock.mockReset();
	postMock.mockReset();
	useUiStore.setState({ inspectorSessions: {} });
	putMock.mockReset();
	mockCommonGets();
	patchMock.mockResolvedValue({ data: { ok: true }, error: undefined, response: { status: 200 } });
	postMock.mockResolvedValue({ data: { ok: true, sessionId: "sess-1" }, error: undefined });
	putMock.mockResolvedValue({ data: { session: {} }, error: undefined, response: { status: 200 } });
});

afterEach(() => {
	vi.useRealTimers();
});

describe("SessionInspector tabs", () => {
	it("gives the Browser viewport the full inspector body without the default content gutter", async () => {
		renderWithQuery(<SessionInspector session={session([])} />);

		const tablist = screen.getByRole("tablist");
		await userEvent.click(screen.getByRole("tab", { name: "Browser" }));

		const body = tablist.nextElementSibling;
		expect(body).toHaveClass("session-inspector__body--browser", "p-0", "overflow-hidden");
		expect(body).not.toHaveClass("p-3", "pb-4", "@max-[300px]/inspector:px-2.5");
	});

	it("sizes rail tabs to their labels instead of stretching across the inspector", () => {
		renderWithQuery(<SessionInspector session={session([])} />);

		const summaryTab = screen.getByRole("tab", { name: "Summary" });

		expect(summaryTab).not.toHaveClass("flex-1");
		expect(summaryTab).toHaveClass("h-control-md", "px-1.5");
		expect(summaryTab).toHaveAttribute("title", "Summary");
		expect(within(summaryTab).getByText("Summary")).toHaveClass("@max-[350px]/inspector:hidden");
	});

	it("shows the glow only while real browser activity is unseen", () => {
		const currentSession = session([]);
		const view = renderWithQuery(<SessionInspector session={currentSession} />);
		expect(screen.queryByTestId("browser-unseen-indicator")).not.toBeInTheDocument();
		view.unmount();

		useUiStore.getState().setBrowserUnseen(currentSession.id, true);
		renderWithQuery(<SessionInspector session={currentSession} />);
		expect(screen.getByTestId("browser-unseen-indicator")).toBeInTheDocument();

		act(() => useUiStore.getState().setInspectorView(currentSession.id, "browser"));
		expect(screen.queryByTestId("browser-unseen-indicator")).not.toBeInTheDocument();
	});

	it("renders the supplied files view when the Files tab opens", async () => {
		const onOpenFiles = vi.fn();
		renderWithQuery(
			<SessionInspector filesView={<div>workspace file review</div>} onOpenFiles={onOpenFiles} session={session([])} />,
		);

		await userEvent.click(screen.getByRole("tab", { name: "Files" }));

		expect(onOpenFiles).toHaveBeenCalledTimes(1);
		expect(screen.getByText("workspace file review")).toBeInTheDocument();
	});

	it("warms the workspace files cache before the Files tab opens", async () => {
		renderWithQuery(<SessionInspector session={session([])} />);

		const filesTab = screen.getByRole("tab", { name: "Files" });
		expect(within(filesTab).getByText("Files")).toBeInTheDocument();
		await waitFor(() =>
			expect(getMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/workspace/files", {
				params: { path: { sessionId: "sess-1" } },
			}),
		);
	});

	it("shows a live changed-file count on the Files tab once the shared cache is populated", () => {
		renderWithQuery(<SessionInspector session={session([])} />, undefined, (client) => {
			client.setQueryData(sessionWorkspaceFilesQueryKey("sess-1"), {
				sessionId: "sess-1",
				truncated: false,
				files: [
					{ path: "src/App.tsx", status: "modified", additions: 2, deletions: 1, size: 120, binary: false },
					{ path: "README.md", status: "unmodified", additions: 0, deletions: 0, size: 80, binary: false },
				],
			});
		});

		const filesTab = screen.getByRole("tab", { name: "Files" });
		expect(within(filesTab).getByText("1 File")).toBeInTheDocument();
		// The accessible name stays static so existing name-based tab queries keep resolving.
		expect(filesTab).toHaveAttribute("title", "Files");
	});

	it("distinguishes a checked-but-clean workspace (0 Files) from an unopened tab (Files)", () => {
		renderWithQuery(<SessionInspector session={session([])} />, undefined, (client) => {
			client.setQueryData(sessionWorkspaceFilesQueryKey("sess-1"), {
				sessionId: "sess-1",
				truncated: false,
				files: [{ path: "README.md", status: "unmodified", additions: 0, deletions: 0, size: 80, binary: false }],
			});
		});

		const filesTab = screen.getByRole("tab", { name: "Files" });
		expect(within(filesTab).getByText("0 Files")).toBeInTheDocument();
	});
});

describe("SessionInspector PR section", () => {
	// Scope assertions to the PR section so the card order is explicit.
	const prSection = (title: string) =>
		within(screen.getByText(title).closest("[data-testid='inspector-section']") as HTMLElement);

	it("renders one card per PR, ordered actionable-first, when a session owns a stack", () => {
		renderWithQuery(<SessionInspector session={session([pr(40, "merged"), pr(41, "open"), pr(42, "draft")])} />);

		expect(screen.getByText("Pull requests (3)")).toBeInTheDocument();
		const cards = prSection("Pull requests (3)")
			.getAllByText(/^PR #\d+$/)
			.map((el) => el.textContent);
		// open (41), draft (42), merged (40)
		expect(cards).toEqual(["PR #41", "PR #42", "PR #40"]);
	});

	it("uses the singular heading and shows enriched facts for a single PR", () => {
		renderWithQuery(<SessionInspector session={session([pr(7, "open")])} />, undefined, (client) => {
			client.setQueryData(sessionScmSummaryQueryKey("sess-1"), [prSummary(7, "open")]);
		});

		expect(screen.getByText("Pull request")).toBeInTheDocument();
		expect(screen.queryByText(/Pull requests \(/)).not.toBeInTheDocument();
		expect(prSection("Pull request").getByText("PR #7")).toBeInTheDocument();
		expect(prSection("Pull request").getByText("Ready to merge")).toBeInTheDocument();
		expect(prSection("Pull request").getByText("Checks passing")).toBeInTheDocument();
		expect(prSection("Pull request").getByRole("link", { name: "PR 7" })).toHaveClass("text-sm");
		expect(prSection("Pull request").getByRole("link", { name: "PR 7" })).toHaveAttribute(
			"href",
			"https://github.com/acme/repo/pull/7",
		);
		expect(prSection("Pull request").getByText("open")).toHaveClass("text-[9px]", "leading-none");
		expect(prSection("Pull request").getByRole("button", { name: "Merge PR #7" })).toBeInTheDocument();
	});

	it("merges a ready pull request directly through the daemon", async () => {
		const readyPR = prSummary(7, "open", {
			url: "https://example.com/pr/7",
			headSha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		});
		renderWithQuery(<SessionInspector session={session([pr(7, "open")])} />, undefined, (client) => {
			client.setQueryData(sessionScmSummaryQueryKey("sess-1"), [readyPR]);
		});

		const mergeButton = screen.getByRole("button", { name: "Merge PR #7" });
		expect(mergeButton).toBeEnabled();
		fireEvent.click(mergeButton);

		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith("/api/v1/prs/{id}/merge", {
				params: { path: { id: "7" } },
				body: {
					prUrl: "https://example.com/pr/7",
					expectedHeadSha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
			}),
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("does not offer Merge when the pull request is not ready", () => {
		renderWithQuery(
			<SessionInspector
				session={session([
					pr(7, "open", {
						ci: "failing",
						mergeability: "blocked",
					}),
				])}
			/>,
		);

		expect(screen.queryByRole("button", { name: "Merge PR #7" })).not.toBeInTheDocument();
	});

	it("uses the state chip as the single merged-state indicator", () => {
		renderWithQuery(<SessionInspector session={session([pr(7, "merged")], { status: "merged" })} />);

		const card = prSection("Pull request").getByText("PR #7").closest("article") as HTMLElement;
		expect(within(card).getByText("merged", { exact: true })).toHaveClass(
			"border-border-strong",
			"bg-overlay",
			"text-success",
		);
		expect(within(card).queryByText("Pull request merged")).not.toBeInTheDocument();
	});

	it("shows the empty state when there are no PRs", () => {
		renderWithQuery(<SessionInspector session={session([])} />);
		expect(screen.getByText("No pull request opened yet.")).toBeInTheDocument();
	});

	it("links each PR to its url", () => {
		renderWithQuery(<SessionInspector session={session([pr(41, "open"), pr(42, "draft")])} />);
		const links = [
			prSection("Pull requests (2)").getByRole("link", { name: "Open PR #41" }),
			prSection("Pull requests (2)").getByRole("link", { name: "Open PR #42" }),
		];
		expect(links[0]).toHaveClass("text-settings-label", "hover:text-settings-label");
		expect(links.map((a) => a.getAttribute("href"))).toEqual([
			"https://example.com/pr/41",
			"https://example.com/pr/42",
		]);
	});
});

describe("SessionInspector completion controls", () => {
	it("persists the terminate-on-merge preference", async () => {
		renderWithQuery(<SessionInspector session={session([])} />);

		await userEvent.click(screen.getByRole("switch", { name: "Terminate session when pull requests merge" }));

		await waitFor(() =>
			expect(patchMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/merge-policy", {
				params: { path: { sessionId: "sess-1" } },
				body: { terminateOnPrMerge: true },
			}),
		);
	});

	it("terminates a live merged session and returns to its orchestrator immediately", async () => {
		postMock.mockReturnValue(new Promise(() => {}));
		const worker = session([pr(7, "merged")], { status: "merged" });
		const orchestrator = session([], { id: "orch-1", kind: "orchestrator", title: "orchestrator" });
		renderWithQuery(<SessionInspector session={worker} />, [
			{
				id: "ws-1",
				name: "my-app",
				path: "/repo",
				sessions: [worker, orchestrator],
			},
		]);

		expect(
			screen.queryByRole("switch", { name: "Terminate session when pull requests merge" }),
		).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Terminate session" }));
		expect(screen.getByRole("dialog", { name: "Terminate do the thing?" })).toBeInTheDocument();
		await userEvent.click(
			within(screen.getByRole("dialog")).getByRole("button", { name: "Yes, terminate session" }),
		);

		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/kill", {
			params: { path: { sessionId: "sess-1" } },
		});
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "ws-1", sessionId: "orch-1" },
		});
	});

	it("keeps the confirmation dismissed after a termination failure", async () => {
		postMock.mockResolvedValueOnce({ error: new Error("runtime teardown failed"), response: { status: 500 } });
		renderWithQuery(<SessionInspector session={session([pr(7, "merged")], { status: "merged" })} />);

		await userEvent.click(screen.getByRole("button", { name: "Terminate session" }));
		await userEvent.click(
			within(screen.getByRole("dialog")).getByRole("button", { name: "Yes, terminate session" }),
		);

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId",
			params: { projectId: "ws-1" },
		});
	});

	it("hides completion controls after the session is terminated", () => {
		renderWithQuery(
			<SessionInspector session={session([pr(7, "merged")], { status: "merged", isTerminated: true })} />,
		);

		expect(screen.queryByText("Completion")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Terminate session" })).not.toBeInTheDocument();
	});

	it("does not show completion controls for orchestrator sessions", () => {
		renderWithQuery(<SessionInspector session={session([], { kind: "orchestrator" })} />);

		expect(screen.queryByText("Completion")).not.toBeInTheDocument();
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});
});

describe("SessionInspector Activity section", () => {
	const activitySection = () =>
		within(screen.getByText("Activity").closest("[data-testid='inspector-section']") as HTMLElement);

	it("offers a managed resume only for an exited, nonterminated agent", async () => {
		renderWithQuery(
			<SessionInspector
				session={session([], {
					status: "exited",
					activity: { state: "exited", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		await userEvent.click(activitySection().getByRole("button", { name: "Resume agent" }));

		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/resume-agent", {
				params: { path: { sessionId: "sess-1" } },
			}),
		);
	});

	it("does not offer agent resume for a live or terminated session", () => {
		const live = renderWithQuery(
			<SessionInspector
				session={session([], {
					status: "idle",
					activity: { state: "idle", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		expect(screen.queryByRole("button", { name: "Resume agent" })).not.toBeInTheDocument();

		live.unmount();
		renderWithQuery(
			<SessionInspector
				session={session([], {
					status: "terminated",
					isTerminated: true,
					activity: { state: "exited", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);
		expect(screen.queryByRole("button", { name: "Resume agent" })).not.toBeInTheDocument();
	});

	it("keeps resume failures visible beside the action", async () => {
		postMock.mockResolvedValueOnce({ error: new Error("agent restart failed"), response: { status: 500 } });
		renderWithQuery(
			<SessionInspector
				session={session([], {
					status: "exited",
					activity: { state: "exited", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		await userEvent.click(activitySection().getByRole("button", { name: "Resume agent" }));

		expect(await activitySection().findByText("agent restart failed")).toBeInTheDocument();
	});

	it.each([
		["idle", "Idle"],
		["active", "Working"],
		["waiting_input", "Input Needed"],
		["exited", "Exited"],
	] as const)("renders %s from raw session activity", (state, label) => {
		renderWithQuery(
			<SessionInspector
				session={session([pr(7, "open")], {
					status: "review_pending",
					activity: { state, lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		expect(activitySection().getByText(label)).toBeInTheDocument();
	});

	it("renders unknown activity through the shared activity label", () => {
		renderWithQuery(
			<SessionInspector
				session={session([], {
					status: "working",
					activity: { state: "unknown", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		expect(activitySection().getByText("Unknown")).toBeInTheDocument();
		expect(activitySection().queryByText("Activity Unavailable")).not.toBeInTheDocument();
	});

	it("falls back to unknown when no activity has been reported", () => {
		renderWithQuery(<SessionInspector session={session([], { status: "working" })} />);

		expect(activitySection().getByText("Unknown")).toBeInTheDocument();
	});

	it("keeps the last known activity visible when the daemon reports no signal", () => {
		renderWithQuery(
			<SessionInspector
				session={session([], {
					status: "no_signal",
					activity: { state: "idle", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		const activityRow = activitySection()
			.getByText("Idle")
			.closest("[data-testid='inspector-timeline-event']") as HTMLElement;
		expect(within(activityRow).getByText("No Signal")).toBeInTheDocument();
	});

	it("does not derive the Activity label from PR-oriented session status", () => {
		renderWithQuery(
			<SessionInspector
				session={session([], {
					status: "review_pending",
					activity: { state: "idle", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		expect(activitySection().getByText("Idle")).toBeInTheDocument();
		expect(activitySection().queryByText("Input Needed")).not.toBeInTheDocument();
	});

	it.each([
		["ci_failed", "CI Failed"],
		["changes_requested", "Changes Requested"],
	] as const)("renders %s as an SCM state in the current Activity row", (status, label) => {
		renderWithQuery(
			<SessionInspector
				session={session([], {
					status,
					activity: { state: "idle", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		const activityRow = activitySection()
			.getByText("Idle")
			.closest("[data-testid='inspector-timeline-event']") as HTMLElement;
		expect(within(activityRow).getByText(label)).toBeInTheDocument();
	});

	it("renders PR conflicts as an SCM state in the current Activity row", () => {
		renderWithQuery(
			<SessionInspector
				session={session([pr(7, "open", { mergeability: "conflicting" })], {
					status: "working",
					activity: { state: "idle", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		const activityRow = activitySection()
			.getByText("Idle")
			.closest("[data-testid='inspector-timeline-event']") as HTMLElement;
		expect(within(activityRow).getByText("Conflict")).toBeInTheDocument();
	});

	it("does not timestamp the live Activity state as a historical event", () => {
		vi.useFakeTimers();
		vi.setSystemTime(new Date("2026-06-15T12:00:00Z"));

		renderWithQuery(
			<SessionInspector
				session={session([], {
					status: "working",
					updatedAt: "2026-06-15T11:55:00Z",
					activity: { state: "active", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		const activityRow = activitySection()
			.getByText("Working")
			.closest("[data-testid='inspector-timeline-event']") as HTMLElement;
		expect(within(activityRow).queryByText("2h ago")).not.toBeInTheDocument();
	});

	it("aligns text-row dots lower while keeping the Activity chip dot centered", () => {
		renderWithQuery(
			<SessionInspector
				session={session([pr(7, "open")], {
					status: "working",
					createdAt: "2026-06-15T09:00:00Z",
					activity: { state: "idle", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		const workspaceRow = activitySection()
			.getByText(/Created workspace/)
			.closest("[data-testid='inspector-timeline-event']") as HTMLElement;
		const workspaceMarker = workspaceRow.querySelector("span[aria-hidden='true'].rounded-full") as HTMLElement;
		expect(workspaceMarker.parentElement).toHaveClass("relative", "flex", "items-center");
		expect(workspaceMarker).toHaveClass("top-1.5");
		expect(workspaceMarker).not.toHaveClass("top-1/2", "-translate-y-1/2");

		const activityRow = activitySection()
			.getByText("Idle")
			.closest("[data-testid='inspector-timeline-event']") as HTMLElement;
		const activityMarker = activityRow.querySelector("span[aria-hidden='true'].rounded-full") as HTMLElement;
		expect(activityMarker.parentElement).toHaveClass("relative", "flex", "items-center");
		expect(activityMarker).toHaveClass("top-1/2", "-translate-y-1/2");
	});

	it("uses the timeline node as the single live activity indicator", () => {
		renderWithQuery(
			<SessionInspector
				session={session([], {
					status: "working",
					activity: { state: "active", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		const activityRow = activitySection()
			.getByText("Working")
			.closest("[data-testid='inspector-timeline-event']") as HTMLElement;
		const marker = activityRow.querySelector("span[aria-hidden='true'].rounded-full") as HTMLElement;
		expect(marker).toHaveClass("animate-status-pulse");
		expect(within(activityRow).getByText("Working").querySelector(".rounded-full")).not.toBeInTheDocument();
	});

	it("aligns summary section headings on one shared inset", () => {
		renderWithQuery(
			<SessionInspector
				session={session([pr(7, "open")], {
					status: "working",
					activity: { state: "active", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		for (const title of ["Pull request", "Completion", "Activity"]) {
			const heading = screen.getByText(title).parentElement;
			expect(heading?.parentElement).toHaveAttribute("data-testid", "inspector-section");
		}
	});

	it("keeps workspace, PR, and SCM context rows in the Activity timeline", () => {
		renderWithQuery(
			<SessionInspector
				session={session([pr(7, "open", { ci: "failing", review: "changes_requested" })], {
					status: "ci_failed",
					activity: { state: "idle", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		expect(activitySection().getByText(/Created workspace/)).toBeInTheDocument();
		expect(activitySection().getByText("Opened")).toBeInTheDocument();
		expect(activitySection().getByText("PR #7")).toBeInTheDocument();
		const activityRow = activitySection()
			.getByText("Idle")
			.closest("[data-testid='inspector-timeline-event']") as HTMLElement;
		expect(within(activityRow).getByText("CI Failed")).toBeInTheDocument();
		expect(within(activityRow).getByText("Changes Requested")).toBeInTheDocument();
	});

	it("links and timestamps draft, opened, and merged PR milestones from backend lifecycle times", async () => {
		const minutesAgo = (minutes: number) => new Date(Date.now() - minutes * 60 * 1000).toISOString();
		const summaries = [
			prSummary(8, "draft", {
				createdAt: minutesAgo(120),
				stateChangedAt: minutesAgo(120),
			}),
			prSummary(7, "open", {
				createdAt: minutesAgo(60),
				stateChangedAt: minutesAgo(15),
			}),
			prSummary(6, "merged", {
				createdAt: minutesAgo(180),
				stateChangedAt: minutesAgo(30),
			}),
		];
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/pr") {
				return { data: { sessionId: "sess-1", prs: summaries }, error: undefined };
			}
			return { data: { reviewerHandleId: "", reviews: [] }, error: undefined };
		});

		renderWithQuery(
			<SessionInspector
				session={session([pr(8, "draft"), pr(7, "open"), pr(6, "merged")], {
					status: "merged",
					activity: { state: "idle", lastActivityAt: "2026-06-15T11:50:00Z" },
				})}
			/>,
		);

		await waitFor(() => {
			expect(screen.getByRole("link", { name: "Draft PR #8" })).toHaveAttribute(
				"href",
				"https://github.com/acme/repo/pull/8",
			);
		});
		const draftLink = screen.getByRole("link", { name: "Draft PR #8" });
		expect(
			within(draftLink.closest("[data-testid='inspector-timeline-event']") as HTMLElement).getByText("2h ago"),
		).toBeInTheDocument();

		const openLink = screen.getByRole("link", { name: "Opened PR #7" });
		expect(
			within(openLink.closest("[data-testid='inspector-timeline-event']") as HTMLElement).getByText("1h ago"),
		).toBeInTheDocument();

		const mergedOpenedLink = screen.getByRole("link", { name: "Opened PR #6" });
		expect(
			within(mergedOpenedLink.closest("[data-testid='inspector-timeline-event']") as HTMLElement).getByText("3h ago"),
		).toBeInTheDocument();

		const mergedLink = screen.getByRole("link", { name: "Merged PR #6" });
		expect(
			within(mergedLink.closest("[data-testid='inspector-timeline-event']") as HTMLElement).getByText("30m ago"),
		).toBeInTheDocument();
		const doneRow = screen.getByText("Done").closest("[data-testid='inspector-timeline-event']") as HTMLElement;
		expect(within(doneRow).getByText("30m ago")).toBeInTheDocument();
	});

	it("renders the current state before reverse-chronological historical milestones", () => {
		vi.useFakeTimers();
		vi.setSystemTime(new Date("2026-06-15T12:00:00Z"));

		renderWithQuery(
			<SessionInspector
				session={session([pr(42, "draft"), pr(41, "open"), pr(40, "merged")], {
					status: "merged",
					createdAt: "2026-06-15T09:00:00Z",
					updatedAt: "2026-06-15T11:55:00Z",
					activity: { state: "idle", lastActivityAt: "2026-06-15T10:00:00Z" },
				})}
			/>,
		);

		const section = screen.getByText("Activity").closest("[data-testid='inspector-section']") as HTMLElement;
		const rows = Array.from(section.querySelectorAll("[data-testid='inspector-timeline-event']"), (row) =>
			row.textContent?.replace(/\s+/g, " ").trim(),
		);
		expect(rows).toEqual([
			"Idle",
			"Done",
			"Merged PR #40",
			"Opened PR #40",
			"Opened PR #41",
			"Draft PR #42",
			"Created workspace3h ago",
		]);

		const eventRows = section.querySelectorAll("[data-testid='inspector-timeline-event']");
		expect(section.querySelectorAll("[data-testid='inspector-timeline-connector']")).toHaveLength(eventRows.length - 1);
		expect(
			within(eventRows[eventRows.length - 1] as HTMLElement).queryByTestId("inspector-timeline-connector"),
		).not.toBeInTheDocument();
	});
});

describe("SessionInspector Usage & cost section", () => {
	beforeEach(() => {
		useUiStore.getState().setDeveloperMode(true);
	});

	it("stays hidden and does not fetch detailed usage outside Developer Mode", async () => {
		useUiStore.getState().setDeveloperMode(false);
		renderWithQuery(<SessionInspector session={session([])} />);

		expect(screen.queryByText("Usage & cost")).not.toBeInTheDocument();
		await waitFor(() => expect(getMock.mock.calls.length).toBeGreaterThan(0));
		expect(getMock.mock.calls.some(([path]) => path === "/api/v1/usage/sessions/{sessionId}")).toBe(false);
	});

	it("hides the section while detailed usage is loading", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/usage/sessions/{sessionId}") {
				return await new Promise(() => undefined);
			}
			return { data: { reviewerHandleId: "", reviews: [] }, error: undefined };
		});

		renderWithQuery(<SessionInspector session={session([])} />);

		await waitFor(() =>
			expect(getMock).toHaveBeenCalledWith("/api/v1/usage/sessions/{sessionId}", {
				params: { path: { sessionId: "sess-1" } },
			}),
		);
		expect(screen.queryByText("Usage & cost")).not.toBeInTheDocument();
	});

	it("keeps API failures visible instead of silently hiding the section", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/usage/sessions/{sessionId}") {
				return { data: undefined, error: { message: "usage query failed" } };
			}
			return { data: { reviewerHandleId: "", reviews: [] }, error: undefined };
		});

		renderWithQuery(<SessionInspector session={session([])} />);

		const usageTitle = await screen.findByText("Usage & cost", undefined, { timeout: 2_500 });
		const usageSection = usageTitle.closest("[data-testid='inspector-section']") as HTMLElement;
		expect(within(usageSection).getByRole("alert")).toHaveTextContent("Total tokens unavailable");
	});

	it("hides the section when no usage event or token value exists", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/usage/sessions/{sessionId}") {
				return {
					data: usageTelemetry({
						totals: {
							inputTokens: null,
							uncachedInputTokens: null,
							cacheReadTokens: null,
							cacheWriteTokens: null,
							outputTokens: null,
							reasoningTokens: null,
						},
						harnesses: [],
					}),
					error: undefined,
				};
			}
			return { data: { reviewerHandleId: "", reviews: [] }, error: undefined };
		});

		const { queryClient } = renderWithQuery(<SessionInspector session={session([])} />);

		await waitFor(() =>
			expect(queryClient.getQueryData(sessionUsageDetailQueryKey("sess-1"))).toEqual(
				expect.objectContaining({ harnesses: [] }),
			),
		);
		expect(screen.queryByText("Usage & cost")).not.toBeInTheDocument();
	});

	it("hides the section when an observed usage record has only zero token values", async () => {
		const zeroUsage = usageTelemetry({
			totals: {
				inputTokens: 0,
				uncachedInputTokens: 0,
				cacheReadTokens: 0,
				cacheWriteTokens: 0,
				outputTokens: 0,
				reasoningTokens: 0,
			},
			harnesses: [],
		});
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/usage/sessions/{sessionId}") {
				return { data: zeroUsage, error: undefined };
			}
			return { data: { reviewerHandleId: "", reviews: [] }, error: undefined };
		});

		const { queryClient } = renderWithQuery(<SessionInspector session={session([])} />);
		await waitFor(() =>
			expect(queryClient.getQueryData(sessionUsageDetailQueryKey("sess-1"))).toEqual(zeroUsage),
		);

		expect(screen.queryByText("Usage & cost")).not.toBeInTheDocument();
	});

	it("shows token telemetry and expands agent and model details on click", async () => {
		const user = userEvent.setup();
		renderWithQuery(<SessionInspector session={session([])} />);

		const usageTitle = await screen.findByText("Usage & cost");
		const usageSection = usageTitle.closest("[data-testid='inspector-section']") as HTMLElement;

		expect(within(usageSection).getByText("Total tokens")).toBeInTheDocument();
		expect(within(usageSection).getByText("Total cost")).toBeInTheDocument();
		expect(within(usageSection).getAllByText("Coming soon")).toHaveLength(1);
		expect(within(usageSection).getByText("Coming soon")).toHaveClass("text-settings-muted");
		expect(within(usageSection).getAllByText("1.2K").length).toBeGreaterThan(0);
		expect(within(usageSection).queryByText("1.2K tok")).not.toBeInTheDocument();
		expect(within(usageSection).getByText("Input tokens")).toBeInTheDocument();
		expect(within(usageSection).getByText("Output tokens")).toBeInTheDocument();
		expect(within(usageSection).getByText("Cache read tokens")).toBeInTheDocument();
		expect(within(usageSection).getByText("Cache write tokens")).toBeInTheDocument();
		expect(within(usageSection).getByText("Reasoning tokens")).toBeInTheDocument();
		expect(within(usageSection).getByText("Uncached input tokens")).toBeInTheDocument();
		expect(within(usageSection).getByTestId("session-usage-metrics")).toHaveClass(
			"rounded-lg",
			"border",
			"bg-(--color-bg-settings-input)",
		);
		expect(within(usageSection).getByText("Codex")).toBeInTheDocument();
		expect(within(usageSection).queryByText("OpenAI")).not.toBeInTheDocument();
		expect(within(usageSection).queryByText("Unknown provider")).not.toBeInTheDocument();
		expect(within(usageSection).queryByText(/coverage|collecting/i)).not.toBeInTheDocument();
		expect(within(usageSection).getByLabelText("Input tokens: 1,000 tokens")).toBeInTheDocument();
		expect(within(usageSection).queryByText("gpt-5.6")).not.toBeInTheDocument();
		const providerTrigger = within(usageSection).getByRole("button", {
			name: "Codex usage details",
		});
		expect(providerTrigger).toHaveAttribute("aria-expanded", "false");
		expect(within(providerTrigger).getByLabelText("Cost telemetry unavailable")).toHaveTextContent("—");

		await user.click(providerTrigger);
		expect(providerTrigger).toHaveAttribute("aria-expanded", "true");
		const providerPeek = await screen.findByRole("region", { name: "Codex usage peek" });
		expect(within(providerPeek).getByText("1 model")).toBeInTheDocument();
		expect(within(providerPeek).getByText("gpt-5.6")).toBeInTheDocument();
		expect(within(providerPeek).getByText("Input tokens")).toBeInTheDocument();
		expect(within(providerPeek).getByText("Output tokens")).toBeInTheDocument();
		expect(within(providerPeek).queryByText("Coming soon")).not.toBeInTheDocument();

		const modelTrigger = within(providerPeek).getByRole("button", {
			name: "gpt-5.6 usage details",
		});
		expect(modelTrigger).toHaveAttribute("aria-expanded", "false");
		expect(within(modelTrigger).getByLabelText("Cost telemetry unavailable")).toHaveTextContent("—");
		await user.click(modelTrigger);
		expect(modelTrigger).toHaveAttribute("aria-expanded", "true");
		const modelPeek = await screen.findByRole("region", { name: "gpt-5.6 usage peek" });
		expect(providerPeek).toContainElement(modelPeek);
		expect(within(modelPeek).getByText("Input tokens")).toBeInTheDocument();
		expect(within(modelPeek).getByText("Output tokens")).toBeInTheDocument();
		expect(within(modelPeek).getByText("Cache read tokens")).toBeInTheDocument();
		expect(within(modelPeek).getByText("Cache write tokens")).toBeInTheDocument();
		expect(within(modelPeek).getByText("Reasoning tokens")).toBeInTheDocument();
		expect(within(modelPeek).getByText("Uncached input tokens")).toBeInTheDocument();
		expect(within(usageSection).getAllByText("Coming soon")).toHaveLength(1);

		const sectionTitles = Array.from(
			document.querySelectorAll("[data-testid='inspector-section']"),
			(section) => section.querySelector("span")?.textContent,
		);
		expect(sectionTitles.indexOf("Usage & cost")).toBe(sectionTitles.indexOf("Activity") + 1);
		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/sessions/{sessionId}", {
			params: { path: { sessionId: "sess-1" } },
		});
	});

	it("does not expose backend integrity state as a usage warning", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/usage/sessions/{sessionId}") {
				return {
					data: usageTelemetry({
						incomplete: true,
					}),
					error: undefined,
				};
			}
			return { data: { reviewerHandleId: "", reviews: [] }, error: undefined };
		});

		renderWithQuery(<SessionInspector session={session([])} />);
		const usageTitle = await screen.findByText("Usage & cost");
		const usageSection = usageTitle.closest("[data-testid='inspector-section']") as HTMLElement;

		expect(within(usageSection).queryByText("Usage may be incomplete")).not.toBeInTheDocument();
		expect(within(usageSection).queryByLabelText(/Usage may be incomplete/)).not.toBeInTheDocument();
	});

	it("opens and closes provider and model details with the keyboard", async () => {
		const user = userEvent.setup();
		renderWithQuery(<SessionInspector session={session([])} />);

		const providerTrigger = await screen.findByRole("button", {
			name: "Codex usage details",
		});
		providerTrigger.focus();
		await user.keyboard("{Enter}");

		const providerPeek = await screen.findByRole("region", { name: "Codex usage peek" });
		const modelTrigger = within(providerPeek).getByRole("button", {
			name: "gpt-5.6 usage details",
		});
		modelTrigger.focus();
		await user.keyboard("{Enter}");
		expect(await screen.findByRole("region", { name: "gpt-5.6 usage peek" })).toBeInTheDocument();
		expect(modelTrigger).toHaveFocus();

		await user.keyboard(" ");
		expect(screen.queryByRole("region", { name: "gpt-5.6 usage peek" })).not.toBeInTheDocument();
		expect(modelTrigger).toHaveFocus();
	});

	it("shows multiple agents as compact rows with independent disclosures", async () => {
		const user = userEvent.setup();
		const codex = usageTelemetry().harnesses[0];
		if (!codex) throw new Error("missing Codex usage fixture");
		const claude = {
			...codex,
			harness: "claude-code",
			totals: { ...codex.totals, reasoningTokens: null },
			models: codex.models.map((model) => ({
				...model,
				modelId: "claude-opus-4.1",
				totals: { ...model.totals, reasoningTokens: null },
			})),
		};
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/usage/sessions/{sessionId}") {
				return { data: usageTelemetry({ harnesses: [codex, claude] }), error: undefined };
			}
			return { data: { reviewerHandleId: "", reviews: [] }, error: undefined };
		});

		renderWithQuery(<SessionInspector session={session([])} />);
		const usageTitle = await screen.findByText("Usage & cost");
		const usageSection = usageTitle.closest("[data-testid='inspector-section']") as HTMLElement;

		await waitFor(() => expect(within(usageSection).getByLabelText(/Codex usage details/)).toBeInTheDocument());
		const codexRow = within(usageSection).getByLabelText(/Codex usage details/);
		const claudeRow = within(usageSection).getByLabelText(/Claude usage details/);
		expect(within(usageSection).getByText("Codex")).toBeInTheDocument();
		expect(within(usageSection).getByText("Claude")).toBeInTheDocument();
		expect(within(usageSection).queryByText("OpenAI")).not.toBeInTheDocument();
		expect(within(usageSection).queryByText("Anthropic")).not.toBeInTheDocument();
		expect(within(usageSection).queryByText("Unknown provider")).not.toBeInTheDocument();
		expect(within(usageSection).queryByText("gpt-5.6")).not.toBeInTheDocument();
		expect(within(usageSection).queryByText("claude-opus-4.1")).not.toBeInTheDocument();

		await user.click(codexRow);
		const codexPeek = await screen.findByRole("region", { name: "Codex usage peek" });
		expect(within(codexPeek).getByText("gpt-5.6")).toBeInTheDocument();

		await user.click(claudeRow);
		const claudePeek = await screen.findByRole("region", { name: "Claude usage peek" });
		expect(within(claudePeek).getByText("claude-opus-4.1")).toBeInTheDocument();
		expect(within(claudePeek).getByLabelText("Reasoning tokens telemetry unavailable")).toHaveTextContent("—");
		expect(codexPeek).toBeInTheDocument();
	});
});

describe("SessionInspector tabs", () => {
	it("exposes Summary, Browser, and Files as inspector tabs", () => {
		renderWithQuery(<SessionInspector session={session([pr(1, "open")])} />);
		const tabs = screen.getAllByRole("tab").map((el) => el.textContent?.trim());
		expect(tabs).toEqual(["Summary", "Browser", "Files"]);
		expect(screen.queryByRole("tab", { name: /Reviews/ })).not.toBeInTheDocument();
	});

	it("does not render the overview card in the summary", () => {
		renderWithQuery(<SessionInspector session={{ ...session([]), issueId: "github:acme/project-one#42" }} />);

		expect(screen.queryByText("Overview")).not.toBeInTheDocument();
		expect(screen.queryByText("Issue")).not.toBeInTheDocument();
		expect(screen.queryByText("github:acme/project-one#42")).not.toBeInTheDocument();
		expect(screen.queryByText("Branch")).not.toBeInTheDocument();
	});
});

describe("SessionInspector summary reviews", () => {
	// PR rows start collapsed, so opening the Summary tab alone shows only their titles.
	// Reveal every row, since these tests are about what a review says.
	const openReviewsSection = async () => {
		// Rows arrive with the reviews query, so wait for them before expanding.
		const rows = await screen.findAllByTestId("review-pr-row").catch(() => []);
		for (const row of rows) {
			if (row.getAttribute("aria-expanded") === "false") await userEvent.click(row);
		}
	};

	it("triggers a review and opens the returned reviewer terminal", async () => {
		mockCommonGets([], "", [reviewState(3, "needs_review")]);
		const runningReview = { ...approvedReview, status: "running", verdict: "", body: "" };
		postMock.mockResolvedValue({
			response: { status: 201 },
			data: {
				reviewerHandleId: "reviewer-pane",
				reviews: [{ ...reviewState(3, "running"), latestRun: runningReview }],
			},
		});
		const onOpenReviewerTerminal = vi.fn();

		renderWithQuery(
			<SessionInspector onOpenReviewerTerminal={onOpenReviewerTerminal} session={session([pr(3, "open")])} />,
		);
		await openReviewsSection();

		await userEvent.click(await screen.findByRole("button", { name: /run review/i }));

		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/reviews/trigger", {
				params: { path: { sessionId: "sess-1" } },
			}),
		);
		expect(onOpenReviewerTerminal).toHaveBeenCalledWith({ handleId: "reviewer-pane", harness: "codex" });
	});

	it("shows claude-code as the default reviewer before a run exists", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/reviews") {
				return { data: { reviewerHandleId: "", reviews: [] } };
			}
			if (path === "/api/v1/projects/{id}") {
				return {
					data: {
						status: "ok",
						project: {
							id: "ws-1",
							kind: "git",
							name: "my-app",
							path: "/repo",
							repo: "my-app",
							defaultBranch: "main",
							config: {},
						},
					},
				};
			}
			return { data: undefined };
		});

		renderWithQuery(<SessionInspector session={sessionWithProvider([pr(3, "open")], "codex")} />);
		await openReviewsSection();

		expect(await screen.findByRole("button", { name: /Select reviewer agent/ })).toHaveTextContent("claude-code");
		expect(screen.queryByText("reviewer")).not.toBeInTheDocument();
	});

	it("hides review summary sections when no review data exists", async () => {
		mockCommonGets([], "", []);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findByRole("button", { name: "Run review" })).toBeInTheDocument();
		expect(screen.queryByText("AO code reviews")).not.toBeInTheDocument();
		expect(screen.queryByText("Reviews on the pull request")).not.toBeInTheDocument();
	});

	it("hides AO code reviews until a review run has been triggered", async () => {
		mockCommonGets([], "", [reviewState(3, "needs_review", "abc123")]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findByRole("button", { name: "Run review" })).toBeInTheDocument();
		expect(screen.queryByText("AO code reviews")).not.toBeInTheDocument();
		expect(screen.queryByText("Reviewable change 3")).not.toBeInTheDocument();
	});

	it("shows AO code reviews for verdict-only review states", async () => {
		mockCommonGets([], "reviewer-pane", [reviewState(3, "changes_requested", "abc123")]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findByText("Reviewable change 3")).toBeInTheDocument();
		expect(screen.getByText("Changes requested")).toBeInTheDocument();
	});

	it("shows eligible and up-to-date open PR review rows", async () => {
		mockCommonGets([approvedReview], "reviewer-pane", [
			reviewState(3, "needs_review", "abc123"),
			reviewState(4, "up_to_date", "def456"),
			reviewState(5, "ineligible", "ghi789"),
		]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open"), pr(4, "open"), pr(5, "draft")])} />);
		await openReviewsSection();

		expect(screen.getByRole("button", { name: /Select reviewer agent/ })).toHaveTextContent("codex");
		expect(screen.queryByText("Reviewable change 3")).not.toBeInTheDocument();
		expect(await screen.findByText("Reviewable change 4")).toBeInTheDocument();
		expect(
			within(screen.getByText("Reviewable change 4").closest("[data-testid='review-pr-row']") as HTMLElement).getByText(
				"Approved",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("Reviewable change 5")).not.toBeInTheDocument();
		expect(screen.getAllByText("Approved")).not.toHaveLength(0);
		expect(screen.getByRole("button", { name: "Re-run review" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Run" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Re-run" })).not.toBeInTheDocument();
	});

	// A review body is a multi-paragraph write-up. Rendered whole it buries the
	// verdict and every earlier pass below it, which is the opposite of reading
	// the history in one place.
	it("clamps a long review summary and expands it in place", async () => {
		const longBody = Array.from({ length: 12 }, (_, i) => `Finding ${i + 1}: something worth reading.`).join("\n");
		mockCommonGets([], "reviewer-pane", [
			{ ...reviewState(3, "up_to_date", "abc123"), latestRun: { ...approvedReview, body: longBody } },
		]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		const summary = await screen.findByTestId("review-run-summary");
		expect(summary).toHaveClass("line-clamp-4");

		await userEvent.click(screen.getByRole("button", { name: "Show more" }));
		expect(screen.getByTestId("review-run-summary")).not.toHaveClass("line-clamp-4");

		await userEvent.click(screen.getByRole("button", { name: "Show less" }));
		expect(screen.getByTestId("review-run-summary")).toHaveClass("line-clamp-4");
	});

	// Nothing to hide, so offering to expand would be noise.
	it("does not offer to expand a short review summary", async () => {
		mockCommonGets([], "reviewer-pane", [
			{ ...reviewState(3, "up_to_date", "abc123"), latestRun: { ...approvedReview, body: "Looks good." } },
		]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findByTestId("review-run-summary")).not.toHaveClass("line-clamp-4");
		expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
	});

	it("renders AO review summaries as Markdown", async () => {
		mockCommonGets([], "reviewer-pane", [
			{
				...reviewState(3, "up_to_date", "abc123"),
				latestRun: { ...approvedReview, body: "Fix **auth validation**.\n\n- Add tests" },
			},
		]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		const summary = await screen.findByTestId("review-run-summary");
		expect(within(summary).getByText("auth validation").tagName).toBe("STRONG");
		expect(within(summary).getByText("Add tests").tagName).toBe("LI");
		expect(summary).not.toHaveTextContent("**auth validation**");
	});

	// An AO pass only gets a review-comment anchor once it is submitted to
	// GitHub, so without a fallback an unsubmitted pass is a dead end.
	it("links a run to its GitHub review, falling back to the PR when it has none", async () => {
		mockCommonGets([], "reviewer-pane", [
			{
				...reviewState(3, "up_to_date", "abc123"),
				latestRun: { ...approvedReview, githubReviewId: "98765" },
			},
		]);
		const { unmount } = renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();
		expect(await screen.findByRole("link", { name: /View review/ })).toHaveAttribute(
			"href",
			"https://example.com/pr/3#pullrequestreview-98765",
		);
		unmount();

		mockCommonGets([], "reviewer-pane", [
			{ ...reviewState(3, "up_to_date", "abc123"), latestRun: { ...approvedReview, githubReviewId: "" } },
		]);
		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();
		expect(await screen.findByRole("link", { name: /View on PR/ })).toBeInTheDocument();
	});

	it.each([
		["needs_review", "changes_requested", "Not run", "Run review"],
		["running", "approved", "Reviewing...", "Stop review"],
	] as const)(
		"keeps the current AO review state clear while the current head is %s",
		async (status, previousVerdict, runLabel, actionLabel) => {
			const current = {
				...reviewState(3, status, "sha-current"),
				previousRun: {
					...approvedReview,
					id: "run-previous",
					status: "delivered",
					verdict: previousVerdict,
					body: "Previous review summary with actionable detail.",
					githubReviewId: "98765",
					targetSha: "sha-previous",
				},
			};
			if (status === "running") {
				current.latestRun = {
					...approvedReview,
					id: "run-current",
					status: "running",
					verdict: "",
					targetSha: "sha-current",
				};
			}
			mockCommonGets([], "reviewer-pane", [current]);

			renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
			await openReviewsSection();

			expect(await screen.findAllByText(runLabel)).not.toHaveLength(0);
			expect(screen.getByText("Previous review summary with actionable detail.")).toBeInTheDocument();
			expect(screen.queryByText(/Previous:/)).not.toBeInTheDocument();
			expect(screen.queryByText("Changes requested")).not.toBeInTheDocument();
			expect(screen.getByRole("link", { name: "View review" })).toHaveAttribute(
				"href",
				"https://example.com/pr/3#pullrequestreview-98765",
			);
			// A run in flight gets its own live strip naming the harness, not just a
			// word on the button.
			if (status === "running") {
				expect(screen.getByText("Review in progress · codex")).toBeInTheDocument();
			} else {
				expect(screen.queryByText(/is reviewing this change/)).not.toBeInTheDocument();
			}
			expect(screen.getByRole("button", { name: actionLabel })).toBeInTheDocument();
		},
	);

	it("shows PRs with unresolved comments but no decisive review", async () => {
		mockCommonGets([], "reviewer-pane", [reviewState(3, "up_to_date", "sha-1")]);
		const previous = getMock.getMockImplementation()!;
		getMock.mockImplementation(async (path: string, opts?: unknown) => {
			if (path === "/api/v1/sessions/{sessionId}/pr") {
				return {
					data: {
						prs: [
							{
								number: 3,
								title: "Reviewable change 3",
								url: "https://example.com/pr/3",
								htmlUrl: "https://example.com/pr/3",
								state: "open",
								ci: { state: "passing", failingChecks: [], prUrl: "https://example.com/pr/3" },
								mergeability: {
									state: "mergeable",
									reasons: [],
									prUrl: "https://example.com/pr/3",
									conflictFiles: [],
								},
								review: {
									decision: "changes_requested",
									hasUnresolvedHumanComments: true,
									reviews: [],
									unresolvedBy: [
										{
											reviewerId: "maya",
											count: 2,
											links: [
												{ file: "a.ts", line: 3, autoInjectReview: true },
												{ file: "a.ts", line: 9, autoInjectReview: true },
											],
										},
									],
								},
							},
						],
					},
				};
			}
			return previous(path, opts);
		});
		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findByRole("button", { name: "Re-run review" })).toBeInTheDocument();
		expect((await screen.findAllByText("Reviewable change 3")).length).toBeGreaterThan(0);
		expect(screen.getByText(/2 unresolved/)).toBeInTheDocument();
		// AO's runs and the PR's own reviews share one section keyed by PR, so the
		// unresolved count rides the same row as the AO verdict.
		expect(screen.getByText("Reviews")).toBeInTheDocument();
		expect(screen.queryByText("Reviews on the pull request")).not.toBeInTheDocument();
		expect(screen.queryByText("AO code reviews")).not.toBeInTheDocument();
		expect(screen.queryByText("No unresolved threads.")).not.toBeInTheDocument();
	});

	it("renders PR review summaries as Markdown", async () => {
		mockCommonGets([], "reviewer-pane", [reviewState(3, "up_to_date", "sha-1")]);
		const previous = getMock.getMockImplementation()!;
		getMock.mockImplementation(async (path: string, opts?: unknown) => {
			if (path === "/api/v1/sessions/{sessionId}/pr") {
				return {
					data: {
						prs: [
							{
								number: 3,
								title: "Reviewable change 3",
								url: "https://example.com/pr/3",
								htmlUrl: "https://example.com/pr/3",
								state: "open",
								ci: { state: "passing", failingChecks: [], prUrl: "https://example.com/pr/3" },
								mergeability: {
									state: "mergeable",
									reasons: [],
									prUrl: "https://example.com/pr/3",
									conflictFiles: [],
								},
								review: {
									decision: "approved",
									hasUnresolvedHumanComments: false,
									reviews: [
										{
											reviewerId: "maya",
											verdict: "approved",
											submittedAt: "2026-06-16T11:00:00Z",
											body: "Looks **ready**.\n\n1. Ship it",
											reviewUrl: "https://example.com/pr/3#pullrequestreview-456",
											autoInjectReview: true,
										},
									],
									unresolvedBy: [],
								},
							},
						],
					},
				};
			}
			return previous(path, opts);
		});

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		const summary = await screen.findByTestId("github-review-summary");
		expect(within(summary).getByText("ready").tagName).toBe("STRONG");
		expect(within(summary).getByText("Ship it").tagName).toBe("LI");
		expect(summary).not.toHaveTextContent("**ready**");
	});

	it("marks SCM reviews and individual comments using their stored injection decision", async () => {
		const previous = getMock.getMockImplementation()!;
		getMock.mockImplementation(async (path: string, opts?: unknown) => {
			if (path === "/api/v1/sessions/{sessionId}/pr") {
				return {
					data: {
						prs: [
							prSummary(3, "open", {
								review: {
									decision: "changes_requested",
									hasUnresolvedHumanComments: true,
									reviews: [
										{
											reviewerId: "maya",
											verdict: "changes_requested",
											submittedAt: "2026-06-16T11:00:00Z",
											autoInjectReview: true,
										},
									],
									unresolvedBy: [
										{
											reviewerId: "maya",
											count: 2,
											links: [
												{ file: "a.ts", line: 3, autoInjectReview: true },
												{ file: "a.ts", line: 9, autoInjectReview: false },
											],
										},
									],
								},
							}),
						],
					},
				};
			}
			return previous(path, opts);
		});

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findByText("Not injected")).toBeInTheDocument();
		expect(screen.getByText("On GitHub")).toBeInTheDocument();
	});

	it("marks an AO review using its stored injection decision", async () => {
		mockCommonGets(
			[],
			"reviewer-pane",
			[
				{
					...reviewState(3, "up_to_date", "abc123"),
					latestRun: { ...approvedReview, autoInjectReview: false },
				},
			],
		);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findByText("Not injected")).toBeInTheDocument();
		expect(screen.getByText("AO review")).toBeInTheDocument();
	});

	it("persists the automatic review injection toggle", async () => {
		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		const toggle = screen.getByRole("switch", { name: "Automatically send review feedback" });
		expect(toggle).toBeChecked();
		await userEvent.click(toggle);

		await waitFor(() =>
			expect(patchMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/auto-inject-review", {
				params: { path: { sessionId: "sess-1" } },
				body: { autoInjectReview: false },
			}),
		);
	});

	it("persists the chosen reviewer for the session and uses it for the run", async () => {
		mockCommonGets([], "reviewer-pane", [reviewState(3, "needs_review", "sha-1")]);
		postMock.mockResolvedValue({ data: { reviewerHandleId: "", reviews: [] }, response: { status: 201 } });

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		await userEvent.click(await screen.findByRole("button", { name: /Select reviewer agent/ }));
		await userEvent.click(await screen.findByRole("menuitem", { name: /opencode/ }));
		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/reviews/switch", {
				params: { path: { sessionId: "sess-1" } },
				body: { harness: "opencode" },
			}),
		);
		await userEvent.click(screen.getByRole("button", { name: "Run review" }));

		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/reviews/trigger", {
			params: { path: { sessionId: "sess-1" } },
			body: { harness: "opencode" },
		});
	});

	it("names the reviewer that is actually running, not whichever PR comes first", async () => {
		// One PR reviewed earlier by claude-code, another running under codex.
		const done = {
			...reviewState(3, "up_to_date", "sha-a"),
			latestRun: { ...approvedReview, id: "run-done", harness: "claude-code", status: "complete" },
		};
		const running = {
			...reviewState(4, "running", "sha-b"),
			latestRun: {
				...approvedReview,
				id: "run-live",
				harness: "codex",
				status: "running",
				verdict: "",
				createdAt: "2026-01-02T00:00:00Z",
			},
		};
		mockCommonGets([], "reviewer-pane", [done, running]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open"), pr(4, "open")])} />);
		await openReviewsSection();

		expect(await screen.findByText("Review in progress · codex")).toBeInTheDocument();
		expect(screen.queryByText("Review in progress · claude-code")).not.toBeInTheDocument();
	});

	it("keeps every harness summary visible when selecting the agent for the next run", async () => {
		const state = {
			...reviewState(3, "changes_requested", "sha-1"),
			latestRun: {
				...approvedReview,
				id: "run-codex",
				harness: "codex",
				verdict: "changes_requested",
				body: "codex asked for tests.",
				createdAt: "2026-01-03T00:00:00Z",
			},
		};
		mockCommonGets([], "reviewer-pane", [state]);
		const previous = getMock.getMockImplementation()!;
		getMock.mockImplementation(async (path: string, opts?: unknown) => {
			if (path === "/api/v1/sessions/{sessionId}/reviews") {
				return {
					data: {
						reviewerHandleId: "reviewer-pane",
						reviews: [state],
						runs: [
							state.latestRun,
							{
								...approvedReview,
								id: "run-claude",
								harness: "claude-code",
								verdict: "approved",
								body: "claude-code found nothing blocking.",
								createdAt: "2026-01-01T00:00:00Z",
							},
						],
					},
				};
			}
			return previous(path, opts);
		});

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findByText("codex asked for tests.")).toBeInTheDocument();
		expect(screen.getByText("claude-code found nothing blocking.")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: /Select reviewer agent/ }));
		await userEvent.click(screen.getByRole("menuitem", { name: /claude-code/ }));
		expect(screen.getByText("claude-code found nothing blocking.")).toBeInTheDocument();
		expect(screen.getByText("codex asked for tests.")).toBeInTheDocument();
		expect(screen.getByText("Reviewable change 3")).toBeInTheDocument();
	});

	it("locks the reviewer choice while one is running", async () => {
		const running = {
			...reviewState(3, "running", "sha-1"),
			latestRun: { ...approvedReview, id: "run-live", harness: "codex", status: "running", verdict: "" },
		};
		mockCommonGets([], "reviewer-pane", [running]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		// AO runs one reviewer per worker, so a second harness cannot start
		// alongside it. Say so rather than silently ignoring the choice.
		expect(screen.getByText("Review in progress · codex")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /Select reviewer agent/ })).toBeDisabled();
	});

	it("hides the previous verdict after the current head review completes", async () => {
		const current = {
			...reviewState(3, "up_to_date", "sha-current"),
			previousRun: {
				...approvedReview,
				id: "run-previous",
				status: "delivered",
				verdict: "changes_requested",
				targetSha: "sha-previous",
			},
		};
		mockCommonGets([], "reviewer-pane", [current]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findAllByText("Approved")).not.toHaveLength(0);
		expect(screen.queryByText(/Previous:/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Re-run review" })).toBeInTheDocument();
	});

	it("shows a no-needed-reviews notice instead of opening the terminal when the backend reuses runs", async () => {
		mockCommonGets([approvedReview], "reviewer-pane", [reviewState(3, "up_to_date")]);
		postMock.mockResolvedValue({
			response: { status: 200 },
			data: {
				reviewerHandleId: "reviewer-pane",
				reviews: [],
			},
		});
		const onOpenReviewerTerminal = vi.fn();

		renderWithQuery(
			<SessionInspector onOpenReviewerTerminal={onOpenReviewerTerminal} session={session([pr(3, "open")])} />,
		);
		await openReviewsSection();

		await userEvent.click(await screen.findByRole("button", { name: /re-run review/i }));

		// The notice is a compact marker; the sentence itself is its accessible name
		// and rides a tooltip, so it costs the rail one line instead of a boxed
		// paragraph that outlives the click that caused it.
		const alreadyReviewed = await screen.findByRole("button", {
			name: "This commit has already been reviewed. Push a new commit to run another review.",
		});
		expect(alreadyReviewed).toHaveTextContent("This commit has already been reviewed");
		expect(onOpenReviewerTerminal).not.toHaveBeenCalled();
	});

	it("cancels the running review instead of allowing rerun", async () => {
		mockCommonGets([approvedReview], "reviewer-pane", [
			reviewState(3, "running", "abc123"),
			reviewState(4, "up_to_date", "def456"),
		]);
		const onOpenReviewerTerminal = vi.fn();

		renderWithQuery(
			<SessionInspector onOpenReviewerTerminal={onOpenReviewerTerminal} session={session([pr(3, "open")])} />,
		);
		await openReviewsSection();

		await waitFor(() => expect(screen.getByRole("button", { name: "Stop review" })).toBeEnabled());
		expect(screen.queryByRole("button", { name: /re-run review/i })).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: /stop review/i }));

		await waitFor(() => {
			expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/reviews/cancel", {
				params: { path: { sessionId: "sess-1" } },
			});
		});
		expect(onOpenReviewerTerminal).not.toHaveBeenCalled();
	});

	it("shows cancelled review runs without marking them failed", async () => {
		mockCommonGets([], "reviewer-pane", [
			{ ...reviewState(3, "needs_review", "abc123"), latestRun: { ...failedReview, status: "cancelled" } },
		]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findAllByText("Cancelled")).toHaveLength(1);
		expect(screen.queryByText("Failed")).not.toBeInTheDocument();
		expect(screen.queryByText("reviewer crashed")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Re-run review" })).toBeEnabled();
	});

	it("shows the reviewer identity and aggregate verdict", async () => {
		mockCommonGets([], "reviewer-pane", [
			{
				...reviewState(3, "changes_requested", "abc123"),
				latestRun: { ...approvedReview, verdict: "changes_requested", body: "Please fix auth." },
			},
		]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findByRole("button", { name: /Select reviewer agent/ })).toHaveTextContent("codex");
		expect(screen.queryByText("reviewer")).not.toBeInTheDocument();
		expect(screen.queryByText("sess-1")).not.toBeInTheDocument();
		expect(screen.queryByText("review session")).not.toBeInTheDocument();
		expect(screen.getAllByText("Changes requested")).not.toHaveLength(0);
	});

	it("shows failed latest runs as failed and still allows rerun", async () => {
		mockCommonGets([failedReview], "reviewer-pane", [
			{ ...reviewState(3, "needs_review", "abc123"), latestRun: failedReview },
		]);

		renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
		await openReviewsSection();

		expect(await screen.findAllByText("Failed")).not.toHaveLength(0);
		expect(screen.getByRole("button", { name: "Re-run review" })).toBeEnabled();
	});

	it("does not expose the Reviews tab when the session has no PRs", async () => {
		mockCommonGets();
		renderWithQuery(<SessionInspector session={session([])} />);

		await screen.findByRole("tab", { name: /Summary/ });
		expect(screen.queryByRole("tab", { name: /Reviews/ })).not.toBeInTheDocument();
	});
});
