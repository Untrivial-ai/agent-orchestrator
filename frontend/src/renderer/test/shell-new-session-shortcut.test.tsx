import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { CancelledError } from "@tanstack/react-query";
import { Suspense, type ComponentType, type PropsWithChildren } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { KeybindingOverrides } from "../../shared/shortcuts";
import { useUiStore } from "../stores/ui-store";
import type { WorkspaceSummary } from "../types/workspace";

const shellMocks = vi.hoisted(() => {
	const state = {
		newSessionListener: undefined as (() => void) | undefined,
		keyboardShortcutsListener: undefined as (() => void) | undefined,
		newShellTerminalListener: undefined as (() => void) | undefined,
		openSettingsListener: undefined as (() => void) | undefined,
		previousSessionListener: undefined as (() => void) | undefined,
		nextSessionListener: undefined as (() => void) | undefined,
		focusTerminalListener: undefined as (() => void) | undefined,
		routeParams: {} as { projectId?: string; sessionId?: string },
		routeSearch: {} as Record<string, unknown>,
		workspaces: [] as WorkspaceSummary[],
		workspaceQuery: {
			data: [] as WorkspaceSummary[],
			dataUpdatedAt: 0,
			isError: false,
			isSuccess: true,
		},
		daemonStatus: { state: "stopped" } as {
			state: "ready" | "starting" | "stopped" | "error";
			port?: number;
			code?: "not_ready";
		},
		shellValue: undefined as { workspaceStartupState?: string } | undefined,
	};
	return {
		navigate: vi.fn(),
		onNewSessionShortcut: vi.fn((listener: () => void) => {
			state.newSessionListener = listener;
			return vi.fn();
		}),
		onKeyboardShortcutsHelp: vi.fn((listener: () => void) => {
			state.keyboardShortcutsListener = listener;
			return vi.fn();
		}),
		onNewShellTerminalShortcut: vi.fn((listener: () => void) => {
			state.newShellTerminalListener = listener;
			return vi.fn();
		}),
		openShellTerminal: vi.fn(),
		onOpenSettingsShortcut: vi.fn((listener: () => void) => {
			state.openSettingsListener = listener;
			return vi.fn();
		}),
		onPreviousSessionShortcut: vi.fn((listener: () => void) => {
			state.previousSessionListener = listener;
			return vi.fn();
		}),
		onNextSessionShortcut: vi.fn((listener: () => void) => {
			state.nextSessionListener = listener;
			return vi.fn();
		}),
		onFocusTerminalShortcut: vi.fn((listener: () => void) => {
			state.focusTerminalListener = listener;
			return vi.fn();
		}),
		getKeybindings: vi.fn(async () => ({})),
		setKeybindings: vi.fn(async (overrides: KeybindingOverrides) => overrides),
		setKeybindingRecording: vi.fn(async () => undefined),
		queryClient: {
			ensureQueryData: vi.fn(),
			fetchQuery: vi.fn(),
			getQueryState: vi.fn(),
			getQueryData: vi.fn(),
			invalidateQueries: vi.fn(),
			prefetchQuery: vi.fn(async () => undefined),
			setQueryData: vi.fn(),
		},
		state,
	};
});

vi.mock("@tanstack/react-query", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-query")>()),
	useQueryClient: () => shellMocks.queryClient,
	// TerminalCacheProvider owns reviewer queries in production. This shell-only
	// harness has no routed terminal children and intentionally omits a provider.
	useQueries: () => [],
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	createFileRoute: () => (options: unknown) => ({ options }),
	Outlet: () => null,
	useMatchRoute: () => () => false,
	useNavigate: () => shellMocks.navigate,
	useParams: () => shellMocks.state.routeParams,
	useSearch: () => shellMocks.state.routeSearch,
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		app: {
			onNewSessionShortcut: shellMocks.onNewSessionShortcut,
			onKeyboardShortcutsHelp: shellMocks.onKeyboardShortcutsHelp,
			onNewShellTerminalShortcut: shellMocks.onNewShellTerminalShortcut,
			onOpenSettingsShortcut: shellMocks.onOpenSettingsShortcut,
			onPreviousSessionShortcut: shellMocks.onPreviousSessionShortcut,
			onNextSessionShortcut: shellMocks.onNextSessionShortcut,
			onFocusTerminalShortcut: shellMocks.onFocusTerminalShortcut,
		},
		keybindings: {
			get: shellMocks.getKeybindings,
			set: shellMocks.setKeybindings,
			setRecording: shellMocks.setKeybindingRecording,
		},
		window: {},
		tray: {
			setAttentionState: () => undefined,
			onOpenSession: () => () => undefined,
		},
	},
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceQuery: () => shellMocks.state.workspaceQuery,
	workspaceQueryKey: ["workspaces"],
	workspaceQueryOptions: {},
}));

