import { StrictMode, type ReactNode, type Ref } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render as rtlRender, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { SessionView } from "./SessionView";
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
}));
const interfaceTransitionState = vi.hoisted(() => ({
	status: undefined as
		| { supported: boolean; targetMode?: "chat" | "tui"; reason?: string }
		| undefined,
}));
const reviewGetMock = vi.hoisted(() => vi.fn());

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
	useSessionInterfaceTransition: () => ({
		status: interfaceTransitionState.status,
		transition: undefined,
		isLoading: false,
		statusError: undefined,
		start: interfaceTransitionMock.start,
		starting: false,
		startError: undefined,
		resetStartError: interfaceTransitionMock.resetStartError,
		cancel: interfaceTransitionMock.cancel,
		cancelling: false,
		cancelError: undefined,
	}),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: reviewGetMock,
	},
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

type FakePanelHandle = {
	collapse: Mock;
	expand: Mock;
	getSize: Mock;
	isCollapsed: Mock;
	resize: Mock;
};

type PanelEntry = {
	handle: FakePanelHandle;
	onResize?: (size: { asPercentage: number; inPixels: number }) => void;
};

const { workspaces, workspaceQueryState, panels, shellTerminalsState } = vi.hoisted(() => {
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
	return { workspaces, workspaceQueryState, panels: new Map<string, PanelEntry>(), shellTerminalsState };
});

