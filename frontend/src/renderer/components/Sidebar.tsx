import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { useNavigate, useParams, useRouterState } from "@tanstack/react-router";
import {
	DndContext,
	DragOverlay,
	PointerSensor,
	closestCenter,
	pointerWithin,
	useDraggable,
	useDroppable,
	useSensor,
	useSensors,
	type CollisionDetection,
	type Modifier,
	type DragMoveEvent,
	type DragOverEvent,
	type DragStartEvent,
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
	type MouseEvent,
	type PointerEvent as ReactPointerEvent,
	type ReactNode,
} from "react";
import { flushSync } from "react-dom";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import type { UpdateStatus } from "../../main/update-settings";
import { parseNightlyVersion } from "../lib/build-channel";
import {
	hasConfiguredOrchestratorAgent,
	newestActiveOrchestrator,
	type WorkspaceSession,
	type WorkspaceSummary,
	sortedWorkerSessions,
	workerSessions,
	STANDALONE_PROJECT_KIND,
	STANDALONE_WORKSPACE_ID,
} from "../types/workspace";
import { getSessionStatusDotView } from "../lib/session-presentation";
import { deriveSessionAgentSwitchPresentation } from "../lib/agent-switch-presentation";
import { aoBridge } from "../lib/bridge";
import { useCommandPaletteEnabled } from "../hooks/useCommandPaletteEnabled";
import { cloudSessionsQueryKey, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { usePinSession, useUnpinSession } from "../hooks/usePinSession";
import { spawnCloudOrchestrator } from "../lib/cloud-orchestrator";
import { spawnOrchestrator } from "../lib/spawn-orchestrator";
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
import { Badge } from "./ui/badge";
import aoLogo from "../../../assets/ao-logo.svg";
import { cn } from "../lib/utils";
import { useUiStore } from "../stores/ui-store"
import { useKeybindingsStore } from "../stores/keybindings-store";
import { ConfirmDialog } from "./ConfirmDialog";
import { CreateProjectFlow, type CloneProjectInput, type CreateProjectInput } from "./CreateProjectFlow";
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
	"grid size-5 shrink-0 place-items-center rounded-md bg-transparent text-passive hover:bg-transparent focus:bg-transparent focus-visible:bg-transparent active:bg-transparent data-[state=open]:bg-transparent hover:text-foreground disabled:pointer-events-none disabled:opacity-50 data-[state=open]:text-foreground [&_svg]:size-icon-lg";

// Session actions overlay the row without changing its footprint. The primary
// label only yields their width while the row is hovered or contains focus.
const SESSION_ACTION_CLASS =
	"grid size-5 shrink-0 place-items-center rounded-md bg-transparent p-1 text-passive hover:bg-transparent focus:bg-transparent focus-visible:bg-transparent active:bg-transparent data-[state=open]:bg-transparent hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50 disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-3!";

// Shared nav-row chrome (Codex-style): inset pill hover/selected, 14px type, no accent bar.
const NAV_ROW_CLASS =
	"h-9 gap-2.5 rounded-lg px-2.5 text-sm font-medium text-muted-foreground transition-[background-color,color] hover:bg-interactive-hover hover:text-foreground active:bg-interactive-hover active:text-foreground data-[active=true]:bg-interactive-active data-[active=true]:font-medium data-[active=true]:text-foreground";

// Search + Pinned/Projects section chrome: same type, icon, and row size.
const SECTION_ROW_CLASS =
	"flex h-8 w-full min-w-0 items-center gap-2 rounded-md px-2.5 text-sm font-medium text-passive [&_svg]:size-icon-md [&_svg]:shrink-0";
// Hover fill only for collapsible section headers (Pinned). Projects is a static label.
const SECTION_ROW_INTERACTIVE_CLASS = "transition-colors hover:bg-interactive-hover hover:text-foreground";
const PROJECT_DRAG_OVERLAY_STYLE: CSSProperties = { willChange: "transform" };

// Mirrors the daemon's display-name cap (maxDisplayNameLen) and the spawn
// `--name` flag, so inline edits never round-trip a value the API would reject.

// Reorder drags start from the row's primary click surface. The 4px activation
// distance keeps a plain navigation/disclosure click from starting a drag;
// nested action buttons remain outside that activator surface.
const REORDER_ACTIVATION_DISTANCE = 4;

/** Stable drag-context ids: one for the project list, one per project's sessions. */
export const PROJECT_DND_ID = "sidebar-projects";
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

// Session drags use their owning list as the visual boundary. Project drags use
// row-derived bounds below because their list fills the remaining sidebar height.
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

type DragBounds = { minY: number; maxY: number };
type ProjectDropPlacement = "before" | "after";

// Each full project block is one droppable. Pointer intersection covers the
// row and expanded sessions; the fallback exposes the outermost top/bottom
// boundaries and the tiny gaps between adjacent blocks.
const projectBlockCollision: CollisionDetection = (args) => {
	const direct = pointerWithin(args);
	if (direct.length > 0 || !args.pointerCoordinates) return direct;
	const { x, y } = args.pointerCoordinates;
	let closest: {
		container: (typeof args.droppableContainers)[number];
		rect: NonNullable<ReturnType<typeof args.droppableRects.get>>;
		distance: number;
	} | null = null;
	let listLeft = Number.POSITIVE_INFINITY;
	let listRight = Number.NEGATIVE_INFINITY;
	for (const container of args.droppableContainers) {
		const rect = args.droppableRects.get(container.id);
		if (!rect) continue;
		listLeft = Math.min(listLeft, rect.left);
		listRight = Math.max(listRight, rect.right);
		const distance = y < rect.top ? rect.top - y : y > rect.bottom ? y - rect.bottom : 0;
		if (!closest || distance < closest.distance) closest = { container, rect, distance };
	}
	if (!closest || x < listLeft || x > listRight) return [];
	return [{
		id: closest.container.id,
		data: { droppableContainer: closest.container, value: closest.distance },
	}];
};

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
export const SIDEBAR_MIN_WIDTH = 200;
export const SIDEBAR_MAX_WIDTH = 420;
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
	onRemoveProject: (projectId: string) => Promise<void>;
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
			className={cn(
				"size-2 shrink-0 rounded-full",
				dot.className,
				dot.breathe && "animate-status-pulse",
			)}
			data-session-status={session.status}
		/>
	);
}

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

	const [projectOrder, setProjectOrder] = useState<string[]>([]);
	const [sessionOrderByProject, setSessionOrderByProject] = useState<Record<string, string[]>>({});
	const orderedWorkspaces = useMemo(
		() => applyOrder(workspaces, (workspace) => workspace.id, projectOrder, "end"),
		[projectOrder, workspaces],
	);
	const projectIds = useMemo(() => orderedWorkspaces.map((workspace) => workspace.id), [orderedWorkspaces]);
	const reorderSensors = useReorderSensors();
	const projectDragClickGuard = usePostDragClickGuard();
	const [draggingProjectId, setDraggingProjectId] = useState<string | null>(null);
	const projectDragBoundsRef = useRef<DragBounds | null>(null);
	const projectDropTargetRef = useRef<{ overId: string; placement: ProjectDropPlacement } | null>(null);
	const projectDropNodesRef = useRef(new Map<string, HTMLElement>());
	useGrabbingCursor(draggingProjectId !== null);

	const activeDragWorkspace = useMemo(
		() => orderedWorkspaces.find((workspace) => workspace.id === draggingProjectId) ?? null,
		[draggingProjectId, orderedWorkspaces],
	);
	const activeDragSessions = useMemo(
		() => activeDragWorkspace
			? applyOrder(
				sortedWorkerSessions(activeDragWorkspace.sessions).filter((session) => session.isTerminated !== true),
				(session) => session.id,
				sessionOrderByProject[activeDragWorkspace.id] ?? [],
				"start",
			)
			: [],
		[activeDragWorkspace, sessionOrderByProject],
	);
	const recordSessionOrder = useCallback((projectId: string, order: string[]) => {
		setSessionOrderByProject((previous) => ({ ...previous, [projectId]: order }));
	}, []);
	const restrictProjectOverlayToRows = useCallback<Modifier>(({ transform }) => {
		const bounds = projectDragBoundsRef.current;
		return {
			...transform,
			x: 0,
			y: bounds ? Math.min(bounds.maxY, Math.max(bounds.minY, transform.y)) : transform.y,
			scaleX: 1,
			scaleY: 1,
		};
	}, []);

	const commitProjectOrder = useCallback((next: string[] | null) => {
		if (next) setProjectOrder(next);
	}, []);
	const setProjectDropIndicator = useCallback((next: { overId: string; placement: ProjectDropPlacement } | null) => {
		const previous = projectDropTargetRef.current;
		if (previous && (previous.overId !== next?.overId || previous.placement !== next?.placement)) {
			projectDropNodesRef.current.get(previous.overId)?.removeAttribute("data-drop-indicator");
		}
		if (next && (previous?.overId !== next.overId || previous.placement !== next.placement)) {
			projectDropNodesRef.current.get(next.overId)?.setAttribute("data-drop-indicator", next.placement);
		}
		projectDropTargetRef.current = next;
	}, []);

	const onProjectDragEnd = useCallback(
		({ active, over }: DragEndEvent) => {
			const projectId = String(active.id);
			projectDragClickGuard.markDragEnded(projectId);
			if (over) {
				const targetId = String(over.id);
				const placement = projectDropTargetRef.current?.overId === targetId
					? projectDropTargetRef.current.placement
					: projectIds.indexOf(projectId) < projectIds.indexOf(targetId) ? "after" : "before";
				commitProjectOrder(reorderAtProjectBoundary(projectIds, projectId, targetId, placement));
			}
			projectDragBoundsRef.current = null;
			setProjectDropIndicator(null);
			projectDropNodesRef.current.clear();
			setDraggingProjectId(null);
		},
		[commitProjectOrder, projectDragClickGuard, projectIds, setProjectDropIndicator],
	);
	const onProjectDragStart = useCallback(({ active }: DragStartEvent) => {
		const projectId = String(active.id);
		projectDragBoundsRef.current = null;
		projectDropTargetRef.current = null;
		const blocks = Array.from(document.querySelectorAll<HTMLElement>("[data-project-drop-target]"));
		projectDropNodesRef.current = new Map(blocks.map((block) => [block.dataset.projectId ?? "", block]));
		const activeRow = blocks.find((block) => block.dataset.projectId === projectId)
			?.querySelector<HTMLElement>("[data-project-drag-row]");
		if (activeRow && blocks.length > 0) {
			const activeTop = activeRow.getBoundingClientRect().top;
			projectDragBoundsRef.current = {
				minY: blocks[0].getBoundingClientRect().top - activeTop,
				maxY: blocks[blocks.length - 1].getBoundingClientRect().bottom - activeTop,
			};
		}
		setDraggingProjectId(projectId);
	}, []);
	const updateProjectDropTarget = useCallback(({ active, activatorEvent, delta, over }: DragMoveEvent | DragOverEvent) => {
		const activeId = String(active.id);
		const overId = over ? String(over.id) : null;
		if (!over || activeId === overId) {
			setProjectDropIndicator(null);
			return;
		}
		const pointerY = activatorEvent && "clientY" in activatorEvent && typeof activatorEvent.clientY === "number"
			? activatorEvent.clientY + delta.y
			: null;
		const activeRect = active.rect.current.translated ?? active.rect.current.initial;
		const activeCenter = activeRect ? activeRect.top + activeRect.height / 2 : null;
		const boundaryReference = pointerY ?? activeCenter;
		const placement: ProjectDropPlacement = boundaryReference === null
			? projectIds.indexOf(activeId) < projectIds.indexOf(overId!) ? "after" : "before"
			: boundaryReference <= over.rect.top + over.rect.height / 2 ? "before" : "after";
		const changesOrder = reorderAtProjectBoundary(projectIds, activeId, overId!, placement) !== null;
		setProjectDropIndicator(changesOrder ? { overId: overId!, placement } : null);
	}, [projectIds, setProjectDropIndicator]);
	const onProjectDragCancel = useCallback(() => {
		projectDragBoundsRef.current = null;
		setProjectDropIndicator(null);
		projectDropNodesRef.current.clear();
		setDraggingProjectId(null);
	}, [setProjectDropIndicator]);

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

	return (
		// Pinned sidebars start below shell chrome.
		<SidebarRoot
			collapsible="offcanvas"
			data-expanded-chrome={expandedChromeVisible ? "visible" : "hidden"}
			data-topbar-offset={underTopbar ? topbarOffset : undefined}
			className={cn(
				"sidebar-focusless",
				hideEdgeBorder ? "border-transparent" : "border-r-0 group-data-[side=left]:border-r-0",
				!underTopbar
					? "top-0 h-svh!"
					: "top-(--sidebar-chrome-offset) h-[calc(100svh-var(--sidebar-chrome-offset))]!",
			)}
		>
			<SidebarHeader className="gap-0 p-0 px-3 pt-2 group-data-[collapsible=icon]:px-1.5 group-data-[collapsible=icon]:pt-2">
				{/* Brand (project-sidebar__brand); in the icon rail it becomes the old
            36px board button wrapping the 22px accent mark. */}
				<div
					className={cn(
						"group/brand flex shrink-0 items-center gap-1.5 rounded-md px-0.5 group-data-[collapsible=icon]:flex-col group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:gap-1 group-data-[collapsible=icon]:px-0 group-data-[collapsible=icon]:pb-2",
						commandPaletteEnabled ? "pb-2" : "pb-3",
					)}
				>
					<span
						className={cn(
							"grid h-5.5 w-5.5 shrink-0 place-items-center",
							"group-data-[collapsible=icon]:size-control-board group-data-[collapsible=icon]:rounded-lg",
						)}
					>
						<img src={aoLogo} alt="" aria-hidden="true" className="h-5.5 w-5.5 -translate-y-[3px] rounded-md object-cover" />
					</span>
					{isWindows ? (
						<span
							className="sidebar-expanded-chrome min-w-0 flex-1 truncate text-sm font-bold leading-tight tracking-tight-lg text-foreground group-data-[collapsible=icon]:hidden"
						>
							Agent Orchestrator
						</span>
					) : (
						<span
							className="sidebar-expanded-chrome min-w-0 flex-1 truncate text-sm font-bold leading-tight tracking-tight-lg text-foreground group-data-[collapsible=icon]:hidden"
						>
							Agent Orchestrator
						</span>
					)}
					{isNightly && (
						<span className="sidebar-expanded-chrome shrink-0 rounded-full bg-purple-subtle px-1.5 py-0.5 text-micro font-semibold leading-none text-purple-accent group-data-[collapsible=icon]:hidden">
							{t("shell.nightly")}
						</span>
					)}
				</div>
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
								<PinnedSessionRow
									key={session.id}
									session={session}
									active={selection.activeSessionId === session.id}
									onOpenSession={selection.goSession}
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
								onCloneProject={onCloneProject}
								onCreateProject={onCreateProject}
								onInitializeProject={onInitializeProject}
							/>
						}
					/>
				</div>
			</div>

			<SidebarContent className="project-sidebar-scrollbar gap-0 px-2 group-data-[collapsible=icon]:items-center group-data-[collapsible=icon]:px-1.5">
				<SidebarGroup className="min-h-full p-0">
					{/* Tree (project-sidebar__tree) */}
					<SidebarGroupContent className="min-h-full">
						{workspaceError ? (
							<div className="sidebar-expanded-chrome px-2.5 py-3 group-data-[collapsible=icon]:hidden">
								<p className="text-sm text-foreground">{t("shell.couldNotLoadProjects")}</p>
								<p className="mt-1 text-caption text-passive">{workspaceError}</p>
							</div>
						) : workspaces.length === 0 ? null : (
							<DndContext
								collisionDetection={projectBlockCollision}
								id={PROJECT_DND_ID}
								onDragStart={onProjectDragStart}
								onDragMove={updateProjectDropTarget}
								onDragOver={updateProjectDropTarget}
								onDragCancel={onProjectDragCancel}
								onDragEnd={onProjectDragEnd}
								sensors={reorderSensors}
							>
								<SidebarMenu className="min-h-full gap-0.5 rounded-lg group-data-[collapsible=icon]:gap-1 group-data-[collapsible=icon]:rounded-none">
									{orderedWorkspaces.map((workspace) => (
										<ProjectItem
											key={workspace.id}
											workspace={workspace}
											expanded={expandedIds.has(workspace.id) || (initialActiveSessionProjectId === workspace.id && !dismissedInitialActiveProjectIds.has(workspace.id))}
											suppressInitialExpandAnimation={expandedIds.has(workspace.id)}
											selection={selection}
											draggingProjectId={draggingProjectId}
											consumeDragClick={projectDragClickGuard.consumeClick}
											onSessionOrderChange={recordSessionOrder}
											onToggle={toggleProjectDisclosure}
											onRemoveProject={onRemoveProject}
										/>
									))}
									{isCollapsed && <CreateProjectListItem />}
								</SidebarMenu>
								<DragOverlay adjustScale={false} dropAnimation={null} modifiers={[restrictProjectOverlayToRows]} style={PROJECT_DRAG_OVERLAY_STYLE} zIndex={60}>
									{activeDragWorkspace ? (
										<ProjectDragPreview
											expanded={
												!isCollapsed &&
												(expandedIds.has(activeDragWorkspace.id) ||
													(initialActiveSessionProjectId === activeDragWorkspace.id &&
														!dismissedInitialActiveProjectIds.has(activeDragWorkspace.id)))
											}
											selection={selection}
											sessions={activeDragSessions}
											workspace={activeDragWorkspace}
										/>
									) : null}
								</DragOverlay>
							</DndContext>
						)}
					</SidebarGroupContent>
				</SidebarGroup>
			</SidebarContent>

			{/* Footer — Settings opens the global settings page directly.
			    Its hairline and row height match the board Archive bar. Bottom
			    spacing stays inside the footer so there is no empty strip beneath
			    the final action. */}
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
						onRequestInstall={openUpdateInstallPrompt}
						status={updateStatus}
						tabIndex={isCollapsed ? -1 : 0}
					/>
					<CloudSignInRow tabIndex={isCollapsed ? -1 : 0} />
					<CloudAccountRow tabIndex={isCollapsed ? -1 : 0} />
					<button
						aria-label={t("settings.connectMobile")}
						className={cn(
							NAV_ROW_CLASS,
							"flex h-9 w-full items-center text-left [&_svg]:size-icon-md [&_svg]:shrink-0",
						)}
						onClick={() => selection.goConnectMobile()}
						tabIndex={isCollapsed ? -1 : 0}
						type="button"
					>
						<Smartphone aria-hidden="true" />
						<span className="tracking-tight">{t("settings.connectMobile")}</span>
					</button>
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
								className="grid size-control-board place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground [&_svg]:size-icon-base"
								onClick={() => selection.goConnectMobile()}
								tabIndex={isCollapsed ? 0 : -1}
								type="button"
							>
								<Smartphone aria-hidden="true" />
							</button>
						</TooltipTrigger>
						<TooltipContent side="right">{t("settings.connectMobile")}</TooltipContent>
					</Tooltip>
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

