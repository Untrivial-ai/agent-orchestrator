import { StrictMode, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render as rtlRender, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SessionView } from "./SessionView";
import { SessionTopbarProvider } from "./SessionTopbarPortal";
import { TooltipProvider } from "./ui/tooltip";
import type { SessionInterfaceTransitionStatus } from "../hooks/useSessionInterfaceTransition";
import { useUiStore, type InspectorView } from "../stores/ui-store";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { setChatDraftBoundary } from "../lib/chat-draft-boundary";
import { chatDraftScopeKey } from "../lib/chat-drafts";
import { useFileAttachments, type FileAttachment } from "../hooks/useFileAttachments";

const navigateMock = vi.hoisted(() => vi.fn());
const openShellTerminalMock = vi.hoisted(() => vi.fn());
const closeShellTerminalMock = vi.hoisted(() => vi.fn());
const nativeFullScreenMock = vi.hoisted(() => vi.fn(() => false));
const interfaceTransitionMock = vi.hoisted(() => ({
	start: vi.fn(),
	refreshStatus: vi.fn(),
	resetStartError: vi.fn(),
	cancel: vi.fn(),
	acknowledgeNotice: vi.fn(),
}));
const interfaceTransitionState = vi.hoisted(() => ({
	starting: false,
	settling: false,
	startError: undefined as string | undefined,
	status: undefined as SessionInterfaceTransitionStatus | undefined,
}));
const reviewGetMock = vi.hoisted(() => vi.fn());
const inspectorVisibilityRenders = vi.hoisted(() => [] as boolean[]);
const chatSurfaceWorkState = vi.hoisted(() => ({
	controllerBusy: false,
	hasRunningTurn: false,
	queuedTurnCount: 0,
}));
const codexAccountsQueryState = vi.hoisted(() => ({ data: undefined as unknown }));
const recoverCodexAccountSwitchMock = vi.hoisted(() => vi.fn());

async function chooseSessionAction(name: string) {
	const user = userEvent.setup();
	await user.click(screen.getByRole("button", { name: "Session actions" }));
	await user.click(await screen.findByRole("menuitem", { name }));
}
const routeBlockerState = vi.hoisted(() => ({
	options: undefined as
		| {
				disabled: boolean;
				enableBeforeUnload: boolean;
				shouldBlockFn: () => boolean | Promise<boolean>;
			}
		| undefined,
}));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
	useBlocker: (options: NonNullable<typeof routeBlockerState.options>) => {
		routeBlockerState.options = options;
	},
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
	interfaceTransitionIsActive: (transition?: { phase?: string }) =>
		Boolean(
			transition &&
				[
					"requested",
					"preflighting",
					"draining",
					"source_stopping",
					"source_stopped",
					"target_starting",
					"activating",
				].includes(transition.phase ?? ""),
		),
	interfaceTransitionIsCancellable: (transition?: { phase?: string }) =>
		Boolean(
			transition && ["requested", "preflighting", "draining"].includes(transition.phase ?? ""),
		),
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
		refreshStatus: interfaceTransitionMock.refreshStatus,
		starting: interfaceTransitionState.starting,
		settling: interfaceTransitionState.settling,
		startError: interfaceTransitionState.startError,
		resetStartError: interfaceTransitionMock.resetStartError,
		cancel: interfaceTransitionMock.cancel,
		cancelling: false,
		cancelError: undefined,
		acknowledgeNotice: interfaceTransitionMock.acknowledgeNotice,
		acknowledgingNotice: false,
		acknowledgeNoticeError: undefined,
	}),
}));

vi.mock("../hooks/useCodexAccountsQuery", () => ({
	useCodexAccountsQuery: () => ({ data: codexAccountsQueryState.data, isLoading: false }),
}));