vi.mock("../hooks/useDaemonStatus", () => ({
	useDaemonStatus: () => shellMocks.state.daemonStatus,
}));

// The shell layout opens standalone terminals; this suite only covers the
// shortcut subscriptions, so the mutation is stubbed rather than driven.
vi.mock("../hooks/useShellTerminals", () => ({
	useShellTerminals: () => ({ data: [], isSuccess: true }),
	useOpenShellTerminal: () => ({ mutate: shellMocks.openShellTerminal }),
}));

vi.mock("../hooks/useAgentsQuery", () => ({
	agentsQueryKey: ["agents"],
	agentsQueryOptions: {},
	refreshAgents: vi.fn(),
	// The shell reports the install's agent inventory once per launch, so the
	// mock has to answer this too. Undefined data means the hook reports nothing,
	// which keeps these shortcut tests free of telemetry side effects.
	useAgentsQuery: () => ({ data: undefined }),
}));

vi.mock("../components/NotificationCenter", () => ({ NotificationRuntime: () => null }));
vi.mock("../components/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../components/OrchestratorReplacementDialog", () => ({ OrchestratorReplacementDialog: () => null }));
vi.mock("../components/ShellTopbar", () => ({ ShellTopbar: () => null }));
vi.mock("../components/TitlebarNav", async () => {
	const { useUiStore: useStore } = await vi.importActual<typeof import("../stores/ui-store")>("../stores/ui-store");
	return {
		TitlebarNav: ({ onSidebarPreviewEnter }: { onSidebarPreviewEnter?: () => void }) => {
			const isSidebarOpen = useStore((state) => state.isSidebarOpen);
			const toggleSidebar = useStore((state) => state.toggleSidebar);
			return (
				<button
					aria-label={isSidebarOpen ? "Collapse sidebar" : "Expand sidebar"}
					onClick={toggleSidebar}
					onPointerEnter={onSidebarPreviewEnter}
					type="button"
				/>
			);
		},
	};
});
vi.mock("../components/WindowTitlebar", () => ({ WindowTitlebar: () => null }));
vi.mock("../components/SettingsDialog", () => ({ SettingsDialog: () => null }));
vi.mock("../components/KeyboardShortcutsDialog", () => ({
	KeyboardShortcutsDialog: ({ open }: { open: boolean }) => (open ? <div data-testid="keyboard-shortcuts" /> : null),
}));
vi.mock("../lib/shell-context", () => ({
	ShellProvider: ({ children, value }: PropsWithChildren<{ value?: { workspaceStartupState?: string } }>) => {
		shellMocks.state.shellValue = value;
		return children;
	},
}));
vi.mock("../components/ui/sidebar", () => ({
	SidebarProvider: ({ children, open }: PropsWithChildren<{ open?: boolean }>) => (
		<div data-open={open ? "true" : "false"} data-testid="sidebar-provider">
			{children}
		</div>
	),
}));

vi.mock("../components/GlobalNewTaskDialog", async () => {
	const { useUiStore: useStore } = await vi.importActual<typeof import("../stores/ui-store")>("../stores/ui-store");
	return {
		GlobalNewTaskDialog: () => {
			const request = useStore((state) => state.newTaskRequest);
			return request ? <div data-testid="new-task-flow" data-project={request.projectId} /> : null;
		},
	};
});

