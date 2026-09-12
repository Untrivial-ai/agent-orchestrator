import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import {
	CLOUD_PROJECT_KIND,
	type SessionActivityState,
	type WorkspaceSession,
	type WorkspaceSummary,
} from "../types/workspace";
import { ShellTopbar, TopbarKillButton } from "./ShellTopbar";
import { TooltipProvider } from "./ui/tooltip";

const { navigateMock, onKilledMock, paramsMock, postMock, spawnMock, useWorkspaceQueryMock } = vi.hoisted(() => ({
	navigateMock: vi.fn(),
	onKilledMock: vi.fn(),
	paramsMock: { projectId: undefined as string | undefined, sessionId: undefined as string | undefined },
	postMock: vi.fn(),
	spawnMock: vi.fn(),
	useWorkspaceQueryMock: vi.fn(),
}));

vi.mock("@tanstack/react-router", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@tanstack/react-router")>();
	return {
		...actual,
		useNavigate: () => navigateMock,
		useParams: () => paramsMock,
	};
});

vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceScope: () => {
		const query = useWorkspaceQueryMock();
		const project = query.data?.find((workspace: WorkspaceSummary) => workspace.id === paramsMock.projectId);
		const session = query.data
			?.flatMap((workspace: WorkspaceSummary) => workspace.sessions)
			.find((candidate: WorkspaceSession) => candidate.id === paramsMock.sessionId);
		return {
			...query,
			data: {
				project,
				session,
				orchestrator: project?.sessions.find((candidate: WorkspaceSession) => candidate.kind === "orchestrator"),
			},
		};
	},
	workspaceQueryKey: ["workspaces"],
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		POST: postMock,
	},
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		if (typeof error === "object" && error !== null && "message" in error) {
			return String((error as { message: unknown }).message);
		}
		return fallback;
	},
}));

vi.mock("../lib/spawn-orchestrator", () => ({ spawnOrchestrator: spawnMock }));
vi.mock("../lib/telemetry", () => ({
	addRendererExceptionStep: vi.fn(),
	captureRendererEvent: vi.fn(),
	captureRendererException: vi.fn(),
}));
vi.mock("./NewTaskDialog", () => ({ NewTaskDialog: () => null }));
vi.mock("./NotificationCenter", () => ({
	NotificationCenter: () => <button aria-label="Notifications" type="button" />,
}));

const worker: WorkspaceSession = {
	id: "sess-1",
	workspaceId: "proj-1",
	workspaceName: "my-app",
	title: "do the thing",
	provider: "claude-code",
	kind: "worker",
	branch: "ao/sess-1",
	status: "working",
	updatedAt: "2026-06-10T00:00:00Z",
	prs: [],
};

const secondWorker: WorkspaceSession = {
	...worker,
	id: "sess-2",
	title: "do the other thing",
	branch: "ao/sess-2",
};

const orchestrator: WorkspaceSession = {
	id: "orch-1",
	workspaceId: "proj-1",
	workspaceName: "my-app",
	title: "orchestrator",
	provider: "claude-code",
	kind: "orchestrator",
	branch: "main",
	status: "working",
	updatedAt: "2026-06-10T00:00:00Z",
	prs: [],
};

function sessionWith(overrides: Partial<WorkspaceSession> = {}): WorkspaceSession {
	return {
		...worker,
		activity: { state: "active", lastActivityAt: "2026-06-10T00:00:00Z" },
		...overrides,
	};
}

function activeAgentSwitch(
	overrides: Partial<NonNullable<WorkspaceSession["activeAgentSwitch"]>> = {},
): NonNullable<WorkspaceSession["activeAgentSwitch"]> {
	return {
		agentHandoffStatus: "received",
		fromHarness: "claude-code",
		id: "switch-1",
		state: "starting_target",
		targetHarness: "codex",
		...overrides,
	};
}

function renderTopbar(session: WorkspaceSession, embedded = false, sessionAction?: ReactNode) {
	return renderTopbarSessions([session], session.id, embedded, sessionAction);
}

