import { createFileRoute, Outlet, useMatchRoute, useNavigate, useParams } from "@tanstack/react-router";
import { isCancelledError, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { memo, type CSSProperties, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { FolderPlus } from "lucide-react";
import { useTranslation } from "react-i18next";
import { CommandPalette } from "../components/CommandPalette";
import { CenterPanelShell } from "../components/CenterPanelShell";
import { DaemonFailureBanner } from "../components/DaemonFailureBanner";
import { DaemonStartupLoader } from "../components/DaemonStartupLoader";
import { NotificationRuntime } from "../components/NotificationCenter";
import { TrayRuntime } from "../components/TrayRuntime";
import { GlobalNewTaskDialog } from "../components/GlobalNewTaskDialog";
import { GlobalToast } from "../components/GlobalToast";
import { SettingsDialog } from "../components/SettingsDialog";
import { KeyboardShortcutsDialog } from "../components/KeyboardShortcutsDialog";
import { KeyboardShortcutsSettingsDialog } from "../components/settings/KeyboardShortcutsSettingsDialog";
import { ShellTopbar } from "../components/ShellTopbar";
import { SessionTopbarProvider } from "../components/SessionTopbarPortal";
import { OrchestratorReplacementDialog } from "../components/OrchestratorReplacementDialog";
import { RestartToUpdateDialog } from "../components/RestartToUpdateDialog";
import { Sidebar } from "../components/Sidebar";
import { SidebarProvider } from "../components/ui/sidebar";
import { TitlebarNav } from "../components/TitlebarNav";
import { WindowTitlebar } from "../components/WindowTitlebar";
import { TerminalCacheProvider } from "../components/TerminalPane";
import { agentModelsQueryOptions } from "../hooks/useAgentModelsQuery";
import { useDaemonStatus } from "../hooks/useDaemonStatus";
import { useOpenShellTerminal } from "../hooks/useShellTerminals";
import { useWindowFullScreen } from "../hooks/useWindowFullScreen";
import { useWorkspaceQuery, workspaceQueryKey, workspaceQueryOptions } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorCode, apiErrorDetails, apiErrorMessage, apiErrorRequestId, hasTrustedApiBaseUrl } from "../lib/api-client";
import { refreshDaemonStatus } from "../lib/daemon-status";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import { addRendererExceptionStep, captureRendererEvent, captureRendererException } from "../lib/telemetry";
import { ShellProvider } from "../lib/shell-context";
import { restartProjectOrchestrator } from "../lib/restart-orchestrator";
import { captureOrchestratorReplacementFailure } from "../lib/orchestrator-replacement-telemetry";
import { applyDocumentTheme, applyDocumentThemeStyle } from "../lib/theme";
import { aoBridge } from "../lib/bridge";
import { handleModifierLinkClick } from "../lib/external-link-policy";
import { recordProjectOpened } from "../lib/project-history";
import { cn } from "../lib/utils";
import {
	isLinuxPlatform,
	isMacPlatform,
	isWindowsPlatform,
	usesFramedAppTopbar,
	hidesShellTopbar,
} from "../lib/platform";
import { sidebarIsVisible, sidebarOccupiesLayout, useUiStore } from "../stores/ui-store";
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
		return context.queryClient.fetchQuery({ ...workspaceQueryOptions, staleTime: 0 });
	},
	component: ShellLayout,
});

function errorMessage(error: unknown) {
	return error instanceof Error ? error.message : "Could not load projects";
}

function normalizeProjectPath(path: string): string {
	if (!path) return path;
	const trimmed = path.trim().replace(/[\\/]+$/, "");
	return trimmed === "" ? path : trimmed;
}

function findRegisteredWorkspaceByPath(workspaces: WorkspaceSummary[], path: string): WorkspaceSummary | undefined {
	const normalizedPath = normalizeProjectPath(path);
	return workspaces.find((workspace) => normalizeProjectPath(workspace.path) === normalizedPath);
}

type CreateProjectConfigInput = {
	workerAgent: string;
	orchestratorAgent: string;
	trackerIntake?: components["schemas"]["TrackerIntakeConfig"];
	defaultBranch?: string;
};

