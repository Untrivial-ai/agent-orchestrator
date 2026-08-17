import { StrictMode, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render as rtlRender, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SessionView } from "./SessionView";
import { SessionTopbarProvider } from "./SessionTopbarPortal";
import { TooltipProvider } from "./ui/tooltip";
import { useUiStore } from "../stores/ui-store";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";

const navigateMock = vi.hoisted(() => vi.fn());
const openShellTerminalMock = vi.hoisted(() => vi.fn());
const closeShellTerminalMock = vi.hoisted(() => vi.fn());
const nativeFullScreenMock = vi.hoisted(() => vi.fn(() => false));
const interfaceTransitionMock = vi.hoisted(() => ({
	start: vi.fn(),
	resetStartError: vi.fn(),
	cancel: vi.fn(),
	acknowledgeNotice: vi.fn(),
}));
const interfaceTransitionState = vi.hoisted(() => ({
	status: undefined as
		| {
				supported: boolean;
				targetMode?: "chat" | "tui";
				reason?: string;
				reasonCode?: string;
				transition?: {
					id: string;
					sessionId: string;
					sourceMode: "chat" | "tui";
					targetMode: "chat" | "tui";
					policy: "drain" | "interrupt";
					phase: "failed" | "recovery_required";
					errorCode?: string;
					errorDetail?: string;
					createdAt: string;
					updatedAt: string;
					completedAt?: string;
					noticeAcknowledgedAt?: string;
				};
		  }
		| undefined,
}));
const reviewGetMock = vi.hoisted(() => vi.fn());
const inspectorVisibilityRenders = vi.hoisted(() => [] as boolean[]);

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
}));

