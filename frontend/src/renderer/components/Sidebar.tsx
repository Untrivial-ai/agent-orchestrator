import {
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams, useRouterState } from "@tanstack/react-router";
import {
	ChevronRight,
	Folder,
	FolderOpen,
	LogIn,
	LogOut,
	MoreVertical,
	Pencil,
	Pin,
	PinOff,
	Plus,
	RefreshCw,
	Search,
	Settings,
	Trash2,
	User,
} from "lucide-react";
import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactNode } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import type { UpdateStatus } from "../../main/update-settings";
import {
	hasConfiguredOrchestratorAgent,
	newestActiveOrchestrator,
	type WorkspaceSession,
	type WorkspaceSummary,
	sortedWorkerSessions,
	workerSessions,
} from "../types/workspace";
import { getAgentActivityView } from "../lib/session-presentation";
import { deriveSessionAgentSwitchPresentation } from "../lib/agent-switch-presentation";
import { aoBridge } from "../lib/bridge";
import { useCommandPaletteEnabled } from "../hooks/useCommandPaletteEnabled";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { usePinSession, useUnpinSession } from "../hooks/usePinSession";
import { spawnOrchestrator } from "../lib/spawn-orchestrator";
import { renameSession } from "../lib/rename-session";
import { useTerminateSession } from "../hooks/useTerminateSession";
import { useResizable } from "../hooks/useResizable";
import { useShellMaybe } from "../lib/shell-context";
import { useUpdateStatus } from "../hooks/useUpdateStatus";
import { effectiveShortcutBindings, shortcutBindingKeys } from "../../shared/shortcuts";
import {
	ContextMenu,
	ContextMenuContent,
	ContextMenuItem,
	ContextMenuSeparator,
	ContextMenuTrigger,
} from "./ui/context-menu";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import {
	Sidebar as SidebarRoot,
	SidebarContent,
	SidebarFooter,
	SidebarGroup,
	SidebarGroupContent,
	SidebarHeader,
	SidebarMenu,
	SidebarMenuButton,
	SidebarMenuItem,
	SidebarRail,
	SidebarMenuSub,
	SidebarMenuSubItem,
	useSidebar,
} from "./ui/sidebar";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { OrchestratorIcon } from "./icons";
import aoLogo from "../../../assets/ao-logo.svg";
import { cn } from "../lib/utils";
import { useUiStore } from "../stores/ui-store"
import { useKeybindingsStore } from "../stores/keybindings-store";
import { ConfirmDialog } from "./ConfirmDialog";
import { CreateProjectFlow, type CreateProjectInput } from "./CreateProjectFlow";
import { ResizeHandle } from "./ResizeHandle";
import { isMacPlatform, isWindowsPlatform } from "../lib/platform";
import { useCloudSession } from "../lib/cloud-session";

// macOS paints framed chrome: the fixed TitlebarNav cluster carries the
// sidebar toggle + history arrows above this surface. Windows hangs the sidebar
// under its custom titlebar.
const isMac = isMacPlatform();
const isWindows = isWindowsPlatform();
const noDragStyle = isMac ? ({ WebkitAppRegion: "no-drag" } as React.CSSProperties) : undefined;

// Shared styling for the per-project hover action buttons (orchestrator, kebab):
// a 20px square icon button that tints on hover, matching the old
// SidebarMenuAction footprint.
const HOVER_ACTION_CLASS =
	"grid size-5 shrink-0 place-items-center rounded-md text-passive transition-colors hover:text-foreground disabled:pointer-events-none disabled:opacity-50 data-[state=open]:text-foreground [&_svg]:size-icon-lg";

// Shared nav-row chrome (Codex-style): inset pill hover/selected, 14px type, no accent bar.
const NAV_ROW_CLASS =
	"h-9 gap-2.5 rounded-lg px-2.5 text-sm font-medium text-muted-foreground transition-[background-color,color] hover:bg-interactive-hover hover:text-foreground active:bg-interactive-hover active:text-foreground data-[active=true]:bg-interactive-active data-[active=true]:font-medium data-[active=true]:text-foreground";

// Search + Pinned/Projects section chrome: same type, icon, and row size.
const SECTION_ROW_CLASS =
	"flex h-8 w-full min-w-0 items-center gap-2 rounded-md px-2.5 text-sm font-medium text-passive [&_svg]:size-icon-md [&_svg]:shrink-0";
// Hover fill only for collapsible section headers (Pinned). Projects is a static label.
const SECTION_ROW_INTERACTIVE_CLASS = "transition-colors hover:bg-interactive-hover hover:text-foreground";

// Mirrors the daemon's display-name cap (maxDisplayNameLen) and the spawn
// `--name` flag, so inline edits never round-trip a value the API would reject.
const MAX_DISPLAY_NAME_LEN = 20;
export const SIDEBAR_DEFAULT_WIDTH = 240;
export const SIDEBAR_MIN_WIDTH = 200;
export const SIDEBAR_MAX_WIDTH = 420;

type SidebarProps = {
	/** Hide the sidebar's right edge stroke on the welcome board inset chrome. */
	hideEdgeBorder?: boolean;
	/** Render the expanded sidebar over content without reserving layout width. */
	isOverlay?: boolean;
	underTopbar?: boolean;
	/** Chrome height to clear when underTopbar is set. Defaults to --size-toolbar. */
	topbarOffset?: "toolbar" | "titlebar" | "trafficLights" | "session";
	onPreviewLeave?: () => void;
	workspaceError?: string;
	workspaces: WorkspaceSummary[];
	onCreateProject: (input: CreateProjectInput) => Promise<void>;
	onInitializeProject: (path: string) => Promise<void>;
	onRemoveProject: (projectId: string) => Promise<void>;
};

// Selection state comes from the URL: which project/session is active is the
// route params, and clicks navigate rather than mutate a store.
function useSelection() {
	const navigate = useNavigate();
	const openGlobalSettings = useUiStore((state) => state.openGlobalSettings);
	const openProjectSettings = useUiStore((state) => state.openProjectSettings);
	const params = useParams({ strict: false }) as { projectId?: string; sessionId?: string };
	const pathname = useRouterState({ select: (state) => state.location.pathname });
	return {
		isHome: pathname === "/",
		activeProjectId: params.projectId,
		activeSessionId: params.sessionId,
		goHome: () => void navigate({ to: "/" }),
		// Settings is a modal — open it in place so the current page (session
		// terminal, board, etc.) stays underneath.
		goGlobalSettings: () => openGlobalSettings(),
		goSettings: (projectId: string) => openProjectSettings(projectId),
		goProject: (projectId: string) => void navigate({ to: "/projects/$projectId", params: { projectId } }),
		goSession: (projectId: string, sessionId: string) =>
			void navigate({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId, sessionId } }),
	};
}

// Agent activity is the shared source for both color and motion. PR and CI
// state is presented on cards and board lanes instead of repainting this dot.
function SessionStatusDot({ session }: { session: WorkspaceSession }) {
	const activity = getAgentActivityView(session.activity);
	return (
		<span
			aria-hidden="true"
			className={cn("size-2 shrink-0 rounded-full", activity.indicatorClassName)}
			data-session-status=""
		/>
	);
}

