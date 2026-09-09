import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionInspector } from "./SessionInspector";
import { TooltipProvider } from "./ui/tooltip";
import type { SessionPRSummary } from "../hooks/useSessionScmSummary";
import { sessionScmSummaryQueryKey } from "../hooks/useSessionScmSummary";
import { sessionWorkspaceFilesQueryKey } from "../hooks/useSessionWorkspaceFiles";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import { useUiStore } from "../stores/ui-store";
import type {
  PRState,
  PullRequestFacts,
  WorkspaceSession,
  WorkspaceSummary,
} from "../types/workspace";

const { getMock, navigateMock, patchMock, putMock, postMock } = vi.hoisted(
  () => ({
    getMock: vi.fn(),
    navigateMock: vi.fn(),
    patchMock: vi.fn(),
    putMock: vi.fn(),
    postMock: vi.fn(),
  }),
);

function postCallsFor(path: string) {
  return postMock.mock.calls.filter(([calledPath]) => calledPath === path);
}

function setRenderedOverflow(element: HTMLElement, overflowing: boolean) {
  Object.defineProperties(element, {
    clientHeight: { configurable: true, value: 64 },
    scrollHeight: { configurable: true, value: overflowing ? 96 : 64 },
  });
  fireEvent(window, new Event("resize"));
}

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

