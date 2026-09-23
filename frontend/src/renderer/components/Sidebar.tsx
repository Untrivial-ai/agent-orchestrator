import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { useNavigate, useParams, useRouterState } from "@tanstack/react-router";
import {
	DndContext,
	PointerSensor,
	closestCenter,
	useSensor,
	useSensors,
	type Modifier,
	type DragEndEvent,
} from "@dnd-kit/core";
import { SortableContext, useSortable, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
	AlertTriangle,
	ChevronRight,
	Download,
	Folder,
	FolderOpen,
	LogIn,
	LogOut,
	MoreVertical,
	PanelLeft,
	Pencil,
	Pin,
	PinOff,
	Plus,
	RefreshCw,
	Search,
	Settings,
	Smartphone,
	Trash2,
	User,
	X,
} from "lucide-react";
import {
	useCallback,
	useEffect,
	useId,
	useLayoutEffect,
	memo,
	useMemo,
	useRef,
	useState,
	type CSSProperties,
	type DragEvent as ReactDragEvent,
	type MouseEvent,
	type ReactNode,
	type RefObject,
} from "react";
import { flushSync } from "react-dom";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import type { UpdateStatus } from "../../main/update-settings";
import { parseNightlyVersion } from "../lib/build-channel";
import { IS_DEV } from "../lib/is-dev";
import {
	hasConfiguredOrchestratorAgent,
	newestActiveOrchestrator,
	openPRs,
	type WorkspaceSession,
	type WorkspaceSummary,
	sortedWorkerSessions,
	resolveNextNavigationAfterSessionKill,
	workerSessions,
	CLOUD_PROJECT_KIND,
	STANDALONE_PROJECT_KIND,
	STANDALONE_WORKSPACE_ID,
} from "../types/workspace";
import { getSessionStatusDotView } from "../lib/session-presentation";
import { deriveSessionAgentSwitchPresentation } from "../lib/agent-switch-presentation";
import { aoBridge } from "../lib/bridge";
import { useCommandPaletteEnabled } from "../hooks/useCommandPaletteEnabled";
import { useCanResumeAgent } from "../hooks/useCanResumeAgent";
import { cloudSessionsQueryKey, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { usePinSession, useUnpinSession } from "../hooks/usePinSession";
import { spawnCloudOrchestrator } from "../lib/cloud-orchestrator";
import { resumeOrchestrator, spawnOrchestrator } from "../lib/spawn-orchestrator";
import { formatTimeCompact, formatTimeTerse } from "../lib/format-time";
import { useTerminateSession } from "../hooks/useTerminateSession";
import { useResizable } from "../hooks/useResizable";
import { useCloudGate } from "../hooks/useCloudGate";
import { useCloudLocalAuth } from "../hooks/useCloudLocalAuth";
import { useLocalSignInDialogStore } from "../stores/local-signin-dialog-store";
import { useShellMaybe } from "../lib/shell-context";
import { useSidebarUpdateDismissal } from "../hooks/useSidebarUpdateDismissal";
import { useUpdateStatus } from "../hooks/useUpdateStatus";
import { MAX_SESSION_DISPLAY_NAME_LEN, useSessionRename } from "../hooks/useSessionRename";
import { effectiveShortcutBindings, shortcutBindingKeys } from "../../shared/shortcuts";
import {
	ContextMenu,
	ContextMenuContent,
	ContextMenuItem,
	ContextMenuTrigger,
} from "./ui/context-menu";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
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
import { Badge } from "./ui/badge";
import aoLogo from "../../../assets/ao-logo.svg";
import { cn } from "../lib/utils";
import { useUiStore } from "../stores/ui-store";
import { useKeybindingsStore } from "../stores/keybindings-store";
import { ConfirmDialog } from "./ConfirmDialog";
import { CreateProjectFlow, type CloneProjectInput, type CreateProjectInput } from "./CreateProjectFlow";
import { ResizeHandle } from "./ResizeHandle";
import { NAV_ROW_HIGHLIGHT_HOST_CLASS, NavRowHighlight } from "./NavRowHighlight";
import { isMacPlatform } from "../lib/platform";
import { useCloudSession } from "../lib/cloud-session";

// macOS paints framed chrome: the fixed TitlebarNav cluster carries the
// sidebar toggle + history arrows above this surface. Windows hangs the sidebar
// under its custom titlebar.
const isMac = isMacPlatform();
const noDragStyle = isMac ? ({ WebkitAppRegion: "no-drag" } as React.CSSProperties) : undefined;

// Shared styling for the per-project hover action buttons (orchestrator, kebab):
// a 20px square icon button that tints on hover, matching the old
// SidebarMenuAction footprint. Never painted — `.sidebar-icon-action` also
// opts out of the sidebar focus fill in styles.css.
const HOVER_ACTION_CLASS =
	"sidebar-icon-action grid size-5 shrink-0 place-items-center rounded-md !bg-transparent text-passive hover:!bg-transparent focus:!bg-transparent focus-visible:!bg-transparent active:!bg-transparent data-[state=open]:!bg-transparent hover:text-foreground disabled:pointer-events-none disabled:opacity-50 data-[state=open]:text-foreground [&_svg]:size-icon-lg";

// Session actions overlay the row without changing its footprint. The primary
// label only yields their width while the row is hovered or contains focus.
const SESSION_ACTION_CLASS =
	"sidebar-icon-action grid size-5 shrink-0 place-items-center rounded-md !bg-transparent p-1 text-passive hover:!bg-transparent focus:!bg-transparent focus-visible:!bg-transparent active:!bg-transparent data-[state=open]:!bg-transparent hover:text-foreground disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-3!";

// Shared nav-row chrome (Codex-style): inset pill, 14px type, no accent bar.
// Plain fill stays for non-interactive status rows; interactive rows use
// {@link NavRowHighlight} via {@link NAV_ROW_HIGHLIGHT_HOST_CLASS}.
const NAV_ROW_CLASS =
	"h-9 gap-2.5 rounded-lg px-2.5 text-sm font-medium text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground active:bg-interactive-hover active:text-foreground data-[active=true]:bg-interactive-active data-[active=true]:font-medium data-[active=true]:text-foreground";

/** Expanded footer action row: growing highlight behind icon + label. */
const FOOTER_NAV_BUTTON_CLASS = cn(
	NAV_ROW_CLASS,
	NAV_ROW_HIGHLIGHT_HOST_CLASS,
	"flex h-9 w-full items-center text-left transition-none",
);

/** Collapsed footer icon-rail control: same growing highlight in the square. */
const FOOTER_RAIL_BUTTON_CLASS = cn(
	NAV_ROW_HIGHLIGHT_HOST_CLASS,
	"grid size-control-board place-items-center rounded-lg text-muted-foreground [&_svg]:size-icon-base",
);

// Search + Pinned/Projects section chrome: same type, icon, and row size.
const SECTION_ROW_CLASS =
	"flex h-8 w-full min-w-0 items-center gap-2 rounded-md px-2.5 text-sm font-medium text-passive [&_svg]:size-icon-md [&_svg]:shrink-0";

// Mirrors the daemon's display-name cap (maxDisplayNameLen) and the spawn
// `--name` flag, so inline edits never round-trip a value the API would reject.

// Reorder drags start from the row's primary click surface. The 4px activation
// distance keeps a plain navigation/disclosure click from starting a drag;
// nested action buttons remain outside that activator surface.
const REORDER_ACTIVATION_DISTANCE = 4;
// A transparent 1x1 image replaces the browser's default drag ghost, so the
// dragged row stays put at reduced opacity instead of trailing under the cursor.
const EMPTY_DRAG_IMAGE = typeof Image === "undefined" ? null : new Image();
if (EMPTY_DRAG_IMAGE) EMPTY_DRAG_IMAGE.src = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7";

/** Stable drag-context id per project's session list. */
export const sessionDndId = (projectId: string) => `sidebar-sessions-${projectId}`;

function useReorderSensors() {
	return useSensors(
		useSensor(PointerSensor, {
			activationConstraint: { distance: REORDER_ACTIVATION_DISTANCE },
		}),
	);
}

// Browsers dispatch a click after pointerup even when dnd-kit has just completed
// a drag. Suppress only that same-turn synthetic click; if no click follows,
// clear the guard before the next user interaction.
function usePostDragClickGuard() {
	const guardedIdRef = useRef<string | null>(null);
	const clearTimerRef = useRef<number | null>(null);

	const markDragEnded = useCallback((id: string) => {
		guardedIdRef.current = id;
		if (clearTimerRef.current !== null) window.clearTimeout(clearTimerRef.current);
		clearTimerRef.current = window.setTimeout(() => {
			guardedIdRef.current = null;
			clearTimerRef.current = null;
		}, 0);
	}, []);

	const consumeClick = useCallback((id: string) => {
		if (guardedIdRef.current !== id) return false;
		guardedIdRef.current = null;
		if (clearTimerRef.current !== null) {
			window.clearTimeout(clearTimerRef.current);
			clearTimerRef.current = null;
		}
		return true;
	}, []);

	useEffect(() => () => {
		if (clearTimerRef.current !== null) window.clearTimeout(clearTimerRef.current);
	}, []);

	return useMemo(() => ({ consumeClick, markDragEnded }), [consumeClick, markDragEnded]);
}

type SortableRow = ReturnType<typeof useSortable>;

/** Session sorting stays vertical and never inherits dnd-kit's scale correction. */
function sortableRowStyle({ transform, transition, isDragging, dropTransitionDisabled }: Pick<SortableRow, "transform" | "transition" | "isDragging"> & { dropTransitionDisabled?: boolean }): CSSProperties {
	return {
		transform: transform ? CSS.Transform.toString({ ...transform, x: 0, scaleX: 1, scaleY: 1 }) : undefined,
		// The active row must clear its drag transform immediately on drop; its
		// siblings retain dnd-kit's smooth displacement while the pointer moves.
		transition: isDragging || dropTransitionDisabled ? "none" : (transition ?? "transform 180ms cubic-bezier(0.22, 1, 0.36, 1)"),
	};
}

// Session drags use their owning list as the visual boundary.
const restrictToListBounds: Modifier = ({ activeNodeRect, containerNodeRect, transform }) => {
	if (!activeNodeRect || !containerNodeRect) return transform;
	const minY = containerNodeRect.top - activeNodeRect.top;
	const maxY = containerNodeRect.bottom - activeNodeRect.bottom;
	return {
		...transform,
		x: 0,
		y: Math.min(maxY, Math.max(minY, transform.y)),
	};
};

type ProjectDropPlacement = "before" | "after";

function reorderAtProjectBoundary(
	ids: string[],
	activeId: string,
	targetId: string,
	placement: ProjectDropPlacement,
): string[] | null {
	if (activeId === targetId || !ids.includes(activeId) || !ids.includes(targetId)) return null;
	const next = ids.filter((id) => id !== activeId);
	const targetIndex = next.indexOf(targetId);
	next.splice(targetIndex + (placement === "after" ? 1 : 0), 0, activeId);
	return next.every((id, index) => id === ids[index]) ? null : next;
}

function reorderById(ids: string[], activeId: string, overId: string): string[] | null {
	if (activeId === overId) return null;
	const from = ids.indexOf(activeId);
	const to = ids.indexOf(overId);
	if (from < 0 || to < 0) return null;
	const next = [...ids];
	const [moved] = next.splice(from, 1);
	next.splice(to, 0, moved);
	return next;
}

function applyOrder<T>(items: readonly T[], idOf: (item: T) => string, order: readonly string[], unplaced: "start" | "end"): T[] {
	if (order.length === 0) return [...items];
	const byId = new Map(items.map((item) => [idOf(item), item]));
	const placed = order.flatMap((id) => {
		const item = byId.get(id);
		return item ? [item] : [];
	});
	const placedIds = new Set(order);
	const rest = items.filter((item) => !placedIds.has(idOf(item)));
	return unplaced === "start" ? [...rest, ...placed] : [...placed, ...rest];
}

function useGrabbingCursor(active: boolean) {
	useEffect(() => {
		if (!active) return;
		document.documentElement.classList.add("sidebar-reordering");
		return () => document.documentElement.classList.remove("sidebar-reordering");
	}, [active]);
}

export const SIDEBAR_DEFAULT_WIDTH = 240;
/** Floor/ceiling for sidebar resize — pass the same values to useResizable AND ResizeHandle. */
export const SIDEBAR_MIN_WIDTH = 200;
export const SIDEBAR_MAX_WIDTH = 420;
/** Cap each section's list until the user clicks Show more.
 *  One-way for now (no Show less / no persistence) — intentional first cut.
 *  Collapsed icon rail always shows the full list so projects stay reachable. */
const SIDEBAR_INITIAL_SECTION_LIMIT = 10;
/** Section bodies are capped scrollers, not fit-content: a long list scrolls
 *  inside its own section instead of pushing the section below it off-screen.
 *  Show more raises the cap and reveals the rest by scrolling. One row is
 *  h-control-form (32px) plus the list's 2px gap. */
const SIDEBAR_SECTION_ROW_HEIGHT = 34;
const SIDEBAR_EXPANDED_SECTION_ROWS = 16;
const SIDEBAR_SECTION_INITIAL_HEIGHT = SIDEBAR_INITIAL_SECTION_LIMIT * SIDEBAR_SECTION_ROW_HEIGHT;
const SIDEBAR_SECTION_EXPANDED_HEIGHT = SIDEBAR_EXPANDED_SECTION_ROWS * SIDEBAR_SECTION_ROW_HEIGHT;
const SECTION_SCROLLER_CLASS =
	"scrollbar-none overflow-y-auto overflow-x-hidden overscroll-contain group-data-[collapsible=icon]:overflow-visible";

/** The capped scroller's inline height, or none in the collapsed icon rail. */
function sectionScrollerStyle(isCollapsed: boolean, showAll: boolean): CSSProperties | undefined {
	if (isCollapsed) return undefined;
	return { maxHeight: showAll ? SIDEBAR_SECTION_EXPANDED_HEIGHT : SIDEBAR_SECTION_INITIAL_HEIGHT };
}
const expandedProjectsStorageKey = "ao.sidebar.expanded-projects";

function readExpandedProjectIds(): ReadonlySet<string> {
	if (typeof window === "undefined" || !window.localStorage) return new Set();
	try {
		const value: unknown = JSON.parse(window.localStorage.getItem(expandedProjectsStorageKey) ?? "null");
		return new Set(Array.isArray(value) ? value.filter((id): id is string => typeof id === "string") : []);
	} catch {
		return new Set();
	}
}

type SidebarProps = {
	/** Hide the sidebar's right edge stroke on the welcome board inset chrome. */
	hideEdgeBorder?: boolean;
	underTopbar?: boolean;
	/** Chrome height to clear when underTopbar is set. Defaults to --size-toolbar. */
	topbarOffset?: "toolbar" | "titlebar" | "trafficLights" | "session";
	workspaceError?: string;
	workspaces: WorkspaceSummary[];
	onCloneProject: (input: CloneProjectInput) => Promise<void>;
	onCreateProject: (input: CreateProjectInput) => Promise<void>;
	onInitializeProject: (path: string) => Promise<void>;
	onRemoveProject: (projectId: string, force?: boolean) => Promise<void>;
	/** Fixed shell chrome that also consumes the live sidebar width. */
	resizeAuxiliaryTargetRef?: RefObject<HTMLElement | null>;
};

// Selection state comes from the URL: which project/session is active is the
// route params, and clicks navigate rather than mutate a store.
function useSelection() {
	const navigate = useNavigate();
	const openGlobalSettings = useUiStore((state) => state.openGlobalSettings);
	const openProjectSettings = useUiStore((state) => state.openProjectSettings);
	const params = useParams({ strict: false }) as {
		projectId?: string;
		sessionId?: string;
	};
	const pathname = useRouterState({
		select: (state) => state.location.pathname,
	});
	const goHome = useCallback(() => void navigate({ to: "/" }), [navigate]);
	const goGlobalSettings = useCallback(() => openGlobalSettings(), [openGlobalSettings]);
	const goConnectMobile = useCallback(() => openGlobalSettings("mobile"), [openGlobalSettings]);
	const goSettings = useCallback((projectId: string) => openProjectSettings(projectId), [openProjectSettings]);
	const goProject = useCallback(
		(projectId: string) => void navigate({ to: "/projects/$projectId", params: { projectId } }),
		[navigate],
	);
	const goSession = useCallback(
		(projectId: string, sessionId: string) => {
			if (projectId === STANDALONE_WORKSPACE_ID) {
				void navigate({ to: "/sessions/$sessionId", params: { sessionId } });
				return;
			}
			void navigate({
				to: "/projects/$projectId/sessions/$sessionId",
				params: { projectId, sessionId },
			});
		},
		[navigate],
	);
	return useMemo(() => ({
		isHome: pathname === "/",
		activeProjectId: params.projectId,
		activeSessionId: params.sessionId,
		goHome,
		// Settings is a modal — open it in place so the current page (session
		// terminal, board, etc.) stays underneath.
		goGlobalSettings,
		goConnectMobile,
		goSettings,
		goProject,
		goSession,
	}), [goConnectMobile, goGlobalSettings, goHome, goProject, goSession, goSettings, params.projectId, params.sessionId, pathname]);
}

// Colour tracks the session's board section, preserving SCM state while the
// agent runs; motion stays on raw agent activity. A no-PR idle session turns
// blue when it starts working. See getSessionStatusDotView for the lane mapping.
function SessionStatusDot({ session }: { session: WorkspaceSession }) {
	const dot = getSessionStatusDotView(session);
	return (
		<span
			aria-hidden="true"
			className="relative z-[1] inline-flex shrink-0 items-center justify-center px-1.5"
		>
			<span
				className={cn(
					"size-2 rounded-full",
					dot.className,
					dot.breathe && "animate-status-pulse",
				)}
				data-session-status={session.status}
			/>
		</span>
	);
}

export {
	resolveNextNavigationAfterSessionKill,
	type NextSessionNavigation,
} from "../types/workspace";

// Built on shadcn's sidebar primitives (components/ui/sidebar): the provider in
// _shell owns the persistent open state. Collapsed sidebars move fully off-canvas.
export function Sidebar({
	hideEdgeBorder = false,
	underTopbar = true,
	topbarOffset = "toolbar",
	workspaceError,
	workspaces,
	onCloneProject,
	onCreateProject,
	onInitializeProject,
	onRemoveProject,
	resizeAuxiliaryTargetRef,
}: SidebarProps) {
	const { t } = useTranslation();
	const selection = useSelection();
	const { state, setOpen, toggleSidebar } = useSidebar();
	const isCollapsed = state === "collapsed";
	const [expandedChromeVisible, setExpandedChromeVisible] = useState(!isCollapsed);
	// One IPC subscription for both footer variants of the restart-to-update prompt.
	const updateStatus = useUpdateStatus();
	const availableUpdateVersion = updateStatus.state === "available" ? updateStatus.version : undefined;
	const updateDismissal = useSidebarUpdateDismissal(availableUpdateVersion);
	const openUpdateInstallPrompt = useUiStore((state) => state.openUpdateInstallPrompt);
	// Daemon status for the smoke suite's sr-only mirror in the footer. Null when
	// rendered outside the shell (unit tests) — the mirror simply doesn't render.
	const daemonStatus = useShellMaybe()?.daemonStatus ?? null;
	const commandPaletteEnabled = useCommandPaletteEnabled();
	const setCommandPaletteOpen = useUiStore((s) => s.setCommandPaletteOpen);
	const existingProjectPaths = useMemo(
		() => workspaces
			.filter((workspace) => workspace.kind !== STANDALONE_PROJECT_KIND)
			.map((workspace) => workspace.path)
			.filter((path): path is string => Boolean(path)),
		[workspaces],
	);
	const openExistingProject = useCallback(
		(path: string) => {
			const workspace = workspaces.find(
				(candidate) => candidate.kind !== STANDALONE_PROJECT_KIND && candidate.path === path,
			);
			if (workspace) selection.goProject(workspace.id);
		},
		[selection, workspaces],
	);
	const initialActiveSessionProjectId = useRef(
		selection.activeSessionId ? selection.activeProjectId : undefined,
	).current;
	useLayoutEffect(() => {
		// Offcanvas: the panel slides off-screen on collapse — no need to hide content.
		// Reveal immediately on expand so there's no fade-in delay.
		if (!isCollapsed) {
			setExpandedChromeVisible(true);
		}
	}, [isCollapsed]);

	// Disclosure state is persisted as the IDs of projects that were expanded.
	// An empty/missing store intentionally means all projects start collapsed.
	const [expandedIds, setExpandedIds] = useState<ReadonlySet<string>>(() => readExpandedProjectIds());
	const [dismissedInitialActiveProjectIds, setDismissedInitialActiveProjectIds] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	const toggleProjectDisclosure = useCallback((id: string) => {
		const routeFallbackActive =
			initialActiveSessionProjectId === id && !dismissedInitialActiveProjectIds.has(id);
		const currentlyExpanded = expandedIds.has(id) || routeFallbackActive;
		setExpandedIds((prev) => {
			const next = new Set(prev);
			currentlyExpanded ? next.delete(id) : next.add(id);
			if (typeof window !== "undefined") {
				window.localStorage?.setItem(expandedProjectsStorageKey, JSON.stringify([...next]));
			}
			return next;
		});
		setDismissedInitialActiveProjectIds((prev) => {
			if (initialActiveSessionProjectId !== id) return prev;
			const next = new Set(prev);
			currentlyExpanded ? next.add(id) : next.delete(id);
			return next;
		});
	}, [dismissedInitialActiveProjectIds, expandedIds, initialActiveSessionProjectId]);
	// Section disclosure: every section header collapses its own body.
	const [pinnedOpen, setPinnedOpen] = useState(true);
	const [projectsOpen, setProjectsOpen] = useState(true);
	const [scratchpadOpen, setScratchpadOpen] = useState(true);
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
	// only to the two layout consumers and fixed titlebar strip, rather than
	// :root. Dragging clamps
	// at SIDEBAR_MIN_WIDTH — collapsing stays on the explicit toggle (⌘B /
	// titlebar button), never on a drag.
	const resizeScopeRef = useRef<HTMLDivElement>(null);
	const getResizeTargets = useCallback(() => {
		const scope = resizeScopeRef.current;
		return [
			scope?.querySelector<HTMLElement>('[data-slot="sidebar-gap"]') ?? null,
			scope?.querySelector<HTMLElement>('[data-slot="sidebar-container"]') ?? null,
			resizeAuxiliaryTargetRef?.current ?? null,
		];
	}, [resizeAuxiliaryTargetRef]);
	// Stable getter — ResizeHandle keeps callbacks in refs; an inline arrow would
	// rebuild observers on every Sidebar render (daemon ticks / activity).
	const getSidebarBorderElement = useCallback(
		() =>
			resizeScopeRef.current?.querySelector<HTMLElement>('[data-slot="sidebar-container"]') ?? null,
		[],
	);
	const {
		onPointerDown: onResizePointerDown,
		onCollapsedPointerDown: onCollapsedResizePointerDown,
		onDoubleClick: onResizeDoubleClick,
	} = useResizable({
		cssVar: "--ao-sidebar-w",
		getCssTargets: getResizeTargets,
		storageKey: "ao-sidebar-w",
		defaultWidth: SIDEBAR_DEFAULT_WIDTH,
		min: SIDEBAR_MIN_WIDTH,
		max: SIDEBAR_MAX_WIDTH,
		edge: "right",
		onExpand: () => setOpen(true),
	});

	// Suppress layout animations for the first 500ms so background session
	// re-sorts during daemon settle don't cause visible row shuffling.
	const [layoutSettled, setLayoutSettled] = useState(false);
	useEffect(() => {
		const timer = window.setTimeout(() => setLayoutSettled(true), 500);
		return () => window.clearTimeout(timer);
	}, []);

	const [projectOrder, setProjectOrder] = useState<string[]>([]);
	const orderedWorkspaces = useMemo(
		() => applyOrder(workspaces, (workspace) => workspace.id, projectOrder, "end"),
		[projectOrder, workspaces],
	);
	// The ad hoc group is a bucket for projectless sessions, not a project: it
	// gets its own Scratchpad section below the project list rather than a
	// project row appended to the end of it.
	const projectWorkspaces = useMemo(
		() => orderedWorkspaces.filter((workspace) => workspace.kind !== STANDALONE_PROJECT_KIND),
		[orderedWorkspaces],
	);
	const standaloneWorkspace = useMemo(
		() => workspaces.find((workspace) => workspace.kind === STANDALONE_PROJECT_KIND),
		[workspaces],
	);
	const [showAllProjects, setShowAllProjects] = useState(false);
	const activeProjectBeyondLimit = useMemo(() => {
		if (showAllProjects || projectWorkspaces.length <= SIDEBAR_INITIAL_SECTION_LIMIT) return false;
		const activeId = selection.activeProjectId;
		if (!activeId) return false;
		const index = projectWorkspaces.findIndex((workspace) => workspace.id === activeId);
		return index >= SIDEBAR_INITIAL_SECTION_LIMIT;
	}, [projectWorkspaces, selection.activeProjectId, showAllProjects]);
	useEffect(() => {
		if (activeProjectBeyondLimit) setShowAllProjects(true);
	}, [activeProjectBeyondLimit]);
	const visibleWorkspaces = useMemo(
		() =>
			isCollapsed || showAllProjects || projectWorkspaces.length <= SIDEBAR_INITIAL_SECTION_LIMIT
				? projectWorkspaces
				: projectWorkspaces.slice(0, SIDEBAR_INITIAL_SECTION_LIMIT),
		[isCollapsed, projectWorkspaces, showAllProjects],
	);
	const hiddenProjectCount = Math.max(0, projectWorkspaces.length - SIDEBAR_INITIAL_SECTION_LIMIT);
	const projectIds = useMemo(
		() => projectWorkspaces.map((workspace) => workspace.id),
		[projectWorkspaces],
	);
	const projectDragClickGuard = usePostDragClickGuard();
	const [draggingProjectId, setDraggingProjectId] = useState<string | null>(null);
	// Keep the active id and drop target in refs so dragover can decide without a
	// full-list re-render while dragging: only the dragged row re-renders (via
	// `isDragged`), and the drop line updates through dedicated React state.
	const draggingProjectIdRef = useRef<string | null>(null);
	const projectDropTargetRef = useRef<{ overId: string; placement: ProjectDropPlacement } | null>(null);
	useGrabbingCursor(draggingProjectId !== null);
	// The drop line is a single element placed at the boundary's gap centre, so
	// "after A" and "before B" resolve to the same spot (no shift across the
	// boundary). Its top animates so the line slides between projects.
	const [dropLine, setDropLine] = useState<{ top: number; visible: boolean }>({ top: 0, visible: false });

	const clearProjectDropIndicator = useCallback(() => {
		projectDropTargetRef.current = null;
		setDropLine((previous) => ({ ...previous, visible: false }));
	}, []);

	const handleProjectDragStart = useCallback((event: ReactDragEvent<HTMLElement>, projectId: string) => {
		if (!projectIds.includes(projectId)) return;
		event.dataTransfer.effectAllowed = "move";
		// Some engines refuse to begin a drag unless the transfer carries data.
		event.dataTransfer.setData("text/plain", projectId);
		if (EMPTY_DRAG_IMAGE) event.dataTransfer.setDragImage(EMPTY_DRAG_IMAGE, 0, 0);
		draggingProjectIdRef.current = projectId;
		projectDropTargetRef.current = null;
		setDraggingProjectId(projectId);
	}, [projectIds]);

	const handleProjectDragEnd = useCallback(() => {
		const projectId = draggingProjectIdRef.current;
		if (projectId) projectDragClickGuard.markDragEnded(projectId);
		draggingProjectIdRef.current = null;
		clearProjectDropIndicator();
		setDraggingProjectId(null);
	}, [clearProjectDropIndicator, projectDragClickGuard]);

	const handleProjectDragOver = useCallback((event: ReactDragEvent<HTMLElement>, overId: string) => {
		const activeId = draggingProjectIdRef.current;
		if (!activeId || activeId === overId) {
			clearProjectDropIndicator();
			return;
		}
		const row = event.currentTarget;
		const rect = row.getBoundingClientRect();
		const placement: ProjectDropPlacement = event.clientY <= rect.top + rect.height / 2 ? "before" : "after";
		if (reorderAtProjectBoundary(projectIds, activeId, overId, placement) === null) {
			clearProjectDropIndicator();
			return;
		}
		// Only invite the drop once this is a real reorder target.
		event.preventDefault();
		event.dataTransfer.dropEffect = "move";
		const target = projectDropTargetRef.current;
		if (target?.overId === overId && target.placement === placement) return;
		projectDropTargetRef.current = { overId, placement };
		const rows = row.parentElement
			? Array.from(row.parentElement.querySelectorAll<HTMLElement>(":scope > [data-project-drop-target]"))
			: [row];
		const index = rows.indexOf(row);
		const insertAt = placement === "before" ? index : index + 1;
		const last = rows[rows.length - 1];
		const top =
			insertAt <= 0
				? rows[0].offsetTop
				: insertAt >= rows.length
					? last.offsetTop + last.offsetHeight
					: (rows[insertAt - 1].offsetTop + rows[insertAt - 1].offsetHeight + rows[insertAt].offsetTop) / 2;
		setDropLine({ top, visible: true });
	}, [clearProjectDropIndicator, projectIds]);

	const handleProjectDrop = useCallback((event: ReactDragEvent<HTMLElement>) => {
		const activeId = draggingProjectIdRef.current;
		const target = projectDropTargetRef.current;
		if (!activeId || !target) return;
		event.preventDefault();
		const next = reorderAtProjectBoundary(projectIds, activeId, target.overId, target.placement);
		if (next) setProjectOrder(next);
		handleProjectDragEnd();
	}, [handleProjectDragEnd, projectIds]);

	const pinnedSessions = useMemo(
		() => workspaces
			.flatMap((w) => workerSessions(w.sessions))
			.filter((s) => s.isPinned && s.isTerminated !== true)
			.sort((a, b) => {
				const aTime = a.pinnedAt ? new Date(a.pinnedAt).getTime() : 0;
				const bTime = b.pinnedAt ? new Date(b.pinnedAt).getTime() : 0;
				return bTime - aTime;
			}),
		[workspaces],
	);

	const handlePinnedSessionKilled = useCallback(
		(killedSession: WorkspaceSession) => {
			if (selection.activeSessionId !== killedSession.id) return;
			const workspace = workspaces.find((w) => w.id === killedSession.workspaceId);
			const nextRoute = resolveNextNavigationAfterSessionKill(workspace, killedSession.id);
			if (nextRoute.target === "session") {
				selection.goSession(killedSession.workspaceId, nextRoute.sessionId);
			} else {
				selection.goProject(killedSession.workspaceId);
			}
		},
		[selection, workspaces],
	);

	return (
		// Pinned sidebars start below shell chrome.
		<SidebarRoot
			collapsible="offcanvas"
			resizeScopeRef={resizeScopeRef}
			data-expanded-chrome={expandedChromeVisible ? "visible" : "hidden"}
			data-topbar-offset={underTopbar ? topbarOffset : undefined}
			className={cn(
				"sidebar-focusless",
				hideEdgeBorder ? "border-transparent" : "border-r-0 group-data-[side=left]:border-r-0",
				// Prefer top/bottom over h-svh/inset-y so titlebar offset (`top-(--sidebar-chrome-offset)`)
				// clears chrome without fighting a second height constraint.
				!underTopbar
					? "top-0 bottom-0"
					: "top-(--sidebar-chrome-offset) bottom-0 h-auto!",
			)}
		>
			<SidebarHeader className="gap-0 p-0 px-3 pt-2 group-data-[collapsible=icon]:px-1.5 group-data-[collapsible=icon]:pt-2">
				{/*
				 * Brand → home. Design contracts (do not regress):
				 * - Click navigates home; do NOT add hover/focus *fill* (styles.css
				 *   opts `[data-sidebar-brand]` out of `.sidebar-focusless` wash).
				 * - Keyboard focus uses the dedicated outline rule in styles.css —
				 *   never `focus-visible:outline-none` (global kill would leave it blind).
				 * - No separate "home" affordance on the mark — the whole brand is the control.
				 */}
				<button
					aria-label={t("shell.goHome")}
					className={cn(
						"group/brand flex w-full shrink-0 items-center gap-1.5 rounded-md px-0.5 text-left",
						"group-data-[collapsible=icon]:flex-col group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:gap-1 group-data-[collapsible=icon]:px-0 group-data-[collapsible=icon]:pb-2",
						commandPaletteEnabled ? "pb-2" : "pb-3",
					)}
					data-sidebar-brand=""
					onClick={() => selection.goHome()}
					type="button"
				>
					<span
						className={cn(
							"grid h-5.5 w-5.5 shrink-0 place-items-center",
							"group-data-[collapsible=icon]:size-control-board group-data-[collapsible=icon]:rounded-lg",
						)}
					>
						<img src={aoLogo} alt="" aria-hidden="true" className="h-5.5 w-5.5 -translate-y-[3px] rounded-md object-cover" />
					</span>
					<span
						className="sidebar-expanded-chrome min-w-0 flex-1 truncate text-sm font-bold leading-tight tracking-tight-lg text-foreground group-data-[collapsible=icon]:hidden"
					>
						Agent Orchestrator
					</span>
					{isNightly && (
						<span className="sidebar-expanded-chrome shrink-0 rounded-full bg-purple-subtle px-1.5 py-0.5 text-micro font-semibold leading-none text-purple-accent group-data-[collapsible=icon]:hidden">
							{t("shell.nightly")}
						</span>
					)}
					{IS_DEV && (
						<span
							data-testid="sidebar-dev-badge"
							className="sidebar-expanded-chrome shrink-0 rounded-full bg-amber-500/15 px-1.5 py-0.5 text-micro font-semibold leading-none text-amber-600 group-data-[collapsible=icon]:hidden dark:text-amber-400"
						>
							{t("shell.dev")}
						</span>
					)}
				</button>
				<Tooltip>
					<TooltipTrigger asChild>
						<button
							aria-label={isCollapsed ? t("shell.expandSidebar") : t("shell.collapseSidebar")}
							className="hidden size-control-board place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground group-data-[collapsible=icon]:grid [&_svg]:size-icon-base"
							onClick={toggleSidebar}
							type="button"
						>
							<PanelLeft aria-hidden="true" />
						</button>
					</TooltipTrigger>
					<TooltipContent side="right">
						{isCollapsed ? t("shell.expandSidebar") : t("shell.collapseSidebar")}
					</TooltipContent>
				</Tooltip>
			</SidebarHeader>

			{/* Keep Search + section chrome fixed; each section's list scrolls
			    inside its own capped body below. */}
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
								<PinnedSessionRow
									key={session.id}
									session={session}
									active={selection.activeSessionId === session.id}
									layoutSettled={layoutSettled}
									onKilled={handlePinnedSessionKilled}
									onOpenSession={selection.goSession}
								/>
								))}
							</SidebarMenuSub>
						) : null}
					</div>
				)}

				{/* Projects — collapsible; the "+" stays mounted through the empty
				    state so it keeps owning the ⌘N create flow. */}
				<div className="sidebar-expanded-chrome flex shrink-0 pb-0.5 group-data-[collapsible=icon]:hidden">
					<SectionDisclosure
						label={t("shell.projects")}
						open={projectsOpen}
						onToggle={() => setProjectsOpen((open) => !open)}
						trailing={
							<CreateProjectButton
								existingProjectPaths={existingProjectPaths}
								hideTrigger={workspaces.length === 0}
								onCloneProject={onCloneProject}
								onCreateProject={onCreateProject}
								onInitializeProject={onInitializeProject}
								onOpenExistingProject={openExistingProject}
							/>
						}
					/>
				</div>
			</div>

			<SidebarContent className="scrollbar-none gap-0 px-2 group-data-[collapsible=icon]:items-center group-data-[collapsible=icon]:px-1.5">
				<SidebarGroup className="min-h-full p-0">
					{/* Tree (project-sidebar__tree) */}
					<SidebarGroupContent className="min-h-full">
						{workspaceError ? (
							<div className="sidebar-expanded-chrome px-2.5 py-3 group-data-[collapsible=icon]:hidden">
								<p className="text-sm text-foreground">{t("shell.couldNotLoadProjects")}</p>
								<p className="mt-1 text-caption text-passive">{workspaceError}</p>
							</div>
						) : workspaces.length === 0 ? null : (
							<>
								{projectsOpen || isCollapsed ? (
									<div
										className={SECTION_SCROLLER_CLASS}
										data-testid="sidebar-projects-scroller"
										style={sectionScrollerStyle(isCollapsed, showAllProjects)}
									>
										<SidebarMenu className="relative gap-0.5 rounded-lg group-data-[collapsible=icon]:gap-1 group-data-[collapsible=icon]:rounded-none">
											{visibleWorkspaces.map((workspace) => (
												<ProjectItem
													key={workspace.id}
													workspace={workspace}
													expanded={expandedIds.has(workspace.id) || (initialActiveSessionProjectId === workspace.id && !dismissedInitialActiveProjectIds.has(workspace.id))}
													suppressInitialExpandAnimation={expandedIds.has(workspace.id)}
													selection={selection}
													isDragged={draggingProjectId === workspace.id}
													projectDragInProgress={draggingProjectId !== null}
													layoutSettled={layoutSettled}
													consumeDragClick={projectDragClickGuard.consumeClick}
													onToggle={toggleProjectDisclosure}
													onRemoveProject={onRemoveProject}
													onProjectDragStart={handleProjectDragStart}
													onProjectDragEnd={handleProjectDragEnd}
													onProjectDragOver={handleProjectDragOver}
													onProjectDrop={handleProjectDrop}
												/>
											))}
											{isCollapsed && <CreateProjectListItem />}
											<div
												aria-hidden="true"
												data-project-drop-line=""
												className="pointer-events-none absolute inset-x-0 z-[70] h-px rounded-full bg-foreground transition-opacity duration-100"
												style={{ top: dropLine.top, opacity: dropLine.visible ? 1 : 0 }}
											/>
										</SidebarMenu>
									</div>
								) : null}
								{/* Outside the scroller so the cap never hides its own release. */}
								{projectsOpen && !isCollapsed && !showAllProjects && hiddenProjectCount > 0 ? (
									<ShowMoreRow
										label={t("shell.showMoreProjects", { count: hiddenProjectCount })}
										onClick={() => setShowAllProjects(true)}
									/>
								) : null}
								{standaloneWorkspace ? (
									<ScratchpadSection
										workspace={standaloneWorkspace}
										selection={selection}
										isCollapsed={isCollapsed}
										layoutSettled={layoutSettled}
										open={scratchpadOpen}
										onToggle={() => setScratchpadOpen((open) => !open)}
									/>
								) : null}
							</>
						)}
					</SidebarGroupContent>
				</SidebarGroup>
			</SidebarContent>

			{/* Footer — Settings opens the global settings page directly.
			    Footer rows share NAV_ROW height so Settings, Connect mobile,
			    and account actions line up. Bottom spacing stays inside the
			    footer so there is no empty strip beneath the final action. */}
			<SidebarFooter
				className="relative mt-auto gap-0 overflow-hidden border-t border-border-strong px-2 !py-2 transition-[padding] duration-200 ease-linear group-data-[collapsible=icon]:min-h-20 group-data-[collapsible=icon]:items-center group-data-[collapsible=icon]:border-t-0 group-data-[collapsible=icon]:overflow-visible group-data-[collapsible=icon]:px-1.5 group-data-[collapsible=icon]:!pb-2 group-data-[collapsible=icon]:!pt-1.5"
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
					hidden={isCollapsed}
					className="sidebar-expanded-chrome relative flex w-full min-w-46.5 flex-col gap-0.5"
				>
					<UpdateStatusRow
						availableDismissed={updateDismissal.dismissed}
						onDismissAvailable={updateDismissal.dismiss}
						status={updateStatus}
						tabIndex={isCollapsed ? -1 : 0}
					/>
					<CloudSignInRow tabIndex={isCollapsed ? -1 : 0} />
					<CloudAccountRow tabIndex={isCollapsed ? -1 : 0} />
					<UpdateInstallSlide
						availableDismissed={updateDismissal.dismissed}
						onRequestInstall={openUpdateInstallPrompt}
						status={updateStatus}
						tabIndex={isCollapsed ? -1 : 0}
					/>
					<button
						aria-label={t("settings.connectMobile")}
						className={FOOTER_NAV_BUTTON_CLASS}
						onClick={() => selection.goConnectMobile()}
						tabIndex={isCollapsed ? -1 : 0}
						type="button"
					>
						<NavRowHighlight />
						<span className="relative z-[1] flex min-w-0 flex-1 items-center gap-2.5 [&_svg]:size-icon-md [&_svg]:shrink-0">
							<Smartphone aria-hidden="true" />
							<span className="tracking-tight">{t("settings.connectMobile")}</span>
						</span>
					</button>
					<button
						aria-label={t("shell.settings")}
						className={FOOTER_NAV_BUTTON_CLASS}
						onClick={() => selection.goGlobalSettings()}
						tabIndex={isCollapsed ? -1 : 0}
						type="button"
					>
						<NavRowHighlight />
						<span className="relative z-[1] flex min-w-0 flex-1 items-center gap-2.5 [&_svg]:size-icon-md [&_svg]:shrink-0">
							<Settings aria-hidden="true" />
							<span className="tracking-tight">{t("shell.settings")}</span>
						</span>
					</button>
				</div>
				<div
					aria-hidden={!isCollapsed || undefined}
					className="pointer-events-none absolute inset-x-1.5 bottom-0 top-auto flex min-h-row-md flex-col items-center justify-end gap-1 opacity-0 transition-opacity duration-150 ease-out group-data-[collapsible=icon]:pointer-events-auto group-data-[collapsible=icon]:!bottom-2 group-data-[collapsible=icon]:opacity-100"
				>
					<UpdateStatusRail
						availableDismissed={updateDismissal.dismissed}
						onRequestInstall={openUpdateInstallPrompt}
						status={updateStatus}
						tabIndex={isCollapsed ? 0 : -1}
					/>
					<CloudSignInRailButton tabIndex={isCollapsed ? 0 : -1} />
					<CloudAccountRailButton tabIndex={isCollapsed ? 0 : -1} />
					<Tooltip>
						<TooltipTrigger asChild>
							<button
								aria-label={t("settings.connectMobile")}
								className={FOOTER_RAIL_BUTTON_CLASS}
								onClick={() => selection.goConnectMobile()}
								tabIndex={isCollapsed ? 0 : -1}
								type="button"
							>
								<NavRowHighlight />
								<span className="relative z-[1] grid place-items-center [&_svg]:size-icon-base">
									<Smartphone aria-hidden="true" />
								</span>
							</button>
						</TooltipTrigger>
						<TooltipContent side="right">{t("settings.connectMobile")}</TooltipContent>
					</Tooltip>
					<Tooltip>
						<TooltipTrigger asChild>
							<button
								aria-label={t("shell.settings")}
								className={FOOTER_RAIL_BUTTON_CLASS}
								onClick={() => selection.goGlobalSettings()}
								tabIndex={isCollapsed ? 0 : -1}
								type="button"
							>
								<NavRowHighlight />
								<span className="relative z-[1] grid place-items-center [&_svg]:size-icon-base">
									<Settings aria-hidden="true" />
								</span>
							</button>
						</TooltipTrigger>
						<TooltipContent side="right">{t("shell.settings")}</TooltipContent>
					</Tooltip>
				</div>
			</SidebarFooter>

			{/* Grip follows the painted sidebar-container edge; useResizable owns clamp. */}
			<ResizeHandle
				className="group-data-[state=collapsed]:hidden"
				getBorderElement={getSidebarBorderElement}
				getObserveElements={getResizeTargets}
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