vi.mock("../components/Sidebar", async () => {
	const { useUiStore: useStore } = await vi.importActual<typeof import("../stores/ui-store")>("../stores/ui-store");
	return {
		Sidebar: ({ isOverlay, onPreviewLeave, topbarOffset }: { isOverlay?: boolean; onPreviewLeave?: () => void; topbarOffset?: string }) => {
			const nonce = useStore((state) => state.createProjectNonce);
			return (
				<div data-overlay={isOverlay ? "true" : "false"} data-testid="sidebar" data-topbar-offset={topbarOffset} onPointerLeave={onPreviewLeave}>
					{nonce > 0 ? <div data-testid="create-project-flow" /> : null}
				</div>
			);
		},
	};
});

import { Route } from "../routes/_shell";
const ShellRoute = Route.options.component as ComponentType;

const workspaces = [
	{
		id: "proj-1",
		name: "Project One",
		path: "/one",
		sessions: [
			{ id: "sess-1", workspaceId: "proj-1", status: "working" },
			{ id: "sess-2", workspaceId: "proj-1", status: "terminated" },
			{ id: "sess-merged-terminated", workspaceId: "proj-1", status: "merged", isTerminated: true },
			{ id: "sess-3", workspaceId: "proj-1", status: "idle" },
		],
	},
	{
		id: "proj-2",
		name: "Project Two",
		path: "/two",
		sessions: [{ id: "sess-cross", workspaceId: "proj-2", status: "working" }],
	},
] as unknown as WorkspaceSummary[];

async function renderShell() {
	let view: ReturnType<typeof render> | undefined;
	await act(async () => {
		view = render(
			<Suspense fallback={null}>
				<ShellRoute />
			</Suspense>,
		);
	});
	await waitFor(() => expect(shellMocks.onNewSessionShortcut).toHaveBeenCalledTimes(1), { timeout: 30_000 });
	await waitFor(() => expect(shellMocks.onKeyboardShortcutsHelp).toHaveBeenCalledTimes(1));
	await waitFor(() => expect(shellMocks.onNewShellTerminalShortcut).toHaveBeenCalledTimes(1));
	await waitFor(() => expect(shellMocks.onOpenSettingsShortcut).toHaveBeenCalledTimes(1));
	await waitFor(() => expect(shellMocks.onPreviousSessionShortcut).toHaveBeenCalledTimes(1));
	await waitFor(() => expect(shellMocks.onNextSessionShortcut).toHaveBeenCalledTimes(1));
	await waitFor(() => expect(shellMocks.onFocusTerminalShortcut).toHaveBeenCalledTimes(1));
	return view!;
}

function emitShortcut() {
	const listener = shellMocks.state.newSessionListener;
	if (!listener) throw new Error("shell shortcut listener was not registered");
	act(() => listener());
}

beforeEach(() => {
	shellMocks.navigate.mockReset();
	shellMocks.onNewSessionShortcut.mockClear();
	shellMocks.onKeyboardShortcutsHelp.mockClear();
	shellMocks.onNewShellTerminalShortcut.mockClear();
	shellMocks.openShellTerminal.mockClear();
	shellMocks.state.newShellTerminalListener = undefined;
	shellMocks.onOpenSettingsShortcut.mockClear();
	shellMocks.onPreviousSessionShortcut.mockClear();
	shellMocks.onNextSessionShortcut.mockClear();
	shellMocks.onFocusTerminalShortcut.mockClear();
	shellMocks.state.newSessionListener = undefined;
	shellMocks.state.keyboardShortcutsListener = undefined;
	shellMocks.state.openSettingsListener = undefined;
	shellMocks.state.previousSessionListener = undefined;
	shellMocks.state.nextSessionListener = undefined;
	shellMocks.state.focusTerminalListener = undefined;
	shellMocks.state.routeParams = {};
	shellMocks.state.routeSearch = {};
	shellMocks.state.workspaces = workspaces;
	shellMocks.state.workspaceQuery = {
		data: workspaces,
		dataUpdatedAt: 0,
		isError: false,
		isSuccess: true,
	};
	shellMocks.state.daemonStatus = { state: "error", code: "not_ready" };
	shellMocks.state.shellValue = undefined;
	shellMocks.queryClient.fetchQuery.mockReset();
	shellMocks.queryClient.getQueryState.mockReset().mockReturnValue({ dataUpdatedAt: 0 });
	useUiStore.setState({
		createProjectNonce: 0,
		isSidebarOpen: true,
		newTaskRequest: null,
		newShellTerminalNonce: 0,
		settingsModal: null,
	});
});

