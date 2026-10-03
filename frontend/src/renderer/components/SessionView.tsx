import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe2, Loader2, PanelRight, Plus } from "lucide-react";
import { useBlocker } from "@tanstack/react-router";
import { motion } from "motion/react";
import {
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
	type CSSProperties,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { defaultShortcutBindings, shortcutBindingLabel } from "../../shared/shortcuts";
import { BrowserPanelView, useBrowserAnnotationQueue } from "./BrowserPanel";
import { CenterPane } from "./CenterPane";
import type { FileOpenOptions, FileViewMode } from "./FileContentPane";
import { SessionChatSurface } from "./chat/SessionChatSurface";
import { CloudSessionChatSurface } from "./chat/CloudSessionChatSurface";
import { ReviewerChatSurface } from "./chat/ReviewerChatSurface";
import { NotificationCenter } from "./NotificationCenter";
import { SessionInspectorRail, initialInspectorSize, inspectorSizing, sizingGeometryEqual, INSPECTOR_SPRING_MS, INSPECTOR_SPRING_EASING, type InspectorSizing } from "./SessionInspectorRail";
import { SessionFileExplorer } from "./SessionFileExplorer";
import { FilesTopbarHostContext } from "./files-topbar-host";
import { CloudFileContentPane, CloudWorkspaceDiff } from "./CloudWorkspaceDiff";
import { SessionFileTab } from "./SessionFileTabs";
import { SessionFileWorkspace } from "./SessionFileWorkspace";
import { SessionFilesPopOut } from "./SessionFilesPopOut";
import { SessionBrowserPopOut } from "./SessionBrowserPopOut";
import { SessionActionsMenu } from "./SessionActionsMenu";
import { SessionInspector } from "./SessionInspector";
import { ShellTopbar } from "./ShellTopbar";
import { SwitchAgentDialog } from "./SwitchAgentDialog";
import { SessionTopbarHost } from "./SessionTopbarPortal";
import { TerminalSwitchAgentButton } from "./TerminalSwitchAgentButton";
import { TopbarButton } from "./TopbarButton";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { MultiStepLoader } from "./ui/multi-step-loader";
import { useBrowserView } from "../hooks/useBrowserView";
import { useFileAnnotation } from "../hooks/useFileAnnotation";
import {
	useCloseShellTerminal,
	useOpenShellTerminal,
	useRenameShellTerminal,
	useShellTerminals,
} from "../hooks/useShellTerminals";
import { useSessionInterfaceSwitch } from "../hooks/useSessionInterfaceSwitch";
import { discardCapturedPendingFileAttachments } from "../hooks/useFileAttachments";
import { useAgentSwitchRouteVisibility } from "../hooks/useAgentSwitchVisibility";
import {
	toCloudWorkspaceSession,
	useCloudSessionQuery,
	useWorkspaceQuery,
	useWorkspaceSession,
	workspaceQueryKeyForHost,
} from "../hooks/useWorkspaceQuery";
import { cloudLifecycleStage } from "../lib/cloud-lifecycle";
import { subscribeSessionEventsBridged } from "../lib/cloud-cp/stream-bridge";
import { useTerminalResetStore } from "../stores/terminal-reset-store";
import { useCloudCp } from "../hooks/useCloudCp";
import { useSessionHandoffMenu } from "../hooks/useSessionHandoffMenu";
import { clearSwitchAgentState } from "../hooks/useSwitchAgent";
import { apiErrorCode, apiErrorMessage } from "../lib/api-client";
import { clientForSessionHost } from "../lib/host-clients";
import { useHostConnection } from "../hooks/useHostConnection";
import { sessionReviewsQueryKey } from "../lib/session-reviews";
import { sessionUiKey } from "../lib/hosts";
import { sessionWorkspaceFilesQueryOptions } from "../hooks/useSessionWorkspaceFiles";
import { matchWorkspaceFilePath } from "../lib/workspace-file-path";
import { markFileViewerPerformance } from "../lib/file-viewer-performance";
import { aoBridge } from "../lib/bridge";
import {
	chatDraftDialogCopy,
} from "../lib/chat-draft-boundary";
import {
	activateSessionFile,
	closeSessionFile,
	EMPTY_SESSION_FILE_TABS,
	openSessionFile,
	type SessionFileTabState,
} from "../lib/session-file-tabs";
import { isMacPlatform } from "../lib/platform";
import { useShell } from "../lib/shell-context";
import { cn } from "../lib/utils";
import { isOrchestratorSession, sessionIsActive } from "../types/workspace";
import { terminalTargetBelongsToSession, type TerminalTarget } from "../types/terminal";
import { matchesRendererShortcut } from "../stores/keybindings-store";
import { inspectorIsOpen, useResolvedTheme, useUiStore, type InspectorView } from "../stores/ui-store";
import {
	INSPECTOR_SEPARATOR_RESERVE_PX,
	inspectorMaxWidthCss,
} from "../lib/inspector-width";

type CenterFileOpenRequest = { commitSha?: string; editing: boolean; key: number; mode: FileViewMode; scope?: FileOpenOptions["scope"] };
const EMPTY_AUXILIARY_TAB_ORDER: string[] = [];
// Centre-file open requests take keys from this process-wide counter, not a
// per-mount one: the display mode remembered for a request (ui-store) outlives a
// SessionView remount, so a new request must never reuse a recorded key.
let lastCenterFileRequestKey = 0;
function nextCenterFileRequestKey(): number {
	lastCenterFileRequestKey += 1;
	return lastCenterFileRequestKey;
}
// The inspector tab labels respond to the tablist's remaining width. The
// 239px tablist breakpoint plus the 76px pinned-action reserve and 10px leading
// inset gives a 325px inspector breakpoint for the animation lock.
const INSPECTOR_COMPACT_MAX_PX = 325;
const TOPBAR_SECONDARY_COMPACT_MAX_PX = 759;
const isMac = isMacPlatform();
const noDragStyle = isMac ? ({ WebkitAppRegion: "no-drag" } as CSSProperties) : undefined;
const newTerminalShortcutLabel = shortcutBindingLabel(defaultShortcutBindings("new-shell-terminal", isMac)[0], isMac);

type ReviewsResponse = components["schemas"]["ListReviewsResponse"];
type ReviewerTerminalTarget = { handleId: string; harness: string };
type ReviewerChatTarget = { reviewId: string; harness: string };

type BrowserPopOutPhase = "docked" | "mounting" | "open";
type BrowserPopOutState = {
	sessionId: string;
	phase: BrowserPopOutPhase;
};

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
	const { inspectorSessions } = useUiStore.getState();
	return inspectorIsOpen(inspectorSessions, sessionId) && (inspectorSessions[sessionId]?.view ?? "summary") === "browser";
}

function reviewerTerminalFromReviews(data?: ReviewsResponse): ReviewerTerminalTarget | undefined {
	if (data?.reviewerSurface?.mode === "chat") return undefined;
	const handleId = data?.reviewerHandleId?.trim();
	if (!handleId) return undefined;
	const latest = data?.reviews?.find((review) => review.latestRun)?.latestRun;
	return { handleId, harness: data?.reviewerHarness || latest?.harness || "codex" };
}

function reviewerChatFromReviews(data?: ReviewsResponse): ReviewerChatTarget | undefined {
	const surface = data?.reviewerSurface;
	if (surface?.mode !== "chat" || !surface.reviewId) return undefined;
	return { reviewId: surface.reviewId, harness: surface.harness || "codex" };
}

type SessionViewProps = {
	sessionId: string;
	cloudOrgId?: string;
	projectId?: string;
	hostId?: string;
};

// The session detail screen: terminal + git rail. On Win/Linux the shell owns
// ShellTopbar above this view; when the platform hides the shell topbar
// (macOS), the same topbar mounts here so the outer panel stays full-height.
// Rendered by both the project-scoped and cross-project session routes.
// The persistent shell cache owns terminal lifetime by logical session + handle:
// route switches retain the xterm instance and latest output, while a replacement
// handle gets a clean xterm/mux binding.
//
// The inspector uses the same Motion spring as the left sidebar (gap width +
// x-transform). Summary/Reviews/Files share a utility width, while Browser
// automatically grows into a co-work canvas. Chat readability clamps either
// profile before the conversation can become unusably narrow.
// Startup steps: 0 creating the workspace, 1 connecting to the worker,
// 2 preparing the repository and agent, 3 connecting the terminal. The
// workspace exists once the sandbox reaches "bootstrapping" (the provider
// reports it running and AO is starting its worker inside); "requested" and
// "provisioning" are still creating it.
function cloudStartupStage(observedState: string | undefined, workerConnected: boolean, terminalOnly = false): number {
	return terminalOnly ? 3
		: workerConnected && (observedState === "bootstrapping" || observedState === "running") ? 2
		: observedState === "bootstrapping" || observedState === "running" ? 1
		: 0;
}