type ProjectRemoveBlocker = { class?: string; reason?: string };

// readProjectRemoveBlockers extracts the structured blockers the daemon
// attaches to PROJECT_REMOVE_BLOCKED error details. Returns [] when details
// are missing or malformed so the force dialog still opens with copy only.
function readProjectRemoveBlockers(err: unknown): ProjectRemoveBlocker[] {
	const details = (err as { details?: unknown }).details;
	if (typeof details !== "object" || details === null) return [];
	const blockers = (details as { blockers?: unknown }).blockers;
	if (!Array.isArray(blockers)) return [];
	return blockers.flatMap((item): ProjectRemoveBlocker[] => {
		if (typeof item !== "object" || item === null) return [];
		const record = item as { class?: unknown; reason?: unknown };
		return [{
			class: typeof record.class === "string" ? record.class : undefined,
			reason: typeof record.reason === "string" ? record.reason : undefined,
		}];
	});
}

type ProjectItemProps = {
	workspace: WorkspaceSummary;
	expanded: boolean;
	selection: Selection;
	isDragged: boolean;
	projectDragInProgress: boolean;
	consumeDragClick: (id: string) => boolean;
	layoutSettled: boolean;
	onToggle: (projectId: string) => void;
	onRemoveProject: (projectId: string, force?: boolean) => Promise<void>;
	suppressInitialExpandAnimation: boolean;
	onProjectDragStart: (event: ReactDragEvent<HTMLElement>, projectId: string) => void;
	onProjectDragEnd: () => void;
	onProjectDragOver: (event: ReactDragEvent<HTMLElement>, overId: string) => void;
	onProjectDrop: (event: ReactDragEvent<HTMLElement>) => void;
};

