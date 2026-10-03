import { memo, useCallback, useEffect, useRef, useState, type MouseEvent } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
	SessionsArchiveView,
	SessionsBoardGridView,
	archiveToggleOffsetClassName,
} from "@aoagents/product-ui";
import { AlertTriangle, LayoutDashboard, RotateCw } from "lucide-react";
import {
	CLOUD_PROJECT_KIND,
	type WorkspaceSession,
	newestActiveOrchestrator,
	orchestratorHealth,
	workerSessions,
	hasConfiguredOrchestratorAgent,
} from "../types/workspace";
import {
	boardKanbanColumnOrder,
	getKanbanColumnView,
	type KanbanColumnView,
} from "../lib/session-presentation";
import {
	useSessionUsageSummaries,
	type SessionUsageSummary,
} from "../hooks/useSessionUsageSummaries";
import { useRestoreSession } from "../hooks/useRestoreSession";
import { useTerminateSession } from "../hooks/useTerminateSession";
import { useRemoteProjectQuery, useWorkspaceQuery, workspaceQueryKeyForHost } from "../hooks/useWorkspaceQuery";
import { NotificationCenter } from "./NotificationCenter";
import { BoardWelcome, ProjectBoardEmpty } from "./BoardEmptyStates";
import { TopbarButton, topbarProjectLabelClass } from "./TopbarButton";
import { restartProjectOrchestrator } from "../lib/restart-orchestrator";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import { demoBoardSessions } from "../lib/demo-board-sessions";
import { isLinuxPlatform, isMacPlatform, usesBoardActionsInPanel } from "../lib/platform";
import { cn } from "../lib/utils";
import { useUiStore } from "../stores/ui-store";
import { RestoreUnavailableDialog } from "./RestoreUnavailableDialog";
import { DaemonStartupLoader } from "./DaemonStartupLoader";
import { useBoardPresentation } from "../hooks/useBoardPresentation";
import { useProjectOrchestratorAction } from "../hooks/useProjectOrchestratorAction";
import { openRemoteOrchestrator } from "../lib/remote-orchestrator";
import { labelForHost } from "../lib/host-clients";
import { useConnectedHosts } from "../hooks/useHostConnection";
import { LOCAL_HOST, refKey } from "../lib/hosts";
import { useShellMaybe } from "../lib/shell-context";
import { sessionNavigateTarget } from "../lib/navigate-to-session";
import { ProjectBoardActions } from "./ProjectBoardActions";
import {
	ArchivedSessionCardAdapter,
	BoardSessionCardAdapter,
	sessionsBoardLabels,
} from "./SessionsBoardAdapters";

