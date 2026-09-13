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
		openFolderPathListener: undefined as ((path: string) => void) | undefined,
		routeParams: {} as { projectId?: string; sessionId?: string },
		routeSearch: {} as Record<string, unknown>,
		matchRouteTarget: null as string | null,
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
		shellValue: undefined as
				| {
						workspaceStartupState?: string;
						createProject?: (input: {
							path: string;
							defaultBranch?: string;
							workerAgent: string;
							orchestratorAgent: string;
							asWorkspace?: boolean;
						}) => Promise<void>;
				  }
			| undefined,
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
		openShellTerminal: vi.fn((input: { projectId?: string; sessionId?: string }) => ({
			handleId: "pending-shell:test",
			projectId: input.projectId,
			sessionId: input.sessionId,
			workingDir: "",
			title: "Terminal 1",
			createdAt: new Date().toISOString(),
			optimistic: true as const,
		})),
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
		getPathForFile: vi.fn((file: File) => `/dropped/${file.name}`),
		onOpenFolderPath: vi.fn((listener: (path: string) => void) => {
			state.openFolderPathListener = listener;
			return vi.fn();
		}),
		getKeybindings: vi.fn(async () => ({})),
		setKeybindings: vi.fn(async (overrides: KeybindingOverrides) => overrides),
		setKeybindingRecording: vi.fn(async () => undefined),
		queryClient: {
			ensureQueryData: vi.fn(),
			fetchQuery: vi.fn(),
			getQueryData: vi.fn(),
			getQueryState: vi.fn(),
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
	useMatchRoute: () => (options: { to: string }) => shellMocks.state.matchRouteTarget === options.to,
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
			getPathForFile: shellMocks.getPathForFile,
			onOpenFolderPath: shellMocks.onOpenFolderPath,
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
	useWorkspaceTraySessions: () => ({ data: [] }),
	workspaceQueryKey: ["workspaces"],
	workspaceQueryOptions: {},
}));

vi.mock("../hooks/useDaemonStatus", () => ({
	useDaemonStatus: () => shellMocks.state.daemonStatus,
}));

vi.mock("../lib/api-client", async (importOriginal) => ({
	...(await importOriginal<typeof import("../lib/api-client")>()),
	apiClient: { POST: vi.fn(), DELETE: vi.fn() },
	apiErrorCode: (error: { code?: string } | undefined) => error?.code,
	apiErrorMessage: (error: { message?: string } | undefined) => error?.message ?? "request failed",
	hasTrustedApiBaseUrl: () => true,
}));

vi.mock("../lib/daemon-status", () => ({
	refreshDaemonStatus: vi.fn(async () => shellMocks.state.daemonStatus),
}));

// TerminalCacheProvider resolves the cloud terminal transport in production.
// These shell shortcut tests never mount a terminal, so keep that unrelated
// settings/query path out of the provider-free harness.
vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({ client: {}, ready: false, baseUrl: "" }),
}));

// The shell layout opens standalone terminals; this suite only covers the
// shortcut subscriptions, so the mutation is stubbed rather than driven.
vi.mock("../hooks/useShellTerminals", () => ({
	useShellTerminals: () => ({ data: [], isSuccess: true }),
	useOpenShellTerminal: () => ({
		open: shellMocks.openShellTerminal,
		mutate: shellMocks.openShellTerminal,
	}),
}));

vi.mock("../hooks/useAgentReadinessQuery", () => ({
	agentReadinessQueryKey: ["agent-readiness"],
	// The shell reports the install's agent inventory once per launch, so the
	// mock has to answer this too. Undefined data means the hook reports nothing,
	// which keeps these shortcut tests free of telemetry side effects.
	useAgentReadinessQuery: () => ({ data: undefined }),
	useEnsureAgentReadiness: vi.fn(),
}));