// Built on shadcn's sidebar primitives (components/ui/sidebar): the provider in
// _shell owns the persistent open state plus a transient hover-preview state.
// Collapsed sidebars move fully off-canvas; previews return as overlays without
// reserving layout width.
export function Sidebar({
	hideEdgeBorder = false,
	isOverlay = false,
	underTopbar = true,
	topbarOffset = "toolbar",
	onPreviewLeave,
	workspaceError,
	workspaces,
	onCreateProject,
	onInitializeProject,
	onRemoveProject,
}: SidebarProps) {
	const { t } = useTranslation();
	const selection = useSelection();
	const { state, setOpen } = useSidebar();
	const isCollapsed = state === "collapsed";
	const [expandedChromeVisible, setExpandedChromeVisible] = useState(!isCollapsed);
	// One IPC subscription for both footer variants of the restart-to-update prompt.
	const updateStatus = useUpdateStatus();
	// Daemon status for the smoke suite's sr-only mirror in the footer. Null when
	// rendered outside the shell (unit tests) — the mirror simply doesn't render.
	const daemonStatus = useShellMaybe()?.daemonStatus ?? null;
	const commandPaletteEnabled = useCommandPaletteEnabled();
	const setCommandPaletteOpen = useUiStore((s) => s.setCommandPaletteOpen);

	useLayoutEffect(() => {
		// Offcanvas: the panel slides off-screen on collapse — no need to hide content.
		// Reveal immediately on expand so there's no fade-in delay.
		if (!isCollapsed) {
			setExpandedChromeVisible(true);
		}
	}, [isCollapsed]);

	// Disclosure state: projects are expanded by default; a project id present in
	// this set is collapsed (sessions hidden).
	const [collapsedIds, setCollapsedIds] = useState<ReadonlySet<string>>(() => new Set());
	const toggleCollapsed = (id: string) =>
		setCollapsedIds((prev) => {
			const next = new Set(prev);
			next.has(id) ? next.delete(id) : next.add(id);
			return next;
		});
	// Section disclosure: Pinned header collapses its body. Projects stays open.
	const [pinnedOpen, setPinnedOpen] = useState(true);
	// Fetch the running app version to derive the build channel. Channel is
	// identity: derived from the version string, not the update-channel setting
	// (the setting can be changed mid-session; the binary cannot).
	const { data: appVersion } = useQuery({
		queryKey: ["app-version"],
		queryFn: () => aoBridge.app.getVersion(),
		staleTime: Infinity,
	});
	const isNightly = typeof appVersion === "string" && appVersion.includes("-nightly.");

	// agent-orchestrator's sidebar resize: drag the right edge (200-420px,
	// persisted), double-click to reset to 240px. Drives --ao-sidebar-w on :root,
	// which the provider forwards into shadcn's --sidebar-width. Dragging clamps
	// at SIDEBAR_MIN_WIDTH — collapsing stays on the explicit toggle (⌘B /
	// titlebar button), never on a drag.
	const {
		onPointerDown: onResizePointerDown,
		onCollapsedPointerDown: onCollapsedResizePointerDown,
		onDoubleClick: onResizeDoubleClick,
	} = useResizable({
		cssVar: "--ao-sidebar-w",
		storageKey: "ao-sidebar-w",
		defaultWidth: SIDEBAR_DEFAULT_WIDTH,
		min: SIDEBAR_MIN_WIDTH,
		max: SIDEBAR_MAX_WIDTH,
		edge: "right",
		onExpand: () => setOpen(true),
	});

	const pinnedSessions = workspaces
		.flatMap((w) => workerSessions(w.sessions))
		.filter((s) => s.isPinned && s.isTerminated !== true)
		.sort((a, b) => {
			const aTime = a.pinnedAt ? new Date(a.pinnedAt).getTime() : 0;
			const bTime = b.pinnedAt ? new Date(b.pinnedAt).getTime() : 0;
			return bTime - aTime;
		});

	return (
		// Pinned sidebars start below shell chrome. Hover previews paint a
		// full-height surface behind the titlebar while their content keeps the
		// same chrome clearance, leaving the titlebar controls unobstructed.
		<SidebarRoot
			collapsible="offcanvas"
			data-expanded-chrome={expandedChromeVisible ? "visible" : "hidden"}
			data-topbar-offset={underTopbar ? topbarOffset : undefined}
			onPointerLeave={onPreviewLeave}
			overlay={isOverlay}
			className={cn(
				"sidebar-focusless",
				hideEdgeBorder ? "border-transparent" : "border-r-0 group-data-[side=left]:border-r-0",
				isOverlay && "z-sidebar-preview shadow-2xl",
				isOverlay || !underTopbar
					? "top-0 h-svh!"
					: "top-(--sidebar-chrome-offset) h-[calc(100svh-var(--sidebar-chrome-offset))]!",
			)}
		>
			<SidebarHeader
				className={cn(
					"gap-0 p-0 px-3 pt-2 group-data-[collapsible=icon]:px-1.5 group-data-[collapsible=icon]:pt-2",
					isOverlay && underTopbar && "pt-(--sidebar-chrome-offset)!",
				)}
			>
				{/* Brand (project-sidebar__brand); in the icon rail it becomes the old
            36px board button wrapping the 22px accent mark. */}
				<div
					className={cn(
						"flex shrink-0 items-center gap-1.5 px-0.5 group-data-[collapsible=icon]:flex-col group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:gap-1 group-data-[collapsible=icon]:px-0 group-data-[collapsible=icon]:pb-2",
						commandPaletteEnabled ? "pb-2" : "pb-3",
					)}
				>
					<Tooltip>
						<TooltipTrigger asChild>
							<button
								aria-label={t("shell.orchestratorBoard")}
								className={cn(
									"grid h-5.5 w-5.5 shrink-0 place-items-center",
									"group-data-[collapsible=icon]:size-control-board group-data-[collapsible=icon]:rounded-lg",
									selection.isHome
										? "group-data-[collapsible=icon]:bg-interactive-active"
										: "group-data-[collapsible=icon]:hover:bg-interactive-hover",
								)}
								onClick={selection.goHome}
								type="button"
							>
								<img src={aoLogo} alt="" aria-hidden="true" className="h-5.5 w-5.5 -translate-y-[3px] rounded-md object-cover" />
							</button>
						</TooltipTrigger>
						<TooltipContent side="right" hidden={state !== "collapsed"}>
							{t("shell.orchestratorBoard")}
						</TooltipContent>
					</Tooltip>
					{isWindows ? (
						<span
							aria-label={t("shell.orchestratorBoard")}
							className="sidebar-expanded-chrome min-w-0 flex-1 truncate text-sm font-bold leading-tight tracking-tight-lg text-foreground group-data-[collapsible=icon]:hidden"
							onClick={selection.goHome}
							onKeyDown={(event: KeyboardEvent<HTMLSpanElement>) => {
								if (event.key !== "Enter" && event.key !== " ") return;
								event.preventDefault();
								selection.goHome();
							}}
							role="button"
							tabIndex={0}
						>
							Agent Orchestrator
						</span>
					) : (
						<span className="sidebar-expanded-chrome min-w-0 flex-1 truncate text-sm font-bold leading-tight tracking-tight-lg text-foreground group-data-[collapsible=icon]:hidden">
							Agent Orchestrator
						</span>
					)}
					{isNightly && (
						<span className="sidebar-expanded-chrome shrink-0 rounded-full bg-purple-subtle px-1.5 py-0.5 text-micro font-semibold leading-none text-purple-accent group-data-[collapsible=icon]:hidden">
							{t("shell.nightly")}
						</span>
					)}
				</div>
			</SidebarHeader>

			{/* Keep Search + section chrome fixed; only the project tree scrolls. */}
			<div className="flex shrink-0 flex-col gap-0 px-2 group-data-[collapsible=icon]:items-center group-data-[collapsible=icon]:px-1.5">
				{commandPaletteEnabled ? (
					<SidebarGroup className="p-0 pb-4">
						<SidebarGroupContent>
							<SidebarMenu className="gap-0.5 group-data-[collapsible=icon]:gap-1">
								<SidebarSearchButton onOpen={() => setCommandPaletteOpen(true)} />
							</SidebarMenu>
						</SidebarGroupContent>
					</SidebarGroup>
				) : null}

				{/* Pinned — collapsible; hidden when empty. */}
				{pinnedSessions.length > 0 && (
					<div className="sidebar-expanded-chrome flex shrink-0 flex-col group-data-[collapsible=icon]:hidden">
						<SectionDisclosure
							label={t("shell.pinned")}
							open={pinnedOpen}
							onToggle={() => setPinnedOpen((v) => !v)}
							className="mb-1"
						/>
						{pinnedOpen ? (
							<SidebarMenuSub
								className="sidebar-expanded-chrome mx-0 ml-0 translate-x-0 gap-0.5 border-l-0 px-0 py-0.5 mb-2"
								data-testid="pinned-session-list"
							>
								{pinnedSessions.map((session) => (
									<SessionRow
										key={session.id}
										session={session}
										active={selection.activeSessionId === session.id}
										indented={false}
										onOpen={() => selection.goSession(session.workspaceId, session.id)}
									/>
								))}
							</SidebarMenuSub>
						) : null}
					</div>
				)}

				{/* Projects — always open; only the trailing "+" is interactive. */}
				<div className="sidebar-expanded-chrome flex shrink-0 pb-1.5 group-data-[collapsible=icon]:hidden">
					<SectionDisclosure
						label={t("shell.projects")}
						collapsible={false}
						trailing={
							<CreateProjectButton
								hideTrigger={workspaces.length === 0}
								onCreateProject={onCreateProject}
								onInitializeProject={onInitializeProject}
							/>
						}
					/>
				</div>
			</div>

			<SidebarContent className="project-sidebar-scrollbar gap-0 px-2 group-data-[collapsible=icon]:items-center group-data-[collapsible=icon]:px-1.5">
				<SidebarGroup className="p-0">
					{/* Tree (project-sidebar__tree) */}
					<SidebarGroupContent>
						{workspaceError ? (
							<div className="sidebar-expanded-chrome px-2.5 py-3 group-data-[collapsible=icon]:hidden">
								<p className="text-sm text-foreground">{t("shell.couldNotLoadProjects")}</p>
								<p className="mt-1 text-caption text-passive">{workspaceError}</p>
							</div>
						) : workspaces.length === 0 ? null : (
							<SidebarMenu className="gap-0.5 rounded-lg overflow-hidden group-data-[collapsible=icon]:gap-1 group-data-[collapsible=icon]:rounded-none group-data-[collapsible=icon]:overflow-visible">
								{workspaces.map((workspace) => (
									<ProjectItem
										key={workspace.id}
										workspace={workspace}
										expanded={!collapsedIds.has(workspace.id)}
										selection={selection}
										onToggle={() => toggleCollapsed(workspace.id)}
										onRemoveProject={onRemoveProject}
									/>
								))}
								{isCollapsed && <CreateProjectListItem />}
							</SidebarMenu>
						)}
					</SidebarGroupContent>
				</SidebarGroup>
			</SidebarContent>

			{/* Footer — Settings opens the global settings page directly.
			    Its hairline and row height match the board Archive bar. Bottom
			    margin matches the framed center-panel inset plus the 1px surface
			    border so the two hairlines meet. Native fullscreen drops the
			    mac inset, so the footer collapses to the 1px surface border. */}
			<SidebarFooter
				className={cn(
					"relative mt-auto gap-0 overflow-hidden border-t border-border-strong px-2 !py-2 transition-[padding] duration-200 ease-linear group-data-[collapsible=icon]:min-h-16 group-data-[collapsible=icon]:items-center group-data-[collapsible=icon]:border-t-0 group-data-[collapsible=icon]:px-1.5 group-data-[collapsible=icon]:!pb-0 group-data-[collapsible=icon]:!pt-1.5",
					isMac
						? "mb-[calc(var(--size-center-panel-inset-mac)+1px)] in-[.native-fullscreen]:mb-px"
						: "mb-[calc(var(--size-center-panel-bottom-inset)+1px)]",
				)}
			>
				{/* Always-present daemon status mirror for the smoke suite: no visible
				    daemon-state copy is guaranteed to be mounted elsewhere. */}
				{daemonStatus && (
					<span aria-hidden="true" className="sr-only" data-testid="daemon-status" data-state={daemonStatus.state}>
						daemon {daemonStatus.state}
					</span>
				)}
				<div
					aria-hidden={isCollapsed || undefined}
					className="sidebar-expanded-chrome relative flex w-full min-w-46.5 flex-col gap-0.5 transition-[opacity,transform] duration-150 ease-out group-data-[collapsible=icon]:pointer-events-none group-data-[collapsible=icon]:-translate-x-2 group-data-[collapsible=icon]:opacity-0"
				>
					<RestartToUpdateRow status={updateStatus} tabIndex={isCollapsed ? -1 : 0} />
					<CloudAccountRow tabIndex={isCollapsed ? -1 : 0} />
					<button
						aria-label={t("shell.settings")}
						className={cn(
							NAV_ROW_CLASS,
							"flex h-[42px] w-full items-center text-left [&_svg]:size-icon-md [&_svg]:shrink-0",
						)}
						onClick={() => selection.goGlobalSettings()}
						tabIndex={isCollapsed ? -1 : 0}
						type="button"
					>
						<Settings aria-hidden="true" />
						<span className="tracking-tight">{t("shell.settings")}</span>
					</button>
				</div>
				<div
					aria-hidden={!isCollapsed || undefined}
					className="pointer-events-none absolute inset-x-1.5 bottom-0 top-auto flex min-h-row-md flex-col items-center justify-end gap-1 opacity-0 transition-opacity duration-150 ease-out group-data-[collapsible=icon]:pointer-events-auto group-data-[collapsible=icon]:opacity-100"
				>
					<RestartToUpdateRailButton status={updateStatus} tabIndex={isCollapsed ? 0 : -1} />
					<CloudAccountRailButton tabIndex={isCollapsed ? 0 : -1} />
					<Tooltip>
						<TooltipTrigger asChild>
							<button
								aria-label={t("shell.settings")}
								className="grid size-control-board place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground [&_svg]:size-icon-base"
								onClick={() => selection.goGlobalSettings()}
								tabIndex={isCollapsed ? 0 : -1}
								type="button"
							>
								<Settings aria-hidden="true" />
							</button>
						</TooltipTrigger>
						<TooltipContent side="right">{t("shell.settings")}</TooltipContent>
					</Tooltip>
				</div>
			</SidebarFooter>

			<ResizeHandle
				className="group-data-[state=collapsed]:hidden"
				onDoubleClick={onResizeDoubleClick}
				onPointerDown={onResizePointerDown}
				side="right"
				style={noDragStyle}
			/>
			<SidebarRail
				aria-label={t("shell.expandSidebar")}
				className="group-data-[state=expanded]:hidden hover:after:bg-transparent"
				onClick={() => setOpen(true)}
				onPointerDown={onCollapsedResizePointerDown}
			/>
		</SidebarRoot>
	);
}