const ProjectItem = memo(function ProjectItem({
	workspace,
	expanded,
	selection,
	isDragged,
	projectDragInProgress,
	consumeDragClick,
	layoutSettled,
	onToggle,
	onRemoveProject,
	suppressInitialExpandAnimation,
	onProjectDragStart,
	onProjectDragEnd,
	onProjectDragOver,
	onProjectDrop,
}: ProjectItemProps) {
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
	const [forceConfirmOpen, setForceConfirmOpen] = useState(false);
	// Structured blockers from PROJECT_REMOVE_BLOCKED so the force confirmation
	// can show *why* AO refused (dirty worktree, live process, open shell, …)
	// instead of a single generic warning.
	const [forceBlockers, setForceBlockers] = useState<Array<{ class?: string; reason?: string }>>([]);
	const [isSpawning, setIsSpawning] = useState(false);
	// Skip enter animation on first mount — sessions arrive async and we don't
	// want them to slide in on every sidebar load. Only animate on subsequent
	// expand/collapse toggles.
	const [animReady, setAnimReady] = useState(false);
	const hasInteractedWithDisclosure = useRef(false);
	useEffect(() => {
		const id = window.setTimeout(() => setAnimReady(true), 500);
		return () => window.clearTimeout(id);
	}, []);
	const isProjectProvisioning = useUiStore((state) => state.provisioningProjectIds.has(workspace.id));
	const isProjectRestarting = useUiStore((state) => state.restartingProjectIds.has(workspace.id));
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const projectIsDragging = isDragged;
	// Keep completed PR sessions reachable while their runtime still exists.
	// Only termination removes a worker from the sidebar; archived sessions stay
	// reachable through SessionsBoard.
	const visibleSessions = useMemo(
		() => sortedWorkerSessions(workspace.sessions).filter((session) => session.isTerminated !== true),
		[workspace.sessions],
	);
	const [sessionOrder, setSessionOrder] = useState<string[]>([]);
	const sessions = useMemo(
		() => applyOrder(visibleSessions, (session) => session.id, sessionOrder, "start"),
		[sessionOrder, visibleSessions],
	);
	const sessionIds = useMemo(() => sessions.map((session) => session.id), [sessions]);
	const commitSessionOrder = useCallback((next: string[] | null) => {
		if (!next) return;
		setSessionOrder(next);
	}, []);
	const openSession = useCallback((sessionId: string) => {
		selection.goSession(workspace.id, sessionId);
	}, [selection, workspace.id]);
	const handleSessionKilled = useCallback(
		(killedSession: WorkspaceSession) => {
			if (selection.activeSessionId !== killedSession.id) return;
			const nextRoute = resolveNextNavigationAfterSessionKill(workspace, killedSession.id, sessions);
			if (nextRoute.target === "session") {
				selection.goSession(workspace.id, nextRoute.sessionId);
			} else {
				selection.goProject(workspace.id);
			}
		},
		[selection, sessions, workspace],
	);
	// The project's live orchestrator (if any) backs the hover Orchestrator
	// button: navigate to it when present, otherwise spawn one first.
	const orchestrator = newestActiveOrchestrator(workspace.sessions);
	const canResumeOrchestrator = useCanResumeAgent(orchestrator);
	const toggleDisclosure = () => {
		hasInteractedWithDisclosure.current = true;
		onToggle(workspace.id);
	};

	// Mirrors ShellTopbar's launcher: attach to the running orchestrator, or
	// spawn one via the daemon and follow it once the workspace refetches.
	// Expand a collapsed project so opening the orchestrator also reveals its
	// session list — otherwise the tree stays shut while you're inside it.
	const openOrchestrator = async () => {
		if (isProjectProvisioning || isProjectRestarting) return;
		if (!expanded) toggleDisclosure();
		if (orchestrator) {
			// Mirrors useProjectOrchestratorAction; both launchers must stay in step.
			if (canResumeOrchestrator && workspace.kind !== "cloud") {
				setIsSpawning(true);
				try {
					await resumeOrchestrator(orchestrator.id);
					await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
				} catch (err) {
					console.error("Failed to resume orchestrator:", err);
					showGlobalToast(
						t("inspector.resumeAgent"),
						err instanceof Error ? err.message : t("shell.couldNotSpawn"),
						"error",
					);
					return;
				} finally {
					setIsSpawning(false);
				}
			}
			selection.goSession(workspace.id, orchestrator.id);
			return;
		}
		// A cloud project has no local orchestrator-agent config, so the settings
		// fallback below would dead-end it. Spawn the orchestrator as a cloud
		// session in its own sandbox instead.
		if (workspace.kind === "cloud") {
			setIsSpawning(true);
			try {
				const sessionId = await spawnCloudOrchestrator(queryClient, workspace.id);
				await queryClient.invalidateQueries({ queryKey: cloudSessionsQueryKey });
				selection.goSession(workspace.id, sessionId);
			} catch (err) {
				console.error("Failed to spawn cloud orchestrator:", err);
			} finally {
				setIsSpawning(false);
			}
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

	const handleForceRemove = async () => {
		setForceConfirmOpen(false);
		setIsRemoving(true);
		try { await onRemoveProject(workspace.id, true); }
		catch (err) { setRemoveError(err instanceof Error ? err.message : t("shell.couldNotRemoveProject")); }
		finally { setIsRemoving(false); }
	};

	// Expanded + already on the project board → collapse. Expanded + on a
	// session (orchestrator or worker) → board. Collapsed → expand + board.
	// Do not treat orchestratorActive like the board: the project row is the
	// one-click path back from the orchestrator button.
	const onProjectClick = () => {
		if (consumeDragClick(workspace.id)) return;
		if (!expanded) {
			toggleDisclosure();
			selection.goProject(workspace.id);
		} else if (dashboardActive) {
			toggleDisclosure();
		} else {
			selection.goProject(workspace.id);
		}
	};

	// Folder icon always toggles disclosure, even when another project is
	// selected — without this, collapsing a non-active project required a
	// select click then a second click (felt like a double-click).
	const onFolderClick = (event: MouseEvent) => {
		event.stopPropagation();
		if (consumeDragClick(workspace.id)) return;
		toggleDisclosure();
	};


	const removeProject = () => {
		setRemoveError(null);
		setConfirmOpen(true);
	};
	const openPullRequestCount = new Set(
		workspace.sessions.flatMap((session) => openPRs(session).map((pr) => pr.url)),
	).size;

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
			if (err instanceof Error && (err as Error & { code?: string }).code === "PROJECT_REMOVE_BLOCKED") {
				setForceBlockers(readProjectRemoveBlockers(err));
				setForceConfirmOpen(true);
				return;
			}
			const message = err instanceof Error ? err.message : t("shell.couldNotRemoveProject");
			setRemoveError(message);
		} finally {
			setIsRemoving(false);
		}
	};

	return (
		<ContextMenu>
			<ContextMenuTrigger asChild>
				<motion.li
					className={cn(
						"group/menu-item relative group-data-[collapsible=icon]:mb-0",
						projectIsDragging && "opacity-50",
					)}
					data-dragging={projectIsDragging ? "true" : undefined}
					data-project-drop-target=""
					data-project-id={workspace.id}
					data-sidebar="menu-item"
					data-slot="sidebar-menu-item"
					layout={!layoutSettled || projectDragInProgress ? false : "position"}
					onDragOver={(event) => onProjectDragOver(event, workspace.id)}
					onDrop={onProjectDrop}
					transition={prefersReducedMotion ? { duration: 0 } : { type: "spring", stiffness: 520, damping: 42, mass: 0.55 }}
				>
					<div
						className="relative"
						data-project-drag-row=""
						data-project-id={workspace.id}
						draggable
						onDragStart={(event) => onProjectDragStart(event, workspace.id)}
						onDragEnd={onProjectDragEnd}
					>
						<div className={cn("relative", projectIsDragging && "cursor-grabbing")}>
							<div>
								{/* project-sidebar__proj-row */}
								<SidebarMenuButton
									aria-current={dashboardActive ? "page" : undefined}
									aria-expanded={expanded}
									isActive={projectActive}
									tooltip={workspace.name}
									onClick={onProjectClick}
									className={cn(
										NAV_ROW_CLASS,
										NAV_ROW_HIGHLIGHT_HOST_CLASS,
										// gap-2 matches SectionDisclosure so project icons/labels share the
										// Projects header's left edge (NAV_ROW defaults to gap-2.5).
										"cursor-grab active:cursor-grabbing",
										"gap-2 pr-sidebar-project-actions [&_svg]:size-icon-md",
										"transition-none",
										projectIsDragging && "!cursor-grabbing",
										projectDragInProgress && "hover:text-muted-foreground active:text-muted-foreground",
										"group-data-[collapsible=icon]:size-control-board! group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:rounded-lg group-data-[collapsible=icon]:p-0! group-data-[collapsible=icon]:font-semibold",
									)}
								>
									<NavRowHighlight active={projectActive} disabled={projectIsDragging} />
									{/* Expanded sidebar: visual folder/chevron icon (decorative — toggle button is a sibling).
		    size-icon-md matches the Projects section row; an 18px centered box was
		    optically indenting these icons relative to the header. */}
									<span
										aria-hidden="true"
										className="relative z-[1] inline-flex size-icon-md shrink-0 translate-y-px items-center justify-center text-muted-foreground group-data-[collapsible=icon]:hidden"
										data-expanded={expanded ? "" : undefined}
										data-project-folder-visual=""
									>
										{/* 1.2 — contextual icon swap: scale 0.8↔1 (animated); opacity snaps for hide.
										    Hover paint lives in styles.css (fine pointer only). */}
										<span
											className="inline-flex size-icon-md items-center justify-center transition-[scale] duration-normal ease-[var(--ease-out)] motion-reduce:transition-none"
											data-project-folder-icon=""
										>
											{expanded ? <FolderOpen strokeWidth={1.75} /> : <Folder strokeWidth={1.75} />}
										</span>
										<span
											className={cn(
												"absolute inline-flex size-icon-md scale-[0.8] items-center justify-center opacity-0",
												"transition-[scale,rotate] duration-normal ease-[var(--ease-out)]",
												"motion-reduce:transition-none",
												expanded && "rotate-90",
											)}
											data-project-chevron-icon=""
										>
											<ChevronRight strokeWidth={1.75} />
										</span>
									</span>
									{/* Collapsed icon rail: folder icon */}
									<span
										aria-hidden="true"
										className="relative z-[1] hidden size-8 items-center justify-center text-muted-foreground group-data-[collapsible=icon]:inline-flex"
									>
										{expanded ? <FolderOpen className="size-5" strokeWidth={1.75} /> : <Folder className="size-5" strokeWidth={1.75} />}
									</span>
									<span
										className="sidebar-expanded-chrome relative z-[1] min-w-0 flex-1 translate-y-px truncate group-data-[collapsible=icon]:hidden"
										data-project-label=""
									>
										{workspace.name}
									</span>
									{workspace.kind === "cloud" && (
										<Badge
											variant="outline"
											className="sidebar-expanded-chrome relative z-[1] h-4 shrink-0 px-1.5 text-2xs group-data-[collapsible=icon]:hidden"
										>
											{t("shell.cloudProjectBadge")}
										</Badge>
									)}
								</SidebarMenuButton>
								{/* Folder disclosure toggle: sibling of the nav button, absolutely positioned over
	    the icon area so it intercepts clicks there without nesting buttons. */}
								<button
									aria-label={t("shell.toggleProject", {
										name: workspace.name,
									})}
									aria-expanded={expanded}
									className="absolute inset-y-0 left-0 z-10 w-9 cursor-pointer bg-transparent group-data-[collapsible=icon]:hidden"
									data-project-folder=""
									onClick={onFolderClick}
									type="button"
								/>
							</div>
							{/* Per-project actions: orchestrator and kebab menu. Outside the row's
		navigation surface so their own presses stay independent. */}
							<div
								className={cn(
									"sidebar-expanded-chrome absolute top-0 right-0.5 z-chrome flex h-control-form items-center gap-px",
									"group-data-[collapsible=icon]:hidden",
									projectDragInProgress && "pointer-events-none",
								)}
								data-project-actions=""
								draggable={false}
								onClick={(event) => event.stopPropagation()}
								onPointerDown={(event) => event.stopPropagation()}
							>
								<Tooltip>
									<TooltipTrigger asChild>
										<span className="inline-flex">
											<button
												aria-current={orchestratorActive ? "page" : undefined}
												aria-label={
													orchestrator
														? t("shell.openProjectOrchestrator", {
																name: workspace.name,
															})
														: t("shell.spawnProjectOrchestrator", {
																name: workspace.name,
															})
												}
													className={cn(HOVER_ACTION_CLASS, orchestratorActive && "text-foreground")}
													disabled={isSpawning || isProjectProvisioning || isProjectRestarting}
												onClick={() => void openOrchestrator()}
												type="button"
											>
												<OrchestratorIcon aria-hidden="true" strokeWidth={orchestratorActive ? 2.5 : 2} />
											</button>
										</span>
									</TooltipTrigger>
										<TooltipContent>
											{isProjectProvisioning || isProjectRestarting
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
										<button
											aria-label={t("shell.projectActions", {
												name: workspace.name,
											})}
											className={HOVER_ACTION_CLASS}
											type="button"
										>
											<MoreVertical aria-hidden="true" />
										</button>
									</DropdownMenuTrigger>
									<DropdownMenuContent side="right" align="start" className="min-w-44">
										<DropdownMenuItem disabled={isProjectRestarting} onSelect={() => requestNewTask(workspace.id)}>
											<Plus aria-hidden="true" />
											{t("shell.newTask")}
										</DropdownMenuItem>
										<DropdownMenuItem onSelect={() => selection.goSettings(workspace.id)}>
											<Settings aria-hidden="true" />
											{t("shell.projectSettings")}
										</DropdownMenuItem>
										<DropdownMenuItem
											className="text-destructive focus:text-destructive [&_svg]:text-destructive focus:[&_svg]:text-destructive"
											disabled={isRemoving}
											onSelect={() => void removeProject()}
										>
											<Trash2 aria-hidden="true" />
											{t("shell.removeProjectTitle")}
										</DropdownMenuItem>
									</DropdownMenuContent>
								</DropdownMenu>
							</div>
						</div>
						{/* end outer relative */}
					</div>
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
					initial={
						animReady && (!suppressInitialExpandAnimation || hasInteractedWithDisclosure.current) ? { gridTemplateRows: "0fr" } : false
					}
					animate={{ gridTemplateRows: "1fr" }}
					exit={{ gridTemplateRows: "0fr" }}
					transition={prefersReducedMotion ? { duration: 0 } : { duration: 0.14, ease: [0.25, 0.46, 0.45, 0.94] }}
					style={{ display: "grid" }}
					className="sidebar-expanded-chrome"
				>
					<div style={{ minHeight: 0, overflow: "hidden" }}>
					<motion.div
						initial={
							animReady && (!suppressInitialExpandAnimation || hasInteractedWithDisclosure.current)
								? { y: -12, opacity: 0 }
								: false
						}
						animate={{ y: 0, opacity: 1 }}
						exit={{ y: -12, opacity: 0 }}
						transition={prefersReducedMotion ? { duration: 0 } : { duration: 0.14, ease: [0.25, 0.46, 0.45, 0.94] }}
					>
											<SessionReorderList
												dndId={sessionDndId(workspace.id)}
												testId={`session-list-${workspace.id}`}
												className="mx-0 ml-3.5 translate-x-0 gap-px border-l-0 px-0 py-1"
												sessions={sessions}
												sessionIds={sessionIds}
												activeSessionId={selection.activeSessionId}
												disableLayout={!layoutSettled}
												plain={projectDragInProgress}
												onReorder={commitSessionOrder}
												onKilled={handleSessionKilled}
												onOpen={openSession}
											/>
								</motion.div>
							</div>
							</motion.div>
						)}
					</AnimatePresence>
					<ConfirmDialog
						open={confirmOpen}
						onOpenChange={setConfirmOpen}
						title={t("shell.removeProjectTitle")}
						description={
							<>
								<p className="text-sm font-medium text-foreground">{t("shell.removeProjectLead", { name: workspace.name })}</p>
								<p className="mt-1 text-xs text-muted-foreground">
									{workspace.kind === CLOUD_PROJECT_KIND
										? t("shell.removeCloudProjectBody")
										: t("shell.removeProjectBody")}
								</p>
								{openPullRequestCount > 0 ? (
									<p className="mt-2 text-xs font-medium text-error">
										{t("shell.removeProjectOpenPrWarning", { count: openPullRequestCount })}
									</p>
								) : null}
							</>
						}
						confirmLabel={t("shell.remove")}
						destructive
						onConfirm={handleConfirmRemove}
					/>
					<ConfirmDialog
						open={forceConfirmOpen}
						onOpenChange={(open) => {
							setForceConfirmOpen(open);
							if (!open) setForceBlockers([]);
						}}
						title={t("shell.forceRemoveProjectTitle")}
						description={
							<>
								<p className="text-sm font-medium text-foreground">{t("shell.forceRemoveProjectLead")}</p>
								<p className="mt-1 text-xs text-muted-foreground">{t("shell.forceRemoveProjectBody")}</p>
								{forceBlockers.length > 0 ? (
									<ul className="mt-2 space-y-1 text-xs text-muted-foreground" data-testid="force-remove-blockers">
										{forceBlockers.map((blocker, index) => (
											<li key={`${blocker.class ?? "blocker"}-${index}`}>
												{blocker.reason || blocker.class}
											</li>
										))}
									</ul>
								) : null}
							</>
						}
						confirmLabel={t("shell.forceRemoveProjectConfirm")}
						destructive
						onConfirm={handleForceRemove}
					/>
				</motion.li>
			</ContextMenuTrigger>
			<ContextMenuContent className="min-w-44">
				<ContextMenuItem disabled={isProjectRestarting} onSelect={() => requestNewTask(workspace.id)}>
					<Plus aria-hidden="true" />
					{t("shell.newTask")}
				</ContextMenuItem>
				<ContextMenuItem onSelect={() => selection.goSettings(workspace.id)}>
					<Settings aria-hidden="true" />
					{t("shell.projectSettings")}
				</ContextMenuItem>
				<ContextMenuItem
					className="text-destructive focus:text-destructive [&_svg]:text-destructive focus:[&_svg]:text-destructive"
					disabled={isRemoving}
					onSelect={() => void removeProject()}
				>
					<Trash2 aria-hidden="true" />
					{t("shell.removeProjectTitle")}
				</ContextMenuItem>
			</ContextMenuContent>
		</ContextMenu>
	);
});