describe("shell workspace startup", () => {
	it("leaves the session topbar row to the session split instead of reserving a full-width shell row", async () => {
		shellMocks.state.routeParams = { sessionId: "sess-1" };
		await renderShell();

		const sidebar = screen.getByTestId("sidebar");
		expect(screen.queryByTestId("session-topbar-host")).not.toBeInTheDocument();
		// The route shell owns only the frame. SessionView places its topbar host
		// inside the terminal panel so the inspector header can occupy this row too.
		expect(sidebar).not.toHaveAttribute("data-topbar-offset", "session");
		expect(document.querySelector(".center-panel-shell--session > .center-panel-surface")).toBeInTheDocument();
	});

	it("forces a confirmed fetch and preserves a collapsed sidebar preference", async () => {
		let resolveFetch: ((value: WorkspaceSummary[]) => void) | undefined;
		useUiStore.setState({ isSidebarOpen: false });
		shellMocks.state.daemonStatus = { state: "ready", port: 4777 };
		shellMocks.state.workspaceQuery = {
			data: [],
			dataUpdatedAt: 100,
			isError: false,
			isSuccess: true,
		};
		shellMocks.queryClient.getQueryState.mockReturnValue({ dataUpdatedAt: 100 });
		shellMocks.queryClient.fetchQuery.mockReturnValueOnce(
			new Promise<WorkspaceSummary[]>((resolve) => {
				resolveFetch = resolve;
			}),
		);

		const view = await renderShell();
		expect(shellMocks.state.shellValue?.workspaceStartupState).toBe("loading");
		expect(screen.getByTestId("sidebar-provider")).toHaveAttribute("data-open", "false");
		expect(shellMocks.queryClient.fetchQuery).toHaveBeenCalledWith(expect.objectContaining({ staleTime: 0 }));

		await act(async () => resolveFetch?.(workspaces));

		await waitFor(() => expect(shellMocks.state.shellValue?.workspaceStartupState).toBe("ready"));
		expect(screen.getByTestId("sidebar-provider")).toHaveAttribute("data-open", "false");
		expect(useUiStore.getState().isSidebarOpen).toBe(false);
		view.unmount();
	});

	it("does not turn a cancelled confirmed fetch into a startup error", async () => {
		shellMocks.state.daemonStatus = { state: "ready", port: 4777 };
		shellMocks.state.workspaceQuery = {
			data: [],
			dataUpdatedAt: 100,
			isError: false,
			isSuccess: true,
		};
		shellMocks.queryClient.getQueryState.mockReturnValue({ dataUpdatedAt: 100 });
		shellMocks.queryClient.fetchQuery.mockRejectedValueOnce(new CancelledError());

		await renderShell();

		await waitFor(() =>
			expect(shellMocks.queryClient.fetchQuery).toHaveBeenCalledWith(expect.objectContaining({ staleTime: 0 })),
		);
		expect(shellMocks.state.shellValue?.workspaceStartupState).toBe("loading");
	});

	it("forces a workspace fetch when a daemon returns ready on the same port", async () => {
		shellMocks.state.daemonStatus = { state: "starting", port: 4777 };
		shellMocks.queryClient.fetchQuery.mockResolvedValue(workspaces);

		const view = await renderShell();
		expect(shellMocks.state.shellValue?.workspaceStartupState).toBe("loading");
		expect(shellMocks.queryClient.fetchQuery).not.toHaveBeenCalled();

		shellMocks.state.daemonStatus = { state: "ready", port: 4777 };
		view.rerender(
			<Suspense fallback={null}>
				<ShellRoute />
			</Suspense>,
		);

		await waitFor(() =>
			expect(shellMocks.queryClient.fetchQuery).toHaveBeenCalledWith(expect.objectContaining({ staleTime: 0 })),
		);
		await waitFor(() => expect(shellMocks.state.shellValue?.workspaceStartupState).toBe("ready"));
	});

	it("recovers after a newer workspace query succeeds", async () => {
		shellMocks.state.daemonStatus = { state: "ready", port: 4777 };
		shellMocks.state.workspaceQuery = {
			data: workspaces,
			dataUpdatedAt: 100,
			isError: false,
			isSuccess: true,
		};
		shellMocks.queryClient.getQueryState.mockReturnValue({ dataUpdatedAt: 100 });
		shellMocks.queryClient.fetchQuery.mockRejectedValueOnce(new Error("temporary failure"));

		const view = await renderShell();
		await waitFor(() => expect(shellMocks.state.shellValue?.workspaceStartupState).toBe("error"));

		shellMocks.state.workspaceQuery = {
			...shellMocks.state.workspaceQuery,
			dataUpdatedAt: 200,
		};
		view.rerender(
			<Suspense fallback={null}>
				<ShellRoute />
			</Suspense>,
		);

		await waitFor(() => expect(shellMocks.state.shellValue?.workspaceStartupState).toBe("ready"));
	});
});

