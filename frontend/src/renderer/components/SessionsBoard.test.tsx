import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { toKanbanColumn } from "@aoagents/product-ui";
import { appI18n } from "../i18n";

// Instant motion updates so height tweens do not leave tests waiting on timers.
vi.mock("motion/react", async (importOriginal) => {
	const actual = await importOriginal<typeof import("motion/react")>();
	return {
		...actual,
		AnimatePresence: ({ children }: { children: React.ReactNode }) => children,
	};
});

const {
	navigateMock,
	notificationShowMock,
	postMock,
	workspaceQueryMock,
	usageQueryMock,
	boardActionsInPanelMock,
} = vi.hoisted(() => ({
	navigateMock: vi.fn(),
	notificationShowMock: vi.fn(),
	postMock: vi.fn(),
	workspaceQueryMock: vi.fn(),
	usageQueryMock: vi.fn(),
	boardActionsInPanelMock: vi.fn(() => false),
}));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	workspaceQueryKey: ["workspaces"],
	useWorkspaceQuery: workspaceQueryMock,
	useWorkspaceScope: (projectId?: string) => {
		const query = workspaceQueryMock();
		return {
			...query,
			data: { project: query.data?.find((workspace: WorkspaceSummary) => workspace.id === projectId) },
		};
	},
}));

vi.mock("../hooks/useSessionUsageSummaries", () => ({
	useSessionUsageSummaries: usageQueryMock,
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: (...args: unknown[]) => postMock(...args) },
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		clipboard: {
			writeText: vi.fn(),
		},
		notifications: {
			show: (...args: unknown[]) => notificationShowMock(...args),
		},
	},
}));

vi.mock("../lib/platform", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../lib/platform")>();
	return {
		...actual,
		usesBoardActionsInPanel: () => boardActionsInPanelMock(),
		isLinuxPlatform: () => false,
	};
});

import { archiveToggleHeightClassName, archiveToggleOffsetClassName } from "@aoagents/product-ui";
import { SessionsBoard } from "./SessionsBoard";
import { toBoardSessionPresentation } from "./SessionsBoardAdapters";
import { TooltipProvider } from "./ui/tooltip";

function renderBoard(projectId?: string) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderBoardWithClient(queryClient, projectId);
	return queryClient;
}

