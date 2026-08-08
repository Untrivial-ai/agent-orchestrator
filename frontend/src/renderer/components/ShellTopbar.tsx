import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "@tanstack/react-router";
import {
	GitBranch,
	PanelRightClose,
	PanelRightOpen,
	Plus,
	Trash2,
} from "lucide-react";
import { useEffect, useState } from "react";
import { animate, LayoutGroup, motion, useMotionValue, useReducedMotion } from "motion/react";
import { NotificationCenter } from "./NotificationCenter";
import {
	findProjectOrchestrator,
	hasConfiguredOrchestratorAgent,
	isOrchestratorSession,
	sessionIsActive,
	type WorkspaceSession,
} from "../types/workspace";
import { useWorkspaceQuery, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import {
	clearTerminateSessionState,
	useProjectTerminateSessionStates,
	useTerminateSession,
	useTerminateSessionState,
} from "../hooks/useTerminateSession";
import { spawnOrchestrator } from "../lib/spawn-orchestrator";
import { addRendererExceptionStep, captureRendererEvent, captureRendererException } from "../lib/telemetry";
import { useUiStore } from "../stores/ui-store";
import { OrchestratorIcon } from "./icons";
import { OrchestratorActivityIndicator } from "./OrchestratorActivityIndicator";
import { getAgentActivityView } from "../lib/session-presentation";
import { isMacPlatform, usesBoardActionsInPanel } from "../lib/platform";
import { cn } from "../lib/utils";
import { useWindowFullScreen } from "../hooks/useWindowFullScreen";
import { StatusPill } from "./StatusPill";
import { TopbarButton, TopbarKillError, topbarHeaderClass, topbarProjectLabelClass } from "./TopbarButton";
import { SessionTerminationPopover } from "./SessionTerminationPopover";

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
// board/orchestrator + inspector controls (orchestrators open the board via the
// project-name control; workers open their orchestrator); otherwise it's the
// dashboard crumb plus the Orchestrator launcher when a project is in scope.
// Embedded mode contributes session actions to the terminal bar — and for
// orchestrators, the clickable project name that replaces the old Kanban button.
// Pixel equivalents of the CSS custom properties used for titlebar clearance.
// --size-titlebar-cluster-left (72) + --size-titlebar-cluster-width (3×28+2×4=92)
// + --size-titlebar-content-gap (12) = 176; minus --size-center-panel-inset-mac (6) = 170.
// Fullscreen: --space-2 (8) + 92 + 12 = 112.
const PADDING_DEFAULT = 18; // 1.125rem
const PADDING_CLEARANCE = 170;
const PADDING_CLEARANCE_FULLSCREEN = 112;

export function ShellTopbar({ embedded = false }: { embedded?: boolean } = {}) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const params = useParams({ strict: false }) as { projectId?: string; sessionId?: string };
	const currentSessionId = params.sessionId;
	const isInspectorOpen = useUiStore((state) =>
		currentSessionId ? (state.inspectorSessions[currentSessionId]?.isOpen ?? true) : false,
	);
	const toggleInspector = useUiStore((state) => state.toggleInspector);
	const restartingProjectIds = useUiStore((state) => state.restartingProjectIds);
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	const isSidebarOpen = useUiStore((state) => state.isSidebarOpen);
	const isFullScreen = useWindowFullScreen();
	const prefersReducedMotion = useReducedMotion();
	const mac = isMacPlatform();
	const targetPaddingLeft =
		!embedded && mac && !isSidebarOpen
			? isFullScreen
				? PADDING_CLEARANCE_FULLSCREEN
				: PADDING_CLEARANCE
			: PADDING_DEFAULT;
	const paddingLeft = useMotionValue(targetPaddingLeft);
	useEffect(() => {
		const controls = animate(
			paddingLeft,
			targetPaddingLeft,
			prefersReducedMotion
				? { duration: 0 }
				: { type: "spring", stiffness: 420, damping: 40, mass: 0.6 },
		);
		return controls.stop;
	}, [targetPaddingLeft, paddingLeft, prefersReducedMotion]);
	const [isSpawning, setIsSpawning] = useState(false);
	// Board-scope spawn failures surface where the board actions render.
	const [boardSpawnError, setBoardSpawnError] = useState<string | null>(null);
	const all = useWorkspaceQuery().data ?? [];

	const session = params.sessionId
		? all.flatMap((workspace) => workspace.sessions).find((s) => s.id === params.sessionId)
		: undefined;
	const isSessionRoute = Boolean(params.sessionId);
	const isOrchestrator = session ? isOrchestratorSession(session) : false;
	// Project in scope: the session's workspace wins over the route param so the
	// cross-project /sessions/$sessionId route still resolves a crumb. A
	// projectId that no longer resolves (stale route after the project was
	// removed, or data still loading) shows an empty crumb — never the raw
	// route slug. "Board" is the root-board crumb only.
	const projectId = session?.workspaceId ?? params.projectId;
	const isProjectBoardRoute = !isSessionRoute && Boolean(projectId);
	const isRootBoardRoute = !isSessionRoute && !isProjectBoardRoute;
	const project = projectId ? all.find((workspace) => workspace.id === projectId) : undefined;
	const projectLabel = project?.name ?? session?.workspaceName ?? (projectId ? "" : t("shell.board"));
	const orchestrator = projectId ? findProjectOrchestrator(all, projectId) : undefined;
	const orchestratorActivityLabel = orchestrator ? getAgentActivityView(orchestrator.activity, t).label : undefined;
	const isProjectRestarting = projectId ? restartingProjectIds.has(projectId) : false;

	const openBoard = () =>
		projectId ? void navigate({ to: "/projects/$projectId", params: { projectId } }) : void navigate({ to: "/" });

	const openNewTask = () => {
		if (!projectId || isProjectRestarting) return;
		requestNewTask(projectId);
	};

	const handleToggleInspector = () => {
		if (!currentSessionId) return;
		toggleInspector(currentSessionId);
	};

	const openOrchestrator = async () => {
		if (!projectId) return;
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
			className={embedded ? "contents" : topbarHeaderClass}
			style={embedded ? undefined : { ...dragStyle, paddingLeft }}
		>
			{!embedded ? (
				<div className="flex min-w-0 items-center gap-3">
				{isSessionRoute && isOrchestrator ? (
					<div className="inline-flex min-w-0 items-center gap-2">
						<div className="inline-flex min-w-0 items-center gap-1.5">
							<ProjectBoardLabelButton label={projectLabel} onOpen={openBoard} style={noDragStyle} />
								<span aria-hidden="true" className="text-xs leading-none text-passive">
									·
								</span>
								<span className="inline-flex h-control-sm items-center gap-1 rounded-md border border-border bg-surface px-2 text-micro font-semibold leading-none tracking-wide-sm text-muted-foreground">
									<OrchestratorIcon className="size-3 shrink-0" aria-hidden="true" />
									{t("shell.orchestrator")}
								</span>
							</div>
						</div>
					) : isSessionRoute ? (
						<div className="flex min-w-0 items-center gap-3">
							{session?.branch ? (
								<div className="inline-flex min-w-0 items-center gap-1 font-mono text-2xs leading-none text-passive">
									<GitBranch className="size-icon-2xs shrink-0" aria-hidden="true" />
									<span className="truncate">{session.branch}</span>
								</div>
							) : null}
							{session ? <SessionStatusPill session={session} /> : null}
						</div>
					) : (isProjectBoardRoute && boardActionsInPanel) ||
				  (isMac && isRootBoardRoute && boardActionsInPanel) ? null : (
					<div className="inline-flex min-w-0 items-center gap-1.5">
						<motion.span
							layoutId="topbar-project-label"
							layout="position"
							className={topbarProjectLabelClass}
							transition={{ type: "spring", stiffness: 400, damping: 40 }}
						>
							{projectLabel}
						</motion.span>
					</div>
				)}
				</div>
			) : null}

			{!embedded ? <div className="min-w-0 flex-1" /> : null}

			<div className="flex shrink-0 items-center gap-1.5">
				{!boardActionsInPanel && isProjectBoardRoute ? (
					<>
						{boardSpawnError ? (
							<TopbarKillError className="max-w-content-max truncate" title={boardSpawnError}>
								{boardSpawnError}
							</TopbarKillError>
						) : null}
						<TopbarButton
							aria-label={t("shell.newTask")}
							disabled={isProjectRestarting}
							onClick={openNewTask}
							style={noDragStyle}
							variant="accent"
						>
							<Plus className="size-icon-lg" aria-hidden="true" />
							{t("shell.newTask")}
						</TopbarButton>
						<TopbarButton
							aria-label={
								orchestratorActivityLabel
									? t("shell.orchestratorWithActivity", { activity: orchestratorActivityLabel })
									: t("shell.spawnOrchestrator")
							}
							disabled={isSpawning || isProjectRestarting}
							onClick={() => void openOrchestrator()}
							style={noDragStyle}
							variant="primary"
						>
							<OrchestratorIcon className="size-icon-lg" aria-hidden="true" />
							{orchestrator ? <OrchestratorActivityIndicator session={orchestrator} /> : null}
							{isProjectRestarting
								? t("shell.restarting")
								: isSpawning
									? t("shell.spawning")
									: orchestrator
										? t("shell.orchestrator")
										: t("shell.spawnOrchestrator")}
						</TopbarButton>
					</>
				) : null}
				{isSessionRoute ? (
					<>
						{isOrchestrator ? (
							<>
								{/* Session routes mount this topbar embedded — the lead crumb
								    above is hidden — so the project name must live here too. */}
								{embedded ? (
									<ProjectBoardLabelButton
										className="mr-1"
										label={projectLabel}
										onOpen={openBoard}
										style={noDragStyle}
									/>
								) : null}
								<ProjectTerminationFeedback projectId={projectId} />
								<TopbarButton
									aria-label={t("shell.newTask")}
									disabled={isProjectRestarting}
									onClick={openNewTask}
									style={noDragStyle}
									variant="accent"
								>
									<Plus className="size-icon-lg" aria-hidden="true" />
									{t("shell.newTask")}
								</TopbarButton>
							</>
						) : null}
						{/* Kill control sits beside the orchestrator link for active workers —
						    moved here from the inspector's Summary "Danger zone". */}
						{!isOrchestrator && session && sessionIsActive(session) ? (
							<TopbarKillButton
								key={`kill-session:${session.id}`}
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
						{!isOrchestrator && (
							<TopbarButton
								aria-label={t("shell.openOrchestrator")}
								disabled={isSpawning || isProjectRestarting}
								onClick={() => void openOrchestrator()}
								style={noDragStyle}
								variant="primary"
							>
								<OrchestratorIcon className="size-icon-lg" aria-hidden="true" />
								{isProjectRestarting
									? t("shell.restarting")
									: isSpawning
										? t("shell.spawning")
										: t("shell.orchestrator")}
							</TopbarButton>
						)}
						{/* Inspector collapse (worker sessions only — orchestrators have no rail). */}
						{!isOrchestrator && (
							<TopbarButton
								aria-label={isInspectorOpen ? t("shell.closeInspector") : t("shell.openInspector")}
								aria-pressed={isInspectorOpen}
								onClick={handleToggleInspector}
								style={noDragStyle}
								title={isInspectorOpen ? t("shell.closeInspectorTitle") : t("shell.openInspectorTitle")}
								variant="icon"
							>
								{isInspectorOpen ? (
									<PanelRightClose className="size-5" aria-hidden="true" />
								) : (
									<PanelRightOpen className="size-5" aria-hidden="true" />
								)}
							</TopbarButton>
						)}
					</>
				) : null}
				{/* The bell always trails the actions row, on every platform. */}
				<NotificationCenter style={noDragStyle} />
			</div>
		</motion.header>
	</LayoutGroup>
	);
}

const projectBoardLabelTransition = { type: "spring" as const, stiffness: 400, damping: 40 };

/** Project name → board. Outer `<button>` owns the click so Motion's shared-layout spring cannot swallow the first pointer event (#3682). */
function ProjectBoardLabelButton({
	label,
	onOpen,
	style,
	className,
}: {
	label: string;
	onOpen: () => void;
	style?: React.CSSProperties;
	className?: string;
}) {
	const { t } = useTranslation();
	return (
		<button
			aria-label={t("shell.openKanban")}
			className={cn("min-w-0 rounded-sm text-left hover:opacity-80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring", className)}
			onClick={onOpen}
			style={style}
			type="button"
		>
			<motion.span
				layoutId="topbar-project-label"
				layout="position"
				className={topbarProjectLabelClass}
				transition={projectBoardLabelTransition}
			>
				{label}
			</motion.span>
		</button>
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
						title={t("shell.killSession")}
						variant="kill"
					>
						<Trash2 className="size-icon-lg" aria-hidden="true" />
						{isPending ? t("shell.killing") : t("shell.kill")}
					</TopbarButton>
				}
			/>
			{error ? <TopbarKillError>{error}</TopbarKillError> : null}
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
					<TopbarKillError className="max-w-48 truncate" key={state.session.id} title={state.error}>
						{state.session.title}: {state.error}
					</TopbarKillError>
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
	const { label, tone, breathe } = getAgentActivityView(session.activity, t);
	return (
		<StatusPill label={label} tone={tone} breathe={breathe} leading="none" className="px-3.5 py-2 text-sm" />
	);
}