type Selection = ReturnType<typeof useSelection>;

function ProjectItem({
	workspace,
	expanded,
	selection,
	onToggle,
	onRemoveProject,
}: {
	workspace: WorkspaceSummary;
	expanded: boolean;
	selection: Selection;
	onToggle: () => void;
	onRemoveProject: (projectId: string) => Promise<void>;
}) {
	const { t } = useTranslation();
	const prefersReducedMotion = useReducedMotion();
	const activeProjectMatches = selection.activeProjectId === workspace.id;
	const dashboardActive = activeProjectMatches && !selection.activeSessionId;
	const orchestratorActive =
		activeProjectMatches &&
		workspace.sessions.some(
			(session) => session.id === selection.activeSessionId && session.kind === "orchestrator",
		);
	const projectActive = dashboardActive || orchestratorActive;
	const queryClient = useQueryClient();
	const [removeError, setRemoveError] = useState<string | null>(null);
	const [isRemoving, setIsRemoving] = useState(false);
	const [confirmOpen, setConfirmOpen] = useState(false);
	const [isSpawning, setIsSpawning] = useState(false);
	const [projectPressed, setProjectPressed] = useState(false);
	const [rowHovered, setRowHovered] = useState(false);
	// Skip enter animation on first mount — sessions arrive async and we don't
	// want them to slide in on every sidebar load. Only animate on subsequent
	// expand/collapse toggles.
	const [animReady, setAnimReady] = useState(false);
	useEffect(() => {
		const id = requestAnimationFrame(() => setAnimReady(true));
		return () => cancelAnimationFrame(id);
	}, []);
	const restartingProjectIds = useUiStore((state) => state.restartingProjectIds);
	const isProjectRestarting = restartingProjectIds.has(workspace.id);
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	// Keep completed PR sessions reachable while their runtime still exists.
	// Only termination removes a worker from the sidebar; archived sessions stay
	// reachable through SessionsBoard.
	const sessions = sortedWorkerSessions(workspace.sessions).filter((session) => session.isTerminated !== true);
	// The project's live orchestrator (if any) backs the hover Orchestrator
	// button: navigate to it when present, otherwise spawn one first.
	const orchestrator = newestActiveOrchestrator(workspace.sessions);

	// Mirrors ShellTopbar's launcher: attach to the running orchestrator, or
	// spawn one via the daemon and follow it once the workspace refetches.
	// Expand a collapsed project so opening the orchestrator also reveals its
	// session list — otherwise the tree stays shut while you're inside it.
	const openOrchestrator = async () => {
		if (isProjectRestarting) return;
		if (!expanded) onToggle();
		if (orchestrator) {
			selection.goSession(workspace.id, orchestrator.id);
			return;
		}
		if (!hasConfiguredOrchestratorAgent(workspace)) {
			selection.goSettings(workspace.id);
			return;
		}
		setIsSpawning(true);
		try {
			const sessionId = await spawnOrchestrator(workspace.id, "sidebar");
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			selection.goSession(workspace.id, sessionId);
		} catch (err) {
			console.error("Failed to spawn orchestrator:", err);
		} finally {
			setIsSpawning(false);
		}
	};

	// Expanded + already on the project board → collapse. Expanded + on a
	// session (orchestrator or worker) → board. Collapsed → expand + board.
	// Do not treat orchestratorActive like the board: the project row is the
	// one-click path back from the orchestrator button.
	const onProjectClick = () => {
		if (!expanded) {
			onToggle();
			selection.goProject(workspace.id);
		} else if (dashboardActive) {
			onToggle();
		} else {
			selection.goProject(workspace.id);
		}
	};

	// Folder icon always toggles disclosure, even when another project is
	// selected — without this, collapsing a non-active project required a
	// select click then a second click (felt like a double-click).
	const onFolderClick = (event: MouseEvent) => {
		event.stopPropagation();
		onToggle();
	};

	const onProjectKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
		if (event.key !== "Enter" && event.key !== " ") return;
		event.preventDefault();
		onProjectClick();
	};

	const removeProject = () => {
		setRemoveError(null);
		setConfirmOpen(true);
	};

	const handleConfirmRemove = async () => {
		setConfirmOpen(false);
		setIsRemoving(true);
		// Teardown can take a while when a project owns several sessions. Leave
		// the confirmation immediately and move to the route that remains valid
		// after removal while the sidebar keeps progress/error feedback visible.
		selection.goHome();
		try {
			await onRemoveProject(workspace.id);
		} catch (err) {
			const message = err instanceof Error ? err.message : t("shell.couldNotRemoveProject");
			setRemoveError(message);
		} finally {
			setIsRemoving(false);
		}
	};

	return (
		<ContextMenu>
		<ContextMenuTrigger asChild>
		<SidebarMenuItem
			className="group-data-[collapsible=icon]:mb-0"
			onMouseEnter={() => setRowHovered(true)}
			onMouseLeave={() => setRowHovered(false)}
		>
		{/* The whole visual row scales when its navigation surface is pressed.
		    Action-button presses stop before reaching this boundary. */}
		<div
			className={cn(
				"relative transition-[transform] duration-[100ms] ease-out",
				projectPressed && "scale-[0.98]",
			)}
			data-project-press=""
			onPointerCancel={() => setProjectPressed(false)}
			onPointerDown={() => setProjectPressed(true)}
			onPointerLeave={() => setProjectPressed(false)}
			onPointerUp={() => setProjectPressed(false)}
		>
		<div>
		{/* project-sidebar__proj-row */}
	<SidebarMenuButton
		aria-current={dashboardActive ? "page" : undefined}
		aria-expanded={expanded}
		isActive={projectActive}
		tooltip={workspace.name}
		onClick={onProjectClick}
		onKeyDown={onProjectKeyDown}
		className={cn(
			NAV_ROW_CLASS,
			// gap-2 matches SectionDisclosure so project icons/labels share the
			// Projects header's left edge (NAV_ROW defaults to gap-2.5).
			"gap-2 pr-sidebar-project-actions [&_svg]:size-icon-md",
			"group-data-[collapsible=icon]:size-control-board! group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:rounded-lg group-data-[collapsible=icon]:p-0! group-data-[collapsible=icon]:font-semibold",
		)}
	>
		{/* Expanded sidebar: visual folder/chevron icon (decorative — toggle button is a sibling).
		    size-icon-md matches the Projects section row; an 18px centered box was
		    optically indenting these icons relative to the header. */}
		<span
			aria-hidden="true"
			className="relative inline-flex size-icon-md shrink-0 translate-y-px items-center justify-center text-muted-foreground group-data-[collapsible=icon]:hidden"
			data-project-folder-visual=""
		>
			{rowHovered ? (
				<motion.span
					animate={{ rotate: expanded ? 90 : 0 }}
					initial={false}
					transition={prefersReducedMotion ? { duration: 0 } : { duration: 0.14, ease: [0.25, 0.46, 0.45, 0.94] }}
					className="inline-flex size-icon-md items-center justify-center"
				>
					<ChevronRight strokeWidth={1.75} />
				</motion.span>
			) : expanded ? (
				<FolderOpen strokeWidth={1.75} />
			) : (
				<Folder strokeWidth={1.75} />
			)}
		</span>
		{/* Collapsed icon rail: folder icon */}
		<span
			aria-hidden="true"
			className="hidden group-data-[collapsible=icon]:inline-flex size-8 items-center justify-center text-muted-foreground"
		>
			{expanded ? (
				<FolderOpen className="size-5" strokeWidth={1.75} />
			) : (
				<Folder className="size-5" strokeWidth={1.75} />
			)}
		</span>
		<span
			className="sidebar-expanded-chrome min-w-0 flex-1 translate-y-px truncate group-data-[collapsible=icon]:hidden"
			data-project-label=""
		>
			{workspace.name}
		</span>
	</SidebarMenuButton>
	{/* Folder disclosure toggle: sibling of the nav button, absolutely positioned over
	    the icon area so it intercepts clicks there without nesting buttons. */}
	<button
		aria-label={t("shell.toggleProject", { name: workspace.name })}
		aria-expanded={expanded}
		className="absolute inset-y-0 left-0 z-10 w-9 group-data-[collapsible=icon]:hidden focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring rounded-sm"
		data-project-folder=""
		onClick={onFolderClick}
		type="button"
	/>
		</div>
		{/* Per-project actions: orchestrator and kebab menu. Inside the scaled visual
		row, but outside its navigation surface so their own presses stay independent.
		Always visible (not hover-gated) to avoid CSS :hover group propagation in Chromium. */}
		<div
			className={cn(
				"sidebar-expanded-chrome absolute top-0 right-0.5 z-chrome flex h-control-form items-center gap-px",
				"group-data-[collapsible=icon]:hidden",
			)}
			data-project-actions=""
			onClick={(event) => event.stopPropagation()}
			onPointerDown={(event) => event.stopPropagation()}
		>
			<Tooltip>
				<TooltipTrigger asChild>
					<button
						aria-current={orchestratorActive ? "page" : undefined}
						aria-label={
							orchestrator
								? t("shell.openProjectOrchestrator", { name: workspace.name })
								: t("shell.spawnProjectOrchestrator", { name: workspace.name })
						}
						className={cn(HOVER_ACTION_CLASS, orchestratorActive && "text-foreground")}
						disabled={isSpawning || isProjectRestarting}
						onClick={() => void openOrchestrator()}
						type="button"
					>
						<OrchestratorIcon aria-hidden="true" strokeWidth={orchestratorActive ? 2.5 : 2} />
					</button>
				</TooltipTrigger>
				<TooltipContent>
					{isProjectRestarting
						? t("shell.restarting")
						: isSpawning
							? t("shell.spawning")
							: orchestrator
								? t("shell.orchestrator")
								: t("shell.spawnOrchestratorLower")}
				</TooltipContent>
			</Tooltip>
			<DropdownMenu>
				<DropdownMenuTrigger asChild>
					<button aria-label={t("shell.projectActions", { name: workspace.name })} className={HOVER_ACTION_CLASS} type="button">
						<MoreVertical aria-hidden="true" />
					</button>
				</DropdownMenuTrigger>
				<DropdownMenuContent side="right" align="start" className="min-w-44">
					<DropdownMenuItem disabled={isProjectRestarting} onSelect={() => requestNewTask(workspace.id)}>
						<Plus aria-hidden="true" />
						{t("shell.newSession")}
					</DropdownMenuItem>
					<DropdownMenuSeparator />
					<DropdownMenuItem onSelect={() => selection.goSettings(workspace.id)}>
						<Settings aria-hidden="true" />
						{t("shell.projectSettings")}
					</DropdownMenuItem>
					<DropdownMenuSeparator />
					<DropdownMenuItem
						className="text-destructive focus:text-destructive [&_svg]:text-destructive"
						disabled={isRemoving}
						onSelect={() => void removeProject()}
					>
						<Trash2 aria-hidden="true" />
						{t("shell.removeProjectTitle")}
					</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>
		</div>
		</div>{/* end outer relative */}
		{isRemoving ? (
			<div className="sidebar-expanded-chrome px-5 py-1 text-2xs text-muted-foreground" role="status">
				{t("shell.removingNamed", { name: workspace.name })}
			</div>
		) : removeError ? (
			<div className="sidebar-expanded-chrome px-5 py-1 text-2xs text-destructive" role="alert">
				{removeError}
			</div>
		) : null}
		{/* project-sidebar__sessions: indented under the project parent so worker
          sessions read as children without adding a persistent guide rail. */}
		<AnimatePresence initial={false}>
			{expanded && sessions.length > 0 && (
				<motion.div
					key="sessions"
					initial={animReady ? { height: 0 } : false}
					animate={{ height: "auto" }}
					exit={{ height: 0 }}
					transition={prefersReducedMotion ? { duration: 0 } : { duration: 0.14, ease: [0.25, 0.46, 0.45, 0.94] }}
					style={{ overflow: "hidden" }}
					className="sidebar-expanded-chrome"
				>
					<motion.div
						initial={animReady ? { y: -12, opacity: 0 } : false}
						animate={{ y: 0, opacity: 1 }}
						exit={{ y: -12, opacity: 0 }}
						transition={prefersReducedMotion ? { duration: 0 } : { duration: 0.14, ease: [0.25, 0.46, 0.45, 0.94] }}
					>
						<SidebarMenuSub className="mx-0 ml-3.5 translate-x-0 gap-px border-l-0 px-0 py-1">
							{sessions.map((session) => (
								<SessionRow
									key={session.id}
									session={session}
									active={selection.activeSessionId === session.id}
									onOpen={() => selection.goSession(workspace.id, session.id)}
								/>
							))}
						</SidebarMenuSub>
					</motion.div>
				</motion.div>
			)}
		</AnimatePresence>
		<ConfirmDialog
			open={confirmOpen}
			onOpenChange={setConfirmOpen}
			title={t("shell.removeProjectTitle")}
			description={
				<>
					<p className="text-sm font-medium text-foreground">
						{t("shell.removeProjectLead", { name: workspace.name })}
					</p>
					<p className="mt-1 text-xs text-muted-foreground">{t("shell.removeProjectBody")}</p>
				</>
			}
			confirmLabel={t("shell.remove")}
			destructive
			onConfirm={handleConfirmRemove}
		/>
		</SidebarMenuItem>
		</ContextMenuTrigger>
		<ContextMenuContent className="min-w-44">
			<ContextMenuItem disabled={isProjectRestarting} onSelect={() => requestNewTask(workspace.id)}>
				<Plus aria-hidden="true" />
				{t("shell.newSession")}
			</ContextMenuItem>
			<ContextMenuSeparator />
			<ContextMenuItem onSelect={() => selection.goSettings(workspace.id)}>
				<Settings aria-hidden="true" />
				{t("shell.projectSettings")}
			</ContextMenuItem>
			<ContextMenuSeparator />
			<ContextMenuItem
				className="text-destructive focus:text-destructive [&_svg]:text-destructive"
				disabled={isRemoving}
				onSelect={() => void removeProject()}
			>
				<Trash2 aria-hidden="true" />
				{t("shell.removeProjectTitle")}
			</ContextMenuItem>
		</ContextMenuContent>
		</ContextMenu>
	);
}