describe("shell sidebar hover preview", () => {
	it("temporarily overlays a collapsed sidebar from the titlebar toggle and closes after pointer leave", async () => {
		useUiStore.setState({ isSidebarOpen: false });
		await renderShell();

		const provider = screen.getByTestId("sidebar-provider");
		const sidebar = screen.getByTestId("sidebar");
		const previewTrigger = screen.getByRole("button", { name: "Expand sidebar" });
		expect(screen.queryByRole("button", { name: "Preview sidebar" })).not.toBeInTheDocument();

		expect(provider).toHaveAttribute("data-open", "false");
		fireEvent.pointerEnter(previewTrigger);

		expect(provider).toHaveAttribute("data-open", "true");
		expect(sidebar).toHaveAttribute("data-overlay", "true");
		expect(useUiStore.getState().isSidebarOpen).toBe(false);

		fireEvent.pointerMove(window, { clientX: 500, clientY: 300 });
		await waitFor(() => expect(provider).toHaveAttribute("data-open", "false"));
		expect(useUiStore.getState().isSidebarOpen).toBe(false);
	});

	it("pins the sidebar open when the titlebar toggle is clicked", async () => {
		useUiStore.setState({ isSidebarOpen: false });
		await renderShell();

		const previewTrigger = screen.getByRole("button", { name: "Expand sidebar" });
		fireEvent.pointerEnter(previewTrigger);
		fireEvent.click(previewTrigger);

		expect(useUiStore.getState().isSidebarOpen).toBe(true);
		expect(screen.getByTestId("sidebar-provider")).toHaveAttribute("data-open", "true");
		expect(screen.getByRole("button", { name: "Collapse sidebar" })).toBeInTheDocument();
	});
});

describe("shell new-shell-terminal shortcut subscription", () => {
	function pressNewShellTerminal() {
		const listener = shellMocks.state.newShellTerminalListener;
		if (!listener) throw new Error("new-shell-terminal listener was not registered");
		act(() => listener());
	}

	// Regression: the shell LAYOUT must own this, not the session view. When the
	// session view owned it, the shortcut did nothing outside a session route —
	// nothing was mounted to hear it.
	it("opens a terminal even with no session on screen", async () => {
		await renderShell();

		pressNewShellTerminal();

		expect(useUiStore.getState().newShellTerminalNonce).toBe(1);
		expect(shellMocks.openShellTerminal).toHaveBeenCalledTimes(1);
	});

	it("scopes the terminal to the project in scope", async () => {
		shellMocks.state.routeParams = { projectId: "proj-1" };
		await renderShell();

		pressNewShellTerminal();

		expect(shellMocks.openShellTerminal).toHaveBeenCalledWith(
			expect.objectContaining({ projectId: "proj-1" }),
			expect.anything(),
		);
	});

	// Regression: a terminal opened from a session view must carry the session
	// id, not just its owning project's, so the daemon can resolve the
	// session's own worktree instead of the registered project root.
	it("scopes the terminal to the session in scope", async () => {
		shellMocks.state.routeParams = { sessionId: "sess-1" };
		await renderShell();

		pressNewShellTerminal();

		expect(shellMocks.openShellTerminal).toHaveBeenCalledWith(
			expect.objectContaining({ projectId: "proj-1", sessionId: "sess-1" }),
			expect.anything(),
		);
	});

	// Session terminals always belong to the session on screen — there is no
	// longer an "owner" session whose worktree could be borrowed here (#3208).
	it("scopes the terminal to the session on screen, not the route's project alone", async () => {
		shellMocks.state.routeParams = { projectId: "proj-2", sessionId: "sess-cross" };
		await renderShell();

		pressNewShellTerminal();

		expect(shellMocks.openShellTerminal).toHaveBeenCalledWith(
			expect.objectContaining({ projectId: "proj-2", sessionId: "sess-cross" }),
			expect.anything(),
		);
	});

	it("re-fires on a repeat press so a second terminal can be opened", async () => {
		await renderShell();

		pressNewShellTerminal();
		pressNewShellTerminal();

		expect(useUiStore.getState().newShellTerminalNonce).toBe(2);
		expect(shellMocks.openShellTerminal).toHaveBeenCalledTimes(2);
	});
});

