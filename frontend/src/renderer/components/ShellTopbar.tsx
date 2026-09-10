import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "@tanstack/react-router";
import { Folder, LayoutDashboard, Plus, Trash2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { animate, LayoutGroup, motion, useMotionValue, useReducedMotion } from "motion/react";
import { NotificationCenter } from "./NotificationCenter";
import {
	CLOUD_PROJECT_KIND,
	hasConfiguredOrchestratorAgent,
	isOrchestratorSession,
	sessionIsActive,
	type WorkspaceSession,
} from "../types/workspace";
import { cloudSessionsQueryKey, useWorkspaceScope, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import {
	clearTerminateSessionState,
	useProjectTerminateSessionStates,
	useTerminateSession,
	useTerminateSessionState,
} from "../hooks/useTerminateSession";
import { spawnCloudOrchestrator } from "../lib/cloud-orchestrator";
import { spawnOrchestrator } from "../lib/spawn-orchestrator";
import { addRendererExceptionStep, captureRendererEvent, captureRendererException } from "../lib/telemetry";
import { sidebarOccupiesLayout, useUiStore } from "../stores/ui-store";
import { OrchestratorIcon } from "./icons";
import { OrchestratorActivityIndicator } from "./OrchestratorActivityIndicator";
import { getAgentActivityView } from "../lib/session-presentation";
import { isLinuxPlatform, isMacPlatform, usesBoardActionsInPanel } from "../lib/platform";
import { cn } from "../lib/utils";
import { SHELL_PANEL_SPRING } from "../lib/motion-spring";
import { useWindowFullScreen } from "../hooks/useWindowFullScreen";
import { StatusPill } from "./StatusPill";
import { TopbarActionError, TopbarButton, topbarHeaderClass, topbarProjectLabelClass } from "./TopbarButton";
import { SessionTerminationPopover } from "./SessionTerminationPopover";
import { TopbarOpenEditorButton } from "./TopbarOpenEditorButton";
import {
	agentSwitchStatusVisual,
	deriveSessionAgentSwitchPresentation,
} from "../lib/agent-switch-presentation";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

const isMac = isMacPlatform();
const boardActionsInPanel = usesBoardActionsInPanel();
const dragStyle = isMac ? ({ WebkitAppRegion: "drag" } as React.CSSProperties) : undefined;
const noDragStyle = isMac ? ({ WebkitAppRegion: "no-drag" } as React.CSSProperties) : undefined;

// The one app topbar (.dashboard-app-header). On Win/Linux the shell mounts it
// inside the framed center panel; when the platform hides the shell topbar
// (macOS), SessionView mounts the same component in-panel so Kill / Orchestrator
// / inspector stay available. The variant is derived from the route, not props:
// a sessionId in the URL swaps the lead to the session identity (orchestrator
// crumb + mode badge, or worker branch + status pill) and the actions to
// board/orchestrator + inspector controls (orchestrators open the Kanban board;
// workers open their orchestrator); otherwise it's the dashboard crumb plus the
// Orchestrator launcher when a project is in scope. Embedded mode contributes
// only session actions to the terminal bar; other routes retain this full bar.
// Pixel equivalents of the CSS custom properties used for titlebar clearance.
// --size-titlebar-cluster-left (72) + --size-titlebar-cluster-width (3×28+2×4=92)
// + --size-titlebar-content-gap (12) = 176; minus --size-center-panel-inset-mac (6) = 170.
// Fullscreen: --space-2 (8) + 92 + 12 = 112.
// Linux: --size-titlebar-cluster-left-linux (6) + 92 + 12 = 110; minus
// --size-center-panel-inline-inset (16) because the framed panel keeps that gutter.
const PADDING_DEFAULT = 18; // 1.125rem
const PADDING_CLEARANCE = 170;
const PADDING_CLEARANCE_FULLSCREEN = 112;
// Off-canvas the Linux cluster shifts right to clear the framed panel border, so
// measure the reserve from that inset, relative to the panel's own left edge:
// --size-titlebar-cluster-left-linux-panel (26) + --size-titlebar-cluster-width
// (92) + --size-titlebar-content-gap (12) - --size-center-panel-inline-inset (16).
const PADDING_CLEARANCE_LINUX = 114;

export function ShellTopbar({
	embedded = false,
	sessionAction,
	compactActions = false,
}: {
	embedded?: boolean;
	sessionAction?: ReactNode;
	compactActions?: boolean;
} = {}) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const params = useParams({ strict: false }) as { projectId?: string; sessionId?: string };
	const currentSessionId = params.sessionId;
	const isInspectorOpen = useUiStore((state) =>
		currentSessionId ? (state.inspectorSessions[currentSessionId]?.isOpen ?? true) : false,
	);
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	const isSidebarOpen = useUiStore(sidebarOccupiesLayout);
	const isFullScreen = useWindowFullScreen();
	const prefersReducedMotion = useReducedMotion();
	const mac = isMacPlatform();
	const linux = isLinuxPlatform();
	const targetPaddingLeft =
		!embedded && !isSidebarOpen && mac
			? isFullScreen
				? PADDING_CLEARANCE_FULLSCREEN
				: PADDING_CLEARANCE
			: !embedded && !isSidebarOpen && linux
				? PADDING_CLEARANCE_LINUX
				: PADDING_DEFAULT;
	const paddingLeft = useMotionValue(targetPaddingLeft);
	useEffect(() => {
		const controls = animate(
			paddingLeft,
			targetPaddingLeft,
			prefersReducedMotion ? { duration: 0 } : SHELL_PANEL_SPRING,
		);
		return controls.stop;
	}, [targetPaddingLeft, paddingLeft, prefersReducedMotion]);
	const [isSpawning, setIsSpawning] = useState(false);
	// Board-scope spawn failures surface where the board actions render.
	const [boardSpawnError, setBoardSpawnError] = useState<string | null>(null);
	const workspaceScope = useWorkspaceScope(params.projectId, params.sessionId).data;
	const session = workspaceScope?.session;
	const isSessionRoute = Boolean(params.sessionId);
	const isOrchestrator = session ? isOrchestratorSession(session) : false;
	// Project in scope: the session's workspace wins over the route param so the
	// cross-project /sessions/$sessionId route still resolves a crumb. A
	// projectId that no longer resolves (stale route after the project was
	// removed, or data still loading) shows an empty crumb — never the raw
	// route slug. "Board" is the root-board crumb only.
	const projectId = session?.workspaceId ?? params.projectId;
	const isProjectRestarting = useUiStore((state) =>
		projectId ? state.restartingProjectIds.has(projectId) : false,
	);
	const isProvisioning = useUiStore((state) =>
		projectId ? state.provisioningProjectIds.has(projectId) : false,
	);
	const isProjectBoardRoute = !isSessionRoute && Boolean(projectId);
	const isRootBoardRoute = !isSessionRoute && !isProjectBoardRoute;
	const project = workspaceScope?.project;
	const projectLabel = project?.name ?? session?.workspaceName ?? (projectId ? "" : t("shell.board"));
	const orchestrator = workspaceScope?.orchestrator;
	const orchestratorActivityLabel = orchestrator ? getAgentActivityView(orchestrator.activity, t).label : undefined;
	const orchestratorActionLabel = orchestrator ? t("shell.openOrchestrator") : t("shell.spawnOrchestrator");
	const orchestratorTooltip = isProjectRestarting
		? t("shell.restarting")
		: isSpawning
			? t("shell.spawning")
			: orchestratorActionLabel;

	const openBoard = () =>
		projectId ? void navigate({ to: "/projects/$projectId", params: { projectId } }) : void navigate({ to: "/" });

	const openNewTask = () => {
		if (!projectId || isProjectRestarting || isProvisioning) return;
		requestNewTask(projectId);
	};

	const openOrchestrator = async () => {
		if (!projectId || isProvisioning) return;
		setBoardSpawnError(null);
		void addRendererExceptionStep("Orchestrator open requested", {
			source: "orchestrator-open",
			operation: "open_orchestrator",
			surface: isSessionRoute ? "session_detail" : "project_board",
			project_id: projectId,
		});
		void captureRendererEvent("ao.renderer.orchestrator_open_requested", { project_id: projectId });
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
		if (project?.kind === CLOUD_PROJECT_KIND) {
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
				setBoardSpawnError(error instanceof Error ? error.message : t("shell.couldNotSpawn"));
			} finally {
				setIsSpawning(false);
			}
			return;
		}
		if (!hasConfiguredOrchestratorAgent(project)) {
			if (project) {
				useUiStore.getState().openProjectSettings(projectId);
			}
			return;
		}
		setIsSpawning(true);
		try {
			const sessionId = await spawnOrchestrator(projectId, "topbar");
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			void navigate({
				to: "/projects/$projectId/sessions/$sessionId",
				params: { projectId, sessionId },
			});
		} catch (error) {
			void captureRendererException(error, {
				source: "orchestrator-open",
				operation: "open_orchestrator",
				surface: isSessionRoute ? "session_detail" : "project_board",
				project_id: projectId,
			});
			console.error("Failed to spawn orchestrator:", error);
			setBoardSpawnError(error instanceof Error ? error.message : t("shell.couldNotSpawn"));
		} finally {
			setIsSpawning(false);
		}
	};

	return (
		<LayoutGroup id="shell-topbar">
		<motion.header
			className={
				embedded ? "contents" : cn(topbarHeaderClass, "workspace-topbar-container", isSessionRoute && "pr-2")
			}
			style={embedded ? undefined : { ...dragStyle, paddingLeft }}
		>
			{!embedded ? (
				<div className="flex min-w-0 items-center gap-3">
				{isSessionRoute && session ? (
					<div className="flex min-w-0 items-center gap-2.5" data-testid="session-topbar-identity">
						{isOrchestrator ? (
							<span className={cn(topbarProjectLabelClass, "inline-flex min-w-0 items-center gap-1.5")}>
								<Folder aria-hidden="true" className="size-icon-md shrink-0 text-muted-foreground" />
								<span className="max-w-content-max truncate">{projectLabel}</span>
							</span>
						) : (
							<span className={cn(topbarProjectLabelClass, "max-w-content-max truncate")}>{session.title}</span>
						)}
						<span aria-hidden="true" className="workspace-topbar__identity-separator" />
						<SessionStatusPill session={session} />
					</div>
				) : (isProjectBoardRoute && boardActionsInPanel) ||
				  (isMac && isRootBoardRoute && boardActionsInPanel) ? null : (
					<div className="inline-flex min-w-0 items-center gap-1.5" data-testid="board-topbar-label">
						<motion.span
							layoutId="topbar-project-label"
							layout="position"
							className={cn(topbarProjectLabelClass, "inline-flex items-center gap-1.5")}
							transition={{ type: "spring", stiffness: 400, damping: 40 }}
						>
							<LayoutDashboard aria-hidden="true" className="size-icon-md" />
							{t("shell.board")}
						</motion.span>
					</div>
				)}
				</div>
			) : null}

			{!embedded ? <div className="min-w-0 flex-1" /> : null}

			<div
				className="workspace-topbar-actions flex shrink-0 items-center"
				data-compact-actions={compactActions ? "true" : "false"}
				data-testid="workspace-topbar-actions"
			>
				{!boardActionsInPanel && isProjectBoardRoute ? (
					<>
						{boardSpawnError ? (
							<TopbarActionError className="max-w-content-max truncate" title={boardSpawnError}>
								{boardSpawnError}
							</TopbarActionError>
						) : null}
						<Tooltip>
							<TooltipTrigger asChild>
								<span className="inline-flex" style={noDragStyle}>
									<TopbarButton
										aria-label={t("shell.newTask")}
										className="topbar-control--labeled"
										data-priority="primary"
										disabled={isProjectRestarting || isProvisioning}
										onClick={openNewTask}
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
								<span className="inline-flex" style={noDragStyle}>
									<TopbarButton
										aria-label={
											orchestratorActivityLabel
												? t("shell.orchestratorWithActivity", { activity: orchestratorActivityLabel })
												: orchestratorActionLabel
										}
										className="topbar-control--labeled"
										data-priority="secondary"
										disabled={isSpawning || isProjectRestarting || isProvisioning}
										onClick={() => void openOrchestrator()}
										variant="primary"
									>
										<OrchestratorIcon className="size-icon-md" aria-hidden="true" />
										<span data-compact-label>{t("shell.orchestrator")}</span>
										{orchestrator ? <OrchestratorActivityIndicator session={orchestrator} /> : null}
									</TopbarButton>
								</span>
							</TooltipTrigger>
							<TooltipContent side="bottom">{orchestratorTooltip}</TooltipContent>
						</Tooltip>
					</>
				) : null}
				{isSessionRoute ? (
					<>
						{isOrchestrator ? (
							<>
								<ProjectTerminationFeedback projectId={projectId} />
								{sessionAction ? (
									<div className="inline-flex shrink-0 items-center" style={noDragStyle}>
										{sessionAction}
									</div>
								) : null}
								<Tooltip>
									<TooltipTrigger asChild>
										<span className="inline-flex" style={noDragStyle}>
											<TopbarButton
												aria-label={t("shell.newTask")}
												className="topbar-control--labeled"
												data-priority="primary"
												disabled={isProjectRestarting || isProvisioning}
												onClick={openNewTask}
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
										<TopbarButton
											aria-label={t("shell.openKanban")}
											className="topbar-control--labeled"
											data-priority="secondary"
											onClick={openBoard}
											style={noDragStyle}
											variant="feature"
										>
											<LayoutDashboard className="size-icon-md" aria-hidden="true" />
											<span data-compact-label>{t("shell.openKanban")}</span>
										</TopbarButton>
									</TooltipTrigger>
									<TooltipContent side="bottom">{t("shell.openKanban")}</TooltipContent>
								</Tooltip>
							</>
						) : null}
						{/* Open-in-editor leads the session actions: it is the only
						    non-destructive one, and it must sit left of Kill. Kept outside
						    the local-actions group because Electron main independently
						    reports whether this session has a live workspace. Cloud sessions
						    have no local workspace to hand off to an editor: the local daemon
						    has never heard of them, so querying it just surfaces its 404 as a
						    confusing "Unknown session" error (see workspace.ts's `kind` doc). */}
						{session && project?.kind !== CLOUD_PROJECT_KIND ? (
							// Keyed per session so a stale launch error does not carry over
							// when switching sessions. The prefix keeps it distinct from the
							// kill button's key: identical sibling keys make React duplicate
							// the nodes.
							<TopbarOpenEditorButton
								key={`open-workspace-${session.id}`}
								sessionId={session.id}
								projectId={session.workspaceId}
								sessionCreatedAt={session.createdAt}
								sessionTerminated={session.isTerminated}
								style={noDragStyle}
							/>
						) : null}
						{/* Local worker actions share one tight control group. Navigation
						    remains a separate visual target in the outer top-bar row. */}
						{!isOrchestrator && session && (sessionAction || sessionIsActive(session)) ? (
							<div
								className="inline-flex shrink-0 items-center gap-1"
								data-testid="session-local-actions"
								style={noDragStyle}
							>
								{sessionAction ? <div className="inline-flex shrink-0 items-center">{sessionAction}</div> : null}
								{sessionIsActive(session) ? (
									<TopbarKillButton
										key={session.id}
										session={session}
										orchestratorId={orchestrator?.id}
										onKilled={(workspaceId, orchestratorId) => {
											if (orchestratorId) {
												void navigate({
													to: "/projects/$projectId/sessions/$sessionId",
													params: { projectId: workspaceId, sessionId: orchestratorId },
												});
												return;
											}
											void navigate({ to: "/projects/$projectId", params: { projectId: workspaceId } });
										}}
									/>
								) : null}
							</div>
						) : null}
						{!isOrchestrator ? (
							<Tooltip>
								<TooltipTrigger asChild>
									<span className="inline-flex" style={noDragStyle}>
										<TopbarButton
											aria-label={t("shell.openOrchestrator")}
											className="topbar-control--labeled -mr-1"
											data-priority="secondary"
											disabled={isSpawning || isProjectRestarting || isProvisioning}
											onClick={() => void openOrchestrator()}
											variant="primary"
										>
											<OrchestratorIcon className="size-icon-md" aria-hidden="true" />
											<span data-compact-label>{t("shell.orchestrator")}</span>
										</TopbarButton>
									</span>
								</TooltipTrigger>
								<TooltipContent side="bottom">{orchestratorTooltip}</TooltipContent>
							</Tooltip>
						) : null}
					</>
				) : null}
				{isSessionRoute && !isOrchestrator ? (
					/* The pinned controls are owned by SessionView so they stay at the
					   window's right edge. Reserve their width only when the rail is closed. */
					<div
						className="session-pinned-actions-reserve"
						data-state={isInspectorOpen ? "collapsed" : "expanded"}
						data-testid="session-pinned-actions-reserve"
						aria-hidden="true"
					/>
				) : (
					<NotificationCenter style={noDragStyle} />
				)}
			</div>
		</motion.header>
	</LayoutGroup>
	);
}