function renderTopbarSessions(
	sessions: WorkspaceSession[],
	sessionId: string,
	embedded = false,
	sessionAction?: ReactNode,
	projectKind?: WorkspaceSummary["kind"],
) {
	const data: WorkspaceSummary[] = [
		{
			id: sessions[0].workspaceId,
			name: sessions[0].workspaceName,
			path: "/repo/my-app",
			orchestratorAgent: "claude-code",
			kind: projectKind,
			sessions,
		},
	];
	useWorkspaceQueryMock.mockReturnValue({ data, isError: false, isLoading: false });
	paramsMock.projectId = sessions[0].workspaceId;
	paramsMock.sessionId = sessionId;
	const queryClient = new QueryClient();
	const topbar = () => (
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<ShellTopbar embedded={embedded} sessionAction={sessionAction} />
			</TooltipProvider>
		</QueryClientProvider>
	);
	const result = render(topbar());
	return { ...result, queryClient, rerenderTopbar: () => result.rerender(topbar()) };
}

function renderKill(session: WorkspaceSession = worker, orchestratorId?: string) {
	const queryClient = new QueryClient({
		defaultOptions: {
			queries: { retry: false },
			mutations: { retry: false },
		},
	});
	const killButton = (currentSession: WorkspaceSession, currentOrchestratorId?: string) => (
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<TopbarKillButton
					session={currentSession}
					orchestratorId={currentOrchestratorId}
					onKilled={onKilledMock}
				/>
			</TooltipProvider>
		</QueryClientProvider>
	);
	const result = render(killButton(session, orchestratorId));
	return {
		...result,
		queryClient,
		rerenderKill: (nextSession: WorkspaceSession, nextOrchestratorId?: string) =>
			result.rerender(killButton(nextSession, nextOrchestratorId)),
	};
}

async function clickKillDialogConfirm() {
	const dialog = await screen.findByRole("dialog", { name: "Terminate do the thing?" });
	await userEvent.click(within(dialog).getByRole("button", { name: "Yes, terminate session" }));
}

beforeEach(() => {
	navigateMock.mockReset();
	onKilledMock.mockReset();
	paramsMock.projectId = undefined;
	paramsMock.sessionId = undefined;
	postMock.mockReset();
	postMock.mockResolvedValue({ data: { ok: true, sessionId: "sess-1" }, error: undefined });
	useWorkspaceQueryMock.mockReset();
	useWorkspaceQueryMock.mockReturnValue({ data: [], isError: false, isLoading: false });
	useUiStore.setState({ inspectorSessions: {}, settingsModal: null });
});