/** Projectless ("ad hoc") agents. Their own section under Projects — same
 *  header chrome, own capped scroller, own Show more — instead of a project row
 *  appended to the project list. */
function ScratchpadSection({
	workspace,
	selection,
	isCollapsed,
	layoutSettled,
	open,
	onToggle,
}: {
	workspace: WorkspaceSummary;
	selection: Selection;
	isCollapsed: boolean;
	layoutSettled: boolean;
	open: boolean;
	onToggle: () => void;
}) {
	const { t } = useTranslation();
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	// Mirrors the project tree: only termination removes an agent from the
	// sidebar, so a completed PR session stays reachable.
	const visibleSessions = useMemo(
		() => sortedWorkerSessions(workspace.sessions).filter((session) => session.isTerminated !== true),
		[workspace.sessions],
	);
	const [sessionOrder, setSessionOrder] = useState<string[]>([]);
	const sessions = useMemo(
		() => applyOrder(visibleSessions, (session) => session.id, sessionOrder, "start"),
		[sessionOrder, visibleSessions],
	);
	const [showAll, setShowAll] = useState(false);
	const activeSessionBeyondLimit = useMemo(() => {
		if (showAll || sessions.length <= SIDEBAR_INITIAL_SECTION_LIMIT) return false;
		const activeId = selection.activeSessionId;
		if (!activeId) return false;
		return sessions.findIndex((session) => session.id === activeId) >= SIDEBAR_INITIAL_SECTION_LIMIT;
	}, [selection.activeSessionId, sessions, showAll]);
	useEffect(() => {
		if (activeSessionBeyondLimit) setShowAll(true);
	}, [activeSessionBeyondLimit]);
	const listedSessions = useMemo(
		() =>
			isCollapsed || showAll || sessions.length <= SIDEBAR_INITIAL_SECTION_LIMIT
				? sessions
				: sessions.slice(0, SIDEBAR_INITIAL_SECTION_LIMIT),
		[isCollapsed, sessions, showAll],
	);
	const listedSessionIds = useMemo(() => listedSessions.map((session) => session.id), [listedSessions]);
	const hiddenSessionCount = Math.max(0, sessions.length - SIDEBAR_INITIAL_SECTION_LIMIT);
	const commitSessionOrder = useCallback(
		(next: string[] | null) => {
			if (!next) return;
			// Only the listed slice is draggable, so keep the still-hidden tail
			// behind it rather than letting applyOrder float it to the front.
			const listed = new Set(next);
			setSessionOrder([...next, ...sessions.filter((session) => !listed.has(session.id)).map((session) => session.id)]);
		},
		[sessions],
	);
	const openSession = useCallback(
		(sessionId: string) => selection.goSession(STANDALONE_WORKSPACE_ID, sessionId),
		[selection],
	);
	const handleSessionKilled = useCallback(
		(killedSession: WorkspaceSession) => {
			if (selection.activeSessionId !== killedSession.id) return;
			const nextRoute = resolveNextNavigationAfterSessionKill(workspace, killedSession.id, sessions);
			// An ad hoc agent has no project board to fall back to.
			if (nextRoute.target === "session") {
				selection.goSession(STANDALONE_WORKSPACE_ID, nextRoute.sessionId);
			} else {
				selection.goHome();
			}
		},
		[selection, sessions, workspace],
	);

	return (
		<div className="sidebar-expanded-chrome flex flex-col group-data-[collapsible=icon]:hidden" data-scratchpad-section="">
			<SectionDisclosure
				label={workspace.name}
				open={open}
				onToggle={onToggle}
				className="mt-1"
				trailing={
					<Tooltip>
						<TooltipTrigger asChild>
							<span className="inline-flex">
								<button
									aria-label={t("shell.openNewAgent")}
									className="sidebar-icon-action grid size-icon-xl shrink-0 place-items-center rounded-sm !bg-transparent text-passive hover:!bg-transparent focus:!bg-transparent focus-visible:!bg-transparent active:!bg-transparent hover:text-foreground"
									onClick={() => requestNewTask(STANDALONE_WORKSPACE_ID)}
									type="button"
								>
									<Plus className="size-icon-sm" aria-hidden="true" />
								</button>
							</span>
						</TooltipTrigger>
						<TooltipContent>{t("shell.openNewAgent")}</TooltipContent>
					</Tooltip>
				}
			/>
			{open && listedSessions.length > 0 ? (
				<div
					className={SECTION_SCROLLER_CLASS}
					data-testid="sidebar-scratchpad-scroller"
					style={sectionScrollerStyle(isCollapsed, showAll)}
				>
					<SessionReorderList
						dndId={sessionDndId(STANDALONE_WORKSPACE_ID)}
						testId={`session-list-${STANDALONE_WORKSPACE_ID}`}
						className="mx-0 ml-0 translate-x-0 gap-0.5 border-l-0 px-0 py-0.5"
						sessions={listedSessions}
						sessionIds={listedSessionIds}
						activeSessionId={selection.activeSessionId}
						disableLayout={!layoutSettled}
						indented={false}
						onReorder={commitSessionOrder}
						onKilled={handleSessionKilled}
						onOpen={openSession}
					/>
				</div>
			) : null}
			{/* Outside the scroller so the cap never hides its own release. */}
			{open && !isCollapsed && !showAll && hiddenSessionCount > 0 ? (
				<ShowMoreRow
					label={t("shell.showMoreAgents", { count: hiddenSessionCount })}
					onClick={() => setShowAll(true)}
				/>
			) : null}
		</div>
	);
}

