import { memo, useCallback, useEffect, useRef, useState, type MouseEvent } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
	SessionsArchiveView,
	SessionsBoardGridView,
	archiveToggleOffsetClassName,
} from "@aoagents/product-ui";
import { AlertTriangle, LayoutDashboard, Plus, RotateCw } from "lucide-react";
import {
	type WorkspaceSession,
	hasConfiguredOrchestratorAgent,
	newestActiveOrchestrator,
	orchestratorHealth,
	workerSessions,
} from "../types/workspace";
import {
	boardKanbanColumnOrder,
	getAgentActivityView,
	getKanbanColumnView,
	type KanbanColumnView,
} from "../lib/session-presentation";
import {
	useSessionUsageSummaries,
	type SessionUsageSummary,
} from "../hooks/useSessionUsageSummaries";
import { useRestoreSession } from "../hooks/useRestoreSession";
import { useTerminateSession } from "../hooks/useTerminateSession";
import { cloudSessionsQueryKey, useWorkspaceQuery, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { NotificationCenter } from "./NotificationCenter";
import { ProjectBoardEmpty } from "./BoardEmptyStates";
import { HomePage } from "./HomePage";
import { OrchestratorIcon } from "./icons";
import { OrchestratorActivityIndicator } from "./OrchestratorActivityIndicator";
import { TopbarActionError, TopbarButton, topbarProjectLabelClass } from "./TopbarButton";
import { spawnCloudOrchestrator } from "../lib/cloud-orchestrator";
import { isChatPreflightError, spawnOrchestrator } from "../lib/spawn-orchestrator";
import { restartProjectOrchestrator } from "../lib/restart-orchestrator";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import { demoBoardSessions } from "../lib/demo-board-sessions";
import { isLinuxPlatform, isMacPlatform, usesBoardActionsInPanel } from "../lib/platform";
import { formatOrchestratorStartupError } from "../lib/orchestrator-startup-error";
import { cn } from "../lib/utils";
import { useUiStore } from "../stores/ui-store";
import { RestoreUnavailableDialog } from "./RestoreUnavailableDialog";
import { DaemonStartupLoader } from "./DaemonStartupLoader";
import { useShellMaybe } from "../lib/shell-context";
import { useSystemRequirementsGate } from "../hooks/useSystemRequirementsGate";
import {
	ArchivedSessionCardAdapter,
	BoardSessionCardAdapter,
	sessionsBoardLabels,
} from "./SessionsBoardAdapters";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type SessionsBoardProps = {
	/** When set, the board shows only this project's sessions. */
	projectId?: string;
};

type UsageBySession = ReadonlyMap<string, SessionUsageSummary>;
const emptyUsageBySession: UsageBySession = new Map();

// Live merged sessions remain in-flow. A terminated runtime is archived even
// when its SCM outcome remains `merged`, which is exactly what the daemon's
// `archive` column means.
function isArchivedSession(session: WorkspaceSession): boolean {
	return (
		session.kanbanColumn === "archive" ||
		session.isTerminated === true ||
		session.status === "terminated"
	);
}

const isMac = isMacPlatform();
const dragStyle = isMac ? ({ WebkitAppRegion: "drag" } as React.CSSProperties) : undefined;
const noDragStyle = isMac ? ({ WebkitAppRegion: "no-drag" } as React.CSSProperties) : undefined;

export function SessionsBoard({ projectId }: SessionsBoardProps) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	// Lanes follow the daemon's delivery order: building -> validating ->
	// in review -> ready. The middle two are one review-feedback loop, split by
	// whose turn it is.
	const columns: KanbanColumnView[] = boardKanbanColumnOrder.map((column) => getKanbanColumnView(column, t));
	const workspaceQuery = useWorkspaceQuery();
	const shell = useShellMaybe();
	const liveUsageBySession = useSessionUsageSummaries(projectId).data ?? emptyUsageBySession;
	// Evaluated at render so platform mocks in tests can flip the in-panel chrome.
	const boardActionsInPanel = usesBoardActionsInPanel();
	/** Bell lives in the board action row when the shell topbar does not host it. */
	const boardOwnsNotificationCenter = isLinuxPlatform() || boardActionsInPanel;
	const all = workspaceQuery.data ?? [];
	const workspaces = projectId ? all.filter((workspace) => workspace.id === projectId) : all;
	const workspace = projectId ? workspaces[0] : undefined;
	// Board chrome stays route-oriented; project context remains in the sidebar.
	const boardLabel = t("shell.board");
	const liveSessions = workspaces.flatMap((workspace) => workerSessions(workspace.sessions));
	const demoWorkspaceId = projectId ?? workspaces[0]?.id;
	const sessions = usesPreviewWorkspaceData && demoWorkspaceId && liveSessions.length === 0
		? demoBoardSessions(demoWorkspaceId)
		: liveSessions;
	const usageBySession = usesPreviewWorkspaceData
		? new Map<string, SessionUsageSummary>(
				sessions.map((session, index) => [
						session.id,
						liveUsageBySession.get(session.id) ?? {
							estimatedCost: null,
							sessionId: session.id,
							processedTokens: [18_400, 46_700, 12_900, 81_200, 3_100][index % 5],
							totalTokens: 100_000,
							incomplete: false,
					},
				]),
			)
		: liveUsageBySession;
	const orchestrator = projectId ? newestActiveOrchestrator(workspaces[0]?.sessions ?? []) : undefined;
	const orchestratorActivityLabel = orchestrator ? getAgentActivityView(orchestrator.activity, t).label : undefined;
	const [isSpawning, setIsSpawning] = useState(false);
	const [spawnError, setSpawnError] = useState<string | null>(null);
	const [canCreateAsTui, setCanCreateAsTui] = useState(false);
	const restartingProjectIds = useUiStore((state) => state.restartingProjectIds);
	const orchestratorStartupError = useUiStore((state) =>
		projectId ? (state.orchestratorStartupErrors[projectId] ?? null) : null,
	);
	const setProjectRestarting = useUiStore((state) => state.setProjectRestarting);
	const setOrchestratorReplacementError = useUiStore((state) => state.setOrchestratorReplacementError);
	const setOrchestratorStartupError = useUiStore((state) => state.setOrchestratorStartupError);
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	const isProjectRestarting = projectId ? restartingProjectIds.has(projectId) : false;
	const health = workspace ? orchestratorHealth(workspace, isProjectRestarting) : { state: "ok" as const };
	const visibleSpawnError = formatOrchestratorStartupError(spawnError ?? orchestratorStartupError ?? "");

	// The board instance survives project-to-project navigation (same route,
	// new param), so a spawn failure must not follow the user to another board.
	useEffect(() => {
		setSpawnError(null);
		setCanCreateAsTui(false);
	}, [projectId]);
	const previousProjectIdRef = useRef(projectId);
	useEffect(() => {
		const previousProjectId = previousProjectIdRef.current;
		if (previousProjectId && previousProjectId !== projectId) {
			setOrchestratorStartupError(previousProjectId, null);
		}
		previousProjectIdRef.current = projectId;
	}, [projectId, setOrchestratorStartupError]);
	useEffect(() => {
		if (projectId && orchestrator && orchestratorStartupError) {
			setOrchestratorStartupError(projectId, null);
		}
	}, [orchestrator, orchestratorStartupError, projectId, setOrchestratorStartupError]);

	const archived = sessions
		.filter(isArchivedSession)
		.sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
	const activeSessions = sessions.filter((candidate) => !isArchivedSession(candidate));
	const boardLabels = sessionsBoardLabels(t);
	// First-run orientation replaces the empty column shells (only once the
	// query has resolved, so the welcome never flashes over real data): the
	// global board teaches the app before any project exists, and a fresh
	// project board invites the first task instead of showing four zeros.
	const isDaemonReady = usesPreviewWorkspaceData || (shell ? shell.daemonStatus.state === "ready" : true);
	const daemonHasFailed = Boolean(shell?.daemonStatus.code);
	const workspaceStartupState = shell?.workspaceStartupState ?? "ready";
	const { blocked: requirementsBlocked } = useSystemRequirementsGate();
	const isLoaded = isDaemonReady && workspaceStartupState === "ready" && workspaceQuery.isSuccess && !requirementsBlocked;
	// The shell deliberately keeps its sidebar closed until this same readiness
	// boundary. Keep the full-screen loader up until then so the first visible
	// frame contains the restored sidebar and center pane together, rather than
	// showing a collapsed layout that expands a moment later.
	const showStartup =
		shell !== null &&
		!daemonHasFailed &&
		(!isDaemonReady || workspaceStartupState === "loading" || (!workspaceQuery.isSuccess && !workspaceQuery.isError) || requirementsBlocked);
	const showWelcome = !projectId && isLoaded && all.length === 0;
	const showProjectEmpty = projectId !== undefined && isLoaded && workspaces.length > 0 && sessions.length === 0;
	const hasArchive = archived.length > 0;
	const terminateSession = useTerminateSession();
	const activeProjectIdRef = useRef(projectId);
	activeProjectIdRef.current = projectId;

	const openSession = useCallback((session: WorkspaceSession) =>
		void navigate({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: session.workspaceId, sessionId: session.id },
		}), [navigate]);

	const openOrchestrator = async (mode?: "tui") => {
		if (!projectId || isProjectRestarting) return;
		if (orchestrator) {
			void navigate({
				to: "/projects/$projectId/sessions/$sessionId",
				params: { projectId, sessionId: orchestrator.id },
			});
			return;
		}
		// Cloud projects carry no local orchestrator-agent config; spawn the
		// orchestrator as a cloud session in its own sandbox instead of falling
		// through to the project-settings page.
		if (workspace?.kind === "cloud") {
			setSpawnError(null);
			setIsSpawning(true);
			try {
				const sessionId = await spawnCloudOrchestrator(queryClient, projectId);
				await queryClient.invalidateQueries({ queryKey: cloudSessionsQueryKey });
				void navigate({
					to: "/projects/$projectId/sessions/$sessionId",
					params: { projectId, sessionId },
				});
			} catch (error) {
				console.error("Failed to spawn cloud orchestrator:", error);
				setSpawnError(formatOrchestratorStartupError(error instanceof Error ? error.message : t("shell.couldNotSpawn")));
			} finally {
				setIsSpawning(false);
			}
			return;
		}
		if (!hasConfiguredOrchestratorAgent(workspace)) {
			if (workspace) {
				useUiStore.getState().openProjectSettings(projectId);
			}
			return;
		}
		setSpawnError(null);
		setCanCreateAsTui(false);
		setOrchestratorStartupError(projectId, null);
		setIsSpawning(true);
		try {
			const sessionId = await spawnOrchestrator(projectId, "board", false, mode);
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			setOrchestratorStartupError(projectId, null);
			void navigate({
				to: "/projects/$projectId/sessions/$sessionId",
				params: { projectId, sessionId },
			});
		} catch (error) {
			// Never fail silently: the daemon's message (e.g. a worktree/branch
			// conflict) is the only actionable signal the user gets.
			console.error("Failed to spawn orchestrator:", error);
			setSpawnError(formatOrchestratorStartupError(error instanceof Error ? error.message : t("shell.couldNotSpawn")));
			setCanCreateAsTui(isChatPreflightError(error));
		} finally {
			setIsSpawning(false);
		}
	};

	const restartOrchestrator = async () => {
		if (!projectId) return;
		await restartProjectOrchestrator({
			projectId,
			queryClient,
			navigate,
			setProjectRestarting,
			setOrchestratorReplacementError,
		});
	};

	const actions = projectId ? (
		<>
			{visibleSpawnError && !showProjectEmpty && (
				<TopbarActionError className="max-w-content-max truncate" title={visibleSpawnError}>
					{visibleSpawnError}
				</TopbarActionError>
			)}
			{visibleSpawnError && canCreateAsTui && !showProjectEmpty ? (
				<TopbarButton disabled={isSpawning || isProjectRestarting} onClick={() => void openOrchestrator("tui")}>
					{t("newTask.createAsTui")}
				</TopbarButton>
			) : null}
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="inline-flex">
						<TopbarButton
							aria-label={t("shell.newTask")}
							className="topbar-control--labeled"
							data-priority="primary"
							disabled={isProjectRestarting}
							onClick={() => projectId && requestNewTask(projectId)}
							variant="accent"
						>
							<Plus className="size-icon-md" aria-hidden="true" />
							<span data-compact-label>{t("newTask.task")}</span>
						</TopbarButton>
					</span>
				</TooltipTrigger>
				<TooltipContent side="bottom">{t("shell.newTask")}</TooltipContent>
			</Tooltip>
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="inline-flex">
						<TopbarButton
							aria-label={
								orchestratorActivityLabel
									? t("shell.orchestratorWithActivity", { activity: orchestratorActivityLabel })
									: t("shell.spawnOrchestrator")
							}
							className="topbar-control--labeled"
							data-priority="secondary"
							disabled={isSpawning || isProjectRestarting}
							onClick={() => void openOrchestrator()}
							variant="primary"
						>
							<OrchestratorIcon className="size-icon-md" aria-hidden="true" />
							<span data-compact-label>{t("shell.orchestrator")}</span>
							{orchestrator ? <OrchestratorActivityIndicator session={orchestrator} /> : null}
						</TopbarButton>
					</span>
				</TooltipTrigger>
				<TooltipContent side="bottom">
					{isProjectRestarting
						? t("shell.restarting")
						: isSpawning
							? t("shell.spawning")
							: orchestrator
								? t("shell.openOrchestrator")
								: t("shell.spawnOrchestrator")}
				</TooltipContent>
			</Tooltip>
			{boardOwnsNotificationCenter ? (
				<>
					<NotificationCenter />
				</>
			) : null}
		</>
	) : boardOwnsNotificationCenter ? (
		<NotificationCenter />
	) : undefined;

	return (
		<div className="relative flex h-full min-h-0 flex-col bg-background text-foreground" data-testid="board">
			{!boardActionsInPanel && isLoaded && visibleSpawnError && !showProjectEmpty ? (
				<p role="alert" className="mx-3 my-3 whitespace-pre-wrap break-words text-sm text-destructive">
					{visibleSpawnError}
				</p>
			) : null}
			{/* macOS: shell topbar is hidden on board routes, so the project/"Board"
			    crumb + New task / Orchestrator / bell live in this in-panel row.
			    Win/Linux keep the crumb and actions in the framed ShellTopbar.
			    Welcome skips the row — a dangling "Board" above the import
			    chooser was review feedback on #2432. */}
			{!showWelcome && boardActionsInPanel && (boardLabel || actions) ? (
				<div
					className="workspace-topbar-container center-panel-titlebar flex h-toolbar shrink-0 items-center gap-2 border-b border-border-strong pr-1"
					style={dragStyle}
				>
					{boardLabel ? (
						<span
							className={cn(topbarProjectLabelClass, "inline-flex items-center gap-1.5")}
							data-testid="board-topbar-label"
						>
							<LayoutDashboard aria-hidden="true" className="size-icon-md" />
							{boardLabel}
						</span>
					) : null}
					<div className="min-w-0 flex-1" />
					{actions ? (
						<div className="workspace-topbar-actions flex shrink-0 items-center" style={noDragStyle}>
							{actions}
						</div>
					) : null}
				</div>
			) : null}

			{/* Reserve only the collapsed archive bar. Expanded archive overlays the
			    board so lane height (and Needs You scrollbars) stay stable. */}
			<div className={cn("min-h-0 flex-1 overflow-hidden", hasArchive && archiveToggleOffsetClassName)}>
				{projectId && health.state !== "ok" ? (
					<div className="mx-3 my-3 flex items-center gap-3 rounded-md border border-border bg-surface px-3 py-2 text-xs text-muted-foreground">
						<AlertTriangle className="size-icon-base shrink-0 text-warning" aria-hidden="true" />
						<span className="min-w-0 flex-1">{health.message}</span>
						{health.state === "restart_needed" || health.state === "duplicates" ? (
							<TopbarButton disabled={isProjectRestarting} onClick={() => void restartOrchestrator()} variant="primary">
								<RotateCw className="size-3.5" aria-hidden="true" />
								{t("shell.restart")}
							</TopbarButton>
						) : null}
					</div>
				) : null}
				{workspace?.folderMissing ? (
					<div className="mx-3 my-3 flex items-center gap-3 rounded-md border border-border bg-surface px-3 py-2 text-xs text-muted-foreground">
						<AlertTriangle className="size-icon-base shrink-0 text-warning" aria-hidden="true" />
						<span className="min-w-0 flex-1">{t("home.folderMissing")}</span>
					</div>
				) : null}
				{workspaceStartupState === "error" || workspaceQuery.isError ? (
					<p className="py-10 text-center text-xs text-passive">{t("shell.couldNotLoadSessions")}</p>
				) : showWelcome ? (
					<HomePage />
				) : showProjectEmpty ? (
					<ProjectBoardEmpty
						hasOrchestrator={orchestrator !== undefined}
						isSpawning={isSpawning}
						isProjectRestarting={isProjectRestarting}
						onNewTask={() => projectId && requestNewTask(projectId)}
						onOpenOrchestrator={() => void openOrchestrator()}
						onOpenOrchestratorAsTui={canCreateAsTui ? () => void openOrchestrator("tui") : undefined}
						spawnError={visibleSpawnError}
					/>
				) : (
					<SessionsBoardGridView
						columns={columns}
						key={projectId ?? "all"}
						labels={boardLabels}
						renderSessionCard={(session) => (
							<BoardSessionCardAdapter
								onOpenSession={openSession}
								onTerminateSession={terminateSession.mutate}
								session={session}
								usage={usageBySession.get(session.id)}
							/>
						)}
						sessions={activeSessions}
					/>
				)}
			</div>

			{hasArchive ? (
				<BoardArchivePanel
					activeProjectIdRef={activeProjectIdRef}
					projectId={projectId}
					sessions={archived}
					usageBySession={usageBySession}
				/>
			) : null}
			{showStartup ? <DaemonStartupLoader /> : null}
		</div>
	);
}