// One worker-session row. Reads as a link by default; a hover-revealed pencil
// flips the label into an inline input (Enter/blur saves, Escape cancels) that
// persists through the daemon rename endpoint, so the new name survives reload.
function SessionRow({
	session,
	active,
	indented = true,
	onOpen,
}: {
	session: WorkspaceSession;
	active: boolean;
	indented?: boolean;
	onOpen: () => void;
}) {
	const { t } = useTranslation();
	const switchPresentation = deriveSessionAgentSwitchPresentation(session);
	const switchLabel = switchPresentation
		? t(switchPresentation.compactLabelKey, switchPresentation.values)
		: undefined;
	const switchStatusId = useId();
	const [isEditing, setIsEditing] = useState(false);
	const [draft, setDraft] = useState(session.title);
	// Escape must not be swallowed by the blur-to-save path: the keydown handler
	// blurs the input, so it flags a cancel here for onBlur to honour.
	const cancelledRef = useRef(false);

	const queryClient = useQueryClient();
	const { mutate: pinSession } = usePinSession();
	const { mutate: unpinSession } = useUnpinSession();
	const { mutate: terminateSession, isPending: isKilling } = useTerminateSession();

	const startEditing = () => {
		setDraft(session.title);
		setIsEditing(true);
	};

	const commit = async () => {
		if (cancelledRef.current) {
			cancelledRef.current = false;
			setIsEditing(false);
			return;
		}
		setIsEditing(false);
		const name = draft.trim();
		if (!name || name === session.title) return;
		try {
			await renameSession(session.id, name);
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		} catch (err) {
			console.error("Failed to rename session:", err);
		}
	};

	if (isEditing) {
		return (
			<SidebarMenuSubItem className={cn(indented && "pl-4.5")}>
				<div className="relative flex h-8 w-full items-center gap-1.5 rounded-lg px-2.5 py-0">
					<SessionStatusDot session={session} />
					<input
						aria-label={t("shell.renameSession", { title: session.title })}
						autoFocus
						className="min-w-0 flex-1 rounded-xs border border-accent bg-transparent px-1 py-px text-sm text-foreground outline-none focus-visible:ring-1 focus-visible:ring-accent"
						maxLength={MAX_DISPLAY_NAME_LEN}
						onBlur={() => void commit()}
						onChange={(e) => setDraft(e.target.value)}
						onFocus={(e) => e.currentTarget.select()}
						onKeyDown={(e) => {
							if (e.key === "Enter") {
								e.preventDefault();
								e.currentTarget.blur();
							} else if (e.key === "Escape") {
								e.preventDefault();
								cancelledRef.current = true;
								e.currentTarget.blur();
							}
						}}
						value={draft}
					/>
				</div>
			</SidebarMenuSubItem>
		);
	}

	return (
		<SidebarMenuSubItem className={cn(indented && "pl-4.5")}>
			<div
				className={cn(
					"group/session-row flex h-8 w-full items-center rounded-lg transition-[background-color,color]",
					"hover:bg-interactive-hover hover:text-foreground focus-within:bg-interactive-hover",
					active && "bg-interactive-active text-foreground",
				)}
				data-session-row=""
			>
				{/* Scale wrapper — only around the open button so action buttons don't trigger press animation */}
				<div className="flex min-w-0 flex-1 transition-[transform] duration-[100ms] ease-out active:scale-[0.97]">
					<button
						aria-current={active ? "page" : undefined}
						aria-describedby={switchLabel ? switchStatusId : undefined}
						aria-label={t("shell.openSession", { title: session.title })}
						className="flex h-8 min-w-0 flex-1 items-center gap-1.5 rounded-lg px-2.5 py-0 text-left text-sm outline-hidden focus-visible:ring-2 focus-visible:ring-sidebar-ring"
						onClick={onOpen}
						type="button"
					>
						<SessionStatusDot session={session} />
						<span className="flex min-w-0 flex-1 items-center gap-1.5">
							<span
								className={cn(
									"min-w-0 flex-1 truncate transition-colors",
									active ? "text-foreground" : "text-muted-foreground group-hover/session-row:text-foreground",
								)}
							>
								{session.title}
							</span>
							{switchLabel ? (
								<span id={switchStatusId} className="max-w-28 shrink-0 truncate text-2xs text-muted-foreground">
									{switchLabel}
								</span>
							) : null}
						</span>
					</button>
				</div>{/* end scale wrapper */}
				{/* Pin, rename, kill: outside scale wrapper so clicking them doesn't trigger press animation */}
				<button
					aria-label={session.isPinned ? t("shell.unpinSession") : t("shell.pinSession")}
					className={cn(
						"grid h-5 w-0 shrink-0 place-items-center overflow-hidden rounded-md text-passive opacity-0",
						"transition-[width,margin,background-color,color,opacity] hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50 [&_svg]:size-3!",
						"group-hover/session-row:w-5 group-hover/session-row:opacity-100",
						"group-focus-within/session-row:w-5 group-focus-within/session-row:opacity-100",
						session.isPinned && "text-foreground",
					)}
					onClick={(e) => {
						e.stopPropagation();
						session.isPinned ? unpinSession(session) : pinSession(session);
					}}
					type="button"
				>
					{session.isPinned ? <PinOff aria-hidden="true" /> : <Pin aria-hidden="true" />}
				</button>
				<button
					aria-label={t("shell.renameSession", { title: session.title })}
					className={cn(
						"grid h-5 w-0 shrink-0 place-items-center overflow-hidden rounded-md text-passive opacity-0",
						"transition-[width,margin,background-color,color,opacity] hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50 [&_svg]:size-3!",
						"group-hover/session-row:mr-0.5 group-hover/session-row:w-5 group-hover/session-row:opacity-100",
						"group-focus-within/session-row:mr-0.5 group-focus-within/session-row:w-5 group-focus-within/session-row:opacity-100",
					)}
					onClick={(e) => {
						e.stopPropagation();
						startEditing();
					}}
					type="button"
				>
					<Pencil aria-hidden="true" />
				</button>
				<button
					aria-label={t("shell.killSession")}
					className={cn(
						"grid h-5 w-0 shrink-0 place-items-center overflow-hidden rounded-md text-passive opacity-0",
						"transition-[width,margin,background-color,color,opacity] hover:text-destructive focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50 [&_svg]:size-3!",
						"group-hover/session-row:mr-1 group-hover/session-row:w-5 group-hover/session-row:opacity-100",
						"group-focus-within/session-row:mr-1 group-focus-within/session-row:w-5 group-focus-within/session-row:opacity-100",
					)}
				disabled={isKilling}
				onClick={(e) => {
					e.stopPropagation();
					terminateSession(session);
				}}
					type="button"
				>
					<Trash2 aria-hidden="true" />
				</button>
			</div>
		</SidebarMenuSubItem>
	);
}