// The terminal and inspector body pull in xterm/SSE machinery irrelevant to
// the split under test. (ShellTopbar is shell-owned on Win/Linux; when the
// platform hides the shell topbar, SessionView mounts it in-panel.)
vi.mock("./ShellTopbar", () => ({ ShellTopbar: () => null }));
vi.mock("./chat/SessionChatSurface", () => ({
	SessionChatSurface: ({ onOpenShell, headerActions }: { onOpenShell?: () => void; headerActions?: ReactNode }) => (
		<div data-testid="chat-surface">
			chat surface
			{headerActions}
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
		onNewShellTerminal,
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
		onNewShellTerminal?: () => void;
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
			<button type="button" onClick={() => onNewShellTerminal?.()}>
				new terminal
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
vi.mock("./PendingReviewCommentsContext", () => ({
	PendingReviewCommentsProvider: ({ children }: { children: ReactNode }) => children,
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
		onOpenFiles,
		onToggleBrowserPopOut,
		view,
	}: {
		filesView?: ReactNode;
		onOpenFiles?: () => void;
		onToggleBrowserPopOut?: () => void;
		view?: string;
	}) => (
		<div>
			<button type="button" data-view={view} onClick={onToggleBrowserPopOut}>
				pop browser
			</button>
			<button type="button" onClick={onOpenFiles}>
				open files
			</button>
			{view === "files" ? filesView : null}
		</div>
	),
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

// jsdom has no layout engine, so the real react-resizable-panels would never
// produce meaningful sizes — record the props SessionView passes and expose a
// fake imperative handle per panel instead.
vi.mock("./ui/resizable", () => ({
	ResizablePanelGroup: ({ children }: { children?: ReactNode }) => <div>{children}</div>,
	ResizableHandle: ({ elementRef }: { elementRef?: Ref<HTMLDivElement | null> }) => (
		<div
			data-separator="inactive"
			data-testid="resize-handle"
			ref={(el) => {
				if (elementRef && typeof elementRef === "object") {
					(elementRef as { current: HTMLDivElement | null }).current = el;
				}
			}}
		/>
	),
	ResizablePanel: ({
		children,
		id,
		defaultSize,
		minSize,
		maxSize,
		collapsible,
		panelRef,
		onResize,
		style: _style,
		...rest
	}: {
		children?: ReactNode;
		id: string;
		defaultSize?: number | string;
		minSize?: number | string;
		maxSize?: number | string;
		collapsible?: boolean;
		panelRef?: Ref<FakePanelHandle | null>;
		onResize?: (size: { asPercentage: number; inPixels: number }) => void;
		style?: React.CSSProperties;
	}) => {
		let entry = panels.get(id);
		if (!entry) {
			entry = {
				handle: {
					collapse: vi.fn(),
					expand: vi.fn(),
					getSize: vi.fn(() => ({ asPercentage: 28, inPixels: 280 })),
					isCollapsed: vi.fn(() => false),
					resize: vi.fn(),
				},
			};
			panels.set(id, entry);
		}
		entry.onResize = onResize;
		if (panelRef && typeof panelRef === "object") {
			(panelRef as { current: FakePanelHandle | null }).current = entry.handle;
		}
		return (
			<div data-testid={`panel-${id}`} data-collapsible={collapsible ? "true" : undefined} {...rest}>
				<span data-testid={`panel-${id}-sizes`}>
					{JSON.stringify([defaultSize, minSize, maxSize].filter((s) => s !== undefined))}
				</span>
				{children}
			</div>
		);
	},
}));

function panelSizes(id: string): unknown[] {
	return JSON.parse(screen.getByTestId(`panel-${id}-sizes`).textContent ?? "[]") as unknown[];
}

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
			wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
		}),
		client,
	};
}

describe("SessionView", () => {
	beforeEach(() => {
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
		panels.clear();
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

		act(() => useUiStore.getState().setActiveShellTerminal("chat-shell"));
		expect(screen.getByText("terminal center")).toBeInTheDocument();
		expect(screen.queryByTestId("chat-surface")).not.toBeInTheDocument();

		fireEvent.click(screen.getByRole("button", { name: "select agent tab" }));
		expect(screen.getByTestId("chat-surface")).toBeInTheDocument();
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

		fireEvent.click(screen.getByRole("button", { name: "new terminal" }));
		expect(openShellTerminalMock).toHaveBeenCalledWith({ projectId: "proj-1", sessionId: "sess-2" }, expect.anything());
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

		fireEvent.click(screen.getByRole("button", { name: "open shell from chat" }));
		expect(screen.getByText("terminal center")).toBeInTheDocument();
		expect(screen.getByTestId("shell-tabs")).toHaveTextContent("chat shell");

		fireEvent.click(screen.getByRole("button", { name: "select agent tab" }));
		expect(screen.getByText("chat surface")).toBeInTheDocument();
	});

	it.each([
		["Terminal UI worker", "sess-1", "tui", "chat", "Switch to chat UI"],
		["Terminal UI orchestrator", "sess-orch", "tui", "chat", "Switch to chat UI"],
		["Chat worker", "sess-1", "chat", "tui", "Switch to terminal UI"],
		["Chat orchestrator", "sess-orch", "chat", "tui", "Switch to terminal UI"],
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

	it.each([
		["working status", "sess-1", "tui", "chat", "Switch to chat UI", "working", "idle"],
		["needs-input status", "sess-orch", "tui", "chat", "Switch to chat UI", "needs_input", "idle"],
		["active activity", "sess-1", "chat", "tui", "Switch to terminal UI", "idle", "active"],
		["waiting-input activity", "sess-orch", "chat", "tui", "Switch to terminal UI", "idle", "waiting_input"],
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

	it("restores the selected reviewer terminal when the session becomes active again", async () => {
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

		worker.status = "terminated";
		worker.isTerminated = true;
		view.rerender(<SessionView sessionId="sess-1" />);
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("reviewer");
		expect(screen.queryByRole("button", { name: "select reviewer tab" })).not.toBeInTheDocument();

		worker.status = "working";
		worker.isTerminated = false;
		view.rerender(<SessionView sessionId="sess-1" />);

		await screen.findByRole("button", { name: "select reviewer tab" });
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("reviewer");
	});

	// Regression: react-resizable-panels v4 treats bare numeric sizes as PIXELS
	// (numbers were percentages in the older API the shadcn examples use).
	// defaultSize={28}/maxSize={45} clamped the inspector rail to a 45px sliver.
	// Every size must be an explicit percentage string.
	it("sizes the terminal/inspector split in percentages, not pixels", () => {
		render(<SessionView sessionId="sess-1" />);

		for (const panelId of ["terminal", "inspector"]) {
			const sizes = panelSizes(panelId);
			expect(sizes.length).toBeGreaterThan(0);
			for (const size of sizes) {
				expect(size, `${panelId} size ${String(size)} must be a percentage string`).toMatch(/^\d+(\.\d+)?%$/);
			}
		}
	});

	it("opens the Summary inspector alongside the terminal by default", () => {
		render(<SessionView sessionId="sess-1" />);

		expect(screen.getByText("terminal center")).toBeInTheDocument();
		expect(panelSizes("inspector")[0]).toBe("30%");
		// Open panels are non-collapsible so a drag clamps at minSize instead of
		// snapping the rail away; only the closed panel is collapsible.
		expect(screen.getByTestId("panel-inspector")).not.toHaveAttribute("data-collapsible");
		expect(screen.getByTestId("resize-handle")).toBeInTheDocument();
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

		expect(panelSizes("inspector")[0]).toMatch(/^[1-9]\d*(\.\d+)?%$/);
		const pane = screen.getByTestId("panel-inspector");
		expect(pane).not.toHaveAttribute("inert");
		expect(pane).toHaveAttribute("aria-hidden", "false");
		expect(panels.get("inspector")!.handle.expand).not.toHaveBeenCalled();
	});

	it("mounts collapsed and inert when the store says closed", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		render(<SessionView sessionId="sess-1" />);

		expect(panelSizes("inspector")[0]).toBe("0%");
		const pane = screen.getByTestId("panel-inspector");
		expect(pane).toHaveAttribute("inert");
		expect(pane).toHaveAttribute("aria-hidden", "true");
		// Collapsed panels stay collapsible so the 0% size is a valid rrp state
		// (and the separator can drag the rail back open).
		expect(pane).toHaveAttribute("data-collapsible", "true");
		expect(panels.get("inspector")!.handle.collapse).not.toHaveBeenCalled();
	});

	it("keeps StrictMode mount imperative-free and collapses on the first user toggle", () => {
		render(
			<StrictMode>
				<SessionView sessionId="sess-1" />
			</StrictMode>,
		);
		const handle = panels.get("inspector")!.handle;

		expect(handle.expand).not.toHaveBeenCalled();
		expect(handle.collapse).not.toHaveBeenCalled();

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });

		expect(inspectorOpen("sess-1")).toBe(false);
		expect(handle.collapse).toHaveBeenCalledTimes(1);
		expect(handle.expand).not.toHaveBeenCalled();
	});

	it("keeps StrictMode mount imperative-free and expands on the first user toggle", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		render(
			<StrictMode>
				<SessionView sessionId="sess-1" />
			</StrictMode>,
		);
		const handle = panels.get("inspector")!.handle;

		expect(handle.resize).not.toHaveBeenCalled();
		expect(handle.collapse).not.toHaveBeenCalled();

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });

		expect(inspectorOpen("sess-1")).toBe(true);
		// Opening resizes to the persisted split rather than expand(): the open
		// panel re-registers as non-collapsible, and rrp's expand() no-ops on a
		// non-collapsible panel.
		expect(handle.resize).toHaveBeenCalledWith("30%");
		expect(handle.collapse).not.toHaveBeenCalled();
	});

	it("toggles the inspector with mod+shift+B through the imperative panel API", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);
		const handle = panels.get("inspector")!.handle;

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });
		expect(inspectorOpen("sess-1")).toBe(false);
		expect(handle.collapse).toHaveBeenCalledTimes(1);

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });
		expect(inspectorOpen("sess-1")).toBe(true);
		expect(handle.resize).toHaveBeenCalledWith("30%");

		// Plain ⌘B belongs to the sidebar — the inspector must not react.
		fireEvent.keyDown(window, { key: "b", metaKey: true });
		expect(inspectorOpen("sess-1")).toBe(true);
	});

	it("persists drag resizes and never closes the store from a drag", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);
		const entry = panels.get("inspector")!;
		// rrp marks the separator active for the duration of a pointer drag.
		screen.getByTestId("resize-handle").setAttribute("data-separator", "active");

		// Dragging persists the width.
		act(() => entry.onResize?.({ asPercentage: 31.5, inPixels: 400 }));
		expect(inspectorOpen("sess-1")).toBe(true);
		expect(window.localStorage.getItem("ao.inspector.split")).toBe("31.5");

		// A drag can never auto-collapse the rail: even if a 0-size frame arrives
		// mid-drag, the store stays open — collapse belongs to the explicit
		// controls (topbar button / ⌘⇧B) only.
		act(() => entry.onResize?.({ asPercentage: 0, inPixels: 0 }));
		expect(inspectorOpen("sess-1")).toBe(true);
		expect(window.localStorage.getItem("ao.inspector.split")).toBe("31.5");
	});

	it("reopens the store when a drag pulls the collapsed rail back open", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		render(<SessionView sessionId="sess-1" />);
		const entry = panels.get("inspector")!;
		screen.getByTestId("resize-handle").setAttribute("data-separator", "active");

		act(() => entry.onResize?.({ asPercentage: 25, inPixels: 320 }));

		expect(useUiStore.getState().inspectorSessions["sess-1"]).toMatchObject({ isOpen: true });
		expect(window.localStorage.getItem("ao.inspector.split")).toBe("25");
	});

	// Regression: rrp v4 reports observed DOM sizes, so the flex-grow
	// transition animating an imperative collapse fires onResize with transient
	// non-zero sizes. Mirroring those into the store re-opened the panel
	// mid-animation — the topbar toggle looked dead and a mount-time 0-size
	// event flipped a fresh profile to collapsed. Only drag events (separator
	// active) may write back.
	it("ignores onResize churn while the separator is not being dragged", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);
		const entry = panels.get("inspector")!;

		// Mount-time/layout event at 0% must not collapse the store…
		act(() => entry.onResize?.({ asPercentage: 0, inPixels: 0 }));
		expect(inspectorOpen("sess-1")).toBe(true);

		// …and a mid-collapse transition frame must not re-open or persist.
		act(() => useUiStore.getState().toggleInspector("sess-1"));
		act(() => entry.onResize?.({ asPercentage: 12.4, inPixels: 160 }));
		expect(inspectorOpen("sess-1")).toBe(false);
		expect(window.localStorage.getItem("ao.inspector.split")).toBeNull();
	});

	it("restores the persisted split width", () => {
		window.localStorage.setItem("ao.inspector.split", "40");
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);
		expect(panelSizes("inspector")[0]).toBe("40%");
	});

	// Regression: rrp only derives a panel's constraints one commit after it
	// registers into a live group. Driving the imperative API in the commit
	// where the inspector mounts (orchestrator → worker navigation; SessionView
	// itself stays mounted) threw "Panel constraints not found for Panel
	// inspector" and unwound the route to the error boundary. The panel must
	// mount already in sync via defaultSize instead.
	it("mounts the inspector in sync when navigating from an orchestrator session, without the imperative API", () => {
		const { rerender } = render(<SessionView sessionId="sess-orch" />);
		expect(screen.queryByTestId("panel-inspector")).not.toBeInTheDocument();

		// Already-open worker state — the panel that mounts later must pick this
		// up from defaultSize alone.
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		rerender(<SessionView sessionId="sess-1" />);

		expect(panelSizes("inspector")[0]).toMatch(/^[1-9]\d*(\.\d+)?%$/);
		const handle = panels.get("inspector")!.handle;
		expect(handle.expand).not.toHaveBeenCalled();
		expect(handle.collapse).not.toHaveBeenCalled();
		expect(handle.resize).not.toHaveBeenCalled();
	});

	it("expands on the first toggle after a closed worker inspector remounts", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		const { rerender } = render(<SessionView sessionId="sess-1" />);
		const handle = panels.get("inspector")!.handle;

		act(() => useUiStore.getState().setInspectorOpen("sess-2", false));
		rerender(<SessionView sessionId="sess-orch" />);
		expect(screen.queryByTestId("panel-inspector")).not.toBeInTheDocument();

		act(() => useUiStore.getState().setInspectorOpen("sess-2", false));
		rerender(<SessionView sessionId="sess-2" />);
		expect(panelSizes("inspector")[0]).toBe("0%");
		expect(handle.collapse).not.toHaveBeenCalled();

		fireEvent.keyDown(window, { key: "B", ctrlKey: true, shiftKey: true });

		expect(inspectorOpen("sess-2")).toBe(true);
		expect(handle.resize).toHaveBeenCalledWith("30%");
	});

	it("renders no inspector panel or handle for orchestrator sessions", () => {
		render(<SessionView sessionId="sess-orch" />);

		expect(screen.queryByTestId("panel-inspector")).not.toBeInTheDocument();
		expect(screen.queryByTestId("resize-handle")).not.toBeInTheDocument();

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

	it("opens the Browser tab for a new `ao preview` target without replacing the terminal", () => {
		const worker = workerSession("sess-1");
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		worker.previewUrl = "http://localhost:5173/";
		worker.previewRevision = 1;
		rerender(<SessionView sessionId="sess-1" />);

		// Browser opens in the inspector rail, not as a center-pane popout.
		expect(screen.getByText("terminal center")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "browser center" })).not.toBeInTheDocument();
		expect(inspectorOpen("sess-1")).toBe(true);
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");
		expect(browserViewOptions.current).toMatchObject({ active: true });
	});

	it("expands a collapsed inspector when a new preview arrives", () => {
		const worker = workerSession("sess-1");
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		const { rerender } = render(<SessionView sessionId="sess-1" />);
		const handle = panels.get("inspector")!.handle;

		worker.previewUrl = "http://localhost:5173/";
		worker.previewRevision = 1;
		rerender(<SessionView sessionId="sess-1" />);

		expect(inspectorOpen("sess-1")).toBe(true);
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");
		expect(browserUnseen("sess-1")).toBe(false);
		expect(browserViewOptions.current).toMatchObject({ active: true });
		expect(handle.resize).toHaveBeenCalledWith("30%");
	});

	it("auto-opens first content, then glows for later preview work after the user leaves Browser", () => {
		const secondWorker = workerSession("sess-2");
		secondWorker.previewUrl = "http://localhost:5173/";
		secondWorker.previewRevision = 1;

		const { rerender } = render(<SessionView sessionId="sess-1" />);

		expect(panelSizes("inspector")[0]).toBe("30%");
		expect(screen.getByTestId("panel-inspector")).not.toHaveAttribute("inert");
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");

		// The first content observed for this worker is immediately revealed.
		rerender(<SessionView sessionId="sess-2" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");
		expect(browserUnseen("sess-2")).toBe(false);

		// Once the user deliberately leaves Browser, later work must not steal
		// the tab back. It marks Browser as unseen instead.
		act(() => useUiStore.getState().setInspectorView("sess-2", "summary"));

		secondWorker.previewRevision = 2;
		rerender(<SessionView sessionId="sess-2" />);
		expect(inspectorOpen("sess-2")).toBe(true);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(browserUnseen("sess-2")).toBe(true);

		act(() => useUiStore.getState().setInspectorView("sess-2", "browser"));
		expect(browserUnseen("sess-2")).toBe(false);
	});

	it("opens Browser when the first preview arrives with the async workspace response", () => {
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
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");
		const handle = panels.get("inspector")!.handle;
		expect(handle.expand).not.toHaveBeenCalled();
	});

	it("glows for agent browser activity after the user leaves first content", () => {
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		browserViewState.url = "http://localhost:4173/";
		rerender(<SessionView sessionId="sess-1" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");

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
						previewKey: "revision:1",
						browserContentRevealed: true,
					},
				},
			});
		});
		const { rerender } = render(<SessionView sessionId="sess-1" />);
		fireEvent.click(screen.getByRole("button", { name: "pop browser" }));
		expect(screen.getByRole("button", { name: "browser center" })).toBeInTheDocument();

		rerender(<SessionView sessionId="sess-2" />);

		expect(browserUnseen("sess-2")).toBe(true);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
	});

	it("does not open Browser when `ao preview clear` removes the target", () => {
		const worker = workerSession("sess-1");
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		worker.previewUrl = "http://localhost:5173/";
		worker.previewRevision = 1;
		rerender(<SessionView sessionId="sess-1" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");

		act(() => useUiStore.getState().setInspectorView("sess-1", "summary"));
		worker.previewUrl = undefined;
		worker.previewRevision = 2;
		rerender(<SessionView sessionId="sess-1" />);

		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(browserUnseen("sess-1")).toBe(false);

		worker.previewUrl = "http://localhost:3000/";
		worker.previewRevision = 3;
		rerender(<SessionView sessionId="sess-1" />);

		// Clearing starts a fresh content lifecycle, so its next first target
		// automatically opens again.
		expect(inspectorButton()).toHaveAttribute("data-view", "browser");
	});

	// Regression: a terminated session's `previewUrl` is a stale DB fact —
	// useBrowserView suppresses and destroys the live preview for terminated
	// sessions, so it must not count as content that auto-opens Browser either.
	it("does not auto-open Browser for a terminated session with a stale previewUrl", () => {
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