// Confirmation is modal, but teardown progress is not: confirming closes the
// dialog and returns to the project's orchestrator while the daemon finishes.
// Mutation-cache state is filtered by worker ID so rapid route switches never
// carry another worker's Killing/error state into the current topbar.
export function TopbarKillButton({
	session,
	orchestratorId,
	onKilled,
}: {
	session: WorkspaceSession;
	orchestratorId?: string;
	onKilled: (workspaceId: string, orchestratorId?: string) => void;
}) {
	const { t } = useTranslation();
	const [confirmOpen, setConfirmOpen] = useState(false);
	const queryClient = useQueryClient();
	const kill = useTerminateSession();
	const { error, isPending } = useTerminateSessionState(session.id);

	const confirmKill = () => {
		setConfirmOpen(false);
		kill.mutate(session);
		onKilled(session.workspaceId, orchestratorId);
	};

	return (
		<div className="inline-flex items-center gap-1.5" style={noDragStyle}>
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="inline-flex">
						<SessionTerminationPopover
							onConfirm={confirmKill}
							onOpenChange={setConfirmOpen}
							open={confirmOpen}
							session={session}
							trigger={
								<TopbarButton
									aria-label={isPending ? t("shell.killing") : t("shell.killSession")}
									disabled={isPending}
									onClick={() => {
										clearTerminateSessionState(queryClient, session.id);
									}}
									variant="killIcon"
								>
									<Trash2 className="size-icon-md" aria-hidden="true" />
								</TopbarButton>
							}
						/>
					</span>
				</TooltipTrigger>
				<TooltipContent side="bottom">{t("shell.killSession")}</TooltipContent>
			</Tooltip>
			{error ? <TopbarActionError>{error}</TopbarActionError> : null}
		</div>
	);
}