const PinnedSessionRow = memo(function PinnedSessionRow({
	session,
	active,
	layoutSettled,
	onKilled,
	onOpenSession,
}: {
	session: WorkspaceSession;
	active: boolean;
	layoutSettled: boolean;
	onKilled?: (session: WorkspaceSession) => void;
	onOpenSession: (projectId: string, sessionId: string) => void;
}) {
	const onOpen = useCallback(() => onOpenSession(session.workspaceId, session.id), [onOpenSession, session.id, session.workspaceId]);
	return <SessionRow session={session} active={active} disableLayout={!layoutSettled} indented={false} onKilled={onKilled} onOpen={onOpen} />;
});

// A session row inside its project's drag context. The Pinned section renders
// plain SessionRows instead: that list is ordered by pin time, not by hand.
const SortableSessionRow = memo(function SortableSessionRow({
	session,
	active,
	consumeDragClick,
	disableLayout = false,
	indented = true,
	layoutDependency,
	listIsDragging,
	dropTransitionDisabled,
	onKilled,
	onOpen,
}: {
	session: WorkspaceSession;
	active: boolean;
	consumeDragClick: (id: string) => boolean;
	disableLayout?: boolean;
	indented?: boolean;
	layoutDependency: string;
	listIsDragging: boolean;
	dropTransitionDisabled: boolean;
	onKilled?: (session: WorkspaceSession) => void;
	onOpen: (sessionId: string) => void;
}) {
	const { isDragging, listeners, setActivatorNodeRef, setNodeRef, transform, transition } = useSortable({
		id: session.id,
	});
	return (
		<SessionRow
			session={session}
			active={active}
			indented={indented}
			onKilled={onKilled}
			onOpen={() => {
				if (!consumeDragClick(session.id)) onOpen(session.id);
			}}
			disableLayout={disableLayout}
			layoutDependency={layoutDependency}
			listIsDragging={listIsDragging}
			reorder={{
				isDragging,
				listeners,
				setActivatorNodeRef,
				setNodeRef,
				transform,
				transition,
				dropTransitionDisabled,
			}}
		/>
	);
});