vi.mock("../lib/platform", () => ({
	// Exercise the macOS shell layout without changing the existing Ctrl-based
	// shortcut assertions in this suite.
	hidesShellTopbar: () => true,
	isMacPlatform: () => false,
}));
vi.mock("../hooks/useWindowFullScreen", () => ({
	useWindowFullScreen: () => nativeFullScreenMock(),
}));
vi.mock("../hooks/useSessionInterfaceTransition", () => ({
	interfaceTransitionIsActive: () => false,
	interfaceTransitionHasUnacknowledgedNotice: (transition?: {
		phase?: string;
		noticeAcknowledgedAt?: string;
	}) =>
		Boolean(
			transition &&
				!transition.noticeAcknowledgedAt &&
				(transition.phase === "failed" || transition.phase === "recovery_required"),
		),
	useSessionInterfaceTransition: () => ({
		status: interfaceTransitionState.status,
		transition: interfaceTransitionState.status?.transition,
		isLoading: false,
		statusError: undefined,
		start: interfaceTransitionMock.start,
		starting: false,
		startError: undefined,
		resetStartError: interfaceTransitionMock.resetStartError,
		cancel: interfaceTransitionMock.cancel,
		cancelling: false,
		cancelError: undefined,
		acknowledgeNotice: interfaceTransitionMock.acknowledgeNotice,
		acknowledgingNotice: false,
		acknowledgeNoticeError: undefined,
	}),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: reviewGetMock,
	},
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

const { workspaces, workspaceQueryState, shellTerminalsState } = vi.hoisted(() => {
	const worker = {
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
	} satisfies WorkspaceSession;
	const secondWorker = {
		...worker,
		id: "sess-2",
		title: "do the other thing",
		branch: "ao/sess-2",
	} satisfies WorkspaceSession;
	const orchestrator = {
		...worker,
		id: "sess-orch",
		kind: "orchestrator",
		title: "orchestrate",
	} satisfies WorkspaceSession;
	const crossProjectWorker = {
		...worker,
		id: "sess-cross-project",
		workspaceId: "proj-2",
		workspaceName: "other-app",
		title: "cross-project task",
		branch: "ao/cross-project",
	} satisfies WorkspaceSession;
	const workspaces: WorkspaceSummary[] = [
		{ id: "proj-1", name: "my-app", path: "/p", type: "main", sessions: [worker, secondWorker, orchestrator] },
		{ id: "proj-2", name: "other-app", path: "/q", type: "main", sessions: [crossProjectWorker] },
	];
	const workspaceQueryState: { data: WorkspaceSummary[] | undefined; isLoading: boolean } = {
		data: workspaces,
		isLoading: false,
	};
	const shellTerminalsState: {
		data: Array<{
			handleId: string;
			projectId?: string;
			sessionId?: string;
			title: string;
			workingDir: string;
			createdAt: string;
		}>;
	} = {
		data: [],
	};
	return { workspaces, workspaceQueryState, shellTerminalsState };
});

// The terminal and inspector body pull in xterm/SSE machinery irrelevant to
// the split under test. (ShellTopbar is shell-owned on Win/Linux; when the
// platform hides the shell topbar, SessionView mounts it in-panel.)
vi.mock("./ShellTopbar", () => ({
	ShellTopbar: ({ sessionAction }: { sessionAction?: ReactNode }) => (
		<div data-testid="mock-session-topbar">{sessionAction}</div>
	),
}));
vi.mock("./NotificationCenter", () => ({
	NotificationCenter: () => <button type="button">Notifications</button>,
}));
vi.mock("./TerminalSwitchAgentButton", () => ({
	TerminalSwitchAgentButton: () => (
		<button aria-label="Switch agent" type="button" />
	),
}));
vi.mock("./chat/SessionChatSurface", () => ({
	SessionChatSurface: ({
		onOpenShell,
		headerActions,
		reviewerTerminal,
		onOpenReviewerTerminal,
		reviewerTarget,
		onSelectChat,
		shellTerminals = [],
		shellTarget,
		onSelectShellTerminal,
	}: {
		onOpenShell?: () => void;
		headerActions?: ReactNode;
		reviewerTerminal?: { handleId: string; harness: string };
		onOpenReviewerTerminal?: (target: { handleId: string; harness: string }) => void;
		reviewerTarget?: { kind: "reviewer"; handleId: string; harness: string; sessionId: string };
		onSelectChat?: () => void;
		shellTerminals?: Array<{ handleId: string; title: string }>;
		shellTarget?: { kind: "shell"; handleId: string };
		onSelectShellTerminal?: (handleId: string) => void;
	}) => (
		<div data-testid="chat-surface">
			chat surface
			{headerActions}
			{reviewerTerminal ? (
				<button type="button" onClick={() => onOpenReviewerTerminal?.(reviewerTerminal)}>
					Reviewer
				</button>
			) : null}
			{reviewerTarget ? (
				<div data-testid="terminal-target">reviewer</div>
			) : null}
			{reviewerTarget ? (
				<button type="button" onClick={onSelectChat}>
					select chat tab
				</button>
			) : null}
			<div data-testid="shell-tabs">
				{shellTerminals.map((shell) => (
					<button
						key={shell.handleId}
						onClick={() => onSelectShellTerminal?.(shell.handleId)}
						type="button"
					>
						{shell.title}
					</button>
				))}
			</div>
			{shellTarget ? <div data-testid="terminal-target">shell</div> : null}
			{shellTarget ? (
				<button type="button" onClick={onSelectChat}>
					select chat tab
				</button>
			) : null}
			<button type="button" onClick={onOpenShell}>
				open shell from chat
			</button>
		</div>
	),
}));
vi.mock("./CenterPane", () => ({
	CenterPane: ({
		session,
		shellTerminals = [],
		onCloseShellTerminal,
		onSelectShellTerminal,
		onSelectSessionTerminal,
		onSelectReviewerTerminal,
		topbarActions,
		reviewerTerminal,
		terminalTarget,
	}: {
		session?: WorkspaceSession;
		shellTerminals?: Array<{ handleId: string; title: string }>;
		onCloseShellTerminal?: (handleId: string) => void;
		onSelectShellTerminal?: (handleId: string) => void;
		onSelectSessionTerminal?: () => void;
		onSelectReviewerTerminal?: (target: { handleId: string; harness: string }) => void;
		topbarActions?: ReactNode;
		reviewerTerminal?: { handleId: string; harness: string };
		terminalTarget?: { kind: string; handleId?: string };
	}) => (
		<div>
			terminal center
			{topbarActions}
			<div data-testid="terminal-target">
				{terminalTarget?.kind === "shell" ? terminalTarget.handleId : (terminalTarget?.kind ?? "worker")}
			</div>
			<div data-testid="session-tab">{session?.title ?? ""}</div>
			<div data-testid="reviewer-harness">{reviewerTerminal?.harness ?? ""}</div>
			{reviewerTerminal ? (
				<button type="button" onClick={() => onSelectReviewerTerminal?.(reviewerTerminal)}>
					select reviewer tab
				</button>
			) : null}
			<div data-testid="shell-tabs">{shellTerminals.map((s) => s.title).join(",")}</div>
			{shellTerminals.map((s) => (
				<button key={s.handleId} type="button" onClick={() => onSelectShellTerminal?.(s.handleId)}>
					select {s.title}
				</button>
			))}
			{shellTerminals.map((s) => (
				<button key={`close-${s.handleId}`} type="button" onClick={() => onCloseShellTerminal?.(s.handleId)}>
					close {s.title}
				</button>
			))}
			<button type="button" onClick={() => onSelectSessionTerminal?.()}>
				select agent tab
			</button>
		</div>
	),
}));
vi.mock("./BrowserPanel", () => ({
	BrowserPanelView: ({
		poppedOut,
		onTogglePopOut,
	}: {
		poppedOut: boolean;
		onTogglePopOut: (next: boolean) => void;
	}) => (
		<button type="button" onClick={() => onTogglePopOut(!poppedOut)}>
			{poppedOut ? "browser center" : "browser rail"}
		</button>
	),
	useBrowserAnnotationQueue: () => ({
		status: "idle",
		error: "",
		queuedCount: 0,
		beginPicking: vi.fn(),
		cancelPicking: vi.fn(),
		enqueue: vi.fn(),
		failPicking: vi.fn(),
		retryQueued: vi.fn(),
	}),
}));
vi.mock("./SessionFilesView", () => ({
	SessionFilesView: ({
		isMaximized,
		onToggleMaximized,
	}: {
		isMaximized?: boolean;
		onToggleMaximized?: (next: boolean) => void;
	}) => (
		<button type="button" onClick={() => onToggleMaximized?.(!isMaximized)}>
			{isMaximized ? "files center" : "files rail"}
		</button>
	),
}));
const { browserDestroy, browserViewOptions, browserViewState } = vi.hoisted(() => ({
	browserDestroy: vi.fn(),
	browserViewOptions: { current: undefined as { active: boolean; sessionId: string; terminated: boolean } | undefined },
	browserViewState: { url: "", agentBrowserActive: false },
}));
vi.mock("../hooks/useBrowserView", () => ({
	useBrowserView: (options: { active: boolean; sessionId: string; terminated: boolean }) => {
		browserViewOptions.current = options;
		return {
			viewId: "browser:sess-1",
			navState: {
				viewId: "browser:sess-1",
				url: browserViewState.url,
				title: browserViewState.url ? "Calculator" : "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			},
			slotRef: vi.fn(),
			navigate: vi.fn(),
			goBack: vi.fn(),
			goForward: vi.fn(),
			reload: vi.fn(),
			stop: vi.fn(),
			tabs: [{ id: "t1", url: "http://127.0.0.1:4173/", title: "Calculator", active: true }],
			activeTabId: "t1",
			tabNotice: "",
			agentBrowserActive: browserViewState.agentBrowserActive,
			selectTab: vi.fn(),
			closeTab: vi.fn(),
			annotationMode: false,
			setAnnotationMode: vi.fn(),
			destroy: browserDestroy,
		};
	},
}));
vi.mock("./SessionInspector", () => ({
	SessionInspector: ({
		filesView,
		isInspectorVisible = true,
		onOpenFiles,
		onToggleBrowserPopOut,
		view,
	}: {
		filesView?: ReactNode;
		isInspectorVisible?: boolean;
		onOpenFiles?: () => void;
		onToggleBrowserPopOut?: () => void;
		view?: string;
	}) => {
		inspectorVisibilityRenders.push(isInspectorVisible);
		return (
			<div>
				<button type="button" data-view={view} onClick={onToggleBrowserPopOut}>
					pop browser
				</button>
				<button type="button" onClick={onOpenFiles}>
					open files
				</button>
				{view === "files" ? filesView : null}
			</div>
		);
	},
}));
vi.mock("../lib/shell-context", () => ({
	useShell: () => ({ daemonStatus: { state: "ready" } }),
}));
vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceQuery: () => ({
		data: workspaceQueryState.data,
		isLoading: workspaceQueryState.isLoading,
	}),
}));
// Standalone shell terminals are orthogonal to the split under test, and their
// real hooks would need a QueryClientProvider this suite deliberately omits.
vi.mock("../hooks/useShellTerminals", () => ({
	useShellTerminals: () => ({ data: shellTerminalsState.data, isLoading: false }),
	useOpenShellTerminal: () => ({ mutate: openShellTerminalMock }),
	useCloseShellTerminal: () => ({ mutate: closeShellTerminalMock }),
	useRenameShellTerminal: () => ({ mutate: vi.fn() }),
}));

function workerSession(sessionId: string): WorkspaceSession {
	const session = workspaces[0].sessions.find((item) => item.id === sessionId);
	if (!session) throw new Error(`missing test session ${sessionId}`);
	return session;
}