function renderBoardWithClient(queryClient: QueryClient, projectId?: string) {
	return render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<SessionsBoard projectId={projectId} />
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

/** Archive cards mount on the next frame via startTransition — wait for the list. */
async function expandArchive() {
	await userEvent.click(screen.getByRole("button", { name: /archive/i }));
	return screen.findByRole("list", { name: "Archived sessions" });
}

beforeEach(() => {
	navigateMock.mockReset();
	notificationShowMock.mockReset().mockResolvedValue(undefined);
	postMock.mockReset().mockResolvedValue({ data: {} });
	workspaceQueryMock.mockReset().mockReturnValue({ data: [], isError: false });
	usageQueryMock.mockReset().mockReturnValue({ data: new Map() });
	window.localStorage.removeItem("ao.board.archive.layout");
	boardActionsInPanelMock.mockReset().mockReturnValue(false);
});

describe("SessionsBoard", () => {
	it("uses the last human message time rather than generic session updatedAt", () => {
		const presentation = toBoardSessionPresentation(
			boardSession({
				id: "timestamp-session",
				lastUserMessageAt: "2026-01-01T09:00:00Z",
				status: "idle",
				title: "timestamp task",
				updatedAt: "2026-01-01T10:00:00Z",
			}),
		);

		expect(presentation.lastUserMessageAt).toBe("2026-01-01T09:00:00Z");
	});

	it("localizes dynamic card actions and pull request lifecycle labels", async () => {
		await appI18n.changeLanguage("zh-CN");
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-localized",
						title: "localized worker",
						status: "pr_open",
						prs: [
							{
								url: "https://github.com/acme/repo/pull/42",
								number: 42,
								state: "open",
								ci: "passing",
								review: "approved",
								mergeability: "mergeable",
								reviewComments: false,
								updatedAt: "2026-01-01T00:00:00Z",
							},
						],
					}),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		try {
			renderBoard("p1");
			expect(screen.getByRole("button", { name: "终止 localized worker" })).toBeInTheDocument();
			expect(screen.getByRole("link", { name: "PR #42 已打开" })).toHaveAttribute(
				"href",
				"https://github.com/acme/repo/pull/42",
			);
		} finally {
			await appI18n.changeLanguage("en");
		}
	});

	it("does not show an agent setup warning on the board", () => {
		renderBoard();

		expect(screen.queryByText(/reload agents/i)).not.toBeInTheDocument();
	});

	it("shows the Board identity and compact actions in the in-panel board chrome", () => {
		boardActionsInPanelMock.mockReturnValue(true);
		workspaceQueryMock.mockReturnValue({
			data: [
				{
					id: "p1",
					name: "solkit-ui",
					path: "/tmp/solkit-ui",
					sessions: [
						{
							id: "s1",
							workspaceId: "p1",
							workspaceName: "solkit-ui",
							title: "test",
							provider: "codex",
							branch: "ao/dev/solkit-ui-5/root",
							status: "running",
							activity: { state: "working", lastActivityAt: "2026-01-01T00:00:00Z" },
							updatedAt: "2026-01-01T00:00:00Z",
							prs: [],
						},
					],
				},
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		expect(screen.getByTestId("board-topbar-label").textContent).toContain("Board");
		expect(screen.queryByText("solkit-ui")).toBeNull();
		expect(screen.getByRole("button", { name: "New task" }).closest(".center-panel-titlebar")).toHaveClass(
			"workspace-topbar-container",
		);
		expect(
			within(screen.getByRole("button", { name: "New task" })).getByText("Task").hasAttribute("data-compact-label"),
		).toBe(true);
	});

	it.each([
		["active", "Working", "bg-status-working", true],
		["idle", "Idle", "bg-status-idle", false],
	] as const)("shows %s orchestrator activity in the in-panel board toolbar", (state, label, tone, pulses) => {
		boardActionsInPanelMock.mockReturnValue(true);
		workspaceQueryMock.mockReturnValue({
			data: [
				{
					id: "p1",
					name: "solkit-ui",
					path: "/tmp/solkit-ui",
					sessions: [
						{
							id: "orch-1",
							workspaceId: "p1",
							workspaceName: "solkit-ui",
							title: "orchestrator",
							provider: "codex",
							kind: "orchestrator",
							branch: "main",
							status: "working",
							activity: { state, lastActivityAt: "2026-01-01T00:00:00Z" },
							updatedAt: "2026-01-01T00:00:00Z",
							prs: [],
						},
					],
				},
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const button = screen.getByRole("button", { name: `Orchestrator, ${label}` });
		const indicator = button.querySelector("span.size-dot-sm") as HTMLElement;
		expect(within(button).getByText("Orchestrator").hasAttribute("data-compact-label")).toBe(true);
		expect(indicator).toHaveAttribute("aria-hidden", "true");
		expect(indicator).toHaveClass(tone);
		expect(indicator).toHaveClass(pulses ? "animate-status-pulse" : "size-dot-sm");
		if (!pulses) expect(indicator).not.toHaveClass("animate-status-pulse");
	});

	it("shows the Board crumb on the root board when actions live in the panel", () => {
		boardActionsInPanelMock.mockReturnValue(true);
		workspaceQueryMock.mockReturnValue({
			data: [
				{
					id: "p1",
					name: "solkit-ui",
					path: "/tmp/solkit-ui",
					sessions: [],
				},
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard();

		expect(screen.getByText("Board")).toBeInTheDocument();
	});

	it("labels an idle session as Idle, not Working", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				{
					id: "p1",
					name: "radic",
					path: "/tmp/radic",
					sessions: [
						{
							id: "s1",
							workspaceId: "p1",
							workspaceName: "radic",
							title: "brand-font-pipeline",
							provider: "claude-code",
							branch: "ao/radic-5",
							status: "idle",
							activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
							updatedAt: "2026-01-01T00:00:00Z",
							prs: [],
						},
					],
				},
			],
			isError: false,
		});

		renderBoard("p1");

		const idleCard = screen
			.getByText("brand-font-pipeline")
			.closest('[data-testid="board-session-card"]') as HTMLElement;
		expect(within(idleCard).getByText("Idle")).toBeInTheDocument();
		const terminateButton = within(idleCard).getByRole("button", { name: "Terminate brand-font-pipeline" });
		expect(terminateButton).toHaveClass("opacity-0", "group-hover:opacity-100", "group-focus-within:opacity-100");
		expect(terminateButton.querySelector("svg")).toHaveClass("lucide-trash-2");
		expect(within(idleCard).getByText("Idle").parentElement?.parentElement).toHaveClass("flex");
		expect(within(idleCard).getByText("brand-font-pipeline")).toHaveClass("font-semibold", "line-clamp-2");
	});

	it("shows coverage-aware cost with tokens on active and archived cards", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({ id: "s-active", title: "active worker", status: "idle" }),
					boardSession({ id: "s-empty", title: "empty worker", status: "idle" }),
					boardSession({ id: "s-tokens", title: "tokens worker", status: "idle" }),
					terminatedSession(),
				]),
			],
			isError: false,
			isSuccess: true,
		});
		usageQueryMock.mockReturnValue({
			data: new Map([
				[
					"s-active",
					{
						estimatedCost: {
							cachedInputNanos: 100_000_000,
							coverage: "complete",
							inputNanos: 540_000_000,
							outputNanos: 600_000_000,
							providerAttribution: "observed",
							totalNanos: 1_240_000_000,
						},
						sessionId: "s-active",
						processedTokens: 12_300,
						totalTokens: 12_400,
						incomplete: false,
					},
				],
				[
					"s-empty",
					{
						estimatedCost: null,
						sessionId: "s-empty",
						processedTokens: 0,
						totalTokens: 0,
						incomplete: false,
					},
				],
				[
					"s-tokens",
					{
						estimatedCost: null,
						sessionId: "s-tokens",
						processedTokens: 800,
						totalTokens: 800,
						incomplete: false,
					},
				],
				[
					"s-dead",
					{
						estimatedCost: {
							cachedInputNanos: null,
							coverage: "partial",
							inputNanos: 5_000_000,
							outputNanos: 15_000_000,
							providerAttribution: "inferred",
							totalNanos: 20_000_000,
						},
						sessionId: "s-dead",
						processedTokens: 1_900,
						totalTokens: 2_000,
						incomplete: true,
					},
				],
			]),
		});

		renderBoard("p1");

		// The card shows dollar cost by default; the token count remains in the
		// hover tooltip and accessible label.
		const activeUsage = screen.getByText("$1.24", { selector: "span" });
		expect(activeUsage).toHaveAttribute("aria-hidden", "true");
		expect(screen.getByText("$1.24 · 12,300 tokens")).toHaveClass("sr-only");
		expect(screen.queryByText(/processed/i)).not.toBeInTheDocument();
		// Sessions without a dollar estimate stay visible if they still have
		// token usage; sessions with no cost and no tokens still show nothing.
		const emptyCard = screen.getByText("empty worker").closest('[data-testid="board-session-card"]') as HTMLElement;
		expect(within(emptyCard).queryByText("Unavailable")).not.toBeInTheDocument();
		expect(within(emptyCard).queryByText("0 tokens")).not.toBeInTheDocument();
		const tokensOnlyCard = screen.getByText("tokens worker").closest('[data-testid="board-session-card"]') as HTMLElement;
		expect(within(tokensOnlyCard).getByText("800", { selector: "span" })).toHaveAttribute("aria-hidden", "true");
		expect(within(tokensOnlyCard).getByText("800 tokens")).toHaveClass("sr-only");
		expect(usageQueryMock).toHaveBeenCalledWith("p1");

		const archive = await expandArchive();
		expect(within(archive).getByText("$0.02")).toHaveAttribute("aria-hidden", "true");
		expect(within(archive).getByText("$0.02 · 1,900 tokens")).toHaveClass("sr-only");
	});

	it("shows cost by default and cost plus tokens on hover without a tab stop", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({ id: "s-keyboard", title: "keyboard worker", status: "idle" }),
				]),
			],
			isError: false,
			isSuccess: true,
		});
		usageQueryMock.mockReturnValue({
			data: new Map([
				[
					"s-keyboard",
					{
						estimatedCost: {
							cachedInputNanos: 100_000_000,
							coverage: "complete",
							inputNanos: 540_000_000,
							outputNanos: 600_000_000,
							providerAttribution: "observed",
							totalNanos: 1_240_000_000,
						},
						incomplete: false,
						sessionId: "s-keyboard",
						processedTokens: 12_400,
						totalTokens: 12_400,
					},
				],
			]),
		});

		renderBoard("p1");

		const card = screen.getByText("keyboard worker").closest('[data-testid="board-session-card"]') as HTMLElement;
		const usage = within(card).getByText("$1.24", { selector: "span" });
		expect(usage.tagName).toBe("SPAN");
		// The compact text is decorative; the full label is real off-screen text
		// rather than an aria-label on a generic span, which is not reliably
		// exposed. The hover trigger is not a tab stop.
		expect(usage).toHaveAttribute("aria-hidden", "true");
		expect(within(card).getByText("$1.24 · 12,400 tokens")).toHaveClass("sr-only");

		within(card).getByRole("button", { name: "keyboard worker" }).focus();
		await userEvent.tab();
		expect(within(card).getByRole("button", { name: "Terminate keyboard worker" })).toHaveFocus();

		await userEvent.hover(usage);
		expect(await screen.findByRole("tooltip")).toHaveTextContent("$1.24 · 12,400 tokens");
	});

	it("shows token-only usage when pricing is unavailable", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({ id: "s-tokens", title: "tokens worker", status: "idle" }),
				]),
			],
			isError: false,
			isSuccess: true,
		});
		usageQueryMock.mockReturnValue({
			data: new Map([
				[
					"s-tokens",
					{
						estimatedCost: null,
						incomplete: false,
						sessionId: "s-tokens",
						processedTokens: 12_400,
						totalTokens: 12_400,
					},
				],
			]),
		});

		renderBoard("p1");

		const card = screen.getByText("tokens worker").closest('[data-testid="board-session-card"]') as HTMLElement;
		const usage = within(card).getByText("12.4K", { selector: "span" });
		expect(usage).toHaveAttribute("aria-hidden", "true");
		expect(within(card).getByText("12,400 tokens")).toHaveClass("sr-only");

		await userEvent.hover(usage);
		expect(await screen.findByRole("tooltip")).toHaveTextContent("12,400 tokens");
	});

	it("styles a working card from its building lane without inferring from runtime activity", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-active",
						title: "active-card-task",
						status: "working",
						activity: { state: "active", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");
		const card = screen.getByText("active-card-task").closest('[data-testid="board-session-card"]') as HTMLElement;
		const working = within(card).getByText("Working").parentElement as HTMLElement;
		expect(working).toHaveAttribute("data-kanban-column", "building");
		expect(working).toHaveClass("text-status-working");
		expect(working.style.getPropertyValue("--session-status-tone")).toBe("");
		expect(working.querySelector('[aria-hidden="true"]')).toHaveClass("animate-spin");
	});

	// A multi-PR session aggregates `status` from its worst open PR while
	// `displayStatus` comes from its best one, so a settled "Mergeable" card can
	// carry status `review_pending`. The loader must follow the label the card
	// actually shows, not the hidden aggregate, or an idle session spins forever.
	it("does not spin a settled Mergeable card whose worst PR aggregates to review_pending", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-mergeable",
						title: "mergeable-card-task",
						status: "review_pending",
						displayStatus: "Mergeable",
						kanbanColumn: "ready",
						activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");
		const card = screen.getByText("mergeable-card-task").closest('[data-testid="board-session-card"]') as HTMLElement;
		const status = within(card).getByTestId("session-status");
		expect(status).toHaveTextContent("Mergeable");
		expect(status.querySelector(".animate-spin")).toBeNull();
	});

	it("keeps the loader on a Review pending card", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-review-pending",
						title: "review-pending-card-task",
						status: "review_pending",
						displayStatus: "Review pending",
						kanbanColumn: "needs_review",
						activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");
		const card = screen.getByText("review-pending-card-task").closest('[data-testid="board-session-card"]') as HTMLElement;
		const status = within(card).getByTestId("session-status");
		expect(status).toHaveTextContent("Review pending");
		expect(status.querySelector(".animate-spin")).not.toBeNull();
	});

	it("keeps the loader on a Mergeable card while its agent is actually working", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-mergeable-active",
						title: "mergeable-active-task",
						status: "working",
						displayStatus: "Mergeable",
						kanbanColumn: "ready",
						activity: { state: "active", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");
		const card = screen.getByText("mergeable-active-task").closest('[data-testid="board-session-card"]') as HTMLElement;
		const status = within(card).getByTestId("session-status");
		expect(status.querySelector(".animate-spin")).not.toBeNull();
	});

	it("keeps a spawning card labeled Working when raw activity has not become active", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-spawning",
						title: "spawning-card-task",
						status: "working",
						activity: { state: "exited", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");
		const card = screen.getByText("spawning-card-task").closest('[data-testid="board-session-card"]') as HTMLElement;
		expect(within(card).getByText("Working")).toBeInTheDocument();
		expect(within(card).queryByText("Exited")).not.toBeInTheDocument();
	});

	it("shows switch progress instead of the exited source on a card", () => {
		const worker = boardSession({
			id: "s-switching",
			title: "switching worker",
			status: "exited",
			activity: {
				state: "exited",
				lastActivityAt: "2026-01-01T00:00:00Z",
			},
		});
		worker.activeAgentSwitch = activeAgentSwitch(worker.id);
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([worker])],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const card = screen.getByText("switching worker").closest('[data-testid="board-session-card"]') as HTMLElement;
		const status = within(card).getByText("Switching to Codex").parentElement as HTMLElement;
		expect(status).toHaveClass("text-status-working");
		expect(status).not.toHaveAttribute("data-kanban-column");
		expect(status.style.getPropertyValue("--session-status-tone")).toBe("");
		expect(status.querySelector(".animate-status-pulse")).toBeNull();
		expect(within(card).queryByText("Exited")).not.toBeInTheDocument();
	});

	it("styles legacy statuses from their status-implied Kanban columns", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				{
					id: "p1",
					name: "radic",
					path: "/tmp/radic",
					sessions: [
						{
							id: "s0",
							workspaceId: "p1",
							workspaceName: "radic",
							title: "idle-card-task",
							provider: "claude-code",
							branch: "ao/radic-5",
							status: "idle",
							activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
							updatedAt: "2026-01-01T00:00:00Z",
							prs: [],
						},
						{
							id: "s1",
							workspaceId: "p1",
							workspaceName: "radic",
							title: "no-signal-card-task",
							provider: "claude-code",
							branch: "ao/radic-6",
							status: "no_signal",
							activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
							updatedAt: "2026-01-01T00:00:00Z",
							prs: [],
						},
						{
							id: "s2",
							workspaceId: "p1",
							workspaceName: "radic",
							title: "draft-card-task",
							provider: "claude-code",
							branch: "ao/radic-7",
							status: "draft",
							activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
							updatedAt: "2026-01-01T00:00:00Z",
							prs: [],
						},
					],
				},
			],
			isError: false,
		});

		renderBoard("p1");
		const idleCard = screen.getByText("idle-card-task").closest('[data-testid="board-session-card"]') as HTMLElement;
		const noSignalCard = screen.getByText("no-signal-card-task").closest('[data-testid="board-session-card"]') as HTMLElement;
		const draftCard = screen.getByText("draft-card-task").closest('[data-testid="board-session-card"]') as HTMLElement;

		expect(within(idleCard).getByText("Idle").parentElement).toHaveAttribute(
			"data-kanban-column",
			"building",
		);
		expect(within(noSignalCard).getByText("No signal").parentElement).toHaveAttribute(
			"data-kanban-column",
			"needs_review",
		);
		expect(within(draftCard).getByText("Draft PR").parentElement).toHaveAttribute(
			"data-kanban-column",
			"validating",
		);
	});

	it("keeps a PR-less exited session in the building lane with an Exited badge", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					{
						id: "s-exited",
						workspaceId: "p1",
						workspaceName: "radic",
						title: "agent-exited-task",
						provider: "codex",
						branch: "ao/exited",
						status: "exited",
						// What the daemon derives for a worker with no PR, whatever its
						// runtime status. Set explicitly so this covers the daemon path,
						// not the older-daemon fallback.
						kanbanColumn: "building",
						activity: { state: "exited", lastActivityAt: "2026-01-01T00:00:00Z" },
						updatedAt: "2026-01-01T00:00:00Z",
						prs: [],
					},
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		// Lanes follow the daemon's column: a worker with no PR is still building,
		// whatever its runtime status. The card keeps its Exited badge.
		const buildingColumn = screen.getByLabelText("Building sessions");
		expect(within(buildingColumn).getByText("agent-exited-task")).toBeInTheDocument();
		expect(within(buildingColumn).getByText("Exited").parentElement).toHaveAttribute(
			"data-kanban-column",
			"building",
		);
	});

	it("swaps the building lane's cards when navigating between project boards", () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		workspaceQueryMock.mockReturnValue({
			data: [
				{
					id: "p1",
					name: "radic",
					path: "/tmp/radic",
					sessions: [
						{
							id: "p1-active",
							workspaceId: "p1",
							workspaceName: "radic",
							title: "p1 active",
							provider: "claude-code",
							branch: "ao/radic-active",
							status: "working",
							activity: { state: "active", lastActivityAt: "2026-01-01T00:00:00Z" },
							updatedAt: "2026-01-01T00:00:00Z",
							prs: [],
						},
						{
							id: "p1-idle",
							workspaceId: "p1",
							workspaceName: "radic",
							title: "p1 idle",
							provider: "claude-code",
							branch: "ao/radic-idle",
							status: "idle",
							activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
							updatedAt: "2026-01-01T00:00:00Z",
							prs: [],
						},
					],
				},
				{
					id: "p2",
					name: "other",
					path: "/tmp/other",
					sessions: [
						{
							id: "p2-active",
							workspaceId: "p2",
							workspaceName: "other",
							title: "p2 active",
							provider: "claude-code",
							branch: "ao/other-active",
							status: "working",
							activity: { state: "active", lastActivityAt: "2026-01-01T00:00:00Z" },
							updatedAt: "2026-01-01T00:00:00Z",
							prs: [],
						},
						{
							id: "p2-idle",
							workspaceId: "p2",
							workspaceName: "other",
							title: "p2 idle",
							provider: "claude-code",
							branch: "ao/other-idle",
							status: "idle",
							activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
							updatedAt: "2026-01-01T00:00:00Z",
							prs: [],
						},
					],
				},
			],
			isError: false,
		});
		const view = renderBoardWithClient(queryClient, "p1");

		const p1Lane = screen.getByRole("region", { name: "Building sessions" });
		expect(p1Lane).toHaveTextContent("p1 idle");
		expect(p1Lane).toHaveTextContent("p1 active");

		view.rerender(
			<QueryClientProvider client={queryClient}>
				<TooltipProvider>
					<SessionsBoard projectId="p2" />
				</TooltipProvider>
			</QueryClientProvider>,
		);

		const p2Lane = screen.getByRole("region", { name: "Building sessions" });
		expect(screen.queryByText("p1 idle")).not.toBeInTheDocument();
		expect(p2Lane).toHaveTextContent("p2 idle");
		expect(p2Lane).toHaveTextContent("p2 active");
	});

	it("shows a static archive card with a persistent restore action", async () => {
		const archivedSession = terminatedSession();
		const mergedPr = archivedSession.prs[0];
		if (!mergedPr) throw new Error("Archived-session fixture requires a pull request");
		archivedSession.prs = [
			{
				...mergedPr,
				number: 41,
				state: "open",
				url: "https://github.com/example/radic/pull/41",
			},
			mergedPr,
		];
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([archivedSession])],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const archiveButton = screen.getByRole("button", { name: /archive/i });
		expect(archiveButton).toHaveClass(archiveToggleHeightClassName, "w-full", "py-0");
		const archiveLabel = within(archiveButton).getByText("Archive");
		expect(archiveLabel).not.toHaveClass("font-mono", "uppercase");
		expect(archiveLabel).toHaveClass("text-2xs", "font-medium");
		// Expanded archive overlays the board instead of shrinking lanes (which would
		// force a persistent Needs You column scrollbar gutter).
		expect(archiveButton.parentElement).toHaveClass("absolute", "inset-x-0", "bottom-0", "bg-background");
		expect(screen.getByTestId("board")).toHaveClass("relative");
		expect(screen.getByTestId("board").querySelector(":scope > .min-h-0.flex-1")).toHaveClass(
			archiveToggleOffsetClassName,
		);
		const archive = await expandArchive();
		expect(archive).toHaveClass("scrollbar-none", "overflow-y-auto", "max-h-[28vh]");
		const terminatedCard = within(archive).getByText("dead worker").closest<HTMLElement>("[role='listitem']");
		expect(terminatedCard).not.toBeNull();
		expect(terminatedCard).toHaveAttribute("data-testid", "board-session-card");
		expect(terminatedCard).not.toHaveClass("min-h-28");
		expect(within(terminatedCard!).queryByRole("button", { name: "Open dead worker" })).not.toBeInTheDocument();
		expect(within(terminatedCard!).getByText("Terminated")).toBeInTheDocument();
		expect(within(terminatedCard!).getByTestId("session-pr-progress")).toHaveTextContent(
			"1 of 2 PRs merged · 1 open",
		);
		// Agent shown as its brand logo with an accessible name (not a text label).
		expect(within(terminatedCard!).getByRole("img", { name: "claude-code" })).toBeInTheDocument();
		expect(screen.getByText("ao/dead-worker")).toBeInTheDocument();
		expect(within(terminatedCard!).queryByText("github:INT-17")).not.toBeInTheDocument();
		expect(within(terminatedCard!).getByRole("link", { name: "PR #42 merged" })).toHaveAttribute(
			"href",
			"https://github.com/example/radic/pull/42",
		);
		expect(within(terminatedCard!).getByRole("link", { name: "PR #41 open" })).toHaveAttribute(
			"href",
			"https://github.com/example/radic/pull/41",
		);
		expect(within(terminatedCard!).getByRole("button", { name: "Copy branch ao/dead-worker" })).toBeInTheDocument();
		const divider = terminatedCard!.querySelector("div.border-t.border-border");
		expect(divider).not.toBeNull();
		const mergedPrLink = within(terminatedCard!).getByRole("link", { name: "PR #42 merged" });
		expect(divider!.compareDocumentPosition(mergedPrLink) & Node.DOCUMENT_POSITION_PRECEDING).not.toBe(0);
		expect(
			screen.getByText("ao/dead-worker").compareDocumentPosition(divider!) & Node.DOCUMENT_POSITION_FOLLOWING,
		).not.toBe(0);
		expect(screen.getByRole("button", { name: "Restore dead worker" })).toBeInTheDocument();

		expect(screen.queryByRole("group", { name: "Archive layout" })).not.toBeInTheDocument();
	});

	it("hides PR progress for a live merged session that has not actually terminated", () => {
		const liveMergedSession = terminatedSession({
			id: "s-live-merged",
			title: "live merged worker",
			status: "merged",
			isTerminated: false,
			kanbanColumn: "ready",
		});
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([liveMergedSession])],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const card = screen.getByText("live merged worker").closest<HTMLElement>("[data-testid='board-session-card']");
		expect(card).not.toBeNull();
		expect(within(card!).queryByTestId("session-pr-progress")).not.toBeInTheDocument();
	});

	it("keeps archive cards mounted after collapse so reopen does not remount them", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([terminatedSession()])],
			isError: false,
			isSuccess: true,
		});
		renderBoard("p1");

		const archiveButton = screen.getByRole("button", { name: /archive/i });
		const archive = await expandArchive();
		const card = within(archive).getByText("dead worker");

		await userEvent.click(archiveButton);
		expect(archiveButton).toHaveAttribute("aria-expanded", "false");
		expect(archive).toBeInTheDocument();
		expect(archive).toHaveAttribute("aria-hidden", "true");
		expect(archive).toHaveAttribute("inert");
		expect(archive).toHaveClass("pointer-events-none");
		expect(screen.queryByRole("list", { name: "Archived sessions" })).not.toBeInTheDocument();

		await userEvent.click(archiveButton);
		expect(archiveButton).toHaveAttribute("aria-expanded", "true");
		const reopened = screen.getByRole("list", { name: "Archived sessions" });
		expect(reopened).toBe(archive);
		expect(within(reopened).getByText("dead worker")).toBe(card);
		expect(reopened).not.toHaveAttribute("inert");
		expect(reopened).not.toHaveClass("pointer-events-none");
	});

	it("renders archived sessions as a grid even when rows were previously saved", async () => {
		window.localStorage.setItem("ao.board.archive.layout", "rows");
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([terminatedSession()])],
			isError: false,
			isSuccess: true,
		});
		renderBoard("p1");

		await expandArchive();
		expect(screen.queryByRole("group", { name: "Archive layout" })).not.toBeInTheDocument();
		const archive = screen.getByRole("list", { name: "Archived sessions" });
		expect(archive).toHaveClass("grid");
		const restore = screen.getByRole("button", { name: "Restore dead worker" });
		expect(restore.closest("[role='listitem']")).toContainElement(screen.getByText("Terminated"));
		expect(screen.queryByRole("button", { name: "Open dead worker" })).not.toBeInTheDocument();
	});

	it("restores a terminated session, refreshes workspace data, and opens the restored terminal", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([terminatedSession()])],
			isError: false,
			isSuccess: true,
		});
		const queryClient = renderBoard("p1");
		const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

		await expandArchive();
		await userEvent.click(screen.getByRole("button", { name: "Restore dead worker" }));

		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/restore", {
				params: { path: { sessionId: "s-dead" } },
			}),
		);
		expect(invalidate).toHaveBeenCalledWith({ queryKey: ["workspaces"] });
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "p1", sessionId: "s-dead" },
		});
	});

	it("shows a toast when restore falls back to a saved-prompt conversation", async () => {
		postMock.mockResolvedValueOnce({ data: { restoreMode: "saved_prompt" } });
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([terminatedSession()])],
			isError: false,
			isSuccess: true,
		});
		renderBoard("p1");

		await expandArchive();
		await userEvent.click(screen.getByRole("button", { name: "Restore dead worker" }));

		await waitFor(() =>
			expect(notificationShowMock).toHaveBeenCalledWith(
				expect.objectContaining({
					title: "Started from saved prompt",
					body: expect.stringContaining("started a new conversation from the saved prompt"),
				}),
			),
		);
	});

	it("does not show a fallback toast when restore uses native resume", async () => {
		postMock.mockResolvedValueOnce({ data: { restoreMode: "native" } });
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([terminatedSession()])],
			isError: false,
			isSuccess: true,
		});
		renderBoard("p1");

		await expandArchive();
		await userEvent.click(screen.getByRole("button", { name: "Restore dead worker" }));

		await waitFor(() => expect(postMock).toHaveBeenCalled());
		expect(notificationShowMock).not.toHaveBeenCalled();
	});

	it("keeps restore actions visible and disables siblings while one session is restoring", async () => {
		let finishRestore: ((value: { data: Record<string, never> }) => void) | undefined;
		postMock.mockReturnValueOnce(
			new Promise((resolve) => {
				finishRestore = resolve;
			}),
		);
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([terminatedSession(), terminatedSession({ id: "s-other", title: "other worker" })])],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		await expandArchive();
		await userEvent.click(screen.getByRole("button", { name: "Restore dead worker" }));

		const restoringButton = screen.getByRole("button", { name: "Restore dead worker" });
		const otherButton = screen.getByRole("button", { name: "Restore other worker" });
		expect(restoringButton.querySelector("svg")).toHaveClass("animate-spin");
		expect(otherButton).toBeDisabled();
		expect(otherButton).not.toHaveClass("opacity-0");

		await act(async () => {
			finishRestore?.({ data: {} });
		});
	});

	it("opens the restore-unavailable dialog when a session is not resumable", async () => {
		postMock.mockResolvedValueOnce({ error: { code: "SESSION_NOT_RESUMABLE" } });
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([terminatedSession()])],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		await expandArchive();
		await userEvent.click(screen.getByRole("button", { name: "Restore dead worker" }));

		expect(await screen.findByText("Session can no longer be restored")).toBeInTheDocument();
	});

	it("shows an archive row error when restore fails", async () => {
		postMock.mockResolvedValueOnce({ error: { code: "RESTORE_FAILED", message: "boom" } });
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([terminatedSession()])],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		await expandArchive();
		await userEvent.click(screen.getByRole("button", { name: "Restore dead worker" }));

		expect(await screen.findByText("Unable to restore session")).toBeInTheDocument();
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("does not navigate when the static archive card is clicked", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([terminatedSession()])],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		await expandArchive();
		await userEvent.click(screen.getByText("dead worker"));

		expect(postMock).not.toHaveBeenCalled();
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("ignores restore completion after navigating to another project board", async () => {
		let finishRestore: ((value: { data: Record<string, never> }) => void) | undefined;
		postMock.mockReturnValueOnce(
			new Promise((resolve) => {
				finishRestore = resolve;
			}),
		);
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([terminatedSession()]),
				{
					id: "p2",
					name: "other",
					path: "/tmp/other",
					sessions: [],
				},
			],
			isError: false,
			isSuccess: true,
		});
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const view = renderBoardWithClient(queryClient, "p1");

		await expandArchive();
		await userEvent.click(screen.getByRole("button", { name: "Restore dead worker" }));

		view.rerender(
			<QueryClientProvider client={queryClient}>
				<TooltipProvider>
					<SessionsBoard projectId="p2" />
				</TooltipProvider>
			</QueryClientProvider>,
		);
		await act(async () => {
			finishRestore?.({ data: {} });
		});

		expect(navigateMock).not.toHaveBeenCalled();
		expect(screen.queryByText("Session can no longer be restored")).not.toBeInTheDocument();
	});

	it("ignores restore-unavailable completion after navigating to another project board", async () => {
		let finishRestore: ((value: { error: { code: string } }) => void) | undefined;
		postMock.mockReturnValueOnce(
			new Promise((resolve) => {
				finishRestore = resolve;
			}),
		);
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([terminatedSession()]),
				{
					id: "p2",
					name: "other",
					path: "/tmp/other",
					sessions: [],
				},
			],
			isError: false,
			isSuccess: true,
		});
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const view = renderBoardWithClient(queryClient, "p1");

		await expandArchive();
		await userEvent.click(screen.getByRole("button", { name: "Restore dead worker" }));

		view.rerender(
			<QueryClientProvider client={queryClient}>
				<TooltipProvider>
					<SessionsBoard projectId="p2" />
				</TooltipProvider>
			</QueryClientProvider>,
		);
		await act(async () => {
			finishRestore?.({ error: { code: "SESSION_NOT_RESUMABLE" } });
		});

		expect(navigateMock).not.toHaveBeenCalled();
		expect(screen.queryByText("Session can no longer be restored")).not.toBeInTheDocument();
	});

	it("keeps a live merged session in the ready lane and opens its card without restore", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([boardSession({ id: "s-merged", title: "merged worker", status: "merged" })])],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const readyLane = screen.getByRole("region", { name: "Ready sessions" });
		expect(within(readyLane).getByText("Ready")).toHaveClass("text-status-ready");
		expect(within(readyLane).getByText("merged worker")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /archive/i })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Restore merged worker" })).not.toBeInTheDocument();

		await userEvent.click(screen.getByText("merged worker"));

		expect(postMock).not.toHaveBeenCalled();
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "p1", sessionId: "s-merged" },
		});
	});

	it("groups lanes by the daemon's Kanban column, not by display status", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					// Both are "working" on the card; only the column decides the lane.
					boardSession({
						id: "s-validating",
						title: "validating worker",
						status: "working",
						kanbanColumn: "validating",
					}),
					boardSession({
						id: "s-building",
						title: "building worker",
						status: "working",
						kanbanColumn: "building",
					}),
					// Mergeable on the card, but no AO loop is turning it, so the
					// review-feedback loop is on a person's turn.
					boardSession({
						id: "s-needs-review",
						title: "in review worker",
						status: "mergeable",
						kanbanColumn: "needs_review",
					}),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const lane = (label: string) => screen.getByLabelText(label);
		expect(within(lane("Building sessions")).getByText("building worker")).toBeInTheDocument();
		expect(within(lane("Validating sessions")).getByText("validating worker")).toBeInTheDocument();
		expect(within(lane("In review sessions")).getByText("in review worker")).toBeInTheDocument();
		expect(within(lane("Ready sessions")).queryByText("in review worker")).toBeNull();
	});

	it("renders the daemon's display status on the card", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-ci",
						title: "ci worker",
						status: "ci_failed",
						kanbanColumn: "validating",
						displayStatus: "Fixing CI failures",
					}),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		expect(screen.getByText("Fixing CI failures")).toBeInTheDocument();
		expect(screen.queryByText("CI failed")).not.toBeInTheDocument();
	});

	it("highlights every user-attention status while leaving ordinary cards neutral", () => {
		const attentionStatuses = [
			"ci_failed",
			"changes_requested",
		] as const;
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					...attentionStatuses.map((status) =>
						boardSession({
							id: `s-${status}`,
							kanbanColumn: "building",
							status,
							title: `${status} worker`,
						}),
					),
					boardSession({ id: "s-idle", title: "idle worker", status: "idle" }),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		for (const status of attentionStatuses) {
			const card = screen
				.getByText(`${status} worker`)
				.closest('[data-testid="board-session-card"]');
			expect(card).toHaveClass(
				"animate-attention-card-pulse",
				"border-status-needs-you",
				"bg-[color-mix(in_srgb,var(--color-status-needs-you)_8%,var(--color-surface))]",
			);
		}

		const ordinaryCard = screen
			.getByText("idle worker")
			.closest('[data-testid="board-session-card"]');
		expect(ordinaryCard).toHaveClass("border-border", "bg-surface");
		expect(ordinaryCard).not.toHaveClass("animate-attention-card-pulse");
	});

	// Mixed-version upgrade: an older daemon sends no kanbanColumn at all. Cards
	// must stay in the lanes their status already put them in, not pile into the
	// leftmost one.
	it("keeps an older daemon's sessions in their status-implied lanes", () => {
		const legacy = (id: string, title: string, status: WorkspaceSession["status"]): WorkspaceSession => {
			const session = boardSession({ id, title, status });
			delete session.kanbanColumn;
			return session;
		};
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					legacy("s-ready", "legacy ready worker", "mergeable"),
					legacy("s-action", "legacy action worker", "changes_requested"),
					legacy("s-review", "legacy review worker", "review_pending"),
					legacy("s-working", "legacy working worker", "working"),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const lane = (label: string) => screen.getByLabelText(label);
		expect(within(lane("Building sessions")).getByText("legacy working worker")).toBeInTheDocument();
		expect(within(lane("Validating sessions")).getByText("legacy review worker")).toBeInTheDocument();
		expect(within(lane("In review sessions")).getByText("legacy action worker")).toBeInTheDocument();
		expect(within(lane("Ready sessions")).getByText("legacy ready worker")).toBeInTheDocument();
	});

	it("orders the lanes building, validating, in review, then ready", () => {
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([boardSession({ id: "s-one", title: "worker one", status: "idle" })])],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		expect(screen.getAllByTestId("board-column").map((column) => column.dataset.column)).toEqual([
			"building",
			"validating",
			"needs_review",
			"ready",
		]);
	});

	it("uses the shared minimal scrollbar styling for every Kanban lane", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({ id: "s-idle", title: "idle worker", status: "idle" }),
					boardSession({ id: "s-working", title: "working worker", status: "working" }),
					boardSession({ id: "s-action", title: "action worker", status: "needs_input" }),
					boardSession({ id: "s-review", title: "review worker", status: "review_pending" }),
					boardSession({ id: "s-ready", title: "ready worker", status: "mergeable" }),
					boardSession({ id: "s-merged", title: "merged worker", status: "merged" }),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const laneScrollers = screen
			.getAllByTestId("board-column")
			.flatMap((column) => Array.from(column.querySelectorAll<HTMLElement>(".overflow-y-auto")));
		expect(laneScrollers).toHaveLength(4);
		for (const scroller of laneScrollers) {
			expect(scroller).toHaveClass("board-scrollbar", "overflow-y-auto");
		}
	});

	it("archives a terminated merged runtime without duplicating it in the ready lane", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({ id: "s-live-merged", title: "live merged worker", status: "merged" }),
					terminatedSession({ id: "s-archived-merged", title: "archived merged worker", status: "merged" }),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const readyLane = screen.getByRole("region", { name: "Ready sessions" });
		expect(within(readyLane).getByText("live merged worker")).toBeInTheDocument();
		expect(within(readyLane).queryByText("archived merged worker")).not.toBeInTheDocument();

		await expandArchive();
		const archive = screen.getByRole("list", { name: "Archived sessions" });
		const archivedMergedCard = within(archive)
			.getByText("archived merged worker")
			.closest<HTMLElement>("[role='listitem']");
		expect(archivedMergedCard).not.toBeNull();
		expect(
			within(archivedMergedCard!).queryByRole("button", { name: "Open archived merged worker" }),
		).not.toBeInTheDocument();
		expect(
			within(archivedMergedCard!).queryByRole("button", { name: "Terminate archived merged worker" }),
		).not.toBeInTheDocument();
		expect(within(archivedMergedCard!).getByText("Merged").parentElement).toHaveAttribute(
			"data-kanban-column",
			"archive",
		);
		expect(within(archive).getByRole("button", { name: "Restore archived merged worker" })).toBeInTheDocument();
	});

	it("asks for confirmation when terminating an ordinary live session from its card", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([boardSession({ id: "s-idle", title: "idle worker", status: "idle" })])],
			isError: false,
			isSuccess: true,
		});
		renderBoard("p1");

		await userEvent.click(screen.getByRole("button", { name: "Terminate idle worker" }));

		expect(navigateMock).not.toHaveBeenCalled();
		expect(screen.getByRole("dialog", { name: "Terminate idle worker?" })).toBeInTheDocument();
	});

	it("terminates a live merged session from its card without opening the session", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([boardSession({ id: "s-merged", title: "merged worker", status: "merged" })])],
			isError: false,
			isSuccess: true,
		});
		renderBoard("p1");

		const terminateButton = screen.getByRole("button", { name: "Terminate merged worker" });
		expect(terminateButton).toHaveClass("opacity-100");
		expect(terminateButton).not.toHaveClass("opacity-0");
		await userEvent.click(terminateButton);
		expect(navigateMock).not.toHaveBeenCalled();
		const dialog = screen.getByRole("dialog", { name: "Terminate merged worker?" });
		await userEvent.click(within(dialog).getByRole("button", { name: "Yes, terminate session" }));

		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/kill", {
				params: { path: { sessionId: "s-merged" } },
			}),
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("keeps only the targeted card disabled while its termination is pending", async () => {
		let finishKill!: (value: { data: { ok: boolean; sessionId: string }; error: undefined }) => void;
		postMock.mockReturnValueOnce(
			new Promise((resolve) => {
				finishKill = resolve;
			}),
		);
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({ id: "s-one", title: "worker one", status: "working" }),
					boardSession({ id: "s-two", title: "worker two", status: "merged" }),
				]),
			],
			isError: false,
			isSuccess: true,
		});
		renderBoard("p1");

		await userEvent.click(screen.getByRole("button", { name: "Terminate worker one" }));
		await userEvent.click(
			within(screen.getByRole("dialog")).getByRole("button", { name: "Yes, terminate session" }),
		);

		expect(screen.getByRole("button", { name: "Killing worker one" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Killing worker one" })).toHaveClass("opacity-100");
		expect(screen.getByRole("button", { name: "Terminate worker two" })).toBeEnabled();
		expect(postMock).toHaveBeenCalledTimes(1);

		finishKill({ data: { ok: true, sessionId: "s-one" }, error: undefined });
		await waitFor(() => expect(screen.getByRole("button", { name: "Terminate worker one" })).toBeEnabled());
	});

	it("keeps the merged-card confirmation dismissed and surfaces termination failures", async () => {
		postMock.mockResolvedValueOnce({ error: { message: "runtime failed" }, response: { status: 500 } });
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([boardSession({ id: "s-merged", title: "merged worker", status: "merged" })])],
			isError: false,
			isSuccess: true,
		});
		renderBoard("p1");

		await userEvent.click(screen.getByRole("button", { name: "Terminate merged worker" }));
		await userEvent.click(
			within(screen.getByRole("dialog")).getByRole("button", { name: "Yes, terminate session" }),
		);

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(await screen.findByRole("alert")).toHaveTextContent("Failed to terminate session (500)");
		expect(screen.getByRole("button", { name: "Terminate merged worker" })).toBeEnabled();
	});

	it("shows a folder-missing banner when the project root no longer exists on disk", () => {
		workspaceQueryMock.mockReturnValue({
			data: [{ ...workspaceWithSessions([]), folderMissing: true }],
		});
		renderBoard("p1");
		expect(screen.getByText(appI18n.t("home.folderMissing"))).toBeInTheDocument();
	});

	it("does not show the folder-missing banner when the project folder exists", () => {
		workspaceQueryMock.mockReturnValue({
			data: [{ ...workspaceWithSessions([]), folderMissing: false }],
		});
		renderBoard("p1");
		expect(screen.queryByText(appI18n.t("home.folderMissing"))).not.toBeInTheDocument();
	});
});