/** The reorderable session list shared by a project's tree and the Scratchpad
 *  section. It owns the drag context; the committed order stays with the caller
 *  so each list keeps its own persistence and slicing rules. */
function SessionReorderList({
	dndId,
	testId,
	className,
	sessions,
	sessionIds,
	activeSessionId,
	disableLayout = false,
	indented = true,
	plain = false,
	onReorder,
	onKilled,
	onOpen,
}: {
	dndId: string;
	testId: string;
	className: string;
	sessions: WorkspaceSession[];
	sessionIds: string[];
	activeSessionId?: string;
	disableLayout?: boolean;
	indented?: boolean;
	/** While a project is being dragged, leave the session lists as plain rows:
	 *  otherwise every expanded project's DnD context measures its sortable
	 *  descendants on drop. */
	plain?: boolean;
	onReorder: (next: string[] | null) => void;
	onKilled?: (session: WorkspaceSession) => void;
	onOpen: (sessionId: string) => void;
}) {
	const layoutDependency = useMemo(() => sessionIds.join("\u0000"), [sessionIds]);
	const sensors = useReorderSensors();
	const dragClickGuard = usePostDragClickGuard();
	const [listDragging, setListDragging] = useState(false);
	const [dropTransitionDisabledId, setDropTransitionDisabledId] = useState<string | null>(null);

	const onDragEnd = useCallback(({ active, over }: DragEndEvent) => {
		const sessionId = String(active.id);
		dragClickGuard.markDragEnded(sessionId);
		if (!over) {
			setListDragging(false);
			setDropTransitionDisabledId(null);
			if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
			return;
		}
		// reorderById rejects any id that is not in THIS list, so a stray
		// cross-list drop leaves both lists' orders untouched.
		const next = reorderById(sessionIds, sessionId, String(over.id));
		// Commit the destination DOM order before dnd-kit removes its live transform.
		// Otherwise the row briefly snaps back to its derived (usually top) position,
		// then Motion animates it forward to the persisted destination.
		flushSync(() => {
			onReorder(next);
			setListDragging(false);
			setDropTransitionDisabledId(sessionId);
		});
		requestAnimationFrame(() => setDropTransitionDisabledId(null));
		if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
	}, [dragClickGuard, onReorder, sessionIds]);

	const onDragCancel = useCallback(() => {
		setListDragging(false);
		setDropTransitionDisabledId(null);
		if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
	}, []);

	if (plain) {
		return (
			<SidebarMenuSub className={className} data-testid={testId}>
				{sessions.map((session) => (
					<SessionRow
						key={session.id}
						session={session}
						active={activeSessionId === session.id}
						disableLayout
						indented={indented}
						onKilled={onKilled}
						onOpen={() => onOpen(session.id)}
					/>
				))}
			</SidebarMenuSub>
		);
	}

	return (
		<DndContext
			collisionDetection={closestCenter}
			modifiers={[restrictToListBounds]}
			id={dndId}
			onDragStart={() => setListDragging(true)}
			onDragCancel={onDragCancel}
			onDragEnd={onDragEnd}
			sensors={sensors}
		>
			<SortableContext items={sessionIds} strategy={verticalListSortingStrategy}>
				<SidebarMenuSub className={className} data-testid={testId}>
					{sessions.map((session) => (
						<SortableSessionRow
							key={session.id}
							session={session}
							active={activeSessionId === session.id}
							consumeDragClick={dragClickGuard.consumeClick}
							disableLayout={disableLayout}
							indented={indented}
							layoutDependency={layoutDependency}
							listIsDragging={listDragging}
							dropTransitionDisabled={dropTransitionDisabledId === session.id}
							onKilled={onKilled}
							onOpen={onOpen}
						/>
					))}
				</SidebarMenuSub>
			</SortableContext>
		</DndContext>
	);
}

type SessionReorder = Pick<SortableRow, "isDragging" | "listeners" | "setActivatorNodeRef" | "setNodeRef" | "transform" | "transition"> & {
	dropTransitionDisabled: boolean;
};

// One worker-session row. Reads as a link by default; double-click/double-tap
// on the name or F2 flips the label into an inline input (Enter/blur saves,
// Escape cancels) that persists through the daemon rename endpoint.
function SessionRow({
	session,
	active,
	indented = true,
	layoutDependency,
	listIsDragging = false,
	disableLayout = false,
	onKilled,
	onOpen,
	reorder,
}: {
	session: WorkspaceSession;
	active: boolean;
	indented?: boolean;
	layoutDependency?: string;
	listIsDragging?: boolean;
	/** Project drags pause nested session projection work. */
	disableLayout?: boolean;
	onKilled?: (session: WorkspaceSession) => void;
	onOpen: () => void;
	/** Present only for rows inside a reorderable project list. */
	reorder?: SessionReorder;
}) {
	const { t } = useTranslation();
	const prefersReducedMotion = useReducedMotion();
	useGrabbingCursor(Boolean(reorder?.isDragging));
	const switchPresentation = deriveSessionAgentSwitchPresentation(session);
	const switchLabel = switchPresentation
		? t(switchPresentation.compactLabelKey, switchPresentation.values)
		: undefined;
	const switchStatusId = useId();
	const describedBy = switchLabel ? switchStatusId : undefined;
	const queryClient = useQueryClient();
	const refreshWorkspaces = useCallback(
		() => queryClient.invalidateQueries({ queryKey: workspaceQueryKey }),
		[queryClient],
	);
	const rename = useSessionRename(session, refreshWorkspaces);
	const lastTouchAtRef = useRef(0);
	const suppressTouchOpenRef = useRef(false);
	const beginRename = useCallback(() => {
		rename.begin();
	}, [rename.begin]);

	if (rename.isEditing) {
		return (
			<SidebarMenuSubItem className={cn(indented && "pl-0.5")}>
				<div
					className={cn(
						"group/nav-row relative flex h-8 w-full items-center gap-1.5 rounded-lg py-0 pl-1.5 pr-1",
						active && "text-foreground",
					)}
					data-session-row=""
				>
					<NavRowHighlight active={active} />
					<SessionStatusDot session={session} />
					<input
						aria-label={t("shell.renameSession", { title: session.title })}
						autoFocus
						className={cn(
							"relative z-[1] h-full min-w-0 flex-1 appearance-none border-0 bg-transparent! p-0 text-sm text-foreground outline-none ring-0 focus:outline-none focus:ring-0",
							session.lastUserMessageAt && "pr-[36px]",
						)}
						data-session-inline-editor=""
						maxLength={MAX_SESSION_DISPLAY_NAME_LEN}
						onBlur={() => void rename.commit()}
						onChange={(e) => rename.setDraft(e.target.value)}
						onFocus={(e) => e.currentTarget.select()}
						onKeyDown={(e) => {
							if (e.key === "Enter") {
								e.preventDefault();
								e.currentTarget.blur();
							} else if (e.key === "Escape") {
								e.preventDefault();
								rename.cancel();
							}
						}}
						value={rename.draft}
					/>
					<SessionMessageAge session={session} />
				</div>
			</SidebarMenuSubItem>
		);
	}

	return (
		<ContextMenu>
			<ContextMenuTrigger asChild>
				<SidebarMenuSubItem
					className={cn(indented && "pl-0.5", reorder?.isDragging && "z-chrome cursor-grabbing opacity-60")}
					data-dragging={reorder?.isDragging ? "true" : undefined}
					ref={reorder?.setNodeRef}
					style={reorder ? sortableRowStyle(reorder) : undefined}
				>
			<motion.div
				layout={disableLayout || listIsDragging ? false : "position"}
				layoutDependency={disableLayout ? undefined : layoutDependency}
				transition={prefersReducedMotion ? { duration: 0 } : { type: "spring", stiffness: 520, damping: 42, mass: 0.55 }}
			>
				<div
					className={cn(
						"group/session-row group/nav-row relative flex h-8 w-full items-center rounded-lg",
						"hover:text-foreground",
						active && "text-foreground",
					)}
					data-session-row=""
					data-dragging={reorder?.isDragging ? "true" : undefined}
				>
					<NavRowHighlight active={active} disabled={Boolean(reorder?.isDragging)} />
					<div className={cn("relative z-[1] flex min-w-0 flex-1", reorder?.isDragging && "cursor-grabbing")}>
						<button
							aria-current={active ? "page" : undefined}
							aria-describedby={describedBy}
							aria-keyshortcuts="F2"
							aria-label={t("shell.openSession", { title: session.title })}
							className={cn(
								"flex h-8 min-w-0 flex-1 items-center gap-1.5 rounded-lg py-0 pl-1.5 text-left text-sm outline-hidden focus-visible:ring-2 focus-visible:ring-sidebar-ring",
								session.lastUserMessageAt ? "pr-[36px]" : "pr-2.5",
								!reorder?.isDragging &&
									"group-hover/session-row:pr-[50px] group-focus-within/session-row:pr-[50px]",
								reorder && "cursor-grab active:cursor-grabbing",
								reorder?.isDragging && "!cursor-grabbing",
							)}
							{...(reorder?.listeners ?? {})}
							onClick={(event) => {
								if (event.detail > 1) return;
								if (suppressTouchOpenRef.current) {
									suppressTouchOpenRef.current = false;
									return;
								}
								onOpen();
							}}
							onKeyDown={(event) => {
								if (event.key !== "F2") return;
								event.preventDefault();
								beginRename();
							}}
							onDoubleClick={(event) => {
								event.preventDefault();
								event.stopPropagation();
								beginRename();
							}}
							ref={reorder?.setActivatorNodeRef}
							type="button"
						>
							<SessionStatusDot session={session} />
							<span className="flex min-w-0 flex-1 items-center gap-1.5">
								<span
									className={cn(
										"min-w-0 flex-1 truncate",
										active ? "text-foreground" : "text-muted-foreground group-hover/session-row:text-foreground",
									)}
									data-session-name=""
									onPointerUp={(event) => {
										if (event.pointerType !== "touch") return;
										const now = Date.now();
										if (now - lastTouchAtRef.current <= 500) {
											suppressTouchOpenRef.current = true;
										beginRename();
										}
										lastTouchAtRef.current = now;
									}}
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
					</div>
					{/* The timestamp is stable at the right edge. Pin and kill use label
					    space while idle, then reveal without changing the row footprint. */}
					<SessionActions
						isDragging={Boolean(reorder?.isDragging)}
						onKilled={onKilled}
						session={session}
					/>
				</div>
			</motion.div>
				</SidebarMenuSubItem>
			</ContextMenuTrigger>
			<ContextMenuContent className="min-w-44">
				<ContextMenuItem aria-label={t("shell.renameSession", { title: session.title })} onSelect={beginRename}>
					<Pencil aria-hidden="true" />
					{t("shell.rename")}
				</ContextMenuItem>
			</ContextMenuContent>
		</ContextMenu>
	);
}