describe("ShellTopbar status pill", () => {
	it("matches the session action edge inset to the toolbar spacing", () => {
		renderTopbar(sessionWith());

		const header = screen.getByTestId("workspace-topbar-actions").closest("header");
		expect(header).toHaveClass("pr-2");
		expect(header).not.toHaveClass("pr-4");
	});

	it("shows the worker session name and activity in the full topbar identity", () => {
		renderTopbar(sessionWith());

		const identity = screen.getByTestId("session-topbar-identity");
		expect(identity.textContent).toContain("do the thing");
		expect(identity.textContent).toContain("Working");
		expect(identity.textContent).not.toContain("ao/sess-1");
		expect(identity.querySelector(".workspace-topbar__identity-separator")).not.toBeNull();
	});

	it("shows project identity and activity without redundant Orchestrator text", () => {
		renderTopbar(
			sessionWith({
				...orchestrator,
				activity: { state: "idle", lastActivityAt: "2026-06-10T00:00:00Z" },
			}),
		);

		const identity = screen.getByTestId("session-topbar-identity");
		expect(identity.textContent).toContain("my-app");
		expect(identity.textContent).toContain("Idle");
		expect(identity.textContent).not.toContain("Orchestrator");
		expect(identity.querySelector(".lucide-folder")).not.toBeNull();
	});

	// The branch belongs to detail surfaces, not the top bar: an orchestrator's
	// identity stays the project crumb plus its activity, with its own controls
	// intact (#3874, regressed by the badge #4252 added beside these actions).
	it("keeps the worktree branch out of the orchestrator identity and actions", () => {
		renderTopbar(sessionWith({ ...orchestrator, branch: "ao/orch-root" }));

		const identity = screen.getByTestId("session-topbar-identity");
		expect(identity.textContent).toContain("my-app");
		expect(identity.textContent).toContain("Working");
		expect(screen.queryByText("ao/orch-root")).toBeNull();
		expect(screen.getByRole("button", { name: "Open Kanban" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "New task" })).toBeInTheDocument();
	});

	it("renders only session actions when embedded in the terminal bar", () => {
		renderTopbar(
			sessionWith(),
			true,
			<>
				<button type="button">New terminal</button>
				<button type="button">Switch agent</button>
				<button type="button">Switch to chat UI</button>
			</>,
		);

		expect(screen.queryByText("ao/sess-1")).toBeNull();
		expect(screen.queryByText("Working")).toBeNull();
		const localActions = screen.getByTestId("session-local-actions");
		expect(localActions).toHaveClass("gap-1");
		expect(localActions).not.toHaveClass("gap-px", "mr-0.5");
		expect(localActions.contains(screen.getByRole("button", { name: "New terminal" }))).toBe(true);
		expect(localActions.contains(screen.getByRole("button", { name: "Switch agent" }))).toBe(true);
		expect(localActions.contains(screen.getByRole("button", { name: "Switch to chat UI" }))).toBe(true);
		expect(localActions.contains(screen.getByRole("button", { name: "Kill session" }))).toBe(true);
		expect(localActions.contains(screen.getByRole("button", { name: "Open orchestrator" }))).toBe(false);
	});

	it("marks embedded session actions compact when requested", () => {
		render(
			<QueryClientProvider client={new QueryClient()}>
				<TooltipProvider>
					<ShellTopbar compactActions embedded sessionAction={<button type="button">New terminal</button>} />
				</TooltipProvider>
			</QueryClientProvider>,
		);

		expect(screen.getByTestId("workspace-topbar-actions")).toHaveAttribute("data-compact-actions", "true");
	});

	it.each([
		["active", "Working"],
		["idle", "Idle"],
		["waiting_input", "Input Needed"],
		["exited", "Exited"],
	] as const)("renders %s activity as %s", (state: SessionActivityState, label) => {
		renderTopbar(sessionWith({ activity: { state, lastActivityAt: "2026-06-10T00:00:00Z" } }));

		expect(screen.getByText(label)).toBeInTheDocument();
	});

	it.each([
		["ci_failed", "idle", "Idle", "CI failed"],
		["mergeable", "active", "Working", "Ready"],
		["merged", "exited", "Exited", "Done"],
		["changes_requested", "waiting_input", "Input Needed", "Needs input"],
	] as const)("ignores derived %s topbar status in favor of activity", (status, state, label, hidden) => {
		renderTopbar(
			sessionWith({
				status,
				activity: { state, lastActivityAt: "2026-06-10T00:00:00Z" },
			}),
		);

		expect(screen.getByText(label)).toBeInTheDocument();
		expect(screen.queryByText(hidden)).not.toBeInTheDocument();
	});

	it("uses a compact unknown state when activity is missing or unknown", () => {
		const first = renderTopbar(sessionWith({ activity: undefined }));
		expect(screen.getByText("Unknown")).toBeInTheDocument();

		first.unmount();
		renderTopbar(sessionWith({ activity: { state: "unknown", lastActivityAt: "" } }));
		expect(screen.getByText("Unknown")).toBeInTheDocument();
	});

	it("does not synthesize branch text for branchless sessions", () => {
		renderTopbar(sessionWith({ branch: undefined }));

		expect(screen.queryByText("session/sess-1")).not.toBeInTheDocument();
		expect(screen.getByText("Working")).toBeInTheDocument();
	});

	it("shows switch progress instead of the exited source in the status pill", () => {
		renderTopbar(sessionWith({
			status: "exited",
			activity: {
				state: "exited",
				lastActivityAt: "2026-06-10T00:00:00Z",
			},
			activeAgentSwitch: activeAgentSwitch(),
		}));

		const pill = screen.getByText("Switching to Codex").closest("span") as HTMLElement;
		expect(pill).toHaveStyle({ color: "var(--color-status-working)" });
		expect(pill.querySelector("span")).toHaveClass("animate-status-pulse");
		expect(screen.queryByText("Exited")).not.toBeInTheDocument();
	});
});