// CloudAccountRow: shown above the Settings button. When unauthenticated it
// starts the native WorkOS PKCE flow. When authenticated it shows the user's
// email with a sign-out action in a dropdown.
function CloudAccountRow({ tabIndex }: { tabIndex: number }) {
	const { t } = useTranslation();
	const { configured, session, status, signIn, signOut } = useCloudSession();
	if (!configured || status === "loading") return null;

	if (status === "unauthenticated") {
		return (
			<Tooltip>
				<TooltipTrigger asChild>
					<button
						aria-label={t("shell.signInToAOCloud")}
						className={cn(
							NAV_ROW_CLASS,
							"flex h-9 w-full items-center text-left [&_svg]:size-icon-md [&_svg]:shrink-0",
						)}
						onClick={() => signIn()}
						tabIndex={tabIndex}
						type="button"
					>
						<User aria-hidden="true" />
						<span className="tracking-tight">{t("shell.signIn")}</span>
					</button>
				</TooltipTrigger>
				<TooltipContent side="right" hidden={tabIndex !== -1}>
					{t("shell.signInToAOCloud")}
				</TooltipContent>
			</Tooltip>
		);
	}

	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<button
					aria-label={t("shell.signedInAs", { email: session?.user.email ?? "AO Cloud" })}
					className={cn(
						NAV_ROW_CLASS,
						"flex h-9 w-full items-center text-left [&_svg]:size-icon-md [&_svg]:shrink-0",
					)}
					tabIndex={tabIndex}
					type="button"
				>
					<User aria-hidden="true" />
					<span className="min-w-0 flex-1 truncate tracking-tight">
						{session?.user.email ?? "AO Cloud"}
					</span>
				</button>
			</DropdownMenuTrigger>
			<DropdownMenuContent side="top" align="start" className="min-w-44">
				<DropdownMenuItem
					className="text-destructive focus:text-destructive [&_svg]:text-destructive"
					onSelect={() => void signOut()}
				>
					<LogOut aria-hidden="true" />
					{t("shell.signOut")}
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

// Icon-rail variant for collapsed sidebar.
function CloudAccountRailButton({ tabIndex }: { tabIndex: number }) {
	const { t } = useTranslation();
	const { configured, session, status, signIn, signOut } = useCloudSession();
	if (!configured || status === "loading") return null;

	if (status === "unauthenticated") {
		return (
			<Tooltip>
				<TooltipTrigger asChild>
					<button
						aria-label={t("shell.signInToAOCloud")}
						className="grid size-control-board place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground [&_svg]:size-icon-base"
						onClick={() => signIn()}
						tabIndex={tabIndex}
						type="button"
					>
						<LogIn aria-hidden="true" />
					</button>
				</TooltipTrigger>
				<TooltipContent side="right">{t("shell.signInToAOCloud")}</TooltipContent>
			</Tooltip>
		);
	}

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					aria-label={t("shell.signedInAs", { email: session?.user.email ?? "AO Cloud" })}
					className="grid size-control-board place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground [&_svg]:size-icon-base"
					onClick={() => void signOut()}
					tabIndex={tabIndex}
					type="button"
				>
					<User aria-hidden="true" />
				</button>
			</TooltipTrigger>
			<TooltipContent side="right">
				{t("shell.signOutWithEmail", { email: session?.user.email ?? "AO Cloud" })}
			</TooltipContent>
		</Tooltip>
	);
}