vi.mock("../hooks/useCodexAccountActions", () => ({
	useCodexAccountActions: () => ({
		error: null,
		recoverPending: false,
		recoverSwitch: recoverCodexAccountSwitchMock,
	}),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: reviewGetMock,
	},
	apiErrorCode: (error: { code?: string }) => error.code,
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
	ShellTopbar: ({
		sessionAction,
		compactActions,
	}: {
		sessionAction?: ReactNode;
		compactActions?: boolean;
	}) => (
		<div data-compact-actions={compactActions ? "true" : "false"} data-testid="mock-session-topbar">
			{sessionAction}
		</div>
	),
}));
vi.mock("./NotificationCenter", () => ({
	NotificationCenter: () => <button type="button">Notifications</button>,
}));
vi.mock("../hooks/useSessionHandoffMenu", () => ({
	useSessionHandoffMenu: () => ({
		agentSwitch: undefined,
		switchControlPresentation: undefined,
		switchError: null,
	}),
}));
vi.mock("./TerminalSwitchAgentButton", () => ({
	TerminalSwitchAgentButton: ({ variant }: { variant?: "icon" | "menu-item" }) =>
		variant === "menu-item" ? null : <button aria-label="Switch agent" type="button" />,
}));
vi.mock("./chat/SessionChatSurface", () => ({
	SessionChatSurface: ({
		session,
		onOpenShell,
		onOpenFile,
		headerActions,
		sessionTabAction,
		tabStripAction,
		reviewerTerminal,
		onOpenReviewerTerminal,
		reviewerTarget,
		onSelectChat,
		controllerTransitioning,
		shellTerminals = [],
		shellTarget,
		onSelectShellTerminal,
		onCloseShellTerminal,
		workspaceTabs,
		workspaceTabActions,
		newWorkDisabled,
		onConversationWorkChange,
		auxiliaryTabOrder,
		onAuxiliaryTabOrderChange,
	}: {
		session: WorkspaceSession;
		onOpenShell?: () => void;
		onOpenFile?: (path: string) => void;
		headerActions?: ReactNode;
		sessionTabAction?: ReactNode;
		tabStripAction?: ReactNode;
		reviewerTerminal?: { handleId: string; harness: string };
		onOpenReviewerTerminal?: (target: { handleId: string; harness: string }) => void;
		reviewerTarget?: { kind: "reviewer"; handleId: string; harness: string; sessionId: string };
		onSelectChat?: () => void;
		controllerTransitioning?: boolean;
		shellTerminals?: Array<{ handleId: string; title: string }>;
		shellTarget?: { kind: "shell"; handleId: string };
		onSelectShellTerminal?: (handleId: string) => void;
		onCloseShellTerminal?: (handleId: string) => void;
		workspaceTabs?: Array<{ key: string; content: ReactNode; onSelect: () => void }>;
		workspaceTabActions?: ReactNode;
		newWorkDisabled?: boolean;
		onConversationWorkChange?: (state: typeof chatSurfaceWorkState) => void;
		auxiliaryTabOrder?: string[];
		onAuxiliaryTabOrderChange?: (keys: string[]) => void;
	}) => (
		<div
			data-testid="chat-surface"
			data-transitioning={controllerTransitioning ? "true" : "false"}
			data-new-work-disabled={newWorkDisabled ? "true" : "false"}
		>
			chat surface
			<div data-testid={`auxiliary-tab-order-${session.id}`}>
				{auxiliaryTabOrder?.join("|") ?? ""}
			</div>
			<button
				type="button"
				onClick={() => onAuxiliaryTabOrderChange?.(["sh-a", "file:src/panel.tsx"])}
			>
				reorder auxiliary tabs
			</button>
			<button
				type="button"
				onClick={() =>
					onAuxiliaryTabOrderChange?.(["sh-a", "reviewer:review-sess-1", "file:src/panel.tsx"])
				}
			>
				reorder reviewer tab
			</button>
			<button
				type="button"
				onClick={() => onAuxiliaryTabOrderChange?.(["file:src/panel.tsx", "sh-a"])}
			>
				reorder visible tabs
			</button>
			{headerActions}
			{sessionTabAction}
			<div role="tablist">
				{workspaceTabs?.map((tab) => <div key={tab.key}>{tab.content}</div>)}
				{workspaceTabActions}
				{tabStripAction}
			</div>
			{onOpenFile ? (
				<button type="button" onClick={() => onOpenFile("notes.txt")}>
					open chat basename
				</button>
			) : null}
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
					<div key={shell.handleId}>
						<button onClick={() => onSelectShellTerminal?.(shell.handleId)} type="button">
							{shell.title}
						</button>
						<button onClick={() => onCloseShellTerminal?.(shell.handleId)} type="button">
							close {shell.title}
						</button>
					</div>
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
			<button type="button" onClick={() => onConversationWorkChange?.({ ...chatSurfaceWorkState })}>
				report chat work
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
		sessionTabAction,
		tabStripAction,
		workspaceTabs,
		workspaceTabActions,
		reviewerTerminal,
		terminalTarget,
		auxiliaryTabOrder,
	}: {
		session?: WorkspaceSession;
		shellTerminals?: Array<{ handleId: string; title: string }>;
		onCloseShellTerminal?: (handleId: string) => void;
		onSelectShellTerminal?: (handleId: string) => void;
		onSelectSessionTerminal?: () => void;
		onSelectReviewerTerminal?: (target: { handleId: string; harness: string }) => void;
		topbarActions?: ReactNode;
		sessionTabAction?: ReactNode;
		tabStripAction?: ReactNode;
		workspaceTabs?: Array<{ key: string; content: ReactNode; onSelect: () => void }>;
		workspaceTabActions?: ReactNode;
		reviewerTerminal?: { handleId: string; harness: string };
		terminalTarget?: { kind: string; handleId?: string };
		auxiliaryTabOrder?: string[];
	}) => (
		<div>
			terminal center
			<div data-testid={`auxiliary-tab-order-tui-${session?.id ?? "none"}`}>
				{auxiliaryTabOrder?.join("|") ?? ""}
			</div>
			{topbarActions}
			{sessionTabAction}
			<div role="tablist">
				{workspaceTabs?.map((tab) => <div key={tab.key}>{tab.content}</div>)}
				{workspaceTabActions}
				{tabStripAction}
			</div>
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
vi.mock("./SessionFileExplorer", () => ({
	SessionFileExplorer: ({
		isMaximized,
		onOpenFile,
		onToggleMaximized,
		revealRequest,
	}: {
		isMaximized?: boolean;
		onOpenFile?: (path: string) => void;
		onToggleMaximized?: (next: boolean) => void;
		revealRequest?: { path: string; key: number } | null;
	}) => (
		<div>
			<button type="button" onClick={() => onToggleMaximized?.(!isMaximized)}>
				{isMaximized ? "files center" : "files rail"}
			</button>
			{!isMaximized && onOpenFile ? (
				<>
					<span>{revealRequest ? `rail preview ${revealRequest.path}` : "file tree"}</span>
					<button type="button">select src/App.tsx</button>
					<button type="button" onClick={() => onOpenFile("src/App.tsx")}>
						pop out src/App.tsx
					</button>
					{revealRequest ? (
						<button type="button" onClick={() => onOpenFile(revealRequest.path)}>
							pop out {revealRequest.path}
						</button>
					) : null}
				</>
			) : null}
		</div>
	),
}));
vi.mock("./SessionFileWorkspace", () => ({
	SessionFileWorkspace: ({ path }: { path: string }) => <div data-testid="session-file-workspace">{path}</div>,
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
		onOpenReviewFile,
		onToggleBrowserPopOut,
		onViewChange,
		view,
	}: {
		filesView?: ReactNode;
		isInspectorVisible?: boolean;
		onOpenFiles?: () => void;
		onOpenReviewFile?: (target: { line?: number; path: string }) => void;
		onToggleBrowserPopOut?: (next: boolean, sourceRect?: DOMRectReadOnly) => void;
		onViewChange?: (view: InspectorView) => void;
		view?: string;
	}) => {
		inspectorVisibilityRenders.push(isInspectorVisible);
		return (
			<div>
				<button role="tab" type="button" onClick={() => onViewChange?.("summary")}>
					Summary
				</button>
				<button role="tab" type="button" onClick={() => onViewChange?.("reviews")}>
					Reviews
				</button>
				<button role="tab" type="button" onClick={() => onViewChange?.("browser")}>
					Browser
				</button>
				<div data-browser-dock-target="">
					<button
						type="button"
						data-view={view}
						onClick={(event) =>
							onToggleBrowserPopOut?.(true, event.currentTarget.parentElement?.getBoundingClientRect())
						}
					>
						pop browser
					</button>
				</div>
				<button type="button" onClick={onOpenFiles}>
					open files
				</button>
				<button type="button" onClick={() => onOpenReviewFile?.({ path: "src/panel.tsx", line: 42 })}>
					view review file
				</button>
				<button type="button" onClick={() => onOpenReviewFile?.({ path: "notes.txt" })}>
					view review basename
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
	useWorkspaceSession: (sessionId: string) => ({
		data: workspaceQueryState.data
			?.flatMap((workspace) => workspace.sessions)
			.find((session) => session.id === sessionId),
		isLoading: workspaceQueryState.isLoading,
	}),
}));
// Standalone shell terminals are orthogonal to the split under test, and their
// real hooks would need a QueryClientProvider this suite deliberately omits.
vi.mock("../hooks/useShellTerminals", () => ({
	useShellTerminals: () => ({ data: shellTerminalsState.data, isLoading: false }),
	useOpenShellTerminal: () => ({ open: openShellTerminalMock, isPending: false }),
	useCloseShellTerminal: () => ({ mutate: closeShellTerminalMock }),
	useRenameShellTerminal: () => ({ mutate: vi.fn() }),
}));

function workerSession(sessionId: string): WorkspaceSession {
	const session = workspaces[0].sessions.find((item) => item.id === sessionId);
	if (!session) throw new Error(`missing test session ${sessionId}`);
	return session;
}

function testInterfaceTransition(
	id: string,
	phase: NonNullable<NonNullable<typeof interfaceTransitionState.status>["transition"]>["phase"],
) {
	return {
		id,
		sessionId: "sess-1",
		sourceMode: "chat" as const,
		targetMode: "tui" as const,
		policy: "interrupt" as const,
		phase,
		createdAt: "2026-08-26T09:00:00.000Z",
		updatedAt: "2026-08-26T09:00:01.000Z",
	};
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

async function unsafeChatLeaveDialog(): Promise<HTMLElement> {
	return screen.findByRole("dialog", { name: "Discard unsafe Chat draft state?" });
}

async function confirmUnsafeChatLeave(): Promise<HTMLElement> {
	const dialog = await unsafeChatLeaveDialog();
	fireEvent.click(within(dialog).getByRole("button", { name: "Leave chat" }));
	return dialog;
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
		for (const sessionId of ["sess-1", "sess-2", "sess-orch", "sess-cross-project"]) {
			setChatDraftBoundary(sessionId, "composer", undefined);
			setChatDraftBoundary(sessionId, "inline-edit", undefined);
		}
		routeBlockerState.options = undefined;
		inspectorVisibilityRenders.length = 0;
		nativeFullScreenMock.mockReturnValue(false);
		window.localStorage.clear();
		for (const session of workspaces.flatMap((workspace) => workspace.sessions)) {
			delete session.previewUrl;
			delete session.previewRevision;
			delete session.isTerminated;
			session.status = "working";
			session.provider = "claude-code";
			delete session.mode;
			session.prs = [];
		}
		workspaceQueryState.data = workspaces;
		workspaceQueryState.isLoading = false;
		useUiStore.setState({
			activeShellTerminalHandleId: null,
			inspectorSessions: {},
			isSidebarOpen: true,
			visibleTerminalKindBySession: {},
		});
		browserDestroy.mockReset();
		browserViewOptions.current = undefined;
		browserViewState.url = "";
		browserViewState.agentBrowserActive = false;
		shellTerminalsState.data = [];
		navigateMock.mockReset();
		openShellTerminalMock.mockReset();
		openShellTerminalMock.mockImplementation((input: { projectId?: string; sessionId?: string }) => ({
			handleId: "pending-shell:test",
			projectId: input.projectId,
			sessionId: input.sessionId,
			workingDir: "",
			title: "Terminal 1",
			createdAt: "2026-08-31T00:00:00Z",
			optimistic: true,
		}));
		closeShellTerminalMock.mockReset();
		interfaceTransitionMock.start.mockReset();
		interfaceTransitionMock.refreshStatus.mockReset();
		interfaceTransitionMock.refreshStatus.mockImplementation(
			async () => interfaceTransitionState.status,
		);
		interfaceTransitionMock.resetStartError.mockReset();
		interfaceTransitionMock.cancel.mockReset();
		interfaceTransitionMock.acknowledgeNotice.mockReset();
		interfaceTransitionState.starting = false;
		interfaceTransitionState.settling = false;
		interfaceTransitionState.startError = undefined;
		interfaceTransitionState.status = undefined;
		chatSurfaceWorkState.controllerBusy = false;
		chatSurfaceWorkState.hasRunningTurn = false;
		chatSurfaceWorkState.queuedTurnCount = 0;
		codexAccountsQueryState.data = undefined;
		recoverCodexAccountSwitchMock.mockReset();
		reviewGetMock.mockReset();
		reviewGetMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
				return {
					data: {
						sessionId: "sess-1",
						files: [],
						truncated: false,
						sections: { staged: [], unstaged: [], untracked: [], committed: [] },
						commits: [],
						summary: { files: 0, additions: 0, deletions: 0 },
					},
					error: undefined,
				};
			}
			return { data: { reviewerHandleId: "", reviews: [], runs: [] }, error: undefined };
		});
	});

	it("offers recovery directly from a Codex session blocked by a failed account switch", async () => {
		const session = workerSession("sess-1");
		session.provider = "codex";
		codexAccountsQueryState.data = {
			currentSwitch: {
				id: "switch-1",
				sourceAccountId: "account-a",
				targetAccountId: "account-b",
				phase: "recovery_required",
				canRecover: true,
				sessions: [{
					sessionId: "sess-1",
					interfaceMode: "tui",
					wasRunning: true,
					stopState: "stopped",
					restartState: "failed",
				}],
				createdAt: "2026-09-02T00:00:00Z",
				updatedAt: "2026-09-02T00:01:00Z",
			},
		};
		recoverCodexAccountSwitchMock.mockResolvedValue(undefined);

		render(<SessionView sessionId="sess-1" />);

		const retry = screen.getByRole("button", { name: "Retry recovery" });
		expect(retry).toBeEnabled();
		await userEvent.click(retry);
		expect(recoverCodexAccountSwitchMock).toHaveBeenCalledWith("switch-1");
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

	it("remounts the session-owned Chat surface when navigation selects another Chat session", () => {
		workerSession("sess-1").mode = "chat";
		workerSession("sess-2").mode = "chat";
		const view = render(<SessionView sessionId="sess-1" />);
		const firstSurface = screen.getByTestId("chat-surface");

		view.rerender(<SessionView sessionId="sess-2" />);

		expect(screen.getByTestId("chat-surface")).not.toBe(firstSurface);
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
		fireEvent.click(newTerminalButton);
		expect(openShellTerminalMock).toHaveBeenCalledWith({ projectId: "proj-1", sessionId: "sess-2" }, expect.anything());
		expect(useUiStore.getState().activeShellTerminalHandleId).toBe("pending-shell:test");
	});

	it("activates a new terminal opened while a file tab is selected", async () => {
		const shell = {
			handleId: "sh-after-file",
			projectId: "proj-1",
			sessionId: "sess-1",
			title: "Terminal 1",
			workingDir: "/p",
			createdAt: "2026-08-31T00:00:00Z",
		};
		openShellTerminalMock.mockImplementation((_input, options) => {
			shellTerminalsState.data = [shell];
			options.onSuccess(shell);
		});
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "view review file" }));
		await screen.findByText("rail preview src/panel.tsx");
		fireEvent.click(screen.getByRole("button", { name: "pop out src/panel.tsx" }));
		expect(await screen.findByTestId("session-file-workspace")).toHaveTextContent("src/panel.tsx");

		fireEvent.click(screen.getByRole("button", { name: "New terminal" }));
		expect(screen.queryByTestId("session-file-workspace")).not.toBeInTheDocument();
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("sh-after-file");
	});

	it("does not offer a new terminal for orchestrator sessions", () => {
		render(<SessionView sessionId="sess-orch" />);

		expect(screen.queryByRole("button", { name: "New terminal" })).not.toBeInTheDocument();
	});

	// Regression (#3874 then re-added by #4252): the prime top bar carries session
	// identity, status, and controls — never the worktree branch. A branch badge
	// beside the actions duplicates a fact the inspector, board card, and command
	// palette already own, and its long name crowds the controls it sits next to.
	it.each([
		["a terminal worker", "sess-1", "tui", true],
		["a chat worker", "sess-1", "chat", true],
		["an orchestrator", "sess-orch", "tui", false],
	] as const)("keeps the git branch out of %s session's top bar", (_label, sessionId, mode, offersNewTerminal) => {
		workerSession(sessionId).mode = mode;

		render(<SessionView sessionId={sessionId} />);

		expect(screen.queryByText("ao/sess-1")).not.toBeInTheDocument();
		expect(screen.queryByTitle("ao/sess-1")).not.toBeInTheDocument();
		expect(document.querySelector(".lucide-git-branch")).toBeNull();
		// The session's own actions still ride in the same top-bar slot.
		expect(screen.getByTestId("mock-session-topbar")).toBeInTheDocument();
		expect(Boolean(screen.queryByRole("button", { name: "New terminal" }))).toBe(offersNewTerminal);
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
			return shell;
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

	it("preserves mixed tab order across session navigation and interface changes", async () => {
		workerSession("sess-1").mode = "chat";
		workerSession("sess-2").mode = "chat";
		shellTerminalsState.data = [
			{
				handleId: "sh-a",
				projectId: "proj-1",
				sessionId: "sess-1",
				title: "session one shell",
				workingDir: "/p",
				createdAt: "2026-08-31T00:00:00Z",
			},
		];
		const view = render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "view review file" }));
		await screen.findByText("rail preview src/panel.tsx");
		fireEvent.click(screen.getByRole("button", { name: "pop out src/panel.tsx" }));
		await screen.findByTestId("session-file-workspace");
		fireEvent.click(screen.getByRole("button", { name: "reorder auxiliary tabs" }));
		expect(screen.getByTestId("auxiliary-tab-order-sess-1")).toHaveTextContent(
			"sh-a|file:src/panel.tsx",
		);

		view.rerender(<SessionView sessionId="sess-2" />);
		view.rerender(<SessionView sessionId="sess-1" />);

		expect(screen.getByTestId("auxiliary-tab-order-sess-1")).toHaveTextContent(
			"sh-a|file:src/panel.tsx",
		);

		workerSession("sess-1").mode = "tui";
		view.rerender(<SessionView sessionId="sess-1" />);
		expect(screen.getByTestId("auxiliary-tab-order-tui-sess-1")).toHaveTextContent(
			"sh-a|file:src/panel.tsx",
		);
	});

	it("selects the adjacent shell when closing a reordered file tab", async () => {
		workerSession("sess-1").mode = "chat";
		shellTerminalsState.data = [
			{
				handleId: "sh-a",
				projectId: "proj-1",
				sessionId: "sess-1",
				title: "session one shell",
				workingDir: "/p",
				createdAt: "2026-08-31T00:00:00Z",
			},
		];
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "view review file" }));
		await screen.findByText("rail preview src/panel.tsx");
		fireEvent.click(screen.getByRole("button", { name: "pop out src/panel.tsx" }));
		await screen.findByTestId("session-file-workspace");
		fireEvent.click(screen.getByRole("button", { name: "reorder auxiliary tabs" }));
		fireEvent.click(screen.getByRole("button", { name: "Close panel.tsx" }));

		expect(screen.queryByTestId("session-file-workspace")).not.toBeInTheDocument();
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("shell");
	});

	it("preserves a reordered reviewer position while the session is inactive", async () => {
		const worker = workerSession("sess-1");
		worker.mode = "chat";
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
		shellTerminalsState.data = [
			{
				handleId: "sh-a",
				projectId: "proj-1",
				sessionId: "sess-1",
				title: "session one shell",
				workingDir: "/p",
				createdAt: "2026-08-31T00:00:00Z",
			},
		];
		reviewGetMock.mockResolvedValueOnce({
			data: { reviewerHandleId: "review-sess-1", reviewerHarness: "codex", reviews: [] },
			error: undefined,
		});
		const view = render(<SessionView sessionId="sess-1" />);

		await screen.findByRole("button", { name: "Reviewer" });
		fireEvent.click(screen.getByRole("button", { name: "view review file" }));
		await screen.findByText("rail preview src/panel.tsx");
		fireEvent.click(screen.getByRole("button", { name: "pop out src/panel.tsx" }));
		await screen.findByTestId("session-file-workspace");
		fireEvent.click(screen.getByRole("button", { name: "reorder reviewer tab" }));
		expect(screen.getByTestId("auxiliary-tab-order-sess-1")).toHaveTextContent(
			"sh-a|reviewer:review-sess-1|file:src/panel.tsx",
		);

		worker.status = "terminated";
		worker.isTerminated = true;
		view.rerender(<SessionView sessionId="sess-1" />);
		fireEvent.click(screen.getByRole("button", { name: "reorder visible tabs" }));
		worker.status = "working";
		worker.isTerminated = false;
		view.rerender(<SessionView sessionId="sess-1" />);

		await screen.findByRole("button", { name: "Reviewer" });
		expect(screen.getByTestId("auxiliary-tab-order-sess-1")).toHaveTextContent(
			"file:src/panel.tsx|reviewer:review-sess-1|sh-a",
		);
	});

	it("keeps a shell's reordered position when closing it fails", async () => {
		workerSession("sess-1").mode = "chat";
		shellTerminalsState.data = [
			{
				handleId: "sh-a",
				projectId: "proj-1",
				sessionId: "sess-1",
				title: "session one shell",
				workingDir: "/p",
				createdAt: "2026-08-31T00:00:00Z",
			},
		];
		closeShellTerminalMock.mockImplementation((_handleId, options) => {
			options?.onError?.({ code: "SHELL_TERMINAL_CLOSE_FAILED" });
		});
		const view = render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "view review file" }));
		await screen.findByText("rail preview src/panel.tsx");
		fireEvent.click(screen.getByRole("button", { name: "pop out src/panel.tsx" }));
		await screen.findByTestId("session-file-workspace");
		fireEvent.click(screen.getByRole("button", { name: "reorder auxiliary tabs" }));
		fireEvent.click(screen.getByRole("button", { name: "session one shell" }));
		fireEvent.click(screen.getByRole("button", { name: "close session one shell" }));
		view.rerender(<SessionView sessionId="sess-1" />);

		expect(screen.getByTestId("auxiliary-tab-order-sess-1")).toHaveTextContent(
			"sh-a|file:src/panel.tsx",
		);
	});

	it.each([
		["Terminal UI worker", "sess-1", "tui", "chat", "Switch to chat UI"],
		["Terminal UI orchestrator", "sess-orch", "tui", "chat", "Switch to chat UI"],
	] as const)("switches an idle %s directly with drain", async (_label, sessionId, mode, targetMode, buttonName) => {
		interfaceTransitionState.status = { supported: true, targetMode };
		const session = workerSession(sessionId);
		session.mode = mode;
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId={sessionId} />);

		await chooseSessionAction(buttonName);

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({ targetMode, policy: "drain" });
	});

	it.each([
		["worker", "sess-1"],
		["orchestrator", "sess-orch"],
	] as const)("switches an idle Chat %s directly to Terminal UI with drain", async (_label, sessionId) => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession(sessionId);
		session.mode = "chat";
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId={sessionId} />);
		fireEvent.click(screen.getByRole("button", { name: "report chat work" }));

		await chooseSessionAction("Switch to terminal UI");

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		await waitFor(() =>
			expect(interfaceTransitionMock.start).toHaveBeenCalledWith({
				targetMode: "tui",
				policy: "drain",
			}),
		);
	});

	it("keeps the policy dialog closed when an idle direct switch fails", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "chat" };
		const session = workerSession("sess-1");
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };
		interfaceTransitionMock.start.mockRejectedValueOnce(new Error("switch failed"));

		render(<SessionView sessionId="sess-1" />);

		await chooseSessionAction("Switch to chat UI");

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("shows and dismisses a refused idle switch outside the policy dialog", () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		interfaceTransitionState.startError = "The agent has not exposed a native conversation (NATIVE_SESSION_MISSING)";
		const session = workerSession("sess-1");
		session.mode = "chat";
		session.status = "idle";
		render(<SessionView sessionId="sess-1" />);
		expect(screen.getByRole("alert")).toHaveTextContent("NATIVE_SESSION_MISSING");
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Dismiss interface switch error" }));
		expect(interfaceTransitionMock.resetStartError).toHaveBeenCalled();
	});

	it("prioritizes a new refusal over an older transition notice until dismissed", () => {
		workerSession("sess-1");
		interfaceTransitionState.status = { supported: true, targetMode: "chat", transition: {
			id: "old-transition", sessionId: "sess-1", sourceMode: "tui", targetMode: "chat", policy: "drain", phase: "failed",
			errorDetail: "Previous switch failed", createdAt: "2026-09-05T00:00:00Z", updatedAt: "2026-09-05T00:00:00Z",
		} };
		interfaceTransitionState.startError = "Current request refused";
		const view = render(<SessionView sessionId="sess-1" />);
		expect(screen.getByRole("alert")).toHaveTextContent("Current request refused");
		expect(screen.queryByText("Previous switch failed")).not.toBeInTheDocument();
		interfaceTransitionState.startError = undefined;
		view.rerender(<SessionView sessionId="sess-1" />);
		expect(screen.getByText("Previous switch failed")).toBeInTheDocument();
	});

	it("keeps the policy dialog open when an explicit busy Chat choice fails", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession("sess-1");
		session.mode = "chat";
		session.status = "working";
		session.activity = { state: "active", lastActivityAt: "2026-08-06T00:00:00Z" };
		interfaceTransitionMock.start.mockRejectedValueOnce(new Error("switch failed"));

		render(<SessionView sessionId="sess-1" />);

		await chooseSessionAction("Switch to terminal UI");
		expect(screen.getByRole("dialog", { name: "Switch to Terminal UI?" })).toBeInTheDocument();
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		});

		expect(screen.getByRole("dialog", { name: "Switch to Terminal UI?" })).toBeInTheDocument();
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({ targetMode: "tui", policy: "interrupt" });
	});

	it.each([
		["working status", "sess-1", "tui", "chat", "Switch to chat UI", "working", "idle"],
		["needs-input status", "sess-orch", "tui", "chat", "Switch to chat UI", "needs_input", "idle"],
		["blocked activity", "sess-1", "tui", "chat", "Switch to chat UI", "idle", "blocked"],
	] as const)("opens the switch policy dialog for %s", async (_label, sessionId, mode, targetMode, buttonName, status, activityState) => {
		interfaceTransitionState.status = { supported: true, targetMode };
		const session = workerSession(sessionId);
		session.mode = mode;
		session.status = status;
		session.activity = { state: activityState, lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId={sessionId} />);

		await chooseSessionAction(buttonName);

		expect(screen.getByRole("dialog")).toBeInTheDocument();
		expect(interfaceTransitionMock.start).not.toHaveBeenCalled();
	});

	it("requires an explicit policy while current Chat work is not yet known", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession("sess-1");
		session.mode = "chat";
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId="sess-1" />);
		await chooseSessionAction("Switch to terminal UI");

		expect(screen.getByRole("dialog", { name: "Switch to Terminal UI?" })).toBeInTheDocument();
		expect(interfaceTransitionMock.start).not.toHaveBeenCalled();
	});

	it.each([
		["working status", "sess-1", "working", "idle"],
		["needs-input status", "sess-orch", "needs_input", "idle"],
		["active activity", "sess-1", "idle", "active"],
		["waiting-input activity", "sess-orch", "idle", "waiting_input"],
		["blocked activity", "sess-1", "idle", "blocked"],
	] as const)("asks for policy before a busy Chat session switches for %s", async (_label, sessionId, status, activityState) => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession(sessionId);
		session.mode = "chat";
		session.status = status;
		session.activity = { state: activityState, lastActivityAt: "2026-08-06T00:00:00Z" };

			render(<SessionView sessionId={sessionId} />);

			await chooseSessionAction("Switch to terminal UI");

			expect(screen.getByRole("dialog", { name: "Switch to Terminal UI?" })).toBeInTheDocument();
			expect(interfaceTransitionMock.start).not.toHaveBeenCalled();
		},
	);

	it("blocks route unload while a Chat draft is unsafe and uses explicit discard copy", async () => {
		let finishStaging!: (attachments: FileAttachment[]) => void;
		const staging = renderHook(() =>
			useFileAttachments({
				initialKey: chatDraftScopeKey({
					sessionId: "sess-1",
					incarnation: "2026-08-26T08:00:00.000Z",
				}),
				prepareAttachments: () =>
					new Promise<FileAttachment[]>((resolve) => {
						finishStaging = resolve;
					}),
			}),
		);
		let pending!: Promise<void>;
		act(() => {
			pending = staging.result.current.addFiles([
				new File([new Uint8Array(8).fill(1)], "route-late.txt", { type: "text/plain" }),
			]);
		});
		await waitFor(() => expect(finishStaging).toBeTypeOf("function"));
		setChatDraftBoundary("sess-1", "composer", "persistence-failed");
		render(<SessionView sessionId="sess-1" />);

		expect(routeBlockerState.options).toMatchObject({
			disabled: false,
			enableBeforeUnload: true,
		});
		let blocked!: Promise<boolean>;
		act(() => {
			blocked = Promise.resolve(routeBlockerState.options!.shouldBlockFn());
		});
		const cancelled = await unsafeChatLeaveDialog();
		expect(cancelled).toHaveTextContent("discard the unsaved changes");
		fireEvent.click(within(cancelled).getByRole("button", { name: "Cancel" }));
		expect(await blocked).toBe(true);

		let allowed!: Promise<boolean>;
		act(() => {
			allowed = Promise.resolve(routeBlockerState.options!.shouldBlockFn());
		});
		await confirmUnsafeChatLeave();
		expect(await allowed).toBe(false);
		await waitFor(() => expect(staging.result.current.preparing).toBe(false));
		await act(async () => {
			finishStaging([
				{
					id: "route-discarded-late",
					mimeType: "text/plain",
					bytes: 8,
					name: "route-late.txt",
					stagedPath: ".ao/attachments/route-late.txt",
				},
			]);
			await pending;
		});
		expect(staging.result.current.attachments).toEqual([]);
		act(() => setChatDraftBoundary("sess-1", "composer", undefined));
		expect(routeBlockerState.options).toMatchObject({
			disabled: true,
			enableBeforeUnload: false,
		});
	});

	it("publishes the full stable native risk set and clears it only on unmount", async () => {
		const publishRisk = vi.spyOn(window.ao!.app, "setChatDraftRisk");
		const view = render(<SessionView sessionId="sess-1" />);
		await waitFor(() => expect(publishRisk).toHaveBeenLastCalledWith([], expect.objectContaining({ stay: "Stay" })));

		act(() => setChatDraftBoundary("sess-1", "composer", "persistence-failed"));
		await waitFor(() =>
			expect(publishRisk).toHaveBeenLastCalledWith(["persistence-failed"], expect.objectContaining({ detail: expect.stringContaining("could not be saved locally") })),
		);
		publishRisk.mockClear();

		act(() =>
			setChatDraftBoundary("sess-1", "composer", [
				"persistence-failed",
				"pending-attachments",
			]),
		);
		await waitFor(() =>
			expect(publishRisk).toHaveBeenCalledWith([
				"persistence-failed",
				"pending-attachments",
			], expect.objectContaining({ detail: expect.stringContaining("Attachments are still being saved") })),
		);
		expect(publishRisk).not.toHaveBeenCalledWith([]);

		view.unmount();
		expect(publishRisk).toHaveBeenLastCalledWith([]);
		publishRisk.mockRestore();
	});

	it("uses an app-rendered confirmation before leaving unsafe Chat for Terminal", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession("sess-1");
		session.mode = "chat";
		setChatDraftBoundary("sess-1", "composer", "pending-attachments");
		const nativeConfirm = vi.spyOn(window, "confirm");
		render(<SessionView sessionId="sess-1" />);

		await chooseSessionAction("Switch to terminal UI");
		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		expect(interfaceTransitionMock.start).not.toHaveBeenCalled();
		const cancelled = await screen.findByRole("dialog", { name: "Discard unsafe Chat draft state?" });
		expect(cancelled).toHaveTextContent("Attachments are still being saved");
		fireEvent.click(within(cancelled).getByRole("button", { name: "Cancel" }));
		expect(interfaceTransitionMock.start).not.toHaveBeenCalled();

		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		const confirmed = await screen.findByRole("dialog", { name: "Discard unsafe Chat draft state?" });
		fireEvent.click(within(confirmed).getByRole("button", { name: "Leave chat" }));
		await waitFor(() =>
			expect(interfaceTransitionMock.start).toHaveBeenCalledWith({
				targetMode: "tui",
				policy: "interrupt",
			}),
		);
		expect(nativeConfirm).not.toHaveBeenCalled();
		setChatDraftBoundary("sess-1", "composer", undefined);
		nativeConfirm.mockRestore();
	});

	it("cancels pending attachments only after the exact interface transition completes", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const transition = testInterfaceTransition("discard-after-complete", "requested");
		interfaceTransitionMock.start.mockResolvedValueOnce({ transition });
		const session = workerSession("sess-1");
		session.mode = "chat";
		let finishStaging!: (attachments: FileAttachment[]) => void;
		const staging = renderHook(() =>
			useFileAttachments({
				initialKey: chatDraftScopeKey({
					sessionId: session.id,
					incarnation: "2026-08-26T09:00:00.000Z",
				}),
				prepareAttachments: () =>
					new Promise<FileAttachment[]>((resolve) => {
						finishStaging = resolve;
					}),
			}),
		);
		let pending!: Promise<void>;
		act(() => {
			pending = staging.result.current.addFiles([
				new File([new Uint8Array(8).fill(1)], "discard-late.txt", {
					type: "text/plain",
				}),
			]);
		});
		await waitFor(() => expect(finishStaging).toBeTypeOf("function"));
		act(() => setChatDraftBoundary(session.id, "composer", "pending-attachments"));
		const view = render(<SessionView sessionId={session.id} />);

		await chooseSessionAction("Switch to terminal UI");
		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		await confirmUnsafeChatLeave();
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({
			targetMode: "tui",
			policy: "interrupt",
		});
		await waitFor(() => expect(interfaceTransitionMock.start).toHaveBeenCalledTimes(1));
		expect(staging.result.current.preparing).toBe(true);
		await act(async () => undefined);

		interfaceTransitionState.status = {
			supported: true,
			targetMode: "tui",
			transition: testInterfaceTransition("older-completed-transition", "completed"),
		};
		view.rerender(<SessionView sessionId={session.id} />);
		expect(staging.result.current.preparing).toBe(true);

		interfaceTransitionState.status = {
			supported: true,
			targetMode: "tui",
			transition: { ...transition, phase: "completed" },
		};
		view.rerender(<SessionView sessionId={session.id} />);
		await waitFor(() => expect(staging.result.current.preparing).toBe(false));

		await act(async () => {
			finishStaging([
				{
					id: "discarded-late",
					mimeType: "text/plain",
					bytes: 8,
					name: "discard-late.txt",
					stagedPath: ".ao/attachments/discard-late.txt",
				},
			]);
			await pending;
		});
		expect(staging.result.current.attachments).toEqual([]);
		act(() => setChatDraftBoundary(session.id, "composer", undefined));
	});

	it("preserves attachment work started after the switch confirmation", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const transition = testInterfaceTransition("preserve-after-confirmation", "requested");
		let admitSwitch!: (response: { transition: typeof transition }) => void;
		interfaceTransitionMock.start.mockReturnValueOnce(
			new Promise((resolve) => {
				admitSwitch = resolve;
			}),
		);
		const session = workerSession("sess-1");
		session.mode = "chat";
		act(() => setChatDraftBoundary(session.id, "composer", "persistence-failed"));
		const view = render(<SessionView sessionId={session.id} />);

		await chooseSessionAction("Switch to terminal UI");
		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		await confirmUnsafeChatLeave();
		await waitFor(() => expect(interfaceTransitionMock.start).toHaveBeenCalledTimes(1));
		await waitFor(() =>
			expect(screen.getByTestId("chat-surface")).toHaveAttribute("data-transitioning", "true"),
		);

		let finishStaging!: (attachments: FileAttachment[]) => void;
		const staging = renderHook(() =>
			useFileAttachments({
				initialKey: chatDraftScopeKey({
					sessionId: session.id,
					incarnation: "2026-08-26T09:30:00.000Z",
				}),
				prepareAttachments: () =>
					new Promise<FileAttachment[]>((resolve) => {
						finishStaging = resolve;
					}),
			}),
		);
		let pending!: Promise<void>;
		act(() => {
			pending = staging.result.current.addFiles([
				new File([new Uint8Array(8).fill(1)], "after-confirmation.txt", {
					type: "text/plain",
				}),
			]);
		});
		await waitFor(() => expect(finishStaging).toBeTypeOf("function"));
		await act(async () => admitSwitch({ transition }));

		interfaceTransitionState.status = {
			supported: true,
			targetMode: "tui",
			transition: { ...transition, phase: "completed" },
		};
		view.rerender(<SessionView sessionId={session.id} />);
		expect(staging.result.current.preparing).toBe(true);

		await act(async () => {
			finishStaging([
				{
					id: "after-confirmation",
					mimeType: "text/plain",
					bytes: 8,
					name: "after-confirmation.txt",
					stagedPath: ".ao/attachments/after-confirmation.txt",
				},
			]);
			await pending;
		});
		expect(staging.result.current.attachments.map((attachment) => attachment.name)).toEqual([
			"after-confirmation.txt",
		]);
		act(() => setChatDraftBoundary(session.id, "composer", undefined));
	});

	it("unlocks Chat when a pending Chat-to-Terminal request is rejected", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession("sess-1");
		session.mode = "chat";
		let rejectSwitch!: (error: Error) => void;
		interfaceTransitionMock.start.mockReturnValueOnce(
			new Promise((_resolve, reject) => {
				rejectSwitch = reject;
			}),
		);
		render(<SessionView sessionId={session.id} />);

		await chooseSessionAction("Switch to terminal UI");
		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		await waitFor(() =>
			expect(screen.getByTestId("chat-surface")).toHaveAttribute("data-transitioning", "true"),
		);
		await act(async () => rejectSwitch(new Error("switch rejected")));
		expect(interfaceTransitionMock.refreshStatus).toHaveBeenCalledTimes(1);
		await waitFor(() =>
			expect(screen.getByTestId("chat-surface")).toHaveAttribute("data-transitioning", "false"),
		);
	});

	it("keeps Chat locked when a rejected start reconciles to a durable transition", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const transition = testInterfaceTransition("admitted-before-response-loss", "requested");
		interfaceTransitionMock.start.mockRejectedValueOnce(new TypeError("connection closed"));
		interfaceTransitionMock.refreshStatus.mockResolvedValueOnce({
			supported: true,
			targetMode: "tui",
			transition,
		});
		const session = workerSession("sess-1");
		session.mode = "chat";
		const view = render(<SessionView sessionId={session.id} />);

		await chooseSessionAction("Switch to terminal UI");
		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		await waitFor(() => expect(interfaceTransitionMock.refreshStatus).toHaveBeenCalledTimes(1));
		expect(screen.getByTestId("chat-surface")).toHaveAttribute("data-transitioning", "true");

		interfaceTransitionState.status = {
			supported: true,
			targetMode: "tui",
			transition: { ...transition, phase: "failed" },
		};
		view.rerender(<SessionView sessionId={session.id} />);
		await waitFor(() =>
			expect(screen.getByTestId("chat-surface")).toHaveAttribute("data-transitioning", "false"),
		);
	});

	it("does not let a stale reconciliation response clear a transition already observed by CDC", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const transition = testInterfaceTransition("cdc-wins-reconciliation-race", "requested");
		interfaceTransitionMock.start.mockRejectedValueOnce(new TypeError("connection closed"));
		let finishRefresh!: (status: { supported: boolean; targetMode: "tui" }) => void;
		interfaceTransitionMock.refreshStatus.mockReturnValueOnce(
			new Promise((resolve) => {
				finishRefresh = resolve;
			}),
		);
		const session = workerSession("sess-1");
		session.mode = "chat";
		const view = render(<SessionView sessionId={session.id} />);

		await chooseSessionAction("Switch to terminal UI");
		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		await waitFor(() => expect(interfaceTransitionMock.refreshStatus).toHaveBeenCalledTimes(1));
		interfaceTransitionState.status = { supported: true, targetMode: "tui", transition };
		await act(async () => {
			view.rerender(<SessionView sessionId={session.id} />);
			await Promise.resolve();
		});

		await act(async () => finishRefresh({ supported: true, targetMode: "tui" }));
		expect(screen.getByTestId("chat-surface")).toHaveAttribute("data-transitioning", "true");

		interfaceTransitionState.status = {
			supported: true,
			targetMode: "tui",
			transition: { ...transition, phase: "failed" },
		};
		view.rerender(<SessionView sessionId={session.id} />);
		await waitFor(() =>
			expect(screen.getByTestId("chat-surface")).toHaveAttribute("data-transitioning", "false"),
		);
	});

	it("retains confirmed attachment capture until CDC resolves an unreadable start outcome", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const transition = testInterfaceTransition("admitted-before-status-outage", "requested");
		interfaceTransitionMock.start.mockRejectedValueOnce(new TypeError("connection closed"));
		interfaceTransitionMock.refreshStatus.mockRejectedValueOnce(new TypeError("daemon unavailable"));
		const session = workerSession("sess-1");
		session.mode = "chat";
		let finishStaging!: (attachments: FileAttachment[]) => void;
		const staging = renderHook(() =>
			useFileAttachments({
				initialKey: chatDraftScopeKey({
					sessionId: session.id,
					incarnation: "2026-08-26T10:30:00.000Z",
				}),
				prepareAttachments: () =>
					new Promise<FileAttachment[]>((resolve) => {
						finishStaging = resolve;
					}),
			}),
		);
		let pending!: Promise<void>;
		act(() => {
			pending = staging.result.current.addFiles([
				new File([new Uint8Array(8).fill(1)], "captured-through-outage.txt", {
					type: "text/plain",
				}),
			]);
		});
		await waitFor(() => expect(finishStaging).toBeTypeOf("function"));
		act(() => setChatDraftBoundary(session.id, "composer", "pending-attachments"));
		const view = render(<SessionView sessionId={session.id} />);

		await chooseSessionAction("Switch to terminal UI");
		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		await confirmUnsafeChatLeave();
		await waitFor(() => expect(interfaceTransitionMock.refreshStatus).toHaveBeenCalledTimes(1));
		expect(screen.getByTestId("chat-surface")).toHaveAttribute("data-transitioning", "true");
		expect(staging.result.current.preparing).toBe(true);

		interfaceTransitionState.status = {
			supported: true,
			targetMode: "tui",
			transition,
		};
		view.rerender(<SessionView sessionId={session.id} />);
		expect(screen.getByTestId("chat-surface")).toHaveAttribute("data-transitioning", "true");

		interfaceTransitionState.status = {
			supported: true,
			targetMode: "tui",
			transition: { ...transition, phase: "completed" },
		};
		view.rerender(<SessionView sessionId={session.id} />);
		await waitFor(() => expect(staging.result.current.preparing).toBe(false));

		await act(async () => {
			finishStaging([
				{
					id: "discarded-after-outage",
					mimeType: "text/plain",
					bytes: 8,
					name: "captured-through-outage.txt",
					stagedPath: ".ao/attachments/captured-through-outage.txt",
				},
			]);
			await pending;
		});
		expect(staging.result.current.attachments).toEqual([]);
		act(() => setChatDraftBoundary(session.id, "composer", undefined));
	});

	it("preserves pending attachments when starting the confirmed interface switch rejects", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		interfaceTransitionMock.start.mockRejectedValueOnce(new Error("switch failed"));
		const session = workerSession("sess-1");
		session.mode = "chat";
		let finishStaging!: (attachments: FileAttachment[]) => void;
		const staging = renderHook(() =>
			useFileAttachments({
				initialKey: chatDraftScopeKey({
					sessionId: session.id,
					incarnation: "2026-08-26T10:00:00.000Z",
				}),
				prepareAttachments: () =>
					new Promise<FileAttachment[]>((resolve) => {
						finishStaging = resolve;
					}),
			}),
		);
		let pending!: Promise<void>;
		act(() => {
			pending = staging.result.current.addFiles([
				new File([new Uint8Array(8).fill(1)], "preserved-after-rejection.txt", {
					type: "text/plain",
				}),
			]);
		});
		await waitFor(() => expect(finishStaging).toBeTypeOf("function"));
		act(() => setChatDraftBoundary(session.id, "composer", "pending-attachments"));
		render(<SessionView sessionId={session.id} />);

		await chooseSessionAction("Switch to terminal UI");
		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		await confirmUnsafeChatLeave();
		await waitFor(() => expect(interfaceTransitionMock.start).toHaveBeenCalledTimes(1));
		expect(staging.result.current.preparing).toBe(true);

		await act(async () => {
			finishStaging([
				{
					id: "preserved-after-rejection",
					mimeType: "text/plain",
					bytes: 8,
					name: "preserved-after-rejection.txt",
					stagedPath: ".ao/attachments/preserved-after-rejection.txt",
				},
			]);
			await pending;
		});
		expect(staging.result.current.attachments).toHaveLength(1);
		act(() => setChatDraftBoundary(session.id, "composer", undefined));
	});

	it("preserves pending attachments when an admitted switch later fails", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const transition = testInterfaceTransition("preserve-after-late-failure", "requested");
		interfaceTransitionMock.start.mockResolvedValueOnce({ transition });
		const session = workerSession("sess-1");
		session.mode = "chat";
		let finishStaging!: (attachments: FileAttachment[]) => void;
		const staging = renderHook(() =>
			useFileAttachments({
				initialKey: chatDraftScopeKey({
					sessionId: session.id,
					incarnation: "2026-08-26T11:00:00.000Z",
				}),
				prepareAttachments: () =>
					new Promise<FileAttachment[]>((resolve) => {
						finishStaging = resolve;
					}),
			}),
		);
		let pending!: Promise<void>;
		act(() => {
			pending = staging.result.current.addFiles([
				new File([new Uint8Array(8).fill(1)], "preserved-after-failure.txt", {
					type: "text/plain",
				}),
			]);
		});
		await waitFor(() => expect(finishStaging).toBeTypeOf("function"));
		act(() => setChatDraftBoundary(session.id, "composer", "pending-attachments"));
		const view = render(<SessionView sessionId={session.id} />);

		await chooseSessionAction("Switch to terminal UI");
		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		await confirmUnsafeChatLeave();
		await waitFor(() => expect(interfaceTransitionMock.start).toHaveBeenCalledTimes(1));
		session.mode = "tui";
		interfaceTransitionState.status = {
			supported: true,
			targetMode: "chat",
			transition: { ...transition, phase: "failed" },
		};
		view.rerender(<SessionView sessionId={session.id} />);
		expect(staging.result.current.preparing).toBe(true);

		await act(async () => {
			finishStaging([
				{
					id: "preserved-after-failure",
					mimeType: "text/plain",
					bytes: 8,
					name: "preserved-after-failure.txt",
					stagedPath: ".ao/attachments/preserved-after-failure.txt",
				},
			]);
			await pending;
		});
		expect(staging.result.current.attachments).toHaveLength(1);
		act(() => setChatDraftBoundary(session.id, "composer", undefined));
	});

	it("cancels hidden attachment work after a confirmed mixed-risk leave without resurrection", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const transition = testInterfaceTransition("mixed-risk-complete", "requested");
		interfaceTransitionMock.start.mockResolvedValueOnce({ transition });
		const session = workerSession("sess-1");
		session.mode = "chat";
		const draftKey = chatDraftScopeKey({
			sessionId: session.id,
			incarnation: "2026-08-27T09:00:00.000Z",
		});
		let finishStaging!: (attachments: FileAttachment[]) => void;
		const staging = renderHook(() =>
			useFileAttachments({
				initialKey: draftKey,
				prepareAttachments: () =>
					new Promise<FileAttachment[]>((resolve) => {
						finishStaging = resolve;
					}),
			}),
		);
		let pending!: Promise<void>;
		act(() => {
			pending = staging.result.current.addFiles([
				new File([new Uint8Array(8).fill(1)], "mixed-late.txt", {
					type: "text/plain",
				}),
			]);
		});
		await waitFor(() => expect(finishStaging).toBeTypeOf("function"));
		act(() => {
			// Inline edit registers first, so the legacy single-kind snapshot hides
			// the composer's pending attachment work behind a failed draft save.
			setChatDraftBoundary(session.id, "inline-edit", "persistence-failed");
			setChatDraftBoundary(session.id, "composer", "pending-attachments");
		});
		const view = render(<SessionView sessionId={session.id} />);

		await chooseSessionAction("Switch to terminal UI");
		await userEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		const dialog = await unsafeChatLeaveDialog();
		const warning = dialog.textContent ?? "";
		expect(warning).toContain("could not be saved locally");
		expect(warning).toContain("Attachments are still being saved");
		fireEvent.click(within(dialog).getByRole("button", { name: "Leave chat" }));
		await waitFor(() => expect(interfaceTransitionMock.start).toHaveBeenCalledTimes(1));
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({
			targetMode: "tui",
			policy: "interrupt",
		});
		expect(staging.result.current.preparing).toBe(true);
		interfaceTransitionState.status = {
			supported: true,
			targetMode: "tui",
			transition: { ...transition, phase: "completed" },
		};
		view.rerender(<SessionView sessionId={session.id} />);
		await waitFor(() => expect(staging.result.current.preparing).toBe(false));

		view.unmount();
		staging.unmount();
		await act(async () => {
			finishStaging([
				{
					id: "mixed-discarded-late",
					mimeType: "text/plain",
					bytes: 8,
					name: "mixed-late.txt",
					stagedPath: ".ao/attachments/mixed-late.txt",
				},
			]);
			await pending;
		});
		const replacement = renderHook(() => useFileAttachments({ initialKey: draftKey }));
		expect(replacement.result.current.attachments).toEqual([]);
		expect(replacement.result.current.preparing).toBe(false);

		replacement.unmount();
		act(() => {
			setChatDraftBoundary(session.id, "inline-edit", undefined);
			setChatDraftBoundary(session.id, "composer", undefined);
		});
	});

	it.each([
		["a busy controller", { controllerBusy: true, hasRunningTurn: false, queuedTurnCount: 0 }],
		["a running turn", { controllerBusy: false, hasRunningTurn: true, queuedTurnCount: 0 }],
		["accepted queued work", { controllerBusy: false, hasRunningTurn: false, queuedTurnCount: 1 }],
	] as const)("asks for policy when Chat reports %s but the session projection is idle", async (_label, work) => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession("sess-1");
		session.mode = "chat";
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };
		Object.assign(chatSurfaceWorkState, work);

		render(<SessionView sessionId="sess-1" />);
		fireEvent.click(screen.getByRole("button", { name: "report chat work" }));
		await chooseSessionAction("Switch to terminal UI");

		expect(screen.getByRole("dialog", { name: "Switch to Terminal UI?" })).toBeInTheDocument();
		expect(interfaceTransitionMock.start).not.toHaveBeenCalled();
	});

	it("does not apply one Chat session's reported work to another selected session", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		for (const sessionId of ["sess-1", "sess-2"]) {
			const session = workerSession(sessionId);
			session.mode = "chat";
			session.status = "idle";
			session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };
		}
		chatSurfaceWorkState.queuedTurnCount = 1;

		const view = render(<SessionView sessionId="sess-1" />);
		fireEvent.click(screen.getByRole("button", { name: "report chat work" }));
		view.rerender(<SessionView sessionId="sess-2" />);
		chatSurfaceWorkState.queuedTurnCount = 0;
		fireEvent.click(screen.getByRole("button", { name: "report chat work" }));
		await chooseSessionAction("Switch to terminal UI");

		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({ targetMode: "tui", policy: "drain" });
	});

	it.each([
		["Finish work, then switch", "drain"],
		["Stop now and switch", "interrupt"],
	] as const)("maps explicit busy Chat consent through the %s action", async (buttonName, policy) => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession("sess-1");
		session.mode = "chat";
		session.status = "working";
		session.activity = { state: "active", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId="sess-1" />);

		await chooseSessionAction("Switch to terminal UI");
		expect(interfaceTransitionMock.start).not.toHaveBeenCalled();
		fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${buttonName}`) }));

		expect(interfaceTransitionMock.start).toHaveBeenCalledOnce();
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({ targetMode: "tui", policy });
	});

	it("disables Chat input while an accepted Chat-to-Terminal drain starts, runs, or settles", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession("sess-1");
		session.mode = "chat";
		session.status = "working";
		session.activity = { state: "active", lastActivityAt: "2026-08-06T00:00:00Z" };

		const view = render(<SessionView sessionId="sess-1" />);
		const chatSurface = () => screen.getByTestId("chat-surface");
		expect(chatSurface()).toHaveAttribute("data-new-work-disabled", "false");

		await chooseSessionAction("Switch to terminal UI");
		fireEvent.click(screen.getByRole("button", { name: /^Finish work, then switch/ }));
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({ targetMode: "tui", policy: "drain" });

		interfaceTransitionState.starting = true;
		view.rerender(<SessionView sessionId="sess-1" />);
		expect(chatSurface()).toHaveAttribute("data-new-work-disabled", "true");

		interfaceTransitionState.starting = false;
		interfaceTransitionState.status = {
			supported: true,
			targetMode: "tui",
			transition: {
				id: "switch-chat-to-tui",
				sessionId: "sess-1",
				sourceMode: "chat",
				targetMode: "tui",
				policy: "drain",
				phase: "draining",
				createdAt: "2026-08-06T00:00:00Z",
				updatedAt: "2026-08-06T00:00:01Z",
			},
		};
		view.rerender(<SessionView sessionId="sess-1" />);
		expect(chatSurface()).toHaveAttribute("data-new-work-disabled", "true");

		interfaceTransitionState.status.transition!.phase = "completed";
		interfaceTransitionState.settling = true;
		view.rerender(<SessionView sessionId="sess-1" />);
		expect(chatSurface()).toHaveAttribute("data-new-work-disabled", "true");

		interfaceTransitionState.settling = false;
		view.rerender(<SessionView sessionId="sess-1" />);
		expect(chatSurface()).toHaveAttribute("data-new-work-disabled", "false");
	});

	it("discards one session's switch consent dialog when navigating to another session", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		for (const sessionId of ["sess-1", "sess-2"]) {
			const session = workerSession(sessionId);
			session.mode = "chat";
			session.status = "working";
			session.activity = { state: "active", lastActivityAt: "2026-08-06T00:00:00Z" };
		}
		const view = render(<SessionView sessionId="sess-1" />);

		await chooseSessionAction("Switch to terminal UI");
		expect(screen.getByRole("dialog", { name: "Switch to Terminal UI?" })).toBeInTheDocument();

		view.rerender(<SessionView sessionId="sess-2" />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(interfaceTransitionMock.start).not.toHaveBeenCalled();

		view.rerender(<SessionView sessionId="sess-1" />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("does not let a completed switch close another session's newer consent dialog", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		for (const sessionId of ["sess-1", "sess-2"]) {
			const session = workerSession(sessionId);
			session.mode = "chat";
			session.status = "working";
			session.activity = { state: "active", lastActivityAt: "2026-08-06T00:00:00Z" };
		}
		let finishFirstSwitch!: () => void;
		interfaceTransitionMock.start.mockImplementationOnce(
			() => new Promise<void>((resolve) => {
				finishFirstSwitch = resolve;
			}),
		);
		const view = render(<SessionView sessionId="sess-1" />);

		await chooseSessionAction("Switch to terminal UI");
		fireEvent.click(screen.getByRole("button", { name: /^Stop now and switch/ }));
		expect(interfaceTransitionMock.start).toHaveBeenCalledWith({
			targetMode: "tui",
			policy: "interrupt",
		});

		view.rerender(<SessionView sessionId="sess-2" />);
		await chooseSessionAction("Switch to terminal UI");
		expect(screen.getByRole("dialog", { name: "Switch to Terminal UI?" })).toBeInTheDocument();

		await act(async () => finishFirstSwitch());

		expect(screen.getByRole("dialog", { name: "Switch to Terminal UI?" })).toBeInTheDocument();
	});

	it("discards switch consent when the requested target changes", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		const session = workerSession("sess-1");
		session.mode = "chat";
		session.status = "working";
		session.activity = { state: "active", lastActivityAt: "2026-08-06T00:00:00Z" };
		const view = render(<SessionView sessionId="sess-1" />);

		await chooseSessionAction("Switch to terminal UI");
		expect(screen.getByRole("dialog", { name: "Switch to Terminal UI?" })).toBeInTheDocument();

		interfaceTransitionState.status = { supported: true, targetMode: "chat" };
		view.rerender(<SessionView sessionId="sess-1" />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(interfaceTransitionMock.start).not.toHaveBeenCalled();

		interfaceTransitionState.status = { supported: true, targetMode: "tui" };
		view.rerender(<SessionView sessionId="sess-1" />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("checks only the selected session when deciding whether to show the policy dialog", async () => {
		interfaceTransitionState.status = { supported: true, targetMode: "chat" };
		const selected = workerSession("sess-1");
		selected.status = "idle";
		selected.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };
		const other = workerSession("sess-2");
		other.status = "working";
		other.activity = { state: "active", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId="sess-1" />);

		await chooseSessionAction("Switch to chat UI");

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

	it("returns to the source terminal while a failed Chat switch mode refetch settles", () => {
		const session = workerSession("sess-1");
		// The workspace cache observed the transition's intermediate mode commit,
		// but the durable transition already says the target failed and rolled back.
		session.mode = "chat";
		const transition = {
			id: "transition-failed",
			sessionId: "sess-1",
			sourceMode: "tui" as const,
			targetMode: "chat" as const,
			policy: "drain" as const,
			phase: "failed" as const,
			errorCode: "TARGET_HISTORY_UNSETTLED",
			errorDetail: "The native conversation history did not settle.",
			createdAt: "2026-08-23T17:00:00Z",
			updatedAt: "2026-08-23T17:01:00Z",
			completedAt: "2026-08-23T17:01:00Z",
		};
		interfaceTransitionState.status = { supported: true, targetMode: "chat", transition };

		render(<SessionView sessionId="sess-1" />);

		expect(screen.getByText("terminal center")).toBeInTheDocument();
		expect(screen.queryByTestId("chat-surface")).not.toBeInTheDocument();
		expect(screen.getByText(transition.errorDetail)).toBeInTheDocument();
	});

	it.each([
		["worker", "sess-1"],
		["orchestrator", "sess-orch"],
	] as const)("hides the interface switch button for %s sessions when Chat UI is unsupported", async (_label, sessionId) => {
		interfaceTransitionState.status = { supported: false, targetMode: "chat", reasonCode: "CHAT_UNSUPPORTED" };
		const session = workerSession(sessionId);
		session.mode = "tui";
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId={sessionId} />);

		await userEvent.click(screen.getByRole("button", { name: "Session actions" }));
		expect(screen.queryByRole("menuitem", { name: "Switch to chat UI" })).not.toBeInTheDocument();
	});

	it("shows the switch button when the adapter only reports a generic unsupported reason", async () => {
		interfaceTransitionState.status = { supported: false, targetMode: "chat", reasonCode: "INTERFACE_HANDOFF_UNSUPPORTED" };
		const session = workerSession("sess-1");
		session.mode = "tui";
		session.status = "idle";
		session.activity = { state: "idle", lastActivityAt: "2026-08-06T00:00:00Z" };

		render(<SessionView sessionId="sess-1" />);

		await userEvent.click(screen.getByRole("button", { name: "Session actions" }));
		expect(screen.getByRole("menuitem", { name: "Switch to chat UI" })).toBeInTheDocument();
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
		expect(closeShellTerminalMock).toHaveBeenCalledWith("sh-b", expect.any(Object));
		expect(screen.getByTestId("terminal-target")).toHaveTextContent("sh-a");
		expect(useUiStore.getState().activeShellTerminalHandleId).toBe("sh-a");

		shellTerminalsState.data = shellTerminalsState.data.filter((shell) => shell.handleId !== "sh-b");
		view.rerender(<SessionView sessionId="sess-1" />);
		fireEvent.click(screen.getByRole("button", { name: "close first shell" }));
		expect(closeShellTerminalMock).toHaveBeenCalledWith("sh-a", expect.any(Object));
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
			"--session-inspector-motion-duration": "300ms",
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

			act(() => vi.advanceTimersByTime(299));
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

			act(() => vi.advanceTimersByTime(299));
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

	it("keeps session chrome expanded when Browser is active", () => {
		useUiStore.setState({
			isSidebarOpen: true,
			inspectorSessions: { "sess-1": { initialized: true, isOpen: true, view: "browser" } },
		});

		render(<SessionView sessionId="sess-1" />);

		const topbar = screen.getByTestId("mock-session-topbar");
		expect(topbar).toHaveAttribute("data-compact-actions", "false");
		expect(topbar.closest("[data-compact-session-chrome]")).toHaveAttribute(
			"data-compact-session-chrome",
			"false",
		);
	});

	it("never shrinks the inspector when entering Browser", async () => {
		window.localStorage.setItem("ao.inspector.widthPx", "720");
		window.localStorage.setItem("ao.workspace.browser.canvasWidthPx", "460");
		render(<SessionView sessionId="sess-1" />);
		expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("720px");

		fireEvent.click(screen.getByRole("tab", { name: "Browser" }));
		await waitFor(() => {
			expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("720px");
		});
	});

	it("restores and clamps the persisted inspector width in pixels", () => {
		window.localStorage.setItem("ao.inspector.widthPx", "240");
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);
		expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("340px");
	});

	it("grows Browser into a co-work canvas while utility surfaces stay consistent", async () => {
		render(<SessionView sessionId="sess-1" />);
		expect(screen.getByTestId("panel-group")).toHaveAttribute("data-workspace-mode", "utility");
		expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("500px");

		act(() => useUiStore.getState().setInspectorView("sess-1", "browser"));
		await waitFor(() => {
			expect(screen.getByTestId("panel-group")).toHaveAttribute("data-workspace-mode", "browser");
			expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("900px");
			expect(
				screen.getByTestId("panel-group").style.getPropertyValue("--session-inspector-max-width"),
			).toBe("min(68%, max(300px, calc(100% - 440px)))");
		});

		act(() => useUiStore.getState().setInspectorView("sess-1", "files"));
		await waitFor(() => {
			expect(screen.getByTestId("panel-group")).toHaveAttribute("data-workspace-mode", "files");
			expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("500px");
			expect(
				screen.getByTestId("panel-group").style.getPropertyValue("--session-inspector-max-width"),
			).toBe("min(55%, max(300px, calc(100% - 560px)))");
		});
	});

	it("keeps Browser entry and utility return on one complete workspace spring", () => {
		vi.useFakeTimers();
		try {
			render(<SessionView sessionId="sess-1" />);
			fireEvent.click(screen.getByRole("tab", { name: "Browser" }));

			const split = screen.getByTestId("panel-group");
			expect(split).toHaveAttribute("data-workspace-resizing", "true");
			expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("900px");
			act(() => vi.advanceTimersByTime(300));
			expect(split).not.toHaveAttribute("data-workspace-resizing");

			fireEvent.click(screen.getByRole("tab", { name: "Summary" }));
			expect(split).toHaveAttribute("data-workspace-resizing", "true");
			expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("500px");
			act(() => vi.advanceTimersByTime(300));
			expect(split).not.toHaveAttribute("data-workspace-resizing");
		} finally {
			vi.useRealTimers();
		}
	});

	it("restores a separate user-sized Browser workspace without changing utility width", () => {
		window.localStorage.setItem("ao.workspace.browser.canvasWidthPx", "820");
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("tab", { name: "Browser" }));
		expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("820px");
		expect(useUiStore.getState().isSidebarOpen).toBe(true);

		fireEvent.click(screen.getByRole("tab", { name: "Summary" }));
		expect(document.documentElement.style.getPropertyValue("--ao-inspector-w")).toBe("500px");
	});

	it("never changes the sidebar preference while browser surfaces open and close", async () => {
		render(<SessionView sessionId="sess-1" />);
		expect(useUiStore.getState().isSidebarOpen).toBe(true);

		fireEvent.click(screen.getByRole("tab", { name: "Reviews" }));
		expect(useUiStore.getState().isSidebarOpen).toBe(true);

		fireEvent.click(screen.getByRole("tab", { name: "Browser" }));
		expect(useUiStore.getState().isSidebarOpen).toBe(true);

		fireEvent.click(screen.getByRole("tab", { name: "Summary" }));
		expect(useUiStore.getState().isSidebarOpen).toBe(true);

		fireEvent.click(screen.getByRole("tab", { name: "Browser" }));
		fireEvent.click(screen.getByRole("button", { name: "open files" }));
		expect(useUiStore.getState().isSidebarOpen).toBe(true);

		fireEvent.click(screen.getByRole("tab", { name: "Browser" }));
		fireEvent.click(screen.getByRole("button", { name: "Close inspector panel" }));
		await waitFor(() => expect(useUiStore.getState().isSidebarOpen).toBe(true));
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

	it("smoothly morphs the browser over the whole app window and back to its dock", async () => {
		const dockRect = {
			x: 780,
			y: 96,
			top: 96,
			left: 780,
			right: 1280,
			bottom: 796,
			width: 500,
			height: 700,
			toJSON: () => ({}),
		} as DOMRect;
		const rect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(dockRect);
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		try {
			render(<SessionView sessionId="sess-1" />);

			expect(screen.getByText("terminal center")).toBeInTheDocument();
			fireEvent.click(screen.getByRole("button", { name: "pop browser" }));

			// The portal begins exactly where the docked browser was, then expands.
			const overlay = document.querySelector(".browser-popout-overlay");
			expect(overlay).toHaveAttribute("data-phase", "opening");
			expect(overlay).toHaveStyle({ "--browser-popout-dock-left": "780px", "--browser-popout-dock-width": "500px" });
			expect(overlay).toHaveClass("browser-popout-overlay--mac-windowed");
			expect(screen.getByRole("button", { name: "browser center" })).toBeInTheDocument();
			expect(screen.getByText("terminal center")).toBeInTheDocument();
			await waitFor(() => expect(overlay).toHaveAttribute("data-phase", "open"));

			fireEvent.click(screen.getByRole("button", { name: "browser center" }));
			expect(overlay).toHaveAttribute("data-phase", "closing");
			expect(screen.getByRole("button", { name: "browser center" })).toBeInTheDocument();
			fireEvent.transitionEnd(overlay?.querySelector(".browser-popout-frame") as Element, {
				propertyName: "width",
			});
			// Keep the portal alive briefly at the destination so the native browser
			// can commit its final bounds before React hands ownership back to the dock.
			expect(screen.getByRole("button", { name: "browser center" })).toBeInTheDocument();
			await waitFor(() =>
				expect(screen.queryByRole("button", { name: "browser center" })).not.toBeInTheDocument(),
			);
			expect(screen.getByText("terminal center")).toBeInTheDocument();
			expect(browserDestroy).not.toHaveBeenCalled();
		} finally {
			rect.mockRestore();
		}
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

	it("previews docked files and only opens a center tab on explicit pop-out", () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "open files" }));
		fireEvent.click(screen.getByRole("button", { name: "select src/App.tsx" }));

		expect(screen.queryByRole("tab", { name: "App.tsx" })).not.toBeInTheDocument();
		expect(screen.queryByTestId("session-file-workspace")).not.toBeInTheDocument();
		expect(screen.getByText("terminal center")).toBeInTheDocument();

		fireEvent.click(screen.getByRole("button", { name: "pop out src/App.tsx" }));

		expect(screen.getByRole("tab", { name: "App.tsx" })).toHaveAttribute("aria-selected", "true");
		expect(screen.getByTestId("session-file-workspace")).toHaveTextContent("src/App.tsx");
		expect(screen.getByText("terminal center")).toBeInTheDocument();

		fireEvent.click(screen.getByRole("button", { name: "select agent tab" }));
		expect(screen.queryByTestId("session-file-workspace")).not.toBeInTheDocument();
		expect(screen.getByRole("tab", { name: "App.tsx" })).toHaveAttribute("aria-selected", "false");
	});

	it("previews a review file target in the Files inspector without replacing the center", async () => {
		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "view review file" }));

		await waitFor(() => {
			expect(screen.getByText("rail preview src/panel.tsx")).toBeInTheDocument();
		});
		expect(screen.queryByRole("tab", { name: "panel.tsx" })).not.toBeInTheDocument();
		expect(screen.queryByTestId("session-file-workspace")).not.toBeInTheDocument();
		expect(screen.getByText("terminal center")).toBeInTheDocument();
		expect(useUiStore.getState().inspectorSessions["sess-1"]?.view).toBe("files");
		expect(screen.queryByRole("button", { name: "files center" })).not.toBeInTheDocument();

		fireEvent.click(screen.getByRole("button", { name: "pop out src/panel.tsx" }));
		expect(screen.getByRole("tab", { name: "panel.tsx" })).toHaveAttribute("aria-selected", "true");
		expect(screen.getByTestId("session-file-workspace")).toHaveTextContent("src/panel.tsx");
	});

	it("resolves a basename against workspace files before opening on a cold cache", async () => {
		reviewGetMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
				return {
					data: {
						sessionId: "sess-1",
						files: [
							{
								path: "docs/notes.txt",
								status: "added",
								additions: 1,
								deletions: 0,
								binary: false,
								size: 10,
							},
						],
						truncated: false,
						sections: { staged: [], unstaged: [], untracked: [], committed: [] },
						commits: [],
						summary: { files: 1, additions: 1, deletions: 0 },
					},
					error: undefined,
				};
			}
			return { data: { reviewerHandleId: "", reviews: [], runs: [] }, error: undefined };
		});

		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "view review basename" }));

		await waitFor(() => {
			expect(screen.getByText("rail preview docs/notes.txt")).toBeInTheDocument();
		});
		expect(screen.queryByTestId("session-file-workspace")).not.toBeInTheDocument();
	});

	it("resolves a chat basename against workspace files before opening on a cold cache", async () => {
		workspaces[0].sessions[0].mode = "chat";
		reviewGetMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/workspace/files") {
				return {
					data: {
						sessionId: "sess-1",
						files: [
							{
								path: "docs/notes.txt",
								status: "added",
								additions: 1,
								deletions: 0,
								binary: false,
								size: 10,
							},
						],
						truncated: false,
						sections: { staged: [], unstaged: [], untracked: [], committed: [] },
						commits: [],
						summary: { files: 1, additions: 1, deletions: 0 },
					},
					error: undefined,
				};
			}
			return { data: { reviewerHandleId: "", reviews: [], runs: [] }, error: undefined };
		});

		act(() => useUiStore.getState().setInspectorOpen("sess-1", true));
		render(<SessionView sessionId="sess-1" />);

		fireEvent.click(screen.getByRole("button", { name: "open chat basename" }));

		await waitFor(() => {
			expect(screen.getByText("rail preview docs/notes.txt")).toBeInTheDocument();
		});
		expect(screen.queryByTestId("session-file-workspace")).not.toBeInTheDocument();
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

	it("badges Browser as unseen for a new live `ao preview` target instead of auto-opening it", () => {
		const worker = workerSession("sess-1");
		const { rerender } = render(<SessionView sessionId="sess-1" />);
		const viewBefore = inspectorButton().getAttribute("data-view");
		const openBefore = inspectorOpen("sess-1");

		worker.previewUrl = "http://localhost:5173/";
		worker.previewRevision = 1;
		rerender(<SessionView sessionId="sess-1" />);

		expect(screen.getByText("terminal center")).toBeInTheDocument();
		expect(inspectorOpen("sess-1")).toBe(openBefore);
		expect(inspectorButton()).toHaveAttribute("data-view", viewBefore);
		expect(browserUnseen("sess-1")).toBe(true);
	});

	it("badges Browser as unseen without opening a collapsed inspector when a new live preview arrives", () => {
		const worker = workerSession("sess-1");
		act(() => useUiStore.getState().setInspectorOpen("sess-1", false));
		const { rerender } = render(<SessionView sessionId="sess-1" />);

		worker.previewUrl = "http://localhost:5173/";
		worker.previewRevision = 1;
		rerender(<SessionView sessionId="sess-1" />);

		expect(inspectorOpen("sess-1")).toBe(false);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(browserUnseen("sess-1")).toBe(true);
	});

	it("keeps Summary on session entry and badges Browser as unseen for later preview work", () => {
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
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(browserUnseen("sess-2")).toBe(true);
	});

	// Regression: the session-entry effect that defaults a brand-new session to
	// Summary tracked only the single most-recently-initialized session ID, so
	// re-entering ANY session that was not the immediately preceding one looked
	// identical to a first-ever visit and forced it back to Summary — silently
	// discarding whatever tab (Files, Browser) the user had left it on.
	it("remembers the tab a session was left on when returning to it after visiting another session", () => {
		const { rerender } = render(<SessionView sessionId="sess-1" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");

		fireEvent.click(screen.getByRole("button", { name: "open files" }));
		expect(inspectorButton()).toHaveAttribute("data-view", "files");

		rerender(<SessionView sessionId="sess-2" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");

		rerender(<SessionView sessionId="sess-1" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "files");
	});

	// Regression: the "initialized" marker used to live in a component-local
	// ref, which is recreated whenever SessionView unmounts. Remounting an
	// already-visited session (e.g. across a route transition) then looked
	// identical to a first-ever visit and forced the tab back to Summary. The
	// marker now lives in the ui-store's persisted inspectorSessions state, so
	// it survives unmount/remount, not just re-renders of one mounted instance.
	it("remembers the tab a session was left on after unmounting and remounting the view", () => {
		const view = render(<SessionView sessionId="sess-1" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");

		fireEvent.click(screen.getByRole("button", { name: "open files" }));
		expect(inspectorButton()).toHaveAttribute("data-view", "files");

		view.unmount();

		render(<SessionView sessionId="sess-1" />);
		expect(inspectorButton()).toHaveAttribute("data-view", "files");
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
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(browserUnseen("sess-1")).toBe(true);

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

		expect(inspectorOpen("sess-1")).toBe(false);
		expect(inspectorButton()).toHaveAttribute("data-view", "summary");
		expect(browserUnseen("sess-1")).toBe(true);
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