vi.mock("../components/NotificationCenter", () => ({ NotificationRuntime: () => null }));
vi.mock("../components/DaemonStartupLoader", () => ({
	DaemonStartupLoader: () => <div data-testid="daemon-startup-loader" />,
}));
vi.mock("../components/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../components/OrchestratorReplacementDialog", () => ({ OrchestratorReplacementDialog: () => null }));
vi.mock("../components/ShellTopbar", () => ({ ShellTopbar: () => null }));
vi.mock("../components/TitlebarNav", async () => {
	const { sidebarIsVisible, useUiStore: useStore } = await vi.importActual<
		typeof import("../stores/ui-store")
	>("../stores/ui-store");
	return {
		TitlebarNav: () => {
			const isSidebarOpen = useStore(sidebarIsVisible);
			const toggleSidebar = useStore((state) => state.toggleSidebar);
			return (
				<button
					aria-label={isSidebarOpen ? "Collapse sidebar" : "Expand sidebar"}
					onClick={toggleSidebar}
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

vi.mock("../components/GlobalToast", async () => {
	const { useUiStore: useStore } = await vi.importActual<typeof import("../stores/ui-store")>("../stores/ui-store");
	return {
		GlobalToast: () => {
			const toast = useStore((state) => state.globalToast);
			return toast ? <div data-testid="global-toast">{toast.title}</div> : null;
		},
	};
});

vi.mock("../components/Sidebar", async () => {
	const { useUiStore: useStore } = await vi.importActual<typeof import("../stores/ui-store")>("../stores/ui-store");
	return {
		SIDEBAR_DEFAULT_WIDTH: 240,
		Sidebar: ({ topbarOffset }: { topbarOffset?: string }) => {
			const nonce = useStore((state) => state.createProjectNonce);
			const folderDropRequest = useStore((state) => state.folderDropRequest);
			return (
				<div data-testid="sidebar" data-topbar-offset={topbarOffset}>
					{nonce > 0 || folderDropRequest ? (
						<div data-path={folderDropRequest?.path} data-testid="create-project-flow" />
					) : null}
				</div>
			);
		},
	};
});

import { Route } from "../routes/_shell";
import { apiClient } from "../lib/api-client";
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
	shellMocks.getPathForFile.mockClear();
	shellMocks.onOpenFolderPath.mockClear();
	shellMocks.state.newSessionListener = undefined;
	shellMocks.state.keyboardShortcutsListener = undefined;
	shellMocks.state.openSettingsListener = undefined;
	shellMocks.state.previousSessionListener = undefined;
	shellMocks.state.nextSessionListener = undefined;
	shellMocks.state.focusTerminalListener = undefined;
	shellMocks.state.openFolderPathListener = undefined;
	shellMocks.state.routeParams = {};
	shellMocks.state.routeSearch = {};
	shellMocks.state.matchRouteTarget = null;
	shellMocks.state.workspaces = workspaces;
	shellMocks.state.workspaceQuery = {
		data: workspaces,
		dataUpdatedAt: 0,
		isError: false,
		isSuccess: true,
	};
	shellMocks.state.daemonStatus = { state: "error", code: "not_ready" };
	shellMocks.state.shellValue = undefined;
	shellMocks.queryClient.fetchQuery.mockReset().mockResolvedValue(workspaces);
	shellMocks.queryClient.getQueryData.mockReset().mockReturnValue(workspaces);
	shellMocks.queryClient.getQueryState.mockReset().mockReturnValue({ dataUpdatedAt: 0 });
	useUiStore.setState({
		createProjectNonce: 0,
		folderDropRequest: null,
		globalToast: null,
		isSidebarOpen: true,
		newTaskRequest: null,
		newShellTerminalNonce: 0,
		activeShellTerminalHandleId: null,
		settingsModal: null,
	});
});

describe("shell workspace startup", () => {
	it("routes duplicate-path project adds to the registered project and shows a toast", async () => {
		shellMocks.state.daemonStatus = { state: "ready", port: 4777 };
		vi.mocked(apiClient.POST).mockResolvedValueOnce({
			data: undefined,
			error: {
				code: "PATH_ALREADY_REGISTERED",
				message: "A project at this path is already registered",
			},
		});

		await renderShell();

		await expect(
			shellMocks.state.shellValue?.createProject?.({
				path: "/one/",
				workerAgent: "codex",
				orchestratorAgent: "codex",
			}),
		).resolves.toBeUndefined();

		expect(shellMocks.navigate).toHaveBeenCalledWith({
			to: "/projects/$projectId",
			params: { projectId: "proj-1" },
		});
		expect(screen.getByTestId("global-toast")).toHaveTextContent("Project already added");
	});

	it("uses the daemon project identity when an imported path is an alias", async () => {
		shellMocks.state.daemonStatus = { state: "ready", port: 4777 };
		vi.mocked(apiClient.POST).mockResolvedValueOnce({ error: {
			code: "PATH_ALREADY_REGISTERED", message: "Already registered", details: { existingProjectId: "proj-1" },
		} });
		await renderShell();
		await expect(shellMocks.state.shellValue?.createProject?.({
			path: "/alias/one", workerAgent: "codex", orchestratorAgent: "codex",
		})).resolves.toBeUndefined();
		expect(shellMocks.navigate).toHaveBeenCalledWith({
			to: "/projects/$projectId", params: { projectId: "proj-1" },
		});
	});

	it("refreshes a stale project list before opening the daemon's registered identity", async () => {
		shellMocks.state.daemonStatus = { state: "ready", port: 4777 };
		vi.mocked(apiClient.POST).mockResolvedValueOnce({ error: {
			code: "PATH_ALREADY_REGISTERED", message: "Already registered", details: { existingProjectId: "new-registration" },
		} });
		await renderShell();
		shellMocks.queryClient.fetchQuery.mockResolvedValueOnce([{ ...workspaces[0], id: "new-registration", path: "/canonical" }]);
		await expect(shellMocks.state.shellValue?.createProject?.({ path: "/alias", workerAgent: "codex", orchestratorAgent: "codex" })).resolves.toBeUndefined();
		expect(shellMocks.queryClient.fetchQuery).toHaveBeenCalledWith(expect.objectContaining({ staleTime: 0, retry: false }));
		expect(shellMocks.navigate).toHaveBeenCalledWith({ to: "/projects/$projectId", params: { projectId: "new-registration" } });
	});

	it.each([undefined, null, 123, [], "", "missing", "../outside"])(
		"does not navigate to an unverified conflict identity: %j", async (existingProjectId) => {
			shellMocks.state.daemonStatus = { state: "ready", port: 4777 };
			vi.mocked(apiClient.POST).mockResolvedValueOnce({ error: {
				code: "PATH_ALREADY_REGISTERED", message: "Already registered", requestId: "request-4403", details: { existingProjectId },
			} });
			await renderShell();
			await expect(shellMocks.state.shellValue?.createProject?.({ path: "/unknown", workerAgent: "codex", orchestratorAgent: "codex" })).rejects.toMatchObject({
				code: "PATH_ALREADY_REGISTERED", requestId: "request-4403", details: { existingProjectId },
			});
			expect(shellMocks.navigate).not.toHaveBeenCalled();
		},
	);

	it("preserves the original conflict when refreshing registered projects fails", async () => {
		shellMocks.state.daemonStatus = { state: "ready", port: 4777 };
		vi.mocked(apiClient.POST).mockResolvedValueOnce({ error: {
			code: "PATH_ALREADY_REGISTERED", message: "Already registered", details: { existingProjectId: "not-cached" },
		} });
		await renderShell();
		shellMocks.queryClient.fetchQuery.mockRejectedValueOnce(new Error("refresh failed"));
		await expect(shellMocks.state.shellValue?.createProject?.({ path: "/unknown", workerAgent: "codex", orchestratorAgent: "codex" })).rejects.toMatchObject({ code: "PATH_ALREADY_REGISTERED", message: "Already registered" });
		expect(shellMocks.navigate).not.toHaveBeenCalled();
	});

	it("forwards an explicit default branch when creating a local project", async () => {
		shellMocks.state.daemonStatus = { state: "ready", port: 4777 };
		vi.mocked(apiClient.POST).mockResolvedValueOnce({
			data: {
				project: {
					id: "proj-new",
					name: "proj-new",
					kind: "single_repo",
					path: "/repo/project",
					workspaceRepos: [],
				},
			},
		});

		await renderShell();

		await shellMocks.state.shellValue?.createProject?.({
			path: "/repo/project",
			defaultBranch: "main",
			workerAgent: "codex",
			orchestratorAgent: "codex",
		});

		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/projects", {
			body: {
				path: "/repo/project",
				asWorkspace: undefined,
				config: {
					defaultBranch: "main",
					worker: { agent: "codex" },
					orchestrator: { agent: "codex" },
				},
			},
		});
	});

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
		expect(screen.getByTestId("daemon-startup-loader")).toBeInTheDocument();
		expect(screen.queryByTestId("sidebar-provider")).not.toBeInTheDocument();
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
		expect(screen.getByTestId("daemon-startup-loader")).toBeInTheDocument();
	});

	it("forces a workspace fetch when a daemon returns ready on the same port", async () => {
		shellMocks.state.daemonStatus = { state: "starting", port: 4777 };
		shellMocks.queryClient.fetchQuery.mockResolvedValue(workspaces);

		const view = await renderShell();
		expect(screen.getByTestId("daemon-startup-loader")).toBeInTheDocument();
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

describe("shell sidebar toggle", () => {
	it("does not open a collapsed sidebar on titlebar hover", async () => {
		useUiStore.setState({ isSidebarOpen: false });
		await renderShell();

		const provider = screen.getByTestId("sidebar-provider");
		const expandTrigger = screen.getByRole("button", { name: "Expand sidebar" });

		expect(provider).toHaveAttribute("data-open", "false");
		fireEvent.pointerEnter(expandTrigger);

		expect(provider).toHaveAttribute("data-open", "false");
		expect(useUiStore.getState().isSidebarOpen).toBe(false);
	});

	it("opens the sidebar when the titlebar toggle is clicked", async () => {
		useUiStore.setState({ isSidebarOpen: false });
		await renderShell();

		fireEvent.click(screen.getByRole("button", { name: "Expand sidebar" }));

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
	it("opens a terminal from the dedicated terminals route", async () => {
		shellMocks.state.matchRouteTarget = "/terminals";
		await renderShell();

		pressNewShellTerminal();

		expect(useUiStore.getState().newShellTerminalNonce).toBe(1);
		expect(shellMocks.openShellTerminal).toHaveBeenCalledTimes(1);
	});

	// Regression (#4772): ⌘T on the project board must not yank users into /terminals.
	it("ignores the shortcut on the project board", async () => {
		shellMocks.state.routeParams = { projectId: "proj-1" };
		await renderShell();

		pressNewShellTerminal();

		expect(useUiStore.getState().newShellTerminalNonce).toBe(0);
		expect(shellMocks.openShellTerminal).not.toHaveBeenCalled();
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

	it("preserves the cloud identity for a session-scoped terminal", async () => {
		const session = workspaces[0]!.sessions[0]!;
		session.cloud = { orgId: "cloud-org" };
		shellMocks.state.routeParams = { sessionId: "sess-1" };
		await renderShell();

		pressNewShellTerminal();

		expect(shellMocks.openShellTerminal).toHaveBeenCalledWith(
			expect.objectContaining({
				projectId: "proj-1",
				sessionId: "sess-1",
				cloud: { orgId: "cloud-org" },
			}),
			expect.anything(),
		);
		delete session.cloud;
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
		shellMocks.state.routeParams = { sessionId: "sess-1" };
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

describe("shell folder drag-and-drop", () => {
	function fileDragTransfer(options: { fileName?: string; isDirectory: boolean }) {
		const file = new File([], options.fileName ?? "dropped-folder");
		return {
			dropEffect: "none",
			items: [
				{
					getAsFile: () => file,
					kind: "file",
					webkitGetAsEntry: () => ({ isDirectory: options.isDirectory }),
				},
			],
			types: ["Files"],
		};
	}

	it("does not show the overlay for a plain-file drag", async () => {
		await renderShell();

		fireEvent.dragEnter(window, { dataTransfer: fileDragTransfer({ isDirectory: false }) });
		expect(screen.queryByTestId("folder-drop-overlay")).not.toBeInTheDocument();
	});

	it("shows the drop overlay while a folder is dragged over the window, and hides it on leave", async () => {
		await renderShell();

		fireEvent.dragEnter(window, { dataTransfer: fileDragTransfer({ isDirectory: true }) });
		expect(screen.getByTestId("folder-drop-overlay")).toBeInTheDocument();

		fireEvent.dragLeave(window, { dataTransfer: fileDragTransfer({ isDirectory: true }) });
		expect(screen.queryByTestId("folder-drop-overlay")).not.toBeInTheDocument();
	});

	// Regression: dragenter/dragleave fire again for every child-element boundary
	// the pointer crosses while hovering inside the window, not just at the
	// window's own edge. A relatedTarget-blind counter is what keeps the overlay
	// from flickering off mid-drag.
	it("does not flicker the overlay when the pointer crosses a child element", async () => {
		await renderShell();

		fireEvent.dragEnter(window, { dataTransfer: fileDragTransfer({ isDirectory: true }) });
		fireEvent.dragEnter(window, { dataTransfer: fileDragTransfer({ isDirectory: true }) });
		expect(screen.getByTestId("folder-drop-overlay")).toBeInTheDocument();

		fireEvent.dragLeave(window, { dataTransfer: fileDragTransfer({ isDirectory: true }) });
		expect(screen.getByTestId("folder-drop-overlay")).toBeInTheDocument();

		fireEvent.dragLeave(window, { dataTransfer: fileDragTransfer({ isDirectory: true }) });
		expect(screen.queryByTestId("folder-drop-overlay")).not.toBeInTheDocument();
	});

	it("resolves the dropped folder's real path and opens the create-project flow", async () => {
		await renderShell();

		fireEvent.dragEnter(window, { dataTransfer: fileDragTransfer({ isDirectory: true }) });
		fireEvent.drop(window, { dataTransfer: fileDragTransfer({ fileName: "my-project", isDirectory: true }) });

		expect(shellMocks.getPathForFile).toHaveBeenCalledTimes(1);
		expect(screen.queryByTestId("folder-drop-overlay")).not.toBeInTheDocument();
		expect(screen.getByTestId("create-project-flow")).toHaveAttribute("data-path", "/dropped/my-project");
		expect(useUiStore.getState().folderDropRequest).toEqual({ nonce: 1, path: "/dropped/my-project" });
	});

	it("ignores a drop that is not a folder", async () => {
		await renderShell();

		fireEvent.drop(window, { dataTransfer: fileDragTransfer({ isDirectory: false }) });

		expect(shellMocks.getPathForFile).not.toHaveBeenCalled();
		expect(useUiStore.getState().folderDropRequest).toBeNull();
		expect(screen.queryByTestId("create-project-flow")).not.toBeInTheDocument();
	});

	it("re-fires on a repeated drop so a second folder can be added", async () => {
		await renderShell();

		fireEvent.drop(window, { dataTransfer: fileDragTransfer({ fileName: "first", isDirectory: true }) });
		fireEvent.drop(window, { dataTransfer: fileDragTransfer({ fileName: "second", isDirectory: true }) });

		expect(useUiStore.getState().folderDropRequest).toEqual({ nonce: 2, path: "/dropped/second" });
	});

	// Regression: XtermTerminal used to stopPropagation() on a plain (non-folder)
	// file drop, so this drop event never reached the window and dragDepthRef
	// was never reset. The next folder drag then bumped from a stale nonzero
	// depth, missed the === 1 branch, and never showed the overlay. Xterm no
	// longer stops propagation for a non-directory drop, so this event now
	// always reaches the window; this test locks in that the window's own
	// reset logic correctly recovers once it does.
	it("does not suppress the next folder drag's overlay after a plain file drop resets the counter", async () => {
		await renderShell();

		fireEvent.dragEnter(window, { dataTransfer: fileDragTransfer({ isDirectory: false }) });
		fireEvent.drop(window, { dataTransfer: fileDragTransfer({ isDirectory: false }) });
		expect(screen.queryByTestId("folder-drop-overlay")).not.toBeInTheDocument();

		fireEvent.dragEnter(window, { dataTransfer: fileDragTransfer({ isDirectory: true }) });
		expect(screen.getByTestId("folder-drop-overlay")).toBeInTheDocument();
	});
});

describe("shell taskbar-icon folder drop subscription", () => {
	it("opens the create-project flow for a folder dropped on the app's icon/shortcut", async () => {
		await renderShell();
		expect(shellMocks.onOpenFolderPath).toHaveBeenCalledTimes(1);

		const listener = shellMocks.state.openFolderPathListener;
		if (!listener) throw new Error("open-folder-path listener was not registered");
		act(() => listener("/dropped-on-icon/my-project"));

		expect(useUiStore.getState().folderDropRequest).toEqual({ nonce: 1, path: "/dropped-on-icon/my-project" });
		expect(screen.getByTestId("create-project-flow")).toHaveAttribute("data-path", "/dropped-on-icon/my-project");
	});
});