const pr = (
  n: number,
  state: PRState,
  overrides: Partial<PullRequestFacts> = {},
): PullRequestFacts => ({
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

const session = (
  prs: PullRequestFacts[],
  overrides: Partial<WorkspaceSession> = {},
): WorkspaceSession => ({
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
  autoInjectCI: true,
  prs,
  ...overrides,
});

const sessionWithProvider = (
  prs: PullRequestFacts[],
  provider: WorkspaceSession["provider"],
): WorkspaceSession => ({
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
    ci: { autoInjectCI: true, state: "passing", failingChecks: [] },
    review: {
      decision: "none",
      hasUnresolvedHumanComments: false,
      unresolvedBy: [],
    },
    mergeability: {
      state: "mergeable",
      reasons: [],
      prUrl: url,
      conflictFiles: [],
    },
    updatedAt: "2026-06-15T12:00:00Z",
    ...overrides,
  };
};

function renderWithQuery(
  children: ReactNode,
  workspaces?: WorkspaceSummary[],
  seed?: (client: QueryClient) => void,
) {
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

function commonGetsResponder(
  _unusedRuns: unknown[] = [],
  reviewerHandleId = "",
  reviews: unknown[] = [],
) {
  return async (path: string) => {
    if (path === "/api/v1/agents/readiness") {
      const agents = ["claude-code", "codex", "opencode"].map((id) => agentReadiness(id));
      return { data: { agents } };
    }
    if (path === "/api/v1/agents/{agent}/models") {
      return {
        data: {
          agentId: "unknown",
          selectionMode: "text",
          models: [],
          allowCustom: false,
          source: "manual",
          fetchedAt: "2026-08-30T00:00:00Z",
          stale: false,
        },
        error: undefined,
      };
    }
    if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
      return {
        data: { sessionId: "sess-1", files: [], truncated: false },
        error: undefined,
      };
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
  };
}

function mockCommonGets(
  _unusedRuns: unknown[] = [],
  reviewerHandleId = "",
  reviews: unknown[] = [],
) {
  getMock.mockImplementation(commonGetsResponder(_unusedRuns, reviewerHandleId, reviews));
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
    status === "up_to_date"
      ? { ...approvedReview, prUrl: `https://example.com/pr/${n}`, targetSha }
      : undefined,
});

beforeEach(() => {
  getMock.mockReset();
  navigateMock.mockReset();
  patchMock.mockReset();
  postMock.mockReset();
  useUiStore.setState({ developerMode: false, inspectorSessions: {} });
  putMock.mockReset();
  mockCommonGets();
  patchMock.mockResolvedValue({
    data: { ok: true },
    error: undefined,
    response: { status: 200 },
  });
  postMock.mockResolvedValue({
    data: { ok: true, sessionId: "sess-1" },
    error: undefined,
  });
  putMock.mockResolvedValue({
    data: { session: {} },
    error: undefined,
    response: { status: 200 },
  });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("SessionInspector tabs", () => {
  it("gives the Browser viewport the full inspector body without the default content gutter", async () => {
    renderWithQuery(<SessionInspector session={session([])} />);

    await userEvent.click(screen.getByRole("tab", { name: "Browser" }));

    const body = screen
      .getByRole("complementary", { name: "Session inspector" })
      .querySelector(".session-inspector__body--browser");
    expect(body).toHaveClass(
      "session-inspector__body--browser",
      "p-0",
      "overflow-hidden",
    );
    expect(body).not.toHaveClass(
      "p-3",
      "pb-4",
      "@max-[300px]/inspector:px-2.5",
    );
  });

  it("keeps rail tabs square instead of stretching across the inspector", () => {
    renderWithQuery(<SessionInspector session={session([])} />);

    const summaryTab = screen.getByRole("tab", { name: "Summary" });


		expect(summaryTab).not.toHaveClass("flex-1");
		expect(summaryTab).toHaveClass("size-control-md", "p-0", "shrink-0");
		expect(summaryTab).not.toHaveClass("h-control-md", "px-1");
		expect(summaryTab).toHaveAttribute("title", "Summary");
	});

  it("shows the glow only while real browser activity is unseen", () => {
    const currentSession = session([]);
    const view = renderWithQuery(<SessionInspector session={currentSession} />);
    expect(
      screen.queryByTestId("browser-unseen-indicator"),
    ).not.toBeInTheDocument();
    view.unmount();

    useUiStore.getState().setBrowserUnseen(currentSession.id, true);
    renderWithQuery(<SessionInspector session={currentSession} />);
    expect(screen.getByTestId("browser-unseen-indicator")).toBeInTheDocument();

    act(() =>
      useUiStore.getState().setInspectorView(currentSession.id, "browser"),
    );
    expect(
      screen.queryByTestId("browser-unseen-indicator"),
    ).not.toBeInTheDocument();
  });

  it("renders the supplied files view when the Files tab opens", async () => {
    const onOpenFiles = vi.fn();
    renderWithQuery(
      <SessionInspector
        filesView={<div>workspace file review</div>}
        onOpenFiles={onOpenFiles}
        session={session([])}
      />,
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
      expect(getMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/workspace/files",
        {
          params: { path: { sessionId: "sess-1" } },
        },
      ),
    );
  });

  it("shows a live changed-file count on the Files tab once the shared cache is populated", () => {
    renderWithQuery(
      <SessionInspector session={session([])} />,
      undefined,
      (client) => {
        client.setQueryData(sessionWorkspaceFilesQueryKey("sess-1"), {
          sessionId: "sess-1",
          truncated: false,
          files: [
            {
              path: "src/App.tsx",
              status: "modified",
              additions: 2,
              deletions: 1,
              size: 120,
              binary: false,
            },
            {
              path: "README.md",
              status: "unmodified",
              additions: 0,
              deletions: 0,
              size: 80,
              binary: false,
            },
          ],
        });
      },
    );

    const filesTab = screen.getByRole("tab", { name: "Files" });
    expect(within(filesTab).getByText("1 File")).toBeInTheDocument();
    // The accessible name stays static so existing name-based tab queries keep resolving.
    expect(filesTab).toHaveAttribute("title", "Files");
  });

  it("distinguishes a checked-but-clean workspace (0 Files) from an unopened tab (Files)", () => {
    renderWithQuery(
      <SessionInspector session={session([])} />,
      undefined,
      (client) => {
        client.setQueryData(sessionWorkspaceFilesQueryKey("sess-1"), {
          sessionId: "sess-1",
          truncated: false,
          files: [
            {
              path: "README.md",
              status: "unmodified",
              additions: 0,
              deletions: 0,
              size: 80,
              binary: false,
            },
          ],
        });
      },
    );

    const filesTab = screen.getByRole("tab", { name: "Files" });
    expect(within(filesTab).getByText("0 Files")).toBeInTheDocument();
  });

  it("keeps collapsed inspector content hidden and inert", () => {
    renderWithQuery(
      <SessionInspector isInspectorVisible={false} session={session([])} />,
    );

    expect(screen.queryByRole("tablist")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /inspector panel/i }),
    ).not.toBeInTheDocument();
    expect(
      document.querySelector("[aria-hidden='true'][inert]"),
    ).toBeInTheDocument();
  });
});

describe("SessionInspector PR section", () => {
  // Scope assertions to the PR section so the card order is explicit.
  const prSection = (title: string) =>
    within(
      screen
        .getByText(title)
        .closest("[data-testid='inspector-section']") as HTMLElement,
    );

  it("renders one card per PR, ordered actionable-first, when a session owns a stack", () => {
    renderWithQuery(
      <SessionInspector
        session={session([pr(40, "merged"), pr(41, "open"), pr(42, "draft")])}
      />,
    );

    expect(screen.getByText("Pull requests (3)")).toBeInTheDocument();
    const cards = prSection("Pull requests (3)")
      .getAllByText(/^PR #\d+$/)
      .map((el) => el.textContent);
    // open (41), draft (42), merged (40)
    expect(cards).toEqual(["PR #41", "PR #42", "PR #40"]);
  });

  it("uses the singular heading and shows enriched facts for a single PR", () => {
    renderWithQuery(
      <SessionInspector session={session([pr(7, "open")])} />,
      undefined,
      (client) => {
        client.setQueryData(sessionScmSummaryQueryKey("sess-1"), [
          prSummary(7, "open", {
            review: {
              decision: "approved",
              hasUnresolvedHumanComments: false,
              unresolvedBy: [],
            },
          }),
        ]);
      },
    );

    expect(screen.getByText("Pull request")).toBeInTheDocument();
    expect(screen.queryByText(/Pull requests \(/)).not.toBeInTheDocument();
    expect(prSection("Pull request").getByText("PR #7")).toBeInTheDocument();
    expect(
      prSection("Pull request").getByText("Ready to merge"),
    ).toBeInTheDocument();
    expect(
      prSection("Pull request").getByText("Checks passing"),
    ).toBeInTheDocument();
    expect(
      prSection("Pull request").getByRole("link", { name: "PR 7" }),
    ).toHaveClass("text-sm");
    expect(
      prSection("Pull request").getByRole("link", { name: "PR 7" }),
    ).toHaveAttribute("href", "https://github.com/acme/repo/pull/7");
    expect(prSection("Pull request").getByText("open")).toHaveClass(
      "text-[9px]",
      "leading-none",
    );
    expect(
      prSection("Pull request").getByRole("button", { name: "Merge PR #7" }),
    ).toBeInTheDocument();
  });

  it("merges a ready pull request directly through the daemon", async () => {
    const readyPR = prSummary(7, "open", {
      url: "https://example.com/pr/7",
      headSha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      review: {
        decision: "approved",
        hasUnresolvedHumanComments: false,
        unresolvedBy: [],
      },
    });
    renderWithQuery(
      <SessionInspector session={session([pr(7, "open")])} />,
      undefined,
      (client) => {
        client.setQueryData(sessionScmSummaryQueryKey("sess-1"), [readyPR]);
      },
    );

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

    expect(
      screen.queryByRole("button", { name: "Merge PR #7" }),
    ).not.toBeInTheDocument();
  });

  it.each(["unknown", "blocked", "unstable"] as const)(
    "does not offer Merge when provider mergeability is %s",
    (mergeability) => {
      renderWithQuery(
        <SessionInspector session={session([pr(7, "open")])} />,
        undefined,
        (client) => {
          client.setQueryData(sessionScmSummaryQueryKey("sess-1"), [
            prSummary(7, "open", {
              ci: { autoInjectCI: true, state: "passing", failingChecks: [] },
              review: { decision: "approved", hasUnresolvedHumanComments: false, unresolvedBy: [] },
              mergeability: { state: mergeability, reasons: [], prUrl: "https://example.com/pr/7" },
            }),
          ]);
        },
      );
      expect(screen.queryByRole("button", { name: "Merge PR #7" })).not.toBeInTheDocument();
    },
  );

  it("does not offer Merge without a current head SHA", () => {
    renderWithQuery(
      <SessionInspector session={session([pr(7, "open")])} />,
      undefined,
      (client) => {
        client.setQueryData(sessionScmSummaryQueryKey("sess-1"), [
          prSummary(7, "open", {
            headSha: "",
            ci: { autoInjectCI: true, state: "passing", failingChecks: [] },
            review: { decision: "approved", hasUnresolvedHumanComments: false, unresolvedBy: [] },
            mergeability: { state: "mergeable", reasons: [], prUrl: "https://example.com/pr/7" },
          }),
        ]);
      },
    );
    expect(screen.queryByRole("button", { name: "Merge PR #7" })).not.toBeInTheDocument();
  });

  it("uses the state chip as the single merged-state indicator", () => {
    renderWithQuery(
      <SessionInspector
        session={session([pr(7, "merged")], { status: "merged" })}
      />,
    );

    const card = prSection("Pull request")
      .getByText("PR #7")
      .closest("article") as HTMLElement;
    expect(within(card).getByText("merged", { exact: true })).toHaveClass(
      "border-border-strong",
      "bg-overlay",
      "text-success",
    );
    expect(
      within(card).queryByText("Pull request merged"),
    ).not.toBeInTheDocument();
  });

  it("shows the empty state when there are no PRs", () => {
    renderWithQuery(<SessionInspector session={session([])} />);
    expect(screen.getByText("No pull request opened yet.")).toBeInTheDocument();
  });

  it("keeps durable session policies in Summary and operational review controls in Reviews", async () => {
    renderWithQuery(<SessionInspector session={session([pr(7, "open")])} />);

    expect(screen.getByText("Session controls")).toBeInTheDocument();

    const policyRow = (name: string) =>
      screen
        .getByRole("switch", { name })
        .closest("[data-slot='inspector-policy-row']") as HTMLElement;
    const ciRow = policyRow("Automatically fix CI failures");
    const reviewRow = policyRow("Automatically fix review comments");
    const terminateRow = policyRow(
      "Terminate session when pull requests merge",
    );
    const prCard = prSection("Pull request")
      .getByText("PR #7")
      .closest("article") as HTMLElement;
    const appearsBefore = (first: HTMLElement, second: HTMLElement) =>
      Boolean(
        first.compareDocumentPosition(second) &
        Node.DOCUMENT_POSITION_FOLLOWING,
      );

    expect(appearsBefore(prCard, ciRow)).toBe(true);
    expect(ciRow.className).toBe(terminateRow.className);
    expect(reviewRow.className).toBe(terminateRow.className);
    expect(ciRow.parentElement).not.toHaveClass(
      "rounded-lg",
      "border",
      "bg-surface",
    );


    for (const name of [
      "Automatically fix CI failures",
      "Automatically fix review comments",
      "Terminate session when pull requests merge",
    ]) {
      const toggle = screen.getByRole("switch", { name });
      expect(toggle).toHaveClass("h-4", "w-8", "rounded-full");
      expect(toggle.querySelector("[data-slot='switch-thumb']")).toHaveClass(
        "size-3",
        "rounded-full",
      );
    }
    expect(
      screen.getByRole("button", {
        name: "Sends CI failures to the worker for this session's PRs.",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", {
        name: "When disabled, AO keeps this session open after all pull requests merge.",
      }),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByRole("tab", { name: "Reviews" }));
    expect(
      await screen.findByRole("button", { name: "Run review" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("switch", { name: "Automatically fix review comments" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Auto review" })).toBeInTheDocument();

  });

  it("persists the CI injection policy before a PR exists", async () => {
    renderWithQuery(<SessionInspector session={session([])} />);

    const toggle = screen.getByRole("switch", {
      name: "Automatically fix CI failures",
    });
    expect(toggle).toBeChecked();
    await userEvent.click(toggle);

    expect(toggle).not.toBeChecked();
    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/auto-inject-ci",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { autoInjectCI: false },
        },
      ),
    );
  });

  it("restores the CI injection toggle and shows the API error when saving fails", async () => {
    patchMock.mockResolvedValueOnce({
      error: new Error("CI policy unavailable"),
      response: { status: 500 },
    });
    renderWithQuery(<SessionInspector session={session([pr(7, "open")])} />);

    const toggle = screen.getByRole("switch", {
      name: "Automatically fix CI failures",
    });
    await userEvent.click(toggle);

    expect(
      await screen.findByText("CI policy unavailable"),
    ).toBeInTheDocument();
    expect(toggle).toBeChecked();
  });

  it("shows failing checks without an injection notice", () => {
    const failingPR = prSummary(7, "open", {
      ci: {
        autoInjectCI: false,
        state: "failing",
        failingChecks: [
          {
            name: "unit",
            status: "failed",
            conclusion: "failure",
            url: "https://ci.example/unit",
          },
        ],
      },
      mergeability: {
        state: "blocked",
        reasons: ["required checks failing"],
        prUrl: "https://example.com/pr/7",
      },
    });
    renderWithQuery(
      <SessionInspector session={session([pr(7, "open")])} />,
      undefined,
      (client) => {
        client.setQueryData(sessionScmSummaryQueryKey("sess-1"), [failingPR]);
      },
    );

    const card = prSection("Pull request")
      .getByText("PR #7")
      .closest("article") as HTMLElement;
    expect(within(card).getByText("Checks failing")).toBeInTheDocument();
    expect(within(card).queryByText("CI failures not injected")).not.toBeInTheDocument();
  });

  it("links each PR to its url", () => {
    renderWithQuery(
      <SessionInspector session={session([pr(41, "open"), pr(42, "draft")])} />,
    );
    const links = [
      prSection("Pull requests (2)").getByRole("link", { name: "Open PR #41" }),
      prSection("Pull requests (2)").getByRole("link", { name: "Open PR #42" }),
    ];
    expect(links[0]).toHaveClass(
      "text-settings-label",
      "hover:text-settings-label",
    );
    expect(links.map((a) => a.getAttribute("href"))).toEqual([
      "https://example.com/pr/41",
      "https://example.com/pr/42",
    ]);
  });
});

describe("SessionInspector usage", () => {
	const canonicalTotals = {
		inputTokens: 1200,
		cachedInputTokens: 1000,
		uncachedInputTokens: 200,
		outputTokens: 300,
		processedTokens: 1500,
	};

	const tokenTotals = (estimatedCost: unknown) => ({ ...canonicalTotals, estimatedCost });

	function mockUsage(estimatedCost: unknown, harnesses?: unknown[]) {
		const totals = tokenTotals(estimatedCost);
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/usage/sessions/{sessionId}") {
				return {
					data: {
						sessionId: "sess-1",
						incomplete: false,
						totals,
						harnesses: harnesses ?? [
							{
								harness: "codex",
								totals,
								models: [
									{ modelId: "gpt-5.5", totals },
									{ modelId: "gpt-5.5-mini", totals },
								],
							},
						],
					},
					error: undefined,
				};
			}
			return { data: undefined };
		});
	}

	it("shows detailed token statistics only when Developer Mode is enabled", async () => {
		useUiStore.getState().setDeveloperMode(true);
		mockUsage(null);

		renderWithQuery(<SessionInspector session={session([])} />);
		expect(await screen.findByText("Usage & cost")).toBeInTheDocument();
		expect(screen.getByText("Tokens processed")).toBeInTheDocument();
		expect(screen.getByLabelText("1,500 tokens processed")).toBeInTheDocument();
		expect(screen.getByText("Estimated cost").parentElement?.nextElementSibling).toHaveTextContent("Unavailable");
		expect(screen.queryByText("Coming soon")).not.toBeInTheDocument();
		const metrics = screen.getAllByTestId("session-usage-metrics")[0];
		expect(within(metrics).getAllByRole("term").map((term) => term.textContent)).toEqual([
			"Fresh Input", "Cache Reads", "Output", "Cache Hit Rate",
		]);
		expect(within(metrics).getByLabelText("Cache Reads: 1,000 tokens")).toHaveTextContent("1K");
		expect(within(metrics).getByLabelText("83.3% cache hit rate (cache reads / total input)")).toHaveTextContent("83.3%");
		expect(within(metrics).queryByText("Cached Output")).not.toBeInTheDocument();
		expect(screen.queryByText("Cache write tokens")).not.toBeInTheDocument();
		expect(screen.queryByText("Reasoning (included in output)")).not.toBeInTheDocument();
		const agentAttribution = screen.getByText("Codex").parentElement;
		expect(agentAttribution?.querySelector("img")).toBeInTheDocument();
		const agentDisclosure = screen.getByRole("button", { name: "Codex usage details" });
		await userEvent.click(agentDisclosure);
		const details = screen.getByRole("region", { name: "Codex usage peek" });
		expect(within(details).getByRole("button", { name: "GPT 5.5 usage details" })).toBeInTheDocument();
		expect(within(details).getByRole("button", { name: "GPT 5.5 Mini usage details" })).toBeInTheDocument();
		expect(within(details).queryByText("2 models")).not.toBeInTheDocument();
		expect(within(details).queryByText("Processed")).not.toBeInTheDocument();
		expect(within(details).queryByText("Cost")).not.toBeInTheDocument();
	});

	it("shows icon disclosures without repeated metrics when multiple agents contributed", async () => {
		useUiStore.getState().setDeveloperMode(true);
		const totals = { ...canonicalTotals, estimatedCost: null };
		mockUsage(null, [
			{ harness: "codex", totals, models: [{ modelId: "gpt-5.5", totals }] },
			{ harness: "claude-code", totals, models: [{ modelId: "claude-haiku-4-5-20251001", totals }] },
		]);

		renderWithQuery(<SessionInspector session={session([])} />);
		const codexDisclosure = await screen.findByRole("button", { name: "Codex usage details" });
		expect(codexDisclosure.querySelector("img")).toBeInTheDocument();

		await userEvent.click(codexDisclosure);
		const details = screen.getByRole("region", { name: "Codex usage peek" });
		expect(within(details).getByRole("button", { name: "GPT 5.5 usage details" })).toBeInTheDocument();
		expect(within(details).queryByText("1 model")).not.toBeInTheDocument();
		expect(within(details).queryByText("Processed")).not.toBeInTheDocument();
		expect(within(details).queryByText("Cost")).not.toBeInTheDocument();
		expect(within(details).queryByText("Fresh Input")).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Claude usage details" }));
		const claudeDetails = screen.getByRole("region", { name: "Claude usage peek" });
		const haikuDisclosure = within(claudeDetails).getByRole("button", { name: "Haiku 4.5 usage details" });
		expect(within(haikuDisclosure).getByText("Haiku 4.5")).toHaveAttribute(
			"title",
			"claude-haiku-4-5-20251001",
		);
	});

	it("renders complete costs and provider/model attribution", async () => {
		useUiStore.getState().setDeveloperMode(true);
		const completeCost = {
			cachedInputNanos: 100_000_000,
			coverage: "complete",
			inputNanos: 540_000_000,
			outputNanos: 600_000_000,
			providerAttribution: "observed",
			totalNanos: 1_240_000_000,
		};
		mockUsage(completeCost, [
			{
				harness: "claude-code",
				totals: tokenTotals(completeCost),
				models: [
					{
						modelId: "claude-sonnet-4",
						totals: tokenTotals({ ...completeCost, totalNanos: 600_000_000 }),
					},
				],
			},
		]);

		renderWithQuery(<SessionInspector session={session([])} />);

		const section = (await screen.findByText("Usage & cost")).closest(
			"[data-testid='inspector-section']",
		) as HTMLElement;
		// The value carries no coverage qualifier, and the disclosure beside the
		// heading explains the estimate without claiming it is billing.
		expect(within(section).getAllByText("$1.24").length).toBeGreaterThan(0);
		expect(section).not.toHaveTextContent(/[≈≥]\$/);
		// The row already sits under its agent, so the billing provider is not
		// repeated in the model name.
		expect(within(section).getByText("Sonnet 4")).toBeInTheDocument();
		expect(section).not.toHaveTextContent("anthropic ·");

		await userEvent.hover(within(section).getByRole("button", { name: "About estimated cost" }));
		const tooltip = await screen.findByRole("tooltip");
		expect(tooltip).toHaveTextContent(/published API list prices/);
		expect(tooltip).not.toHaveTextContent(/could not be priced/);
	});

	it("explains when the displayed price uses an inferred billing provider", async () => {
		useUiStore.getState().setDeveloperMode(true);
		mockUsage({
			cachedInputNanos: 100_000_000,
			coverage: "complete",
			inputNanos: 540_000_000,
			outputNanos: 600_000_000,
			providerAttribution: "inferred",
			totalNanos: 1_240_000_000,
		});

		renderWithQuery(<SessionInspector session={session([])} />);

		const section = (await screen.findByText("Usage & cost")).closest(
			"[data-testid='inspector-section']",
		) as HTMLElement;
		expect(within(section).getAllByText("$1.24").length).toBeGreaterThan(0);

		await userEvent.hover(within(section).getByRole("button", { name: "About estimated cost" }));
		const tooltip = await screen.findByRole("tooltip");
		expect(tooltip).toHaveTextContent(/Billing provider not confirmed/);
		expect(tooltip).toHaveTextContent(/inferred from the model/);
		expect(tooltip).toHaveTextContent(/Actual charges may differ/);
	});

	it("explains when an aggregate mixes detected and inferred providers", async () => {
		useUiStore.getState().setDeveloperMode(true);
		mockUsage({
			cachedInputNanos: 100_000_000,
			coverage: "complete",
			inputNanos: 540_000_000,
			outputNanos: 600_000_000,
			providerAttribution: "mixed",
			totalNanos: 1_240_000_000,
		});

		renderWithQuery(<SessionInspector session={session([])} />);
		const section = (await screen.findByText("Usage & cost")).closest(
			"[data-testid='inspector-section']",
		) as HTMLElement;
		await userEvent.hover(within(section).getByRole("button", { name: "About estimated cost" }));

		const tooltip = await screen.findByRole("tooltip");
		expect(tooltip).toHaveTextContent(/Some billing providers were detected/);
		expect(tooltip).toHaveTextContent(/others inferred from their models/);
		expect(tooltip).toHaveTextContent(/actual charges may differ/i);
	});

	it("presents a partial total as a plain value and discloses the gap in words", async () => {
		useUiStore.getState().setDeveloperMode(true);
		mockUsage({
			cachedInputNanos: null,
			coverage: "partial",
			inputNanos: 2_000_000,
			outputNanos: 5_000_000,
			providerAttribution: "observed",
			totalNanos: 7_000_000,
		});

		renderWithQuery(<SessionInspector session={session([])} />);

		const section = (await screen.findByText("Usage & cost")).closest(
			"[data-testid='inspector-section']",
		) as HTMLElement;
		expect(within(section).getAllByText("$0.007").length).toBeGreaterThan(0);
		expect(section).not.toHaveTextContent(/[≈≥]\$/);
		expect(section).not.toHaveTextContent(/partial/i);

		await userEvent.hover(within(section).getByRole("button", { name: "About estimated cost" }));
		const tooltip = await screen.findByRole("tooltip");
		expect(tooltip).toHaveTextContent(/Some usage could not be priced/);
	});

	// The column itself carries the "nothing here is priced" case: it disappears
	// when no row has an estimate, so an install without pricing shows no empty
	// column at all. Once any row is priced the column earns its place, and the
	// rows that are not priced say so in words rather than trailing a dash.
	it("drops the cost column only when no agent has an estimate", async () => {
		useUiStore.getState().setDeveloperMode(true);
		const totals = tokenTotals(null);
		mockUsage(null, [
			{ harness: "codex", totals, models: [{ modelId: "gpt-5.5", totals }] },
			{ harness: "claude-code", totals, models: [{ modelId: "claude-sonnet-4", totals }] },
		]);

		renderWithQuery(<SessionInspector session={session([])} />);

		const section = (await screen.findByText("Usage & cost")).closest(
			"[data-testid='inspector-section']",
		) as HTMLElement;
		// The header row's parent is the list container holding every agent row.
		const agentList = within(section).getByText("Agent").parentElement?.parentElement as HTMLElement;
		expect(within(agentList).queryByText("Cost")).not.toBeInTheDocument();
		expect(agentList).not.toHaveTextContent("Unavailable");
	});

	it("keeps the cost column and marks unpriced agents unavailable", async () => {
		useUiStore.getState().setDeveloperMode(true);
		const priced = tokenTotals({
			cachedInputNanos: 100_000_000,
			coverage: "complete",
			inputNanos: 540_000_000,
			outputNanos: 600_000_000,
			providerAttribution: "observed",
			totalNanos: 1_240_000_000,
		});
		const unpriced = tokenTotals(null);
		mockUsage(null, [
			{ harness: "codex", totals: priced, models: [{ modelId: "gpt-5.5", totals: priced }] },
			{
				harness: "claude-code",
				totals: unpriced,
				models: [{ modelId: "claude-sonnet-4", totals: unpriced }],
			},
		]);

		renderWithQuery(<SessionInspector session={session([])} />);

		const section = (await screen.findByText("Usage & cost")).closest(
			"[data-testid='inspector-section']",
		) as HTMLElement;
		// The header row's parent is the list container holding every agent row.
		const agentList = within(section).getByText("Agent").parentElement?.parentElement as HTMLElement;
		expect(within(agentList).getByText("Cost")).toBeInTheDocument();
		expect(within(agentList).getByText("$1.24")).toBeInTheDocument();
		expect(within(agentList).getByText("Unavailable")).toBeInTheDocument();
		expect(within(agentList).queryByText("—")).not.toBeInTheDocument();
	});

	it("shows an unavailable estimate as words rather than a dash", async () => {
		useUiStore.getState().setDeveloperMode(true);
		mockUsage(null);

		renderWithQuery(<SessionInspector session={session([])} />);

		const section = (await screen.findByText("Usage & cost")).closest(
			"[data-testid='inspector-section']",
		) as HTMLElement;
		expect(within(section).getAllByText("Unavailable").length).toBeGreaterThan(0);
	});
});

describe("SessionInspector completion controls", () => {
  it("persists the terminate-on-merge preference", async () => {
    renderWithQuery(<SessionInspector session={session([])} />);

    await userEvent.click(
      screen.getByRole("switch", {
        name: "Terminate session when pull requests merge",
      }),
    );

    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/merge-policy",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { terminateOnPrMerge: true },
        },
      ),
    );
  });

  it("terminates a live merged session and returns to its orchestrator immediately", async () => {
    postMock.mockReturnValue(new Promise(() => {}));
    const worker = session([pr(7, "merged")], { status: "merged" });
    const orchestrator = session([], {
      id: "orch-1",
      kind: "orchestrator",
      title: "orchestrator",
    });
    renderWithQuery(<SessionInspector session={worker} />, [
      {
        id: "ws-1",
        name: "my-app",
        path: "/repo",
        sessions: [worker, orchestrator],
      },
    ]);

    expect(
      screen.queryByRole("switch", {
        name: "Terminate session when pull requests merge",
      }),
    ).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "Terminate session" }),
    );
    expect(
      screen.getByRole("dialog", { name: "Terminate do the thing?" }),
    ).toBeInTheDocument();
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Yes, terminate session",
      }),
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
    postMock.mockResolvedValueOnce({
      error: new Error("runtime teardown failed"),
      response: { status: 500 },
    });
    renderWithQuery(
      <SessionInspector
        session={session([pr(7, "merged")], { status: "merged" })}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: "Terminate session" }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Yes, terminate session",
      }),
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
      <SessionInspector
        session={session([pr(7, "merged")], {
          status: "merged",
          isTerminated: true,
        })}
      />,
    );

    expect(screen.queryByText("Completion")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Terminate session" }),
    ).not.toBeInTheDocument();
  });

  it("does not show completion controls for orchestrator sessions", () => {
    renderWithQuery(
      <SessionInspector session={session([], { kind: "orchestrator" })} />,
    );

    expect(screen.queryByText("Completion")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("switch", {
        name: "Terminate session when pull requests merge",
      }),
    ).not.toBeInTheDocument();
  });
});