function ProjectTerminationFeedback({ projectId }: { projectId: string | undefined }) {
	const { t } = useTranslation();
	const states = useProjectTerminateSessionStates(projectId);
	if (states.length === 0) return null;

	return (
		<div aria-label={t("shell.sessionTerminationStatus")} className="flex max-w-content-max items-center gap-2">
			{states.map((state) =>
				state.error ? (
					<TopbarActionError className="max-w-48 truncate" key={state.session.id} title={state.error}>
						{state.session.title}: {state.error}
					</TopbarActionError>
				) : (
					<span
						className="max-w-40 truncate text-caption text-muted-foreground"
						key={state.session.id}
						role="status"
						title={t("shell.killingNamed", { title: state.session.title })}
					>
						{t("shell.killingNamed", { title: state.session.title })}
					</span>
				),
			)}
		</div>
	);
}
function SessionStatusPill({ session }: { session: WorkspaceSession }) {
	const { t } = useTranslation();
	const switchPresentation = deriveSessionAgentSwitchPresentation(session);
	if (switchPresentation) {
		const visual = agentSwitchStatusVisual(switchPresentation);
		return (
			<StatusPill
				label={t(switchPresentation.compactLabelKey, switchPresentation.values)}
				tone={visual.tone}
				breathe={visual.breathe}
				leading="none"
				className="px-2 py-1 text-micro"
			/>
		);
	}
	const { label, tone, breathe } = getAgentActivityView(session.activity, t);
	return (
		<StatusPill label={label} tone={tone} breathe={breathe} leading="none" className="px-2 py-1 text-micro" />
	);
}