describe("ShellTopbar orchestrator actions", () => {
	it("owns the responsive action container on the full board topbar", () => {
		renderTopbarSessions([orchestrator], "");

		const actions = screen.getByTestId("workspace-topbar-actions");
		expect(actions.closest("header")).toHaveClass("workspace-topbar-container");
	});

	it.each([
		["active", "Working", "bg-status-working", true],
		["waiting_input", "Input Needed", "bg-status-needs-you", false],
	] as const)("shows %s orchestrator activity on the project board", (state, label, tone, pulses) => {
		renderTopbarSessions(
			[
				{
					...orchestrator,
					activity: { state, lastActivityAt: "2026-06-10T00:00:00Z" },
				},
			],
			"",
		);

		const button = screen.getByRole("button", { name: `Orchestrator, ${label}` });
		const indicator = button.querySelector("span.size-dot-sm") as HTMLElement;
		expect(indicator).toHaveAttribute("aria-hidden", "true");
		expect(indicator).toHaveClass(tone);
		expect(indicator).toHaveClass(pulses ? "animate-status-pulse" : "size-dot-sm");
		if (!pulses) expect(indicator).not.toHaveClass("animate-status-pulse");
	});

	it("shows a clear Kanban button on embedded orchestrator sessions", async () => {
		renderTopbar(orchestrator, true);

		const kanbanButton = screen.getByRole("button", { name: "Open Kanban" });
		expect(kanbanButton).toHaveTextContent("Open Kanban");
		expect(kanbanButton).toHaveClass("topbar-control--feature");
		expect(screen.queryByText("my-app")).not.toBeInTheDocument();
		await userEvent.click(kanbanButton);
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId",
			params: { projectId: "proj-1" },
		});
	});

	it("opens the board from the Kanban button on the full orchestrator topbar", async () => {
		renderTopbar(orchestrator);

		const kanbanButton = screen.getByRole("button", { name: "Open Kanban" });
		expect(kanbanButton).toHaveTextContent("Open Kanban");
		expect(kanbanButton).toHaveClass("topbar-control--feature");
		expect(screen.getByRole("button", { name: "New task" })).toHaveClass("bg-raised");
		await userEvent.click(kanbanButton);
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId",
			params: { projectId: "proj-1" },
		});
	});

	it("opens project settings instead of spawning when no orchestrator agent is configured", async () => {
		useWorkspaceQueryMock.mockReturnValue({
			data: [
				{
					id: "proj-1",
					name: "my-app",
					path: "/repo/my-app",
					sessions: [worker],
				},
			],
			isError: false,
			isLoading: false,
		});
		paramsMock.projectId = "proj-1";
		paramsMock.sessionId = "sess-1";
		render(
			<QueryClientProvider client={new QueryClient()}>
				<TooltipProvider>
					<ShellTopbar />
				</TooltipProvider>
			</QueryClientProvider>,
		);

		await userEvent.click(screen.getByRole("button", { name: "Open orchestrator" }));

		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-1" });
		expect(navigateMock).not.toHaveBeenCalled();
		expect(spawnMock).not.toHaveBeenCalled();
	});

	it("switches from a worker to its orchestrator as soon as termination is confirmed", async () => {
		postMock.mockReturnValue(new Promise(() => {}));
		renderTopbarSessions([worker, orchestrator], worker.id);

		await userEvent.click(screen.getByRole("button", { name: "Kill session" }));
		await clickKillDialogConfirm();

		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId: "orch-1" },
		});
	});
});