function workspaceWithSessions(sessions: WorkspaceSession[]): WorkspaceSummary {
	return {
		id: "p1",
		name: "radic",
		path: "/tmp/radic",
		sessions,
	};
}

function boardSession(
	overrides: Pick<WorkspaceSession, "id" | "title" | "status"> & Partial<WorkspaceSession>,
): WorkspaceSession {
	return {
		workspaceId: "p1",
		workspaceName: "radic",
		provider: "claude-code",
		branch: `ao/${overrides.id}`,
		kanbanColumn: toKanbanColumn(undefined, overrides.status),
		updatedAt: "2026-01-01T00:00:00Z",
		prs: [],
		...overrides,
	};
}

function activeAgentSwitch(
	sessionId: string,
	overrides: Partial<NonNullable<WorkspaceSession["activeAgentSwitch"]>> = {},
): NonNullable<WorkspaceSession["activeAgentSwitch"]> {
	return {
		agentHandoffStatus: "received",
		fromHarness: "claude-code",
		id: `switch-${sessionId}`,
		state: "starting_target",
		targetHarness: "codex",
		...overrides,
	};
}

function terminatedSession(overrides: Partial<WorkspaceSession> = {}): WorkspaceSession {
	return {
		id: "s-dead",
		workspaceId: "p1",
		workspaceName: "radic",
		title: "dead worker",
		issueId: "github:INT-17",
		provider: "claude-code",
		kind: "worker",
		branch: "ao/dead-worker",
		status: "terminated",
		kanbanColumn: "archive",
		isTerminated: true,
		updatedAt: "2026-01-01T00:00:00Z",
		prs: [
			{
				url: "https://github.com/example/radic/pull/42",
				number: 42,
				state: "merged",
				ci: "passing",
				review: "approved",
				mergeability: "mergeable",
				reviewComments: false,
				updatedAt: "2026-01-01T00:00:00Z",
			},
		],
		...overrides,
	};
}
