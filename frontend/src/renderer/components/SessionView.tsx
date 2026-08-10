import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import type { PanelImperativeHandle, PanelSize } from "react-resizable-panels";
import type { components } from "../../api/schema";
import { BrowserPanelView, useBrowserAnnotationQueue } from "./BrowserPanel";
import { CenterPane } from "./CenterPane";
import { SessionChatSurface } from "./chat/SessionChatSurface";
import { PendingReviewCommentsProvider } from "./PendingReviewCommentsContext";
import { SessionFilesView } from "./SessionFilesView";
import { SessionInspector } from "./SessionInspector";
import {
	SessionInterfaceActionGroup,
	SessionInterfaceSwitchButton,
	SessionInterfaceSwitchDialog,
	SessionInterfaceTransitionNotice,
} from "./SessionInterfaceSwitch";
import { ShellTopbar } from "./ShellTopbar";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "./ui/resizable";
import { useBrowserView } from "../hooks/useBrowserView";
import {
	useCloseShellTerminal,
	useOpenShellTerminal,
	useRenameShellTerminal,
	useShellTerminals,
} from "../hooks/useShellTerminals";
import {
	interfaceTransitionIsActive,
	useSessionInterfaceTransition,
} from "../hooks/useSessionInterfaceTransition";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { useWindowFullScreen } from "../hooks/useWindowFullScreen";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { hidesShellTopbar } from "../lib/platform";
import { useShell } from "../lib/shell-context";
import { cn } from "../lib/utils";
import { isOrchestratorSession, sessionIsActive } from "../types/workspace";
import { terminalTargetBelongsToSession, type TerminalTarget } from "../types/terminal";
import { matchesRendererShortcut } from "../stores/keybindings-store";
import { useResolvedTheme, useUiStore, type InspectorView } from "../stores/ui-store";

const INSPECTOR_MIN_PERCENT = 30;
const INSPECTOR_MAX_PERCENT = 45;
const inspectorSplitStorageKey = "ao.inspector.split";
const shellTopbarHiddenByPlatform = hidesShellTopbar();

type ReviewsResponse = components["schemas"]["ListReviewsResponse"];
type ReviewerTerminalTarget = { handleId: string; harness: string };