describe("shell keyboard-shortcuts help subscription", () => {
	it("opens the keyboard-shortcuts dialog", async () => {
		await renderShell();

		const listener = shellMocks.state.keyboardShortcutsListener;
		if (!listener) throw new Error("keyboard-shortcuts listener was not registered");
		act(() => listener());

		expect(screen.getByTestId("keyboard-shortcuts")).toBeInTheDocument();
	});
});

describe("shell new-session shortcut subscription", () => {
	it("opens the new-task flow for the route project", async () => {
		shellMocks.state.routeParams = { projectId: "proj-1" };
		await renderShell();

		emitShortcut();

		expect(screen.getByTestId("new-task-flow")).toHaveAttribute("data-project", "proj-1");
		expect(screen.queryByTestId("create-project-flow")).not.toBeInTheDocument();
	});

	it("opens the new-task flow for the project owning the current session", async () => {
		shellMocks.state.routeParams = { sessionId: "sess-1" };
		await renderShell();

		emitShortcut();

		expect(screen.getByTestId("new-task-flow")).toHaveAttribute("data-project", "proj-1");
	});

	it("opens the create-project flow when no project is in scope", async () => {
		await renderShell();

		emitShortcut();

		expect(screen.getByTestId("create-project-flow")).toBeInTheDocument();
		expect(screen.queryByTestId("new-task-flow")).not.toBeInTheDocument();
	});
});

describe("shell application shortcut subscriptions", () => {
	it("opens settings", async () => {
		await renderShell();

		act(() => shellMocks.state.openSettingsListener?.());

		expect(useUiStore.getState().settingsModal).toEqual({ scope: "global" });
		expect(shellMocks.navigate).not.toHaveBeenCalled();
	});

	it("moves to the next active session in the current project", async () => {
		shellMocks.state.routeParams = { sessionId: "sess-1" };
		await renderShell();

		act(() => shellMocks.state.nextSessionListener?.());

		expect(shellMocks.navigate).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId: "sess-3" },
		});
	});

	it("wraps to the last session when moving previous from the first", async () => {
		shellMocks.state.routeParams = { sessionId: "sess-1" };
		await renderShell();

		act(() => shellMocks.state.previousSessionListener?.());

		expect(shellMocks.navigate).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId: "sess-3" },
		});
	});

	it("focuses the active terminal without targeting an earlier parked xterm", async () => {
		const parked = document.createElement("div");
		parked.dataset.terminalActivationPhase = "parked";
		parked.inert = true;
		const parkedInput = document.createElement("textarea");
		parkedInput.className = "xterm-helper-textarea";
		parked.appendChild(parkedInput);
		const active = document.createElement("div");
		active.dataset.terminalActivationPhase = "visible";
		const activeInput = document.createElement("textarea");
		activeInput.className = "xterm-helper-textarea";
		active.appendChild(activeInput);
		document.body.append(parked, active);
		await renderShell();

		act(() => shellMocks.state.focusTerminalListener?.());

		expect(document.activeElement).toBe(activeInput);
		parked.remove();
		active.remove();
	});
});