function CloudSessionLifecycleLoader({ sessionId, orgId, createdAt, observedState, workerConnected, terminalOnly, completed = false }: { sessionId: string; orgId: string; createdAt?: string; observedState?: string; workerConnected: boolean; terminalOnly: boolean; completed?: boolean }) {
	const { t } = useTranslation();
	const { baseUrl, client } = useCloudCp();
	const factIndex = cloudStartupStage(observedState, workerConnected, terminalOnly);
	const [factProgress, setFactProgress] = useState({ index: factIndex, since: createdAt ?? new Date().toISOString() });
	const [remoteProgress, setRemoteProgress] = useState({ index: factIndex, since: createdAt ?? new Date().toISOString() });
	const [progress, setProgress] = useState({ index: terminalOnly ? 3 : 0, since: createdAt ?? new Date().toISOString() });
	const replayCutoff = useRef(Date.now() - 60 * 60 * 1_000);
	useEffect(() => {
		if (!baseUrl || !orgId || terminalOnly) return;
		const controller = new AbortController();
		void subscribeSessionEventsBridged({
			baseUrl,
			orgId,
			sessionId,
			after: 0,
			signal: controller.signal,
			onEvent: (event) => {
				const occurredAt = Date.parse(event.createdAt);
				if (event.sessionId !== sessionId || !Number.isFinite(occurredAt) || occurredAt < replayCutoff.current) return;
				// sandbox.provisioning only marks the start of workspace creation;
				// the workspace's completion comes from the observed state above.
				const index = event.type === "worker.connected" || event.type === "worker.ready" ? 2
					: event.type === "agent.ready" ? 3
					: undefined;
				if (index !== undefined) setProgress((current) => index > current.index ? { index, since: event.createdAt } : current);
			},
		});
		return () => controller.abort();
	}, [baseUrl, orgId, sessionId, terminalOnly]);
	useEffect(() => {
		setFactProgress((current) => current.index === factIndex ? current : { index: factIndex, since: new Date().toISOString() });
	}, [factIndex]);
	useEffect(() => {
		if (!orgId || terminalOnly) return;
		const controller = new AbortController();
		let timer: number | undefined;
		const poll = async () => {
			try {
				const { session } = await client.getSession(orgId, sessionId, { signal: controller.signal });
				if (controller.signal.aborted) return;
				const index = cloudStartupStage(session.observedState, session.runtimeConnected);
				setRemoteProgress((current) => current.index === index ? current : { index, since: new Date().toISOString() });
			} catch {
				// Keep the last confirmed stage and retry while the loader is visible.
			} finally {
				if (!controller.signal.aborted) timer = window.setTimeout(() => void poll(), 2_000);
			}
		};
		void poll();
		return () => {
			controller.abort();
			if (timer !== undefined) window.clearTimeout(timer);
		};
	}, [client, orgId, sessionId, terminalOnly]);
	useEffect(() => {
		if (!orgId || terminalOnly) return;
		const controller = new AbortController();
		let after = 0;
		let timer: number | undefined;
		const poll = async () => {
			try {
				let latest: { index: number; since: string } | undefined;
				for (;;) {
					const page = await client.listChatEvents(orgId, sessionId, { after, limit: 500 }, { signal: controller.signal });
					if (controller.signal.aborted) return;
					for (const event of page.events) {
						// Complete the replay before painting a stage. Old epochs can
						// contain agent.ready long before this workspace restart.
						const occurredAt = Date.parse(event.createdAt);
						if (!Number.isFinite(occurredAt) || occurredAt < replayCutoff.current) continue;
						const index = event.type === "worker.connected" || event.type === "worker.ready" ? 2
							: event.type === "agent.ready" ? 3
							: undefined;
						if (index !== undefined) latest = { index, since: event.createdAt };
					}
					after = page.nextAfter;
					if (!page.hasMore) break;
				}
				if (latest) setProgress((current) => latest.index > current.index ? latest : current);
			} catch {
				// Keep the current stage and retry while the session is loading.
			} finally {
				if (!controller.signal.aborted) timer = window.setTimeout(() => void poll(), 2_000);
			}
		};
		void poll();
		return () => {
			controller.abort();
			if (timer !== undefined) window.clearTimeout(timer);
		};
	}, [client, orgId, sessionId, terminalOnly]);
	const steps = useMemo(() => [
		t("terminal.sessionLoader.workspace"),
		t("terminal.sessionLoader.worker"),
		t("terminal.sessionLoader.repositoryAgent"),
		t("terminal.sessionLoader.terminal"),
	], [t]);
	const confirmedFacts = remoteProgress.index > factProgress.index ? remoteProgress : factProgress;
	const target = confirmedFacts.index > progress.index ? confirmedFacts : progress;
	return (
		<div
			// Sits at the session-pane chrome level: it must cover the loading
			// pane's content (topbar/terminal) but MUST stay below the app overlay
			// layer (`z-overlay`, dialogs/dropdowns). A raw high z (this was `z-[200]`)
			// painted over any shell modal opened while a cloud session loads — the
			// New Task dialog, the project three-dots menu — leaving it invisible
			// behind the loader while Radix still applied `body{pointer-events:none}`,
			// which froze the whole UI (sidebar included). Keep this <= z-overlay.
			className={cn("absolute inset-0 z-chrome grid place-items-center bg-background", completed && "cloud-session-loader--complete pointer-events-none")}
			data-testid="cloud-session-loader-screen"
		>
			<MultiStepLoader
				ariaLabel={t("terminal.sessionLoader.label")}
				activeIndex={completed ? 3 : target.index}
				complete={completed}
				steps={steps}
			/>
		</div>
	);
}

function CloudInterfaceSwitchLoader({ target }: { target: "chat" | "tui" }) {
	const label = `Switching to ${target === "chat" ? "Chat UI" : "Terminal UI"}`;
	return (
		<div className="absolute inset-0 z-chrome grid place-items-center bg-background" data-testid="cloud-interface-switch-loader-screen">
			<div role="status" aria-live="polite" aria-label={label} className="flex flex-col items-center gap-3 text-muted-foreground">
				<Loader2 aria-hidden="true" className="size-6 animate-spin" />
				<span className="text-sm">{label}</span>
			</div>
		</div>
	);
}

function CloudPausedStatus() {
	const { t } = useTranslation();
	return (
		<motion.div
			animate={{ opacity: 1, y: 0 }}
			aria-live="polite"
			className={cn(
				"absolute right-3 top-3 z-20 flex h-7 items-center gap-2 rounded-sm border px-2.5",
				"bg-background/92 font-mono text-[11px] tracking-tight shadow-sm backdrop-blur-sm",
				"border-border/80 text-foreground",
			)}
			data-cloud-lifecycle-stage="paused_by_coder"
			initial={{ opacity: 0, y: -4 }}
			role="status"
		>
			<span
				aria-hidden="true"
				className="size-1.5 rounded-full bg-warning"
			/>
			{t("cloud.lifecycle.pausedByCoder")}
		</motion.div>
	);
}