type ProjectItemProps = {
	workspace: WorkspaceSummary;
	expanded: boolean;
	selection: Selection;
	draggingProjectId?: string | null;
	consumeDragClick: (id: string) => boolean;
	onSessionOrderChange: (projectId: string, order: string[]) => void;
	onToggle: (projectId: string) => void;
	onRemoveProject: (projectId: string) => Promise<void>;
	suppressInitialExpandAnimation: boolean;
};

type ProjectDraggable = ReturnType<typeof useDraggable>;
type ProjectItemDndProps = Pick<ProjectDraggable, "listeners" | "setActivatorNodeRef"> & {
	setDraggableNodeRef: ProjectDraggable["setNodeRef"];
	setDroppableNodeRef: ReturnType<typeof useDroppable>["setNodeRef"];
};

// Keep the pointer-frequency draggable subscription outside the expensive
// project/session subtree. The content only rerenders when its visible props
// change (drag start/end or a different drop boundary), not for every transform.
const ProjectItem = memo(function ProjectItem(props: ProjectItemProps) {
	const draggable = useDraggable({
		id: props.workspace.id,
	});
	const droppable = useDroppable({
		id: props.workspace.id,
	});
	// dnd-kit refreshes the objects returned by these hooks as the pointer moves.
	// Keep that high-frequency churn in this thin wrapper: the project content
	// contains the expanded session tree and must not receive new props merely
	// because the cursor crossed another project.
	const draggableRef = useRef(draggable);
	const droppableRef = useRef(droppable);
	draggableRef.current = draggable;
	droppableRef.current = droppable;
	const listeners = useMemo<ProjectDraggable["listeners"]>(
		() => ({
			onPointerDown: (event: ReactPointerEvent<HTMLElement>) =>
				draggableRef.current.listeners?.onPointerDown?.(event),
		}),
		[],
	);
	const setActivatorNodeRef = useCallback(
		(node: HTMLElement | null) => draggableRef.current.setActivatorNodeRef(node),
		[],
	);
	const setDraggableNodeRef = useCallback(
		(node: HTMLElement | null) => draggableRef.current.setNodeRef(node),
		[],
	);
	const setDroppableNodeRef = useCallback(
		(node: HTMLElement | null) => droppableRef.current.setNodeRef(node),
		[],
	);
	return (
		<ProjectItemContent
			{...props}
			listeners={listeners}
			setActivatorNodeRef={setActivatorNodeRef}
			setDraggableNodeRef={setDraggableNodeRef}
			setDroppableNodeRef={setDroppableNodeRef}
		/>
	);
});

