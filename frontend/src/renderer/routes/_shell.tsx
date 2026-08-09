import { createFileRoute, Outlet, useMatchRoute, useNavigate, useParams } from "@tanstack/react-router";
import { isCancelledError, useQueryClient } from "@tanstack/react-query";
import { type CSSProperties, useCallback, useEffect, useRef, useState } from "react";
import { CommandPalette } from "../components/CommandPalette";
import { CenterPanelShell } from "../components/CenterPanelShell";
import { DaemonFailureBanner } from "../components/DaemonFailureBanner";
import { NotificationRuntime } from "../components/NotificationCenter";
import { TrayRuntime } from "../components/TrayRuntime";
import { GlobalNewTaskDialog } from "../components/GlobalNewTaskDialog";
import { SettingsDialog } from "../components/SettingsDialog";
import { KeyboardShortcutsDialog } from "../components/KeyboardShortcutsDialog";
import { KeyboardShortcutsSettingsDialog } from "../components/settings/KeyboardShortcutsSettingsDialog";
import { ShellTopbar } from "../components/ShellTopbar";
import { SessionTopbarHost, SessionTopbarProvider } from "../components/SessionTopbarPortal";
import { OrchestratorReplacementDialog } from "../components/OrchestratorReplacementDialog";
import { Sidebar } from "../components/Sidebar";
import { SidebarProvider } from "../components/ui/sidebar";
import { TitlebarNav } from "../components/TitlebarNav";
import { WindowTitlebar } from "../components/WindowTitlebar";
import { TerminalCacheProvider } from "../components/TerminalPane";
import { agentsQueryKey, agentsQueryOptions, refreshAgents } from "../hooks/useAgentsQuery";
import { useDaemonStatus } from "../hooks/useDaemonStatus";
import { useOpenShellTerminal } from "../hooks/useShellTerminals";
import { useWindowFullScreen } from "../hooks/useWindowFullScreen";
import { useWorkspaceQuery, workspaceQueryKey, workspaceQueryOptions } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorCode, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import { refreshDaemonStatus } from "../lib/daemon-status";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import { addRendererExceptionStep, captureRendererEvent, captureRendererException } from "../lib/telemetry";
import { ShellProvider } from "../lib/shell-context";
import { restartProjectOrchestrator } from "../lib/restart-orchestrator";
import { captureOrchestratorReplacementFailure } from "../lib/orchestrator-replacement-telemetry";
import { applyDocumentTheme, applyDocumentThemeStyle } from "../lib/theme";
import { aoBridge } from "../lib/bridge";
import { handleModifierLinkClick } from "../lib/external-link-policy";
import { cn } from "../lib/utils";
import {
	isLinuxPlatform,
	isMacPlatform,
	isWindowsPlatform,
	usesFramedAppTopbar,
	hidesShellTopbar,
} from "../lib/platform";
import { useUiStore } from "../stores/ui-store";
import { matchesRendererShortcut } from "../stores/keybindings-store";
import { sessionIsActive, toProjectKind, type WorkspaceSummary } from "../types/workspace";
import type { components } from "../../api/schema";
import { useAgentInventoryTelemetry } from "../hooks/useAgentInventoryTelemetry";

export const Route = createFileRoute("/_shell")({
	// Prefetch the workspace list for the whole shell (parent loaders run before
	// children); pairs with the router's defaultPreload: "intent" so a hovered
	// nav target is warm before the click.
	loader: async ({ context }) => {
		await refreshDaemonStatus().catch(() => undefined);
		if (!usesPreviewWorkspaceData && !hasTrustedApiBaseUrl()) return;
		return context.queryClient.ensureQueryData(workspaceQueryOptions);
	},
	component: ShellLayout,
});

function errorMessage(error: unknown) {
	return error instanceof Error ? error.message : "Could not load projects";
}

type CreateProjectConfigInput = {
	workerAgent: string;
	orchestratorAgent: string;
	trackerIntake?: components["schemas"]["TrackerIntakeConfig"];
};

export function createProjectConfig(input: CreateProjectConfigInput): components["schemas"]["ProjectConfig"] {
	return {
		worker: { agent: input.workerAgent as components["schemas"]["RoleOverride"]["agent"] },
		orchestrator: { agent: input.orchestratorAgent as components["schemas"]["RoleOverride"]["agent"] },
		...(input.trackerIntake ? { trackerIntake: input.trackerIntake } : {}),
	};
}