describe("ShellTopbar inspector state", () => {
	it("keeps the expanded worker controls out of the center topbar", () => {
		renderTopbarSessions([worker], "sess-1");

		expect(screen.getByTestId("session-pinned-actions-reserve")).toHaveAttribute("data-state", "collapsed");
		expect(screen.queryByRole("button", { name: "Close inspector panel" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Notifications" })).not.toBeInTheDocument();
	});

	it("sizes the pinned-action reserve for the current worker inspector state", () => {
		useUiStore.setState({
			inspectorSessions: {
				"sess-1": { isOpen: true, view: "summary" },
				"sess-2": { isOpen: false, view: "summary" },
			},
		});
		const view = renderTopbarSessions([worker, secondWorker], "sess-1");

		expect(screen.getByTestId("session-pinned-actions-reserve")).toHaveAttribute("data-state", "collapsed");

		paramsMock.sessionId = "sess-2";
		view.rerenderTopbar();

		expect(screen.getByTestId("session-pinned-actions-reserve")).toHaveAttribute("data-state", "expanded");
	});

	it("keeps one reserve mounted while the inspector changes state", () => {
		useUiStore.setState({ inspectorSessions: { "sess-1": { isOpen: false, view: "summary" } } });
		const view = renderTopbarSessions([worker], "sess-1");
		const reserve = screen.getByTestId("session-pinned-actions-reserve");

		useUiStore.setState({ inspectorSessions: { "sess-1": { isOpen: true, view: "summary" } } });
		view.rerenderTopbar();

		expect(screen.getByTestId("session-pinned-actions-reserve")).toBe(reserve);
		expect(reserve).toHaveAttribute("data-state", "collapsed");
		expect(within(reserve).queryByRole("button")).not.toBeInTheDocument();
	});
});

describe("ShellTopbar open-in-editor control", () => {
	it("does not request an editor workspace for imported history", () => {
		renderTopbarSessions([{ ...worker, importedHistory: true }], "sess-1");
		expect(screen.queryByRole("button", { name: "Open in Cursor" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Choose editor" })).not.toBeInTheDocument();
	});

	it("shows the open-in-editor control for a local session", async () => {
		renderTopbarSessions([worker], "sess-1");

		expect(await screen.findByRole("button", { name: "Open in Cursor" })).toBeInTheDocument();
	});

	it("hides the open-in-editor control for a cloud session instead of asking the local daemon for it", async () => {
		// Cloud sessions have no local workspace: the local daemon has never
		// heard of them, so this control's own "workspace" query would 404 with
		// "Unknown session" and surface that raw local-daemon error in the
		// topbar (see issue #4570). Hiding the control for kind === CLOUD_PROJECT_KIND
		// avoids the query entirely.
		renderTopbarSessions([worker], "sess-1", false, undefined, CLOUD_PROJECT_KIND);

		await waitFor(() => {
			expect(screen.queryByRole("button", { name: "Open in Cursor" })).not.toBeInTheDocument();
			expect(screen.queryByRole("button", { name: "Choose editor" })).not.toBeInTheDocument();
		});
	});
});

describe("TopbarKillButton", () => {
	it("opens a compact confirmation card below the kill control", async () => {
		renderKill();

		const killButton = screen.getByRole("button", { name: "Kill session" });
		await userEvent.click(killButton);
		expect(postMock).not.toHaveBeenCalled();
		expect(killButton).toHaveAttribute("aria-expanded", "true");
		const confirmation = screen.getByRole("dialog", { name: "Terminate do the thing?" });
		expect(confirmation).toHaveClass("w-64", "bg-popover", "p-3");
		expect(confirmation).toHaveAttribute("data-side", "bottom");
		expect(within(confirmation).getByRole("button", { name: "No" })).toBeInTheDocument();
		expect(within(confirmation).getByRole("button", { name: "Yes, terminate session" })).toHaveTextContent("Yes");

		await clickKillDialogConfirm();

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/kill", {
			params: { path: { sessionId: "sess-1" } },
		});
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("can back out of the confirmation without killing", async () => {
		renderKill();

		await userEvent.click(screen.getByRole("button", { name: "Kill session" }));
		await userEvent.click(screen.getByRole("button", { name: "No" }));

		expect(screen.getByRole("button", { name: "Kill session" })).toBeInTheDocument();
		expect(postMock).not.toHaveBeenCalled();
	});

	it("surfaces the daemon error when the kill fails", async () => {
		postMock.mockResolvedValue({ data: undefined, error: { message: "session not found" } });
		renderKill();

		await userEvent.click(screen.getByRole("button", { name: "Kill session" }));
		await clickKillDialogConfirm();

		expect(await screen.findByText("session not found")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Kill session" })).toBeEnabled();
	});

	it("clears a stale daemon error before retrying the kill", async () => {
		postMock
			.mockResolvedValueOnce({ data: undefined, error: { message: "session not found" } })
			.mockReturnValue(new Promise(() => {}));
		renderKill();

		await userEvent.click(screen.getByRole("button", { name: "Kill session" }));
		await clickKillDialogConfirm();
		expect(await screen.findByText("session not found")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Kill session" }));
		await clickKillDialogConfirm();

		await waitFor(() => expect(screen.queryByText("session not found")).not.toBeInTheDocument());
	});

	it("returns to the project orchestrator immediately after confirming", async () => {
		let resolveKill!: (value: { data: { ok: boolean; sessionId: string }; error: undefined }) => void;
		postMock.mockReturnValue(
			new Promise((resolve) => {
				resolveKill = resolve;
			}),
		);
		renderKill(worker, orchestrator.id);

		await userEvent.click(screen.getByRole("button", { name: "Kill session" }));
		await clickKillDialogConfirm();

		expect(onKilledMock).toHaveBeenCalledWith("proj-1", "orch-1");
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		resolveKill({ data: { ok: true, sessionId: "sess-1" }, error: undefined });
	});

	it("shows pending and failure feedback after navigating to the orchestrator", async () => {
		let finishKill!: (value: {
			data: undefined;
			error: { message: string };
			response: { status: number };
		}) => void;
		postMock.mockReturnValue(
			new Promise((resolve) => {
				finishKill = resolve;
			}),
		);
		const view = renderTopbarSessions([worker, orchestrator], worker.id);

		await userEvent.click(screen.getByRole("button", { name: "Kill session" }));
		await clickKillDialogConfirm();
		paramsMock.sessionId = orchestrator.id;
		view.rerenderTopbar();

		expect(screen.getByRole("status")).toHaveTextContent("Killing do the thing");
		finishKill({
			data: undefined,
			error: { message: "runtime teardown failed" },
			response: { status: 500 },
		});

		expect(await screen.findByRole("alert")).toHaveTextContent("do the thing: runtime teardown failed");
	});

	it("falls back to the project board when no orchestrator is available", async () => {
		renderKill();

		await userEvent.click(screen.getByRole("button", { name: "Kill session" }));
		await clickKillDialogConfirm();

		await waitFor(() => {
			expect(onKilledMock).toHaveBeenCalledWith("proj-1", undefined);
		});
	});

	it("scopes Killing state to the worker id during rapid switching", async () => {
		let resolveKill!: (value: { data: { ok: boolean; sessionId: string }; error: undefined }) => void;
		postMock.mockReturnValue(
			new Promise((resolve) => {
				resolveKill = resolve;
			}),
		);
		const view = renderTopbarSessions([worker, secondWorker], "sess-1");

		await userEvent.click(screen.getByRole("button", { name: "Kill session" }));
		await clickKillDialogConfirm();
		expect(await screen.findByRole("button", { name: "Killing..." })).toBeDisabled();

		paramsMock.sessionId = "sess-2";
		view.rerenderTopbar();

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Kill session" })).toBeEnabled();

		paramsMock.sessionId = "sess-1";
		view.rerenderTopbar();
		expect(screen.getByRole("button", { name: "Killing..." })).toBeDisabled();

		resolveKill({ data: { ok: true, sessionId: "sess-1" }, error: undefined });
		await waitFor(() => expect(screen.getByRole("button", { name: "Kill session" })).toBeEnabled());
	});

	it("keeps kill failures with their worker and clears only that worker pending state", async () => {
		let resolveKill!: (value: { data: undefined; error: { message: string }; response: { status: number } }) => void;
		postMock.mockReturnValue(
			new Promise((resolve) => {
				resolveKill = resolve;
			}),
		);
		const view = renderTopbarSessions([worker, secondWorker], "sess-1");

		await userEvent.click(screen.getByRole("button", { name: "Kill session" }));
		await clickKillDialogConfirm();
		paramsMock.sessionId = "sess-2";
		view.rerenderTopbar();
		resolveKill({ data: undefined, error: { message: "worker one failed" }, response: { status: 500 } });

		await waitFor(() => expect(view.queryClient.isMutating()).toBe(0));
		expect(screen.getByRole("button", { name: "Kill session" })).toBeEnabled();
		expect(screen.queryByText("worker one failed")).not.toBeInTheDocument();

		paramsMock.sessionId = "sess-1";
		view.rerenderTopbar();
		expect(await screen.findByText("worker one failed")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Kill session" })).toBeEnabled();
	});
});