/**
 * Restore state lives here so expand/collapse in SessionsArchiveView does not
 * re-render the kanban columns. In-flight restores are invalidated on project
 * change or unmount so completion cannot navigate after the user left.
 */
const BoardArchivePanel = memo(function BoardArchivePanel({
	activeProjectIdRef,
	projectId,
	sessions,
	usageBySession,
}: {
	activeProjectIdRef: React.MutableRefObject<string | undefined>;
	projectId?: string;
	sessions: WorkspaceSession[];
	usageBySession: UsageBySession;
}) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const restoreSessionById = useRestoreSession();
	const [restoringSessionId, setRestoringSessionId] = useState<string | undefined>();
	const [restoreErrors, setRestoreErrors] = useState<Record<string, string>>({});
	const [restoreUnavailableSession, setRestoreUnavailableSession] = useState<WorkspaceSession | undefined>();
	const restoreGenerationRef = useRef(0);

	useEffect(() => {
		setRestoringSessionId(undefined);
		setRestoreErrors({});
		setRestoreUnavailableSession(undefined);
		restoreGenerationRef.current += 1;
	}, [projectId]);

	useEffect(() => {
		const generation = restoreGenerationRef.current;
		return () => {
			// Invalidate in-flight restores if this panel unmounts (e.g. project with
			// no archive) so completion cannot navigate after the user left.
			if (restoreGenerationRef.current === generation) {
				restoreGenerationRef.current += 1;
			}
		};
	}, []);

	const restoreArchivedSession = async (event: MouseEvent<HTMLButtonElement>, session: WorkspaceSession) => {
		event.stopPropagation();
		if (restoringSessionId) return;
		const restoreProjectId = projectId;
		const generation = restoreGenerationRef.current;
		const isStillActiveProject = () =>
			generation === restoreGenerationRef.current &&
			(!restoreProjectId || activeProjectIdRef.current === restoreProjectId);
		setRestoringSessionId(session.id);
		setRestoreErrors((current) => {
			const next = { ...current };
			delete next[session.id];
			return next;
		});
		try {
			const result = await restoreSessionById(session.id);
			if (!isStillActiveProject()) return;
			if (result.status === "success") {
				void navigate({
					to: "/projects/$projectId/sessions/$sessionId",
					params: { projectId: session.workspaceId, sessionId: session.id },
				});
				return;
			}
			if (result.status === "not_resumable") {
				setRestoreUnavailableSession(session);
				return;
			}
			setRestoreErrors((current) => ({ ...current, [session.id]: result.message }));
		} finally {
			if (isStillActiveProject()) {
				setRestoringSessionId(undefined);
			}
		}
	};

	return (
		<>
			<SessionsArchiveView
				labels={{
					archive: t("shell.archive"),
					archiveAria: t("shell.archiveSessionsAria", { count: sessions.length }),
					archivedSessions: t("shell.archivedSessions"),
				}}
				renderSessionCard={(session) => (
					<ArchivedSessionCardAdapter
						isRestoreDisabled={restoringSessionId !== undefined}
						isRestoring={restoringSessionId === session.id}
						restoreAction={(event) => void restoreArchivedSession(event, session)}
						restoreError={restoreErrors[session.id]}
						session={session}
						usage={usageBySession.get(session.id)}
					/>
				)}
				resetKey={projectId}
				sessions={sessions}
			/>
			{restoreUnavailableSession ? (
				<RestoreUnavailableDialog
					open={true}
					session={restoreUnavailableSession}
					onOpenChange={(open) => {
						if (!open) setRestoreUnavailableSession(undefined);
					}}
					onRecreated={async () => {
						await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
					}}
				/>
			) : null}
		</>
	);
});