const isMac = isMacPlatform();
const isWindows = isWindowsPlatform();
const isLinux = isLinuxPlatform();
const framedAppTopbar = usesFramedAppTopbar();
const shellTopbarHiddenByPlatform = hidesShellTopbar();

// Persistent app shell: the Sidebar + shared state survive route changes; only
// the <Outlet> content (board / session / settings / …) swaps. Lifted out of
// the old single <App>, with selection now owned by the router (route params)
// instead of Zustand. The daemon-status effect runs here exactly once.
function ShellLayout() {
	// Reports how many agents this install has available, once per launch.
	useAgentInventoryTelemetry();
	const navigate = useNavigate();
	const matchRoute = useMatchRoute();
	const queryClient = useQueryClient();
	const workspaceQuery = useWorkspaceQuery();
	const workspaces = workspaceQuery.data ?? [];
	const daemonStatus = useDaemonStatus(queryClient);
	const [workspaceStartupState, setWorkspaceStartupState] = useState<"loading" | "ready" | "error">("loading");
	const workspaceStartupBaselineRef = useRef(0);
	const agentCatalogPortRef = useRef<number | undefined>(undefined);
	const { themePreference, resolvedTheme, themeStyle, isSidebarOpen, toggleSidebar } = useUiStore();
	const setThemePreference = useUiStore((state) => state.setThemePreference);
	const syncSystemTheme = useUiStore((state) => state.syncSystemTheme);
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	const requestCreateProject = useUiStore((state) => state.requestCreateProject);
	const requestNewShellTerminal = useUiStore((state) => state.requestNewShellTerminal);
	const newShellTerminalNonce = useUiStore((state) => state.newShellTerminalNonce);
	const setActiveShellTerminal = useUiStore((state) => state.setActiveShellTerminal);
	const openShellTerminal = useOpenShellTerminal();
	// Single subscription for sidebar clearance + drag strip (macOS no-ops inside the hook).
	const isFullScreen = useWindowFullScreen();
	// Drag is on immediately for a normal windowed launch. After leaving fullscreen,
	// wait for the pad/height transition so the growing strip cannot steal clicks.
	const [trafficLightDragActive, setTrafficLightDragActive] = useState(isMac);
	const leftFullScreenRef = useRef(false);
	useEffect(() => {
		if (!isMac) return;
		if (isFullScreen) {
			leftFullScreenRef.current = true;
			setTrafficLightDragActive(false);
			return;
		}
		if (!leftFullScreenRef.current) {
			setTrafficLightDragActive(true);
			return;
		}
		const reducedMotion =
			typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
		if (reducedMotion) {
			setTrafficLightDragActive(true);
			return;
		}
		const timer = window.setTimeout(() => setTrafficLightDragActive(true), 200);
		return () => window.clearTimeout(timer);
	}, [isFullScreen]);
	// Seeded to the current value so a mount never opens a terminal unasked.
	const handledShellNonceRef = useRef(newShellTerminalNonce);
	const [isKeyboardShortcutsOpen, setIsKeyboardShortcutsOpen] = useState(false);
	const [isKeyboardShortcutsSettingsOpen, setIsKeyboardShortcutsSettingsOpen] = useState(false);
	const [isSidebarPeekOpen, setIsSidebarPeekOpen] = useState(false);
	const sidebarPeekCloseTimerRef = useRef<number | undefined>(undefined);
	const routeParams = useParams({ strict: false }) as { projectId?: string; sessionId?: string };
	useEffect(() => {
		document.addEventListener("click", handleModifierLinkClick);
		return () => document.removeEventListener("click", handleModifierLinkClick);
	}, []);
	// Project in scope for a new-session shortcut: the route's project, or the
	// workspace owning the open session (so the shortcut works from a worker's
	// detail view, where the URL carries only a sessionId).
	const scopedProjectId = routeParams.projectId
		? routeParams.projectId
		: routeParams.sessionId
			? workspaces.find((workspace) => workspace.sessions.some((session) => session.id === routeParams.sessionId))?.id
			: undefined;
	// First-launch root board only (no projects in scope).
	const isWelcomeBoard =
		Boolean(matchRoute({ to: "/" })) &&
		workspaceStartupState === "ready" &&
		workspaceQuery.isSuccess &&
		workspaces.length === 0;
	const isSettingsRoute =
		Boolean(matchRoute({ to: "/settings", fuzzy: true })) ||
		Boolean(matchRoute({ to: "/projects/$projectId/settings", fuzzy: true }));
	// Welcome/settings always self-frame. Platforms that hide the shell-owned
	// topbar (macOS) use the same full-height inset; session actions mount
	// inside SessionView.
	const selfFramedCenterPanel = isWelcomeBoard || isSettingsRoute;
	const hideShellTopbar = selfFramedCenterPanel || shellTopbarHiddenByPlatform;
	const setProjectRestarting = useUiStore((state) => state.setProjectRestarting);
	const orchestratorReplacementErrors = useUiStore((state) => state.orchestratorReplacementErrors);
	const setOrchestratorReplacementError = useUiStore((state) => state.setOrchestratorReplacementError);
	const setOrchestratorStartupError = useUiStore((state) => state.setOrchestratorStartupError);
	const replacementErrorProjectId = Object.keys(orchestratorReplacementErrors)[0] ?? null;
	const isStartupLoading =
		!usesPreviewWorkspaceData &&
		!daemonStatus.code &&
		(daemonStatus.state !== "ready" || workspaceStartupState === "loading");
	const cancelSidebarPeekClose = useCallback(() => {
		if (sidebarPeekCloseTimerRef.current === undefined) return;
		window.clearTimeout(sidebarPeekCloseTimerRef.current);
		sidebarPeekCloseTimerRef.current = undefined;
	}, []);

	const previewSidebar = useCallback(() => {
		if (isSidebarOpen) return;
		cancelSidebarPeekClose();
		setIsSidebarPeekOpen(true);
	}, [cancelSidebarPeekClose, isSidebarOpen]);

	const scheduleSidebarPeekClose = useCallback(() => {
		if (isSidebarOpen) return;
		cancelSidebarPeekClose();
		sidebarPeekCloseTimerRef.current = window.setTimeout(() => {
			setIsSidebarPeekOpen(false);
			sidebarPeekCloseTimerRef.current = undefined;
		}, 140);
	}, [cancelSidebarPeekClose, isSidebarOpen]);

	const navigateSession = useCallback(
		(direction: -1 | 1) => {
			if (!scopedProjectId) return;
			const sessions = (workspaces.find((workspace) => workspace.id === scopedProjectId)?.sessions ?? []).filter(
				sessionIsActive,
			);
			if (sessions.length === 0) return;
			const currentIndex = sessions.findIndex((session) => session.id === routeParams.sessionId);
			const nextIndex =
				currentIndex === -1
					? direction === 1
						? 0
						: sessions.length - 1
					: (currentIndex + direction + sessions.length) % sessions.length;
			const session = sessions[nextIndex];
			if (!session || session.id === routeParams.sessionId) return;
			void navigate({
				to: "/projects/$projectId/sessions/$sessionId",
				params: { projectId: scopedProjectId, sessionId: session.id },
			});
		},
		[navigate, routeParams.sessionId, scopedProjectId, workspaces],
	);

	const updateWorkspaces = useCallback(
		(updater: (workspaces: WorkspaceSummary[]) => WorkspaceSummary[]) => {
			queryClient.setQueryData<WorkspaceSummary[]>(workspaceQueryKey, (current = []) => updater(current));
		},
		[queryClient],
	);

	const createProject = useCallback(
		async (input: {
			path: string;
			workerAgent: string;
			orchestratorAgent: string;
			trackerIntake?: components["schemas"]["TrackerIntakeConfig"];
			asWorkspace?: boolean;
		}) => {
			void addRendererExceptionStep("Project add requested", {
				source: "project-add",
				operation: "project_add",
				surface: "project_board",
			});
			void captureRendererEvent("ao.renderer.project_add_requested");
			const status = await refreshDaemonStatus();
			if (status.state !== "ready" || !status.port) {
				throw new Error(status.message || "AO daemon is not ready.");
			}
			const { data, error } = await apiClient.POST("/api/v1/projects", {
				body: {
					path: input.path,
					asWorkspace: input.asWorkspace || undefined,
					config: createProjectConfig(input),
				},
			});
			if (error) {
				const failure = new Error(apiErrorMessage(error)) as Error & { code?: string };
				failure.code = apiErrorCode(error);
				void captureRendererException(failure, {
					source: "project-add",
					operation: "project_add",
					surface: "project_board",
				});
				throw failure;
			}
			if (!data?.project) throw new Error("Project creation returned no project");

			const workspace: WorkspaceSummary = {
				id: data.project.id,
				name: data.project.name,
				kind: toProjectKind(data.project.kind),
				path: data.project.path,
				workspaceRepos: data.project.workspaceRepos,
				type: "main",
				orchestratorAgent: input.orchestratorAgent as WorkspaceSummary["orchestratorAgent"],
				sessions: [],
			};
			void captureRendererEvent("ao.renderer.project_add_succeeded", { project_id: workspace.id });
			updateWorkspaces((current) => [workspace, ...current.filter((item) => item.id !== workspace.id)]);
			setOrchestratorStartupError(workspace.id, null);
			try {
				void captureRendererEvent("ao.renderer.orchestrator_spawn_requested", {
					project_id: workspace.id,
					source: "project_add",
				});
				const {
					data: spawnData,
					error: spawnError,
					response: spawnResponse,
				} = await apiClient.POST("/api/v1/sessions", {
					body: {
						projectId: workspace.id,
						kind: "orchestrator",
						harness: input.orchestratorAgent as components["schemas"]["SpawnSessionRequest"]["harness"],
					},
				});
				if (spawnError || !spawnData?.session?.id) {
					const message = spawnError
						? apiErrorMessage(spawnError, `Failed to spawn orchestrator (${spawnResponse.status})`)
						: `Failed to spawn orchestrator (${spawnResponse.status})`;
					throw new Error(message);
				}
				void captureRendererEvent("ao.renderer.orchestrator_spawn_succeeded", {
					project_id: workspace.id,
					source: "project_add",
				});
				const sessionId = spawnData.session.id;
				await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
				void navigate({
					to: "/projects/$projectId/sessions/$sessionId",
					params: { projectId: workspace.id, sessionId },
				});
			} catch (spawnError) {
				void captureRendererEvent("ao.renderer.orchestrator_spawn_failed", {
					project_id: workspace.id,
					source: "project_add",
				});
				void navigate({ to: "/projects/$projectId", params: { projectId: workspace.id } });
				const message = spawnError instanceof Error ? spawnError.message : "Could not start orchestrator";
				const startupMessage = `Project added, but orchestrator did not start: ${message}`;
				setOrchestratorStartupError(workspace.id, startupMessage);
			}
		},
		[navigate, queryClient, setOrchestratorStartupError, updateWorkspaces],
	);

	const initializeProjectRepository = useCallback(async (path: string) => {
		const { error } = await apiClient.POST("/api/v1/projects/initialize", {
			body: { path },
		});
		if (error) {
			const failure = new Error(apiErrorMessage(error)) as Error & { code?: string };
			failure.code = apiErrorCode(error);
			throw failure;
		}
	}, []);

	const removeProject = useCallback(
		async (projectId: string) => {
			void addRendererExceptionStep("Project removal requested", {
				source: "project-remove",
				operation: "project_remove",
				surface: "project_board",
				project_id: projectId,
			});
			const { error } = await apiClient.DELETE("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
			});
			if (error) {
				const failure = new Error(apiErrorMessage(error)) as Error & { code?: string };
				failure.code = apiErrorCode(error);
				void captureRendererException(failure, {
					source: "project-remove",
					operation: "project_remove",
					surface: "project_board",
					project_id: projectId,
				});
				throw failure;
			}
			void captureRendererEvent("ao.renderer.project_removed", { project_id: projectId });
			updateWorkspaces((current) => current.filter((item) => item.id !== projectId));
		},
		[updateWorkspaces],
	);

	const restartOrchestrator = useCallback(
		async (projectId: string, mode?: "chat" | "tui") => {
			await restartProjectOrchestrator({
				projectId,
				queryClient,
				navigate,
				setProjectRestarting,
				setOrchestratorReplacementError,
				mode,
				onError: (error) => {
					captureOrchestratorReplacementFailure(error, projectId);
				},
			});
		},
		[navigate, queryClient, setOrchestratorReplacementError, setProjectRestarting],
	);

	useEffect(() => {
		applyDocumentTheme(resolvedTheme);
	}, [resolvedTheme]);

	useEffect(() => {
		applyDocumentThemeStyle(themeStyle);
	}, [themeStyle]);

	// A daemon port is not enough to render a trustworthy empty state: the
	// route loader may have cached [] before Electron reported the port. Fetch
	// once against each ready daemon before allowing the board to decide
	// between projects and the first-run import flow.
	useEffect(() => {
		let active = true;
		if (usesPreviewWorkspaceData) {
			workspaceStartupBaselineRef.current = 0;
			setWorkspaceStartupState("ready");
			return () => {
				active = false;
			};
		}
		if (daemonStatus.state !== "ready" || !daemonStatus.port) {
			workspaceStartupBaselineRef.current = 0;
			setWorkspaceStartupState("loading");
			return () => {
				active = false;
			};
		}

		workspaceStartupBaselineRef.current =
			queryClient.getQueryState(workspaceQueryKey)?.dataUpdatedAt ?? 0;
		setWorkspaceStartupState("loading");
		void queryClient
			.fetchQuery({ ...workspaceQueryOptions, staleTime: 0 })
			.then(() => {
				if (active) setWorkspaceStartupState("ready");
			})
			.catch((error) => {
				if (active && !isCancelledError(error)) setWorkspaceStartupState("error");
			});

		return () => {
			active = false;
		};
	}, [daemonStatus.port, daemonStatus.state, queryClient]);

	// The first confirmed fetch may fail transiently even though the daemon is
	// ready. React Query keeps polling and the event transport may invalidate
	// the workspace query later, so let a newer successful result recover the
	// shell without requiring a daemon restart or port change.
	useEffect(() => {
		if (
			usesPreviewWorkspaceData ||
			daemonStatus.state !== "ready" ||
			workspaceStartupState === "ready" ||
			!workspaceQuery.isSuccess ||
			workspaceQuery.dataUpdatedAt <= workspaceStartupBaselineRef.current
		) {
			return;
		}
		setWorkspaceStartupState("ready");
	}, [
		daemonStatus.state,
		workspaceQuery.dataUpdatedAt,
		workspaceQuery.isSuccess,
		workspaceStartupState,
	]);

	// Keep Electron's nativeTheme in step with the shell so the embedded preview
	// WebContentsView (which follows prefers-color-scheme) flips at the same time.
	// Send the preference, not the resolved theme, so "system" keeps both surfaces
	// following the OS instead of freezing matchMedia to a forced value.
	useEffect(() => {
		void aoBridge.theme?.set(themePreference);
	}, [themePreference]);

	// The tray can change the theme while the window is open; mirror that back
	// into the renderer's own store so the shell updates without a restart.
	useEffect(() => aoBridge.theme?.onChanged?.((preference) => setThemePreference(preference)), [setThemePreference]);

	useEffect(() => {
		if (!isSidebarOpen) return;
		cancelSidebarPeekClose();
		setIsSidebarPeekOpen(false);
	}, [cancelSidebarPeekClose, isSidebarOpen]);

	useEffect(() => cancelSidebarPeekClose, [cancelSidebarPeekClose]);

	useEffect(() => {
		if (!isSidebarPeekOpen || isSidebarOpen) return;

		const handlePointerMove = (event: PointerEvent) => {
			const target = event.target instanceof Element ? event.target : null;
			const isInSidebarPortal = Boolean(target?.closest('[role="dialog"], [role="listbox"], [role="menu"]'));
			// TitlebarNav / WindowTitlebar sit above the peek in z-order; keep the
			// preview open while the pointer is on those controls so hover→click
			// to pin still works.
			const isInTitlebarChrome = Boolean(
				target?.closest("[data-slot='titlebar-nav'], .window-titlebar"),
			);
			const sidebar = document.querySelector<HTMLElement>('[data-slot="sidebar-container"]');
			const bounds = sidebar?.getBoundingClientRect();
			const isInSidebar = Boolean(
				bounds &&
				event.clientX >= bounds.left &&
				event.clientX <= bounds.right &&
				event.clientY >= bounds.top &&
				event.clientY <= bounds.bottom,
			);

			if (isInSidebar || isInSidebarPortal || isInTitlebarChrome) {
				cancelSidebarPeekClose();
				return;
			}
			scheduleSidebarPeekClose();
		};

		window.addEventListener("pointermove", handlePointerMove);
		return () => window.removeEventListener("pointermove", handlePointerMove);
	}, [cancelSidebarPeekClose, isSidebarOpen, isSidebarPeekOpen, scheduleSidebarPeekClose]);

	useEffect(() => {
		if (daemonStatus.state !== "ready" || !daemonStatus.port) return;
		if (agentCatalogPortRef.current === daemonStatus.port) return;

		agentCatalogPortRef.current = daemonStatus.port;
		void queryClient.invalidateQueries({ queryKey: agentsQueryKey });
		void queryClient.fetchQuery({ ...agentsQueryOptions, queryFn: refreshAgents });
	}, [daemonStatus.port, daemonStatus.state, queryClient]);

	// Follow OS appearance while the user keeps Theme on System — updates
	// resolvedTheme (and thus React consumers) without writing light/dark to storage.
	useEffect(() => {
		if (themePreference !== "system") return;

		const mediaQuery = window.matchMedia("(prefers-color-scheme: light)");
		const handleChange = () => syncSystemTheme();
		mediaQuery.addEventListener("change", handleChange);
		return () => mediaQuery.removeEventListener("change", handleChange);
	}, [themePreference, syncSystemTheme]);

	useEffect(() => {
		const handleKeyDown = (event: KeyboardEvent) => {
			if (matchesRendererShortcut("toggle-sidebar", event)) {
				event.preventDefault();
				toggleSidebar();
				return;
			}
			if (matchesRendererShortcut("open-project", event)) {
				const workspace = workspaces[Number(event.key) - 1];
				if (workspace) {
					event.preventDefault();
					void navigate({ to: "/projects/$projectId", params: { projectId: workspace.id } });
				}
			}
		};
		window.addEventListener("keydown", handleKeyDown);
		return () => window.removeEventListener("keydown", handleKeyDown);
	}, [navigate, toggleSidebar, workspaces]);

	// New session (⌘N / Ctrl+Shift+N) is detected in the main process and
	// delivered here, so it fires even when focus is inside xterm or a native
	// Browser-preview view. The shell owns the routing: open the New Task flow
	// for the in-scope project, else fall back to create-project.
	useEffect(
		() =>
			aoBridge.app.onNewSessionShortcut(() => {
				if (scopedProjectId) {
					requestNewTask(scopedProjectId);
				} else {
					requestCreateProject();
				}
			}),
		[scopedProjectId, requestNewTask, requestCreateProject],
	);

	useEffect(() => aoBridge.app.onKeyboardShortcutsHelp(() => setIsKeyboardShortcutsOpen(true)), []);

	// New standalone terminal (⌘T / Ctrl+T), also detected in the main process so it
	// fires from inside a terminal pane. It raises the same store signal as the
	// tab-strip + button so the two cannot drift apart.
	useEffect(() => aoBridge.app.onNewShellTerminalShortcut(() => requestNewShellTerminal()), [requestNewShellTerminal]);

	// The shell layout is the single consumer of that signal, because it is the
	// only component mounted on EVERY route. Owning it here is what lets the
	// button and the keyboard shortcut work from the board, a project page, or a session alike
	// — when the session view owned it, both silently did nothing outside a
	// session, since nothing was listening.
	//
	// Where the new shell becomes visible depends on where the user is: inside a
	// session it joins that pane's tab strip, anywhere else it gets the
	// standalone /terminals view. Either way the store records it as active, and
	// whichever view is on screen selects it.
	useEffect(() => {
		if (handledShellNonceRef.current === newShellTerminalNonce) return;
		handledShellNonceRef.current = newShellTerminalNonce;
		openShellTerminal.mutate(
			{ projectId: scopedProjectId, sessionId: routeParams.sessionId },
			{
				onSuccess: (shell) => {
					setActiveShellTerminal(shell.handleId);
					if (!routeParams.sessionId) {
						void navigate({ to: "/terminals" });
					}
				},
			},
		);
	}, [
		newShellTerminalNonce,
		openShellTerminal,
		scopedProjectId,
		routeParams.sessionId,
		navigate,
		setActiveShellTerminal,
	]);

	useEffect(
		() => aoBridge.app.onOpenSettingsShortcut(() => useUiStore.getState().openGlobalSettings()),
		[],
	);

	useEffect(() => {
		const disposePrevious = aoBridge.app.onPreviousSessionShortcut(() => navigateSession(-1));
		const disposeNext = aoBridge.app.onNextSessionShortcut(() => navigateSession(1));
		return () => {
			disposePrevious();
			disposeNext();
		};
	}, [navigateSession]);

	useEffect(
		() =>
			aoBridge.app.onFocusTerminalShortcut(() => {
				document
					.querySelector<HTMLElement>(
						"[data-terminal-activation-phase='visible'] .xterm-helper-textarea, " +
							"[data-testid='session-terminal-slot'] .xterm-helper-textarea",
					)
					?.focus();
			}),
		[],
	);

	return (
		<ShellProvider value={{ daemonStatus, workspaceStartupState, createProject, initializeProjectRepository }}>
			<SessionTopbarProvider>
				<NotificationRuntime />
				<TrayRuntime />
				<GlobalNewTaskDialog />
				<SettingsDialog />
				<KeyboardShortcutsDialog
					open={isKeyboardShortcutsOpen}
					onOpenChange={setIsKeyboardShortcutsOpen}
					onCustomize={() => {
						setIsKeyboardShortcutsOpen(false);
						setIsKeyboardShortcutsSettingsOpen(true);
					}}
				/>
				<KeyboardShortcutsSettingsDialog
					open={isKeyboardShortcutsSettingsOpen}
					onOpenChange={setIsKeyboardShortcutsSettingsOpen}
				/>
				<TerminalCacheProvider
					daemonReady={daemonStatus.state === "ready"}
					theme={resolvedTheme}
				>

			{/* Shell chrome: Win/Linux hang the sidebar under a topbar. macOS uses a
          titlebar strip above the off-canvas sidebar. Session and board actions
          render inside the center panel when the shell topbar is hidden. */}
			<div
				className={cn(
					"flex h-screen min-h-0 flex-col bg-sidebar text-foreground",
					isWindows && "platform-windows",
					isLinux && "platform-linux",
					isFullScreen && "native-fullscreen",
				)}
			>
				{/* Windows-only custom title bar (sidebar toggle + File/Edit/View/…
            menu); paints the chrome the frameless window drops. Renders null on
            macOS/Linux. */}
				<WindowTitlebar onSidebarPreviewEnter={previewSidebar} />
				{/* App routes render their topbar inside the framed panel, matching the board chrome across platforms while leaving OS titlebars native. */}
				{!framedAppTopbar && !hideShellTopbar && !routeParams.sessionId ? <ShellTopbar /> : null}
				{/* Controlled by the ui-store so TitlebarNav / Topbar toggles (which
            call the store directly) stay in sync. --sidebar-width chains to
            the drag-resizable --ao-sidebar-w set on :root by useResizable. */}
				<SidebarProvider
					className="min-h-0 flex-1 flex-col overflow-x-hidden"
					keyboardShortcut={false}
					onOpenChange={(open) => {
						cancelSidebarPeekClose();
						setIsSidebarPeekOpen(false);
						if (open !== isSidebarOpen) toggleSidebar();
					}}
					open={!isStartupLoading && (isSidebarOpen || isSidebarPeekOpen)}
					style={
						{
							"--sidebar-width": "var(--ao-sidebar-w, var(--size-sidebar-default))",
							"--sidebar-width-icon": "var(--size-sidebar-icon)",
						} as CSSProperties
					}
				>
				<div className="flex min-h-0 w-full flex-1 overflow-x-hidden" data-testid="shell-content-row">
				{/* macOS + Linux reserve a titlebar band for the fixed TitlebarNav
              cluster above a full-height sidebar; Windows hangs the sidebar
              below its custom titlebar. */}
				<Sidebar
					hideEdgeBorder={isWelcomeBoard}
					isOverlay={isSidebarPeekOpen && !isSidebarOpen}
					onPreviewLeave={scheduleSidebarPeekClose}
					underTopbar={isMac || isWindows || isLinux}
					topbarOffset={isWindows ? "titlebar" : hideShellTopbar ? "trafficLights" : "toolbar"}
						onCreateProject={createProject}
						onInitializeProject={initializeProjectRepository}
						onRemoveProject={removeProject}
						workspaceError={workspaceQuery.isError ? errorMessage(workspaceQuery.error) : undefined}
						workspaces={workspaces}
					/>
					<main className={cn("flex min-w-0 flex-1 flex-col overflow-x-hidden", !isSidebarOpen && "sidebar-hidden")}>
						<div className="min-h-0 flex-1 overflow-x-hidden">
							{/* Board/session routes render inside the same inset box the welcome board and settings paint for themselves, so every screen sits within the app's outer boundary. */}
							{hideShellTopbar ? (
								selfFramedCenterPanel ? (
									<Outlet />
								) : (
							// Platform hides shell topbar: full-height panel; session mounts actions in-panel.
							<CenterPanelShell className={routeParams.sessionId ? "center-panel-shell--session" : undefined}>
								{routeParams.sessionId ? (
									<SessionTopbarHost
										className="relative z-chrome flex h-inspector-tabs w-full shrink-0 overflow-hidden"
										data-testid="session-topbar-host"
									/>
								) : null}
								<div className="flex min-h-0 flex-1 flex-col">
									<Outlet />
								</div>
							</CenterPanelShell>
						)
					) : framedAppTopbar ? (
						<CenterPanelShell className={routeParams.sessionId ? "center-panel-shell--session" : undefined}>
							{routeParams.sessionId ? (
								<SessionTopbarHost
									className="relative z-chrome flex h-inspector-tabs w-full shrink-0 overflow-hidden"
									data-testid="session-topbar-host"
								/>
							) : (
								<ShellTopbar />
							)}
							<div className="flex min-h-0 flex-1 flex-col">
								<Outlet />
							</div>
						</CenterPanelShell>
					) : (
						<CenterPanelShell className={routeParams.sessionId ? "center-panel-shell--session" : undefined}>
							{routeParams.sessionId ? (
								<SessionTopbarHost
									className="relative z-chrome flex h-inspector-tabs w-full shrink-0 overflow-hidden"
									data-testid="session-topbar-host"
								/>
							) : null}
							<div className="flex min-h-0 flex-1 flex-col">
								<Outlet />
							</div>
						</CenterPanelShell>
					)}
						</div>
					</main>
					</div>
					<DaemonFailureBanner status={daemonStatus} />
					{/* When ShellTopbar is hidden, keep a macOS window-drag strip over
              the traffic-light band only. The fixed TitlebarNav renders after
              this strip so its no-drag buttons remain clickable. */}
					{hideShellTopbar && isMac ? (
						<div
							aria-hidden="true"
							className={cn(
								"fixed top-0 left-0 z-chrome w-(--ao-sidebar-w,var(--size-sidebar-default)) transition-[height] duration-200 ease-out motion-reduce:transition-none",
								isFullScreen ? "pointer-events-none h-0" : "h-traffic-light-clearance",
							)}
							style={trafficLightDragActive ? ({ WebkitAppRegion: "drag" } as CSSProperties) : undefined}
						/>
					) : null}
					{/* Fixed macOS titlebar cluster beside the traffic lights — rendered
              once here so the toggle/history buttons never move when the
              sidebar collapses or expands. History arrows stay visible but
              locked on the empty start page. MUST come after the drag strip
              (ShellTopbar or the welcome substitute) in the DOM: Electron
              builds the window-drag region in document order (drag rects add,
              no-drag rects subtract), so the cluster's no-drag holes only
              survive if they're processed after the drag strips they overlap.
              Rendered first, real clicks get swallowed by window-drag even
              though DOM hit-testing looks correct. */}
					<TitlebarNav
						hasSessionTopbar={Boolean(routeParams.sessionId)}
						historyLocked={isWelcomeBoard}
						isFullScreen={isFullScreen}
						onSidebarPreviewEnter={previewSidebar}
					/>
				</SidebarProvider>
				<OrchestratorReplacementDialog
					error={replacementErrorProjectId ? orchestratorReplacementErrors[replacementErrorProjectId] : undefined}
					onOpenChange={(open) => {
						if (!open && replacementErrorProjectId) setOrchestratorReplacementError(replacementErrorProjectId, null);
					}}
					onRetry={(projectId) => void restartOrchestrator(projectId)}
					onRetryAsTui={(projectId) => void restartOrchestrator(projectId, "tui")}
					projectId={replacementErrorProjectId}
					workspaces={workspaces}
				/>
					<CommandPalette />
				</div>
				</TerminalCacheProvider>
			</SessionTopbarProvider>
		</ShellProvider>
	);
}