const SessionMessageAge = memo(function SessionMessageAge({ session }: { session: WorkspaceSession }) {
	const { t } = useTranslation();
	if (!session.lastUserMessageAt) return null;

	return (
		<time
			className="absolute inset-y-0 right-1.5 z-[1] flex min-w-0 shrink-0 items-center whitespace-nowrap font-sans text-micro tabular-nums text-passive opacity-100 group-focus-within/session-row:opacity-0"
			data-session-message-age=""
			dateTime={session.lastUserMessageAt}
			title={t("shell.lastMessageAt", { time: formatTimeCompact(session.lastUserMessageAt) })}
		>
			{formatTimeTerse(session.lastUserMessageAt)}
		</time>
	);
});

const SessionActions = memo(function SessionActions({
	session,
	isDragging,
	onKilled,
}: {
	session: WorkspaceSession;
	isDragging: boolean;
	onKilled?: (session: WorkspaceSession) => void;
}) {
	const { t } = useTranslation();
	const { mutate: pinSession } = usePinSession();
	const { mutate: unpinSession } = useUnpinSession();
	// Optimistic: navigate + drop the row as soon as kill starts (onMutate),
	// not after the daemon round-trip.
	const onKilledRef = useRef(onKilled);
	onKilledRef.current = onKilled;
	const { mutate: terminateSession, isPending: isKilling } = useTerminateSession({
		onOptimistic: (killed) => {
			onKilledRef.current?.(killed);
		},
	});

	const handleKill = (event: React.MouseEvent) => {
		event.stopPropagation();
		terminateSession(session);
	};

	return (
		<div
			className="pointer-events-none absolute inset-y-0 right-0 z-chrome"
			data-session-actions=""
			onPointerDown={(event) => event.stopPropagation()}
		>
			<div
				className={cn(
					/* 1.3 — pin/kill: scale 0.8↔1 from center (not origin-right — that reads as a slide) */
					"absolute inset-y-0 right-0.5 flex origin-center scale-[0.8] items-center gap-px opacity-0",
					"transition-[scale] duration-normal ease-[var(--ease-out)]",
					"motion-reduce:transition-none",
					!isDragging &&
						"group-focus-within/session-row:pointer-events-auto group-focus-within/session-row:scale-100 group-focus-within/session-row:opacity-100",
				)}
				data-session-action-buttons=""
			>
				<Tooltip>
					<TooltipTrigger asChild>
						<button
							aria-label={session.isPinned ? t("shell.unpinSession") : t("shell.pinSession")}
							className={cn(
								SESSION_ACTION_CLASS,
								"focus-visible:text-foreground",
								session.isPinned && "text-foreground",
							)}
							onClick={(event) => {
								event.stopPropagation();
								session.isPinned ? unpinSession(session) : pinSession(session);
							}}
							type="button"
						>
							{session.isPinned ? <PinOff aria-hidden="true" /> : <Pin aria-hidden="true" />}
						</button>
					</TooltipTrigger>
					<TooltipContent side="top">
						{session.isPinned ? t("shell.unpinSession") : t("shell.pinSession")}
					</TooltipContent>
				</Tooltip>
				<Tooltip>
					<TooltipTrigger asChild>
						<button
							aria-label={t("shell.killSession")}
							className={cn(SESSION_ACTION_CLASS, "hover:text-destructive focus-visible:text-destructive")}
							disabled={isKilling}
							onClick={handleKill}
							type="button"
						>
							<Trash2 aria-hidden="true" />
						</button>
					</TooltipTrigger>
					<TooltipContent side="top">{t("shell.killSession")}</TooltipContent>
				</Tooltip>
			</div>
			<SessionMessageAge session={session} />
		</div>
	);
});

// CloudSignInRow: the entry point that starts the WorkOS sign-in flow. Shown
// only when the cloud offering is enabled (entitled client + flag + control
// plane), WorkOS is configured, and no one is signed in yet.
function CloudSignInRow({ tabIndex }: { tabIndex: number }) {
	const { t } = useTranslation();
	const { cloudEnabled } = useCloudGate();
	const { configured, status, signIn } = useCloudSession();
	// Dev + loopback CP: open the local email/password dialog instead of WorkOS.
	const { available: localAuthAvailable } = useCloudLocalAuth();
	const openLocalSignIn = useLocalSignInDialogStore((s) => s.openDialog);
	const onSignIn = () => (localAuthAvailable ? openLocalSignIn() : signIn());
	if (!configured || !cloudEnabled || status !== "unauthenticated") return null;

	return (
		<button
			aria-label={t("shell.signInToAOCloud")}
			className={FOOTER_NAV_BUTTON_CLASS}
			onClick={onSignIn}
			tabIndex={tabIndex}
			type="button"
		>
			<NavRowHighlight />
			<span className="relative z-[1] flex min-w-0 flex-1 items-center gap-2.5 [&_svg]:size-icon-md [&_svg]:shrink-0">
				<LogIn aria-hidden="true" />
				<span className="tracking-tight">{t("shell.signInToAOCloud")}</span>
			</span>
		</button>
	);
}

// Icon-rail variant for the collapsed sidebar.
function CloudSignInRailButton({ tabIndex }: { tabIndex: number }) {
	const { t } = useTranslation();
	const { cloudEnabled } = useCloudGate();
	const { configured, status, signIn } = useCloudSession();
	// Dev + loopback CP: open the local email/password dialog instead of WorkOS.
	const { available: localAuthAvailable } = useCloudLocalAuth();
	const openLocalSignIn = useLocalSignInDialogStore((s) => s.openDialog);
	const onSignIn = () => (localAuthAvailable ? openLocalSignIn() : signIn());
	if (!configured || !cloudEnabled || status !== "unauthenticated") return null;

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					aria-label={t("shell.signInToAOCloud")}
					className={FOOTER_RAIL_BUTTON_CLASS}
					onClick={onSignIn}
					tabIndex={tabIndex}
					type="button"
				>
					<NavRowHighlight />
					<span className="relative z-[1] grid place-items-center [&_svg]:size-icon-base">
						<LogIn aria-hidden="true" />
					</span>
				</button>
			</TooltipTrigger>
			<TooltipContent side="right">{t("shell.signInToAOCloud")}</TooltipContent>
		</Tooltip>
	);
}

// CloudAccountRow: shown above the Settings button for an existing cloud
// session (the signed-in state). The sign-in entry point is CloudSignInRow.
function CloudAccountRow({ tabIndex }: { tabIndex: number }) {
	const { t } = useTranslation();
	const { cloudEnabled } = useCloudGate();
	const { configured, session, status, signOut } = useCloudSession();
	if (!configured || !cloudEnabled || status !== "authenticated") return null;

	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<button
					aria-label={t("shell.signedInAs", {
						email: session?.user.email ?? "AO Cloud",
					})}
					className={FOOTER_NAV_BUTTON_CLASS}
					tabIndex={tabIndex}
					type="button"
				>
					<NavRowHighlight />
					<span className="relative z-[1] flex min-w-0 flex-1 items-center gap-2.5 [&_svg]:size-icon-md [&_svg]:shrink-0">
						<User aria-hidden="true" />
						<span className="min-w-0 flex-1 truncate tracking-tight">
							{session?.user.email ?? "AO Cloud"}
						</span>
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
	const { cloudEnabled } = useCloudGate();
	const { configured, session, status, signOut } = useCloudSession();
	if (!configured || !cloudEnabled || status !== "authenticated") return null;

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					aria-label={t("shell.signedInAs", {
						email: session?.user.email ?? "AO Cloud",
					})}
					className={FOOTER_RAIL_BUTTON_CLASS}
					onClick={() => void signOut()}
					tabIndex={tabIndex}
					type="button"
				>
					<NavRowHighlight />
					<span className="relative z-[1] grid place-items-center [&_svg]:size-icon-base">
						<User aria-hidden="true" />
					</span>
				</button>
			</TooltipTrigger>
			<TooltipContent side="right">
				{t("shell.signOutWithEmail", {
					email: session?.user.email ?? "AO Cloud",
				})}
			</TooltipContent>
		</Tooltip>
	);
}

/**
 * What the sidebar should act on, derived from the live update status.
 *
 * `status.state` alone is not enough. It cycles through checking → available →
 * not-available on every background check while a staged build sits untouched,
 * which blinked the restart row out of existence every 15 minutes on nightly.
 * `status.staged` is stamped on every status by the main process for exactly
 * this reason, so the staged build is read from there rather than from `state`.
 */
type SidebarUpdateAction =
	| { kind: "downloading"; percent: number }
	| { kind: "download"; version?: string }
	| { kind: "install"; version?: string; escalated: boolean }
	| { kind: "retry" }
	| null;

function sidebarUpdateAction(status: UpdateStatus, availableDismissed: boolean): SidebarUpdateAction {
	if (status.state === "downloading") {
		return { kind: "downloading", percent: Math.min(100, Math.max(0, status.percent ?? 0)) };
	}
	// `staged` is the stamp the main process puts on every status; the
	// `downloaded` fallback keeps this correct for any status that predates it
	// or arrives from a source that does not stamp.
	const staged =
		status.staged ??
		(status.state === "downloaded"
			? {
					version: status.version,
					stagedAt: status.stagedAt ?? 0,
					escalated: status.escalated === true,
				}
			: undefined);
	// Something newer than what is already staged still deserves the download
	// action; the main process reports a re-discovered staged build as
	// "downloaded", so an "available" here is genuinely a different version.
	if (status.state === "available" && !availableDismissed && status.version !== staged?.version) {
		return { kind: "download", version: status.version };
	}
	if (staged) return { kind: "install", version: staged.version, escalated: staged.escalated };
	// Ranked below a staged build on purpose: an update ready to install is more
	// actionable than "checks are failing". Only when there is nothing better to
	// show does the failure take the row — it used to render nothing at all,
	// which reads as "up to date" rather than "checks are not getting through".
	if (status.checksFailing === true) return { kind: "retry" };
	return null;
}