export function createProjectConfig(input: CreateProjectConfigInput): components["schemas"]["ProjectConfig"] {
	return {
		...(input.defaultBranch ? { defaultBranch: input.defaultBranch } : {}),
		worker: { agent: input.workerAgent as components["schemas"]["RoleOverride"]["agent"] },
		orchestrator: { agent: input.orchestratorAgent as components["schemas"]["RoleOverride"]["agent"] },
		...(input.trackerIntake ? { trackerIntake: input.trackerIntake } : {}),
	};
}

async function waitForWorkspaceSession(
	queryClient: QueryClient,
	projectId: string,
	sessionId: string,
): Promise<boolean> {
	for (let attempt = 0; attempt < 3; attempt += 1) {
		const workspaces = await queryClient.fetchQuery({ ...workspaceQueryOptions, staleTime: 0 });
		const found = workspaces
			.find((workspace) => workspace.id === projectId)
			?.sessions.some((session) => session.id === sessionId);
		if (found) return true;
		await new Promise((resolve) => window.setTimeout(resolve, 250));
	}
	return false;
}

const isMac = isMacPlatform();
const isWindows = isWindowsPlatform();
const isLinux = isLinuxPlatform();
const framedAppTopbar = usesFramedAppTopbar();
const shellTopbarHiddenByPlatform = hidesShellTopbar();

/**
 * The shell must observe the complete workspace list for the sidebar, but a
 * streamed update there should not reconcile the active route surface. Keep
 * the center frame behind a primitive-only memo boundary; SessionView and the
 * board own their more granular workspace subscriptions.
 */
const ShellCenter = memo(function ShellCenter({
	hideShellTopbar,
	isSessionRoute,
	selfFramedCenterPanel,
}: {
	hideShellTopbar: boolean;
	isSessionRoute: boolean;
	selfFramedCenterPanel: boolean;
}) {
	const panelClassName = isSessionRoute ? "center-panel-shell--session" : undefined;
	if (hideShellTopbar) {
		return selfFramedCenterPanel ? (
			<Outlet />
		) : (
			<CenterPanelShell className={panelClassName}>
				<div className="flex min-h-0 flex-1 flex-col">
					<Outlet />
				</div>
			</CenterPanelShell>
		);
	}
	if (framedAppTopbar) {
		return (
			<CenterPanelShell className={panelClassName}>
				{isSessionRoute ? null : <ShellTopbar />}
				<div className="flex min-h-0 flex-1 flex-col">
					<Outlet />
				</div>
			</CenterPanelShell>
		);
	}
	return (
		<CenterPanelShell className={panelClassName}>
			<div className="flex min-h-0 flex-1 flex-col">
				<Outlet />
			</div>
		</CenterPanelShell>
	);
});

