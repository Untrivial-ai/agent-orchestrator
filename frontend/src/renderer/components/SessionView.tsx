import { useQuery } from "@tanstack/react-query";
import { PanelRight, Plus } from "lucide-react";
import { motion, useReducedMotion } from "motion/react";
import {
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
	type CSSProperties,
	type ReactNode,
	type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { defaultShortcutBindings, shortcutBindingLabel } from "../../shared/shortcuts";
import { BrowserPanelView, useBrowserAnnotationQueue } from "./BrowserPanel";
import { CenterPane } from "./CenterPane";
import { SessionChatSurface } from "./chat/SessionChatSurface";
import { NotificationCenter } from "./NotificationCenter";
import { ResizeHandle } from "./ResizeHandle";
import { SessionFilesView } from "./SessionFilesView";
import { SessionInspector } from "./SessionInspector";
import {
	SessionInterfaceActionGroup,
	SessionInterfaceSwitchButton,
	SessionInterfaceSwitchDialog,
	SessionInterfaceTransitionNotice,
} from "./SessionInterfaceSwitch";
import { ShellTopbar } from "./ShellTopbar";
import { SessionTopbarHost } from "./SessionTopbarPortal";
import { TopbarButton } from "./TopbarButton";
import { useBrowserView } from "../hooks/useBrowserView";
import { useResizable } from "../hooks/useResizable";
import {
	useCloseShellTerminal,
	useOpenShellTerminal,
	useRenameShellTerminal,
	useShellTerminals,
} from "../hooks/useShellTerminals";
import {
	interfaceTransitionHasUnacknowledgedNotice,
	interfaceTransitionIsActive,
	useSessionInterfaceTransition,
} from "../hooks/useSessionInterfaceTransition";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { useWindowFullScreen } from "../hooks/useWindowFullScreen";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { SHELL_PANEL_SPRING } from "../lib/motion-spring";
import { hidesShellTopbar, isMacPlatform } from "../lib/platform";
import { useShell } from "../lib/shell-context";
import { cn } from "../lib/utils";
import { isOrchestratorSession, sessionIsActive } from "../types/workspace";
import { terminalTargetBelongsToSession, type TerminalTarget } from "../types/terminal";
import { matchesRendererShortcut } from "../stores/keybindings-store";
import { useResolvedTheme, useUiStore, type InspectorView } from "../stores/ui-store";

const INSPECTOR_DEFAULT_PX = 360;
const INSPECTOR_MIN_PX = 280;
const INSPECTOR_MAX_PERCENT = 50;
const INSPECTOR_SEPARATOR_RESERVE_PX = 8;
// The inspector tab labels respond to the tablist's remaining width. The
// 239px tablist breakpoint plus the 76px pinned-action reserve and 10px leading
// inset gives a 325px inspector breakpoint for the animation lock.
const INSPECTOR_COMPACT_MAX_PX = 325;
const TOPBAR_SECONDARY_COMPACT_MAX_PX = 759;
const inspectorWidthStorageKey = "ao.inspector.widthPx";
const inspectorWidthVar = "--ao-inspector-w";
const INSPECTOR_SPRING_MS = 400;
const INSPECTOR_SPRING_EASING =
	"linear(0, 0.333 12.5%, 0.642 25%, 0.813 37.5%, 0.902 50%, 0.949 62.5%, 0.974 75%, 0.986 87.5%, 1)";
const shellTopbarHiddenByPlatform = hidesShellTopbar();
const isMac = isMacPlatform();
const noDragStyle = isMac ? ({ WebkitAppRegion: "no-drag" } as CSSProperties) : undefined;
const newTerminalShortcutLabel = shortcutBindingLabel(defaultShortcutBindings("new-shell-terminal", isMac)[0], isMac);

type ReviewsResponse = components["schemas"]["ListReviewsResponse"];
type ReviewerTerminalTarget = { handleId: string; harness: string };

function inspectorMaxWidthPx(availableWidth?: number): number | undefined {
	if (!Number.isFinite(availableWidth) || !availableWidth || availableWidth <= 0) return undefined;
	return Math.floor((availableWidth * INSPECTOR_MAX_PERCENT) / 100);
}

function initialInspectorSize(availableWidth?: number): string {
	const raw = typeof window === "undefined" ? null : window.localStorage?.getItem(inspectorWidthStorageKey);
	const parsed = raw === null ? Number.NaN : Number(raw);
	const requestedWidth = Number.isFinite(parsed)
		? Math.max(INSPECTOR_MIN_PX, Math.round(parsed))
		: INSPECTOR_DEFAULT_PX;
	const maxWidth = inspectorMaxWidthPx(availableWidth);
	return maxWidth === undefined ? `${requestedWidth}px` : `${Math.min(requestedWidth, maxWidth)}px`;
}

function topbarSecondaryLabelMode(width: number): "compact" | "expanded" {
	return width <= TOPBAR_SECONDARY_COMPACT_MAX_PX ? "compact" : "expanded";
}

function previewRevealKey(previewUrl?: string, previewRevision?: number): string {
	const target = previewUrl?.trim();
	if (!target) return "";
	if (typeof previewRevision === "number") return `revision:${previewRevision}`;
	return `url:${target}`;
}

function browserIsVisible(sessionId: string, browserPoppedOut: boolean): boolean {
	if (browserPoppedOut) return true;
	const current = useUiStore.getState().inspectorSessions[sessionId];
	return (current?.isOpen ?? true) && (current?.view ?? "summary") === "browser";
}

function reviewerTerminalFromReviews(data?: ReviewsResponse): ReviewerTerminalTarget | undefined {
	const handleId = data?.reviewerHandleId?.trim();
	if (!handleId) return undefined;
	const latest = data?.reviews?.find((review) => review.latestRun)?.latestRun;
	return { handleId, harness: data?.reviewerHarness || latest?.harness || "codex" };
}

type SessionViewProps = {
	sessionId: string;
};

// Mirrors the left sidebar: a Motion gap takes layout width while a sibling
// panel slides on `x` with SHELL_PANEL_SPRING. Dragging uses useResizable
// (clamped at min, never auto-collapse). Collapse is the explicit toggle only.
function SessionInspectorRail({
	children,
	isOpen,
	onExpand,
	onCloseAnimationComplete,
	settledClosed,
	splitRef,
}: {
	children: ReactNode;
	isOpen: boolean;
	onExpand: () => void;
	onCloseAnimationComplete?: () => void;
	settledClosed: boolean;
	splitRef: RefObject<HTMLDivElement | null>;
}) {
	const prefersReducedMotion = useReducedMotion();
	const [range, setRange] = useState({ min: INSPECTOR_MIN_PX, max: INSPECTOR_DEFAULT_PX * 2 });
	const { onPointerDown, onCollapsedPointerDown, onDoubleClick } = useResizable({
		cssVar: inspectorWidthVar,
		storageKey: inspectorWidthStorageKey,
		defaultWidth: INSPECTOR_DEFAULT_PX,
		min: range.min,
		max: range.max,
		edge: "left",
		onExpand,
	});

	useLayoutEffect(() => {
		const split = splitRef.current;
		if (!split) return;
		const updateRange = () => {
			const availableWidth = Math.max(0, split.clientWidth - INSPECTOR_SEPARATOR_RESERVE_PX);
			const maxWidth = inspectorMaxWidthPx(availableWidth) ?? INSPECTOR_DEFAULT_PX;
			const minWidth = Math.min(INSPECTOR_MIN_PX, maxWidth);
			setRange((current) => (current.min === minWidth && current.max === maxWidth ? current : { min: minWidth, max: maxWidth }));
		};
		updateRange();
		if (typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(updateRange);
		observer.observe(split);
		return () => observer.disconnect();
	}, [splitRef]);

	const transition = prefersReducedMotion ? { duration: 0 } : SHELL_PANEL_SPRING;
	const hidden = !isOpen && settledClosed;

	const handleAnimationComplete = useCallback(() => {
		if (!isOpen) onCloseAnimationComplete?.();
	}, [isOpen, onCloseAnimationComplete]);

	return (
		<>
			<motion.div
				aria-hidden="true"
				className="relative max-w-[50%] shrink-0"
				data-slot="inspector-gap"
				initial={false}
				animate={{ width: isOpen ? `var(${inspectorWidthVar}, ${INSPECTOR_DEFAULT_PX}px)` : 0 }}
				transition={transition}
			/>
			<motion.div
				aria-hidden={hidden}
				className="absolute inset-y-0 right-0 z-chrome flex h-full max-w-[50%] flex-col overflow-hidden border-l border-border-strong bg-background"
				data-panel=""
				data-settled={settledClosed ? "true" : "false"}
				data-slot="inspector-container"
				data-state={isOpen ? "expanded" : "collapsed"}
				data-testid="panel-inspector"
				hidden={hidden}
				id="inspector"
				inert={hidden}
				initial={false}
				animate={{ x: isOpen ? "0%" : "100%" }}
				onAnimationComplete={handleAnimationComplete}
				style={{ width: `var(${inspectorWidthVar}, ${INSPECTOR_DEFAULT_PX}px)` }}
				transition={transition}
			>
				<ResizeHandle
					className={!isOpen ? "hidden" : undefined}
					data-testid="inspector-resize-handle"
					onDoubleClick={onDoubleClick}
					onPointerDown={onPointerDown}
					side="left"
					style={noDragStyle}
				/>
				<div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">{children}</div>
			</motion.div>
			{isOpen ? null : (
				<div
					className="absolute inset-y-0 right-0 z-chrome w-2 cursor-e-resize touch-none"
					data-slot="inspector-collapsed-rail"
					data-testid="inspector-collapsed-rail"
					onPointerDown={onCollapsedPointerDown}
					style={noDragStyle}
				/>
			)}
		</>
	);
}

// The session detail screen: terminal + git rail. On Win/Linux the shell owns
// ShellTopbar above this view; when the platform hides the shell topbar
// (macOS), the same topbar mounts here so the outer panel stays full-height.
// Rendered by both the project-scoped and cross-project session routes.
// The persistent shell cache owns terminal lifetime by logical session + handle:
// route switches retain the xterm instance and latest output, while a replacement
// handle gets a clean xterm/mux binding.
//
// The inspector uses the same Motion spring as the left sidebar (gap width +
// x-transform). Dragging is useResizable and clamps at the responsive minimum;
// only the explicit controls (topbar button / ⌘⇧B) collapse it. The preferred
// 280px floor is clamped to the 50% maximum on narrow session splits, where
// the inspector tabs compact to icons.
export function SessionView({ sessionId }: SessionViewProps) {
	const { t } = useTranslation();
	const workspaceQuery = useWorkspaceQuery();
	const workspaces = workspaceQuery.data ?? [];
	const theme = useResolvedTheme();
	const isInspectorOpen = useUiStore((state) => state.inspectorSessions[sessionId]?.isOpen ?? true);
	const inspectorView = useUiStore((state) => state.inspectorSessions[sessionId]?.view ?? "summary");
	const setInspectorOpenForSession = useUiStore((state) => state.setInspectorOpen);
	const toggleInspector = useUiStore((state) => state.toggleInspector);
	const setInspectorViewForSession = useUiStore((state) => state.setInspectorView);
	const setBrowserContentRevealed = useUiStore((state) => state.setBrowserContentRevealed);
	const setBrowserUnseen = useUiStore((state) => state.setBrowserUnseen);
	const { daemonStatus } = useShell();
	const previewBaselineRef = useRef<{ sessionId: string; key: string } | null>(null);
	const sessionSplitRef = useRef<HTMLDivElement | null>(null);
	const terminalLiveResizeTimerRef = useRef<number | null>(null);
	const initializedInspectorSessionIdRef = useRef<string | null>(null);
	const [inspectorSettledClosed, setInspectorSettledClosed] = useState(!isInspectorOpen);
	const inspectorPanelVisible = isInspectorOpen || !inspectorSettledClosed;
	const [terminalTarget, setTerminalTarget] = useState<TerminalTarget>({ kind: "worker" });
	const [browserPopOutState, setBrowserPopOutState] = useState({ sessionId, poppedOut: false });
	const [filesPoppedOut, setFilesPoppedOut] = useState(false);
	const browserPoppedOut = browserPopOutState.sessionId === sessionId && browserPopOutState.poppedOut;
	const [interfaceSwitchDialogOpen, setInterfaceSwitchDialogOpen] = useState(false);
	const isNativeFullScreen = useWindowFullScreen();
	const stopTerminalLiveResize = useCallback(() => {
		if (terminalLiveResizeTimerRef.current !== null) {
			window.clearTimeout(terminalLiveResizeTimerRef.current);
			terminalLiveResizeTimerRef.current = null;
		}
		sessionSplitRef.current?.removeAttribute("data-terminal-live-resize");
		sessionSplitRef.current?.removeAttribute("data-inspector-label-mode");
		sessionSplitRef.current?.removeAttribute("data-topbar-secondary-label-mode");
	}, []);
	const startTerminalLiveResize = useCallback(
		(labelMode: "compact" | "expanded", topbarLabelMode: "compact" | "expanded") => {
			const split = sessionSplitRef.current;
			if (!split) return;
			if (terminalLiveResizeTimerRef.current !== null) {
				window.clearTimeout(terminalLiveResizeTimerRef.current);
			}
			split.setAttribute("data-terminal-live-resize", "true");
			split.setAttribute("data-inspector-label-mode", labelMode);
			split.setAttribute("data-topbar-secondary-label-mode", topbarLabelMode);
			terminalLiveResizeTimerRef.current = window.setTimeout(() => {
				split.removeAttribute("data-terminal-live-resize");
				split.removeAttribute("data-inspector-label-mode");
				split.removeAttribute("data-topbar-secondary-label-mode");
				terminalLiveResizeTimerRef.current = null;
			}, INSPECTOR_SPRING_MS);
		},
		[],
	);

	useEffect(() => stopTerminalLiveResize, [stopTerminalLiveResize]);

	const session = workspaces.flatMap((workspace) => workspace.sessions).find((s) => s.id === sessionId);
	const interfaceSwitch = useSessionInterfaceTransition(session?.id);
	const reviewerQuery = useQuery({
		queryKey: ["session-reviews", sessionId],
		enabled: Boolean(
			window.ao && session && sessionIsActive(session) && !isOrchestratorSession(session) && session.prs.length > 0,
		),
		refetchInterval: (query) => {
			const data = query.state.data as ReviewsResponse | undefined;
			return data?.reviews?.some((review) => review.status === "running") ? 2500 : false;
		},
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/reviews", {
				params: { path: { sessionId } },
			});
			if (error) throw new Error(apiErrorMessage(error, "Unable to load reviews"));
			return data ?? ({ reviewerHandleId: "", reviews: [], runs: [] } satisfies ReviewsResponse);
		},
	});
	const availableReviewerTerminal = reviewerTerminalFromReviews(reviewerQuery.data);
	const reviewerTerminal = session && sessionIsActive(session) ? availableReviewerTerminal : undefined;

	// Shell terminals opened inside a session live beside its pane as extra tabs,
	// scoped to the session on screen so each session has its own shell set.
	const allShellTerminals = useShellTerminals().data ?? [];
	const shellTerminals = useMemo(
		() => allShellTerminals.filter((shell) => shell.sessionId === sessionId),
		[allShellTerminals, sessionId],
	);
	const openShellTerminal = useOpenShellTerminal();
	const closeShellTerminal = useCloseShellTerminal();
	const renameShellTerminal = useRenameShellTerminal();
	const activeShellTerminalHandleId = useUiStore((state) => state.activeShellTerminalHandleId);
	const setActiveShellTerminal = useUiStore((state) => state.setActiveShellTerminal);
	const setVisibleTerminalKind = useUiStore((state) => state.setVisibleTerminalKind);
	const clearVisibleTerminalKind = useUiStore((state) => state.clearVisibleTerminalKind);
	const renameShellTerminalByHandle = useCallback(
		(handleId: string, title: string) => renameShellTerminal.mutate({ handleId, title }),
		[renameShellTerminal],
	);

	// Scoped to the session on screen so the daemon roots the shell in that
	// session's worktree (the project id is only the fallback when the session's
	// workspace can no longer be resolved).
	const addShellTerminal = useCallback(() => {
		openShellTerminal.mutate(
			{ projectId: session?.workspaceId, sessionId },
			{
				onSuccess: (shell) => {
					setActiveShellTerminal(shell.handleId);
					setTerminalTarget({
						generation: shell.createdAt,
						kind: "shell",
						handleId: shell.handleId,
						sessionId,
						title: shell.title,
					});
				},
			},
		);
	}, [openShellTerminal, sessionId, session?.workspaceId, setActiveShellTerminal]);

	const selectShellTerminal = useCallback(
		(handleId: string) => {
			const shell = shellTerminals.find((s) => s.handleId === handleId);
			if (!shell) return;
			setActiveShellTerminal(shell.handleId);
			setTerminalTarget({
				generation: shell.createdAt,
				kind: "shell",
				handleId: shell.handleId,
				sessionId,
				title: shell.title,
			});
		},
		[shellTerminals, setActiveShellTerminal],
	);

	const closeShellTerminalByHandle = useCallback(
		(handleId: string) => {
			if (terminalTarget.kind === "shell" && terminalTarget.handleId === handleId) {
				const closingIndex = shellTerminals.findIndex((shell) => shell.handleId === handleId);
				// Match browser-tab ergonomics: closing the selected auxiliary terminal
				// reveals its nearest predecessor, then the next tab when the first one
				// closes. The permanent agent terminal is only the final fallback.
				const nextShell = shellTerminals[closingIndex - 1] ?? shellTerminals[closingIndex + 1];
				if (nextShell) {
					setActiveShellTerminal(nextShell.handleId);
					setTerminalTarget({
						generation: nextShell.createdAt,
						kind: "shell",
						handleId: nextShell.handleId,
						sessionId,
						title: nextShell.title,
					});
				} else {
					setActiveShellTerminal(null);
					setTerminalTarget({ kind: "worker" });
				}
			} else if (activeShellTerminalHandleId === handleId) {
				setActiveShellTerminal(null);
			}
			closeShellTerminal.mutate(handleId);
		},
		[
			activeShellTerminalHandleId,
			closeShellTerminal,
			setActiveShellTerminal,
			sessionId,
			shellTerminals,
			terminalTarget,
		],
	);

	// Selecting the session's own pane also drops the active shell, so the effect
	// above does not immediately pull the view back to that shell.
	const selectSessionTerminal = useCallback(() => {
		setActiveShellTerminal(null);
		setTerminalTarget({ kind: "worker" });
	}, [setActiveShellTerminal]);
	const selectReviewerTerminal = useCallback((target: ReviewerTerminalTarget) => {
		setActiveShellTerminal(null);
		setTerminalTarget({ kind: "reviewer", handleId: target.handleId, harness: target.harness, sessionId });
	}, [sessionId, setActiveShellTerminal]);

	// The shell layout owns opening (it is mounted on every route, so the button
	// and ⌘T / Ctrl+T work everywhere); this view only follows the result. When a new
	// shell becomes active while a session is on screen, switch the pane to it —
	// that is what makes the shortcut feel like it opened a terminal *here*.
	useEffect(() => {
		if (!activeShellTerminalHandleId) return;
		const shell = shellTerminals.find((s) => s.handleId === activeShellTerminalHandleId);
		if (!shell) return;
		setTerminalTarget((current) =>
			current.kind === "shell" &&
			current.handleId === shell.handleId &&
			current.generation === shell.createdAt &&
			current.title === shell.title
				? current
				: {
						generation: shell.createdAt,
						kind: "shell",
						handleId: shell.handleId,
						sessionId,
						title: shell.title,
					},
		);
	}, [activeShellTerminalHandleId, sessionId, shellTerminals]);

	// If the pane is pointed at a shell that is not in THIS session's strip — e.g.
	// after navigating to a different session whose globally-active shell belongs
	// elsewhere — fall back to the session's own pane rather than render a tab
	// that isn't shown here.
	useEffect(() => {
		setTerminalTarget((current) =>
			current.kind === "shell" && !shellTerminals.some((s) => s.handleId === current.handleId)
				? { kind: "worker" }
				: current,
		);
	}, [shellTerminals]);
	useEffect(() => {
		setTerminalTarget((current) =>
			current.kind === "reviewer" &&
				reviewerQuery.isFetched &&
			(!availableReviewerTerminal || availableReviewerTerminal.handleId !== current.handleId)
				? { kind: "worker" }
				: current,
		);
	}, [availableReviewerTerminal, reviewerQuery.isFetched]);
	const isOrchestrator = session ? isOrchestratorSession(session) : false;
	// Orchestrators get the full workspace width; only workers need the inspector rail.
	const hasInspector = Boolean(session && !isOrchestrator);
	const activeInterfaceTransition = interfaceTransitionIsActive(interfaceSwitch.transition);
	const chatControllerTransitioning = Boolean(
		interfaceSwitch.transition?.targetMode === "chat" &&
			(activeInterfaceTransition || interfaceSwitch.settling),
	);
	const interfaceTarget =
		(activeInterfaceTransition ? interfaceSwitch.transition?.targetMode : interfaceSwitch.status?.targetMode) ??
		(session?.mode === "chat" ? "tui" : "chat");
	const chatToTerminal = session?.mode === "chat" && interfaceTarget === "tui";
	const interfaceBusy = Boolean(
		session &&
		(session.status === "working" ||
			session.status === "needs_input" ||
			session.activity?.state === "active" ||
			session.activity?.state === "waiting_input" ||
			session.activity?.state === "blocked"),
	);
	const interfaceWaitingForInput = Boolean(
		session &&
		(session.status === "needs_input" ||
			session.activity?.state === "waiting_input" ||
			session.activity?.state === "blocked"),
	);
	const beginInterfaceSwitch = useCallback(
		async (policy: "drain" | "interrupt") => {
			try {
				await interfaceSwitch.start({ targetMode: interfaceTarget, policy });
				setInterfaceSwitchDialogOpen(false);
			} catch {
				// The mutation owns the typed error. A policy dialog that was already
				// open stays open; a direct switch must not open one on failure.
			}
		},
		[interfaceSwitch, interfaceTarget],
	);
	const requestInterfaceSwitch = useCallback(() => {
		interfaceSwitch.resetStartError();
		// Terminal UI is the escape hatch for a runaway Chat turn. The session
		// projection can briefly report idle while the Chat controller is busy, so
		// this direction must always apply the explicit interrupt policy instead
		// of relying on interfaceBusy. TUI -> Chat keeps the choice dialog because
		// leaving a live terminal is not itself a recovery action.
		if (chatToTerminal) {
			void beginInterfaceSwitch("interrupt");
			return;
		}
		if (!interfaceBusy) {
			void beginInterfaceSwitch("drain");
			return;
		}
		setInterfaceSwitchDialogOpen(true);
	}, [beginInterfaceSwitch, chatToTerminal, interfaceBusy, interfaceSwitch]);
	// Adapters without a Chat driver cannot offer a switch into Chat UI; hide
	// the button entirely rather than showing a permanently disabled control.
	const interfaceSwitchUnsupported = interfaceSwitch.status?.reasonCode === "CHAT_UNSUPPORTED";
	const showInterfaceSwitchAction = Boolean(
		!interfaceSwitchUnsupported && (interfaceSwitch.status || interfaceSwitch.isLoading || interfaceSwitch.statusError),
	);
	const interfaceSwitchAction = session && showInterfaceSwitchAction ? (
		<SessionInterfaceSwitchButton
			target={interfaceTarget}
			supported={Boolean(interfaceSwitch.status?.supported) && !activeInterfaceTransition}
			disabledReason={
				interfaceSwitch.isLoading
					? "Checking whether this agent can switch interfaces…"
					: interfaceSwitch.status?.reason || interfaceSwitch.statusError
			}
			pending={interfaceSwitch.starting || activeInterfaceTransition}
			transition={interfaceSwitch.transition}
			cancelling={interfaceSwitch.cancelling}
			cancelError={interfaceSwitch.cancelError}
			onClick={requestInterfaceSwitch}
			onCancel={() => {
				void interfaceSwitch.cancel().catch(() => {});
			}}
		/>
	) : null;
	const newTerminalError = openShellTerminal.error ? apiErrorMessage(openShellTerminal.error) : undefined;
	const sessionLocalActions = session ? (
		<SessionInterfaceActionGroup>
			{!isOrchestrator ? (
				<TopbarButton
					aria-label={t("shortcut.new-shell-terminal")}
					disabled={openShellTerminal.isPending}
					onClick={addShellTerminal}
					title={newTerminalError ?? t("terminal.newWithShortcut", { shortcut: newTerminalShortcutLabel })}
					type="button"
					variant="icon"
				>
					<Plus aria-hidden="true" className="size-icon-md" />
				</TopbarButton>
			) : null}
			{interfaceSwitchAction}
		</SessionInterfaceActionGroup>
	) : null;
	const sessionHeaderActions = <ShellTopbar embedded sessionAction={sessionLocalActions} />;
	const previewUrl = session?.previewUrl?.trim() || undefined;
	const previewRevision = session?.previewRevision;
	const browserSlotVisible = Boolean(
		session && hasInspector && (browserPoppedOut || (isInspectorOpen && inspectorView === "browser")),
	);
	const terminated = session ? !sessionIsActive(session) : false;
	const browserView = useBrowserView({
		sessionId,
		active: browserSlotVisible,
		poppedOut: browserPoppedOut,
		terminated,
		previewUrl,
		previewRevision,
	});
	const browserAnnotationQueue = useBrowserAnnotationQueue({
		sessionId: session?.id,
		navUrl: browserView.navState.url,
	});
	const browserUrl = browserView.navState.url.trim();
	// A terminated session's `previewUrl` is a stale DB fact; useBrowserView
	// suppresses and destroys the live preview for it, so it must not count as
	// content here either — otherwise a merged/terminated session with an old
	// preview auto-opens Browser onto a view the hook has already torn down.
	const hasBrowserContent = !terminated && Boolean(previewUrl || browserUrl);

	// Entering a session always starts on Summary. Treat browser content that
	// already existed when the route resolved as the baseline for that visit;
	// only preview work arriving afterward may reveal Browser automatically.
	useLayoutEffect(() => {
		if (!session || initializedInspectorSessionIdRef.current === sessionId) return;
		initializedInspectorSessionIdRef.current = sessionId;
		if (!hasInspector) return;
		const current = useUiStore.getState().inspectorSessions[sessionId];
		setInspectorViewForSession(sessionId, "summary");
		if (current?.browserContentRevealed === undefined) {
			setBrowserContentRevealed(sessionId, hasBrowserContent);
		}
	}, [
		hasBrowserContent,
		hasInspector,
		session,
		sessionId,
		setBrowserContentRevealed,
		setInspectorViewForSession,
	]);

	useLayoutEffect(() => {
		setTerminalTarget({ kind: "worker" });
		setBrowserPopOutState({ sessionId, poppedOut: false });
		setFilesPoppedOut(false);
	}, [sessionId]);

	// Route props change one render before the passive reset above. Reject the
	// previous session's shell/reviewer synchronously so its handle can never be
	// cached under the destination session.
	const routedTerminalTarget = terminalTargetBelongsToSession(terminalTarget, sessionId)
		? terminalTarget
		: ({ kind: "worker" } satisfies TerminalTarget);
	// Chat surface stays mounted in chat mode for worker, reviewer, and shell
	// targets. A terminal pane (reviewer or shell) renders as a tab inside the
	// chat surface, so opening one never costs the user the conversation.
	const chatTargetKind = routedTerminalTarget.kind;
	const showChatSurface =
		session?.mode === "chat" &&
		(chatTargetKind === "worker" || chatTargetKind === "reviewer" || chatTargetKind === "shell");

	// The pane shows one terminal at a time, so selecting a shell or the reviewer
	// takes the agent's terminal off screen while the route still points here.
	// Publish which one is showing: the notification runtime lives outside this
	// subtree and must not treat "on the session route" as "watching the agent".
	useEffect(() => {
		setVisibleTerminalKind(sessionId, routedTerminalTarget.kind);
		return () => clearVisibleTerminalKind(sessionId);
	}, [clearVisibleTerminalKind, routedTerminalTarget.kind, sessionId, setVisibleTerminalKind]);

	const handleOpenFiles = useCallback(() => {
		setBrowserPopOutState({ sessionId, poppedOut: false });
		setFilesPoppedOut(false);
		setInspectorViewForSession(sessionId, "files");
		setInspectorOpenForSession(sessionId, true);
	}, [sessionId, setInspectorOpenForSession, setInspectorViewForSession]);

	const handleToggleFilesPopOut = useCallback(
		(next: boolean) => {
			if (next) setBrowserPopOutState({ sessionId, poppedOut: false });
			setFilesPoppedOut(next);
			setInspectorViewForSession(sessionId, "files");
			setInspectorOpenForSession(sessionId, true);
		},
		[sessionId, setInspectorOpenForSession, setInspectorViewForSession],
	);

	const handleToggleBrowserPopOut = useCallback(
		(next: boolean) => {
			if (next) setFilesPoppedOut(false);
			setBrowserPopOutState({ sessionId, poppedOut: next });
		},
		[sessionId],
	);

	useEffect(() => {
		if (!hasInspector) return;
		const current = useUiStore.getState().inspectorSessions[sessionId];
		if (!hasBrowserContent) {
			if (current?.browserContentRevealed) setBrowserContentRevealed(sessionId, false);
			else if (current?.browserUnseen) setBrowserUnseen(sessionId, false);
			return;
		}
		if (current?.browserContentRevealed) return;
		setBrowserContentRevealed(sessionId, true);
	}, [
		hasBrowserContent,
		hasInspector,
		previewRevision,
		sessionId,
		setBrowserContentRevealed,
		setBrowserUnseen,
		terminated,
	]);

	useEffect(() => {
		if (!hasInspector) return;
		const previewKey = previewRevealKey(previewUrl, previewRevision);
		const baseline = previewBaselineRef.current;
		if (!baseline || baseline.sessionId !== sessionId) {
			previewBaselineRef.current = { sessionId, key: previewKey };
			return;
		}
		if (baseline.key === previewKey) return;
		previewBaselineRef.current = { sessionId, key: previewKey };
		if (!previewKey) return;
		setBrowserContentRevealed(sessionId, true);
		if (browserIsVisible(sessionId, browserPoppedOut)) {
			setBrowserUnseen(sessionId, false);
			return;
		}
		setInspectorViewForSession(sessionId, "browser");
		setInspectorOpenForSession(sessionId, true);
	}, [
		browserPoppedOut,
		hasInspector,
		previewRevision,
		previewUrl,
		sessionId,
		setBrowserContentRevealed,
		setBrowserUnseen,
		setInspectorOpenForSession,
		setInspectorViewForSession,
	]);

	// Agent browser commands are genuine browser activity even when they do not
	// navigate (fill, click, snapshot, etc.) or land on an empty target — e.g. a
	// command that runs before any page has loaded. When Browser is hidden,
	// surface that activity as unseen rather than reopening the tab; gating this
	// on hasBrowserContent/browserContentRevealed missed exactly that case.
	useEffect(() => {
		if (!hasInspector || terminated || !browserView.agentBrowserActive) return;
		if (!browserIsVisible(sessionId, browserPoppedOut)) setBrowserUnseen(sessionId, true);
	}, [
		browserPoppedOut,
		browserView.agentBrowserActive,
		hasInspector,
		inspectorView,
		isInspectorOpen,
		sessionId,
		setBrowserUnseen,
		terminated,
	]);

	// Opening Browser consumes the pending activity indicator, including the
	// case where the inspector was collapsed while already parked on Browser.
	useEffect(() => {
		if (hasInspector && browserIsVisible(sessionId, browserPoppedOut)) {
			setBrowserUnseen(sessionId, false);
		}
	}, [browserPoppedOut, hasInspector, inspectorView, isInspectorOpen, sessionId, setBrowserUnseen]);

	useEffect(() => {
		if (!hasInspector) return;
		const handleKeyDown = (event: KeyboardEvent) => {
			if (!matchesRendererShortcut("toggle-inspector", event)) return;
			event.preventDefault();
			toggleInspector(sessionId);
		};
		window.addEventListener("keydown", handleKeyDown);
		return () => window.removeEventListener("keydown", handleKeyDown);
	}, [hasInspector, sessionId, toggleInspector]);

	const inspectorMotionReadyRef = useRef(false);
	const handleInspectorCloseAnimationComplete = useCallback(() => {
		setInspectorSettledClosed(true);
	}, []);
	useLayoutEffect(() => {
		if (!hasInspector) {
			setInspectorSettledClosed(true);
			stopTerminalLiveResize();
			return;
		}
		if (!inspectorMotionReadyRef.current) {
			setInspectorSettledClosed(!isInspectorOpen);
		}
	}, [hasInspector, isInspectorOpen, stopTerminalLiveResize]);
	useEffect(() => {
		if (!hasInspector || !inspectorMotionReadyRef.current) return;
		if (isInspectorOpen) {
			setInspectorSettledClosed(false);
			const groupWidth = sessionSplitRef.current?.clientWidth || window.innerWidth;
			const availableWidth = Math.max(0, groupWidth - INSPECTOR_SEPARATOR_RESERVE_PX);
			const targetInspectorWidth = Number.parseFloat(initialInspectorSize(availableWidth));
			startTerminalLiveResize(
				targetInspectorWidth <= INSPECTOR_COMPACT_MAX_PX ? "compact" : "expanded",
				topbarSecondaryLabelMode(Math.max(0, availableWidth - targetInspectorWidth)),
			);
			return;
		}
		const groupWidth = sessionSplitRef.current?.clientWidth || window.innerWidth;
		startTerminalLiveResize("expanded", topbarSecondaryLabelMode(groupWidth));
	}, [hasInspector, isInspectorOpen, startTerminalLiveResize]);
	useEffect(() => {
		if (!hasInspector) {
			inspectorMotionReadyRef.current = false;
			return;
		}
		inspectorMotionReadyRef.current = true;
		return () => {
			inspectorMotionReadyRef.current = false;
		};
	}, [hasInspector]);
	if (!session && !workspaceQuery.isLoading) {
		return (
			<div className="grid h-full place-items-center p-6 text-center font-mono text-xs text-passive">
				{t("session.notFound")}
			</div>
		);
	}

	return (
		<div className="relative flex h-full min-h-0 flex-col bg-background text-foreground" data-testid="session-detail">
			<div
				className="session-split relative flex min-h-0 flex-1 overflow-hidden"
				data-testid="panel-group"
				id="session-workspace"
				ref={sessionSplitRef}
				style={
					{
						"--session-inspector-max-width": `${INSPECTOR_MAX_PERCENT}%`,
						"--session-inspector-motion-duration": `${INSPECTOR_SPRING_MS}ms`,
						"--session-inspector-motion-easing": INSPECTOR_SPRING_EASING,
					} as CSSProperties
				}
			>
				<div
					className="relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden"
					data-panel=""
					id="terminal"
				>
					<div className="relative flex h-full min-h-0 flex-col">
						<SessionTopbarHost
							className="relative z-chrome flex h-inspector-tabs w-full shrink-0 overflow-hidden"
							data-testid="session-topbar-host"
						/>
						<div className="relative min-h-0 flex-1">
							{/* The committed mode owns the agent surface. Auxiliary shell and
							    reviewer targets remain terminal surfaces in either mode. */}
							{showChatSurface ? (
								<SessionChatSurface
									session={session}
									reviewerTerminal={reviewerTerminal}
									onOpenReviewerTerminal={selectReviewerTerminal}
									reviewerTarget={
										routedTerminalTarget.kind === "reviewer" ? routedTerminalTarget : undefined
									}
									onSelectChat={selectSessionTerminal}
									shellTerminals={shellTerminals}
									shellTarget={
										routedTerminalTarget.kind === "shell" ? routedTerminalTarget : undefined
									}
									onSelectShellTerminal={selectShellTerminal}
									onCloseShellTerminal={closeShellTerminalByHandle}
									onRenameShellTerminal={renameShellTerminalByHandle}
									daemonReady={daemonStatus.state === "ready"}
									theme={theme}
									headerActions={sessionHeaderActions}
									controllerTransitioning={chatControllerTransitioning}
									onOpenShell={addShellTerminal}
									openingShell={openShellTerminal.isPending}
									shellError={
										openShellTerminal.error ? apiErrorMessage(openShellTerminal.error) : undefined
									}
								/>
							) : (
								<CenterPane
									agentInputDisabled={
										(interfaceSwitch.starting || activeInterfaceTransition) && session?.mode === "tui"
									}
									daemonReady={daemonStatus.state === "ready"}
									onCloseShellTerminal={closeShellTerminalByHandle}
									onRenameShellTerminal={renameShellTerminalByHandle}
									onSelectSessionTerminal={selectSessionTerminal}
									onSelectReviewerTerminal={selectReviewerTerminal}
									onSelectShellTerminal={selectShellTerminal}
									reviewerTerminal={reviewerTerminal}
									session={session}
									shellTerminals={shellTerminals}
									terminalTarget={routedTerminalTarget}
									theme={theme}
									topbarActions={sessionHeaderActions}
								/>
							)}
							{interfaceTransitionHasUnacknowledgedNotice(interfaceSwitch.transition) ? (
								<SessionInterfaceTransitionNotice
									transition={interfaceSwitch.transition}
									dismissing={interfaceSwitch.acknowledgingNotice}
									dismissError={interfaceSwitch.acknowledgeNoticeError}
									onDismiss={() => {
										const transitionID = interfaceSwitch.transition?.id;
										if (transitionID) void interfaceSwitch.acknowledgeNotice(transitionID).catch(() => {});
									}}
									onSwitchWithInterrupt={() => {
										interfaceSwitch.resetStartError();
										void beginInterfaceSwitch("interrupt");
									}}
									interrupting={interfaceSwitch.starting}
								/>
							) : null}
						</div>
					</div>
				</div>
				{hasInspector ? (
					<SessionInspectorRail
						isOpen={isInspectorOpen}
						onCloseAnimationComplete={handleInspectorCloseAnimationComplete}
						onExpand={() => setInspectorOpenForSession(sessionId, true)}
						settledClosed={!isInspectorOpen && inspectorSettledClosed}
						splitRef={sessionSplitRef}
					>
						<SessionInspector
							browserAnnotationQueue={browserAnnotationQueue}
							browserPoppedOut={browserPoppedOut}
							filesView={
								session ? (
									<SessionFilesView onToggleMaximized={handleToggleFilesPopOut} sessionId={session.id} />
								) : null
							}
							isInspectorVisible={inspectorPanelVisible}
							onOpenFiles={handleOpenFiles}
							onOpenReviewerTerminal={selectReviewerTerminal}
							onToggleBrowserPopOut={handleToggleBrowserPopOut}
							onViewChange={(next: InspectorView) => setInspectorViewForSession(sessionId, next)}
							view={inspectorView}
							browserView={browserView}
							session={session}
						/>
					</SessionInspectorRail>
				) : null}
			</div>
			{hasInspector ? (
				<div className="session-pinned-actions" data-testid="session-pinned-actions" style={noDragStyle}>
					<TopbarButton
						aria-label={isInspectorOpen ? t("shell.closeInspector") : t("shell.openInspector")}
						aria-pressed={isInspectorOpen}
						onClick={() => toggleInspector(sessionId)}
						style={noDragStyle}
						title={isInspectorOpen ? t("shell.closeInspectorTitle") : t("shell.openInspectorTitle")}
						variant="icon"
					>
						<PanelRight className="size-icon-md" aria-hidden="true" />
					</TopbarButton>
					{/* Keep the global notification action trailing at the window edge. */}
					<NotificationCenter style={noDragStyle} />
				</div>
			) : null}
			<SessionInterfaceSwitchDialog
				open={interfaceSwitchDialogOpen}
				target={interfaceTarget}
				waitingForInput={interfaceWaitingForInput}
				busy={interfaceSwitch.starting}
				error={interfaceSwitch.startError}
				onOpenChange={setInterfaceSwitchDialogOpen}
				onChoose={(policy) => void beginInterfaceSwitch(policy)}
			/>
			{filesPoppedOut && session
				? createPortal(
						<div
							className={cn(
								"files-popout-overlay",
								shellTopbarHiddenByPlatform && !isNativeFullScreen && "files-popout-overlay--mac-windowed",
							)}
						>
							<SessionFilesView
								isMaximized
								onToggleMaximized={handleToggleFilesPopOut}
								sessionId={session.id}
							/>
						</div>,
						document.body,
					)
				: null}
			{/* Maximized browser: a fixed overlay across the app workspace,
          portaled to <body> so it escapes the shell layout (covering the
          sidebar + topbar, not just the session area) and sits outside any
          `[data-panel]` column, so the native WebContentsView is not clamped
          and fills the window below any native titlebar overlay. */}
			{browserPoppedOut && session
				? createPortal(
						<div
							className={cn(
								"browser-popout-overlay",
								shellTopbarHiddenByPlatform && !isNativeFullScreen && "browser-popout-overlay--mac-windowed",
							)}
						>
							<BrowserPanelView
								active
								annotationQueue={browserAnnotationQueue}
								browserView={browserView}
								onTogglePopOut={handleToggleBrowserPopOut}
								poppedOut
								session={session}
							/>
						</div>,
						document.body,
					)
				: null}
		</div>
	);
}