// RestartToUpdateRow sits directly above the Settings row when an update is
// downloaded and staged. Transparent while fresh; orange (working tokens) once
// the main-process evaluator flags it escalated. Clicking installs immediately;
// the row itself is the prompt, so no confirmation dialog. Renders nothing in
// every other update state.
function RestartToUpdateRow({ status, tabIndex }: { status: UpdateStatus; tabIndex: number }) {
	const { t } = useTranslation();
	if (status.state !== "downloaded") return null;
	const escalated = status.escalated === true;
	return (
		<button
			aria-label={
				status.version
					? t("shell.restartInstallUpdateVersion", { version: status.version })
					: t("shell.restartInstallUpdate")
			}
			className={cn(
				"flex w-full items-center gap-2.5 rounded-lg p-2.5 text-left text-control font-medium transition-colors",
				escalated
					? "border border-working/35 bg-working/12 text-working hover:bg-working/18 [&_svg]:text-working"
					: "text-passive hover:bg-interactive-hover hover:text-foreground [&_svg]:text-passive",
			)}
			onClick={() => void aoBridge.updates.install()}
			tabIndex={tabIndex}
			type="button"
		>
			<RefreshCw aria-hidden="true" className="size-icon-lg shrink-0" />
			<span className="min-w-0 flex-1">
				<span className="block truncate tracking-tight">{t("shell.restartToUpdate")}</span>
				{status.version && (
					<span className={cn("block truncate text-caption font-normal", escalated ? "text-working" : "text-passive")}>
						{t("shell.versionReady", { version: status.version })}
					</span>
				)}
			</span>
			<span
				aria-hidden="true"
				className={cn("h-1.5 w-1.5 shrink-0 rounded-full", escalated ? "bg-working" : "bg-passive")}
			/>
		</button>
	);
}