type SessionsBoardProps = {
	/** When set, the board shows only this project's sessions. */
	projectId?: string;
	hostId?: string;
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

export function SessionsBoard({ projectId, hostId }: SessionsBoardProps) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const shell = useShellMaybe();
	const connected = useConnectedHosts().includes(hostId ?? "");
	const scopeKey = refKey({ host: hostId ?? LOCAL_HOST, id: projectId ?? "all" });
	// Lanes follow the daemon's delivery order: building -> validating ->
	// in review -> ready. The middle two are one review-feedback loop, split by
	// whose turn it is.
	const columns: KanbanColumnView[] = boardKanbanColumnOrder.map((column) => getKanbanColumnView(column, t));
	const localWorkspaceQuery = useWorkspaceQuery();
	const remoteProjectQuery = useRemoteProjectQuery(hostId ?? "", projectId ?? "");
	const liveUsageBySession = useSessionUsageSummaries(projectId, hostId).data ?? emptyUsageBySession;
	// Evaluated at render so platform mocks in tests can flip the in-panel chrome.
	const boardActionsInPanel = usesBoardActionsInPanel();
	/** Bell lives in the board action row when the shell topbar does not host it. */
	const boardOwnsNotificationCenter = isLinuxPlatform() || boardActionsInPanel;
	const all = localWorkspaceQuery.data ?? [];
	const workspaces = hostId
		? remoteProjectQuery.data ? [remoteProjectQuery.data] : []
		: projectId ? all.filter((candidate) => candidate.id === projectId) : all;
	const workspace = projectId ? workspaces[0] : undefined;
	// Board chrome stays route-oriented; project context remains in the sidebar.
	const boardLabel = t("shell.board");
	const liveSessions = workspaces.flatMap((workspace) => workerSessions(workspace.sessions));
	const demoWorkspaceId = projectId ?? workspaces[0]?.id;
	const sessions = !hostId && usesPreviewWorkspaceData && demoWorkspaceId && liveSessions.length === 0
		? demoBoardSessions(demoWorkspaceId)
		: liveSessions;
	const usageBySession = !hostId && usesPreviewWorkspaceData
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
	const projectActions = useProjectOrchestratorAction({
		projectId,
		project: workspace,
		orchestrator,
		source: "board",
		hostId,
	});
	const { isProjectRestarting, isProvisioning } = projectActions;
	const setProjectRestarting = useUiStore((state) => state.setProjectRestarting);
	const setOrchestratorReplacementError = useUiStore((state) => state.setOrchestratorReplacementError);
	const setOrchestratorStartupError = useUiStore((state) => state.setOrchestratorStartupError);
	const health = workspace ? orchestratorHealth(workspace, isProjectRestarting) : { state: "ok" as const };
	const archived = sessions
		.filter(isArchivedSession)
		.sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
	const activeSessions = sessions.filter((candidate) => !isArchivedSession(candidate));
	const boardSessions = activeSessions.map((session) =>
		session.status === "no_signal" || session.displayStatus === "No signal"
			? { ...session, kanbanColumn: "building" as const }
			: session,
	);
	const boardLabels = sessionsBoardLabels(t);
	const presentation = useBoardPresentation({
		projectId,
		isSuccess: localWorkspaceQuery.isSuccess,
		isError: localWorkspaceQuery.isError,
		hasProjects: workspaces.length > 0,
		hasWorkerSessions: liveSessions.length > 0,
	});
	const showStartup = !hostId && presentation.showStartup;
	const showWelcome = !hostId && presentation.showWelcome;
	const showProjectEmpty = hostId
		? connected && remoteProjectQuery.isSuccess && Boolean(workspace) && liveSessions.length === 0
		: presentation.showProjectEmpty;
	const hasArchive = archived.length > 0;
	const terminateSession = useTerminateSession();
	const activeScopeRef = useRef(scopeKey);
	activeScopeRef.current = scopeKey;

	const openSession = useCallback((session: WorkspaceSession) => {
		void navigate(sessionNavigateTarget(session.workspaceId, session.id, hostId));
	}, [navigate, hostId]);

	const restartOrchestrator = async () => {
		if (!projectId || isProjectRestarting || isProvisioning) return;
		if (hostId) {
			if (!connected) return;
			setProjectRestarting(projectId, true, hostId);
			setOrchestratorStartupError(projectId, null, hostId);
			try {
				const sessionId = await openRemoteOrchestrator(hostId, projectId, orchestrator, undefined, true, "restart");
				await queryClient.invalidateQueries({ queryKey: workspaceQueryKeyForHost(hostId) });
				if (activeScopeRef.current === scopeKey) void navigate(sessionNavigateTarget(projectId, sessionId, hostId));
			} catch (error) {
				setOrchestratorStartupError(projectId, error instanceof Error ? error.message : t("shell.couldNotSpawn"), hostId);
			} finally {
				setProjectRestarting(projectId, false, hostId);
			}
			return;
		}
		await restartProjectOrchestrator({
			projectId,
			queryClient,
			navigate,
			setProjectRestarting,
			setOrchestratorReplacementError,
		});
	};

	const actions = projectId && (!hostId || connected) ? (
		<>
			<ProjectBoardActions actions={projectActions} placement="header" quiet={showProjectEmpty} cloud={workspace?.kind === CLOUD_PROJECT_KIND} />
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
		<div className="relative flex h-full min-h-0 flex-col bg-background text-foreground" data-testid="board" data-host-id={hostId} data-project-id={projectId}>
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
							{hostId ? <span className="truncate text-muted-foreground">· {labelForHost(hostId) ?? hostId}</span> : null}
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
			{hostId && !connected ? <p role="alert" className="px-4 py-3 text-sm text-destructive">{t("remote.hostOffline")}</p> : null}
			{hostId && remoteProjectQuery.isError ? <p role="alert" className="px-4 py-3 text-sm text-destructive">{t("shell.couldNotLoadProjects")}</p> : null}
			{hostId && remoteProjectQuery.isSuccess && !workspace ? <p role="alert" className="px-4 py-3 text-sm text-destructive">{t("session.notFound")}</p> : null}
			{hostId && projectId && connected && workspace && !orchestrator && !hasConfiguredOrchestratorAgent(workspace) ? (
				<div className="mx-3 my-3 flex items-center gap-3 rounded-md border border-border bg-surface px-3 py-2 text-xs text-muted-foreground">
					<span className="min-w-0 flex-1">{t("remote.configureOrchestratorFirst", { label: labelForHost(hostId) ?? hostId, defaultValue: "Choose an orchestrator agent on {{label}} to start one." })}</span>
					<button type="button" className="shrink-0 rounded-md px-2 py-1 font-medium text-foreground hover:bg-interactive-hover focus-visible:outline-2 focus-visible:outline-ring" onClick={() => shell?.openRemoteProjectSettings(hostId, projectId)}>{t("restoreUnavailable.configureOrchestrator")}</button>
				</div>
			) : null}

			{/* Reserve only the collapsed archive bar. Expanded archive overlays the
			    board so lane height (and Needs You scrollbars) stay stable. */}
			<div className={cn("min-h-0 flex-1 overflow-hidden", hasArchive && archiveToggleOffsetClassName)}>
				{projectId && health.state !== "ok" && (!hostId || connected && (health.state !== "missing" || hasConfiguredOrchestratorAgent(workspace))) ? (
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
			{projectId && isProvisioning ? (
				<div
					className="mx-3 my-3 flex items-center gap-3 rounded-md border border-border bg-surface px-3 py-2 text-xs text-muted-foreground"
					role="status"
				>
					<span
						className="size-icon-base shrink-0 animate-spin rounded-full border-2 border-current border-r-transparent"
						aria-hidden="true"
					/>
					<span className="min-w-0 flex-1">
						{t("shell.provisioning", { defaultValue: "Setting up the project — starting the orchestrator…" })}
					</span>
				</div>
			) : null}
				{!hostId && (presentation.workspaceStartupState === "error" || localWorkspaceQuery.isError) ? (
					<p className="py-10 text-center text-xs text-passive">{t("shell.couldNotLoadSessions")}</p>
				) : hostId && !workspace ? null : showWelcome ? (
				<BoardWelcome />
				) : showProjectEmpty ? (
					<ProjectBoardEmpty actions={<ProjectBoardActions actions={projectActions} placement="empty" />} />
				) : (
					<SessionsBoardGridView
						columns={columns}
						key={scopeKey}
						labels={boardLabels}
						renderSessionCard={(session) => (
							<BoardSessionCardAdapter
								onOpen={() => openSession(session)}
								onTerminate={!hostId || connected ? () => terminateSession.mutate(session) : undefined}
								session={session}
								usage={usageBySession.get(session.id)}
							/>
						)}
						sessions={boardSessions}
					/>
				)}
			</div>

			{hasArchive ? (
				<BoardArchivePanel
					activeScopeRef={activeScopeRef}
					hostId={hostId}
					scopeKey={scopeKey}
					connected={!hostId || connected}
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
	activeScopeRef,
	hostId,
	scopeKey,
	connected,
	sessions,
	usageBySession,
}: {
	activeScopeRef: React.MutableRefObject<string>;
	hostId?: string;
	scopeKey: string;
	connected: boolean;
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
	}, [scopeKey]);

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
		const restoreScopeKey = scopeKey;
		const generation = restoreGenerationRef.current;
		const isStillActiveProject = () =>
			generation === restoreGenerationRef.current &&
			activeScopeRef.current === restoreScopeKey;
		setRestoringSessionId(session.id);
		setRestoreErrors((current) => {
			const next = { ...current };
			delete next[session.id];
			return next;
		});
		try {
			const result = await restoreSessionById(session.id, hostId);
			if (!isStillActiveProject()) return;
			if (result.status === "success") {
				void navigate(sessionNavigateTarget(session.workspaceId, session.id, hostId));
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
						isRestoreDisabled={!connected || restoringSessionId !== undefined}
						isRestoring={restoringSessionId === session.id}
						restoreAction={(event) => void restoreArchivedSession(event, session)}
						restoreError={restoreErrors[session.id]}
						session={session}
						usage={usageBySession.get(session.id)}
					/>
				)}
				resetKey={scopeKey}
				sessions={sessions}
			/>
			{restoreUnavailableSession ? (
				<RestoreUnavailableDialog
					open={true}
					session={restoreUnavailableSession}
					hostId={hostId}
					onOpenChange={(open) => {
						if (!open) setRestoreUnavailableSession(undefined);
					}}
					onRecreated={async () => {
						await queryClient.invalidateQueries({ queryKey: workspaceQueryKeyForHost(hostId) });
					}}
				/>
			) : null}
		</>
	);
});