const ProjectItemContent = memo(function ProjectItemContent({
	workspace,
	expanded,
	selection,
	draggingProjectId,
	consumeDragClick,
	onSessionOrderChange,
	onToggle,
	onRemoveProject,
	suppressInitialExpandAnimation,
	listeners,
	setActivatorNodeRef,
	setDraggableNodeRef,
	setDroppableNodeRef,
}: ProjectItemProps & ProjectItemDndProps) {
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
	// Skip enter animation on first mount — sessions arrive async and we don't
	// want them to slide in on every sidebar load. Only animate on subsequent
	// expand/collapse toggles.
	const [animReady, setAnimReady] = useState(false);
	const hasInteractedWithDisclosure = useRef(false);
	useEffect(() => {
		const id = requestAnimationFrame(() => setAnimReady(true));
		return () => cancelAnimationFrame(id);
	}, []);
	const isProjectRestarting = useUiStore((state) => state.restartingProjectIds.has(workspace.id));
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	const projectIsDragging = draggingProjectId === workspace.id;
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
	const sessionLayoutDependency = useMemo(() => sessionIds.join("\u0000"), [sessionIds]);
	// Project and session reordering use nested DnD contexts. While a project is
	// being dragged, leave the session lists as plain rows: otherwise every
	// expanded project's DnD context measures its sortable descendants on drop.
	// With a dense sidebar that turns one project drop into a full-tree layout.
	const projectDragInProgress = draggingProjectId !== null && draggingProjectId !== undefined;
	const sessionSensors = useReorderSensors();
	const sessionDragClickGuard = usePostDragClickGuard();
	const [sessionDragging, setSessionDragging] = useState(false);
	const [dropTransitionDisabledId, setDropTransitionDisabledId] = useState<string | null>(null);

	const commitSessionOrder = useCallback((next: string[] | null) => {
		if (!next) return;
		setSessionOrder(next);
		onSessionOrderChange(workspace.id, next);
	}, [onSessionOrderChange, workspace.id]);

	const onSessionDragEnd = useCallback(({ active, over }: DragEndEvent) => {
		const sessionId = String(active.id);
		sessionDragClickGuard.markDragEnded(sessionId);
		if (!over) {
			setSessionDragging(false);
			setDropTransitionDisabledId(null);
			if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
			return;
		}
		// reorderById rejects any id that is not in THIS project's list, so a stray
		// cross-project drop leaves both projects' orders untouched.
		const next = reorderById(sessionIds, sessionId, String(over.id));
		// Commit the destination DOM order before dnd-kit removes its live transform.
		// Otherwise the row briefly snaps back to its derived (usually top) position,
		// then Motion animates it forward to the persisted destination.
		flushSync(() => {
			commitSessionOrder(next);
			setSessionDragging(false);
			setDropTransitionDisabledId(sessionId);
		});
		requestAnimationFrame(() => setDropTransitionDisabledId(null));
		if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
	}, [commitSessionOrder, sessionDragClickGuard, sessionIds]);
	const onSessionDragCancel = useCallback(() => {
		setSessionDragging(false);
		setDropTransitionDisabledId(null);
		if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
	}, []);
	const openSession = useCallback((sessionId: string) => {
		selection.goSession(workspace.id, sessionId);
	}, [selection, workspace.id]);
	// The project's live orchestrator (if any) backs the hover Orchestrator
	// button: navigate to it when present, otherwise spawn one first.
	const orchestrator = newestActiveOrchestrator(workspace.sessions);
	const toggleDisclosure = () => {
		hasInteractedWithDisclosure.current = true;
		onToggle(workspace.id);
	};

	// Mirrors ShellTopbar's launcher: attach to the running orchestrator, or
	// spawn one via the daemon and follow it once the workspace refetches.
	// Expand a collapsed project so opening the orchestrator also reveals its
	// session list — otherwise the tree stays shut while you're inside it.
	const openOrchestrator = async () => {
		if (isProjectRestarting) return;
		if (!expanded) toggleDisclosure();
		if (orchestrator) {
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

	// Expanded + already on the project board → collapse. Expanded + on a
	// session (orchestrator or worker) → board. Collapsed → expand + board.
	// Do not treat orchestratorActive like the board: the project row is the
	// one-click path back from the orchestrator button.
	const onProjectClick = () => {
		if (consumeDragClick(workspace.id)) return;
		if (workspace.kind === STANDALONE_PROJECT_KIND) {
			toggleDisclosure();
			if (!expanded) selection.goHome();
			return;
		}
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
				<motion.li
					className={cn(
						"group/menu-item relative group-data-[collapsible=icon]:mb-0",
						projectIsDragging && "opacity-0",
					)}
					data-dragging={projectIsDragging ? "true" : undefined}
					data-project-drop-target=""
					data-project-id={workspace.id}
					data-drop-indicator={undefined}
					data-sidebar="menu-item"
					data-slot="sidebar-menu-item"
					layout={draggingProjectId ? false : "position"}
					ref={setDroppableNodeRef}
					transition={prefersReducedMotion ? { duration: 0 } : { type: "spring", stiffness: 520, damping: 42, mass: 0.55 }}
				>
					<div
						aria-hidden="true"
						className="pointer-events-none absolute inset-x-0 top-0 z-[70] h-px bg-foreground opacity-0 group-data-[drop-indicator=before]/menu-item:opacity-100"
						data-project-drop-indicator="before"
					/>
					<div
						aria-hidden="true"
						className="pointer-events-none absolute inset-x-0 bottom-0 z-[70] h-px bg-foreground opacity-0 group-data-[drop-indicator=after]/menu-item:opacity-100"
						data-project-drop-indicator="after"
					/>
					{/* The whole visual row scales when its navigation surface is pressed.
		    Action-button presses stop before reaching this boundary. */}
					<div
						className="relative"
						data-project-drag-row=""
						data-project-id={workspace.id}
						ref={setDraggableNodeRef}
					>
						<div
							className={cn(
								"relative transition-[transform] duration-[100ms] ease-out",
								projectPressed && !projectIsDragging && "scale-[0.98]",
								projectIsDragging && "cursor-grabbing transition-none",
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
									{...listeners}
									onClick={onProjectClick}
									ref={setActivatorNodeRef}
									className={cn(
										NAV_ROW_CLASS,
										// gap-2 matches SectionDisclosure so project icons/labels share the
										// Projects header's left edge (NAV_ROW defaults to gap-2.5).
										"cursor-grab gap-2 pr-sidebar-project-actions active:cursor-grabbing [&_svg]:size-icon-md",
										"transition-none",
										projectIsDragging && "!cursor-grabbing",
										draggingProjectId && "hover:bg-transparent hover:text-muted-foreground active:bg-transparent active:text-muted-foreground",
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
										<span
											className={cn(
												"inline-flex size-icon-md items-center justify-center transition-opacity duration-150 group-hover/menu-item:opacity-0",
												draggingProjectId && "group-hover/menu-item:opacity-100",
											)}
										>
											{expanded ? <FolderOpen strokeWidth={1.75} /> : <Folder strokeWidth={1.75} />}
										</span>
										<span
											className={cn(
												"absolute inline-flex size-icon-md items-center justify-center opacity-0 transition-[opacity,transform] duration-150 group-hover/menu-item:opacity-100",
												expanded && "rotate-90",
												draggingProjectId && "group-hover/menu-item:opacity-0",
											)}
										>
											<ChevronRight strokeWidth={1.75} />
										</span>
									</span>
									{/* Collapsed icon rail: folder icon */}
									<span
										aria-hidden="true"
										className="hidden group-data-[collapsible=icon]:inline-flex size-8 items-center justify-center text-muted-foreground"
									>
										{expanded ? <FolderOpen className="size-5" strokeWidth={1.75} /> : <Folder className="size-5" strokeWidth={1.75} />}
									</span>
									<span
										className="sidebar-expanded-chrome min-w-0 flex-1 translate-y-px truncate group-data-[collapsible=icon]:hidden"
										data-project-label=""
									>
										{workspace.name}
									</span>
									{workspace.kind === "cloud" && (
										<Badge
											variant="outline"
											className="sidebar-expanded-chrome h-4 shrink-0 px-1.5 text-2xs group-data-[collapsible=icon]:hidden"
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
									className="absolute inset-y-0 left-0 z-10 w-9 cursor-pointer group-data-[collapsible=icon]:hidden"
									data-project-folder=""
									{...listeners}
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
									draggingProjectId && "pointer-events-none",
								)}
								data-project-actions=""
								onClick={(event) => event.stopPropagation()}
								onPointerDown={(event) => event.stopPropagation()}
							>
								{workspace.kind !== STANDALONE_PROJECT_KIND && <Tooltip>
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
												disabled={isSpawning || isProjectRestarting}
												onClick={() => void openOrchestrator()}
												type="button"
											>
												<OrchestratorIcon aria-hidden="true" strokeWidth={orchestratorActive ? 2.5 : 2} />
											</button>
										</span>
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
								</Tooltip>}
								<DropdownMenu>
									<Tooltip>
										<TooltipTrigger asChild>
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
										</TooltipTrigger>
										<TooltipContent>
											{t("shell.projectActions", {
												name: workspace.name,
											})}
										</TooltipContent>
									</Tooltip>
									<DropdownMenuContent side="right" align="start" className="min-w-44">
										<DropdownMenuItem disabled={isProjectRestarting} onSelect={() => requestNewTask(workspace.id)}>
											<Plus aria-hidden="true" />
											{t("shell.newSession")}
										</DropdownMenuItem>
										{workspace.kind !== STANDALONE_PROJECT_KIND && <>
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
										</>}
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
						animReady && (!suppressInitialExpandAnimation || hasInteractedWithDisclosure.current) ? { height: 0 } : false
					}
					animate={{ height: "auto" }}
					exit={{ height: 0 }}
					transition={prefersReducedMotion ? { duration: 0 } : { duration: 0.14, ease: [0.25, 0.46, 0.45, 0.94] }}
					style={{ overflow: "hidden" }}
					className="sidebar-expanded-chrome"
				>
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
											{projectDragInProgress ? (
												<SidebarMenuSub
													className="mx-0 ml-3.5 translate-x-0 gap-px border-l-0 px-0 py-1"
													data-testid={`session-list-${workspace.id}`}
												>
													{sessions.map((session) => (
														<SessionRow
															key={session.id}
															session={session}
															active={selection.activeSessionId === session.id}
															disableLayout
															onOpen={() => openSession(session.id)}
														/>
													))}
												</SidebarMenuSub>
											) : (
												<DndContext
													collisionDetection={closestCenter}
													modifiers={[restrictToListBounds]}
													id={sessionDndId(workspace.id)}
													onDragStart={() => setSessionDragging(true)}
													onDragCancel={onSessionDragCancel}
													onDragEnd={onSessionDragEnd}
													sensors={sessionSensors}
												>
													<SortableContext items={sessionIds} strategy={verticalListSortingStrategy}>
														<SidebarMenuSub
															className="mx-0 ml-3.5 translate-x-0 gap-px border-l-0 px-0 py-1"
															data-testid={`session-list-${workspace.id}`}
														>
															{sessions.map((session) => (
																<SortableSessionRow
																	key={session.id}
																	session={session}
																	active={selection.activeSessionId === session.id}
																	consumeDragClick={sessionDragClickGuard.consumeClick}
																	layoutDependency={sessionLayoutDependency}
																	listIsDragging={sessionDragging}
																	dropTransitionDisabled={dropTransitionDisabledId === session.id}
																	onOpen={openSession}
																/>
															))}
														</SidebarMenuSub>
													</SortableContext>
												</DndContext>
											)}
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
								<p className="text-sm font-medium text-foreground">{t("shell.removeProjectLead", { name: workspace.name })}</p>
								<p className="mt-1 text-xs text-muted-foreground">{t("shell.removeProjectBody")}</p>
							</>
						}
						confirmLabel={t("shell.remove")}
						destructive
						onConfirm={handleConfirmRemove}
					/>
				</motion.li>
			</ContextMenuTrigger>
			<ContextMenuContent className="min-w-44">
				<ContextMenuItem disabled={isProjectRestarting} onSelect={() => requestNewTask(workspace.id)}>
					<Plus aria-hidden="true" />
					{t("shell.newSession")}
				</ContextMenuItem>
				{workspace.kind !== STANDALONE_PROJECT_KIND && <>
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
				</>}
			</ContextMenuContent>
		</ContextMenu>
	);
});

/** Non-interactive drag snapshot: the project row is the anchor, while its
 * visible sessions travel with it without becoming collision targets. */
const ProjectDragPreview = memo(function ProjectDragPreview({ workspace, expanded, selection, sessions }: { workspace: WorkspaceSummary; expanded: boolean; selection: Selection; sessions: WorkspaceSession[] }) {
	const { t } = useTranslation();
	const activeProjectMatches = selection.activeProjectId === workspace.id;
	const projectActive =
		(activeProjectMatches && !selection.activeSessionId) ||
		(activeProjectMatches && workspace.sessions.some((session) => session.id === selection.activeSessionId && session.kind === "orchestrator"));

	return (
		<div className="pointer-events-none w-full cursor-grabbing" data-project-drag-overlay="">
			<div
				className={cn(NAV_ROW_CLASS, "flex w-full cursor-grabbing items-center gap-2 pr-sidebar-project-actions [&_svg]:size-icon-md")}
				data-active={projectActive}
			>
				<span className="inline-flex size-icon-md shrink-0 translate-y-px items-center justify-center text-muted-foreground">
					{expanded ? <FolderOpen strokeWidth={1.75} /> : <Folder strokeWidth={1.75} />}
				</span>
				<span className="min-w-0 flex-1 translate-y-px truncate">{workspace.name}</span>
			</div>
			{expanded && sessions.length > 0 ? (
				<div className="ml-3.5 py-1">
					{sessions.map((session) => {
						const switchPresentation = deriveSessionAgentSwitchPresentation(session);
						const switchLabel = switchPresentation ? t(switchPresentation.compactLabelKey, switchPresentation.values) : undefined;
						const active = selection.activeSessionId === session.id;
						return (
							<div className="pl-0.5" data-project-drag-preview-session="" key={session.id}>
								<div className={cn("flex h-8 w-full items-center rounded-lg", active && "bg-interactive-active text-foreground")}>
									<div className="flex h-8 min-w-0 flex-1 items-center gap-1.5 py-0 pl-1.5 pr-2.5 text-sm">
										<SessionStatusDot session={session} />
										<span className="flex min-w-0 flex-1 items-center gap-1.5">
											<span className={cn("min-w-0 flex-1 truncate", active ? "text-foreground" : "text-muted-foreground")}>
												{session.title}
											</span>
											{switchLabel ? (
												<span className="max-w-28 shrink-0 truncate text-2xs text-muted-foreground">{switchLabel}</span>
											) : null}
										</span>
									</div>
								</div>
							</div>
						);
					})}
				</div>
			) : null}
		</div>
	);
});

const PinnedSessionRow = memo(function PinnedSessionRow({
	session,
	active,
	onOpenSession,
}: {
	session: WorkspaceSession;
	active: boolean;
	onOpenSession: (projectId: string, sessionId: string) => void;
}) {
	const onOpen = useCallback(() => onOpenSession(session.workspaceId, session.id), [onOpenSession, session.id, session.workspaceId]);
	return <SessionRow session={session} active={active} indented={false} onOpen={onOpen} />;
});

// A session row inside its project's drag context. The Pinned section renders
// plain SessionRows instead: that list is ordered by pin time, not by hand.
const SortableSessionRow = memo(function SortableSessionRow({
	session,
	active,
	consumeDragClick,
	layoutDependency,
	listIsDragging,
	dropTransitionDisabled,
	onOpen,
}: {
	session: WorkspaceSession;
	active: boolean;
	consumeDragClick: (id: string) => boolean;
	layoutDependency: string;
	listIsDragging: boolean;
	dropTransitionDisabled: boolean;
	onOpen: (sessionId: string) => void;
}) {
	const { isDragging, listeners, setActivatorNodeRef, setNodeRef, transform, transition } = useSortable({
		id: session.id,
	});
	return (
		<SessionRow
			session={session}
			active={active}
			onOpen={() => {
				if (!consumeDragClick(session.id)) onOpen(session.id);
			}}
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
	const [sessionPressed, setSessionPressed] = useState(false);
	const lastTouchAtRef = useRef(0);
	const suppressTouchOpenRef = useRef(false);
	const pendingOpenRef = useRef<number | null>(null);
	const cancelPendingOpen = useCallback(() => {
		if (pendingOpenRef.current === null) return;
		window.clearTimeout(pendingOpenRef.current);
		pendingOpenRef.current = null;
	}, []);
	useEffect(() => cancelPendingOpen, [cancelPendingOpen]);
	const beginRename = useCallback(() => {
		cancelPendingOpen();
		rename.begin();
	}, [cancelPendingOpen, rename.begin]);

	if (rename.isEditing) {
		return (
			<SidebarMenuSubItem className={cn(indented && "pl-0.5")}>
				<div
					className={cn(
						"relative flex h-8 w-full items-center gap-1.5 rounded-lg py-0 pl-1.5 pr-1",
						active && "bg-interactive-active text-foreground",
					)}
					data-session-row=""
				>
					<SessionStatusDot session={session} />
					<input
						aria-label={t("shell.renameSession", { title: session.title })}
						autoFocus
						className={cn(
							"h-full min-w-0 flex-1 appearance-none border-0 bg-transparent! p-0 text-sm text-foreground outline-none ring-0 focus:outline-none focus:ring-0",
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
						"group/session-row flex h-8 w-full items-center rounded-lg transition-[transform] duration-[100ms] ease-out",
						"hover:bg-interactive-hover hover:text-foreground",
						active && "bg-interactive-active text-foreground",
						sessionPressed && !reorder?.isDragging && "scale-[0.97]",
						reorder?.isDragging && "transition-none",
					)}
					data-session-press=""
					data-session-row=""
					data-dragging={reorder?.isDragging ? "true" : undefined}
					onPointerCancel={() => setSessionPressed(false)}
					onPointerDown={() => setSessionPressed(true)}
					onPointerLeave={() => setSessionPressed(false)}
					onPointerUp={() => setSessionPressed(false)}
				>
					<div className={cn("flex min-w-0 flex-1", reorder?.isDragging && "cursor-grabbing")}>
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
								if (event.detail === 0) {
									cancelPendingOpen();
									onOpen();
									return;
								}
								if (event.detail > 1) {
									cancelPendingOpen();
									return;
								}
								if (suppressTouchOpenRef.current) {
									suppressTouchOpenRef.current = false;
									return;
								}
								// Wait for the native double-click window before navigating. A
								// second click cancels this so inline rename has no route side effect.
								cancelPendingOpen();
								pendingOpenRef.current = window.setTimeout(() => {
									pendingOpenRef.current = null;
									onOpen();
								}, 500);
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
			className="absolute inset-y-0 right-1.5 flex min-w-0 shrink-0 items-center whitespace-nowrap font-mono text-micro text-passive opacity-100 transition-opacity duration-100 ease-out group-hover/session-row:opacity-0 group-focus-within/session-row:opacity-0"
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
}: {
	session: WorkspaceSession;
	isDragging: boolean;
}) {
	const { t } = useTranslation();
	const { mutate: pinSession } = usePinSession();
	const { mutate: unpinSession } = useUnpinSession();
	const { mutate: terminateSession, isPending: isKilling } = useTerminateSession();

	return (
		<div
			className="pointer-events-none absolute inset-y-0 right-0 z-chrome"
			data-session-actions=""
			onPointerDown={(event) => event.stopPropagation()}
		>
			<div
				className={cn(
					"absolute inset-y-0 right-0.5 flex items-center gap-px opacity-0 transition-opacity duration-100 ease-out",
					!isDragging &&
						"group-hover/session-row:pointer-events-auto group-hover/session-row:opacity-100 group-focus-within/session-row:pointer-events-auto group-focus-within/session-row:opacity-100",
				)}
				data-session-action-buttons=""
			>
				<button
					aria-label={session.isPinned ? t("shell.unpinSession") : t("shell.pinSession")}
					className={cn(SESSION_ACTION_CLASS, session.isPinned && "text-foreground")}
					onClick={(event) => {
						event.stopPropagation();
						session.isPinned ? unpinSession(session) : pinSession(session);
					}}
					type="button"
				>
					{session.isPinned ? <PinOff aria-hidden="true" /> : <Pin aria-hidden="true" />}
				</button>
				<button
					aria-label={t("shell.killSession")}
					className={cn(SESSION_ACTION_CLASS, "hover:text-destructive")}
					disabled={isKilling}
					onClick={(event) => {
						event.stopPropagation();
						terminateSession(session);
					}}
					type="button"
				>
					<Trash2 aria-hidden="true" />
				</button>
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
			className={cn(
				NAV_ROW_CLASS,
				"flex h-9 w-full items-center text-left [&_svg]:size-icon-md [&_svg]:shrink-0",
			)}
			onClick={onSignIn}
			tabIndex={tabIndex}
			type="button"
		>
			<LogIn aria-hidden="true" />
			<span className="tracking-tight">{t("shell.signInToAOCloud")}</span>
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
					className="grid size-control-board place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground [&_svg]:size-icon-base"
					onClick={onSignIn}
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
					className={cn(NAV_ROW_CLASS, "flex h-9 w-full items-center text-left [&_svg]:size-icon-md [&_svg]:shrink-0")}
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
					className="grid size-control-board place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground [&_svg]:size-icon-base"
					onClick={() => void signOut()}
					tabIndex={tabIndex}
					type="button"
				>
					<User aria-hidden="true" />
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

// UpdateStatusRow makes update activity visible and actionable from the
// sidebar: an available build downloads on click, progress reports itself, and
// a staged build becomes the restart action. Idle/checking states stay quiet so
// routine background checks do not flash in the sidebar.
function UpdateStatusRow({
	availableDismissed,
	onDismissAvailable,
	onRequestInstall,
	status,
	tabIndex,
}: {
	availableDismissed: boolean;
	onDismissAvailable: () => void;
	/** Opens the restart confirmation; installing outright would quit the app. */
	onRequestInstall: () => void;
	status: UpdateStatus;
	tabIndex: number;
}) {
	const { t, i18n } = useTranslation();
	const locale = i18n.resolvedLanguage ?? i18n.language;
	const action = sidebarUpdateAction(status, availableDismissed);
	if (action === null) return null;

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
					className={cn(NAV_ROW_CLASS, "flex min-w-0 flex-1 items-center text-left [&_svg]:size-icon-md [&_svg]:shrink-0")}
					onClick={() => void aoBridge.updates.download()}
					tabIndex={tabIndex}
					type="button"
				>
					<Download aria-hidden="true" className="size-icon-lg shrink-0" />
					<span className="min-w-0 flex-1">
						<span className="block truncate tracking-tight">{t("shell.updateAvailable")}</span>
						{versionLabel && (
							<span className="block truncate text-caption font-normal text-passive">{versionLabel}</span>
						)}
					</span>
				</button>
				{action.version && (
					<button
						aria-label={t("shell.dismissUpdateVersion", { version: action.version })}
						className="grid size-8 shrink-0 place-items-center text-muted-foreground transition-colors hover:text-foreground"
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

	if (action.kind === "retry") {
		return (
			<button
				aria-label={t("shell.retryUpdateCheck")}
				className="flex w-full items-center gap-2.5 rounded-lg border border-warning/35 bg-warning/12 p-2.5 text-left text-control font-medium text-warning transition-colors hover:bg-warning/18 [&_svg]:text-warning"
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

	const versionLabel = updateVersionLabel(action.version, "ready", t, locale);
	return (
		<button
			aria-label={
				action.version
					? t("shell.restartInstallUpdateVersion", { version: action.version })
					: t("shell.restartInstallUpdate")
			}
			className={cn(
				"flex w-full items-center gap-2.5 rounded-lg border border-primary/35 bg-primary/12 p-2.5 text-left text-control font-medium text-primary transition-colors hover:bg-primary/18 [&_svg]:text-primary",
				action.escalated &&
					"border-working/35 bg-working/12 text-working hover:bg-working/18 [&_svg]:text-working",
			)}
			data-testid="sidebar-update-ready"
			onClick={onRequestInstall}
			tabIndex={tabIndex}
			type="button"
		>
			<RefreshCw aria-hidden="true" className="size-icon-lg shrink-0" />
			<span className="min-w-0 flex-1">
				<span className="block truncate tracking-tight">{t("shell.restartToUpdate")}</span>
				{versionLabel && <span className="block truncate text-caption font-normal">{versionLabel}</span>}
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
	const { t, i18n } = useTranslation();
	const locale = i18n.resolvedLanguage ?? i18n.language;
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
						className="grid size-9 place-items-center rounded-lg text-passive transition-colors hover:bg-interactive-hover hover:text-foreground [&_svg]:size-4"
						onClick={() => void aoBridge.updates.download()}
						tabIndex={tabIndex}
						type="button"
					>
						<Download aria-hidden="true" />
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
						className="grid size-9 place-items-center rounded-lg bg-warning/12 text-warning transition-colors hover:bg-warning/18 [&_svg]:size-4"
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

	const versionLabel = updateVersionLabel(action.version, "ready", t, locale);
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					aria-label={
						action.version
							? t("shell.restartInstallUpdateVersion", { version: action.version })
							: t("shell.restartInstallUpdate")
					}
					className={cn(
						"grid size-9 place-items-center rounded-lg transition-colors [&_svg]:size-4",
						action.escalated
							? "bg-working/12 text-working hover:bg-working/18"
							: "text-passive hover:bg-interactive-hover hover:text-foreground",
					)}
					onClick={onRequestInstall}
					tabIndex={tabIndex}
					type="button"
				>
					<RefreshCw aria-hidden="true" />
				</button>
			</TooltipTrigger>
			<TooltipContent side="right">
				{t("shell.restartToUpdate")}
				{versionLabel ? ` · ${versionLabel}` : ""}
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
	hideTrigger = false,
	onCloneProject,
	onCreateProject,
	onInitializeProject,
}: Pick<SidebarProps, "onCloneProject" | "onCreateProject" | "onInitializeProject"> & { hideTrigger?: boolean }) {
	const { t } = useTranslation();
	// Single CreateProjectFlow owner for the sidebar: the header "+" stays mounted
	// (CSS-hidden when collapsed or on the empty start page) so it can own
	// openSignal for ⌘N on every shell route. The collapsed rail button below
	// reuses this flow via requestCreateProject().
	const createProjectNonce = useUiStore((state) => state.createProjectNonce);
	const folderDropRequest = useUiStore((state) => state.folderDropRequest);
	return (
		<CreateProjectFlow
			droppedPath={folderDropRequest}
			mode="choose"
			onCloneProject={onCloneProject}
			onCreateProject={onCreateProject}
			onInitializeProject={onInitializeProject}
			openSignal={createProjectNonce}
		>
			{({ disabled, choosePath, label }) => (
				<Tooltip>
					<TooltipTrigger asChild>
						<span className="inline-flex">
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