describe("SessionInspector Activity section", () => {
  const activitySection = () =>
    within(
      screen
        .getByText("Activity")
        .closest("[data-testid='inspector-section']") as HTMLElement,
    );

  it("offers a managed resume only for an exited, nonterminated agent", async () => {
    renderWithQuery(
      <SessionInspector
        session={session([], {
          status: "exited",
          activity: { state: "exited", lastActivityAt: "2026-06-15T10:00:00Z" },
        })}
      />,
    );

    await userEvent.click(
      activitySection().getByRole("button", { name: "Resume agent" }),
    );

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/resume-agent",
        {
          params: { path: { sessionId: "sess-1" } },
        },
      ),
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

    expect(
      screen.queryByRole("button", { name: "Resume agent" }),
    ).not.toBeInTheDocument();

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
    expect(
      screen.queryByRole("button", { name: "Resume agent" }),
    ).not.toBeInTheDocument();
  });

  it("does not offer agent resume while an agent switch owns the exited source", () => {
    renderWithQuery(
      <SessionInspector
        session={session([], {
          status: "exited",
          activity: { state: "exited", lastActivityAt: "2026-06-15T10:00:00Z" },
          activeAgentSwitch: {
            id: "switch-1",
            fromHarness: "claude-code",
            targetHarness: "codex",
            state: "source_stopped",
            agentHandoffStatus: "received",
          },
        })}
      />,
    );

    expect(
      screen.queryByRole("button", { name: "Resume agent" }),
    ).not.toBeInTheDocument();
  });

  it("keeps resume failures visible beside the action", async () => {
    postMock.mockResolvedValueOnce({
      error: new Error("agent restart failed"),
      response: { status: 500 },
    });
    renderWithQuery(
      <SessionInspector
        session={session([], {
          status: "exited",
          activity: { state: "exited", lastActivityAt: "2026-06-15T10:00:00Z" },
        })}
      />,
    );

    await userEvent.click(
      activitySection().getByRole("button", { name: "Resume agent" }),
    );

    expect(
      await activitySection().findByText("agent restart failed"),
    ).toBeInTheDocument();
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
          activity: {
            state: "unknown",
            lastActivityAt: "2026-06-15T10:00:00Z",
          },
        })}
      />,
    );

    expect(activitySection().getByText("Unknown")).toBeInTheDocument();
    expect(
      activitySection().queryByText("Activity Unavailable"),
    ).not.toBeInTheDocument();
  });

  it("falls back to unknown when no activity has been reported", () => {
    renderWithQuery(
      <SessionInspector session={session([], { status: "working" })} />,
    );

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
    expect(
      activitySection().queryByText("Input Needed"),
    ).not.toBeInTheDocument();
  });

  it.each([
    ["ci_failed", "CI Failed"],
    ["changes_requested", "Changes Requested"],
  ] as const)(
    "renders %s as an SCM state in the current Activity row",
    (status, label) => {
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
    },
  );

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

  it("timestamps the live Activity state so it participates in chronological ordering", () => {
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
    expect(within(activityRow).getByText("2h ago")).toBeInTheDocument();
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
    const workspaceMarker = workspaceRow.querySelector(
      "span[aria-hidden='true'].rounded-full",
    ) as HTMLElement;
    expect(workspaceMarker.parentElement).toHaveClass(
      "relative",
      "flex",
      "items-center",
    );
    expect(workspaceMarker).toHaveClass("top-1.5");
    expect(workspaceMarker).not.toHaveClass("top-1/2", "-translate-y-1/2");

    const activityRow = activitySection()
      .getByText("Idle")
      .closest("[data-testid='inspector-timeline-event']") as HTMLElement;
    const activityMarker = activityRow.querySelector(
      "span[aria-hidden='true'].rounded-full",
    ) as HTMLElement;
    expect(activityMarker.parentElement).toHaveClass(
      "relative",
      "flex",
      "items-center",
    );
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
    const marker = activityRow.querySelector(
      "span[aria-hidden='true'].rounded-full",
    ) as HTMLElement;
    expect(marker).toHaveClass("animate-status-pulse");
    expect(
      within(activityRow).getByText("Working").querySelector(".rounded-full"),
    ).not.toBeInTheDocument();
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

    for (const title of ["Pull request", "Session controls", "Activity"]) {
      const heading = screen.getByText(title).parentElement;
      expect(heading?.parentElement).toHaveAttribute(
        "data-testid",
        "inspector-section",
      );
    }
  });

  it("keeps workspace, PR, and SCM context rows in the Activity timeline", () => {
    renderWithQuery(
      <SessionInspector
        session={session(
          [pr(7, "open", { ci: "failing", review: "changes_requested" })],
          {
            status: "ci_failed",
            activity: { state: "idle", lastActivityAt: "2026-06-15T10:00:00Z" },
          },
        )}
      />,
    );

    expect(
      activitySection().getByText(/Created workspace/),
    ).toBeInTheDocument();
    expect(activitySection().getByText("Opened")).toBeInTheDocument();
    expect(activitySection().getByText("PR #7")).toBeInTheDocument();
    const activityRow = activitySection()
      .getByText("Idle")
      .closest("[data-testid='inspector-timeline-event']") as HTMLElement;
    expect(within(activityRow).getByText("CI Failed")).toBeInTheDocument();
    expect(
      within(activityRow).getByText("Changes Requested"),
    ).toBeInTheDocument();
  });

  it("links and timestamps draft, opened, and merged PR milestones from backend lifecycle times", async () => {
    const minutesAgo = (minutes: number) =>
      new Date(Date.now() - minutes * 60 * 1000).toISOString();
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
        return {
          data: { sessionId: "sess-1", prs: summaries },
          error: undefined,
        };
      }
      return { data: { reviewerHandleId: "", reviews: [] }, error: undefined };
    });

    renderWithQuery(
      <SessionInspector
        session={session(
          [
            pr(8, "draft", {
              url: `https://api.github.com/repos/acme/repo/pulls/8`,
            }),
            pr(7, "open", {
              url: `https://api.github.com/repos/acme/repo/pulls/7`,
            }),
            pr(6, "merged", {
              url: `https://api.github.com/repos/acme/repo/pulls/6`,
            }),
          ],
          {
            status: "merged",
            activity: { state: "idle", lastActivityAt: "2026-06-15T11:50:00Z" },
          },
        )}
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
      within(
        draftLink.closest(
          "[data-testid='inspector-timeline-event']",
        ) as HTMLElement,
      ).getByText("2h ago"),
    ).toBeInTheDocument();

    const openLink = screen.getByRole("link", { name: "Opened PR #7" });
    expect(
      within(
        openLink.closest(
          "[data-testid='inspector-timeline-event']",
        ) as HTMLElement,
      ).getByText("1h ago"),
    ).toBeInTheDocument();

    const mergedOpenedLink = screen.getByRole("link", { name: "Opened PR #6" });
    expect(
      within(
        mergedOpenedLink.closest(
          "[data-testid='inspector-timeline-event']",
        ) as HTMLElement,
      ).getByText("3h ago"),
    ).toBeInTheDocument();

    const mergedLink = screen.getByRole("link", { name: "Merged PR #6" });
    expect(
      within(
        mergedLink.closest(
          "[data-testid='inspector-timeline-event']",
        ) as HTMLElement,
      ).getByText("30m ago"),
    ).toBeInTheDocument();
    const doneRow = screen
      .getByText("Done")
      .closest("[data-testid='inspector-timeline-event']") as HTMLElement;
    expect(within(doneRow).getByText("30m ago")).toBeInTheDocument();
  });

  it("orders Activity timeline rows by timestamp with the latest event on top", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-06-15T12:00:00Z"));
    const summaries = [
      prSummary(42, "draft", {
        createdAt: "2026-06-15T08:00:00Z",
        stateChangedAt: "2026-06-15T08:00:00Z",
      }),
      prSummary(41, "open", {
        createdAt: "2026-06-15T11:30:00Z",
        stateChangedAt: "2026-06-15T11:30:00Z",
      }),
      prSummary(40, "merged", {
        createdAt: "2026-06-15T09:15:00Z",
        stateChangedAt: "2026-06-15T10:45:00Z",
      }),
    ];

    renderWithQuery(
      <SessionInspector
        session={session(
          [
            pr(42, "draft", {
              url: `https://api.github.com/repos/acme/repo/pulls/42`,
            }),
            pr(41, "open", {
              url: `https://api.github.com/repos/acme/repo/pulls/41`,
            }),
            pr(40, "merged", {
              url: `https://api.github.com/repos/acme/repo/pulls/40`,
            }),
          ],
          {
            status: "merged",
            createdAt: "2026-06-15T09:00:00Z",
            updatedAt: "2026-06-15T11:55:00Z",
            activity: { state: "idle", lastActivityAt: "2026-06-15T10:00:00Z" },
          },
        )}
      />,
      undefined,
      (client) =>
        client.setQueryData(sessionScmSummaryQueryKey("sess-1"), summaries),
    );

    const section = screen
      .getByText("Activity")
      .closest("[data-testid='inspector-section']") as HTMLElement;
    const rows = Array.from(
      section.querySelectorAll("[data-testid='inspector-timeline-event']"),
      (row) => row.textContent?.replace(/\s+/g, " ").trim(),
    );
    expect(rows).toEqual([
      "Opened PR #4130m ago",
      "Merged PR #401h ago",
      "Done1h ago",
      "Idle2h ago",
      "Opened PR #402h ago",
      "Created workspace3h ago",
      "Draft PR #424h ago",
    ]);

    const eventRows = section.querySelectorAll(
      "[data-testid='inspector-timeline-event']",
    );
    expect(
      section.querySelectorAll("[data-testid='inspector-timeline-connector']"),
    ).toHaveLength(eventRows.length - 1);
    expect(
      within(eventRows[eventRows.length - 1] as HTMLElement).queryByTestId(
        "inspector-timeline-connector",
      ),
    ).not.toBeInTheDocument();
  });
});