function initialSplitPercent(): number {
	const raw = typeof window === "undefined" ? null : window.localStorage?.getItem(inspectorSplitStorageKey);
	const parsed = raw === null ? Number.NaN : Number(raw);
	if (!Number.isFinite(parsed)) return INSPECTOR_MIN_PERCENT;
	return Math.min(INSPECTOR_MAX_PERCENT, Math.max(INSPECTOR_MIN_PERCENT, parsed));
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

// The session detail screen: terminal + git rail. On Win/Linux the shell owns
// ShellTopbar above this view; when the platform hides the shell topbar
// (macOS), the same topbar mounts here so the outer panel stays full-height.
// Rendered by both the project-scoped and cross-project session routes.
// The persistent shell cache owns terminal lifetime by logical session + handle:
// route switches retain the xterm instance and latest output, while a replacement
// handle gets a clean xterm/mux binding.
//
// The split is shadcn's resizable (react-resizable-panels v4) with a fully
// collapsible inspector driven to 0% via the imperative API from the ui-store
// (topbar button / ⌘⇧B), animated by the flex-grow transition in styles.css.
// The panel is `collapsible` only while closed: rrp snaps a collapsible panel
// to 0% when a drag crosses minSize, so an always-collapsible inspector let a
// drag vanish the rail. While open the panel is non-collapsible and a drag
// hard-stops at INSPECTOR_MIN_PERCENT; only the explicit controls collapse it.
// Content keeps a stable min-width inside the clipped panel so nothing reflows
// mid-animation; split width persists.
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
	const markInspectorPreviewSeen = useUiStore((state) => state.markInspectorPreviewSeen);
	const setBrowserContentRevealed = useUiStore((state) => state.setBrowserContentRevealed);
	const setBrowserUnseen = useUiStore((state) => state.setBrowserUnseen);
	const { daemonStatus } = useShell();
	const inspectorRef = useRef<PanelImperativeHandle | null>(null);
	const inspectorSeparatorRef = useRef<HTMLDivElement | null>(null);
	const [terminalTarget, setTerminalTarget] = useState<TerminalTarget>({ kind: "worker" });
	const [browserPopOutState, setBrowserPopOutState] = useState({ sessionId, poppedOut: false });
	const [filesPoppedOut, setFilesPoppedOut] = useState(false);
	const browserPoppedOut = browserPopOutState.sessionId === sessionId && browserPopOutState.poppedOut;
	const [interfaceSwitchDialogOpen, setInterfaceSwitchDialogOpen] = useState(false);
	const [dismissedTransitionID, setDismissedTransitionID] = useState("");
	const isNativeFullScreen = useWindowFullScreen();

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
			(!availableReviewerTerminal || availableReviewerTerminal.handleId !== current.handleId)
				? { kind: "worker" }
				: current,
		);
	}, [availableReviewerTerminal]);
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
				// open stays open; a direct idle switch must not open one on failure.
			}
		},
		[interfaceSwitch, interfaceTarget],
	);
	const requestInterfaceSwitch = useCallback(() => {
		interfaceSwitch.resetStartError();
		if (!interfaceBusy) {
			void beginInterfaceSwitch("drain");
			return;
		}
		setInterfaceSwitchDialogOpen(true);
	}, [beginInterfaceSwitch, interfaceBusy, interfaceSwitch]);
	const showInterfaceSwitchAction = Boolean(
		interfaceSwitch.status || interfaceSwitch.isLoading || interfaceSwitch.statusError,
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
	const sessionHeaderActions = (
		<SessionInterfaceActionGroup>
			{interfaceSwitchAction}
			<ShellTopbar embedded />
		</SessionInterfaceActionGroup>
	);
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
	const showChatSurface = session?.mode === "chat" && routedTerminalTarget.kind === "worker";

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

	// Reveal the first real content in each non-empty browser lifecycle. Once the
	// user leaves Browser, subsequent work respects that choice and uses the
	// unseen indicator instead of repeatedly stealing the active inspector tab.
	// previewRevision intentionally retriggers the empty branch so an explicit
	// clear consumes unseen activity even when the browser was already empty.
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
		setInspectorViewForSession(sessionId, "browser");
		setInspectorOpenForSession(sessionId, true);
	}, [
		hasBrowserContent,
		hasInspector,
		previewRevision,
		sessionId,
		setBrowserContentRevealed,
		setBrowserUnseen,
		setInspectorOpenForSession,
		setInspectorViewForSession,
		terminated,
	]);

	// `ao preview` is authoritative browser work, including a same-URL rerun
	// whose revision advances. The first target is handled by the lifecycle
	// effect above; later targets glow only while Browser is not visible.
	useEffect(() => {
		if (!hasInspector) return;
		const previewKey = previewRevealKey(previewUrl, previewRevision);
		const seenKey = useUiStore.getState().inspectorSessions[sessionId]?.previewKey;
		if (seenKey === undefined) {
			markInspectorPreviewSeen(sessionId, previewKey);
			return;
		}
		if (seenKey === previewKey) return;
		markInspectorPreviewSeen(sessionId, previewKey);
		if (!previewKey) return;
		const current = useUiStore.getState().inspectorSessions[sessionId];
		const viewingBrowser = browserIsVisible(sessionId, browserPoppedOut);
		if (current?.browserContentRevealed && !viewingBrowser) setBrowserUnseen(sessionId, true);
	}, [
		browserPoppedOut,
		hasInspector,
		markInspectorPreviewSeen,
		previewRevision,
		previewUrl,
		sessionId,
		setBrowserUnseen,
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

	// Computed when the inspector panel mounts and frozen while it stays
	// mounted: rrp re-registers the panel (a layout effect keyed on defaultSize,
	// among others) whenever this prop's identity changes, and the imperative
	// collapse()/resize() below can race that re-registration within the same
	// commit — rrp then throws "Panel constraints not found for Panel
	// inspector", which unwinds the whole route to the router's CatchBoundary
	// (the toggle button looks dead and the session view is torn down).
	// Re-derived per panel mount (not once per SessionView mount — navigating
	// orchestrator → worker keeps this component mounted while the panel
	// remounts) so a freshly mounted panel reflects the store on its own,
	// without an imperative fix-up in the mount commit. Afterwards the
	// imperative API owns the size, so this must never track live open state.
	const inspectorDefaultSizeRef = useRef<string | null>(null);
	if (!hasInspector) {
		inspectorDefaultSizeRef.current = null;
	} else if (inspectorDefaultSizeRef.current === null) {
		inspectorDefaultSizeRef.current = isInspectorOpen ? `${initialSplitPercent()}%` : "0%";
	}
	const inspectorDefaultSize = inspectorDefaultSizeRef.current ?? "0%";

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

	// Drive the collapsible panel from the store so the topbar button, ⌘⇧B, and
	// drag-to-reopen all stay in sync. When the inspector panel mounts into
	// the already-live group (orchestrator/loading → worker), rrp only derives
	// the new panel's constraints in the next commit. This effect intentionally
	// runs before the readiness effect below, so mount and StrictMode's effect
	// replay remain imperative-free; later store changes can safely drive the
	// registered panel.
	const inspectorImperativeReadyRef = useRef(false);
	useEffect(() => {
		if (!hasInspector || !inspectorImperativeReadyRef.current) return;
		const panel = inspectorRef.current;
		if (!panel) return;
		if (isInspectorOpen) {
			// resize(), not expand(): by the time this effect runs the panel has
			// re-registered as non-collapsible (open panels refuse drag-collapse),
			// and rrp's expand() no-ops on a non-collapsible panel. resize() also
			// restores the persisted split regardless of what "most recent size"
			// rrp remembers, which is 0 when the panel mounted collapsed.
			panel.resize(`${initialSplitPercent()}%`);
			return;
		}
		// Closing flips `collapsible` back on in this same commit, but rrp only
		// re-derives the group's constraints in the follow-up commit its
		// registration effect schedules — so this first collapse() still sees the
		// open panel's non-collapsible constraints and no-ops. Repeat it on the
		// next frame, when the fresh constraints have landed; collapse() is
		// idempotent, so the double call is safe wherever the derivation lands.
		panel.collapse();
		const frame = window.requestAnimationFrame(() => panel.collapse());
		return () => window.cancelAnimationFrame(frame);
	}, [hasInspector, isInspectorOpen]);
	useEffect(() => {
		if (!hasInspector || !inspectorRef.current) {
			inspectorImperativeReadyRef.current = false;
			return;
		}
		inspectorImperativeReadyRef.current = true;
		return () => {
			inspectorImperativeReadyRef.current = false;
		};
	}, [hasInspector]);

	// Persist drags and mirror a drag-reopen (dragging the separator of a
	// collapsed inspector past the snap point) back into the store. Dragging an
	// open inspector can never collapse it — the panel is non-collapsible while
	// open, so rrp clamps the drag at minSize instead of snapping to 0%.
	// Read the store imperatively to avoid a stale closure.
	// Gated on an actively dragged separator: rrp v4 derives sizes from the
	// observed DOM layout, so the flex-grow transition that animates
	// resize()/collapse() (styles.css) fires onResize with transient
	// mid-animation sizes too. Writing those back turned the imperative
	// collapse into a feedback loop — a mid-collapse size read as "dragged
	// back open", re-toggled the store, and the panel bounced back (the
	// topbar button looked dead). rrp marks the separator
	// data-separator="active" only during a pointer drag — the same hook the
	// transition-suppressing CSS keys on, so drag writes are never transition
	// frames.
	// Also wrapped in useCallback: rrp v4's panel registration useLayoutEffect
	// includes onResize in its dep array, so an unstable reference would
	// de-register/re-register the inspector panel on every render and race
	// with the resize()/collapse() effect above.
	const handleInspectorResize = useCallback(
		(size: PanelSize) => {
			if (inspectorSeparatorRef.current?.getAttribute("data-separator") !== "active") return;
			if (size.asPercentage <= 0) return;
			window.localStorage?.setItem(inspectorSplitStorageKey, String(size.asPercentage));
			const currentOpen = useUiStore.getState().inspectorSessions[sessionId]?.isOpen ?? true;
			if (!currentOpen) toggleInspector(sessionId);
		},
		[sessionId, toggleInspector],
	);

	if (!session && !workspaceQuery.isLoading) {
		return (
			<div className="grid h-full place-items-center p-6 text-center font-mono text-xs text-passive">
				{t("session.notFound")}
			</div>
		);
	}

	return (
		<div className="relative flex h-full min-h-0 flex-col bg-background text-foreground" data-testid="session-detail">
			<PendingReviewCommentsProvider sessionId={sessionId}>
			<ResizablePanelGroup className="session-split min-h-0 flex-1" id="session-workspace" orientation="horizontal">
				{/* react-resizable-panels v4: bare numbers are PIXELS; percentages must
            be strings. Numeric sizes here once clamped the inspector to 45px. */}
				{/* RRP's inner panel defaults to overflow:auto. Nothing scrolls at pane level,
				    so clip the chat rail's active marker instead of creating a horizontal scrollbar. */}
				<ResizablePanel defaultSize="72%" id="terminal" minSize="45%" style={{ overflow: "hidden" }}>
					<div className="relative h-full min-h-0">
						{/* The committed mode owns the agent surface. Auxiliary shell and
						    reviewer targets remain terminal surfaces in either mode. */}
						{showChatSurface ? (
							<SessionChatSurface
								session={session}
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
								onNewShellTerminal={addShellTerminal}
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
						{interfaceSwitch.transition?.id !== dismissedTransitionID ? (
							<SessionInterfaceTransitionNotice
								transition={interfaceSwitch.transition}
								onDismiss={() => setDismissedTransitionID(interfaceSwitch.transition?.id ?? "")}
							/>
						) : null}
					</div>
				</ResizablePanel>
				{hasInspector ? (
					<>
						<ResizableHandle
							className="w-1.75 cursor-col-resize touch-none bg-transparent after:w-px after:bg-border-strong hover:after:bg-border focus-visible:ring-0 focus-visible:ring-offset-0 focus-visible:after:bg-border data-[separator=active]:after:bg-border"
							elementRef={inspectorSeparatorRef}
						/>
						<ResizablePanel
							aria-hidden={!isInspectorOpen}
							collapsible={!isInspectorOpen}
							defaultSize={inspectorDefaultSize}
							id="inspector"
							inert={!isInspectorOpen}
							maxSize={`${INSPECTOR_MAX_PERCENT}%`}
							minSize={`${INSPECTOR_MIN_PERCENT}%`}
							onResize={handleInspectorResize}
							panelRef={inspectorRef}
							style={{ overflow: "hidden" }}
						>
							{/* Stable content width while the panel animates (yyork pattern):
                  the pane clips instead of reflowing the inspector mid-collapse. */}
							<div className="h-full min-w-inspector-min">
								<SessionInspector
									browserAnnotationQueue={browserAnnotationQueue}
									browserPoppedOut={browserPoppedOut}
									filesView={
										session ? (
											<SessionFilesView onToggleMaximized={handleToggleFilesPopOut} sessionId={session.id} />
										) : null
									}
									isInspectorVisible={isInspectorOpen}
									onOpenFiles={handleOpenFiles}
									onOpenReviewerTerminal={selectReviewerTerminal}
									onToggleBrowserPopOut={handleToggleBrowserPopOut}
									onViewChange={(next: InspectorView) => setInspectorViewForSession(sessionId, next)}
									view={inspectorView}
									browserView={browserView}
									session={session}
								/>
							</div>
						</ResizablePanel>
					</>
				) : null}
			</ResizablePanelGroup>
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
			</PendingReviewCommentsProvider>
		</div>
	);
}