// Icon-rail variant of RestartToUpdateRow for the collapsed sidebar: icon-only
// with the two-line copy in the tooltip.
function RestartToUpdateRailButton({ status, tabIndex }: { status: UpdateStatus; tabIndex: number }) {
	const { t } = useTranslation();
	if (status.state !== "downloaded") return null;
	const escalated = status.escalated === true;
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					aria-label={
						status.version
							? t("shell.restartInstallUpdateVersion", { version: status.version })
							: t("shell.restartInstallUpdate")
					}
					className={cn(
						"grid size-9 place-items-center rounded-lg transition-colors [&_svg]:size-4",
						escalated
							? "bg-working/12 text-working hover:bg-working/18"
							: "text-passive hover:bg-interactive-hover hover:text-foreground",
					)}
					onClick={() => void aoBridge.updates.install()}
					tabIndex={tabIndex}
					type="button"
				>
					<RefreshCw aria-hidden="true" />
				</button>
			</TooltipTrigger>
			<TooltipContent side="right">
				{t("shell.restartToUpdate")}
				{status.version ? ` · ${t("shell.versionReady", { version: status.version })}` : ""}
			</TooltipContent>
		</Tooltip>
	);
}

function SectionDisclosure({
	icon,
	label,
	open = true,
	onToggle,
	className,
	trailing,
	collapsible = true,
}: {
	icon?: ReactNode;
	label: string;
	open?: boolean;
	onToggle?: () => void;
	className?: string;
	/** Optional trailing control (e.g. Projects "+") — its own button, not the row. */
	trailing?: ReactNode;
	/** When false, render a static label row with no chevron, toggle, or hover fill. */
	collapsible?: boolean;
}) {
	const labelRow = (
		<>
			{icon}
			<span className="truncate">{label}</span>
			{collapsible ? (
				<ChevronRight
					aria-hidden="true"
					className={cn("size-3.5! shrink-0 transition-transform duration-150", open && "rotate-90")}
					strokeWidth={2}
				/>
			) : null}
		</>
	);

	if (!collapsible) {
		return (
			<div className={cn(SECTION_ROW_CLASS, trailing && "pr-1", className)}>
				<div className="flex min-w-0 flex-1 items-center gap-2">
					{labelRow}
				</div>
				{trailing}
			</div>
		);
	}

	if (trailing) {
		return (
			<div className={cn(SECTION_ROW_CLASS, SECTION_ROW_INTERACTIVE_CLASS, "pr-1", className)}>
				<button
					aria-expanded={open}
					aria-label={label}
					className="flex min-w-0 flex-1 items-center gap-2 text-left"
					onClick={onToggle}
					type="button"
				>
					{labelRow}
				</button>
				{trailing}
			</div>
		);
	}

	return (
		<button
			aria-expanded={open}
			aria-label={label}
			className={cn(SECTION_ROW_CLASS, SECTION_ROW_INTERACTIVE_CLASS, "text-left", className)}
			onClick={onToggle}
			type="button"
		>
			{labelRow}
		</button>
	);
}