describe("SessionInspector tabs", () => {
  it("exposes Reviews after Summary and keeps review content out of Summary", async () => {
    mockCommonGets([], "", [reviewState(1, "needs_review")]);
    renderWithQuery(<SessionInspector session={session([pr(1, "open")])} />);
    const tabs = screen.getAllByRole("tab").map((el) => el.textContent?.trim());
    expect(tabs).toEqual(["Summary", "Reviews", "Browser", "Files"]);
    expect(screen.queryByText("Review controls")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("tab", { name: "Reviews" }));

    expect(await screen.findByText("Review controls")).toBeInTheDocument();
    expect(screen.queryByText("Pull request")).not.toBeInTheDocument();
  });

  it("keeps the Reviews tab available for draft PRs", async () => {
    mockCommonGets([], "", [reviewState(1, "needs_review")]);
    renderWithQuery(<SessionInspector session={session([pr(1, "draft")])} />);

    await userEvent.click(screen.getByRole("tab", { name: "Reviews" }));

    expect(await screen.findByText("Review controls")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Review latest commit" })).not.toBeDisabled();
  });

  it("hides the Reviews tab when every PR is merged or closed", async () => {
    mockCommonGets([], "", [reviewState(1, "up_to_date"), reviewState(2, "up_to_date")]);
    renderWithQuery(
      <SessionInspector
        session={session([pr(1, "merged"), pr(2, "closed")])}
        view="reviews"
      />,
    );

    expect(screen.queryByRole("tab", { name: "Reviews" })).not.toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Summary" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.queryByText("Review controls")).not.toBeInTheDocument();
    expect(screen.queryByText("View review details")).not.toBeInTheDocument();
  });

  it("does not render the overview card in the summary", () => {
    renderWithQuery(
      <SessionInspector
        session={{ ...session([]), issueId: "github:acme/project-one#42" }}
      />,
    );

    expect(screen.queryByText("Overview")).not.toBeInTheDocument();
    expect(screen.queryByText("Issue")).not.toBeInTheDocument();
    expect(
      screen.queryByText("github:acme/project-one#42"),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("Branch")).not.toBeInTheDocument();
  });
});

describe("SessionInspector summary reviews", () => {
  // Review rows start collapsed. Open the Reviews tab and reveal every row,
  // since these tests are about what a review says.
  const openReviewsSection = async () => {
    await userEvent.click(screen.getByRole("tab", { name: "Reviews" }));
    // Rows arrive with the reviews query, so wait for them before expanding.
    const rows = await screen.findAllByTestId("review-pr-row").catch(() => []);
    for (const row of rows) {
      if (row.getAttribute("aria-expanded") === "false")
        await userEvent.click(row);
    }
  };

  it("triggers a review and opens the returned reviewer terminal", async () => {
    mockCommonGets([], "", [reviewState(3, "needs_review")]);
    const runningReview = {
      ...approvedReview,
      status: "running",
      verdict: "",
      body: "",
    };
    postMock.mockResolvedValue({
      response: { status: 201 },
      data: {
        reviewerHandleId: "reviewer-pane",
        reviews: [{ ...reviewState(3, "running"), latestRun: runningReview }],
      },
    });
    const onOpenReviewerTerminal = vi.fn();

    renderWithQuery(
      <SessionInspector
        onOpenReviewerTerminal={onOpenReviewerTerminal}
        session={session([pr(3, "open")])}
      />,
    );
    await openReviewsSection();

    await userEvent.click(
      await screen.findByRole("button", { name: "Review latest commit" }),
    );

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/trigger",
        {
          params: { path: { sessionId: "sess-1" } },
        },
      ),
    );
    expect(onOpenReviewerTerminal).toHaveBeenCalledWith({
      handleId: "reviewer-pane",
      harness: "codex",
    });
  });

  it("shows the worker-compatible default reviewer before a run exists", async () => {
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

    renderWithQuery(
      <SessionInspector
        session={sessionWithProvider([pr(3, "open")], "codex")}
      />,
    );
    await openReviewsSection();

    expect(
      await screen.findByRole("button", { name: /Select reviewer agent/ }),
    ).toHaveTextContent("Codex");
    expect(screen.queryByText("reviewer")).not.toBeInTheDocument();
  });

  // The label is a display name, not the wire id: the trigger used to print the
  // raw harness id, which read as a second, selectable "claude-code" entry
  // alongside the catalog's properly-cased "Claude Code".
  it("labels the default reviewer with its display name, not the raw id", async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/agents/readiness") {
        const agents = ["claude-code", "codex", "opencode"].map((id) => agentReadiness(id));
        return { data: { agents } };
      }
      if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
        return {
          data: { sessionId: "sess-1", files: [], truncated: false },
          error: undefined,
        };
      }
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

    renderWithQuery(
      <SessionInspector
        session={sessionWithProvider([pr(3, "open")], "claude-code")}
      />,
    );
    await openReviewsSection();

    const trigger = await screen.findByRole("button", {
      name: /Select reviewer agent/,
    });
    expect(trigger).toHaveTextContent("Claude Code");
    expect(trigger).not.toHaveTextContent("claude-code");
  });

  it("configures session auto-review and disables manual controls", async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/agents/readiness") {
        const agents = ["claude-code", "codex", "opencode"].map((id) => agentReadiness(id));
        return { data: { agents } };
      }
      if (path === "/api/v1/sessions/{sessionId}/reviews") {
        return {
          data: {
            reviewerHandleId: "",
            reviews: [reviewState(3, "needs_review")],
          },
        };
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

    renderWithQuery(
      <SessionInspector
        session={session([pr(3, "open")], { autoReviewEnabled: true })}
      />,
    );
    await openReviewsSection();

    expect(
      screen.getByRole("button", { name: "Review latest commit" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Select reviewer agent" }),
    ).toBeDisabled();
    const toggle = screen.getByRole("switch", { name: "Auto review" });
    expect(toggle).toBeChecked();
    await userEvent.click(toggle);
    await waitFor(() =>
      expect(putMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/auto-review",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { enabled: false },
        },
      ),
    );
    expect(
      screen.queryByRole("button", { name: "Re-run review" }),
    ).not.toBeInTheDocument();
  });

  it("enables auto-review for the current session", async () => {
    mockCommonGets([], "", [reviewState(3, "needs_review")]);
    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    const toggle = screen.getByRole("switch", { name: "Auto review" });
    expect(toggle).not.toBeChecked();
    await userEvent.click(toggle);
    await waitFor(() =>
      expect(putMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/auto-review",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { enabled: true },
        },
      ),
    );
  });

  it("shows reviewing status and cancel action while auto-review is running", async () => {
    const runningReview = {
      ...approvedReview,
      status: "running",
      verdict: "",
      body: "",
    };
    getMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/agents/readiness") {
        const agents = ["claude-code", "codex", "opencode"].map((id) => agentReadiness(id));
        return { data: { agents } };
      }
      if (path === "/api/v1/sessions/{sessionId}/reviews") {
        return {
          data: {
            reviewerHandleId: "reviewer-pane",
            reviews: [
              { ...reviewState(3, "running"), latestRun: runningReview },
            ],
          },
        };
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

    renderWithQuery(
      <SessionInspector
        session={session([pr(3, "open")], { autoReviewEnabled: true })}
      />,
    );
    await openReviewsSection();

    expect(
      screen.getByRole("button", { name: "Stop review" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Kill review session" }),
    ).toBeDisabled();
    expect(
      screen.queryByRole("button", { name: "Re-run review" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Run review" }),
    ).not.toBeInTheDocument();
  });

  it("hides review summary sections when no review data exists", async () => {
    mockCommonGets([], "", []);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    expect(
      await screen.findByRole("button", { name: "Run review" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("AO code reviews")).not.toBeInTheDocument();
    expect(
      screen.queryByText("Reviews on the pull request"),
    ).not.toBeInTheDocument();
  });

  it("hides AO code reviews until a review run has been triggered", async () => {
    mockCommonGets([], "", [reviewState(3, "needs_review", "abc123")]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    expect(
      await screen.findByRole("button", { name: "Review latest commit" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("AO code reviews")).not.toBeInTheDocument();
    expect(screen.queryByText("Reviewable change 3")).not.toBeInTheDocument();
  });

  it("shows AO code reviews for verdict-only review states", async () => {
    mockCommonGets([], "reviewer-pane", [
      reviewState(3, "changes_requested", "abc123"),
    ]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    expect(await screen.findByText("Reviewable change 3")).toBeInTheDocument();
    expect(screen.getByText("Changes requested")).toBeInTheDocument();
  });

  it("hides agent review PR rows while a triggered review is still running without a verdict", async () => {
    const running = {
      ...reviewState(3, "running", "sha-1"),
      latestRun: {
        ...approvedReview,
        id: "run-live",
        harness: "codex",
        status: "running",
        verdict: "",
      },
    };
    mockCommonGets([], "reviewer-pane", [running]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();
    await screen.findByText("Review in progress · Codex");

    expect(screen.queryByText("Reviewable change 3")).not.toBeInTheDocument();
    expect(screen.queryByText("Review summary")).not.toBeInTheDocument();
  });

  it("shows eligible and up-to-date open PR review rows", async () => {
    mockCommonGets([approvedReview], "reviewer-pane", [
      reviewState(3, "needs_review", "abc123"),
      reviewState(4, "up_to_date", "def456"),
      reviewState(5, "ineligible", "ghi789"),
    ]);

    renderWithQuery(
      <SessionInspector
        session={session([pr(3, "open"), pr(4, "open"), pr(5, "draft")])}
      />,
    );
    await openReviewsSection();

    expect(
      screen.getByRole("button", { name: /Select reviewer agent/ }),
    ).toHaveTextContent("Codex");
    expect(screen.queryByText("Reviewable change 3")).not.toBeInTheDocument();
    expect(await screen.findByText("Reviewable change 4")).toBeInTheDocument();
    expect(
      within(
        screen
          .getByText("Reviewable change 4")
          .closest("[data-testid='review-pr-row']") as HTMLElement,
      ).getAllByText("Approved"),
    ).not.toHaveLength(0);
    expect(screen.queryByText("Reviewable change 5")).not.toBeInTheDocument();
    expect(screen.getAllByText("Approved")).not.toHaveLength(0);
    expect(
      screen.getByRole("button", { name: "Review latest commit" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Open terminal" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Run" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Re-run" }),
    ).not.toBeInTheDocument();
  });

  // A review body is a multi-paragraph write-up. Rendered whole it buries the
  // verdict and every earlier pass below it, which is the opposite of reading
  // the history in one place.
  it("clamps a long review summary and expands it in place", async () => {
    const longBody = Array.from(
      { length: 12 },
      (_, i) => `Finding ${i + 1}: something worth reading.`,
    ).join("\n");
    mockCommonGets([], "reviewer-pane", [
      {
        ...reviewState(3, "up_to_date", "abc123"),
        latestRun: { ...approvedReview, body: longBody },
      },
    ]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    const summary = await screen.findByTestId("review-run-summary");
    expect(summary).toHaveClass("line-clamp-4");
    setRenderedOverflow(summary, true);

    await userEvent.click(await screen.findByRole("button", { name: "Show more" }));
    expect(screen.getByTestId("review-run-summary")).not.toHaveClass(
      "line-clamp-4",
    );

    await userEvent.click(screen.getByRole("button", { name: "Show less" }));
    expect(screen.getByTestId("review-run-summary")).toHaveClass(
      "line-clamp-4",
    );
  });

  // Nothing to hide, so offering to expand would be noise.
  it("does not offer to expand a short review summary", async () => {
    mockCommonGets([], "reviewer-pane", [
      {
        ...reviewState(3, "up_to_date", "abc123"),
        latestRun: { ...approvedReview, body: "Looks good." },
      },
    ]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    const summary = await screen.findByTestId("review-run-summary");
    setRenderedOverflow(summary, false);
    expect(summary).toHaveClass("line-clamp-4");
    expect(
      screen.queryByRole("button", { name: "Show more" }),
    ).not.toBeInTheDocument();
  });

  it("renders AO review summaries as Markdown", async () => {
    mockCommonGets([], "reviewer-pane", [
      {
        ...reviewState(3, "up_to_date", "abc123"),
        latestRun: {
          ...approvedReview,
          body: "Fix **auth validation**.\n\n- Add tests",
        },
      },
    ]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    const summary = await screen.findByTestId("review-run-summary");
    expect(within(summary).getByText("auth validation").tagName).toBe("STRONG");
    expect(within(summary).getByText("Add tests").tagName).toBe("LI");
    expect(summary).not.toHaveTextContent("**auth validation**");
  });

  it("does not show a View on PR CTA for review summaries", async () => {
    mockCommonGets([], "reviewer-pane", [
      {
        ...reviewState(3, "up_to_date", "abc123"),
        latestRun: { ...approvedReview, githubReviewId: "98765" },
      },
    ]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    expect(await screen.findByTestId("review-run-summary")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /View on PR/ })).not.toBeInTheDocument();
  });

  it("opens an AO review in Browser and sends its summary to the worker", async () => {
    const reviewUrl = "https://github.com/acme/repo/pull/3#pullrequestreview-98765";
    mockCommonGets([], "reviewer-pane", [
      {
        ...reviewState(3, "up_to_date", "abc123"),
        latestRun: {
          ...approvedReview,
          body: "Please tighten validation and add a regression test.",
          githubReviewId: "98765",
          prUrl: "https://github.com/acme/repo/pull/3",
        },
      },
    ]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    await userEvent.click(await screen.findByRole("button", { name: "Review actions" }));
    await userEvent.click(screen.getByRole("button", { name: "Open in AO Browser" }));

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/preview", {
        params: { path: { sessionId: "sess-1" } },
        body: { url: reviewUrl },
      }),
    );
    expect(useUiStore.getState().inspectorSessions["sess-1"]?.view).toBe("browser");
    expect(useUiStore.getState().inspectorSessions["sess-1"]?.isOpen).toBe(true);

    await userEvent.click(screen.getByRole("button", { name: "Send to worker agent" }));
    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", {
        params: { path: { sessionId: "sess-1" } },
        body: {
          message: expect.stringContaining("Review summary:\nPlease tighten validation and add a regression test."),
        },
      }),
    );
    expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", {
      params: { path: { sessionId: "sess-1" } },
      body: { message: expect.stringContaining(`Review URL: ${reviewUrl}`) },
    });
  });

  it("shows inline comments on their exact AO review pass without duplicating them externally", async () => {
    const onOpenReviewFile = vi.fn();
    const currentRun = {
      ...approvedReview,
      body: "Current AO review.",
      githubReviewId: "111",
      prUrl: "https://example.com/pr/3",
    };
    const previousRun = {
      ...approvedReview,
      id: "run-previous",
      body: "Earlier AO review.",
      githubReviewId: "222",
      prUrl: "https://example.com/pr/3",
      createdAt: "2026-06-15T10:06:00Z",
    };
    mockCommonGets([], "reviewer-pane", [{
      ...reviewState(3, "up_to_date", "abc123"),
      latestRun: currentRun,
      previousRun,
    }]);
    const previousGet = getMock.getMockImplementation()!;
    getMock.mockImplementation(async (path: string, opts?: unknown) => {
      if (path === "/api/v1/sessions/{sessionId}/pr") {
        return {
          data: {
            prs: [prSummary(3, "open", {
              author: "codebanditssss",
              review: {
                decision: "changes_requested",
                hasUnresolvedHumanComments: true,
                reviews: [],
                unresolvedBy: [{
                  reviewerId: "codebanditssss",
                  count: 2,
                  links: [
                    { reviewId: "111", body: "Current-pass comment.", file: "src/current.ts", line: 11, url: "https://example.com/current", autoInjectReview: false },
                    { reviewId: "222", body: "Earlier-pass comment.", file: "src/earlier.ts", line: 22, url: "https://example.com/earlier", autoInjectReview: true },
                  ],
                }, {
                  reviewerId: "maya",
                  count: 1,
                  links: [
                    { body: "Legacy external comment.", file: "src/legacy.ts", line: 33, url: "https://example.com/legacy", autoInjectReview: true },
                  ],
                }],
                resolvedBy: [{
                  reviewerId: "codebanditssss",
                  count: 1,
                  links: [{ reviewId: "111", body: "Resolved current-pass comment.", file: "src/current.ts", line: 9, url: "https://example.com/resolved", autoInjectReview: true }],
                }],
              },
            })],
          },
        };
      }
      return previousGet(path, opts);
    });

    renderWithQuery(
      <SessionInspector
        onOpenReviewFile={onOpenReviewFile}
        session={session([pr(3, "open")])}
      />,
    );
    await openReviewsSection();

    expect(await screen.findByText("Current-pass comment.")).toBeInTheDocument();
    expect(screen.queryByText("Earlier-pass comment.")).not.toBeInTheDocument();
    expect(screen.getByText("Resolved comments · 1")).toBeInTheDocument();

    await userEvent.click(screen.getAllByRole("button", { name: "Comment actions" })[0]!);
    await userEvent.click(screen.getByRole("button", { name: "Send to worker agent" }));
    expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", expect.objectContaining({
      params: { path: { sessionId: "sess-1" } },
    }));
    await userEvent.click(screen.getAllByRole("button", { name: "Comment actions" })[0]!);
    await userEvent.click(screen.getByRole("button", { name: "View in file" }));
    expect(onOpenReviewFile).toHaveBeenCalledWith({ path: "src/current.ts", line: 11 });
    await userEvent.click(screen.getByRole("button", { name: "Resolve comment" }));
    await waitFor(() => expect(postMock).toHaveBeenCalledWith(
      "/api/v1/sessions/{sessionId}/reviews/comments/resolve",
      expect.objectContaining({ params: { path: { sessionId: "sess-1" } } }),
    ));

    await userEvent.click(screen.getByRole("button", { name: /Load more.*1 earlier/i }));
    expect(screen.getByText("Earlier-pass comment.")).toBeInTheDocument();
    expect(screen.getAllByText("Current-pass comment.")).toHaveLength(1);

    await userEvent.click(screen.getByRole("button", { name: /maya.*Commented/i }));
    expect(screen.getByText("Legacy external comment.")).toBeInTheDocument();
    expect(screen.getAllByText("Current-pass comment.")).toHaveLength(1);
    expect(screen.getAllByText("Earlier-pass comment.")).toHaveLength(1);
  });

  it("passes an inline review location to the Files diff viewer", async () => {
    const onOpenReviewFile = vi.fn();
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
                  reviews: [],
                  unresolvedBy: [{
                    reviewerId: "maya",
                    count: 1,
                    links: [{
                      body: "Guard this optional value.",
                      file: "src/panel.tsx",
                      line: 42,
                      url: "https://example.com/comment-42",
                      autoInjectReview: false,
                    }],
                  }],
                },
              }),
            ],
          },
        };
      }
      return previous(path, opts);
    });

    renderWithQuery(
      <SessionInspector
        onOpenReviewFile={onOpenReviewFile}
        session={session([pr(3, "open")])}
      />,
    );
    await openReviewsSection();
    await userEvent.click(await screen.findByRole("button", { name: /maya.*Commented/i }));
    await userEvent.click(screen.getByRole("button", { name: "Comment actions" }));
    await userEvent.click(screen.getByRole("button", { name: "View in file" }));

    expect(onOpenReviewFile).toHaveBeenCalledWith({ path: "src/panel.tsx", line: 42 });
  });

  it.each([
    [
      "needs_review",
      "changes_requested",
      "Review needed",
      "Review latest commit",
    ],
    ["cancelled", "approved", "Review needed", "Review latest commit"],
    ["running", "approved", "Reviewing...", "Stop review"],
  ] as const)(
    "keeps the current AO review state clear while the current head is %s",
    async (status, previousVerdict, runLabel, actionLabel) => {
      const current = {
        ...reviewState(
          3,
          status === "cancelled" ? "needs_review" : status,
          "sha-current",
        ),
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
      if (status === "running" || status === "cancelled") {
        current.latestRun = {
          ...approvedReview,
          id: "run-current",
          status,
          verdict: "",
          targetSha: "sha-current",
        };
      }
      mockCommonGets([], "reviewer-pane", [current]);

      renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
      await openReviewsSection();

      expect(await screen.findAllByText(runLabel)).not.toHaveLength(0);
      expect(
        screen.getByText("Previous review summary with actionable detail."),
      ).toBeInTheDocument();
      expect(screen.queryByText(/Previous:/)).not.toBeInTheDocument();
      if (status === "needs_review" || status === "cancelled") {
        expect(
          screen.getByText(
            status === "cancelled" ? "Approved" : "Changes requested",
          ),
        ).toBeInTheDocument();
        expect(screen.getByText("Earlier commit")).toBeInTheDocument();
        expect(
          screen.getByText("#3 · Latest commit has not been reviewed"),
        ).toBeInTheDocument();
      } else {
        expect(screen.queryByText("Changes requested")).not.toBeInTheDocument();
        expect(screen.queryByText("Earlier commit")).not.toBeInTheDocument();
      }
      expect(screen.queryByRole("link", { name: "View on PR" })).not.toBeInTheDocument();
      // A run in flight gets its own live strip naming the harness, not just a
      // word on the button.
      if (status === "running") {
        expect(
          screen.getByText("Review in progress · Codex"),
        ).toBeInTheDocument();
      } else {
        expect(
          screen.queryByText(/is reviewing this change/),
        ).not.toBeInTheDocument();
      }
      expect(
        screen.getByRole("button", { name: actionLabel }),
      ).toBeInTheDocument();
    },
  );

  it("shows PRs with unresolved comments but no decisive review", async () => {
    mockCommonGets([], "reviewer-pane", [
      reviewState(3, "up_to_date", "sha-1"),
    ]);
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
                author: "ada",
                ci: {
                  state: "passing",
                  failingChecks: [],
                  prUrl: "https://example.com/pr/3",
                },
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

    expect(
      await screen.findByRole("button", { name: "Re-run review" }),
    ).toBeInTheDocument();
    expect(
      (await screen.findAllByText("Reviewable change 3")).length,
    ).toBeGreaterThan(0);
    expect(screen.getAllByText(/2 unresolved comments/).length).toBeGreaterThanOrEqual(
      1,
    );
    expect(
      screen.queryByTestId("github-inline-comments"),
    ).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: /maya.*Commented/i }),
    );
    const comments = screen.getByTestId("github-inline-comments");
    expect(comments).toHaveTextContent("Open comments · 2");
    expect(comments).not.toHaveTextContent("maya");
    expect(
      within(comments).getAllByRole("status", { name: "Sent to worker agent" }),
    ).toHaveLength(2);
    expect(comments).toHaveTextContent("a.ts:3");
    expect(comments).toHaveTextContent("a.ts:9");
    // AO's runs and the PR's own reviews share one section keyed by PR, so the
    // unresolved count rides the same row as the AO verdict.
    expect(screen.getByText("Review summary")).toBeInTheDocument();
    expect(
      screen.queryByText("Reviews on the pull request"),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("AO code reviews")).not.toBeInTheDocument();
    expect(
      screen.queryByText("No unresolved threads."),
    ).not.toBeInTheDocument();
  });

  it("renders PR review summaries as Markdown", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(new Date("2026-06-19T11:00:00Z"));
    mockCommonGets([], "reviewer-pane", [
      reviewState(3, "up_to_date", "sha-1"),
    ]);
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
                author: "ada",
                ci: {
                  state: "passing",
                  failingChecks: [],
                  prUrl: "https://example.com/pr/3",
                },
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
                      reviewUrl:
                        "https://example.com/pr/3#pullrequestreview-456",
                      autoInjectReview: true,
                    },
                    {
                      reviewerId: "ada",
                      verdict: "changes_requested",
                      submittedAt: "2026-06-16T12:00:00Z",
                      body: "Self review should stay hidden.",
                      reviewUrl:
                        "https://example.com/pr/3#pullrequestreview-789",
                      autoInjectReview: true,
                    },
                  ],
                  unresolvedBy: [
                    {
                      reviewerId: "ada",
                      count: 1,
                      links: [
                        {
                          body: "Self comment should stay hidden.",
                          file: "self.ts",
                          line: 1,
                          autoInjectReview: true,
                        },
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

    const summary = await screen.findByTestId("github-review-summary");
    const externalReview = summary.closest("article") as HTMLElement;
    expect(screen.queryByRole("button", { name: /ada/i })).not.toBeInTheDocument();
    expect(screen.queryByText("Self review should stay hidden.")).not.toBeInTheDocument();
    expect(screen.queryByText("Self comment should stay hidden.")).not.toBeInTheDocument();
    expect(summary).toHaveClass("select-text");
    expect(within(summary).getByText("ready").tagName).toBe("STRONG");
    expect(within(summary).getByText("Ship it").tagName).toBe("LI");
    expect(summary).not.toHaveTextContent("**ready**");
    expect(
      within(externalReview).getByText("Reviewed 3d ago"),
    ).toBeInTheDocument();
    expect(
      within(externalReview).queryByRole("link", { name: "View on PR" }),
    ).not.toBeInTheDocument();
    expect(
      within(externalReview).queryByRole("button", { name: "Request to re-review PR" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("External reviews")).toBeInTheDocument();
  });

  it("requests PR re-review from the external review actions menu", async () => {
    const previous = getMock.getMockImplementation()!;
    getMock.mockImplementation(async (path: string, opts?: unknown) => {
      if (path === "/api/v1/sessions/{sessionId}/pr") {
        return {
          data: {
            prs: [
              prSummary(3, "open", {
                review: {
                  decision: "changes_requested",
                  hasUnresolvedHumanComments: false,
                  reviews: [
                    {
                      reviewerId: "maya",
                      verdict: "changes_requested",
                      submittedAt: "2026-06-16T11:00:00Z",
                      reviewUrl:
                        "https://example.com/pr/3#pullrequestreview-456",
                      body: "Please request another look after the fixes.",
                      autoInjectReview: false,
                    },
                  ],
                  unresolvedBy: [],
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
    expect(screen.getByText("Please request another look after the fixes.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Review actions" }));
    await userEvent.click(screen.getByRole("button", { name: "Request to re-review PR" }));
    await waitFor(() => expect(postMock).toHaveBeenCalledWith(
      "/api/v1/sessions/{sessionId}/reviews/rerequest",
      {
        params: { path: { sessionId: "sess-1" } },
        body: {
          pullRequestUrl: "https://api.github.com/repos/acme/repo/pulls/3",
          reviewerId: "maya",
        },
      },
    ));
    expect(
      screen.getByRole("button", { name: "Asked for re-review" }),
    ).toBeDisabled();
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
                        {
                          file: "a.ts",
                          line: 9,
                          url: "https://example.com/comment-9",
                          autoInjectReview: false,
                        },
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

    expect(screen.queryByText("Not injected")).not.toBeInTheDocument();
    expect(screen.getByText("External reviews")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Send to worker agent" }),
    ).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: /maya.*Changes requested/i }),
    );
    await userEvent.click(
      screen.getAllByRole("button", { name: "Comment actions" })[1]!,
    );
    const sendButton = screen.getByRole("button", {
      name: "Send to worker agent",
    });
    expect(sendButton).toBeEnabled();
    await userEvent.click(sendButton);
    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/send",
        {
          params: { path: { sessionId: "sess-1" } },
          body: {
            message: expect.stringContaining("Location: a.ts:9"),
          },
        },
      ),
    );
    expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", {
      params: { path: { sessionId: "sess-1" } },
      body: {
        message: expect.stringContaining(
          "commit the fix, and push the branch to GitHub",
        ),
      },
    });
    expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", {
      params: { path: { sessionId: "sess-1" } },
      body: {
        message: expect.stringContaining("Reviewer: @maya"),
      },
    });
    expect(
      screen.queryByRole("button", { name: "Resolve comment" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getAllByRole("status", { name: "Sent to worker agent" }),
    ).toHaveLength(2);
  });

  it("marks an AO review using its stored injection decision", async () => {
    mockCommonGets([], "reviewer-pane", [
      {
        ...reviewState(3, "up_to_date", "abc123"),
        latestRun: { ...approvedReview, autoInjectReview: false },
      },
    ]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    expect(screen.queryByText("Not injected")).not.toBeInTheDocument();
    expect(screen.getByText("Agent reviews")).toBeInTheDocument();
  });

  it("persists the automatic review injection toggle", async () => {
    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);

    const toggle = screen.getByRole("switch", {
      name: "Automatically fix review comments",
    });
    expect(toggle).toBeChecked();
    await userEvent.click(toggle);

    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/auto-inject-review",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { autoInjectReview: false },
        },
      ),
    );
  });

  it("persists the chosen reviewer for the session and uses it for the run", async () => {
    mockCommonGets([], "reviewer-pane", [
      reviewState(3, "needs_review", "sha-1"),
    ]);
    postMock.mockResolvedValue({
      data: { reviewerHandleId: "", reviews: [] },
      response: { status: 201 },
    });

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    await userEvent.click(
      await screen.findByRole("button", { name: /Select reviewer agent/ }),
    );
    await userEvent.click(
      await screen.findByRole("menuitem", { name: /opencode/ }),
    );
    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/switch",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { harness: "opencode" },
        },
      ),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Review latest commit" }),
    );

    expect(postMock).toHaveBeenCalledWith(
      "/api/v1/sessions/{sessionId}/reviews/trigger",
      {
        params: { path: { sessionId: "sess-1" } },
        body: { harness: "opencode" },
      },
    );
  });

  it("preserves hidden reviewer config fields when saving a session reviewer model", async () => {
    getMock.mockImplementation(async (path: string, options?: { params?: { path?: { agent?: string } } }) => {
      if (path === "/api/v1/agents/{agent}/models" && options?.params?.path?.agent === "codex") {
        return {
          data: {
            agentId: "codex",
            selectionMode: "catalog",
            models: [
              { id: "gpt-5", label: "GPT-5", isDefault: true },
              { id: "gpt-5-mini", label: "GPT-5 Mini" },
            ],
            allowCustom: false,
            source: "official-catalog",
            fetchedAt: "2026-08-30T00:00:00Z",
            stale: false,
          },
          error: undefined,
        };
      }
      return commonGetsResponder([], "reviewer-pane", [reviewState(3, "needs_review", "sha-1")])(path);
    });
    postMock.mockResolvedValue({
      data: { reviewerHandleId: "", reviews: [] },
      error: undefined,
      response: { status: 200 },
    });

    renderWithQuery(
      <SessionInspector
        session={session([pr(3, "open")], {
          reviewerHarness: "codex",
          reviewerConfig: { model: "gpt-5", permissions: "bypass-permissions" },
        })}
      />,
    );
    await openReviewsSection();

    await userEvent.click(await screen.findByRole("button", { name: /Select reviewer agent/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /codex/i }));
    await waitFor(() =>
      expect(screen.getByRole("menuitem", { name: "GPT-5 Mini" })).toBeInTheDocument(),
    );
    expect(postCallsFor("/api/v1/sessions/{sessionId}/reviews/switch")).toHaveLength(0);
    await userEvent.click(screen.getByRole("menuitem", { name: "GPT-5 Mini" }));

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/switch",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { harness: undefined, agentConfig: { model: "gpt-5-mini", permissions: "bypass-permissions" } },
        },
      ),
    );
  });

  it("preserves hidden reviewer config when the explicit override matches the default harness", async () => {
    getMock.mockImplementation(async (path: string, options?: { params?: { path?: { agent?: string } } }) => {
      if (path === "/api/v1/agents/{agent}/models" && options?.params?.path?.agent === "codex") {
        return {
          data: {
            agentId: "codex",
            selectionMode: "catalog",
            models: [
              { id: "gpt-5", label: "GPT-5", isDefault: true },
              { id: "gpt-5-mini", label: "GPT-5 Mini" },
            ],
            allowCustom: false,
            source: "official-catalog",
            fetchedAt: "2026-08-30T00:00:00Z",
            stale: false,
          },
          error: undefined,
        };
      }
      return commonGetsResponder([], "reviewer-pane", [reviewState(3, "needs_review", "sha-1")])(path);
    });
    postMock.mockResolvedValue({
      data: { reviewerHandleId: "", reviews: [] },
      error: undefined,
      response: { status: 200 },
    });

    renderWithQuery(
      <SessionInspector
        session={session([pr(3, "open")], {
          provider: "codex",
          reviewerHarness: "codex",
          reviewerConfig: { permissions: "bypass-permissions" },
        })}
      />,
    );
    await openReviewsSection();

    await userEvent.click(await screen.findByRole("button", { name: /Select reviewer agent/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /codex/i }));
    await waitFor(() =>
      expect(screen.getByRole("menuitem", { name: "GPT-5 Mini" })).toBeInTheDocument(),
    );
    expect(postCallsFor("/api/v1/sessions/{sessionId}/reviews/switch")).toHaveLength(0);

    await userEvent.click(screen.getByRole("menuitem", { name: "GPT-5 Mini" }));
    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/switch",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { harness: undefined, agentConfig: { model: "gpt-5-mini", permissions: "bypass-permissions" } },
        },
      ),
    );
  });

  it("does not expose arbitrary custom reviewer models from another reviewer row", async () => {
    getMock.mockImplementation(async (path: string, options?: { params?: { path?: { agent?: string } } }) => {
      if (path === "/api/v1/agents/{agent}/models") {
        const agent = options?.params?.path?.agent ?? "";
        return {
          data: {
            agentId: agent,
            selectionMode: agent === "opencode" ? "text" : "catalog",
            models: [],
            allowCustom: agent === "opencode",
            source: "manual",
            fetchedAt: "2026-08-30T00:00:00Z",
            stale: false,
          },
          error: undefined,
        };
      }
      return commonGetsResponder([], "reviewer-pane", [reviewState(3, "needs_review", "sha-1")])(path);
    });
    postMock.mockResolvedValue({
      data: { reviewerHandleId: "", reviews: [] },
      error: undefined,
      response: { status: 200 },
    });

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    await userEvent.click(await screen.findByRole("button", { name: /Select reviewer agent/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /^opencode$/i }));

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/switch",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { harness: "opencode" },
        },
      ),
    );
    expect(screen.queryByRole("menuitem", { name: /^custom opencode model$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /custom opencode/i })).not.toBeInTheDocument();
    expect(postCallsFor("/api/v1/sessions/{sessionId}/reviews/switch")).toHaveLength(1);
  });

  it("shows suggested reviewer models after reopening the selected reviewer picker", async () => {
    getMock.mockImplementation(async (path: string, options?: { params?: { path?: { agent?: string } } }) => {
      if (path === "/api/v1/agents/{agent}/models") {
        const agent = options?.params?.path?.agent ?? "";
        if (agent === "opencode") {
          return {
            data: {
              agentId: agent,
              selectionMode: "text",
              models: [
                { id: "suggested-a", label: "Suggested A" },
                { id: "suggested-b", label: "Suggested B" },
              ],
              allowCustom: true,
              source: "manual",
              fetchedAt: "2026-08-30T00:00:00Z",
              stale: false,
            },
            error: undefined,
          };
        }
        return {
          data: {
            agentId: agent,
            selectionMode: "catalog",
            models: [],
            allowCustom: false,
            source: "manual",
            fetchedAt: "2026-08-30T00:00:00Z",
            stale: false,
          },
          error: undefined,
        };
      }
      return commonGetsResponder([], "reviewer-pane", [reviewState(3, "needs_review", "sha-1")])(path);
    });
    postMock.mockResolvedValue({
      data: { reviewerHandleId: "", reviews: [] },
      error: undefined,
      response: { status: 200 },
    });

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    await userEvent.click(await screen.findByRole("button", { name: /Select reviewer agent/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /^opencode$/i }));
    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/switch",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { harness: "opencode" },
        },
      ),
    );
    await userEvent.click(await screen.findByRole("button", { name: /Select reviewer agent/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /^opencode$/i }));
    expect(await screen.findByRole("menuitem", { name: "Suggested A" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /^custom opencode model$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /custom opencode/i })).not.toBeInTheDocument();
  });

  it("allows selecting a text-selection reviewer harness without exposing custom reviewer entry", async () => {
    getMock.mockImplementation(async (path: string, options?: { params?: { path?: { agent?: string } } }) => {
      if (path === "/api/v1/agents/{agent}/models") {
        const agent = options?.params?.path?.agent ?? "";
        return {
          data: {
            agentId: agent,
            selectionMode: agent === "opencode" ? "text" : "catalog",
            models: [],
            allowCustom: agent === "opencode",
            source: "manual",
            fetchedAt: "2026-08-30T00:00:00Z",
            stale: false,
          },
          error: undefined,
        };
      }
      return commonGetsResponder([], "reviewer-pane", [reviewState(3, "needs_review", "sha-1")])(path);
    });
    postMock.mockResolvedValue({
      data: { reviewerHandleId: "", reviews: [] },
      error: undefined,
      response: { status: 200 },
    });

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    await userEvent.click(await screen.findByRole("button", { name: /Select reviewer agent/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /^opencode$/i }));
    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/switch",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { harness: "opencode" },
        },
      ),
    );
    expect(screen.queryByRole("menuitem", { name: /^custom opencode model$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /custom opencode/i })).not.toBeInTheDocument();
  });

  it("keeps the current default reviewer open for model selection", async () => {
    getMock.mockImplementation(async (path: string, options?: { params?: { path?: { agent?: string } } }) => {
      if (path === "/api/v1/agents/{agent}/models" && options?.params?.path?.agent === "codex") {
        return {
          data: {
            agentId: "codex",
            selectionMode: "catalog",
            models: [
              { id: "gpt-5", label: "GPT-5", isDefault: true },
              { id: "gpt-5-mini", label: "GPT-5 Mini" },
            ],
            allowCustom: false,
            source: "official-catalog",
            fetchedAt: "2026-08-29T00:00:00Z",
            stale: false,
          },
          error: undefined,
        };
      }
      return commonGetsResponder([], "reviewer-pane", [reviewState(3, "needs_review", "sha-1")])(path);
    });
    postMock.mockResolvedValue({
      data: { reviewerHandleId: "", reviews: [] },
      error: undefined,
      response: { status: 200 },
    });

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    await userEvent.click(await screen.findByRole("button", { name: /Select reviewer agent/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /codex/i }));

    await waitFor(() =>
      expect(screen.getByRole("menuitem", { name: "GPT-5 Mini" })).toBeInTheDocument(),
    );
    expect(postCallsFor("/api/v1/sessions/{sessionId}/reviews/switch")).toHaveLength(0);
    await userEvent.click(screen.getByRole("menuitem", { name: "GPT-5 Mini" }));
    await waitFor(() =>
      expect(postMock).toHaveBeenLastCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/switch",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { harness: undefined, agentConfig: { model: "gpt-5-mini" } },
        },
      ),
    );
    expect(postCallsFor("/api/v1/sessions/{sessionId}/reviews/switch")).toHaveLength(1);
  });

  it("clears hidden session reviewer config when returning an explicit default-matching reviewer to project default", async () => {
    mockCommonGets([], "reviewer-pane", [reviewState(3, "needs_review", "sha-1")]);
    postMock.mockResolvedValue({
      data: { reviewerHandleId: "", reviews: [] },
      response: { status: 200 },
    });

    renderWithQuery(
      <SessionInspector
        session={session([pr(3, "open")], {
          provider: "codex",
          reviewerHarness: "codex",
          reviewerConfig: { permissions: "bypass-permissions" },
        })}
      />,
    );
    await openReviewsSection();

    const picker = await screen.findByRole("button", {
      name: /Select reviewer agent/,
    });
    await userEvent.click(picker);
    await userEvent.click(screen.getByRole("menuitem", { name: /codex/i }));

    await waitFor(() =>
      expect(postMock).toHaveBeenLastCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/switch",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { harness: undefined, agentConfig: undefined },
        },
      ),
    );
  });

  it("keeps an explicit reviewer visible and lets it return to the resolved default", async () => {
    mockCommonGets([], "reviewer-pane", [
      reviewState(3, "needs_review", "sha-1"),
    ]);
    postMock.mockResolvedValue({
      data: { reviewerHandleId: "", reviews: [] },
      response: { status: 201 },
    });

    renderWithQuery(
      <SessionInspector
        session={sessionWithProvider([pr(3, "open")], "codex")}
      />,
    );
    await openReviewsSection();

    const picker = await screen.findByRole("button", {
      name: /Select reviewer agent/,
    });
    await userEvent.click(picker);
    expect(screen.getAllByRole("menuitem", { name: /codex/i })).toHaveLength(1);
    expect(
      screen.getByRole("menuitem", { name: /opencode/ }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: /opencode/ }));

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/switch",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { harness: "opencode" },
        },
      ),
    );

    await userEvent.click(picker);
    expect(screen.getAllByRole("menuitem", { name: /codex/i })).toHaveLength(1);
    await userEvent.click(screen.getByRole("menuitem", { name: /codex/i }));

    await waitFor(() =>
      expect(postMock).toHaveBeenLastCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/switch",
        {
          params: { path: { sessionId: "sess-1" } },
          body: { harness: undefined },
        },
      ),
    );
  });

  it("names the reviewer that is actually running, not whichever PR comes first", async () => {
    // One PR reviewed earlier by claude-code, another running under codex.
    const done = {
      ...reviewState(3, "up_to_date", "sha-a"),
      latestRun: {
        ...approvedReview,
        id: "run-done",
        harness: "claude-code",
        status: "complete",
      },
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

    renderWithQuery(
      <SessionInspector session={session([pr(3, "open"), pr(4, "open")])} />,
    );
    await openReviewsSection();

    expect(
      await screen.findByText("Review in progress · Codex"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Review in progress · Claude Code"),
    ).not.toBeInTheDocument();
  });

  it("keeps older harness summaries behind explicit pagination when selecting the next agent", async () => {
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

    expect(
      await screen.findByText("codex asked for tests."),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("claude-code found nothing blocking."),
    ).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "Load more · 1 earlier" }),
    );
    expect(
      screen.getByText("claude-code found nothing blocking."),
    ).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "Show latest only" }),
    );
    expect(
      screen.queryByText("claude-code found nothing blocking."),
    ).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: /Select reviewer agent/ }),
    );
    await userEvent.click(
      screen.getByRole("menuitem", { name: /claude-code/ }),
    );
    expect(
      screen.queryByText("claude-code found nothing blocking."),
    ).not.toBeInTheDocument();
    expect(screen.getByText("codex asked for tests.")).toBeInTheDocument();
    expect(screen.getByText("Reviewable change 3")).toBeInTheDocument();
  });

  it("locks the reviewer choice while one is running", async () => {
    const running = {
      ...reviewState(3, "running", "sha-1"),
      latestRun: {
        ...approvedReview,
        id: "run-live",
        harness: "codex",
        status: "running",
        verdict: "",
      },
    };
    mockCommonGets([], "reviewer-pane", [running]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    // AO runs one reviewer per worker, so a second harness cannot start
    // alongside it. Say so rather than silently ignoring the choice.
    expect(screen.getByText("Review in progress · Codex")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Select reviewer agent/ }),
    ).toBeDisabled();
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
    expect(
      screen.getByRole("button", { name: "Re-run review" }),
    ).toBeInTheDocument();
  });

  it("shows a no-needed-reviews notice instead of opening the terminal when the backend reuses runs", async () => {
    mockCommonGets([approvedReview], "reviewer-pane", [
      reviewState(3, "up_to_date"),
    ]);
    postMock.mockResolvedValue({
      response: { status: 200 },
      data: {
        reviewerHandleId: "reviewer-pane",
        reviews: [],
      },
    });
    const onOpenReviewerTerminal = vi.fn();

    renderWithQuery(
      <SessionInspector
        onOpenReviewerTerminal={onOpenReviewerTerminal}
        session={session([pr(3, "open")])}
      />,
    );
    await openReviewsSection();

    await userEvent.click(
      await screen.findByRole("button", { name: /re-run review/i }),
    );

    // The notice is a compact marker; the sentence itself is its accessible name
    // and rides a tooltip, so it costs the rail one line instead of a boxed
    // paragraph that outlives the click that caused it.
    const alreadyReviewed = await screen.findByRole("button", {
      name: "This commit has already been reviewed. Push a new commit to run another review.",
    });
    expect(alreadyReviewed).toHaveTextContent(
      "This commit has already been reviewed",
    );
    expect(onOpenReviewerTerminal).not.toHaveBeenCalled();
  });

  it("cancels the running review instead of allowing rerun", async () => {
    mockCommonGets([approvedReview], "reviewer-pane", [
      reviewState(3, "running", "abc123"),
      reviewState(4, "up_to_date", "def456"),
    ]);
    const onOpenReviewerTerminal = vi.fn();

    renderWithQuery(
      <SessionInspector
        onOpenReviewerTerminal={onOpenReviewerTerminal}
        session={session([pr(3, "open")])}
      />,
    );
    await openReviewsSection();

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Stop review" })).toBeEnabled(),
    );
    expect(
      screen.queryByRole("button", { name: /re-run review/i }),
    ).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /stop review/i }));

    await waitFor(() => {
      expect(postMock).toHaveBeenCalledWith(
        "/api/v1/sessions/{sessionId}/reviews/cancel",
        {
          params: { path: { sessionId: "sess-1" } },
        },
      );
    });
    expect(onOpenReviewerTerminal).not.toHaveBeenCalled();
  });

  it("does not list a cancelled run as a reviewed outcome", async () => {
    mockCommonGets([], "reviewer-pane", [
      {
        ...reviewState(3, "needs_review", "abc123"),
        latestRun: { ...failedReview, status: "cancelled" },
      },
    ]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    expect(screen.queryByText("Cancelled")).not.toBeInTheDocument();
    expect(screen.queryByText("Failed")).not.toBeInTheDocument();
    expect(screen.queryByText("reviewer crashed")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Review latest commit" }),
    ).toBeEnabled();
  });

  it("shows the reviewer identity and aggregate verdict", async () => {
    mockCommonGets([], "reviewer-pane", [
      {
        ...reviewState(3, "changes_requested", "abc123"),
        latestRun: {
          ...approvedReview,
          verdict: "changes_requested",
          body: "Please fix auth.",
        },
      },
    ]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    expect(
      await screen.findByRole("button", { name: /Select reviewer agent/ }),
    ).toHaveTextContent("Codex");
    expect(screen.queryByText("reviewer")).not.toBeInTheDocument();
    expect(screen.queryByText("sess-1")).not.toBeInTheDocument();
    expect(screen.queryByText("review session")).not.toBeInTheDocument();
    expect(screen.getAllByText("Changes requested")).not.toHaveLength(0);
  });

  it("does not list a failed run without a verdict and still allows reviewing the latest commit", async () => {
    mockCommonGets([failedReview], "reviewer-pane", [
      { ...reviewState(3, "needs_review", "abc123"), latestRun: failedReview },
    ]);

    renderWithQuery(<SessionInspector session={session([pr(3, "open")])} />);
    await openReviewsSection();

    expect(screen.queryByText("Failed")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Review latest commit" }),
    ).toBeEnabled();
  });

  it("surfaces the latest automatic review failure while auto review is enabled", async () => {
    const failedAutoReview = {
      ...failedReview,
      triggerSource: "auto",
      body: 'reviewer preflight: reviewer harness "kimi" is unauthorized',
    };
    mockCommonGets([], "", [
      {
        ...reviewState(3, "needs_review", "abc123"),
        latestRun: failedAutoReview,
      },
    ]);

    renderWithQuery(
      <SessionInspector
        session={session([pr(3, "open")], { autoReviewEnabled: true })}
      />,
    );
    await openReviewsSection();

    expect(screen.getByRole("status")).toHaveTextContent(
      'Auto review Failed: reviewer preflight: reviewer harness "kimi" is unauthorized',
    );
  });

  it("hides Reviews when the session has no PR while keeping its durable preference in Summary", async () => {
    mockCommonGets();
    renderWithQuery(<SessionInspector session={session([])} />);

    await screen.findByRole("tab", { name: /Summary/ });
    expect(screen.queryByRole("tab", { name: /Reviews/ })).not.toBeInTheDocument();
    expect(screen.getAllByRole("tab").map((tab) => tab.textContent?.trim())).toEqual([
      "Summary",
      "Browser",
      "Files",
    ]);
    expect(
      screen.getByRole("switch", { name: "Automatically fix review comments" }),
    ).toBeInTheDocument();
    expect(screen.getByText("No pull request opened yet.")).toBeInTheDocument();
  });

  it("falls back to Summary when a controlled Reviews selection has no PR", async () => {
    const onViewChange = vi.fn();
    renderWithQuery(
      <SessionInspector
        onViewChange={onViewChange}
        session={session([])}
        view="reviews"
      />,
    );

    expect(screen.getByRole("tab", { name: "Summary" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.queryByRole("tab", { name: "Reviews" })).not.toBeInTheDocument();
    expect(screen.getByText("Session controls")).toBeInTheDocument();
    await waitFor(() => expect(onViewChange).toHaveBeenCalledWith("summary"));
  });
});