function inspectorOpen(sessionId: string): boolean {
	return useUiStore.getState().inspectorSessions[sessionId]?.isOpen ?? true;
}

function browserUnseen(sessionId: string): boolean {
	return Boolean(useUiStore.getState().inspectorSessions[sessionId]?.browserUnseen);
}

function inspectorButton(): HTMLElement {
	const button = screen.getByText("pop browser").closest("button");
	if (!button) throw new Error("missing inspector button");
	return button;
}

function render(ui: ReactNode) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return {
		...rtlRender(ui, {
			wrapper: ({ children }) => (
				<QueryClientProvider client={client}>
					<TooltipProvider>
						<SessionTopbarProvider>{children}</SessionTopbarProvider>
					</TooltipProvider>
				</QueryClientProvider>
			),
		}),
		client,
	};
}

describe("SessionView", () => {
	beforeEach(() => {
		inspectorVisibilityRenders.length = 0;
		nativeFullScreenMock.mockReturnValue(false);
		window.localStorage.clear();
		for (const session of workspaces.flatMap((workspace) => workspace.sessions)) {
			delete session.previewUrl;
			delete session.previewRevision;
			delete session.isTerminated;
			session.status = "working";
			delete session.mode;
			session.prs = [];
		}
		workspaceQueryState.data = workspaces;
		workspaceQueryState.isLoading = false;
		useUiStore.setState({
			activeShellTerminalHandleId: null,
			inspectorSessions: {},
			visibleTerminalKindBySession: {},
		});
		browserDestroy.mockReset();
		browserViewOptions.current = undefined;
		browserViewState.url = "";
		browserViewState.agentBrowserActive = false;
		shellTerminalsState.data = [];
	navigateMock.mockReset();
	openShellTerminalMock.mockReset();
	closeShellTerminalMock.mockReset();
	interfaceTransitionMock.start.mockReset();
		interfaceTransitionMock.resetStartError.mockReset();
	interfaceTransitionMock.cancel.mockReset();
		interfaceTransitionMock.acknowledgeNotice.mockReset();
		interfaceTransitionState.status = undefined;
		reviewGetMock.mockReset();
		reviewGetMock.mockResolvedValue({ data: { reviewerHandleId: "", reviews: [], runs: [] }, error: undefined });
	});

	// Regression: shell terminals are an app-wide list, so without a per-session
	// filter a shell opened in another session would show up as a tab in this
	// session's strip. Only this session's shells (not another session's, and no
	// session-less ones) should reach the terminal pane.
	it("shows only the current session's shell terminals as tabs", () => {
		shellTerminalsState.data = [
			{
				handleId: "sh-a",
				sessionId: "sess-1",
				title: "sess-1-shell",
				workingDir: "/p",
				createdAt: "2026-07-24T00:00:00Z",
			},
			{
				handleId: "sh-b",
				sessionId: "sess-2",
				title: "sess-2-shell",
				workingDir: "/q",
				createdAt: "2026-07-24T00:00:00Z",
			},
			{ handleId: "sh-c", title: "loose-shell", workingDir: "/r", createdAt: "2026-07-24T00:00:00Z" },
		];
		render(<SessionView sessionId="sess-1" />);
		const tabs = screen.getByTestId("shell-tabs");
		expect(tabs).toHaveTextContent("sess-1-shell");
		expect(tabs).not.toHaveTextContent("sess-2-shell");
		expect(tabs).not.toHaveTextContent("loose-shell");
	});

	// The pane shows one terminal at a time, so selecting a shell takes the
	// agent's terminal off screen while the route still points at this session.
	// The notification runtime lives outside this subtree and reads the published
	// kind to decide whether the user can actually see a needs_input prompt.
	it("publishes which terminal the session pane is showing", () => {
		shellTerminalsState.data = [
			{
				handleId: "sh-a",
				sessionId: "sess-1",
				title: "sess-1-shell",
				workingDir: "/p",
				createdAt: "2026-07-24T00:00:00Z",
			},
		];
		const view = render(<SessionView sessionId="sess-1" />);
		expect(useUiStore.getState().visibleTerminalKindBySession["sess-1"]).toBe("worker");

		fireEvent.click(screen.getByRole("button", { name: "select sess-1-shell" }));
		expect(useUiStore.getState().visibleTerminalKindBySession["sess-1"]).toBe("shell");

		fireEvent.click(screen.getByRole("button", { name: "select agent tab" }));
		expect(useUiStore.getState().visibleTerminalKindBySession["sess-1"]).toBe("worker");

		// Leaving the session drops the entry rather than leaving a stale "worker"
		// behind for a pane that is no longer mounted.
		view.unmount();
		expect(useUiStore.getState().visibleTerminalKindBySession["sess-1"]).toBeUndefined();
	});

	it("keeps a session-scoped shell reachable from a Chat session", () => {
		workspaces[0].sessions[0].mode = "chat";
		shellTerminalsState.data = [
			{
				handleId: "chat-shell",
				sessionId: "sess-1",
				title: "chat worktree shell",
				workingDir: "/p",
				createdAt: "2026-08-04T00:00:00Z",
			},
		];

		render(<SessionView sessionId="sess-1" />);
		expect(screen.getByTestId("chat-surface")).toBeInTheDocument();

		// Selecting the shell keeps the chat surface mounted — the shell renders
		// as a tab inside it instead of swapping in the terminal CenterPane.
		act(() => useUiStore.getState().setActiveShellTerminal("chat-shell"));
		expect(screen.getByTestId("chat-surface")).toBeInTheDocument();
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("shell");

		fireEvent.click(screen.getByRole("button", { name: "select chat tab" }));
		expect(screen.getByTestId("chat-surface")).toBeInTheDocument();
		expect(screen.queryByTestId("terminal-target")).not.toBeInTheDocument();
	});

	// The strip only ever shows the session on screen — pinning another session's
	// terminal as a tab (and the cross-project picker that did it) is gone (#3208).
	it("shows only the session on screen in the tab strip", () => {
		render(<SessionView sessionId="sess-1" />);

		expect(screen.getByTestId("session-tab")).toHaveTextContent("do the thing");
		expect(screen.getByTestId("session-tab")).not.toHaveTextContent("do the other thing");
		expect(screen.queryByRole("button", { name: /^Add / })).not.toBeInTheDocument();
	});

	// The daemon roots a shell in the session's worktree when it is given that
	// session's id, so a new terminal must name the session actually on screen.
	it("opens new terminals in the on-screen session's worktree", () => {
		render(<SessionView sessionId="sess-2" />);

		const newTerminalButton = screen.getByRole("button", { name: "New terminal" });
		expect(newTerminalButton).toHaveAttribute("title", "New terminal (Ctrl+T)");
		fireEvent.click(newTerminalButton);
		expect(openShellTerminalMock).toHaveBeenCalledWith({ projectId: "proj-1", sessionId: "sess-2" }, expect.anything());
	});

	it("does not offer a new terminal for orchestrator sessions", () => {
		render(<SessionView sessionId="sess-orch" />);

		expect(screen.queryByRole("button", { name: "New terminal" })).not.toBeInTheDocument();
	});

	it("shows a shell opened from chat and returns to the chat agent tab", () => {
		const session = workspaces[0]!.sessions.find((candidate) => candidate.id === "sess-1")!;
		session.mode = "chat";
		const shell = {
			handleId: "sh-chat",
			projectId: "proj-1",
			sessionId: "sess-1",
			title: "chat shell",
			workingDir: "/p",
			createdAt: "2026-08-04T00:00:00Z",
		};
		openShellTerminalMock.mockImplementation((_input, options) => {
			shellTerminalsState.data = [shell];
			options.onSuccess(shell);
		});

		render(<SessionView sessionId="sess-1" />);
		expect(screen.getByText("chat surface")).toBeInTheDocument();

		// Opening a shell from chat keeps the chat surface mounted: the shell is
		// a tab in its header and renders as the surface's active pane.
		fireEvent.click(screen.getByRole("button", { name: "open shell from chat" }));
		expect(screen.getByText("chat surface")).toBeInTheDocument();
		expect(screen.getByTestId("shell-tabs")).toHaveTextContent("chat shell");
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("shell");

		fireEvent.click(screen.getByRole("button", { name: "select chat tab" }));
		expect(screen.getByText("chat surface")).toBeInTheDocument();
		expect(screen.queryByTestId("terminal-target")).not.toBeInTheDocument();
	});

	it.each([
		["Terminal UI worker", "sess-1", "tui", "chat", "Switch to chat UI"],
		["Terminal UI orchestrator", "sess-orch", "tui", "chat", "Switch to chat UI"],
	] as const)("switches an idle %s directly with drain", (_label, sessionId, mode, targetMode, buttonName) => {
		interfaceTransitionState.status = { supported: true, targetMode };
		const session = workerSession(sessionId);
		session.mode = mode;
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId={sessionId} />);

		fireEvent.click(screen.getByRole("button", { name: buttonName }));

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({ targetMode, policy: "drain" });
	});

	it.each([
		["worker", "sess-1"],
		["orchestrator", "sess-orch"],
	] as const)("uses interrupt for an idle Chat %s switching to Terminal UI", (_label, sessionId) => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession(sessionId);
		session.mode = "chat";
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId={sessionId} />);

		fireEvent.click(screen.getByRole("button", { name: "Switch to terminal UI" }));

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({ targetMode: "tui", policy: "interrupt" });
	});

	it("keeps the policy dialog closed when an idle direct switch fails", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "chat" };
		const session = workerSession("sess-1");
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };
		interfaceTransitionMock.start.mockRejectedValueOnce(new Error("switch failed"));

		render(<SessionView sessionId="sess-1" />);

		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "Switch to chat UI" }));
		});

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("keeps the policy dialog closed when a busy Chat escape switch fails", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession("sess-1");
		session.mode = "chat";
		session.status = "working";
		session.activity = { state: "active", lastActivityAt: "2026-08-06T00:00:00Z" };
		interfaceTransitionMock.start.mockRejectedValueOnce(new Error("switch failed"));

		render(<SessionView sessionId="sess-1" />);

		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "Switch to terminal UI" }));
		});

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({ targetMode: "tui", policy: "interrupt" });
	});

	it.each([
		["working status", "sess-1", "tui", "chat", "Switch to chat UI", "working", "idle"],
		["needs-input status", "sess-orch", "tui", "chat", "Switch to chat UI", "needs_input", "idle"],
		["blocked activity", "sess-1", "tui", "chat", "Switch to chat UI", "idle", "blocked"],
	] as const)("opens the switch policy dialog for %s", (_label, sessionId, mode, targetMode, buttonName, status, activityState) => {
		interfaceTransitionState.status = { supported: true, targetMode };
		const session = workerSession(sessionId);
		session.mode = mode;
		session.status = status;
		session.activity = { state: activityState, lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId={sessionId} />);

		fireEvent.click(screen.getByRole("button", { name: buttonName }));

		expect(screen.getByRole("dialog")).toBeInTheDocument();
		expect(interfaceTransitionMock.start).not.toHaveBeenCalled();
	});

	it.each([
		["working status", "sess-1", "working", "idle"],
		["needs-input status", "sess-orch", "needs_input", "idle"],
		["active activity", "sess-1", "idle", "active"],
		["waiting-input activity", "sess-orch", "idle", "waiting_input"],
		["blocked activity", "sess-1", "idle", "blocked"],
	] as const)("interrupts a busy Chat session for %s and switches directly to Terminal UI", (_label, sessionId, status, activityState) => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession(sessionId);
		session.mode = "chat";
		session.status = status;
		session.activity = { state: activityState, lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId={sessionId} />);

		fireEvent.click(screen.getByRole("button", { name: "Switch to terminal UI" }));

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({ targetMode: "tui", policy: "interrupt" });
	});

	it("checks only the selected session when deciding whether to show the policy dialog", () => {
		interfaceTransitionState.status = { supported: true, targetMode: "chat" };
		const selected = workerSession("sess-1");
		selected.status = "idle";
		selected.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };
		const other = workerSession("sess-2");
		other.status = "working";
		other.activity = { state: "active", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "Switch to chat UI" }));

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({ targetMode: "chat", policy: "drain" });
	});

	it("does not resurrect an acknowledged recovery notice after the session view remounts", async () => {
		const transition = {
			id: "transition-recovered",
			sessionId: "sess-1",
			sourceMode: "chat" as const,
			targetMode: "tui" as const,
			policy: "drain" as const,
			phase: "recovery_required" as const,
			errorCode: "DAEMON_RESTARTED",
			errorDetail: "AO recovered the session in its last committed mode.",
			createdAt: "2026-08-12T10:00:00Z",
			updatedAt: "2026-08-12T10:01:00Z",
			completedAt: "2026-08-12T10:01:00Z",
		};
		interfaceTransitionState.status = { supported: true, targetMode: "chat", transition };
		interfaceTransitionMock.acknowledgeNotice.mockImplementation(async () => {
			Object.assign(transition, { noticeAcknowledgedAt: "2026-08-13T08:00:00Z" });
		});

		const view = render(<SessionView sessionId="sess-1" />);
		expect(screen.getByText(transition.errorDetail)).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Dismiss interface switch message" }));
		await waitFor(() =>
			expect(interfaceTransitionMock.acknowledgeNotice).toHaveBeenCalledWith("transition-recovered"),
		);

		view.unmount();
		render(<SessionView sessionId="sess-1" />);
		expect(screen.queryByText(transition.errorDetail)).not.toBeInTheDocument();
	});

	it.each([
		["worker", "sess-1"],
		["orchestrator", "sess-orch"],
	] as const)("hides the interface switch button for %s sessions when Chat UI is unsupported", (_label, sessionId) => {
		interfaceTransitionState.status = { supported: false, targetMode: "chat", reasonCode: "CHAT_UNSUPPORTED" };
		const session = workerSession(sessionId);
		session.mode = "tui";
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId={sessionId} />);

		expect(screen.queryByRole("button", { name: "Switch to chat UI" })).not.toBeInTheDocument();
	});

	it("shows the switch button when the adapter only reports a generic unsupported reason", () => {
		interfaceTransitionState.status = { supported: false, targetMode: "chat", reasonCode: "INTERFACE_HANDOFF_UNSUPPORTED" };
		const session = workerSession("sess-1");
		session.mode = "tui";
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId="sess-1" />);

		expect(screen.getByRole("button", { name: "Switch to chat UI" })).toBeInTheDocument();
	});

	it("walks backward through auxiliary terminals before returning to the permanent terminal", () => {
		shellTerminalsState.data = [
			{
				handleId: "sh-a",
				sessionId: "sess-1",
				title: "first shell",
				workingDir: "/p",
				createdAt: "2026-07-24T00:00:00Z",
			},
			{
				handleId: "sh-b",
				sessionId: "sess-1",
				title: "second shell",
				workingDir: "/p",
				createdAt: "2026-07-24T00:01:00Z",
			},
		];
		const view = render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "select second shell" }));
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("sh-b");

		fireEvent.click(screen.getByRole("button", { name: "close second shell" }));
		expect(closeShellTerminalMock).toHaveBeenCalledWith("sh-b");
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("sh-a");
		expect(useUiStore.getState().activeShellTerminalHandleId).toBe("sh-a");

		shellTerminalsState.data = shellTerminalsState.data.filter((shell) => shell.handleId !== "sh-b");
		view.rerender(<SessionView sessionId="sess-1" />);
		fireEvent.click(screen.getByRole("button", { name: "close first shell" }));
		expect(closeShellTerminalMock).toHaveBeenCalledWith("sh-a");
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("worker");
		expect(useUiStore.getState().activeShellTerminalHandleId).toBeNull();
	});

	it("uses the stored reviewer harness for the reviewer tab icon when no latest run is current", async () => {
		const worker = workerSession("sess-1");
		worker.prs = [
			{
				url: "https://github.com/acme/repo/pull/7",
				number: 7,
				state: "open",
				ci: "passing",
				review: "none",
				mergeability: "mergeable",
				reviewComments: false,
				updatedAt: "2026-06-15T00:00:00Z",
			},
		];
		reviewGetMock.mockResolvedValueOnce({
			data: { reviewerHandleId: "review-sess-1", reviewerHarness: "codex", reviews: [], runs: [] },
			error: undefined,
		});

		render(<SessionView sessionId="sess-1" />);

		await waitFor(() => expect(screen.getByTestId("reviewer-harness")).toHaveTextContent("codex"));
	});

	it("keeps the reviewer terminal reachable from a Chat session", async () => {
		const worker = workerSession("sess-1");
		worker.mode = "chat";
		worker.prs = [{
			url: "https://github.com/acme/repo/pull/7",
			number: 7,
			state: "open",
			ci: "passing",
			review: "none",
			mergeability: "mergeable",
			reviewComments: false,
			updatedAt: "2026-06-15T00:00:00Z",
		}];
		reviewGetMock.mockResolvedValueOnce({
			data: { reviewerHandleId: "review-sess-1", reviewerHarness: "codex", reviews: [], runs: [] },
			error: undefined,
		});

		render(<SessionView sessionId="sess-1" />);
		await screen.findByRole("button", { name: "Reviewer" });
		fireEvent.click(screen.getByRole("button", { name: "Reviewer" }));
		// The chat surface stays mounted; the reviewer pane renders inside it.
		expect(screen.getByTestId("chat-surface")).toBeInTheDocument();
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("reviewer");
		// Selecting the chat tab returns to the chat timeline.
		fireEvent.click(screen.getByRole("button", { name: "select chat tab" }));
		expect(screen.queryByTestId("terminal-target")).not.toBeInTheDocument();
		expect(screen.getByTestId("chat-surface")).toBeInTheDocument();
	});

	it("returns to the session terminal when the reviewer handle is cleared", async () => {
		const worker = workerSession("sess-1");
		worker.prs = [
			{
				url: "https://github.com/acme/repo/pull/7",
				number: 7,
				state: "open",
				ci: "passing",
				review: "none",
				mergeability: "mergeable",
				reviewComments: false,
				updatedAt: "2026-06-15T00:00:00Z",
			},
		];
		reviewGetMock.mockResolvedValueOnce({
			data: { reviewerHandleId: "review-sess-1", reviewerHarness: "codex", reviews: [] },
			error: undefined,
		});

		const view = render(<SessionView sessionId="sess-1" />);
		await screen.findByRole("button", { name: "select reviewer tab" });
		fireEvent.click(screen.getByRole("button", { name: "select reviewer tab" }));
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("reviewer");

		act(() => {
			view.client.setQueryData(["session-reviews", "sess-1"], { reviewerHandleId: "", reviews: [] });
		});

		await waitFor(() => expect(screen.getByTestId("terminal-target")).toHaveTextContent("worker"));
		expect(screen.queryByRole("button", { name: "select reviewer tab" })).not.toBeInTheDocument();
	});

	it.each(["tui", "chat"] as const)(
		"restores the selected reviewer terminal when a %s session becomes active again",
		async (mode) => {
			const worker = workerSession("sess-1");
			worker.mode = mode;
			worker.prs = [
				{
					url: "https://github.com/acme/repo/pull/7",
					number: 7,
					state: "open",
					ci: "passing",
					review: "none",
					mergeability: "mergeable",
					reviewComments: false,
					updatedAt: "2026-06-15T00:00:00Z",
				},
			];
			reviewGetMock.mockResolvedValueOnce({
				data: { reviewerHandleId: "review-sess-1", reviewerHarness: "codex", reviews: [] },
				error: undefined,
			});

			const view = render(<SessionView sessionId="sess-1" />);
			const reviewerButtonName = mode === "chat" ? "Reviewer" : "select reviewer tab";
			await screen.findByRole("button", { name: reviewerButtonName });
			fireEvent.click(screen.getByRole("button", { name: reviewerButtonName }));
			expect(screen.getByTestId("terminal-target")).toHaveTextContent("reviewer");

			worker.status = "terminated";
			worker.isTerminated = true;
			view.rerender(<SessionView sessionId="sess-1" />);
			if (mode === "chat") expect(screen.getByTestId("chat-surface")).toBeInTheDocument();
			expect(screen.getByTestId("terminal-target")).toHaveTextContent("reviewer");
			expect(screen.queryByRole("button", { name: reviewerButtonName })).not.toBeInTheDocument();

			worker.status = "working";
			worker.isTerminated = false;
			view.rerender(<SessionView sessionId="sess-1" />);

			await screen.findByRole("button", { name: reviewerButtonName });
			expect(screen.getByTestId("terminal-target")).toHaveTextContent("reviewer");
		},
	);

	it("opens the inspector with the shared sidebar spring tokens", () => {
		render(<SessionView sessionId="sess-1" />);

		expect(screen.getByTestId("panel-group")).toHaveStyle({
			"--session-inspector-motion-duration": "400ms",
			"--session-inspector-motion-easing":
				"linear(0, 0.333 12.5%, 0.642 25%, 0.813 37.5%, 0.902 50%, 0.949 62.5%, 0.974 75%, 0.986 87.5%, 1)",
		});
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "expanded");
		expect(screen.getByTestId("inspector-resize-handle")).toBeInTheDocument();
	});

	it("opens the Summary inspector alongside the terminal by default", () => {
		render(<SessionView sessionId="sess-1" />);

		expect(screen.getByText("terminal center")).toBeInTheDocument();
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "expanded");
		expect(screen.getByTestId("inspector-resize-handle")).not.toHaveClass("hidden");
		expect(screen.getByTestId("panel-inspector")).not.toHaveAttribute("inert");
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
	});

	it("treats a merged terminated session as terminated for Browser preview", () => {
		const worker = workerSession("sess-1");
		worker.status = "merged";
		worker.isTerminated = true;

		render(<SessionView sessionId="sess-1" />);

		expect(browserViewOptions.current).toMatchObject({ sessionId: "sess-1", terminated: true });
	});

	it("mounts the inspector open by default", () => {
		render(<SessionView sessionId="sess-1" />);

		const pane = screen.getByTestId("panel-inspector");
		expect(pane).not.toHaveAttribute("inert");
		expect(pane).toHaveAttribute("aria-hidden", "false");
		expect(pane).toHaveAttribute("data-state", "expanded");
	});

	it("mounts collapsed and inert when the store says closed", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		render(<SessionView sessionId="sess-1" />);

		const pane = screen.getByTestId("panel-inspector");
		expect(pane).toHaveAttribute("inert");
		expect(pane).toHaveAttribute("aria-hidden", "true");
		expect(pane).toHaveAttribute("data-state", "collapsed");
		expect(pane).toHaveAttribute("hidden");
		expect(screen.getByTestId("inspector-resize-handle")).toHaveClass("hidden");
		expect(screen.getByTestId("inspector-collapsed-rail")).toBeInTheDocument();
	});

	it("keeps inspector chrome visible from the first render of the opening transition", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		render(<SessionView sessionId="sess-1" />);
		const renderCountBeforeOpening = inspectorVisibilityRenders.length;

		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));

		const openingRenders = inspectorVisibilityRenders.slice(renderCountBeforeOpening);
		expect(openingRenders.length).toBeGreaterThan(0);
		expect(openingRenders).not.toContain(false);
	});

	it("starts collapsing the resize handle with the inspector panel", () => {
		render(<SessionView sessionId="sess-1" />);

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });

		expect(screen.getByTestId("inspector-resize-handle")).toHaveClass("hidden");
		expect(screen.getByTestId("inspector-collapsed-rail")).toBeInTheDocument();
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("aria-hidden", "false");
	});

	it("keeps StrictMode mount from collapsing, then collapses on the first user toggle", () => {
		render(
			<StrictMode>
				<SessionView sessionId="sess-1" />
			</StrictMode>,
		);

		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "expanded");

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });

		expect(inspectorOpen("sess-1")).toBe(false);
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "collapsed");
	});

	it("marks the split for live terminal fitting throughout the inspector transition", () => {
		vi.useFakeTimers();
		try {
			render(<SessionView sessionId="sess-1" />);

			fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });
			const split = screen.getByTestId("panel-group");
			expect(split).toHaveAttribute("data-terminal-live-resize", "true");
			expect(split).toHaveAttribute("data-topbar-secondary-label-mode", "expanded");

			act(() => vi.advanceTimersByTime(399));
			expect(split).toHaveAttribute("data-terminal-live-resize", "true");
			expect(split).toHaveAttribute("data-topbar-secondary-label-mode", "expanded");

			act(() => vi.advanceTimersByTime(1));
			expect(split).not.toHaveAttribute("data-terminal-live-resize");
			expect(split).not.toHaveAttribute("data-topbar-secondary-label-mode");
		} finally {
			vi.useRealTimers();
		}
	});

	it("keeps inspector labels expanded at the default width throughout the opening transition", () => {
		vi.useFakeTimers();
		try {
			act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
			render(<SessionView sessionId="sess-1" />);

			act(() => useUiStore.getState().setInspectorOpen("sess-1", true));

			const split = screen.getByTestId("panel-group");
			expect(split).toHaveAttribute("data-terminal-live-resize", "true");
			expect(split).toHaveAttribute("data-inspector-label-mode", "expanded");
			expect(split).toHaveAttribute("data-topbar-secondary-label-mode", "compact");

			act(() => vi.advanceTimersByTime(399));
			expect(split).toHaveAttribute("data-inspector-label-mode", "expanded");
			expect(split).toHaveAttribute("data-topbar-secondary-label-mode", "compact");

			act(() => vi.advanceTimersByTime(1));
			expect(split).not.toHaveAttribute("data-inspector-label-mode");
			expect(split).not.toHaveAttribute("data-topbar-secondary-label-mode");
		} finally {
			vi.useRealTimers();
		}
	});

	it("locks responsive inspector labels compact when the opening target is narrow", () => {
		vi.useFakeTimers();
		const clientWidth = vi
			.spyOn(HTMLElement.prototype, "clientWidth", "get")
			.mockImplementation(function (this: HTMLElement) {
				return this.dataset.testid === "panel-group" ? 640 : 640;
			});
		try {
			act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
			render(<SessionView sessionId="sess-1" />);

			act(() => useUiStore.getState().setInspectorOpen("sess-1", true));

			const split = screen.getByTestId("panel-group");
			expect(split).toHaveAttribute("data-inspector-label-mode", "compact");
			expect(split).toHaveAttribute("data-topbar-secondary-label-mode", "compact");
		} finally {
			clientWidth.mockRestore();
			vi.useRealTimers();
		}
	});

	it("keeps StrictMode mount from expanding, then expands on the first user toggle", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		render(
			<StrictMode>
				<SessionView sessionId="sess-1" />
			</StrictMode>,
		);

		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "collapsed");

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });

		expect(inspectorOpen("sess-1")).toBe(true);
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "expanded");
	});

	it("toggles the inspector with mod+shift+B", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });
		expect(inspectorOpen("sess-1")).toBe(false);
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "collapsed");

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });
		expect(inspectorOpen("sess-1")).toBe(true);
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "expanded");

		// Plain ⌘B belongs to the sidebar — the inspector must not react.
		fireEvent.keyDown(window, { key: "b", metaKey: true });
		expect(inspectorOpen("sess-1")).toBe(true);
	});

	it("keeps the inspector toggle and trailing notification pinned while the panel changes state", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);
		const actions = screen.getByTestId("session-pinned-actions");
		const toggle = within(actions).getByRole("button", { name: "Close inspector panel" });
		const notification = within(actions).getByRole("button", { name: "Notifications" });
		const buttons = within(actions).getAllByRole("button");
		expect(buttons.indexOf(notification)).toBeGreaterThan(buttons.indexOf(toggle));
		expect(toggle).toHaveAttribute("aria-pressed", "true");

		fireEvent.click(toggle);

		expect(inspectorOpen("sess-1")).toBe(false);
		expect(screen.getByTestId("session-pinned-actions")).toBe(actions);
		expect(screen.getByRole("button", { name: "Notifications" })).toBe(notification);
		expect(screen.getByRole("button", { name: "Open inspector panel" })).toBe(toggle);
		expect(toggle).toHaveAttribute("aria-pressed", "false");
	});

	it("restores and clamps the persisted inspector width in pixels", () => {
		window.localStorage.setItem("ao.inspector.widthPx", "240");
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);
		expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("280px");
	});

	it("mounts the inspector in sync when navigating from an orchestrator session", () => {
		const { rerender } = render(<SessionView sessionId="sess-orch" />);
		expect(screen.queryByTestId("panel-inspector")).not.toBeInTheDocument();

		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		rerender(<SessionView sessionId="sess-1" />);

		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "expanded");
		expect(screen.getByTestId("panel-inspector")).not.toHaveAttribute("inert");
	});

	it("expands on the first toggle after a closed worker inspector remounts", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		act(() => useUiStore.getState().setInspectorOpen("sess-2", false));
		rerender(<SessionView sessionId="sess-orch" />);
		expect(screen.queryByTestId("panel-inspector")).not.toBeInTheDocument();

		act(() => useUiStore.getState().setInspectorOpen("sess-2", false));
		rerender(<SessionView sessionId="sess-2" />);
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "collapsed");

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });

		expect(inspectorOpen("sess-2")).toBe(true);
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "expanded");
	});

	it("renders no inspector panel or handle for orchestrator sessions", () => {
		render(<SessionView sessionId="sess-orch" />);

		expect(screen.queryByTestId("panel-inspector")).not.toBeInTheDocument();
		expect(screen.queryByTestId("inspector-resize-handle")).not.toBeInTheDocument();
		expect(screen.queryByTestId("inspector-collapsed-rail")).not.toBeInTheDocument();

		// The shortcut is inactive without an inspector.
		fireEvent.keyDown(window, { key: "B", metaKey: true, shiftKey: true });
		expect(useUiStore.getState().inspectorSessions["sess-orch"]).toBeUndefined();
	});

	it("maximizes the browser over the whole app window and returns to the rail", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);

		expect(screen.getByText("terminal center")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "pop browser" }));

		// The maximized overlay appears; the terminal stays mounted behind it.
		expect(screen.getByRole("button", { name: "browser center" })).toBeInTheDocument();
		expect(document.querySelector(".browser-popout-overlay")).toHaveClass("browser-popout-overlay--mac-windowed");
		expect(screen.getByText("terminal center")).toBeInTheDocument();

		fireEvent.click(screen.getByRole("button", { name: "browser center" }));
		expect(screen.queryByRole("button", { name: "browser center" })).not.toBeInTheDocument();
		expect(screen.getByText("terminal center")).toBeInTheDocument();
		expect(browserDestroy).not.toHaveBeenCalled();
	});

	it("does not reserve the traffic-light band during native macOS fullscreen", () => {
		nativeFullScreenMock.mockReturnValue(true);
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "pop browser" }));

		expect(document.querySelector(".browser-popout-overlay")).not.toHaveClass("browser-popout-overlay--mac-windowed");
	});

	it("does not carry popped-out browser visibility into the next session", () => {
		act(() => useUiStore.getState().setInspectorView("sess-1", "browser"));
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "pop browser" }));
		expect(browserViewOptions.current).toMatchObject({ sessionId: "sess-1", active: true });

		rerender(<SessionView sessionId="sess-2" />);

		expect(browserViewOptions.current).toMatchObject({ sessionId: "sess-2", active: false });
	});

	it("opens the files view in the inspector rail first", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "open files" }));

		expect(
			within(screen.getByTestId("panel-inspector")).getByRole("button", { name: "files rail" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "files center" })).not.toBeInTheDocument();
		expect(screen.getByText("terminal center")).toBeInTheDocument();
	});

	it("maximizes files over the whole app window and returns to the rail", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "open files" }));
		fireEvent.click(within(screen.getByTestId("panel-inspector")).getByRole("button", { name: "files rail" }));

		expect(screen.getByRole("button", { name: "files center" })).toBeInTheDocument();
		const overlay = document.querySelector(".files-popout-overlay");
		expect(overlay).toHaveClass("files-popout-overlay--mac-windowed");
		expect(overlay?.parentElement).toBe(document.body);
		expect(screen.getByText("terminal center")).toBeInTheDocument();

		fireEvent.click(screen.getByRole("button", { name: "files center" }));
		expect(screen.queryByRole("button", { name: "files center" })).not.toBeInTheDocument();
		expect(
			within(screen.getByTestId("panel-inspector")).getByRole("button", { name: "files rail" }),
		).toBeInTheDocument();
		expect(screen.getByText("terminal center")).toBeInTheDocument();
	});

	it("does not reserve the traffic-light band for maximized files during native macOS fullscreen", () => {
		nativeFullScreenMock.mockReturnValue(true);
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "open files" }));
		fireEvent.click(within(screen.getByTestId("panel-inspector")).getByRole("button", { name: "files rail" }));

		expect(document.querySelector(".files-popout-overlay")).not.toHaveClass("files-popout-overlay--mac-windowed");
	});

	it("opens Browser for a new live `ao preview` target", () => {
		const worker = workerSession("sess-1");
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		worker.previewUrl = "http://localhost:5173/";
		worker.previewRevision = 1;
		rerender(<SessionView sessionId="sess-1" />);

		expect(screen.getByText("terminal center")).toBeInTheDocument();
		expect(inspectorOpen("sess-1")).toBe(true);
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");
		expect(browserUnseen("sess-1")).toBe(false);
		expect(browserViewOptions.current).toMatchObject({ active: true });
	});

	it("opens a collapsed inspector when a new live preview arrives", () => {
		const worker = workerSession("sess-1");
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		worker.previewUrl = "http://localhost:5173/";
		worker.previewRevision = 1;
		rerender(<SessionView sessionId="sess-1" />);

		expect(inspectorOpen("sess-1")).toBe(true);
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");
		expect(browserUnseen("sess-1")).toBe(false);
		expect(browserViewOptions.current).toMatchObject({ active: true });
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "expanded");
	});

	it("keeps Summary on session entry and opens Browser for later preview work", () => {
		const secondWorker = workerSession("sess-2");
		secondWorker.previewUrl = "http://localhost:5173/";
		secondWorker.previewRevision = 1;

		const { rerender } = render(<SessionView sessionId="sess-1" />);

		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "expanded");
		expect(screen.getByTestId("panel-inspector")).not.toHaveAttribute("inert");
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");

		rerender(<SessionView sessionId="sess-2" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(browserUnseen("sess-2")).toBe(false);

		secondWorker.previewRevision = 2;
		rerender(<SessionView sessionId="sess-2" />);
		expect(inspectorOpen("sess-2")).toBe(true);
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");
		expect(browserUnseen("sess-2")).toBe(false);
	});

	it("keeps Summary selected when preview content arrives with the async workspace response", () => {
		const secondWorker = workerSession("sess-2");
		secondWorker.previewUrl = "http://localhost:5173/";
		secondWorker.previewRevision = 1;
		workspaceQueryState.data = undefined;
		workspaceQueryState.isLoading = true;

		const { rerender } = render(<SessionView sessionId="sess-2" />);

		workspaceQueryState.data = workspaces;
		workspaceQueryState.isLoading = false;
		rerender(<SessionView sessionId="sess-2" />);

		expect(inspectorOpen("sess-2")).toBe(true);
		expect(screen.getByTestId("panel-inspector")).not.toHaveAttribute("inert");
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(browserUnseen("sess-2")).toBe(false);
		expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "expanded");
	});

	it("glows for agent browser activity after the user leaves Browser", () => {
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		browserViewState.url = "http://localhost:4173/";
		rerender(<SessionView sessionId="sess-1" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");

		act(() => useUiStore.getState().setInspectorView("sess-1", "browser"));

		browserViewState.agentBrowserActive = true;
		rerender(<SessionView sessionId="sess-1" />);
		expect(browserUnseen("sess-1")).toBe(false);

		// Switching away during an already-running command must still mark the
		// Browser as unseen; it should not wait for another command transition.
		act(() => useUiStore.getState().setInspectorView("sess-1", "summary"));

		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(browserUnseen("sess-1")).toBe(true);
	});

	it("does not glow for preview or agent activity while Browser is visible as a popout", () => {
		const worker = workerSession("sess-1");
		worker.previewUrl = "http://localhost:4173/";
		worker.previewRevision = 1;
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "pop browser" }));
		expect(screen.getByRole("button", { name: "browser center" })).toBeInTheDocument();
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));

		worker.previewRevision = 2;
		browserViewState.agentBrowserActive = true;
		rerender(<SessionView sessionId="sess-1" />);

		expect(browserUnseen("sess-1")).toBe(false);
	});

	it("does not let a previous session's popout consume the destination session's preview glow", () => {
		const secondWorker = workerSession("sess-2");
		secondWorker.previewUrl = "http://localhost:4173/";
		secondWorker.previewRevision = 2;
		act(() => {
			useUiStore.setState({
				inspectorSessions: {
					"sess-2": {
						isOpen: true,
						view: "summary",
						browserContentRevealed: true,
					},
				},
			});
		});
		const { rerender } = render(<SessionView sessionId="sess-1" />);
		fireEvent.click(screen.getByRole("button", { name: "pop browser" }));
		expect(screen.getByRole("button", { name: "browser center" })).toBeInTheDocument();

		rerender(<SessionView sessionId="sess-2" />);

		expect(browserUnseen("sess-2")).toBe(false);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
	});

	it("does not open Browser when `ao preview clear` removes the target", () => {
		const worker = workerSession("sess-1");
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		worker.previewUrl = "http://localhost:5173/";
		worker.previewRevision = 1;
		rerender(<SessionView sessionId="sess-1" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");
		expect(browserUnseen("sess-1")).toBe(false);

		act(() => {
			useUiStore.getState().setInspectorView("sess-1", "summary");
			useUiStore.getState().setInspectorOpen("sess-1", false);
		});

		worker.previewUrl = undefined;
		worker.previewRevision = 2;
		rerender(<SessionView sessionId="sess-1" />);

		expect(inspectorOpen("sess-1")).toBe(false);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(browserUnseen("sess-1")).toBe(false);

		worker.previewUrl = "http://localhost:3000/";
		worker.previewRevision = 3;
		rerender(<SessionView sessionId="sess-1" />);

		expect(inspectorOpen("sess-1")).toBe(true);
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");
		expect(browserUnseen("sess-1")).toBe(false);
	});

	// Regression: a terminated session's `previewUrl` is a stale DB fact —
	// useBrowserView suppresses and destroys the live preview for terminated
	// sessions, so it must not count as active Browser content.
	it("keeps Summary selected for a terminated session with a stale previewUrl", () => {
		const worker = workerSession("sess-1");
		worker.status = "merged";
		worker.isTerminated = true;
		worker.previewUrl = "http://localhost:5173/";
		worker.previewRevision = 1;

		render(<SessionView sessionId="sess-1" />);

		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(useUiStore.getState().inspectorSessions["sess-1"]?.browserContentRevealed).toBeFalsy();
	});

	// Regression: agent-browser commands (fill, click, snapshot, …) are real
	// activity even on an empty target that has not navigated anywhere yet.
	// Gating the glow on hasBrowserContent/browserContentRevealed meant a
	// command run before any page loaded never surfaced as unseen.
	it("glows for agent browser activity even before any browser content has loaded", () => {
		const { rerender } = render(<SessionView sessionId="sess-1" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");

		browserViewState.agentBrowserActive = true;
		rerender(<SessionView sessionId="sess-1" />);

		expect(browserUnseen("sess-1")).toBe(true);

		// An explicit clear still resets unseen activity when the target was
		// already empty, so only previewRevision changes.
		browserViewState.agentBrowserActive = false;
		workerSession("sess-1").previewRevision = 1;
		rerender(<SessionView sessionId="sess-1" />);
		expect(browserUnseen("sess-1")).toBe(false);
	});
});