/**
 * Sidebar label for a build. A raw nightly string truncates to noise and two
 * consecutive nightlies differ only in trailing digits, so nightlies render as
 * base version plus build date instead.
 */
function updateVersionLabel(
	version: string | undefined,
	variant: "available" | "ready",
	t: TFunction,
	locale: string,
): string | null {
	if (!version) return null;
	const nightly = parseNightlyVersion(version);
	if (nightly) {
		return t("shell.nightlyBuild", {
			version: nightly.base,
			date: new Intl.DateTimeFormat(locale, { month: "short", day: "numeric" }).format(nightly.builtAt),
		});
	}
	return t(variant === "ready" ? "shell.versionReady" : "shell.versionAvailable", { version });
}

/** Plain version number for the install cue — base for nightlies, no channel/date. */
function installVersionNumber(version: string | undefined): string | null {
	if (!version) return null;
	return parseNightlyVersion(version)?.base ?? version;
}

// UpdateStatusRow makes download progress visible in the footer. A staged build
// ready to install renders as UpdateInstallSlide above Connect mobile / Settings.
function UpdateStatusRow({
	availableDismissed,
	onDismissAvailable,
	status,
	tabIndex,
}: {
	availableDismissed: boolean;
	onDismissAvailable: () => void;
	status: UpdateStatus;
	tabIndex: number;
}) {
	const { t, i18n } = useTranslation();
	const locale = i18n.resolvedLanguage ?? i18n.language;
	const action = sidebarUpdateAction(status, availableDismissed);
	if (action === null || action.kind === "install") return null;

	if (action.kind === "download") {
		const versionLabel = updateVersionLabel(action.version, "available", t, locale);
		// A manual check leaves autoDownload off, so without this the row would
		// announce an update and offer nothing to act on.
		return (
			<div className="flex w-full items-center gap-1" data-testid="sidebar-update-available">
				<button
					aria-label={
						action.version
							? t("shell.downloadUpdateVersion", { version: action.version })
							: t("shell.downloadUpdate")
					}
					className={cn(FOOTER_NAV_BUTTON_CLASS, "min-w-0 flex-1")}
					onClick={() => void aoBridge.updates.download()}
					tabIndex={tabIndex}
					type="button"
				>
					<NavRowHighlight />
					<span className="relative z-[1] flex min-w-0 flex-1 items-center gap-2.5 [&_svg]:size-icon-md [&_svg]:shrink-0">
						<Download aria-hidden="true" className="size-icon-lg shrink-0" />
						<span className="min-w-0 flex-1">
							<span className="block truncate tracking-tight">{t("shell.updateAvailable")}</span>
							{versionLabel && (
								<span className="block truncate text-caption font-normal text-passive">{versionLabel}</span>
							)}
						</span>
					</span>
				</button>
				{action.version && (
					<button
						aria-label={t("shell.dismissUpdateVersion", { version: action.version })}
						className="grid size-8 shrink-0 place-items-center text-muted-foreground hover:text-foreground"
						onClick={onDismissAvailable}
						tabIndex={tabIndex}
						type="button"
					>
						<X aria-hidden="true" className="size-icon-base" />
					</button>
				)}
			</div>
		);
	}

	if (action.kind === "downloading") {
		return (
			<div
				aria-live="polite"
				className={cn(NAV_ROW_CLASS, "flex w-full items-center text-left [&_svg]:size-icon-md [&_svg]:shrink-0")}
				data-testid="sidebar-update-downloading"
				role="status"
			>
				<Download aria-hidden="true" className="size-icon-lg shrink-0" />
				<span className="min-w-0 flex-1 truncate tabular-nums">
					{t("settings.updates.downloading", { percent: action.percent })}
				</span>
			</div>
		);
	}

	return (
		<button
			aria-label={t("shell.retryUpdateCheck")}
			className="flex w-full items-center gap-2.5 rounded-lg border border-warning/35 bg-warning/12 p-2.5 text-left text-control font-medium text-warning hover:bg-warning/18 [&_svg]:text-warning"
			data-testid="sidebar-update-failed"
			onClick={() => void aoBridge.updates.check()}
			tabIndex={tabIndex}
			type="button"
		>
			<AlertTriangle aria-hidden="true" className="size-icon-lg shrink-0" />
			<span className="min-w-0 flex-1">
				<span className="block truncate tracking-tight">{t("shell.updateCheckFailed")}</span>
				<span className="block truncate text-caption font-normal text-warning">
					{t("shell.retryUpdateCheck")}
				</span>
			</span>
		</button>
	);
}

/**
 * Alert-style install cue above Connect mobile / Settings. Muted fill so it
 * reads apart from nav rows; shows the version number only (no Nightly/date).
 */
function UpdateInstallSlide({
	availableDismissed,
	onRequestInstall,
	status,
	tabIndex,
}: {
	availableDismissed: boolean;
	onRequestInstall: () => void;
	status: UpdateStatus;
	tabIndex: number;
}) {
	const { t } = useTranslation();
	const action = sidebarUpdateAction(status, availableDismissed);
	if (action?.kind !== "install") return null;

	const versionNumber = installVersionNumber(action.version);
	return (
		<button
			aria-label={
				versionNumber
					? t("shell.restartInstallUpdateVersion", { version: versionNumber })
					: t("shell.restartInstallUpdate")
			}
			className={cn(
				"mb-1 flex h-9 w-full items-center gap-2.5 rounded-lg bg-muted px-3 text-left text-sm font-normal text-foreground",
				"hover:bg-interactive-hover",
			)}
			data-testid="sidebar-update-ready"
			onClick={onRequestInstall}
			tabIndex={tabIndex}
			type="button"
		>
			<RefreshCw aria-hidden="true" className="size-icon-sm shrink-0 text-muted-foreground" />
			<span className="min-w-0 flex-1 truncate tracking-tight">
				{t("shell.restartToUpdate")}
				{versionNumber ? (
					<>
						{" "}
						<span className="text-muted-foreground">{versionNumber}</span>
					</>
				) : null}
			</span>
		</button>
	);
}

// Icon-rail variant of UpdateStatusRow. An available build downloads on click
// and a staged one installs; an in-flight download is informational.
function UpdateStatusRail({
	availableDismissed,
	onRequestInstall,
	status,
	tabIndex,
}: {
	availableDismissed: boolean;
	/** Opens the restart confirmation; installing outright would quit the app. */
	onRequestInstall: () => void;
	status: UpdateStatus;
	tabIndex: number;
}) {
	const { t } = useTranslation();
	const action = sidebarUpdateAction(status, availableDismissed);
	if (action === null) return null;

	if (action.kind === "download") {
		const label = t("settings.updates.available", { version: action.version ? ` (v${action.version})` : "" });
		return (
			<Tooltip>
				<TooltipTrigger asChild>
					<button
						aria-label={
							action.version
								? t("shell.downloadUpdateVersion", { version: action.version })
								: t("shell.downloadUpdate")
						}
						className={cn(FOOTER_RAIL_BUTTON_CLASS, "size-9 text-passive [&_svg]:size-4")}
						onClick={() => void aoBridge.updates.download()}
						tabIndex={tabIndex}
						type="button"
					>
						<NavRowHighlight />
						<span className="relative z-[1] grid place-items-center [&_svg]:size-4">
							<Download aria-hidden="true" />
						</span>
					</button>
				</TooltipTrigger>
				<TooltipContent side="right">{label}</TooltipContent>
			</Tooltip>
		);
	}

	if (action.kind === "downloading") {
		const label = t("settings.updates.downloading", { percent: action.percent });
		return (
			<Tooltip>
				<TooltipTrigger asChild>
					<span
						aria-label={label}
						aria-live="polite"
						className="grid size-9 place-items-center rounded-lg text-passive [&_svg]:size-4"
						role="status"
					>
						<Download aria-hidden="true" />
					</span>
				</TooltipTrigger>
				<TooltipContent side="right">{label}</TooltipContent>
			</Tooltip>
		);
	}

	if (action.kind === "retry") {
		return (
			<Tooltip>
				<TooltipTrigger asChild>
					<button
						aria-label={t("shell.retryUpdateCheck")}
						className="grid size-9 place-items-center rounded-lg bg-warning/12 text-warning hover:bg-warning/18 [&_svg]:size-4"
						onClick={() => void aoBridge.updates.check()}
						tabIndex={tabIndex}
						type="button"
					>
						<AlertTriangle aria-hidden="true" />
					</button>
				</TooltipTrigger>
				<TooltipContent side="right">
					{t("shell.updateCheckFailed")} · {t("shell.retryUpdateCheck")}
				</TooltipContent>
			</Tooltip>
		);
	}

	const versionNumber = installVersionNumber(action.version);
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					aria-label={
						versionNumber
							? t("shell.restartInstallUpdateVersion", { version: versionNumber })
							: t("shell.restartInstallUpdate")
					}
					className="grid size-9 place-items-center rounded-lg bg-muted text-muted-foreground hover:bg-interactive-hover hover:text-foreground [&_svg]:size-4"
					onClick={onRequestInstall}
					tabIndex={tabIndex}
					type="button"
				>
					<RefreshCw aria-hidden="true" />
				</button>
			</TooltipTrigger>
			<TooltipContent side="right">
				{t("shell.restartToUpdate")}
				{versionNumber ? ` ${versionNumber}` : ""}
			</TooltipContent>
		</Tooltip>
	);
}

/** Releases a section's initial cap. Sits below its section's scroller so the
 *  cap can never hide the control that lifts it. */
function ShowMoreRow({ label, onClick }: { label: string; onClick: () => void }) {
	const { t } = useTranslation();
	return (
		<button
			aria-label={label}
			className={cn(
				SECTION_ROW_CLASS,
				NAV_ROW_HIGHLIGHT_HOST_CLASS,
				"mb-1 rounded-lg text-left text-muted-foreground",
			)}
			onClick={onClick}
			type="button"
		>
			<NavRowHighlight />
			<span className="relative z-[1] truncate">{t("shell.showMore")}</span>
		</button>
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
			<div
				className={cn(
					SECTION_ROW_CLASS,
					NAV_ROW_HIGHLIGHT_HOST_CLASS,
					"rounded-lg pr-1",
					className,
				)}
			>
				<NavRowHighlight />
				<button
					aria-expanded={open}
					aria-label={label}
					className="relative z-[1] flex min-w-0 flex-1 items-center gap-2 text-left"
					onClick={onToggle}
					type="button"
				>
					{labelRow}
				</button>
				<span className="relative z-[1] shrink-0">{trailing}</span>
			</div>
		);
	}

	return (
		<button
			aria-expanded={open}
			aria-label={label}
			className={cn(
				SECTION_ROW_CLASS,
				NAV_ROW_HIGHLIGHT_HOST_CLASS,
				"rounded-lg text-left",
				className,
			)}
			onClick={onToggle}
			type="button"
		>
			<NavRowHighlight />
			<span className="relative z-[1] flex min-w-0 flex-1 items-center gap-2">
				{labelRow}
			</span>
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
					"hover:bg-interactive-hover! hover:text-foreground active:bg-interactive-hover! [&_svg]:size-icon-sm!",
					"group-data-[collapsible=icon]:size-control-board! group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:rounded-lg group-data-[collapsible=icon]:bg-transparent group-data-[collapsible=icon]:p-0! group-data-[collapsible=icon]:hover:bg-interactive-hover!",
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
	existingProjectPaths,
	hideTrigger = false,
	onCloneProject,
	onCreateProject,
	onInitializeProject,
	onOpenExistingProject,
}: Pick<SidebarProps, "onCloneProject" | "onCreateProject" | "onInitializeProject"> & {
	existingProjectPaths: readonly string[];
	hideTrigger?: boolean;
	onOpenExistingProject: (path: string) => void | Promise<void>;
}) {
	const { t } = useTranslation();
	// Single CreateProjectFlow owner for the sidebar: the header "+" stays mounted
	// (CSS-hidden when collapsed or on the empty start page) so it can own
	// openSignal for ⌘N on every shell route. The collapsed rail button below
	// reuses this flow via requestCreateProject().
	const createProjectNonce = useUiStore((state) => state.createProjectNonce);
	const folderDropRequest = useUiStore((state) => state.folderDropRequest);
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	return (
		<CreateProjectFlow
			droppedPath={folderDropRequest}
			existingProjectPaths={existingProjectPaths}
			mode="choose"
			onCloneProject={onCloneProject}
			onCreateProject={onCreateProject}
			onCreateStandaloneAgent={() => requestNewTask(STANDALONE_WORKSPACE_ID)}
			onInitializeProject={onInitializeProject}
			onOpenExistingProject={onOpenExistingProject}
			openSignal={createProjectNonce}
		>
			{({ disabled, choosePath, label }) => (
				<Tooltip>
					<TooltipTrigger asChild>
						<span className="inline-flex">
							<button
								aria-label={t("shell.newProject")}
								className={cn(
									"sidebar-icon-action grid size-icon-xl shrink-0 place-items-center rounded-sm !bg-transparent text-passive hover:!bg-transparent focus:!bg-transparent focus-visible:!bg-transparent active:!bg-transparent hover:text-foreground",
									hideTrigger && "hidden",
								)}
								disabled={disabled}
								onClick={choosePath}
								type="button"
							>
								<Plus className="size-icon-sm" aria-hidden="true" />
							</button>
						</span>
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