// Persistent app shell: the Sidebar + shared state survive route changes; only
// the <Outlet> content (board / session / settings / …) swaps. Lifted out of
// the old single <App>, with selection now owned by the router (route params)
// instead of Zustand. The daemon-status effect runs here exactly once.
function ShellLayout() {
	// Reports how many agents this install has available, once per launch.
	useAgentInventoryTelemetry();
	const { t } = useTranslation();
	const navigate = useNavigate();
	const matchRoute = useMatchRoute();
	const queryClient = useQueryClient();
	const workspaceQuery = useWorkspaceQuery();
	const workspaces = workspaceQuery.data ?? [];
	// Global shortcut listeners need the latest workspace list, but recreating
	// those subscriptions for every streamed activity update is avoidable.
	const workspacesRef = useRef(workspaces);
	workspacesRef.current = workspaces;
	const daemonStatus = useDaemonStatus(queryClient);
	const [workspaceStartupState, setWorkspaceStartupState] = useState<"loading" | "ready" | "error">("loading");
	const workspaceStartupBaselineRef = useRef(0);
	const themePreference = useUiStore((state) => state.themePreference);
	const resolvedTheme = useUiStore((state) => state.resolvedTheme);
	const themeStyle = useUiStore((state) => state.themeStyle);
	const isSidebarOpen = useUiStore(sidebarIsVisible);
	const toggleSidebar = useUiStore((state) => state.toggleSidebar);
	const sidebarHasLayout = useUiStore(sidebarOccupiesLayout);
	const syncSystemTheme = useUiStore((state) => state.syncSystemTheme);
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	const requestCreateProject = useUiStore((state) => state.requestCreateProject);
	const requestCreateProjectFromPath = useUiStore((state) => state.requestCreateProjectFromPath);
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
	const routeParams = useParams({ strict: false }) as { projectId?: string; sessionId?: string };
	useEffect(() => {
		document.addEventListener("click", handleModifierLinkClick);
		return () => document.removeEventListener("click", handleModifierLinkClick);
	}, []);
	// Drop a folder anywhere in the app window to add it as a project, mirroring
	// VS Code's "drop a folder to open it". A depth counter (not a relatedTarget
	// check) tracks dragenter/dragleave so the overlay doesn't flicker as the
	// pointer crosses child-element boundaries. XtermTerminal's own drop handler
	// only swallows (preventDefault/stopPropagation) a drop that is NOT a
	// directory — a dropped folder is left untouched so it bubbles here even
	// when it lands on an active terminal pane.
	const [isDragActive, setIsDragActive] = useState(false);
	const dragDepthRef = useRef(0);
	useEffect(() => {
		const isFileDrag = (event: DragEvent) => Array.from(event.dataTransfer?.types ?? []).includes("Files");
		const firstEntryIsDirectory = (event: DragEvent) => {
			const item = event.dataTransfer?.items?.[0];
			return item?.webkitGetAsEntry?.()?.isDirectory ?? false;
		};
		const handleDragEnter = (event: DragEvent) => {
			if (!isFileDrag(event)) return;
			event.preventDefault();
			dragDepthRef.current += 1;
			if (dragDepthRef.current === 1 && firstEntryIsDirectory(event)) setIsDragActive(true);
		};
		const handleDragOver = (event: DragEvent) => {
			if (!isFileDrag(event)) return;
			event.preventDefault();
			if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
		};
		const handleDragLeave = (event: DragEvent) => {
			if (!isFileDrag(event)) return;
			dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
			if (dragDepthRef.current === 0) setIsDragActive(false);
		};
		const handleDrop = (event: DragEvent) => {
			if (!isFileDrag(event)) return;
			event.preventDefault();
			dragDepthRef.current = 0;
			setIsDragActive(false);
			const item = event.dataTransfer?.items?.[0];
			if (!item?.webkitGetAsEntry?.()?.isDirectory) return;
			const file = item.getAsFile();
			if (!file) return;
			// Must be synchronous, on the File taken directly from dataTransfer, in
			// the same tick as the native drop event — see preload.ts's comment.
			const path = aoBridge.app.getPathForFile(file);
			if (path) requestCreateProjectFromPath(path);
		};
		window.addEventListener("dragenter", handleDragEnter);
		window.addEventListener("dragover", handleDragOver);
		window.addEventListener("dragleave", handleDragLeave);
		window.addEventListener("drop", handleDrop);
		return () => {
			window.removeEventListener("dragenter", handleDragEnter);
			window.removeEventListener("dragover", handleDragOver);
			window.removeEventListener("dragleave", handleDragLeave);
			window.removeEventListener("drop", handleDrop);
		};
	}, [requestCreateProjectFromPath]);
	// Project in scope for a new-session shortcut: the route's project, or the
	// workspace owning the open session (so the shortcut works from a worker's
	// detail view, where the URL carries only a sessionId).
	const scopedProjectId = routeParams.projectId
		? routeParams.projectId
		: routeParams.sessionId
			? workspaces.find((workspace) => workspace.sessions.some((session) => session.id === routeParams.sessionId))?.id
			: undefined;
	// Warms the New Task composer's model-catalog cache while the user is just
	// looking at the project, so the picker never shows a loading flash the
	// first time they actually open the dialog.
	useEffect(() => {
		if (!scopedProjectId) return;
		const projectQueryKey = ["project", scopedProjectId];
		void queryClient
			.prefetchQuery({
				queryKey: projectQueryKey,
				queryFn: async () => {
					const { data, error: apiError } = await apiClient.GET("/api/v1/projects/{id}", {
						params: { path: { id: scopedProjectId } },
					});
					if (apiError) throw new Error(apiErrorMessage(apiError));
					if (data?.status !== "ok") throw new Error("Project config unavailable");
					return data.project as components["schemas"]["Project"];
				},
			})
			.then(() => {
				const project = queryClient.getQueryData<components["schemas"]["Project"]>(projectQueryKey);
				const defaultWorkerAgent = project?.config?.worker?.agent || project?.agent || "";
				if (defaultWorkerAgent) {
					void queryClient.prefetchQuery(agentModelsQueryOptions(defaultWorkerAgent, scopedProjectId));
				}
			});
	}, [queryClient, scopedProjectId]);
	// The root route is the intentionally minimal home surface, regardless of
	// whether projects have already been registered.
	const isHomeRoute = Boolean(matchRoute({ to: "/" }));
	useEffect(() => {
		if (routeParams.projectId) recordProjectOpened(routeParams.projectId);
	}, [routeParams.projectId]);
	const isTerminalsRoute = Boolean(matchRoute({ to: "/terminals" }));
	const isSettingsRoute =
		Boolean(matchRoute({ to: "/settings", fuzzy: true })) ||
		Boolean(matchRoute({ to: "/projects/$projectId/settings", fuzzy: true }));
	// Welcome/settings always self-frame. Platforms that hide the shell-owned
	// topbar (macOS) use the same full-height inset; session actions mount
	// inside SessionView.
	// Home keeps the shell's topbar hidden, but still renders inside the shared
	// rounded center panel. Settings owns its complete frame and remains
	// self-framed.
	const selfFramedCenterPanel = isSettingsRoute;
	const hideShellTopbar = isHomeRoute || selfFramedCenterPanel || shellTopbarHiddenByPlatform;
	const setProjectRestarting = useUiStore((state) => state.setProjectRestarting);
	const orchestratorReplacementErrors = useUiStore((state) => state.orchestratorReplacementErrors);
	const setOrchestratorReplacementError = useUiStore((state) => state.setOrchestratorReplacementError);
	const setOrchestratorStartupError = useUiStore((state) => state.setOrchestratorStartupError);
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const replacementErrorProjectId = Object.keys(orchestratorReplacementErrors)[0] ?? null;
	const isStartupLoading =
		!usesPreviewWorkspaceData &&
		!daemonStatus.code &&
		(daemonStatus.state !== "ready" || workspaceStartupState === "loading" || (!workspaceQuery.isSuccess && !workspaceQuery.isError));
	const navigateSession = useCallback(
		(direction: -1 | 1) => {
			if (!scopedProjectId) return;
			const sessions = (workspacesRef.current.find((workspace) => workspace.id === scopedProjectId)?.sessions ?? []).filter(
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
		[navigate, routeParams.sessionId, scopedProjectId],
	);

	const updateWorkspaces = useCallback(
		(updater: (workspaces: WorkspaceSummary[]) => WorkspaceSummary[]) => {
			queryClient.setQueryData<WorkspaceSummary[]>(workspaceQueryKey, (current = []) => updater(current));
		},
		[queryClient],
	);

	const completeProjectCreation = useCallback(
		async (
			project: components["schemas"]["Project"],
			input: CreateProjectConfigInput,
			source: "project_add" | "project_clone",
		) => {
			const workspace: WorkspaceSummary = {
				id: project.id,
				name: project.name,
				kind: toProjectKind(project.kind),
				path: project.path,
				workspaceRepos: project.workspaceRepos,
				type: "main",
				orchestratorAgent: input.orchestratorAgent as WorkspaceSummary["orchestratorAgent"],
				sessions: [],
			};
			void captureRendererEvent(`ao.renderer.${source}_succeeded`, { project_id: workspace.id });
			updateWorkspaces((current) => [workspace, ...current.filter((item) => item.id !== workspace.id)]);
			setOrchestratorStartupError(workspace.id, null);
			try {
				void captureRendererEvent("ao.renderer.orchestrator_spawn_requested", {
					project_id: workspace.id,
					source,
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
						source,
					});
					const sessionId = spawnData.session.id;
					const sessionVisible = await waitForWorkspaceSession(queryClient, workspace.id, sessionId);
					if (!sessionVisible) {
						await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
					}
					void navigate({
						to: "/projects/$projectId/sessions/$sessionId",
						params: { projectId: workspace.id, sessionId },
				});
			} catch (spawnError) {
				void captureRendererEvent("ao.renderer.orchestrator_spawn_failed", {
					project_id: workspace.id,
					source,
				});
				void navigate({ to: "/projects/$projectId", params: { projectId: workspace.id } });
				const message = spawnError instanceof Error ? spawnError.message : "Could not start orchestrator";
				const startupMessage = `Project added, but orchestrator did not start: ${message}`;
				setOrchestratorStartupError(workspace.id, startupMessage);
			}
		},
		[navigate, queryClient, setOrchestratorStartupError, updateWorkspaces],
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
				const failure = new Error(apiErrorMessage(error)) as Error & {
					code?: string;
					details?: Record<string, unknown>;
					requestId?: string;
				};
				failure.code = apiErrorCode(error);
				failure.details = apiErrorDetails(error);
				failure.requestId = apiErrorRequestId(error);
				if (failure.code === "PATH_ALREADY_REGISTERED") {
					const existingProjectId = failure.details?.existingProjectId;
					const findRegisteredWorkspace = (items: WorkspaceSummary[]) => {
						const local = items.filter((workspace) => workspace.kind !== "cloud");
						// The daemon resolves symlinks/canonical paths. Prefer its identity
						// over renderer path spelling, but only open a known local project.
						return typeof existingProjectId === "string" && existingProjectId !== ""
							? local.find((workspace) => workspace.id === existingProjectId)
							: findRegisteredWorkspaceByPath(local, input.path);
					};
					let registeredWorkspace = findRegisteredWorkspace(
						queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey) ?? workspacesRef.current,
					);
					if (!registeredWorkspace) {
						try {
							registeredWorkspace = findRegisteredWorkspace(await queryClient.fetchQuery({
								...workspaceQueryOptions, staleTime: 0, retry: false,
							}));
						} catch {
							// Keep the original conflict and selection if refresh is unavailable.
						}
					}
					if (registeredWorkspace) {
						showGlobalToast("Project already added", "Opened the registered project for this folder.");
						void navigate({ to: "/projects/$projectId", params: { projectId: registeredWorkspace.id } });
						return;
					}
				}
				void captureRendererException(failure, {
					source: "project-add",
					operation: "project_add",
					surface: "project_board",
					});
					throw failure;
			}
			if (!data?.project) throw new Error("Project creation returned no project");
			await completeProjectCreation(data.project, input, "project_add");
		},
		[completeProjectCreation, navigate, queryClient, showGlobalToast],
	);

	const cloneProject = useCallback(
		async (input: {
			remoteUrl: string;
			destinationParent: string;
			workerAgent: string;
			orchestratorAgent: string;
			trackerIntake?: components["schemas"]["TrackerIntakeConfig"];
			signal?: AbortSignal;
		}) => {
			void addRendererExceptionStep("Project clone requested", {
				source: "project-clone",
				operation: "project_clone",
				surface: "project_board",
			});
			void captureRendererEvent("ao.renderer.project_clone_requested");
			const status = await refreshDaemonStatus();
			if (status.state !== "ready" || !status.port) {
				throw new Error(status.message || "AO daemon is not ready.");
			}
			const { data, error } = await apiClient.POST("/api/v1/projects/clone", {
				signal: input.signal,
				body: {
					remoteUrl: input.remoteUrl,
					destinationParent: input.destinationParent,
					config: createProjectConfig(input),
				},
			});
			if (error) {
				const failure = new Error(apiErrorMessage(error)) as Error & {
					code?: string;
					details?: Record<string, unknown>;
					requestId?: string;
				};
				failure.code = apiErrorCode(error);
				failure.details = apiErrorDetails(error);
				failure.requestId = apiErrorRequestId(error);
				void captureRendererException(failure, {
					source: "project-clone",
					operation: "project_clone",
					surface: "project_board",
				});
				throw failure;
			}
			if (!data?.project) throw new Error("Project clone returned no project");
			await completeProjectCreation(data.project, input, "project_clone");
		},
		[completeProjectCreation],
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

	const validateImport = useCallback(
		async (input: { path: string; importKind: "project" | "workspace" }) => {
			const { data, error } = await apiClient.POST("/api/v1/imports/validate", { body: input });
			if (error) throw new Error(apiErrorMessage(error));
			if (!data) throw new Error("Import validation returned no result");
			return data;
		},
		[],
	);

	const removeProject = useCallback(
		async (projectId: string) => {
			const isLastWorkspace =
              workspaces.length === 1 && workspaces[0]?.id === projectId;
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
			if (isLastWorkspace) {
              void navigate({ to: "/" });
}
		},
		[navigate, updateWorkspaces, workspaces],
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

	// Cursor Agent reads TERM_THEME at spawn from this file. Persist the same
	// resolved light/dark scheme the terminal uses, not nativeTheme alone.
	useEffect(() => {
		void aoBridge.theme?.persistTerminal(resolvedTheme);
	}, [resolvedTheme]);

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
				const workspace = workspacesRef.current[Number(event.key) - 1];
				if (workspace) {
					event.preventDefault();
					void navigate({ to: "/projects/$projectId", params: { projectId: workspace.id } });
				}
			}
		};
		window.addEventListener("keydown", handleKeyDown);
		return () => window.removeEventListener("keydown", handleKeyDown);
	}, [navigate, toggleSidebar]);

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

	// A folder was dropped on the app's taskbar icon/shortcut (main process,
	// cold start or an already-running instance) — feeds the same drop flow as
	// dragging a folder into the open window.
	useEffect(
		() => aoBridge.app.onOpenFolderPath((path) => requestCreateProjectFromPath(path)),
		[requestCreateProjectFromPath],
	);

	// New standalone terminal (⌘T / Ctrl+T), also detected in the main process so it
	// fires from inside a terminal pane. It raises the same store signal as the
	// tab-strip + button so the two cannot drift apart.
	useEffect(
		() =>
			aoBridge.app.onNewShellTerminalShortcut(() => {
				// The project board is not a terminal surface — ⌘T here used to yank
				// users into the standalone /terminals route (#4772). Sessions and the
				// dedicated terminals view keep the shortcut; explicit UI can still
				// open shells from the board.
				if (routeParams.sessionId || isTerminalsRoute) {
					requestNewShellTerminal();
				}
			}),
		[isTerminalsRoute, requestNewShellTerminal, routeParams.sessionId],
	);

	// The shell layout is the single consumer of that signal, because it is the
	// only component mounted on EVERY route. Owning it here is what lets the
	// topbar + button and the keyboard shortcut work from a session or the
	// standalone terminals view alike — when the session view owned it, both
	// silently did nothing outside a session, since nothing was listening.
	//
	// Where the new shell becomes visible depends on where the user is: inside a
	// session it joins that pane's tab strip; on /terminals it joins that strip;
	// explicit board UI can still route here without the keyboard shortcut.
	useEffect(() => {
		if (handledShellNonceRef.current === newShellTerminalNonce) return;
		handledShellNonceRef.current = newShellTerminalNonce;
		const shell = openShellTerminal.open(
			{ projectId: scopedProjectId, sessionId: routeParams.sessionId },
			{
				onSuccess: (openedShell) => {
					setActiveShellTerminal(openedShell.handleId);
				},
			},
		);
		if (!shell) return;
		setActiveShellTerminal(shell.handleId);
		if (!routeParams.sessionId) {
			void navigate({ to: "/terminals" });
		}
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
	const shellContextValue = useMemo(
		() => ({
			daemonStatus,
			workspaceStartupState,
			cloneProject,
			createProject,
			initializeProjectRepository,
			validateImport,
		}),
		[
			cloneProject,
			createProject,
			daemonStatus,
			initializeProjectRepository,
			validateImport,
			workspaceStartupState,
		],
	);

	// Keep the shell chrome and its first route behind the same readiness gate.
	// Rendering the sidebar with an empty query while the home outlet shows its
	// loader creates a visible two-stage launch and can make the home page flash
	// before the project list arrives.
	if (isStartupLoading) return <DaemonStartupLoader />;

	return (
		<ShellProvider
			value={shellContextValue}
		>
			<SessionTopbarProvider>
				<NotificationRuntime />
				<TrayRuntime />
				{isDragActive ? (
					<div
						aria-hidden="true"
						className="dialog-overlay pointer-events-none flex items-center justify-center"
						data-testid="folder-drop-overlay"
					>
						<div className="relative isolate flex w-[min(420px,calc(100vw-48px))] flex-col items-center gap-3 rounded-welcome-panel border border-dashed border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] p-8 text-center shadow-[var(--shadow-import-modal)]">
							<span className="grid size-11 place-items-center rounded-xl bg-[var(--color-bg-import-chip)] text-[var(--color-text-import-muted)]">
								<FolderPlus className="size-5" aria-hidden="true" />
							</span>
							<p className="text-[15px] font-semibold text-[var(--color-text-import-title)]">
								{t("createProject.dropToAdd")}
							</p>
						</div>
					</div>
				) : null}
				<GlobalNewTaskDialog />
				<GlobalToast />
				<SettingsDialog />
				<RestartToUpdateDialog />
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
					// `app-shell-root` is the platform-independent hook the native-composition
					// cascade needs to clear this opaque `bg-sidebar` while the live browser
					// page shows through. Windows/Linux were already covered by their
					// platform classes; macOS adds none, so without this the shell painted
					// over the live page whenever an overlay raised it.
					"app-shell-root flex h-screen min-h-0 flex-col bg-sidebar text-foreground",
					isWindows && "platform-windows",
					isLinux && "platform-linux",
					isFullScreen && "native-fullscreen",
				)}
			>
				{/* Windows-only custom title bar (sidebar toggle + File/Edit/View/…
            menu); paints the chrome the frameless window drops. Renders null on
            macOS/Linux. */}
				<WindowTitlebar />
				{/* App routes render their topbar inside the framed panel, matching the board chrome across platforms while leaving OS titlebars native. */}
				{!framedAppTopbar && !hideShellTopbar && !routeParams.sessionId ? <ShellTopbar /> : null}
				{/* Controlled by the ui-store so TitlebarNav / Topbar toggles (which
            call the store directly) stay in sync. --sidebar-width chains to
            the drag-resizable --ao-sidebar-w set on :root by useResizable. */}
				<SidebarProvider
					className="min-h-0 flex-1 flex-col overflow-x-hidden"
					keyboardShortcut={false}
					onOpenChange={(open) => {
						if (open !== isSidebarOpen) toggleSidebar();
					}}
					open={!isStartupLoading && isSidebarOpen}
					style={
						{
							"--sidebar-width": "var(--ao-sidebar-w, var(--size-sidebar-default))",
							"--sidebar-width-icon": "var(--size-sidebar-icon)",
						} as CSSProperties
					}
				>
				<div
					className="flex min-h-0 w-full flex-1 overflow-x-hidden"
					data-testid="shell-content-row"
				>
				{/* macOS + Linux reserve a titlebar band for the fixed TitlebarNav
              cluster above a full-height sidebar; Windows hangs the sidebar
              below its custom titlebar. */}
				<Sidebar
					hideEdgeBorder={isHomeRoute}
					underTopbar={isMac || isWindows || isLinux}
						topbarOffset={isWindows ? "titlebar" : hideShellTopbar ? "trafficLights" : "toolbar"}
						onCloneProject={cloneProject}
						onCreateProject={createProject}
						onInitializeProject={initializeProjectRepository}
						onRemoveProject={removeProject}
						workspaceError={workspaceQuery.isError ? errorMessage(workspaceQuery.error) : undefined}
						workspaces={workspaces}
					/>
					<main className={cn("flex min-w-0 flex-1 flex-col overflow-x-hidden", !sidebarHasLayout && "sidebar-hidden")}>
						<div className="min-h-0 flex-1 overflow-x-hidden">
							{/* Board/session routes render inside the same inset box the welcome board and settings paint for themselves, so every screen sits within the app's outer boundary. */}
							<ShellCenter
								hideShellTopbar={hideShellTopbar}
								isSessionRoute={Boolean(routeParams.sessionId)}
								selfFramedCenterPanel={selfFramedCenterPanel}
							/>
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
						historyLocked={isHomeRoute}
						isFullScreen={isFullScreen}
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