function SidebarSearchButton({ onOpen }: { onOpen: () => void }) {
	const { t } = useTranslation();
	const { state } = useSidebar();
	const isCollapsed = state === "collapsed";
	const overrides = useKeybindingsStore((store) => store.overrides);
	const paletteBinding = effectiveShortcutBindings("command-palette", isMac, overrides)[0];
	const commandPaletteShortcutLabel = paletteBinding
		? shortcutBindingKeys(paletteBinding, isMac).join(isMac ? " " : "+")
		: "Unassigned";
	return (
		<SidebarMenuItem className="group-data-[collapsible=icon]:mb-0">
			<SidebarMenuButton
				aria-label={t("shell.search")}
				onClick={() => {
					// Open on the microtask after this click rather than inside it: mounting
					// the palette dialog while this button's tooltip layer is still tearing
					// down from the same pointer sequence dismissed it immediately. The
					// "defers opening" test pins the deferral so it is not dropped as noise.
					queueMicrotask(onOpen);
				}}
				tooltip={isCollapsed ? t("shell.search") : undefined}
				className={cn(
					// Filled search trigger (Cursor-style): icon + label.
					"h-8 gap-2 rounded-lg bg-muted px-2.5 text-sm font-normal text-muted-foreground",
					"transition-[background-color,color] duration-150 ease-out hover:bg-interactive-hover! hover:text-foreground active:bg-interactive-hover! [&_svg]:size-icon-sm!",
					"group-data-[collapsible=icon]:size-control-form! group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:rounded-lg group-data-[collapsible=icon]:bg-transparent group-data-[collapsible=icon]:p-0! group-data-[collapsible=icon]:hover:bg-interactive-hover!",
				)}
			>
				<Search strokeWidth={1.75} aria-hidden="true" />
				<span className="sidebar-expanded-chrome min-w-0 flex-1 truncate text-left leading-none group-data-[collapsible=icon]:hidden">
					{t("shell.search")}
				</span>
				<kbd className="sidebar-expanded-chrome ml-auto shrink-0 rounded-sm border border-border-strong/60 bg-surface/50 px-1.5 py-0.5 font-mono text-caption leading-none text-muted-foreground/80 group-data-[collapsible=icon]:hidden">
					{commandPaletteShortcutLabel}
				</kbd>
			</SidebarMenuButton>
		</SidebarMenuItem>
	);
}

function CreateProjectButton({
	hideTrigger = false,
	onCreateProject,
	onInitializeProject,
}: Pick<SidebarProps, "onCreateProject" | "onInitializeProject"> & { hideTrigger?: boolean }) {
	const { t } = useTranslation();
	// Single CreateProjectFlow owner for the sidebar: the header "+" stays mounted
	// (CSS-hidden when collapsed or on the empty start page) so it can own
	// openSignal for ⌘N on every shell route. The collapsed rail button below
	// reuses this flow via requestCreateProject().
	const createProjectNonce = useUiStore((state) => state.createProjectNonce);
	return (
		<CreateProjectFlow
			mode="choose"
			onCreateProject={onCreateProject}
			onInitializeProject={onInitializeProject}
			openSignal={createProjectNonce}
		>
			{({ disabled, choosePath, label }) => (
				<Tooltip>
					<TooltipTrigger asChild>
						<button
							aria-label={t("shell.newProject")}
							className={cn(
								"grid size-icon-xl shrink-0 place-items-center rounded-sm text-passive transition-colors hover:bg-interactive-hover hover:text-foreground",
								hideTrigger && "hidden",
							)}
							disabled={disabled}
							onClick={choosePath}
							type="button"
						>
							<Plus className="size-icon-sm" aria-hidden="true" />
						</button>
					</TooltipTrigger>
					<TooltipContent>{label}</TooltipContent>
				</Tooltip>
			)}
		</CreateProjectFlow>
	);
}

function CreateProjectListItem() {
	const { t } = useTranslation();
	const requestCreateProject = useUiStore((state) => state.requestCreateProject);
	return (
		<SidebarMenuItem className="mb-px group-data-[collapsible=icon]:mb-0">
			<Tooltip>
				<TooltipTrigger asChild>
					<button
						aria-label={t("shell.newProject")}
						className="grid h-control-board w-full place-items-center rounded-lg text-passive transition-colors hover:bg-interactive-hover hover:text-muted-foreground"
						onClick={() => requestCreateProject()}
						type="button"
					>
						<Plus className="size-icon-sm" aria-hidden="true" />
					</button>
				</TooltipTrigger>
				<TooltipContent side="right">{t("shell.newProject")}</TooltipContent>
			</Tooltip>
		</SidebarMenuItem>
	);
}