export function SessionView({ sessionId, cloudOrgId, projectId, hostId }: SessionViewProps) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const uiSessionId = sessionUiKey(sessionId, hostId);
	const { baseUrl: remoteBase, label: hostLabel } = useHostConnection(hostId);
	const refreshWorkspaces = useCallback(
		() => queryClient.invalidateQueries({ queryKey: workspaceQueryKeyForHost(hostId) }),
		[hostId, queryClient],
	);
	const workspaceQuery = useWorkspaceQuery({ enabled: !hostId });
	const remoteSessionQuery = useWorkspaceSession(sessionId, hostId, false);
	const workspaces = hostId ? [] : workspaceQuery.data ?? [];
	const routedWorkspaces = projectId ? workspaces.filter((workspace) => workspace.id === projectId) : [];
	const listedMatches = (projectId ? routedWorkspaces : workspaces.filter((workspace) => workspace.kind !== "cloud"))
		.flatMap((workspace) => workspace.sessions.filter((candidate) => candidate.id === sessionId));
	const listedSession = listedMatches.length === 1 ? listedMatches[0] : undefined;
	const routedWorkspace = projectId
		? routedWorkspaces.find((workspace) => listedSession && workspace.sessions.includes(listedSession)) ??
			(routedWorkspaces.length === 1 ? routedWorkspaces[0] : undefined)
		: undefined;
	const ambiguousRoute = listedMatches.length > 1 || Boolean(projectId && routedWorkspaces.length > 1 && !listedSession);
	const isCloudRoute = routedWorkspace?.kind === "cloud";
	// The unscoped /sessions/:id route is local; Cloud sessions have project routes.
	// Newly-created Cloud sessions can be routed before the list cache refreshes.
	const cloudLookupEnabled = Boolean(cloudOrgId && projectId && !ambiguousRoute && (isCloudRoute || (!routedWorkspace && !listedSession)));
	const cloudRouteSession = useCloudSessionQuery(
		cloudOrgId,
		sessionId,
		cloudLookupEnabled,
	);
	const localLookupEnabled = !ambiguousRoute && !isCloudRoute && (
		!cloudOrgId || !projectId || Boolean(listedSession && !listedSession.cloud) ||
		Boolean(routedWorkspace) || cloudRouteSession.isError ||
		Boolean(projectId && cloudRouteSession.data && cloudRouteSession.data.projectId !== projectId)
	);
	const workspaceSessionQuery = useWorkspaceSession(sessionId, undefined, !hostId && localLookupEnabled);
	const directCloudWorkspace = cloudRouteSession.data
		? workspaces.find((workspace) => workspace.kind === "cloud" && workspace.id === cloudRouteSession.data?.projectId)
		: undefined;
	const cloudSessionWorkspace = directCloudWorkspace ?? (isCloudRoute ? routedWorkspace : undefined);
	const directCloudSession = cloudOrgId && cloudRouteSession.data && (!routedWorkspace || isCloudRoute) &&
		projectId === cloudRouteSession.data.projectId
		? toCloudWorkspaceSession(cloudRouteSession.data, {
			id: cloudSessionWorkspace?.id ?? cloudRouteSession.data.projectId,
			displayName: cloudSessionWorkspace?.name ?? "Cloud project",
		}, cloudOrgId)
		: undefined;
	const fallbackSession = !projectId || workspaceSessionQuery.data?.workspaceId === projectId
		? workspaceSessionQuery.data : undefined;
	const scopedFallback = routedWorkspace && fallbackSession && Boolean(fallbackSession.cloud) !== isCloudRoute
		? undefined : fallbackSession;
	const session = hostId ? (remoteBase && !remoteSessionQuery.isError && remoteSessionQuery.data?.workspaceId === (projectId ?? remoteSessionQuery.data?.workspaceId) ? remoteSessionQuery.data : undefined)
		: ambiguousRoute ? undefined : listedSession ?? directCloudSession ?? scopedFallback;
	const interfaceContext = hostId ?? (session ? session.cloud ?? null : cloudOrgId && projectId ? { orgId: cloudOrgId } : undefined);
	const interfaceUi = useSessionInterfaceSwitch(sessionId, session, interfaceContext);
	const { draftBoundaries: chatDraftBoundaries, confirmUnsafeDraftLeave } = interfaceUi;
	const developerMode = useUiStore((state) => state.developerMode);
	const remoteHostsEnabled = useUiStore((state) => state.remoteHosts);
	const setRemoteHosts = useUiStore((state) => state.setRemoteHosts);
	useBlocker({
		disabled: chatDraftBoundaries.length === 0,
		enableBeforeUnload: chatDraftBoundaries.length > 0,
		shouldBlockFn: async () => {
			const decision = await confirmUnsafeDraftLeave();
			if (decision.kind === "cancelled") {
				if (hostId && developerMode && !remoteHostsEnabled) setRemoteHosts(true);
				return true;
			}
			if (decision.kind === "confirmed") {
				// Route navigation is the boundary itself, so confirmed in-flight file
				// work can be invalidated now. Interface switches defer this until the
				// exact durable transition reports completed.
				discardCapturedPendingFileAttachments(decision.pendingAttachments);
			}
			return false;
		},
	});
	useEffect(() => {
		aoBridge.app.setChatDraftRisk?.(chatDraftBoundaries, chatDraftDialogCopy(chatDraftBoundaries));
	}, [chatDraftBoundaries, t]);
	useEffect(
		() => () => aoBridge.app.setChatDraftRisk?.([]),
		[sessionId],
	);
	const { client: cloudCpClient } = useCloudCp();
	const theme = useResolvedTheme();
	const browserOnly = Boolean(session && isOrchestratorSession(session));
	const isInspectorOpen = useUiStore((state) => inspectorIsOpen(state.inspectorSessions, uiSessionId));
	const inspectorView = useUiStore((state) => browserOnly ? "browser" : state.inspectorSessions[uiSessionId]?.view ?? "summary");
	const browserUnseen = useUiStore((state) => Boolean(state.inspectorSessions[uiSessionId]?.browserUnseen));
	const setInspectorOpenForSession = useUiStore((state) => state.setInspectorOpen);
	const toggleInspector = useUiStore((state) => state.toggleInspector);
	const setInspectorViewForSession = useUiStore((state) => state.setInspectorView);
	const setFilesChangedOnly = useUiStore((state) => state.setFilesChangedOnly);
	const initializeInspectorSession = useUiStore((state) => state.initializeInspectorSession);
	const setBrowserContentRevealed = useUiStore((state) => state.setBrowserContentRevealed);
	const setBrowserUnseen = useUiStore((state) => state.setBrowserUnseen);
	const { daemonStatus } = useShell();
	const previewBaselineRef = useRef<{ sessionId: string; key: string } | null>(null);
	const sessionSplitRef = useRef<HTMLDivElement | null>(null);
	const terminalLiveResizeTimerRef = useRef<number | null>(null);
	const workspaceResizeTimerRef = useRef<number | null>(null);
	const [inspectorSettledClosed, setInspectorSettledClosed] = useState(!isInspectorOpen);
	const inspectorPanelVisible = isInspectorOpen || !inspectorSettledClosed;
	const [terminalTarget, setTerminalTarget] = useState<TerminalTarget>({ kind: "worker" });
	const [reviewerChatId, setReviewerChatId] = useState<string | null>(null);
	const [browserPopOutState, setBrowserPopOutState] = useState<BrowserPopOutState>({
		sessionId,
		phase: "docked",
	});
	const [filesPoppedOut, setFilesPoppedOut] = useState(false);
	const [filesSplit, setFilesSplit] = useState(() => window.localStorage.getItem("ao.files.diffStyle") === "split");
	const [filePreviewRequestsBySession, setFilePreviewRequestsBySession] = useState<
		Record<string, { path: string; key: number }>
	>({});
	const [fileTabsBySession, setFileTabsBySession] = useState<Record<string, SessionFileTabState>>({});
	const fileTabs = fileTabsBySession[uiSessionId] ?? EMPTY_SESSION_FILE_TABS;
	const [dirtyFilesBySession, setDirtyFilesBySession] = useState<Record<string, Record<string, true>>>({});
	const dirtyFiles = dirtyFilesBySession[uiSessionId] ?? {};
	const [centerFileRequestsBySession, setCenterFileRequestsBySession] = useState<
		Record<string, Record<string, CenterFileOpenRequest>>
	>({});
	const consumedCenterEditingRequestsRef = useRef(new Set<string>());
	const activeCenterFileRequest = fileTabs.activePath
		? centerFileRequestsBySession[uiSessionId]?.[fileTabs.activePath]
		: undefined;
	const activeCenterFileRequestToken = fileTabs.activePath && activeCenterFileRequest
		? `${uiSessionId}:${fileTabs.activePath}:${activeCenterFileRequest.key}`
		: undefined;
	const activeCenterFileInitialEditing = Boolean(
		activeCenterFileRequest?.editing
		&& activeCenterFileRequestToken
		&& !consumedCenterEditingRequestsRef.current.has(activeCenterFileRequestToken),
	);
	const [auxiliaryTabOrderBySession, setAuxiliaryTabOrderBySession] = useState<Record<string, string[]>>({});
	const auxiliaryTabOrder = auxiliaryTabOrderBySession[uiSessionId] ?? EMPTY_AUXILIARY_TAB_ORDER;
	const setAuxiliaryTabOrder = useCallback(
		(nextOrder: string[]) => {
			setAuxiliaryTabOrderBySession((current) => {
				const currentOrder = current[uiSessionId] ?? [];
				const visibleKeys = new Set(nextOrder);
				let nextIndex = 0;
				const mergedOrder = currentOrder.map((key) =>
					visibleKeys.has(key) ? nextOrder[nextIndex++]! : key,
				);
				while (nextIndex < nextOrder.length) mergedOrder.push(nextOrder[nextIndex++]!);
				return { ...current, [uiSessionId]: mergedOrder };
			});
		},
		[uiSessionId],
	);
	const removeAuxiliaryTab = useCallback(
		(key: string) => {
			setAuxiliaryTabOrderBySession((current) => {
				const currentOrder = current[uiSessionId];
				if (!currentOrder?.includes(key)) return current;
				const nextOrder = currentOrder.filter((candidate) => candidate !== key);
				if (nextOrder.length === 0) {
					const { [uiSessionId]: _removed, ...rest } = current;
					return rest;
				}
				return { ...current, [uiSessionId]: nextOrder };
			});
		},
		[uiSessionId],
	);
	const browserPopOutPhase = browserPopOutState.sessionId === sessionId ? browserPopOutState.phase : "docked";
	const browserPopOutMounted = browserPopOutPhase !== "docked";
	const browserPoppedOut = browserPopOutPhase === "open";
	const [browserPopoutTopbarHost, setBrowserPopoutTopbarHost] = useState<HTMLDivElement | null>(null);
	useLayoutEffect(() => {
		if (browserPopOutPhase !== "mounting" || !browserPopoutTopbarHost) return;
		// Establish the portal destination before moving the browser. This layout
		// effect completes before paint, so the user sees one atomic geometry change
		// and the address/tabs never spend a frame underneath the native view.
		setBrowserPopOutState({ sessionId, phase: "open" });
	}, [browserPopOutPhase, browserPopoutTopbarHost, sessionId]);
	const [handoffDialogOpen, setHandoffDialogOpen] = useState(false);
	const handoffDialogContainerRef = useRef<HTMLDivElement | null>(null);
	const [handoffDialogContainer, setHandoffDialogContainer] = useState<HTMLDivElement | null>(null);
	const bindHandoffDialogContainer = useCallback((node: HTMLDivElement | null) => {
		handoffDialogContainerRef.current = node;
		setHandoffDialogContainer(node);
	}, []);
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

	const cloudStage = cloudLifecycleStage(session);
	const cloudReconnecting = useTerminalResetStore((state) => Boolean(state.reconnecting[sessionId]));
	const [terminalAttachment, setTerminalAttachment] = useState({ sessionId: "", attached: false });
	const terminalAttached = terminalAttachment.sessionId === sessionId && terminalAttachment.attached;
	const onSessionTerminalAttached = useCallback((attached: boolean) => {
		setTerminalAttachment((current) => current.sessionId === sessionId && current.attached === attached
			? current
			: { sessionId, attached });
	}, [sessionId]);
	// Latch the session that has reached "connected" at least once (keyed on
	// sessionId so it resets cleanly when the view switches sessions). After the
	// first successful connect, a transient runtime-connection drop while the
	// sandbox is still running (stage flips to "restoring_agent", e.g. the worker
	// relay row cycles mid-turn) or a terminal reconnect must NOT re-raise the
	// full-screen lifecycle loader over the terminal for the rest of the turn.
	// Only a genuine workspace (re)start — VM stopped/resuming/provisioning/
	// bootstrapping — should block after the session has connected once.
	// Checkout and agent startup continue after the worker connects. Wait for
	// the actual terminal attachment before dismissing the startup view.
	const expectsTerminal = session?.mode !== "chat" && !browserOnly;
	const sessionReady = expectsTerminal ? terminalAttached : cloudStage === "connected";
	const connectedSessionRef = useRef("");
	if (sessionReady) connectedSessionRef.current = sessionId;
	const hasConnectedOnce = connectedSessionRef.current === sessionId;
	const workspaceRestarting = cloudStage === "resuming_workspace"
		|| cloudStage === "waiting_for_coder_agent"
		|| cloudStage === "starting_ao_worker";
	// After the first connect, the ONLY case we stop blocking on is
	// "restoring_agent" while the sandbox is still running (a transient runtime
	// relay drop mid-turn). A terminal re-mint (cloudReconnecting, covers a blank
	// flash) and a genuine workspace restart still raise the loader.
	const showLifecycleLoader = hasConnectedOnce
		? (cloudReconnecting || workspaceRestarting)
		: (cloudReconnecting || (cloudStage != null && cloudStage !== "paused_by_coder" && !sessionReady));
	const loaderVisibleLongEnoughRef = useRef("");
	const [completionDismissed, setCompletionDismissed] = useState(false);
	useEffect(() => {
		if (!showLifecycleLoader) return;
		loaderVisibleLongEnoughRef.current = "";
		setCompletionDismissed(false);
		const timer = window.setTimeout(() => { loaderVisibleLongEnoughRef.current = sessionId; }, 200);
		return () => window.clearTimeout(timer);
	}, [sessionId, showLifecycleLoader]);
	const showCompletedLoader = !showLifecycleLoader && sessionReady && loaderVisibleLongEnoughRef.current === sessionId && !completionDismissed;
	useEffect(() => {
		if (!showCompletedLoader) return;
		const timer = window.setTimeout(() => setCompletionDismissed(true), 360);
		return () => window.clearTimeout(timer);
	}, [showCompletedLoader]);
	const cloudResumeRef = useRef("");
	const requestCloudResume = useCallback(async () => {
		if (!session?.cloud) return;
		await cloudCpClient.resumeSession(session.cloud.orgId, session.id);
		await refreshWorkspaces();
	}, [cloudCpClient, refreshWorkspaces, session]);
	useEffect(() => {
		if (!session?.cloud || cloudResumeRef.current === session.id) return;
		cloudResumeRef.current = session.id;
		void requestCloudResume().catch(() => {
			// Keep the paused lifecycle projection visible. A later message, shell
			// open, or route visit can issue a fresh explicit resume intent.
		});
	}, [requestCloudResume, session]);
	const routeVisibilityOperation =
		session?.activeAgentSwitch &&
		session.activeAgentSwitch.state !== "completed" &&
		session.activeAgentSwitch.state !== "failed"
			? "active"
			: "history";
	useAgentSwitchRouteVisibility(`session/${uiSessionId}`, routeVisibilityOperation);
	const reviewerQuery = useQuery({
		queryKey: sessionReviewsQueryKey(sessionId, hostId),
		enabled: Boolean(
			(hostId ? remoteBase : window.ao) && session && !session.cloud && sessionIsActive(session) && !isOrchestratorSession(session) && session.prs.length > 0,
		),
		refetchInterval: (query) => {
			const data = query.state.data as ReviewsResponse | undefined;
			return data?.reviews?.some((review) => review.status === "running") ? 2500 : false;
		},
		queryFn: async () => {
			const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/sessions/{sessionId}/reviews", {
				params: { path: { sessionId } },
			});
			if (error) throw new Error(apiErrorMessage(error, "Unable to load reviews"));
			return data ?? ({ reviewerHandleId: "", reviews: [], runs: [] } satisfies ReviewsResponse);
		},
	});
	const availableReviewerTerminal = reviewerTerminalFromReviews(reviewerQuery.data);
	const reviewerTerminal = session && sessionIsActive(session) ? availableReviewerTerminal : undefined;
	const availableReviewerChat = reviewerChatFromReviews(reviewerQuery.data);
	const reviewerChat = session && sessionIsActive(session) ? availableReviewerChat : undefined;
	useEffect(() => {
		if (!reviewerChatId || !reviewerQuery.isFetched) return;
		if (availableReviewerChat?.reviewId !== reviewerChatId) {
			setReviewerChatId(null);
		}
	}, [availableReviewerChat?.reviewId, reviewerChatId, reviewerQuery.isFetched]);

	// Shell terminals opened inside a session live beside its pane as extra tabs,
	// scoped to the session on screen so each session has its own shell set.
	const allShellTerminals = useShellTerminals(hostId).data ?? [];
	const shellTerminals = useMemo(
		() => allShellTerminals.filter((shell) => shell.sessionId === sessionId),
		[allShellTerminals, sessionId],
	);
	const resolvedAuxiliaryTabOrder = useMemo(() => {
		const openFileKeys = fileTabs.openPaths.map((path) => `file:${path}`);
		const openShellKeys = shellTerminals.map((shell) => shell.handleId);
		const available = [
			...(reviewerTerminal ? [`reviewer:${reviewerTerminal.handleId}`] : []),
			...(!reviewerTerminal && reviewerChat ? [`reviewer-chat:${reviewerChat.reviewId}`] : []),
			...openFileKeys,
			...openShellKeys,
		];
		const availableKeys = new Set(available);
		const resolved = auxiliaryTabOrder.filter((key) => availableKeys.has(key));
		for (const key of available) {
			if (!resolved.includes(key)) resolved.push(key);
		}
		return resolved;
	}, [auxiliaryTabOrder, fileTabs.openPaths, reviewerChat, reviewerTerminal, shellTerminals]);
	useEffect(() => {
		setAuxiliaryTabOrderBySession((current) => {
			const currentOrder = current[uiSessionId] ?? [];
			const newKeys = resolvedAuxiliaryTabOrder.filter((key) => !currentOrder.includes(key));
			if (newKeys.length === 0) {
				return current;
			}
			return { ...current, [uiSessionId]: [...currentOrder, ...newKeys] };
		});
	}, [resolvedAuxiliaryTabOrder, uiSessionId]);
	const openShellTerminal = useOpenShellTerminal(hostId);
	const closeShellTerminal = useCloseShellTerminal(hostId);
	const renameShellTerminal = useRenameShellTerminal(hostId);
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
		const shell = openShellTerminal.open(
			{ projectId: session?.workspaceId, sessionId, cloud: session?.cloud },
			{
				onSuccess: (openedShell) => {
					setActiveShellTerminal(openedShell.handleId);
					setFileTabsBySession((current) => ({
						...current,
						[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, null),
					}));
					setTerminalTarget({
						generation: openedShell.createdAt,
						kind: "shell",
						handleId: openedShell.handleId,
						sessionId,
						title: openedShell.title,
					});
				},
			},
		);
		if (!shell) return;
		setFileTabsBySession((current) => ({
			...current,
			[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, null),
		}));
		setActiveShellTerminal(shell.handleId);
		setTerminalTarget({
			generation: shell.createdAt,
			kind: "shell",
			handleId: shell.handleId,
			sessionId,
			title: shell.title,
		});
	}, [openShellTerminal, sessionId, session?.cloud, session?.workspaceId, setActiveShellTerminal, uiSessionId]);

	const activateAuxiliaryTab = useCallback(
		(key?: string) => {
			if (key?.startsWith("file:")) {
				const path = key.slice("file:".length);
				setActiveShellTerminal(null);
				setTerminalTarget({ kind: "worker" });
				setFileTabsBySession((current) => ({
					...current,
					[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, path),
				}));
				return;
			}
			if (reviewerTerminal && key === `reviewer:${reviewerTerminal.handleId}`) {
				setActiveShellTerminal(null);
				setTerminalTarget({
					kind: "reviewer",
					handleId: reviewerTerminal.handleId,
					harness: reviewerTerminal.harness,
					sessionId,
				});
				setFileTabsBySession((current) => ({
					...current,
					[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, null),
				}));
				return;
			}
			if (reviewerChat && key === `reviewer-chat:${reviewerChat.reviewId}`) {
				setActiveShellTerminal(null);
				setTerminalTarget({ kind: "worker" });
				setReviewerChatId(reviewerChat.reviewId);
				setFileTabsBySession((current) => ({
					...current,
					[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, null),
				}));
				return;
			}
			const shell = shellTerminals.find((candidate) => candidate.handleId === key);
			if (shell) {
				setActiveShellTerminal(shell.handleId);
				setTerminalTarget({
					generation: shell.createdAt,
					kind: "shell",
					handleId: shell.handleId,
					sessionId,
					title: shell.title,
				});
				setFileTabsBySession((current) => ({
					...current,
					[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, null),
				}));
				return;
			}
			setActiveShellTerminal(null);
			setTerminalTarget({ kind: "worker" });
			setFileTabsBySession((current) => ({
				...current,
				[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, null),
			}));
		},
		[reviewerChat, reviewerTerminal, sessionId, shellTerminals, setActiveShellTerminal, uiSessionId],
	);
	const adjacentAuxiliaryTab = useCallback(
		(closingKey: string) => {
			const closingIndex = resolvedAuxiliaryTabOrder.indexOf(closingKey);
			if (closingIndex < 0) return undefined;
			return resolvedAuxiliaryTabOrder[closingIndex - 1] ?? resolvedAuxiliaryTabOrder[closingIndex + 1];
		},
		[resolvedAuxiliaryTabOrder],
	);

	const selectShellTerminal = useCallback(
		(handleId: string) => {
			const shell = shellTerminals.find((s) => s.handleId === handleId);
			if (!shell) return;
			setReviewerChatId(null);
			setActiveShellTerminal(shell.handleId);
			setFileTabsBySession((current) => ({
				...current,
				[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, null),
			}));
			setTerminalTarget({
				generation: shell.createdAt,
				kind: "shell",
				handleId: shell.handleId,
				sessionId,
				title: shell.title,
			});
		},
		[sessionId, shellTerminals, setActiveShellTerminal, uiSessionId],
	);

	const closeShellTerminalByHandle = useCallback(
		(handleId: string) => {
			if (terminalTarget.kind === "shell" && terminalTarget.handleId === handleId) {
				// Match the visible mixed strip, not the shell-only creation order.
				activateAuxiliaryTab(adjacentAuxiliaryTab(handleId));
			} else if (activeShellTerminalHandleId === handleId) {
				setActiveShellTerminal(null);
			}
			closeShellTerminal.mutate(handleId, {
				onSuccess: () => removeAuxiliaryTab(handleId),
				onError: (error) => {
					if (apiErrorCode(error) === "SHELL_TERMINAL_NOT_FOUND") removeAuxiliaryTab(handleId);
				},
			});
		},
		[
			activeShellTerminalHandleId,
			activateAuxiliaryTab,
			adjacentAuxiliaryTab,
			closeShellTerminal,
			removeAuxiliaryTab,
			setActiveShellTerminal,
			terminalTarget,
		],
	);

	// Selecting the session's own pane also drops the active shell, so the effect
	// above does not immediately pull the view back to that shell.
	const selectSessionTerminal = useCallback(() => {
		setActiveShellTerminal(null);
		setTerminalTarget({ kind: "worker" });
		setReviewerChatId(null);
		setFileTabsBySession((current) => ({
			...current,
			[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, null),
		}));
	}, [setActiveShellTerminal, uiSessionId]);
	const selectReviewerTerminal = useCallback((target: ReviewerTerminalTarget) => {
		setReviewerChatId(null);
		setActiveShellTerminal(null);
		setTerminalTarget({ kind: "reviewer", handleId: target.handleId, harness: target.harness, sessionId });
		setFileTabsBySession((current) => ({
			...current,
			[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, null),
		}));
	}, [sessionId, setActiveShellTerminal, uiSessionId]);
	const selectReviewerChat = useCallback((reviewId: string) => {
		setActiveShellTerminal(null);
		setTerminalTarget({ kind: "worker" });
		setReviewerChatId(reviewId);
		setFileTabsBySession((current) => ({
			...current,
			[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, null),
		}));
	}, [setActiveShellTerminal, uiSessionId]);
	const openCenterFile = useCallback((path: string, options?: FileOpenOptions) => {
		setReviewerChatId(null);
		const key = nextCenterFileRequestKey();
		setCenterFileRequestsBySession((current) => {
			const sessionRequests = current[uiSessionId] ?? {};
			return {
				...current,
				[uiSessionId]: {
					...sessionRequests,
					[path]: {
						commitSha: options?.commitSha,
						editing: options?.editing ?? false,
						key,
						mode: options?.mode ?? "file",
						scope: options?.scope,
					},
				},
			};
		});
		setFileTabsBySession((current) => ({
			...current,
			[uiSessionId]: openSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, path),
		}));
	}, [uiSessionId]);
	const markCenterFileEditingConsumed = useCallback((path: string, requestKey: number) => {
		consumedCenterEditingRequestsRef.current.add(`${uiSessionId}:${path}:${requestKey}`);
	}, [uiSessionId]);
	const setCenterFileDirty = useCallback((path: string, dirty: boolean) => {
		setDirtyFilesBySession((current) => {
			const sessionFiles = current[uiSessionId] ?? {};
			if (dirty) {
				if (sessionFiles[path]) return current;
				return { ...current, [uiSessionId]: { ...sessionFiles, [path]: true } };
			}
			if (!sessionFiles[path]) return current;
			const nextSessionFiles = { ...sessionFiles };
			delete nextSessionFiles[path];
			return { ...current, [uiSessionId]: nextSessionFiles };
		});
	}, [uiSessionId]);
	const activateCenterFile = useCallback((path: string) => {
		setReviewerChatId(null);
		setFileTabsBySession((current) => ({
			...current,
			[uiSessionId]: activateSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, path),
		}));
	}, [uiSessionId]);
	const closeCenterFile = useCallback((path: string) => {
		setFileTabsBySession((current) => ({
			...current,
			[uiSessionId]: closeSessionFile(current[uiSessionId] ?? EMPTY_SESSION_FILE_TABS, path),
		}));
		if (fileTabs.activePath === path) {
			activateAuxiliaryTab(adjacentAuxiliaryTab(`file:${path}`));
		}
		removeAuxiliaryTab(`file:${path}`);
	}, [activateAuxiliaryTab, adjacentAuxiliaryTab, fileTabs.activePath, removeAuxiliaryTab, uiSessionId]);
	// The shell layout owns opening (it is mounted on every route, so the button
	// and ⌘T / Ctrl+T work everywhere); this view only follows the result. When a new
	// shell becomes active while a session is on screen, switch the pane to it —
	// that is what makes the shortcut feel like it opened a terminal *here*.
	useEffect(() => {
		if (!activeShellTerminalHandleId) return;
		const shell = shellTerminals.find((s) => s.handleId === activeShellTerminalHandleId);
		if (!shell) return;
		if (terminalTarget.kind === "shell" && terminalTarget.handleId === shell.handleId &&
			terminalTarget.generation === shell.createdAt && terminalTarget.title === shell.title &&
			!fileTabs.activePath && !reviewerChatId) return;
		selectShellTerminal(shell.handleId);
	}, [activeShellTerminalHandleId, fileTabs.activePath, reviewerChatId, selectShellTerminal, shellTerminals, terminalTarget]);

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
	const hasInspector = Boolean(session);
	const sizing = useMemo(() => inspectorSizing(inspectorView), [inspectorView]);
	const browserEntryWidthFloorRef = useRef<number | null>(null);

	// Arm the shared width transition before the selected inspector surface
	// changes its CSS variable. Browser becomes a co-work canvas; utility views
	// return to their stable rail width on the same spring as the shell sidebar.
	const armWorkspaceTransition = useCallback(() => {
		const split = sessionSplitRef.current;
		if (!split) return;
		if (workspaceResizeTimerRef.current !== null) window.clearTimeout(workspaceResizeTimerRef.current);
		split.setAttribute("data-workspace-resizing", "true");
		void split.offsetWidth;
		workspaceResizeTimerRef.current = window.setTimeout(() => {
			split.removeAttribute("data-workspace-resizing");
			workspaceResizeTimerRef.current = null;
		}, INSPECTOR_SPRING_MS);
	}, []);

	useEffect(
		() => () => {
			if (workspaceResizeTimerRef.current !== null) window.clearTimeout(workspaceResizeTimerRef.current);
			sessionSplitRef.current?.removeAttribute("data-workspace-resizing");
		},
		[],
	);

	const prepareWorkspaceProfile = useCallback(
		(nextSizing: InspectorSizing) => {
			armWorkspaceTransition();
			const groupWidth = sessionSplitRef.current?.clientWidth || window.innerWidth;
			const availableWidth = Math.max(0, groupWidth - INSPECTOR_SEPARATOR_RESERVE_PX);
			const targetInspectorWidth = Number.parseFloat(initialInspectorSize(nextSizing, availableWidth));
			startTerminalLiveResize(
				targetInspectorWidth <= INSPECTOR_COMPACT_MAX_PX ? "compact" : "expanded",
				topbarSecondaryLabelMode(Math.max(0, availableWidth - targetInspectorWidth)),
			);
		},
		[armWorkspaceTransition, startTerminalLiveResize],
	);

	const transitionInspectorView = useCallback(
		(next: InspectorView) => {
			if (next === inspectorView) return;
			if (next === "browser") {
				const currentWidth = Number(window.localStorage.getItem(sizing.storageKey));
				browserEntryWidthFloorRef.current = Number.isFinite(currentWidth) ? currentWidth : null;
			} else {
				browserEntryWidthFloorRef.current = null;
			}
			const nextSizing = inspectorSizing(next);
			if (!sizingGeometryEqual(sizing, nextSizing)) prepareWorkspaceProfile(nextSizing);
			setInspectorViewForSession(uiSessionId, next);
		},
		[
			inspectorView,
			prepareWorkspaceProfile,
			uiSessionId,
			setInspectorViewForSession,
			sizing,
		],
	);

	const newTerminalError = openShellTerminal.error ? apiErrorMessage(openShellTerminal.error) : undefined;
	const newShellTerminalAction = useMemo(() =>
		session && !isOrchestrator ? (
			<Tooltip>
				<TooltipTrigger asChild>
					<TopbarButton
						aria-label={t("shortcut.new-shell-terminal")}
						data-terminal-focus-handoff="true"
						onClick={addShellTerminal}
						type="button"
						variant="icon"
					>
						<Plus aria-hidden="true" className="size-icon-md" />
					</TopbarButton>
				</TooltipTrigger>
				<TooltipContent side="bottom">
					{newTerminalError ?? t("terminal.newWithShortcut", { shortcut: newTerminalShortcutLabel })}
				</TooltipContent>
			</Tooltip>
		) : null,
		[addShellTerminal, isOrchestrator, newTerminalError, newTerminalShortcutLabel, session, t],
	);
	const sendCloudFileAnnotation = useCallback(async (message: string) => {
		const orgId = session?.cloud?.orgId;
		if (!orgId) throw new Error(t("files.feedbackError"));
		await cloudCpClient.sendSessionMessage(orgId, sessionId, { text: message });
	}, [cloudCpClient, session?.cloud?.orgId, sessionId, t]);
	const fileAnnotation = useFileAnnotation(sessionId, { hostId, sendMessage: session?.cloud ? sendCloudFileAnnotation : undefined });
	const centerFileTabs = useMemo(
		() =>
			fileTabs.openPaths.map((path) => ({
				key: `file:${path}`,
				content: (
					<SessionFileTab
						active={fileTabs.activePath === path}
						dirty={Boolean(dirtyFiles[path])}
						onActivate={() => activateCenterFile(path)}
						onClose={() => closeCenterFile(path)}
						path={path}
					/>
				),
				onSelect: () => activateCenterFile(path),
				onClose: () => closeCenterFile(path),
			})),
		[activateCenterFile, closeCenterFile, dirtyFiles, fileTabs.activePath, fileTabs.openPaths],
	);
	const activeWorkspaceTabKey = fileTabs.activePath ? `file:${fileTabs.activePath}` : undefined;
	const previewUrl = session?.previewUrl?.trim() || undefined;
	const previewRevision = session?.previewRevision;
	const browserSlotVisible = Boolean(
		session &&
			hasInspector &&
			(browserPoppedOut || (inspectorPanelVisible && inspectorView === "browser")),
	);
	useEffect(() => {
		if (hostId && remoteBase && browserSlotVisible) void remoteSessionQuery.refetch();
	}, [hostId, remoteBase, browserSlotVisible, remoteSessionQuery.refetch]);
	const terminated = session ? !sessionIsActive(session) : false;
	const browserView = useBrowserView({
		sessionId: uiSessionId,
		origin: hostId ? { hostId, sessionId, proxyBase: remoteBase ?? "" } : undefined,
		active: browserSlotVisible,
		poppedOut: browserPoppedOut,
		terminated,
		previewUrl,
		previewRevision,
	});
	const browserAnnotationQueue = useBrowserAnnotationQueue({
		sessionId: session?.id,
		hostId,
		sourcePreviewUrl: session?.previewUrl,
		navUrl: browserView.navState.url,
	});
	const browserUrl = browserView.navState.url.trim();
	// A terminated session's `previewUrl` is a stale DB fact; useBrowserView
	// suppresses and destroys the live preview for it, so it must not count as
	// content here either — otherwise a merged/terminated session with an old
	// preview auto-opens Browser onto a view the hook has already torn down.
	const hasBrowserContent = !terminated && Boolean(previewUrl || browserUrl);

	// Entering a session for the first time ever always starts on Summary. This
	// must fire exactly once per session's *lifetime*, not once per "was this
	// the last session I looked at" or "is this view currently mounted" — so
	// the initialized flag lives in the ui-store (inspectorSessions[sessionId])
	// rather than a component-local ref, and survives both re-entering a
	// different previously-visited session and unmounting/remounting this view
	// entirely (e.g. across route transitions). Treat browser content that
	// already existed when the route resolved as the baseline for that visit;
	// only preview work arriving afterward may reveal Browser automatically.
	useLayoutEffect(() => {
		if (!session) return;
		if (browserOnly) {
			const current = useUiStore.getState().inspectorSessions[uiSessionId];
			if (!current) setInspectorOpenForSession(uiSessionId, false);
			if (current?.view !== "browser") setInspectorViewForSession(uiSessionId, "browser");
			return;
		}
		initializeInspectorSession(uiSessionId, hasBrowserContent, hasInspector);
	}, [browserOnly, hasBrowserContent, hasInspector, session, uiSessionId, initializeInspectorSession, setInspectorOpenForSession, setInspectorViewForSession]);

	useLayoutEffect(() => {
		setTerminalTarget({ kind: "worker" });
		setReviewerChatId(null);
		setBrowserPopOutState({ sessionId, phase: "docked" });
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
	const renderedSessionMode = interfaceUi.renderedMode;
	const showChatSurface =
		session !== undefined &&
		renderedSessionMode === "chat" &&
		(chatTargetKind === "worker" || chatTargetKind === "reviewer" || chatTargetKind === "shell");
	const {
		agentSwitch: handoffAgentSwitch,
		switchControlPresentation: handoffControlPresentation,
		switchError: handoffSwitchError,
	} = useSessionHandoffMenu(session);
	const handleHandoffDialogOpenChange = useCallback(
		(nextOpen: boolean) => {
			setHandoffDialogOpen(nextOpen);
			if (!nextOpen && handoffSwitchError && session) {
				clearSwitchAgentState(queryClient, session.id, hostId);
			}
		},
		[handoffSwitchError, hostId, queryClient, session],
	);
	useEffect(() => {
		if (handoffSwitchError) setHandoffDialogOpen(true);
	}, [handoffSwitchError]);
	const handoffMenuItem = useMemo(() => session && !session.cloud ? (
		<TerminalSwitchAgentButton
			key={session.id}
			variant="menu-item"
			agentSwitch={handoffAgentSwitch}
			onOpenChange={handleHandoffDialogOpenChange}
			open={handoffDialogOpen}
			presentation={handoffControlPresentation}
			session={session}
			switchError={handoffSwitchError}
		/>
	) : null, [handoffAgentSwitch, handoffControlPresentation, handoffDialogOpen, handoffSwitchError, handleHandoffDialogOpenChange, session]);
	// Cloud sessions only expose the interface switch; agent handoff is local.
	const sessionTabActions = useMemo(() => interfaceUi.unsupported ? null : (
		<SessionActionsMenu inlineStatus={interfaceUi.inlineStatus}>
			{interfaceUi.menuItem}
			{handoffMenuItem}
		</SessionActionsMenu>
	), [handoffMenuItem, interfaceUi.inlineStatus, interfaceUi.menuItem, interfaceUi.unsupported]);
	const sessionHeaderActions = (
		<div
			className="session-topbar-session-chrome flex shrink-0 items-center"
			data-compact-session-chrome="false"
		>
			{hostId ? <span className="max-w-40 truncate px-3 text-xs text-muted-foreground" title={hostId}>{hostLabel ?? hostId}</span> : null}
			<ShellTopbar embedded />
		</div>
	);
	// Spinner replaces the ⋮ at the same size, so the tab title does not need a
	// wider action slot while switching.
	const sessionTabActionWide = false;

	useEffect(() => {
		setHandoffDialogOpen(false);
	}, [sessionId]);

	// The pane shows one terminal at a time, so selecting a shell or the reviewer
	// takes the agent's terminal off screen while the route still points here.
	// Publish which one is showing: the notification runtime lives outside this
	// subtree and must not treat "on the session route" as "watching the agent".
	useEffect(() => {
		setVisibleTerminalKind(uiSessionId, reviewerChatId ? "reviewer" : routedTerminalTarget.kind);
		return () => clearVisibleTerminalKind(uiSessionId);
	}, [clearVisibleTerminalKind, reviewerChatId, routedTerminalTarget.kind, uiSessionId, setVisibleTerminalKind]);

	const prepareFilesInspector = useCallback(() => {
		if (browserOnly) return;
		setBrowserPopOutState({ sessionId, phase: "docked" });
		setFilesPoppedOut(false);
		setFilesChangedOnly(uiSessionId, true);
		transitionInspectorView("files");
		setInspectorOpenForSession(uiSessionId, true);
	}, [browserOnly, sessionId, uiSessionId, setFilesChangedOnly, setInspectorOpenForSession, transitionInspectorView]);

	const fetchWorkspaceFiles = useCallback(async () => {
		return queryClient.fetchQuery(
			sessionWorkspaceFilesQueryOptions(sessionId, t("files.error.loadWorkspace"), hostId),
		);
	}, [hostId, queryClient, sessionId, t]);

	const revealResolvedWorkspaceFile = useCallback(
		async (rawPath: string) => {
			const data = await fetchWorkspaceFiles();
			const path = matchWorkspaceFilePath(rawPath, data.files ?? []);
			if (browserOnly) {
				openCenterFile(path);
				return;
			}
			setFilePreviewRequestsBySession((current) => ({
				...current,
				[uiSessionId]: { path, key: (current[uiSessionId]?.key ?? 0) + 1 },
			}));
		},
		[browserOnly, openCenterFile, fetchWorkspaceFiles, uiSessionId],
	);

	// A reveal is one-shot. Left in place, it would reopen the file (and take
	// focus from the agent tab) every time the explorer re-ran it, which happens
	// on each return to this session.
	const handleRevealHandled = useCallback((key: number) => {
		setFilePreviewRequestsBySession((current) => {
			if (current[uiSessionId]?.key !== key) return current;
			const { [uiSessionId]: _handled, ...rest } = current;
			return rest;
		});
	}, [uiSessionId]);

	const handleOpenFiles = useCallback(() => {
		markFileViewerPerformance("files-click");
		prepareFilesInspector();
		void fetchWorkspaceFiles();
	}, [fetchWorkspaceFiles, prepareFilesInspector]);

	const handleOpenReviewFile = useCallback(
		(target: { line?: number; path: string }) => {
			prepareFilesInspector();
			void revealResolvedWorkspaceFile(target.path);
		},
		[prepareFilesInspector, revealResolvedWorkspaceFile],
	);

	const handleOpenFile = useCallback(
		(path: string) => {
			prepareFilesInspector();
			void revealResolvedWorkspaceFile(path);
		},
		[prepareFilesInspector, revealResolvedWorkspaceFile],
	);

	const handleToggleFilesPopOut = useCallback(
		(next: boolean) => {
			if (next) setBrowserPopOutState({ sessionId, phase: "docked" });
			setFilesPoppedOut(next);
			transitionInspectorView("files");
			setInspectorOpenForSession(uiSessionId, true);
		},
		[sessionId, uiSessionId, setInspectorOpenForSession, transitionInspectorView],
	);

	const handleToggleBrowserPopOut = useCallback(
		(next: boolean) => {
			if (next) setFilesPoppedOut(false);
			setBrowserPopOutState((current) => {
				if (next) {
					if (current.sessionId === sessionId && current.phase !== "docked") return current;
					return { sessionId, phase: "mounting" };
				}
				if (current.sessionId !== sessionId || current.phase === "docked") return current;
				return { sessionId, phase: "docked" };
			});
		},
		[sessionId],
	);

	useEffect(() => {
		if (!hasInspector) return;
		const current = useUiStore.getState().inspectorSessions[uiSessionId];
		if (browserOnly) {
			if (terminated && current?.browserUnseen) setBrowserUnseen(uiSessionId, false);
			return;
		}
		if (!hasBrowserContent) {
			if (current?.browserContentRevealed) setBrowserContentRevealed(uiSessionId, false);
			else if (current?.browserUnseen) setBrowserUnseen(uiSessionId, false);
			return;
		}
		if (current?.browserContentRevealed) return;
		setBrowserContentRevealed(uiSessionId, true);
	}, [
		hasBrowserContent,
		browserOnly,
		hasInspector,
		previewRevision,
		uiSessionId,
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
		if (browserOnly && !terminated && !useUiStore.getState().inspectorSessions[uiSessionId]?.browserContentRevealed) {
			setInspectorOpenForSession(uiSessionId, true);
		}
		setBrowserContentRevealed(uiSessionId, true);
		if (browserIsVisible(uiSessionId, browserPoppedOut)) {
			setBrowserUnseen(uiSessionId, false);
			return;
		}
		// Workers and already-revealed orchestrators badge new browser work.
		// A new preview target used to force-switch the inspector to the Browser
		// tab and pop it open, even if the user was looking at something else
		// entirely (Reviews, a different session's Files tab, mid-typing in
		// chat). Match the agent-activity effect below: badge it as unseen and
		// let the user open Browser themselves when they're ready, instead of
		// grabbing focus out from under them.
		setBrowserUnseen(uiSessionId, true);
	}, [
		browserPoppedOut,
		hasInspector,
		previewRevision,
		previewUrl,
		sessionId,
		uiSessionId,
		setBrowserContentRevealed,
		setBrowserUnseen,
		browserOnly,
		terminated,
		setInspectorOpenForSession,
	]);

	// Agent browser commands are genuine browser activity even when they do not
	// navigate (fill, click, snapshot, etc.) or land on an empty target — e.g. a
	// command that runs before any page has loaded. When Browser is hidden,
	// surface that activity as unseen rather than reopening the tab; gating this
	// on hasBrowserContent/browserContentRevealed missed exactly that case.
	useEffect(() => {
		if (!hasInspector || terminated || !browserView.agentBrowserActive) return;
		if (browserOnly && !useUiStore.getState().inspectorSessions[uiSessionId]?.browserContentRevealed) {
			setBrowserContentRevealed(uiSessionId, true);
			setInspectorOpenForSession(uiSessionId, true);
			return;
		}
		if (!browserIsVisible(uiSessionId, browserPoppedOut)) setBrowserUnseen(uiSessionId, true);
	}, [
		browserPoppedOut,
		browserView.agentBrowserActive,
		hasInspector,
		inspectorView,
		isInspectorOpen,
		uiSessionId,
		setBrowserUnseen,
		terminated,
		browserOnly,
		setBrowserContentRevealed,
		setInspectorOpenForSession,
	]);

	// Opening Browser consumes the pending activity indicator, including the
	// case where the inspector was collapsed while already parked on Browser.
	useEffect(() => {
		if (hasInspector && browserIsVisible(uiSessionId, browserPoppedOut)) {
			setBrowserUnseen(uiSessionId, false);
		}
	}, [browserPoppedOut, hasInspector, inspectorView, isInspectorOpen, uiSessionId, setBrowserUnseen]);

	const handleToggleInspector = useCallback(() => {
		if (browserOnly) setBrowserContentRevealed(uiSessionId, true);
		toggleInspector(uiSessionId);
	}, [browserOnly, uiSessionId, toggleInspector, setBrowserContentRevealed]);

	useEffect(() => {
		if (!hasInspector) return;
		const handleKeyDown = (event: KeyboardEvent) => {
			if (!matchesRendererShortcut("toggle-inspector", event)) return;
			event.preventDefault();
			handleToggleInspector();
		};
		window.addEventListener("keydown", handleKeyDown);
		return () => window.removeEventListener("keydown", handleKeyDown);
	}, [handleToggleInspector, hasInspector]);

	const inspectorMotionReadyRef = useRef<string | null>(null);
	const handleInspectorCloseAnimationComplete = useCallback(() => {
		setInspectorSettledClosed(true);
	}, []);
	useLayoutEffect(() => {
		if (!hasInspector) {
			setInspectorSettledClosed(true);
			stopTerminalLiveResize();
			return;
		}
		if (inspectorMotionReadyRef.current !== uiSessionId) {
			setInspectorSettledClosed(!isInspectorOpen);
			stopTerminalLiveResize();
			if (workspaceResizeTimerRef.current !== null) window.clearTimeout(workspaceResizeTimerRef.current);
			workspaceResizeTimerRef.current = null;
			sessionSplitRef.current?.removeAttribute("data-workspace-resizing");
			browserEntryWidthFloorRef.current = null;
		}
	}, [hasInspector, isInspectorOpen, uiSessionId, stopTerminalLiveResize]);
	useEffect(() => {
		if (!hasInspector || inspectorMotionReadyRef.current !== uiSessionId) return;
		if (isInspectorOpen) {
			setInspectorSettledClosed(false);
			const groupWidth = sessionSplitRef.current?.clientWidth || window.innerWidth;
			const availableWidth = Math.max(0, groupWidth - INSPECTOR_SEPARATOR_RESERVE_PX);
			const targetInspectorWidth = Number.parseFloat(initialInspectorSize(sizing, availableWidth));
			startTerminalLiveResize(
				targetInspectorWidth <= INSPECTOR_COMPACT_MAX_PX ? "compact" : "expanded",
				topbarSecondaryLabelMode(Math.max(0, availableWidth - targetInspectorWidth)),
			);
			return;
		}
		const groupWidth = sessionSplitRef.current?.clientWidth || window.innerWidth;
		startTerminalLiveResize("expanded", topbarSecondaryLabelMode(groupWidth));
	}, [hasInspector, isInspectorOpen, uiSessionId, sizing, startTerminalLiveResize]);
	useEffect(() => {
		if (!hasInspector) {
			inspectorMotionReadyRef.current = null;
			return;
		}
		inspectorMotionReadyRef.current = uiSessionId;
		return () => {
			inspectorMotionReadyRef.current = null;
		};
	}, [hasInspector, uiSessionId]);
	// A Cloud tab may arrive before the paginated workspace cache contains its
	// row. Keep the session surface (and its switch control) mounted while the
	// direct control-plane lookup is in flight; only show "not found" after
	// both sources have settled.
	const cloudSessionResolving = cloudLookupEnabled && cloudRouteSession.isLoading;
	if (!session && (hostId ? !remoteSessionQuery.isLoading : !workspaceQuery.isLoading && !cloudSessionResolving)) {
		const remoteCode = hostId && remoteSessionQuery.error ? apiErrorCode(remoteSessionQuery.error) : undefined;
		const remoteError = hostId ? t(remoteCode === "BAD_PASSWORD" ? "remote.hostUnauthorized"
			: remoteCode === "HOST_API_INCOMPATIBLE" ? "remote.hostIncompatible"
			: !remoteBase || remoteCode === "UPSTREAM_UNAVAILABLE" || remoteCode === "HOST_IDENTITY_UNVERIFIED"
				? "remote.hostOffline" : remoteSessionQuery.isError ? "remote.loadSessionFailed" : "session.notFound") : undefined;
		return (
			<div className="grid h-full place-items-center p-6 text-center font-mono text-xs text-passive" role={remoteError ? "alert" : undefined}>
				{remoteError ?? t("session.notFound")}
			</div>
		);
	}

	return (
		<div className="relative flex h-full min-h-0 flex-col bg-background text-foreground" data-testid="session-detail">
			<div
				className="session-split relative flex min-h-0 flex-1 overflow-hidden"
				data-testid="panel-group"
				data-workspace-mode={sizing.mode}
				id="session-workspace"
				ref={sessionSplitRef}
				style={
					{
						"--session-inspector-max-width": inspectorMaxWidthCss(
							sizing.maxPercent,
							sizing.chatMinWidth,
						),
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
						<div className="relative min-h-0 flex-1" ref={bindHandoffDialogContainer}>
							{cloudStage === "paused_by_coder" ? <CloudPausedStatus /> : null}
							{session && !session.cloud && handoffDialogContainer ? (
								<SwitchAgentDialog
									agentSwitch={handoffAgentSwitch}
									container={handoffDialogContainer}
									onOpenChange={handleHandoffDialogOpenChange}
									open={handoffDialogOpen}
									session={session}
								/>
							) : null}
							{/* The committed mode owns the agent surface. Auxiliary shell and
							    reviewer targets remain terminal surfaces in either mode. */}
							<div
								className={cn("h-full min-h-0", fileTabs.activePath && "invisible pointer-events-none")}
								inert={fileTabs.activePath ? true : undefined}
							>
							{showChatSurface && session?.cloud ? (
								<CloudSessionChatSurface
									controllerTransitioning={interfaceUi.controllerTransitioning}
									headerActions={sessionHeaderActions}
									newWorkDisabled={interfaceUi.newWorkDisabled}
									onConversationWorkChange={interfaceUi.onConversationWorkChange}
									session={session}
									sessionTabAction={sessionTabActions}
								/>
							) : showChatSurface ? (
								<>
								<SessionChatSurface
									key={uiSessionId}
									assetBaseUrl={remoteBase}
									hostId={hostId}
									session={session}
									reviewerTerminal={reviewerTerminal}
									reviewerChat={reviewerChat}
									reviewerChatSelected={Boolean(reviewerChatId)}
									onOpenReviewerTerminal={selectReviewerTerminal}
									onOpenReviewerChat={(target) => selectReviewerChat(target.reviewId)}
									onSessionRenamed={refreshWorkspaces}
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
									daemonReady={hostId ? Boolean(remoteBase) : daemonStatus.state === "ready"}
									theme={theme}
									headerActions={sessionHeaderActions}
									sessionTabAction={sessionTabActions}
									sessionTabActionWide={sessionTabActionWide}
									tabStripAction={newShellTerminalAction}
									handoffDialogOpen={handoffDialogOpen}
									workspaceTabs={centerFileTabs}
									workspaceActiveTabKey={activeWorkspaceTabKey}
									workspaceFileActive={Boolean(fileTabs.activePath)}
									auxiliaryTabOrder={resolvedAuxiliaryTabOrder}
									onAuxiliaryTabOrderChange={setAuxiliaryTabOrder}
									controllerTransitioning={interfaceUi.controllerTransitioning}
									newWorkDisabled={interfaceUi.newWorkDisabled}
									onConversationWorkChange={interfaceUi.onConversationWorkChange}
									onOpenShell={addShellTerminal}
									openingShell={openShellTerminal.isPending}
									shellError={
										openShellTerminal.error ? apiErrorMessage(openShellTerminal.error) : undefined
									}
									onOpenFiles={browserOnly ? undefined : handleOpenFiles}
									onOpenFile={handleOpenFile}
									onOpenLinkInBrowser={browserView.openLink}
								/>
								{reviewerChatId ? (
									<div className="absolute inset-0">
										<ReviewerChatSurface hideHeader hostId={hostId} reviewId={reviewerChatId} />
									</div>
								) : null}
								</>
							) : (
								<CenterPane
									hostId={hostId}
									agentInputDisabled={interfaceUi.agentInputDisabled}
									daemonReady={hostId ? Boolean(remoteBase) : daemonStatus.state === "ready"}
									onCloseShellTerminal={closeShellTerminalByHandle}
									onRenameShellTerminal={renameShellTerminalByHandle}
									onSelectSessionTerminal={selectSessionTerminal}
									onSessionTerminalAttached={onSessionTerminalAttached}
									onSelectReviewerTerminal={selectReviewerTerminal}
									onSelectReviewerChat={(target) => selectReviewerChat(target.reviewId)}
									onSelectShellTerminal={selectShellTerminal}
									reviewerTerminal={reviewerTerminal}
									reviewerChat={reviewerChat}
									reviewerChatSelected={Boolean(reviewerChatId)}
									reviewerChatContent={reviewerChatId ? <ReviewerChatSurface hideHeader hostId={hostId} reviewId={reviewerChatId} /> : undefined}
									session={session}
									shellTerminals={shellTerminals}
									terminalTarget={routedTerminalTarget}
									theme={theme}
									topbarActions={sessionHeaderActions}
									sessionTabAction={sessionTabActions}
									sessionTabActionWide={sessionTabActionWide}
									tabStripAction={newShellTerminalAction}
									handoffDialogOpen={handoffDialogOpen}
									workspaceTabs={centerFileTabs}
									workspaceActiveTabKey={activeWorkspaceTabKey}
									workspaceFileActive={Boolean(fileTabs.activePath)}
									auxiliaryTabOrder={resolvedAuxiliaryTabOrder}
									onAuxiliaryTabOrderChange={setAuxiliaryTabOrder}
								/>
							)}
							</div>
							{fileTabs.activePath ? (
								<div className="absolute inset-0">
					{session?.cloud ? (
						<CloudFileContentPane
							annotation={fileAnnotation}
							commitSha={activeCenterFileRequest?.commitSha}
							initialEditing={activeCenterFileInitialEditing}
							initialMode={activeCenterFileRequest?.mode ?? "file"}
							initialRequestKey={activeCenterFileRequest?.key ?? 0}
							onDirtyChange={setCenterFileDirty}
							path={fileTabs.activePath}
							scope={activeCenterFileRequest?.scope}
							session={session}
							split={filesSplit}
						/>
									) : (
										<SessionFileWorkspace
											hostId={hostId}
											annotation={fileAnnotation}
											commitSha={activeCenterFileRequest?.commitSha}
											initialEditing={activeCenterFileInitialEditing}
											initialMode={activeCenterFileRequest?.mode ?? "file"}
											initialRequestKey={activeCenterFileRequest?.key ?? 0}
											onDirtyChange={setCenterFileDirty}
											onInitialEditingConsumed={markCenterFileEditingConsumed}
											path={fileTabs.activePath}
											sessionId={sessionId}
											split={filesSplit}
											scope={activeCenterFileRequest?.scope}
										/>
									)}
								</div>
							) : null}
							{interfaceUi.notice}
						</div>
					</div>
				</div>
				{hasInspector ? (
					<SessionInspectorRail
						sessionKey={uiSessionId}
						showCollapsedHandle={!browserOnly}
						isOpen={isInspectorOpen}
						onCloseAnimationComplete={handleInspectorCloseAnimationComplete}
						onExpand={() => setInspectorOpenForSession(uiSessionId, true)}
						restoreMinWidth={
							sizing.mode === "browser" ? (browserEntryWidthFloorRef.current ?? undefined) : undefined
						}
						sizing={sizing}
						settledClosed={!isInspectorOpen && inspectorSettledClosed}
						splitRef={sessionSplitRef}
					>
						<SessionInspector
							hostId={hostId}
							browserOnly={browserOnly}
							browserAnnotationQueue={inspectorView === "browser" ? browserAnnotationQueue : undefined}
							browserPoppedOut={browserPoppedOut}
							filesView={
								inspectorView === "files" && session ? (
									session.cloud ? (
										<CloudWorkspaceDiff annotation={fileAnnotation} onOpenFile={openCenterFile} onSplitChange={setFilesSplit} onToggleMaximized={handleToggleFilesPopOut} session={session} split={filesSplit} />
									) : (
										<SessionFileExplorer
											hostId={hostId}
											onOpenFile={openCenterFile}
											onSplitChange={setFilesSplit}
											onToggleMaximized={handleToggleFilesPopOut}
											onRevealHandled={handleRevealHandled}
											revealRequest={filePreviewRequestsBySession[uiSessionId] ?? null}
											sessionId={session.id}
											split={filesSplit}
										/>
									)
								) : null
							}
							isInspectorVisible={inspectorPanelVisible}
							onOpenFiles={browserOnly ? undefined : handleOpenFiles}
							onOpenReviewFile={handleOpenReviewFile}
								onOpenReviewerTerminal={selectReviewerTerminal}
								onOpenReviewerChat={selectReviewerChat}
								onWorkerMessageSent={showChatSurface || reviewerChatId ? selectSessionTerminal : undefined}
							onToggleBrowserPopOut={handleToggleBrowserPopOut}
							onViewChange={transitionInspectorView}
							view={inspectorView}
							browserView={hostId || inspectorView === "browser" ? browserView : undefined}
							session={session}
						/>
					</SessionInspectorRail>
				) : null}
			</div>
			{hasInspector ? (
				<div className="session-pinned-actions" data-testid="session-pinned-actions" style={noDragStyle}>
					<Tooltip>
						<TooltipTrigger asChild>
							<TopbarButton
								aria-label={
									browserOnly
										? `${isInspectorOpen ? t("common.close") : t("inspector.open")} ${t("inspector.browser")}`
										: isInspectorOpen ? t("shell.closeInspector") : t("shell.openInspector")
								}
								aria-pressed={isInspectorOpen}
								onClick={handleToggleInspector}
								style={noDragStyle}
								variant="icon"
							>
								{browserOnly ? (
									<span className="relative inline-flex">
										<Globe2 aria-hidden="true" className="size-icon-md" />
										{!isInspectorOpen && browserUnseen ? (
											<span
												aria-hidden="true"
												className="pointer-events-none absolute -right-0.5 -top-0.5 size-1.5 rounded-full bg-primary ring-2 ring-background"
												data-testid="orchestrator-browser-unseen-indicator"
											/>
										) : null}
									</span>
								) : (
									<PanelRight className="size-icon-md" aria-hidden="true" />
								)}
							</TopbarButton>
						</TooltipTrigger>
						<TooltipContent side="bottom">
							{browserOnly
								? `${isInspectorOpen ? t("common.close") : t("inspector.open")} ${t("inspector.browser")}`
								: isInspectorOpen ? t("shell.closeInspectorTitle") : t("shell.openInspectorTitle")}
						</TooltipContent>
					</Tooltip>
					{/* Keep the global notification action trailing at the window edge. */}
					<NotificationCenter style={noDragStyle} />
				</div>
			) : null}
			{interfaceUi.cloudLoader
				? <CloudInterfaceSwitchLoader target={interfaceUi.target} />
				: showLifecycleLoader || showCompletedLoader
					? <CloudSessionLifecycleLoader
						key={`${sessionId}:${cloudReconnecting && !workspaceRestarting ? "terminal" : "startup"}`}
						sessionId={sessionId}
						orgId={session?.cloud?.orgId ?? ""}
						createdAt={session?.cloud?.observedState === "requested" ? session.createdAt : undefined}
						observedState={session?.cloud?.observedState}
						workerConnected={Boolean(session?.runtimeConnected)}
						terminalOnly={cloudReconnecting && !workspaceRestarting}
						completed={showCompletedLoader}
					/>
					: null}
			{interfaceUi.dialogs}
			{/* Maximized files wear the maximized browser's chrome: a backdrop, the
          filter pinned in the titlebar band where the browser's address bar
          sits, and an inset frame for the explorer. The explorer mounts once
          the band exists so the filter never renders inline first. */}
			{filesPoppedOut && session
				? createPortal(
						<SessionFilesPopOut>{(topbarHost) =>
							<FilesTopbarHostContext.Provider value={topbarHost}>
								{session.cloud ? (
									<CloudWorkspaceDiff annotation={fileAnnotation} isMaximized onOpenFile={openCenterFile} onSplitChange={setFilesSplit} onToggleMaximized={handleToggleFilesPopOut} session={session} split={filesSplit} />
								) : (
									<SessionFileExplorer hostId={hostId} isMaximized onSplitChange={setFilesSplit} onToggleMaximized={handleToggleFilesPopOut} sessionId={session.id} split={filesSplit} />
								)}
							</FilesTopbarHostContext.Provider>
						}</SessionFilesPopOut>,
						document.body,
					)
				: null}
			{/* Maximized browser: a fixed overlay across the app workspace,
          portaled to <body> so it escapes the shell layout (covering the
          sidebar + topbar, not just the session area) and sits outside any
          `[data-panel]` column, so the native WebContentsView is not clamped
          and fills the window below any native titlebar overlay. */}
			{browserPopOutMounted && session
				? createPortal(
						<SessionBrowserPopOut onTopbarHost={setBrowserPopoutTopbarHost} phase={browserPopOutPhase === "open" ? "open" : "mounting"}>
								{browserPoppedOut && browserPopoutTopbarHost ? <BrowserPanelView
									active
									annotationQueue={browserAnnotationQueue}
									browserView={browserView}
									onTogglePopOut={handleToggleBrowserPopOut}
									poppedOut
									session={session}
									topbarHost={browserPopoutTopbarHost}
								/> : null}
						</SessionBrowserPopOut>,
						document.body,
					)
				: null}
		</div>
	);
}
