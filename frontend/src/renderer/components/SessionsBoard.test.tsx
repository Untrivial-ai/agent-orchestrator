import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
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
			expect(screen.getByLabelText("#42 已打开")).toHaveTextContent("已打开");
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

	it("shows compact token usage on active and archived cards and hides empty totals", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({ id: "s-active", title: "active worker", status: "idle" }),
					boardSession({ id: "s-empty", title: "empty worker", status: "idle" }),
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
						sessionId: "s-active",
						totalTokens: 12_400,
						incomplete: false,
					},
				],
				[
					"s-empty",
					{
						sessionId: "s-empty",
						totalTokens: 0,
						incomplete: false,
					},
				],
				[
					"s-dead",
					{
						sessionId: "s-dead",
						totalTokens: 2_000,
						incomplete: true,
					},
				],
			]),
		});

		renderBoard("p1");

		const activeUsage = screen.getByText("12.4K tok");
		expect(activeUsage).toHaveAttribute("aria-label", "12,400 tokens");
		expect(screen.queryByText("0 tok")).not.toBeInTheDocument();
		expect(usageQueryMock).toHaveBeenCalledWith("p1");
		await userEvent.hover(activeUsage);
		expect((await screen.findAllByText("12,400 tokens")).length).toBeGreaterThan(0);

		const archive = await expandArchive();
		const archivedUsage = within(archive).getByText("2K tok");
		expect(archivedUsage).toHaveAttribute("aria-label", "2,000 tokens");
	});

	it("pulses the shared activity indicator on an actively working session card", () => {
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
		expect(working.querySelector('[aria-hidden="true"]')).toHaveClass("bg-status-working", "animate-status-pulse");
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
		expect(status.querySelector("span")).toHaveClass("animate-status-pulse");
		expect(within(card).queryByText("Exited")).not.toBeInTheDocument();
	});

	it("uses distinct card badge tones for idle, no signal, and draft PR sessions", () => {
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

		expect(within(idleCard).getByText("Idle").parentElement).toHaveClass("text-status-idle");
		expect(within(noSignalCard).getByText("No signal").parentElement).toHaveClass("text-status-unknown");
		expect(within(draftCard).getByText("Draft PR").parentElement).toHaveClass("text-status-in-review");
	});

	it("places an exited live session in Needs you with an Exited badge", () => {
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

		const needsYouColumn = screen.getByText("Needs you").closest("section") as HTMLElement;
		expect(needsYouColumn.firstElementChild).toHaveClass("h-12");
		expect(within(needsYouColumn).getByText("agent-exited-task")).toBeInTheDocument();
		expect(within(needsYouColumn).getByText("Exited").parentElement).toHaveClass("text-status-exited");
	});

	it("renders an idle-first work lane with a separate lower working section", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-active",
						title: "active-task",
						status: "working",
						activity: { state: "active", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
					boardSession({
						id: "s-idle-1",
						title: "idle-no-pr-task",
						status: "idle",
						activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
					boardSession({
						id: "s-idle-2",
						title: "second-idle-task",
						status: "idle",
						activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
					boardSession({
						id: "s-review",
						title: "idle-with-pr-task",
						status: "pr_open",
						activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
						prs: [
							{
								number: 7,
								url: "https://github.com/acme/radic/pull/7",
								state: "open",
								ci: "unknown",
								review: "none",
								mergeability: "unknown",
								reviewComments: false,
								updatedAt: "2026-01-01T00:00:00Z",
							},
						],
					}),
				]),
			],
			isError: false,
		});

		renderBoard("p1");

		const workLane = screen.getByRole("region", { name: "Idle / Working sessions" });
		const idleRegion = within(workLane).getByRole("region", { name: "Idle sessions" });
		const workingRegion = within(workLane).getByRole("region", { name: "Working sessions" });
		const reviewRegion = screen.getByRole("region", { name: "In review sessions" });
		const workSummary = within(workLane).getByRole("group", { name: "Idle / Working lane summary" });

		expect(within(workSummary).getByText("Idle").querySelector("span")).toHaveClass("bg-status-idle");
		expect(within(workSummary).getByText("Working").querySelector("span")).toHaveClass("bg-status-working");
		expect(workSummary).toHaveClass("font-mono", "text-2xs", "uppercase");
		expect(workSummary.parentElement).toHaveClass("h-12");
		expect(workingRegion.firstElementChild).toHaveClass("py-2.5");
		expect(within(workLane).getByLabelText("2 idle sessions")).toHaveTextContent("2");
		expect(within(workLane).getByLabelText("1 working session")).toHaveTextContent("1");
		expect(screen.queryByRole("button", { name: /idle sessions/i })).not.toBeInTheDocument();
		expect(workLane.querySelectorAll(".overflow-y-auto")).toHaveLength(1);
		expect(idleRegion).toHaveClass("flex-none");
		expect(idleRegion).not.toHaveClass("overflow-y-auto", "board-scrollbar");
		expect(workingRegion).toHaveClass("flex-1", "border-t");
		expect(workingRegion.className).not.toContain("rounded-t");
		expect(workingRegion).not.toHaveClass("overflow-y-auto", "board-scrollbar");
		expect(within(idleRegion).getByText("idle-no-pr-task")).toBeInTheDocument();
		expect(within(idleRegion).getByText("second-idle-task")).toBeInTheDocument();
		expect(within(workingRegion).getByText("active-task")).toBeInTheDocument();
		expect(within(reviewRegion).getByText("idle-with-pr-task")).toBeInTheDocument();
		expect(within(workLane).queryByText("idle-with-pr-task")).not.toBeInTheDocument();

		const idleCard = screen.getByText("idle-no-pr-task").closest('[data-testid="board-session-card"]') as HTMLElement;
		const badge = within(idleCard).getByText("Idle").parentElement;
		expect(badge).toHaveClass("text-status-idle");
		expect(badge).not.toHaveClass("text-status-working");
	});

	it("uses one shared scrollbar when a short idle section sits above many working sessions", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-idle",
						title: "single-idle-task",
						status: "idle",
						activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
					...Array.from({ length: 7 }, (_, index) =>
						boardSession({
							id: `s-working-${index + 1}`,
							title: `working-task-${index + 1}`,
							status: "working",
							activity: { state: "active", lastActivityAt: "2026-01-01T00:00:00Z" },
						}),
					),
				]),
			],
			isError: false,
		});

		renderBoard("p1");

		const workLane = screen.getByRole("region", { name: "Idle / Working sessions" });
		const idleRegion = within(workLane).getByRole("region", { name: "Idle sessions" });
		const workingRegion = within(workLane).getByRole("region", { name: "Working sessions" });
		const laneScrollers = Array.from(workLane.querySelectorAll<HTMLElement>(".overflow-y-auto"));

		expect(laneScrollers).toHaveLength(1);
		expect(laneScrollers[0]).toHaveClass("board-scrollbar", "overflow-y-auto");
		expect(laneScrollers[0]).toContainElement(idleRegion);
		expect(laneScrollers[0]).toContainElement(workingRegion);
		expect(idleRegion).toHaveClass("flex-none");
		expect(workingRegion).toHaveClass("flex-1");
		expect(idleRegion.querySelector(".overflow-y-auto")).not.toBeInTheDocument();
		expect(workingRegion.querySelector(".overflow-y-auto")).not.toBeInTheDocument();
		expect(within(idleRegion).getByText("single-idle-task")).toBeInTheDocument();
		expect(within(workingRegion).getAllByTestId("board-session-card")).toHaveLength(7);
	});

	it("lets idle sessions fill the lane when no working sessions exist", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-idle",
						title: "idle-task",
						status: "idle",
						activity: { state: "idle", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
				]),
			],
			isError: false,
		});

		renderBoard("p1");

		const workLane = screen.getByRole("region", { name: "Idle / Working sessions" });
		const idleRegion = within(workLane).getByRole("region", { name: "Idle sessions" });
		expect(within(workLane).getByLabelText("1 idle session")).toHaveTextContent("1");
		expect(within(workLane).getByLabelText("0 working sessions")).toHaveTextContent("0");
		expect(idleRegion).toHaveClass("flex-1");
		expect(within(idleRegion).getByText("idle-task")).toBeInTheDocument();
		expect(within(workLane).queryByRole("region", { name: "Working sessions" })).not.toBeInTheDocument();
	});

	it("lets working sessions fill the lane when no idle sessions exist", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({
						id: "s-working-1",
						title: "first-working-task",
						status: "working",
						activity: { state: "active", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
					boardSession({
						id: "s-working-2",
						title: "second-working-task",
						status: "working",
						activity: { state: "active", lastActivityAt: "2026-01-01T00:00:00Z" },
					}),
				]),
			],
			isError: false,
		});

		renderBoard("p1");

		const workLane = screen.getByRole("region", { name: "Idle / Working sessions" });
		const workingRegion = within(workLane).getByRole("region", { name: "Working sessions" });
		expect(within(workLane).getByLabelText("0 idle sessions")).toHaveTextContent("0");
		expect(within(workLane).getByLabelText("2 working sessions")).toHaveTextContent("2");
		expect(within(workLane).queryByRole("region", { name: "Idle sessions" })).not.toBeInTheDocument();
		expect(workingRegion).toHaveClass("flex-1");
		expect(workingRegion).not.toHaveClass("flex-[2]", "border-t");
		expect(workLane.querySelectorAll(".overflow-y-auto")).toHaveLength(1);
		expect(within(workingRegion).getByText("first-working-task")).toBeInTheDocument();
		expect(within(workingRegion).getByText("second-working-task")).toBeInTheDocument();
	});

	it("keeps idle and working sections visible when navigating between project boards", () => {
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

		const p1Lane = screen.getByRole("region", { name: "Idle / Working sessions" });
		expect(within(p1Lane).getByRole("region", { name: "Idle sessions" })).toHaveTextContent("p1 idle");
		expect(within(p1Lane).getByRole("region", { name: "Working sessions" })).toHaveTextContent("p1 active");

		view.rerender(
			<QueryClientProvider client={queryClient}>
				<TooltipProvider>
					<SessionsBoard projectId="p2" />
				</TooltipProvider>
			</QueryClientProvider>,
		);

		const p2Lane = screen.getByRole("region", { name: "Idle / Working sessions" });
		expect(screen.queryByText("p1 idle")).not.toBeInTheDocument();
		expect(within(p2Lane).getByRole("region", { name: "Idle sessions" })).toHaveTextContent("p2 idle");
		expect(within(p2Lane).getByRole("region", { name: "Working sessions" })).toHaveTextContent("p2 active");
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
		// Agent shown as its brand logo with an accessible name (not a text label).
		expect(within(terminatedCard!).getByRole("img", { name: "claude-code" })).toBeInTheDocument();
		expect(screen.getByText("ao/dead-worker")).toBeInTheDocument();
		expect(screen.getByText("github:INT-17")).toBeInTheDocument();
		const prStatus = screen.getByLabelText("#42 merged");
		expect(prStatus).toHaveTextContent("PR#42merged");
		expect(within(prStatus).getByText("merged")).toHaveClass("text-status-merged");
		const openPrStatus = screen.getByLabelText("#41 open");
		expect(openPrStatus.parentElement).toBe(prStatus.parentElement);
		expect(prStatus.parentElement).toHaveClass("flex-wrap");
		expect(within(terminatedCard!).getByRole("link", { name: "#42" })).toHaveAttribute(
			"href",
			"https://github.com/example/radic/pull/42",
		);
		expect(within(terminatedCard!).getByRole("button", { name: "Copy branch ao/dead-worker" })).toBeInTheDocument();
		const divider = terminatedCard!.querySelector("div[aria-hidden='true'].h-px.bg-border");
		expect(divider).not.toBeNull();
		expect(divider!.compareDocumentPosition(prStatus) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
		expect(
			screen.getByText("ao/dead-worker").compareDocumentPosition(divider!) & Node.DOCUMENT_POSITION_FOLLOWING,
		).not.toBe(0);
		expect(screen.getByRole("button", { name: "Restore dead worker" })).toBeInTheDocument();

		expect(screen.queryByRole("group", { name: "Archive layout" })).not.toBeInTheDocument();
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

	it("shows a merged-only lane and opens its card without showing restore", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [workspaceWithSessions([boardSession({ id: "s-merged", title: "merged worker", status: "merged" })])],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const mergeLane = screen.getByRole("region", { name: "Ready to merge / Merged sessions" });
		const mergedRegion = within(mergeLane).getByRole("region", { name: "Merged sessions" });
		const mergeSummary = within(mergeLane).getByRole("group", { name: "Ready to merge / Merged lane summary" });
		expect(within(mergeSummary).getByText("Ready to merge").querySelector("span")).toHaveClass("bg-status-ready");
		expect(within(mergeSummary).getByText("Merged").querySelector("span")).toHaveClass("bg-status-merged");
		expect(within(mergeLane).getByLabelText("0 ready to merge sessions")).toHaveTextContent("0");
		expect(within(mergeLane).getByLabelText("1 merged session")).toHaveTextContent("1");
		expect(within(mergeLane).queryByRole("region", { name: "Ready to merge sessions" })).not.toBeInTheDocument();
		expect(mergedRegion).toHaveClass("flex-1");
		expect(within(mergedRegion).getByText("merged worker")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /archive/i })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Restore merged worker" })).not.toBeInTheDocument();

		await userEvent.click(screen.getByText("merged worker"));

		expect(postMock).not.toHaveBeenCalled();
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "p1", sessionId: "s-merged" },
		});
	});

	it("splits ready and merged sessions into upper and lower regions", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					boardSession({ id: "s-ready", title: "ready worker", status: "mergeable" }),
					boardSession({ id: "s-merged", title: "merged worker", status: "merged" }),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderBoard("p1");

		const mergeLane = screen.getByRole("region", { name: "Ready to merge / Merged sessions" });
		const readyRegion = within(mergeLane).getByRole("region", { name: "Ready to merge sessions" });
		const mergedRegion = within(mergeLane).getByRole("region", { name: "Merged sessions" });
		expect(within(mergeLane).getByLabelText("1 ready to merge session")).toHaveTextContent("1");
		expect(within(mergeLane).getByLabelText("1 merged session")).toHaveTextContent("1");
		expect(mergeLane.querySelectorAll(".overflow-y-auto")).toHaveLength(1);
		expect(readyRegion).toHaveClass("flex-none");
		expect(readyRegion).not.toHaveClass("overflow-y-auto", "board-scrollbar");
		expect(mergedRegion).toHaveClass("flex-1", "border-t");
		expect(mergedRegion.className).not.toContain("rounded-t");
		expect(mergedRegion).not.toHaveClass("overflow-y-auto", "board-scrollbar");
		expect(within(readyRegion).getByText("ready worker")).toBeInTheDocument();
		expect(within(mergedRegion).getByText("merged worker")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /archive/i })).not.toBeInTheDocument();
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

	it("archives a terminated merged runtime without duplicating it in the merged lane", async () => {
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

		const mergedRegion = screen.getByRole("region", { name: "Merged sessions" });
		expect(within(mergedRegion).getByText("live merged worker")).toBeInTheDocument();
		expect(within(mergedRegion).queryByText("archived merged worker")).not.toBeInTheDocument();

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
		expect(within(archivedMergedCard!).getByText("Merged").parentElement).toHaveClass("text-status-merged");
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
