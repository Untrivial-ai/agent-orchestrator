/**
 * The Chat surface for a session whose persisted mode is `chat`.
 *
 * Rendered from a durable snapshot the daemon serves. The renderer stays thin:
 * items arrive already ordered by sequence and are never re-sorted here, turn
 * state comes from the daemon rather than being inferred from the last message,
 * and a decision is a typed action carrying the provider's request id.
 *
 * State this surface must be able to show, because each one happens: empty,
 * streaming, waiting on an approval, a command still running with no completion,
 * a turn the user stopped, a controller that died mid-turn, and history the user
 * has scrolled away from.
 */

import {
	memo,
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
	type CSSProperties,
	type KeyboardEvent as ReactKeyboardEvent,
	type MouseEvent as ReactMouseEvent,
	type PointerEvent as ReactPointerEvent,
	type ReactNode,
	type WheelEvent as ReactWheelEvent,
} from "react";
import { ArrowDown, Loader2, TriangleAlert, Undo2 } from "lucide-react";
import { Reorder, useDragControls } from "motion/react";
import { useTranslation } from "react-i18next";
import { cn } from "../../lib/utils";
import { sameContent, useStableList } from "../../lib/stable-list";
import { useTabScrollEdges } from "../../hooks/useTabScrollEdges";
import { getApiBaseUrl, subscribeApiBaseUrl } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import { isDialogOrMenuOpen } from "../../lib/dom-selectors";
import {
	TERMINAL_FONT_SIZE_DEFAULT,
	TERMINAL_FONT_SIZE_MAX,
	TERMINAL_FONT_SIZE_MIN,
} from "../../lib/design-tokens";
import { isLinuxPlatform, isMacPlatform } from "../../lib/platform";
import { handleTerminalTabListKeyDown } from "../../lib/terminal-tabs";
import { agentLabel } from "../../lib/agent-options";
import type { ShellTerminal } from "../../hooks/useShellTerminals";
import { sidebarOccupiesLayout, useUiStore } from "../../stores/ui-store";
import type { TerminalTarget } from "../../types/terminal";
import {
	isOrchestratorSession,
	type SessionKind,
	type WorkspaceSession,
} from "../../types/workspace";
import { AgentAvatar } from "../AgentAvatar";
import { SessionPaneTab } from "../CenterPane";
import { Button } from "../ui/button";
import { ConfirmDialog } from "../ConfirmDialog";
import { SessionTopbarPortal } from "../SessionTopbarPortal";
import { ShellTerminalTab } from "../ShellTerminalTab";
import { TerminalPane } from "../TerminalPane";
import {
	ActivityRow,
	ApprovalCard,
	AssistantMessage,
	CompactionMarker,
	HumanMessage,
	OriginMessage,
	SteerMessage,
	TurnChangedFiles,
	TurnDuration,
	TurnOutcome,
	type TurnOutcomeRetryControl,
} from "./ChatTimelineItems";
import { HumanMessageEditor } from "./HumanMessageEditor";
import { ChatLinkProvider } from "./ChatMarkdown";
import { ChatComposer } from "./ChatComposer";
import { QueuedMessageDock, type QueuedMessage } from "./QueuedMessageDock";
import { ActivityRun } from "./ActivityRun";
import { TurnPlan } from "./TurnPlan";
import { TurnSettingsBar } from "./TurnSettingsBar";
import { ElicitationCard } from "./ElicitationCard";
import { McpServerBanner, ReauthBanner, ThreadStateBanner } from "./ChatStatusBanners";
import {
	activeTurn,
	activityPlan,
	brokenMcpServers,
	can,
	isCompaction,
	isSteer,
	hiddenTimelineTurnIds,
	queuedTurnIds,
	type ConversationPlan,
	type ConversationSnapshot,
	type ControllerState,
	type ChatConfigOption,
	type ChatConfigOptionValue,
	type ChatModel,
	type ChatSkill,
	type ConversationActivity,
	type ConversationBranchPoint,
	type ConversationContentSummary,
	type ConversationItem,
	type ConversationMessage,
	type TurnDiff,
	type TurnSettings,
} from "../../types/conversation";

const CHAT_FONT_SIZE_DEFAULT = 14;

// Reviewer panes share the terminal font-size preference with CenterPane, so a
// reviewer opened inside the Chat surface matches a reviewer opened in TUI mode.
const terminalFontSizeStorageKey = "ao.terminal.fontSize";
const WHEEL_ZOOM_THRESHOLD = 80;
const WHEEL_ZOOM_RESET_MS = 250;

export interface ChatRetryControl {
	retry: (turnId: string) => void | Promise<unknown>;
	pending?: boolean;
	error?: string;
	turnId?: string;
}

function clampTerminalFontSize(size: number): number {
	return Math.min(TERMINAL_FONT_SIZE_MAX, Math.max(TERMINAL_FONT_SIZE_MIN, size));
}

function initialTerminalFontSize(): number {
	if (typeof window === "undefined") return TERMINAL_FONT_SIZE_DEFAULT;
	const raw = window.localStorage?.getItem(terminalFontSizeStorageKey);
	const parsed = raw === null ? Number.NaN : Number(raw);
	if (!Number.isFinite(parsed)) return TERMINAL_FONT_SIZE_DEFAULT;
	return clampTerminalFontSize(parsed);
}

type ReviewerTerminalTarget = Extract<TerminalTarget, { kind: "reviewer" }>;
type ShellTerminalTarget = Extract<TerminalTarget, { kind: "shell" }>;
type WorkspaceTab = { key: string; content: ReactNode; onSelect: () => void };
type ChatAuxiliaryTab =
	| { key: string; kind: "reviewer"; terminal: { handleId: string; harness: string } }
	| { key: string; kind: "shell"; terminal: ShellTerminal }
	| { key: string; kind: "workspace"; tab: WorkspaceTab };

function DraggableChatTab({ children, value }: { children: ReactNode; value: string }) {
	const dragControls = useDragControls();
	return (
		<Reorder.Item
			as="div"
			className="flex shrink-0 self-stretch touch-pan-y"
			data-terminal-tab-key={value}
			drag="x"
			dragControls={dragControls}
			dragListener={false}
			onPointerDown={(event: ReactPointerEvent<HTMLDivElement>) => {
				if ((event.target as HTMLElement).closest("[data-terminal-tab-action],input,a")) return;
				dragControls.start(event);
			}}
			value={value}
		>
			{children}
		</Reorder.Item>
	);
}

const isMac = isMacPlatform();
const isLinux = isLinuxPlatform();

type TopbarBounds = {
	leftInset: number;
	rightInset: number;
	width: number;
};

type MessageEditDraft = {
	turnId: string;
	text: string;
	content: ConversationContentSummary[];
	reconstructedContext: boolean;
};

/**
 * A stream refresh replaces the complete snapshot object. Queue prompts do not
 * change while a running turn emits output, so retain the previous list when its
 * contents are equal and avoid redrawing the dock and composer subtree.
 */
function useQueuedMessages(snapshot: ConversationSnapshot): QueuedMessage[] {
	const previous = useRef<QueuedMessage[]>([]);
	return useMemo(() => {
		const messagesByTurn = new Map(
			snapshot.items
				.filter(
					(item): item is ConversationMessage =>
						item.kind === "message" &&
						item.role === "user" &&
						item.origin === "human" &&
						Boolean(item.turnId),
				)
				.map((message) => [message.turnId as string, message]),
		);
		const next = snapshot.turns.flatMap((queuedTurn) => {
			if (queuedTurn.state !== "queued") return [];
			const message = messagesByTurn.get(queuedTurn.id);
			return message ? [{ turnId: queuedTurn.id, message }] : [];
		});
		const current = previous.current;
		if (
			current.length === next.length &&
			current.every(
				(entry, index) =>
					entry.turnId === next[index]?.turnId && sameContent(entry.message, next[index]?.message),
			)
		) {
			return current;
		}
		previous.current = next;
		return next;
	}, [snapshot.items, snapshot.turns]);
}

export interface ChatWorkspaceProps {
	snapshot: ConversationSnapshot;
	/** The session title from the sidebar (matches what users see in the left sidebar) */
	sessionTitle?: string;
	/** The AO role using this shared conversation surface. */
	sessionRole?: SessionKind;
	/** Session-level actions owned above the conversation surface. */
	headerActions?: ReactNode;
	/** Agent-session actions on the primary chat tab (interface switch, handoff). */
	sessionTabAction?: ReactNode;
	/** Pinned beside the tab strip, before the workspace topbar actions. */
	tabStripAction?: ReactNode;
	/** File tabs coordinated by SessionView, appended to the native chat tab strip. */
	workspaceTabs?: WorkspaceTab[];
	workspaceTabActions?: ReactNode;
	workspaceActiveTabKey?: string;
	/** Session-owned order shared with the terminal UI surface. */
	auxiliaryTabOrder?: string[];
	onAuxiliaryTabOrderChange?: (keys: string[]) => void;
	/** Suppress a transient stopped snapshot while a mode handoff installs Chat. */
	controllerTransitioning?: boolean;
	/** Freeze agent-owned Chat controls while a durable session mutation owns input. */
	agentInputDisabled?: boolean;
	/** Fence new agent work without blocking decisions required by the current turn. */
	newWorkDisabled?: boolean;
	reviewerTerminal?: { handleId: string; harness: string };
	onOpenReviewerTerminal?: (target: { handleId: string; harness: string }) => void;
	/** Older durable history is available but not loaded into the DOM yet. */
	hasOlder?: boolean;
	loadingOlder?: boolean;
	onLoadOlder?: () => void;
	onSend?: (
		text: string,
		attachments?: { mimeType: string; data: string }[],
	) => void | Promise<unknown>;
	onDecide?: (requestId: string, decisionId: string) => void;
	onResolveInput?: (
		requestId: string,
		action: "accept" | "decline" | "cancel",
		content?: Record<string, unknown>,
	) => Promise<unknown> | void;
	onInterrupt?: () => void;
	commandError?: string;
	onResumeAgent?: () => void;
	resumingAgent?: boolean;
	resumeError?: string;
	onOpenShell?: () => void;
	openingShell?: boolean;
	shellError?: string;
	/** Open an HTTP(S) link in this session's AO Browser panel. */
	onLinkOpen?: (url: string) => void;
	/** A send or decision is in flight. */
	busy?: boolean;
	/** The provider's model catalog. Empty hides the model control. */
	models?: ChatModel[];
	/** The AO session this surface renders for. Used to attach the reviewer pane. */
	session?: WorkspaceSession;
	/** Refresh the owning workspace cache after the shared primary tab is renamed. */
	onSessionRenamed?: () => void | Promise<void>;
	/** The selected reviewer pane. Kept even while its tab is temporarily unavailable. */
	reviewerTarget?: ReviewerTerminalTarget;
	/** Switch the active tab back to the chat timeline. */
	onSelectChat?: () => void;
	/** This session's standalone shells, rendered as tabs after the reviewer. */
	shellTerminals?: ShellTerminal[];
	/** The selected shell pane, if any. Mirrors reviewerTarget. */
	shellTarget?: ShellTerminalTarget;
	onSelectShellTerminal?: (handleId: string) => void;
	onCloseShellTerminal?: (handleId: string) => void;
	onRenameShellTerminal?: (handleId: string, title: string) => void;
	/** Daemon readiness for the reviewer terminal pane. */
	daemonReady?: boolean;
	/** Resolved color theme for the reviewer terminal pane. */
	theme?: "light" | "dark";
	onChooseSettings?: (settings: TurnSettings) => void;
	/** Live provider-owned options, such as ACP model, effort, mode, and fast mode. */
	configOptions?: ChatConfigOption[];
	onChooseConfigOption?: (
		optionId: string,
		value: ChatConfigOptionValue,
	) => Promise<unknown> | void;
	configOptionPending?: boolean;
	configOptionError?: string;
	/** Summarize earlier history to reclaim context. */
	onCompact?: () => void;
	/** A compaction is running provider-side. It takes seconds, not milliseconds. */
	compacting?: boolean;
	/** Why compaction is not available right now, from the daemon's typed refusal. */
	compactUnavailable?: string;
	/**
	 * Discard a turn and everything after it. Absent means the agent cannot undo,
	 * and the affordance is not drawn at all rather than shown and then refused.
	 */
	onRollback?: (turnId: string) => void | Promise<unknown>;
	rollbackPending?: boolean;
	rollbackError?: string;
	/** Opens the session Files inspector from a turn's changed-files Review control. */
	onOpenFiles?: () => void;
	/** Opens the Files inspector focused on one changed path. */
	onOpenFile?: (path: string) => void;
	/**
	 * Re-dispatch a failed turn's durable prompt as a new turn. Offered only for
	 * eligible failed human turns, so the affordance is drawn on the failed-turn
	 * boundary and never for a turn that is running or already succeeded.
	 */
	retryControl?: ChatRetryControl;
	/** Create a conversation branch by replacing a prior human prompt. */
	onEditMessage?: (turnId: string, text: string) => void | Promise<unknown>;
	editMessagePending?: boolean;
	editMessageError?: string;
	/** Switch the visible conversation to another branch. */
	onActivateBranch?: (branchId: string) => void | Promise<unknown>;
	activateBranchPending?: boolean;
	activateBranchError?: string;
	/** The provider's skills. Empty leaves `/` an ordinary character. */
	skills?: ChatSkill[];
	/** Worktree paths offered for `@`. */
	filePaths?: string[];
	/** The path list was capped by the daemon rather than being all of them. */
	filePathsTruncated?: boolean;
	/**
	 * Writes staged images into the worktree and answers with the paths the agent
	 * can open. Absent means no attach control is offered — the fixture preview has
	 * no worktree to write into.
	 */
	onStageAttachments?: (attachments: { mimeType: string; data: string }[]) => Promise<string[]>;
	/** The provider negotiated native image prompt blocks. */
	nativeImages?: boolean;
	/**
	 * Deliver guidance into the turn already running, instead of queueing a message
	 * behind it. Absent means this harness cannot steer and no control is drawn —
	 * Claude answers `CHAT_STEER_UNSUPPORTED`, and an affordance that only ever fails
	 * is worse than none.
	 */
	onSteer?: (
		text: string,
		attachments?: { mimeType: string; data: string }[],
	) => Promise<unknown>;
	sendPending?: boolean;
	steerPending?: boolean;
	/** Why the last steer was refused, from the daemon's typed answer. */
	steerRefusal?: string;
	onPromoteQueuedTurn?: (turnId: string) => Promise<unknown>;
	onEditQueuedTurn?: (turnId: string, text: string) => Promise<unknown>;
	onCancelQueuedTurn?: (turnId: string) => Promise<unknown>;
	onReorderQueuedTurns?: (turnIds: string[]) => Promise<unknown>;
	promoteQueuedTurnPendingTurnId?: string;
	cancelQueuedTurnPendingTurnId?: string;
	editQueuedTurnPendingTurnId?: string;
	/** Start the tool servers again. Absent when the harness cannot. */
	onReloadMcpServers?: () => void;
	reloadingMcpServers?: boolean;
	mcpReloadError?: string;
}

export function ChatWorkspace({
	snapshot,
	sessionTitle,
	sessionRole = "worker",
	headerActions,
	sessionTabAction,
	tabStripAction,
	workspaceTabs,
	workspaceTabActions,
	workspaceActiveTabKey,
	auxiliaryTabOrder,
	onAuxiliaryTabOrderChange,
	controllerTransitioning,
	agentInputDisabled = false,
	newWorkDisabled = false,
	reviewerTerminal,
	onOpenReviewerTerminal,
	session,
	onSessionRenamed,
	reviewerTarget,
	onSelectChat,
	shellTerminals,
	shellTarget,
	onSelectShellTerminal,
	onCloseShellTerminal,
	onRenameShellTerminal,
	daemonReady,
	theme,
	hasOlder,
	loadingOlder,
	onLoadOlder,
	onSend,
	onDecide,
	onResolveInput,
	onInterrupt,
	commandError,
	onResumeAgent,
	resumingAgent,
	resumeError,
	onOpenShell,
	openingShell,
	shellError,
	onLinkOpen,
	busy,
	models,
	onChooseSettings,
	configOptions,
	onChooseConfigOption,
	configOptionPending,
	configOptionError,
	onCompact,
	compacting,
	compactUnavailable,
	onRollback,
	rollbackPending,
	rollbackError,
	onOpenFiles,
	onOpenFile,
	retryControl,
	onEditMessage,
	editMessagePending,
	editMessageError,
	onActivateBranch,
	activateBranchPending,
	activateBranchError,
	skills,
	filePaths,
	filePathsTruncated,
	onStageAttachments,
	nativeImages,
	onSteer,
	sendPending,
	steerPending,
	steerRefusal,
	onPromoteQueuedTurn,
	onEditQueuedTurn,
	onCancelQueuedTurn,
	onReorderQueuedTurns,
	promoteQueuedTurnPendingTurnId,
	cancelQueuedTurnPendingTurnId,
	editQueuedTurnPendingTurnId,
	onReloadMcpServers,
	reloadingMcpServers,
	mcpReloadError,
}: ChatWorkspaceProps) {
	const turn = activeTurn(snapshot);
	const hasPendingInteraction = snapshot.items.some(
		(item) =>
			item.kind === "activity" &&
			(item.activityKind === "approval" || item.activityKind === "user_input") &&
			item.status === "pending" &&
			(!item.turnId || item.turnId === turn?.id),
	);
	const handleChatKeyDown = useCallback(
		(event: ReactKeyboardEvent<HTMLElement>) => {
			if (
				newWorkDisabled ||
				event.key !== "Escape" ||
				event.defaultPrevented ||
				isDialogOrMenuOpen() ||
				event.altKey ||
				event.ctrlKey ||
				event.metaKey ||
				turn?.state !== "running" ||
				hasPendingInteraction ||
				!onInterrupt
			)
				return;
			event.preventDefault();
			onInterrupt();
		},
		[hasPendingInteraction, newWorkDisabled, onInterrupt, turn],
	);
	const handleChatSurfaceClick = useCallback((event: ReactMouseEvent<HTMLElement>) => {
		const target = event.target;
		if (!(target instanceof Element)) return;
		// A click fires after a drag selection ends. Focusing the composer here would
		// collapse the range the user just selected in the transcript.
		if (window.getSelection()?.isCollapsed === false) return;
		if (
			target.closest(
				"button, a, input, textarea, select, [contenteditable='true'], [role='button'], [role='option'], [role='menuitem'], [role='dialog'], [data-testid='session-terminal'], .xterm, .terminal-surface",
			)
		)
			return;

		const composer = surfaceRef.current?.querySelector<HTMLElement>(
			'[aria-label="Message the agent"]',
		);
		if (composer?.getAttribute("aria-disabled") !== "true") composer?.focus();
	}, []);
	// Selection is durable UI state; availability only controls whether the tab is
	// offered. Keeping these separate preserves a selected reviewer while an active
	// session temporarily becomes terminated and later returns.
	const reviewerActive = Boolean(reviewerTarget && session);
	const shellActive = Boolean(shellTarget && session);
	const auxiliaryTabs = useMemo<ChatAuxiliaryTab[]>(
		() => [
			...(reviewerTerminal
				? [{ key: `reviewer:${reviewerTerminal.handleId}`, kind: "reviewer" as const, terminal: reviewerTerminal }]
				: []),
			...(shellTerminals ?? []).map((terminal) => ({ key: terminal.handleId, kind: "shell" as const, terminal })),
			...(workspaceTabs ?? []).map((tab) => ({ key: tab.key, kind: "workspace" as const, tab })),
		],
		[reviewerTerminal, shellTerminals, workspaceTabs],
	);
	const availableTabKeys = useMemo(() => auxiliaryTabs.map((tab) => tab.key), [auxiliaryTabs]);
	const [tabOrderBySession, setTabOrderBySession] = useState<Record<string, string[]>>({});
	const orderedAuxiliaryTabs = useMemo(() => {
		const preferred = auxiliaryTabOrder ?? tabOrderBySession[snapshot.sessionId] ?? [];
		const byKey = new Map(auxiliaryTabs.map((tab) => [tab.key, tab]));
		const ordered = preferred.flatMap((key) => {
			const tab = byKey.get(key);
			if (!tab) return [];
			byKey.delete(key);
			return [tab];
		});
		return [...ordered, ...byKey.values()];
	}, [auxiliaryTabOrder, auxiliaryTabs, snapshot.sessionId, tabOrderBySession]);
	const reorderAuxiliaryTabs = useCallback(
		(nextKeys: string[]) => {
			const available = new Set(availableTabKeys);
			const next = nextKeys.filter((key, index) => available.has(key) && nextKeys.indexOf(key) === index);
			for (const key of availableTabKeys) {
				if (!next.includes(key)) next.push(key);
			}
			if (onAuxiliaryTabOrderChange) onAuxiliaryTabOrderChange(next);
			else setTabOrderBySession((current) => ({ ...current, [snapshot.sessionId]: next }));
		},
		[availableTabKeys, onAuxiliaryTabOrderChange, snapshot.sessionId],
	);
	useEffect(() => {
		if (auxiliaryTabOrder) {
			const available = new Set(availableTabKeys);
			const next = auxiliaryTabOrder.filter((key) => available.has(key));
			for (const key of availableTabKeys) {
				if (!next.includes(key)) next.push(key);
			}
			if (!next.every((key, index) => key === auxiliaryTabOrder[index]) || next.length !== auxiliaryTabOrder.length) {
				onAuxiliaryTabOrderChange?.(next);
			}
			return;
		}
		setTabOrderBySession((current) => {
			const currentOrder = current[snapshot.sessionId] ?? [];
			const available = new Set(availableTabKeys);
			const next = currentOrder.filter((key) => available.has(key));
			for (const key of availableTabKeys) {
				if (!next.includes(key)) next.push(key);
			}
			if (next.length === currentOrder.length && next.every((key, index) => key === currentOrder[index])) return current;
			if (next.length === 0) {
				const { [snapshot.sessionId]: _removed, ...rest } = current;
				return rest;
			}
			return { ...current, [snapshot.sessionId]: next };
		});
	}, [auxiliaryTabOrder, availableTabKeys, onAuxiliaryTabOrderChange, snapshot.sessionId]);
	const queuedMessages = useQueuedMessages(snapshot);
	const stablePromoteQueuedTurn = useStableCallback(onPromoteQueuedTurn);
	const stableCancelQueuedTurn = useStableCallback(onCancelQueuedTurn);
	const [queueEdit, setQueueEdit] = useState<{ turnId: string; text: string } | undefined>();
	useEffect(() => {
		if (!queueEdit) return;
		if (!queuedMessages.some((message) => message.turnId === queueEdit.turnId)) {
			setQueueEdit(undefined);
		}
	}, [queueEdit, queuedMessages]);
	const handleCancelQueuedTurn = useCallback(
		async (turnId: string) => {
			if (!onCancelQueuedTurn) return;
			await stableCancelQueuedTurn(turnId);
			setQueueEdit((current) => (current?.turnId === turnId ? undefined : current));
		},
		[stableCancelQueuedTurn],
	);
	const handleComposerSend = useCallback(
		async (text: string, attachments?: Parameters<NonNullable<typeof onSend>>[1]) => {
			if (queueEdit) {
				if (attachments && attachments.length > 0) {
					throw new Error("Queued message edits cannot include attachments.");
				}
				if (!onEditQueuedTurn) {
					throw new Error("Queued message edits are unavailable right now.");
				}
				await onEditQueuedTurn(queueEdit.turnId, text);
				setQueueEdit(undefined);
				return;
			}
			return onSend?.(text, attachments);
		},
		[onEditQueuedTurn, onSend, queueEdit],
	);
	const composerSend = useStableCallback(handleComposerSend);
	const stableInterrupt = useStableCallback(onInterrupt);
	const stableSteer = useStableCallback(onSteer);
	const beginQueuedEdit = useCallback(
		(turnId: string, text: string) => {
			if (newWorkDisabled) return;
			setQueueEdit({ turnId, text });
		},
		[newWorkDisabled],
	);
	const cancelQueuedEdit = useCallback(() => setQueueEdit(undefined), []);
	const promoteQueuedTurn = useCallback(
		async (turnId: string) => stablePromoteQueuedTurn(turnId),
		[stablePromoteQueuedTurn],
	);
	const steer = useCallback(
		async (
			text: string,
			attachments?: { mimeType: string; data: string }[],
		) => stableSteer(text, attachments),
		[stableSteer],
	);
	// The turn a confirmation is open for. Undo is not reversible and it changes what
	// the agent knows, so it is never one click.
	const [confirming, setConfirming] = useState<string | undefined>(undefined);
	const surfaceRef = useRef<HTMLElement | null>(null);
	const lastWheelZoomAtRef = useRef(0);
	const wheelZoomRemainderRef = useRef(0);
	const [terminalFontSize, setTerminalFontSize] = useState(initialTerminalFontSize);
	const [isFullscreen, setIsFullscreen] = useState(false);
	const [topbarBounds, setTopbarBounds] = useState<TopbarBounds>({
		leftInset: 0,
		rightInset: 0,
		width: 0,
	});

	useEffect(() => {
		const surface = surfaceRef.current;
		if (!surface) return;
		const workspaceSurface = surface.closest<HTMLElement>(".center-panel-surface");
		const measure = () => {
			const surfaceRect = surface.getBoundingClientRect();
			const workspaceRect = workspaceSurface?.getBoundingClientRect() ?? surfaceRect;
			const next = {
				leftInset: workspaceRect.left,
				rightInset: Math.max(0, window.innerWidth - workspaceRect.right),
				width: surfaceRect.width,
			};
			setTopbarBounds((current) =>
				current.leftInset === next.leftInset &&
				current.rightInset === next.rightInset &&
				current.width === next.width
					? current
					: next,
			);
		};
		measure();
		if (typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(measure);
		observer.observe(surface);
		if (workspaceSurface) observer.observe(workspaceSurface);
		return () => observer.disconnect();
	}, []);

	useEffect(() => {
		const handleFullscreenChange = () => {
			setIsFullscreen(document.fullscreenElement === surfaceRef.current);
		};
		document.addEventListener("fullscreenchange", handleFullscreenChange);
		return () => document.removeEventListener("fullscreenchange", handleFullscreenChange);
	}, []);

	const updateTerminalFontSize = useCallback((delta: number) => {
		setTerminalFontSize((current) => {
			const next = clampTerminalFontSize(current + delta);
			window.localStorage?.setItem(terminalFontSizeStorageKey, String(next));
			return next;
		});
	}, []);

	const handleWheelZoom = useCallback(
		(event: ReactWheelEvent<HTMLDivElement>) => {
			if (!event.ctrlKey && !event.metaKey) return;
			event.preventDefault();
			event.stopPropagation();

			if (event.timeStamp - lastWheelZoomAtRef.current > WHEEL_ZOOM_RESET_MS) {
				wheelZoomRemainderRef.current = 0;
			}
			lastWheelZoomAtRef.current = event.timeStamp;
			wheelZoomRemainderRef.current += event.deltaY;

			const steps = Math.floor(Math.abs(wheelZoomRemainderRef.current) / WHEEL_ZOOM_THRESHOLD);
			if (steps === 0) return;

			const direction = wheelZoomRemainderRef.current > 0 ? -1 : 1;
			updateTerminalFontSize(direction * steps);
			wheelZoomRemainderRef.current -=
				Math.sign(wheelZoomRemainderRef.current) * steps * WHEEL_ZOOM_THRESHOLD;
		},
		[updateTerminalFontSize],
	);

	const toggleFullscreen = useCallback(async () => {
		const surface = surfaceRef.current;
		if (!surface) return;
		try {
			if (document.fullscreenElement === surface) {
				await document.exitFullscreen();
				return;
			}
			await surface.requestFullscreen();
		} catch (error) {
			console.warn("Unable to toggle chat reviewer fullscreen", error);
		}
	}, []);

	// Cycle chat → reviewer → shells in strip order, wrapping. With no auxiliary
	// tabs there is nothing to cycle to and the shortcut is a no-op.
	const selectAdjacentTab = useCallback(
		(direction: -1 | 1) => {
			const tabs = [{ key: "chat", kind: "chat" as const }, ...orderedAuxiliaryTabs];
			if (tabs.length <= 1) return;
			const activeKey = workspaceActiveTabKey ?? (shellActive
				? shellTarget?.handleId
				: reviewerActive
					? `reviewer:${reviewerTerminal?.handleId}`
					: "chat");
			const activeIndex = tabs.findIndex((tab) => tab.key === activeKey);
			const currentIndex = activeIndex >= 0 ? activeIndex : 0;
			const next = tabs[(currentIndex + direction + tabs.length) % tabs.length];
			if (!next) return;
			if (next.kind === "chat") {
				onSelectChat?.();
				return;
			}
			if (next.kind === "reviewer") {
				onOpenReviewerTerminal?.(next.terminal);
				return;
			}
			if (next.kind === "shell") {
				onSelectShellTerminal?.(next.terminal.handleId);
				return;
			}
			next.tab.onSelect();
		},
		[
			onOpenReviewerTerminal,
			onSelectChat,
			onSelectShellTerminal,
			reviewerActive,
			reviewerTerminal,
			orderedAuxiliaryTabs,
			shellActive,
			shellTarget,
			workspaceActiveTabKey,
		],
	);
	const handleChatTabsKeyDown = useCallback(
		(event: ReactKeyboardEvent<HTMLDivElement>) => {
			if (
				event.target instanceof HTMLElement &&
				event.target.getAttribute("role") === "tab" &&
				event.key === "Tab" &&
				event.ctrlKey &&
				!event.altKey &&
				!event.metaKey
			) {
				event.preventDefault();
				selectAdjacentTab(event.shiftKey ? -1 : 1);
				return;
			}
			handleTerminalTabListKeyDown(event);
		},
		[selectAdjacentTab],
	);

	useEffect(
		() =>
			aoBridge.app.onCloseShellTerminalShortcut(() => {
				if (shellTarget) onCloseShellTerminal?.(shellTarget.handleId);
			}),
		[onCloseShellTerminal, shellTarget],
	);

	useEffect(() => {
		const disposePrevious = aoBridge.app.onPreviousTabShortcut(() => selectAdjacentTab(-1));
		const disposeNext = aoBridge.app.onNextTabShortcut(() => selectAdjacentTab(1));
		return () => {
			disposePrevious();
			disposeNext();
		};
	}, [selectAdjacentTab]);

	useEffect(() => {
		aoBridge.app.setCloseShellTerminalShortcutEnabled(Boolean(shellTarget && onCloseShellTerminal));
		return () => aoBridge.app.setCloseShellTerminalShortcutEnabled(false);
	}, [onCloseShellTerminal, shellTarget]);

	// Offered only while the agent is idle. The daemon refuses a rollback mid-turn,
	// and a control that exists to be refused is worse than one that waits.
	const rollbackTarget = onRollback && !turn && !newWorkDisabled ? (id: string) => setConfirming(id) : undefined;
	const discarded = snapshot.turns.filter((t) => t.rolledBack).length;

	const brokenServers = useMemo(() => brokenMcpServers(snapshot), [snapshot]);
	const editHumanMessage = onEditMessage;
	const pendingApproval = useMemo(
		() =>
			snapshot.items.reduce<ConversationActivity | undefined>((latest, item) => {
				if (
					item.kind !== "activity" ||
					item.activityKind !== "approval" ||
					item.status !== "pending" ||
					(item.turnId ? item.turnId !== turn?.id : !turn)
				) {
					return latest;
				}
				if (latest?.turnId && !item.turnId) return latest;
				if (item.turnId && !latest?.turnId) return item;
				return !latest || item.sequence > latest.sequence ? item : latest;
			}, undefined),
		[snapshot.items, turn],
	);
	const stableSettings = useStableValue(snapshot.settings);
	const stableModelReroute = useStableValue(snapshot.modelReroute);
	const stablePendingApproval = useStableValue(pendingApproval);
	const composerSettings = useMemo(
		() =>
			onChooseSettings || onChooseConfigOption ? (
				<TurnSettingsBar
					models={models ?? []}
					settings={stableSettings}
					harness={snapshot.harness}
					reroute={stableModelReroute}
					onChange={newWorkDisabled ? undefined : onChooseSettings}
					configOptions={configOptions ?? []}
					onChangeConfigOption={newWorkDisabled ? undefined : onChooseConfigOption}
					configPending={configOptionPending}
					error={configOptionError}
					disabled={
						snapshot.controller.state === "stopped" || configOptionPending || newWorkDisabled
					}
				/>
			) : null,
		[
			configOptionError,
			configOptionPending,
			configOptions,
			models,
			newWorkDisabled,
			onChooseConfigOption,
			onChooseSettings,
			snapshot.controller.state,
			stableModelReroute,
			stableSettings,
		],
	);
	const composerApproval = useMemo(
		() =>
			stablePendingApproval ? (
				<ApprovalCard
					activity={stablePendingApproval}
					onDecide={onDecide}
					busy={busy}
					embedded
				/>
			) : undefined,
		[busy, onDecide, stablePendingApproval],
	);
	const canSteerQueuedMessage =
		Boolean(onSteer) && can(snapshot, "steer") && turn?.state === "running";
	const composerQueuedDock = useMemo(
		() =>
			queuedMessages.length > 0 ? (
				<QueuedMessageDock
					messages={queuedMessages}
					editingTurnId={queueEdit?.turnId}
					canSteer={canSteerQueuedMessage}
					onPromoteQueuedTurn={newWorkDisabled ? undefined : promoteQueuedTurn}
					onBeginQueuedEdit={
						newWorkDisabled || !onEditQueuedTurn ? undefined : beginQueuedEdit
					}
					onCancelQueuedTurn={newWorkDisabled ? undefined : handleCancelQueuedTurn}
					onReorderQueuedTurns={newWorkDisabled ? undefined : onReorderQueuedTurns}
					promotePendingTurnId={promoteQueuedTurnPendingTurnId}
					cancelPendingTurnId={cancelQueuedTurnPendingTurnId}
				/>
			) : null,
		[
			beginQueuedEdit,
			canSteerQueuedMessage,
			cancelQueuedTurnPendingTurnId,
			handleCancelQueuedTurn,
			newWorkDisabled,
			onEditQueuedTurn,
			onReorderQueuedTurns,
			promoteQueuedTurn,
			promoteQueuedTurnPendingTurnId,
			queueEdit?.turnId,
			queuedMessages,
		],
	);
	const composerDraftSeed = useMemo(
		() => (queueEdit ? { id: queueEdit.turnId, text: queueEdit.text } : undefined),
		[queueEdit],
	);
	// Empty chats center the prompt; once a turn or item exists the composer docks
	// at the bottom and stays there for the rest of the session.
	const conversationEmpty = snapshot.items.length === 0 && !turn;
	const composerDockRef = useRef<HTMLDivElement>(null);
	const composerCenteredTopRef = useRef<number | null>(null);
	const composerFlipDyRef = useRef<number | null>(null);
	const composerSessionRef = useRef(snapshot.sessionId);

	useLayoutEffect(() => {
		const dock = composerDockRef.current;
		if (!dock) return;

		const sessionChanged = composerSessionRef.current !== snapshot.sessionId;
		if (sessionChanged) {
			composerSessionRef.current = snapshot.sessionId;
			composerCenteredTopRef.current = null;
			composerFlipDyRef.current = null;
			dock.style.transition = "";
			dock.style.transform = "";
			dock.removeAttribute("data-composer-motion");
		}

		if (conversationEmpty) {
			composerCenteredTopRef.current = dock.getBoundingClientRect().top;
			composerFlipDyRef.current = null;
			return;
		}

		// Capture the centered→docked delta once. Keep it across Strict Mode's
		// setup→cleanup→setup so the docking motion still plays.
		if (composerFlipDyRef.current == null && composerCenteredTopRef.current != null) {
			composerFlipDyRef.current = composerCenteredTopRef.current - dock.getBoundingClientRect().top;
			composerCenteredTopRef.current = null;
		}

		const dy = composerFlipDyRef.current;
		const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
		if (dy == null || reduceMotion || Math.abs(dy) < 1) {
			composerFlipDyRef.current = null;
			return;
		}

		dock.dataset.composerMotion = "animating";
		dock.style.transition = "none";
		dock.style.transform = `translateY(${dy}px)`;
		void dock.offsetHeight;
		dock.style.transition = "transform 500ms cubic-bezier(0.22, 1, 0.36, 1)";
		dock.style.transform = "translateY(0)";

		const onEnd = (event: TransitionEvent) => {
			if (event.target !== dock || event.propertyName !== "transform") return;
			composerFlipDyRef.current = null;
			dock.style.transition = "";
			dock.style.transform = "";
			dock.removeAttribute("data-composer-motion");
		};
		dock.addEventListener("transitionend", onEnd);
		return () => {
			dock.removeEventListener("transitionend", onEnd);
		};
	}, [conversationEmpty, snapshot.sessionId]);

	return (
		<section
			ref={surfaceRef}
			onKeyDown={handleChatKeyDown}
			onClick={handleChatSurfaceClick}
			aria-label="Chat"
			className="cursor-chat-surface flex h-full min-h-0 flex-col [font-size:var(--chat-font-size)]"
			data-session-mode={snapshot.mode}
			data-session-role={sessionRole}
			style={
				{
					"--chat-font-size": `${CHAT_FONT_SIZE_DEFAULT}px`,
				} as CSSProperties
			}
		>
			<ChatHeader
				snapshot={snapshot}
				sessionTitle={sessionTitle}
				sessionRole={sessionRole}
				onOpenReviewerTerminal={onOpenReviewerTerminal}
				reviewerActive={reviewerActive}
				onSelectChat={onSelectChat}
				shellActiveHandleId={shellActive ? shellTarget?.handleId : undefined}
				onSelectShellTerminal={onSelectShellTerminal}
				onCloseShellTerminal={onCloseShellTerminal}
				onRenameShellTerminal={onRenameShellTerminal}
				onTabsKeyDown={handleChatTabsKeyDown}
				headerActions={headerActions}
				session={session}
				onSessionRenamed={onSessionRenamed}
				sessionTabAction={sessionTabAction}
				tabStripAction={tabStripAction}
				workspaceTabActions={workspaceTabActions}
				workspaceActiveTabKey={workspaceActiveTabKey}
				orderedAuxiliaryTabs={orderedAuxiliaryTabs}
				onReorderAuxiliaryTabs={reorderAuxiliaryTabs}
				inline={isFullscreen}
				topbarBounds={topbarBounds}
			/>
			<div className="relative flex min-h-0 flex-1 flex-col">
				{reviewerTarget && session ? (
					<div
						aria-label="Reviewer terminal"
						className="relative min-h-0 flex-1"
						data-testid="chat-reviewer-panel"
						onWheelCapture={handleWheelZoom}
						role="tabpanel"
					>
						<div className="h-full min-h-0" data-testid="chat-reviewer-terminal">
							<TerminalPane
								daemonReady={Boolean(daemonReady)}
								fontSize={terminalFontSize}
								isFullscreen={isFullscreen}
								onChangeFontSize={updateTerminalFontSize}
								onToggleFullscreen={toggleFullscreen}
								session={session}
								terminalTarget={reviewerTarget}
								theme={theme ?? "dark"}
							/>
						</div>
					</div>
				) : null}
				{shellTarget && session ? (
					<div
						aria-label="Shell terminal"
						className="relative min-h-0 flex-1"
						data-testid="chat-shell-panel"
						onWheelCapture={handleWheelZoom}
						role="tabpanel"
					>
						<div className="h-full min-h-0" data-testid="chat-shell-terminal">
							<TerminalPane
								daemonReady={Boolean(daemonReady)}
								fontSize={terminalFontSize}
								focusRequested
								isFullscreen={isFullscreen}
								onChangeFontSize={updateTerminalFontSize}
								onToggleFullscreen={toggleFullscreen}
								session={session}
								terminalTarget={shellTarget}
								theme={theme ?? "dark"}
							/>
						</div>
					</div>
				) : null}
				<div
					aria-hidden={reviewerActive || shellActive}
					aria-label="Chat conversation"
					className="flex min-h-0 flex-1 flex-col"
					data-testid="chat-conversation-panel"
					hidden={reviewerActive || shellActive}
					inert={reviewerActive || shellActive || agentInputDisabled ? true : undefined}
					role="tabpanel"
				>
					{/* Ordered by what blocks what. A session that needs credentials cannot make
				    progress at all, so it is stated first; the controller's own health next;
				    then the two that degrade a session rather than stopping it. */}
					{snapshot.account ? (
						<ReauthBanner account={snapshot.account} harness={snapshot.harness} />
					) : null}
					<ControllerBanner
						controller={snapshot.controller}
						transitioning={controllerTransitioning}
						onResume={newWorkDisabled ? undefined : onResumeAgent}
						resuming={resumingAgent}
						resumeError={resumeError}
						onOpenShell={onOpenShell}
						openingShell={openingShell}
						shellError={shellError}
					/>
					{snapshot.threadState ? <ThreadStateBanner threadState={snapshot.threadState} /> : null}
					<McpServerBanner
						servers={brokenServers}
						onReload={newWorkDisabled ? undefined : onReloadMcpServers}
						reloading={reloadingMcpServers}
						turnInFlight={Boolean(turn)}
						error={mcpReloadError}
					/>
					<div
						className={cn("flex min-h-0 flex-1 flex-col", conversationEmpty && "justify-center")}
						data-composer-placement={conversationEmpty ? "center" : "dock"}
					>
						<ChatLinkProvider onLinkOpen={onLinkOpen}>
							<Timeline
								snapshot={snapshot}
								hasOlder={hasOlder}
								loadingOlder={loadingOlder}
								onLoadOlder={onLoadOlder}
								onDecide={onDecide}
								onResolveInput={onResolveInput}
								busy={busy}
								onRollback={rollbackTarget}
								onOpenFiles={onOpenFiles}
								onOpenFile={onOpenFile}
								retryControl={retryControl}
								onEditHumanMessage={editHumanMessage}
								editPending={editMessagePending}
								editBusy={Boolean(turn)}
								editError={editMessageError}
								onActivateBranch={onActivateBranch}
								activateBranchPending={activateBranchPending}
								activateBranchError={activateBranchError}
								newWorkDisabled={newWorkDisabled}
							/>
						</ChatLinkProvider>

						<div ref={composerDockRef} className="cursor-chat-composer-dock shrink-0 px-4 pb-3">
							<div aria-hidden="true" className="chat-composer-fade" />
							<div
								data-empty={conversationEmpty || undefined}
								className="mx-auto flex w-full max-w-3xl flex-col gap-2 transition-[max-width] duration-500 ease-out data-[empty]:max-w-2xl"
							>
								{discarded > 0 ? <RolledBackNotice count={discarded} /> : null}
								<ChatComposer
									queuedDock={composerQueuedDock}
									approval={composerApproval}
									onSend={composerSend}
									draftSeed={composerDraftSeed}
									editingQueuedTurnId={queueEdit?.turnId}
									savingQueuedEditPending={Boolean(
										queueEdit?.turnId &&
											editQueuedTurnPendingTurnId === queueEdit.turnId,
									)}
									onCancelQueuedEdit={cancelQueuedEdit}
									onInterrupt={turn && !newWorkDisabled ? stableInterrupt : undefined}
									commandError={commandError}
									settings={composerSettings}
									busy={busy}
									willQueue={Boolean(turn)}
									disabled={snapshot.controller.state === "stopped" || newWorkDisabled}
									skills={skills}
									filePaths={filePaths}
									filePathsTruncated={filePathsTruncated}
									onStageAttachments={newWorkDisabled ? undefined : onStageAttachments}
									nativeImages={nativeImages}
									autoFocus={!reviewerActive}
									autoFocusKey={snapshot.sessionId}
									// Steering is only meaningful into a turn that is running. A queued turn
									// has not reached the provider, so there is nothing to steer.
									onSteer={newWorkDisabled ? undefined : steer}
									canSteer={Boolean(onSteer) && turn?.state === "running"}
									sendPending={sendPending}
									steerPending={steerPending}
									steerRefusal={steerRefusal}
									onCompact={newWorkDisabled ? undefined : onCompact}
									compacting={compacting}
									compactUnavailable={compactUnavailable}
									compactBlocked={Boolean(turn)}
								/>
							</div>
						</div>
					</div>
				</div>
			</div>

			{/* The copy has to be honest about the cost: this is not "hide these
			    messages", it is "the agent forgets them". Nothing in the worktree is
			    reverted either, and a user who assumed otherwise would be badly
			    surprised, so it is said out loud. */}
			<ConfirmDialog
				open={Boolean(confirming) && !reviewerActive && !shellActive}
				onOpenChange={(open) => {
					if (!open) setConfirming(undefined);
				}}
				title="Roll back to this point?"
				description={
					<>
						<p className="text-sm font-medium text-foreground">
							The agent will forget this exchange and everything after it.
						</p>
						<p className="mt-1 text-xs text-muted-foreground">
							Its memory of the conversation is discarded up to this point, so it will not know
							about anything you or it said later. Files it already changed in the worktree are left
							exactly as they are; only the conversation is rolled back. This cannot be undone.
						</p>
					</>
				}
				confirmLabel="Roll back"
				destructive
				busy={rollbackPending}
				error={rollbackError ?? null}
				onConfirm={() => {
					const turnId = confirming;
					if (!turnId) return;
					setConfirming(undefined);
					void Promise.resolve(onRollback?.(turnId)).catch(() => {});
				}}
			/>
		</section>
	);
}

/**
 * What an undo took away.
 *
 * Stated above the composer rather than as a timeline entry, because the discarded
 * turns keep their original sequence positions and this notice has none of its own —
 * placing it in the timeline would be claiming an order it cannot know. It sits where
 * the user is looking after an undo, and it says the part that matters: the agent
 * does not remember.
 */
function RolledBackNotice({ count }: { count: number }) {
	return (
		<p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
			<Undo2 aria-hidden="true" className="size-3 shrink-0" />
			{count === 1
				? "1 turn was rolled back. The agent no longer remembers it."
				: `${count} turns were rolled back. The agent no longer remembers them.`}
		</p>
	);
}

/**
 * Batch consecutive plain activities into a run.
 *
 * A run of tool calls is one thought, not several, so it renders as one tight
 * block on a shared rail. Messages and approvals break a run because each is
 * something the reader stops on: prose to read, or a decision to make.
 */
type TimelineRun =
	| { kind: "activities"; key: string; items: ConversationItem[] }
	| { kind: "single"; key: string; items: [ConversationItem] };

function runsOf(items: ConversationItem[]): TimelineRun[] {
	const runs: TimelineRun[] = [];
	for (const item of items) {
		const runnable =
			item.kind === "activity" &&
			item.activityKind !== "approval" &&
			item.activityKind !== "user_input" &&
			item.activityKind !== "error" &&
			// An edit is a result, not a mechanic. Burying it in a summary would hide
			// the one kind of activity that changed the user's worktree.
			item.activityKind !== "file_change" &&
			// Reasoning only reaches the timeline when the reader asked for it, so
			// folding it into "Explored 4 files" would answer that request with the
			// summary they were trying to get past.
			item.activityKind !== "reasoning" &&
			// A compaction is a boundary in the conversation, not a step in one. Folding
			// it into a run of tool calls would hide that everything above it is no
			// longer what the agent sees verbatim. The same holds for every other
			// stamped system event: a reroute, a credential demand and the user's own
			// steer are all things a run summary would swallow.
			item.detail?.event === undefined;
		const last = runs.at(-1);
		if (runnable && last?.kind === "activities") {
			last.items.push(item);
			continue;
		}
		runs.push(
			runnable
				? {
						kind: "activities",
						key: `run-${item.sequence}`,
						items: [item],
					}
				: { kind: "single", key: item.id, items: [item] },
		);
	}
	return runs;
}

/**
 * What belongs in a conversation, as opposed to what the provider happens to emit.
 *
 * Two kinds are dropped:
 *
 *   - usage. The daemon now projects it as current state on the snapshot rather
 *     than as a timeline entry, so this filter is a guard for conversations
 *     recorded by an older build whose usage rows are still on disk. Rendering one
 *     row per report is what buried the actual conversation; it lives in the
 *     header meter instead.
 *   - reasoning. Providers can emit one per tool call and they usually carry no
 *     readable body, so they are internal signal rather than conversation chrome.
 *
 *   - a plan row whose turn already carries the plan. The daemon writes both, from
 *     one event, so they cannot disagree; the turn's copy renders as a checklist at
 *     the end of the turn and a second rendering of the same plan in the middle of
 *     it would just be the same list twice.
 *
 * Everything else is kept, including an activity this build does not fully
 * understand — dropping an unrecognized item would hide work the agent really did.
 */
function readableItems(snapshot: ConversationSnapshot): ConversationItem[] {
	const plannedTurns = new Set(
		snapshot.turns.filter((turn) => turn.plan?.steps.length).map((turn) => turn.id),
	);
	return snapshot.items.filter((item) => {
		if (item.kind !== "activity") return true;
		if (item.activityKind === "usage") return false;
		if (item.activityKind === "plan" && item.turnId && plannedTurns.has(item.turnId)) return false;
		if (item.activityKind === "reasoning") return false;
		return true;
	});
}

/* -------------------------------------------------------------------------- */

function ChatHeader({
	snapshot,
	sessionTitle,
	sessionRole,
	onOpenReviewerTerminal,
	reviewerActive,
	onSelectChat,
	shellActiveHandleId,
	onSelectShellTerminal,
	onCloseShellTerminal,
	onRenameShellTerminal,
	onTabsKeyDown,
	headerActions,
	sessionTabAction,
	tabStripAction,
	workspaceTabActions,
	workspaceActiveTabKey,
	orderedAuxiliaryTabs,
	onReorderAuxiliaryTabs,
	inline,
	topbarBounds,
	session,
	onSessionRenamed,
}: {
	snapshot: ConversationSnapshot;
	sessionTitle?: string;
	sessionRole: SessionKind;
	onOpenReviewerTerminal?: (target: { handleId: string; harness: string }) => void;
	/** The reviewer tab is selected; the chat tab is the clickable alternative. */
	reviewerActive?: boolean;
	/** Return the tab strip to the chat tab. */
	onSelectChat?: () => void;
	/** The selected shell tab, if any. */
	shellActiveHandleId?: string;
	onSelectShellTerminal?: (handleId: string) => void;
	onCloseShellTerminal?: (handleId: string) => void;
	onRenameShellTerminal?: (handleId: string, title: string) => void;
	onTabsKeyDown?: (event: ReactKeyboardEvent<HTMLDivElement>) => void;
	headerActions?: ReactNode;
	sessionTabAction?: ReactNode;
	tabStripAction?: ReactNode;
	workspaceTabActions?: ReactNode;
	workspaceActiveTabKey?: string;
	orderedAuxiliaryTabs: ChatAuxiliaryTab[];
	onReorderAuxiliaryTabs: (keys: string[]) => void;
	session?: WorkspaceSession;
	onSessionRenamed?: () => void | Promise<void>;
	/** Fullscreen content cannot see the normal topbar portal outside its subtree. */
	inline?: boolean;
	topbarBounds: TopbarBounds;
}) {
	const { t } = useTranslation();
	const providerLabel = agentLabel(snapshot.harness);
	const sessionIsOrchestrator = session
		? isOrchestratorSession(session)
		: sessionRole === "orchestrator";
	const label = sessionIsOrchestrator
		? t("shell.orchestrator")
		: (sessionTitle || session?.title || snapshot.title || snapshot.sessionId);
	const tabScrollWatch = `${session?.id ?? ""}|${orderedAuxiliaryTabs.map((tab) => tab.key).join("|")}`;
	const {
		scrollRef: tabsOverflowRef,
		scrollToEnd: scrollTabsToEnd,
		showLeftFade,
		showRightFade,
	} = useTabScrollEdges([tabScrollWatch]);
	const previousTabCountRef = useRef(orderedAuxiliaryTabs.length);
	useEffect(() => {
		if (orderedAuxiliaryTabs.length > previousTabCountRef.current) scrollTabsToEnd();
		previousTabCountRef.current = orderedAuxiliaryTabs.length;
	}, [orderedAuxiliaryTabs.length, scrollTabsToEnd]);
	// The chat tab is "selected" only when neither terminal pane is the body.
	const timelineActive = !workspaceActiveTabKey && !reviewerActive && !shellActiveHandleId;
	// Match CenterPane: when the sidebar is off-canvas, the fixed TitlebarNav
	// cluster sits over the session tab strip. Terminal already reserves that
	// space; chat must too or the back/forward buttons land on the tab label.
	const isSidebarOpen = useUiStore(sidebarOccupiesLayout);
	const header = (
		<header className="flex h-inspector-tabs w-full shrink-0 items-stretch bg-sidebar">
			<div
				className="session-topbar-surface flex min-w-0 flex-1"
				data-testid="session-workspace-topbar"
			>
				<div
					className={cn(
						"flex min-w-0 shrink items-stretch",
						!isSidebarOpen && isMac && "session-topbar-titlebar-clearance-mac",
						!isSidebarOpen && isLinux && "session-topbar-titlebar-clearance-linux",
					)}
					data-testid="session-terminal-region"
					style={{
						width: topbarBounds.width > 0 ? topbarBounds.width : "100%",
					}}
				>
					<div
						aria-label="Chat tabs"
						className="flex h-full min-w-0 flex-1 items-stretch"
						onKeyDown={onTabsKeyDown ?? handleTerminalTabListKeyDown}
						role="tablist"
					>
						{session ? (
							<SessionPaneTab
								isActive={timelineActive}
								label={label}
								onSelect={timelineActive ? undefined : onSelectChat}
								onRenamed={onSessionRenamed}
								session={session}
								tabAction={sessionTabAction}
							/>
						) : (
							<button
								aria-current={timelineActive ? true : undefined}
								aria-label={`${label} · ${providerLabel}`}
								aria-selected={timelineActive}
								data-terminal-role="primary"
								className={cn(
									"group relative inline-flex min-w-shell-tab-min max-w-shell-tab-max shrink-0 self-stretch cursor-pointer items-center gap-1.5 border-r border-border px-3 text-control font-medium leading-none transition-colors focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/50",
									timelineActive
										? "bg-overlay text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground/80"
										: "text-muted-foreground hover:bg-raised hover:text-foreground",
								)}
								onClick={timelineActive ? undefined : onSelectChat}
								role="tab"
								tabIndex={timelineActive || orderedAuxiliaryTabs.length === 0 ? 0 : -1}
								title={label}
								type="button"
							>
								<AgentAvatar className="size-icon-base" decorative provider={snapshot.harness} />
								<span className="truncate">{label}</span>
							</button>
						)}
						<div className="relative min-w-0 flex-1 self-stretch overflow-hidden">
							<div ref={tabsOverflowRef} className="scrollbar-none flex h-full min-w-flex-min min-w-0 items-stretch overflow-x-auto">
								<div className="flex w-max items-stretch">
								<Reorder.Group
									as="div"
									axis="x"
									className="flex items-stretch self-stretch"
									onReorder={onReorderAuxiliaryTabs}
									values={orderedAuxiliaryTabs.map((tab) => tab.key)}
								>
									{orderedAuxiliaryTabs.map((tab) => (
										<DraggableChatTab key={tab.key} value={tab.key}>
											{tab.kind === "reviewer" ? (
												<button
													aria-current={reviewerActive ? true : undefined}
													aria-label="Reviewer"
													aria-selected={Boolean(reviewerActive)}
													className={cn(
														"group relative inline-flex min-w-shell-tab-min max-w-shell-tab-max self-stretch cursor-pointer items-center gap-1.5 border-r border-border px-3 text-control font-medium leading-none transition-colors focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/50",
														reviewerActive
															? "bg-overlay text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground/80"
															: "text-muted-foreground hover:bg-raised hover:text-foreground",
													)}
													onClick={() => onOpenReviewerTerminal?.(tab.terminal)}
													role="tab"
													tabIndex={reviewerActive ? 0 : -1}
													title={tab.terminal.harness}
													type="button"
												>
													<AgentAvatar className="size-icon-base" decorative provider={tab.terminal.harness} />
													<span className="truncate">Reviewer</span>
												</button>
											) : tab.kind === "shell" ? (
												<ShellTerminalTab
													appearance="connected"
													isActive={tab.terminal.handleId === shellActiveHandleId}
													onClose={() => onCloseShellTerminal?.(tab.terminal.handleId)}
													onRename={onRenameShellTerminal ? (title) => onRenameShellTerminal(tab.terminal.handleId, title) : undefined}
													onSelect={() => onSelectShellTerminal?.(tab.terminal.handleId)}
													shell={tab.terminal}
												/>
											) : (
												tab.tab.content
											)}
										</DraggableChatTab>
									))}
								</Reorder.Group>
								{workspaceTabActions}
								</div>
							</div>
							{showLeftFade ? <div aria-hidden="true" className="session-tab-scroll-fade session-tab-scroll-fade--left" /> : null}
							{showRightFade ? <div aria-hidden="true" className="session-tab-scroll-fade" /> : null}
						</div>
					</div>
				</div>
				<div
					className="ml-auto flex shrink-0 items-center gap-1 pl-2 pr-3"
					data-testid="session-action-region"
				>
					{tabStripAction ? <div data-testid="session-tab-strip-action">{tabStripAction}</div> : null}
					{headerActions}
				</div>
			</div>
		</header>
	);
	return inline ? header : <SessionTopbarPortal>{header}</SessionTopbarPortal>;
}

/**
 * Controller health. A stopped or recovering controller is announced, because a
 * silent surface is indistinguishable from an agent that is simply thinking.
 */
function ControllerBanner({
	controller,
	transitioning,
	onResume,
	resuming,
	resumeError,
	onOpenShell,
	openingShell,
	shellError,
}: {
	controller: { state: ControllerState; error?: string };
	transitioning?: boolean;
	onResume?: () => void;
	resuming?: boolean;
	resumeError?: string;
	onOpenShell?: () => void;
	openingShell?: boolean;
	shellError?: string;
}) {
	// The transition coordinator intentionally stops one controller before it
	// starts the other. The top-bar handoff state already explains that interval;
	// presenting its intermediate snapshot as a crash produces a red false alarm.
	if (transitioning && controller.state === "stopped") return null;
	if (controller.state === "ready" || controller.state === "busy") return null;

	const copy: Partial<Record<ControllerState, { title: string; tone: string }>> = {
		connecting: {
			title: "Connecting to the agent…",
			tone: "text-muted-foreground",
		},
		recovering: {
			title: "Reconnecting to the agent",
			tone: "text-warning",
		},
		stopped: {
			title: "The agent controller stopped",
			tone: "text-destructive",
		},
	};
	const shown = copy[controller.state];
	if (!shown) return null;

	return (
		<div
			role={controller.state === "stopped" ? "alert" : "status"}
			aria-atomic="true"
			className="flex shrink-0 items-start gap-2.5 border-b border-border bg-surface px-4 py-2.5"
		>
			{controller.state === "connecting" ? (
				<Loader2
					aria-hidden="true"
					className="mt-0.5 size-3.5 shrink-0 animate-spin text-muted-foreground"
				/>
			) : (
				<TriangleAlert aria-hidden="true" className={cn("mt-0.5 size-3.5 shrink-0", shown.tone)} />
			)}
			<div className="flex min-w-0 flex-1 flex-col gap-0.5">
				<strong className={cn("text-xs font-medium", shown.tone)}>{shown.title}</strong>
				{controller.error ? (
					<span className="text-[11px] leading-snug text-muted-foreground">{controller.error}</span>
				) : null}
				{controller.state === "stopped" ? (
					<>
						<span className="text-[11px] leading-snug text-muted-foreground">
							History is kept. Resume the agent or open a shell in the same worktree.
						</span>
						{resumeError || shellError ? (
							<span className="text-[11px] leading-snug text-destructive">
								{resumeError ?? shellError}
							</span>
						) : null}
						<div className="mt-1.5 flex flex-wrap gap-2">
							{onResume ? (
								<Button
									type="button"
									size="sm"
									variant="outline"
									onClick={onResume}
									disabled={resuming}
								>
									{resuming ? "Resuming…" : "Resume agent"}
								</Button>
							) : null}
							{onOpenShell ? (
								<Button
									type="button"
									size="sm"
									variant="ghost"
									onClick={onOpenShell}
									disabled={openingShell}
								>
									{openingShell ? "Opening shell…" : "Open shell"}
								</Button>
							) : null}
						</div>
					</>
				) : null}
			</div>
		</div>
	);
}

/* -------------------------------------------------------------------------- */

/**
 * The scrolling timeline.
 *
 * Auto-scroll only follows new items while the user is already near the bottom.
 * Once they scroll up to read, new output must not yank them away — it surfaces a
 * jump control instead.
 *
 * A trailing spacer sizes itself so the latest human prompt can sit near the top
 * of the viewport when following — the Cursor/ChatGPT "drop a prompt and the
 * history moves up" behaviour. As the reply grows the spacer shrinks, which keeps
 * the prompt in place without fighting the reader.
 *
 * Finished turns are memoized so a targeted live-event invalidation only redraws
 * the turn that changed. Older pages are prepended explicitly instead of keeping
 * an unbounded history in every snapshot response.
 */
function Timeline({
	snapshot,
	hasOlder,
	loadingOlder,
	onLoadOlder,
	onDecide,
	onResolveInput,
	busy,
	onRollback,
	onOpenFiles,
	onOpenFile,
	retryControl,
	onEditHumanMessage,
	editPending,
	editBusy,
	editError,
	onActivateBranch,
	activateBranchPending,
	activateBranchError,
	newWorkDisabled,
}: {
	snapshot: ConversationSnapshot;
	hasOlder?: boolean;
	loadingOlder?: boolean;
	onLoadOlder?: () => void;
	onDecide?: (requestId: string, decisionId: string) => void;
	onResolveInput?: ChatWorkspaceProps["onResolveInput"];
	busy?: boolean;
	onRollback?: (turnId: string) => void;
	onOpenFiles?: () => void;
	onOpenFile?: (path: string) => void;
	retryControl?: ChatRetryControl;
	onEditHumanMessage?: (turnId: string, text: string) => Promise<unknown> | void;
	editPending?: boolean;
	editBusy?: boolean;
	editError?: string;
	onActivateBranch?: (branchId: string) => Promise<unknown> | void;
	activateBranchPending?: boolean;
	activateBranchError?: string;
	newWorkDisabled?: boolean;
}) {
	const scroller = useRef<HTMLDivElement>(null);
	const scrollContent = useRef<HTMLDivElement>(null);
	const promptSpacer = useRef<HTMLDivElement>(null);
	const scrollTrack = useRef<HTMLDivElement>(null);
	const drag = useRef<{
		pointerId: number;
		startY: number;
		startScrollTop: number;
	} | null>(null);
	const pinnedRef = useRef(true);
	const [pinned, setPinned] = useState(true);
	const [hoveredMarker, setHoveredMarker] = useState<number | null>(null);
	const [messageEdit, setMessageEdit] = useState<MessageEditDraft>();
	const isInspectorOpen = useUiStore(
		(state) => state.inspectorSessions[snapshot.sessionId]?.isOpen ?? true,
	);
	const turn = activeTurn(snapshot);
	const [scrollbar, setScrollbar] = useState({
		visible: false,
		top: 0,
		height: 40,
		percent: 0,
		markers: [] as Array<{
			top: number;
			scrollTop: number;
			visible: boolean;
		}>,
	});
	const minimapEnabled = scrollbar.visible && !isInspectorOpen;
	const queued = useMemo(() => queuedTurnIds(snapshot), [snapshot]);
	const decide = useStableCallback(onDecide);
	const resolveInput = useStableCallback(onResolveInput);
	const rollback = useStableCallback(onRollback);
	const openFiles = useStableCallback(onOpenFiles);
	const openFile = useStableCallback(onOpenFile);
	const retryTurn = useStableCallback(retryControl?.retry);
	const apiBaseUrl = useSyncExternalStore(subscribeApiBaseUrl, getApiBaseUrl, getApiBaseUrl);
	const editHumanMessage = useStableCallback(onEditHumanMessage);
	const activateBranch = useStableCallback(onActivateBranch);
	const canEditHumanMessage = Boolean(onEditHumanMessage) && !newWorkDisabled;
	const canActivateBranch = Boolean(onActivateBranch) && !newWorkDisabled;
	const canForkHistoricalContext = can(snapshot, "fork");
	const canReconstructHistoricalContext =
		can(snapshot, "prompt_replay") && can(snapshot, "embedded_context");
	const branchPoints = useMemo(
		() => new Map((snapshot.branchPoints ?? []).map((point) => [point.turnId, point])),
		[snapshot.branchPoints],
	);
	const firstEditableHumanPromptTurnId = useMemo(
		() =>
			snapshot.items.find(
				(item) =>
					item.kind === "message" &&
					item.role === "user" &&
					item.origin === "human" &&
					item.editAvailable &&
					item.turnId,
			)?.turnId,
		[snapshot.items],
	);
	const { editableTurns, reconstructedTurns } = useMemo(() => {
		const editable = new Set<string>();
		const reconstructed = new Set<string>();
		if (snapshot.controller.state !== "ready") {
			return {
				editableTurns: editable,
				reconstructedTurns: reconstructed,
			};
		}
		const accepted = new Set(
			snapshot.turns
				.filter(
					(turn) =>
						turn.state === "completed" || turn.state === "interrupted" || turn.state === "failed",
				)
				.map((turn) => turn.id),
		);
		for (const item of snapshot.items) {
			if (
				item.kind !== "message" ||
				item.role !== "user" ||
				item.origin !== "human" ||
				!item.turnId
			) {
				continue;
			}
			const firstPromptInBinding =
				!snapshot.hasMoreBefore && item.turnId === firstEditableHumanPromptTurnId;
			const canNativeFork =
				canForkHistoricalContext &&
				(snapshot.nativeForkAvailableAfterSequence ?? 0) > 0 &&
				item.sequence > (snapshot.nativeForkAvailableAfterSequence ?? 0);
			if (
				item.editAvailable &&
				accepted.has(item.turnId) &&
				(firstPromptInBinding || canNativeFork || canReconstructHistoricalContext)
			) {
				editable.add(item.turnId);
				if (!firstPromptInBinding && !canNativeFork) reconstructed.add(item.turnId);
			}
		}
		return { editableTurns: editable, reconstructedTurns: reconstructed };
	}, [
		canForkHistoricalContext,
		canReconstructHistoricalContext,
		firstEditableHumanPromptTurnId,
		snapshot.controller.state,
		snapshot.hasMoreBefore,
		snapshot.items,
		snapshot.nativeForkAvailableAfterSequence,
		snapshot.turns,
	]);

	// An edit draft belongs to the branch where it began. A replacement branch can
	// become active even when the provider send ends ambiguously; once that durable
	// state arrives, the old prompt is no longer the message being edited.
	useEffect(() => setMessageEdit(undefined), [snapshot.activeBranchId, snapshot.sessionId]);
	useEffect(() => {
		if (isInspectorOpen) {
			drag.current = null;
			setHoveredMarker(null);
		}
	}, [isInspectorOpen]);
	const consumedRetrySources = useMemo(() => retrySourceTurnIds(snapshot), [snapshot]);
	const retryableTurns = useMemo(
		() =>
			new Set(
				snapshot.turns
					.filter(
						(turn) =>
							turn.state === "failed" &&
							Boolean(turn.providerTurnId) &&
							!consumedRetrySources.has(turn.id),
					)
					.map((turn) => turn.id),
			),
		[snapshot.turns, consumedRetrySources],
	);

	useEffect(() => {
		pinnedRef.current = pinned;
	}, [pinned]);

	const startMessageEdit = useCallback(
		(message: ConversationMessage) => {
			if (!message.turnId) return;
			setMessageEdit({
				turnId: message.turnId,
				text: message.text,
				content: message.content ?? [],
				reconstructedContext: reconstructedTurns.has(message.turnId),
			});
		},
		[reconstructedTurns],
	);
	const updateMessageEdit = useCallback((text: string) => {
		setMessageEdit((current) => (current ? { ...current, text } : current));
	}, []);
	const cancelMessageEdit = useCallback(() => setMessageEdit(undefined), []);
	const submitMessageEdit = useCallback(
		async (text: string) => {
			const current = messageEdit;
			if (!current || !onEditHumanMessage) return;
			await editHumanMessage(current.turnId, text);
			setMessageEdit((active) => (active?.turnId === current.turnId ? undefined : active));
		},
		[editHumanMessage, messageEdit, onEditHumanMessage],
	);

	const readable = useMemo(() => readableItems(snapshot), [snapshot]);
	const items = useStableList(readable, itemKey, sameContent);
	const seenHumanMessageIds = useRef<Set<string> | undefined>(undefined);
	const lastSeenLatestSequence = useRef<number | undefined>(undefined);
	const [newHumanMessageIds, setNewHumanMessageIds] = useState<ReadonlySet<string>>(new Set());
	const editedMessageVisible = Boolean(
		messageEdit &&
		items.some(
			(item) =>
				item.kind === "message" && item.role === "user" && item.turnId === messageEdit.turnId,
		),
	);
	useEffect(() => {
		const humanMessages = items.filter(
			(item): item is ConversationMessage =>
				item.kind === "message" && item.role === "user" && item.origin === "human",
		);
		const humanMessageIds = new Set(humanMessages.map((item) => item.id));
		if (!seenHumanMessageIds.current) {
			seenHumanMessageIds.current = humanMessageIds;
			lastSeenLatestSequence.current = snapshot.latestSequence;
			return;
		}
		const added = new Set(
			humanMessages
				.filter(
					(item) =>
						!seenHumanMessageIds.current?.has(item.id) &&
						item.sequence > (lastSeenLatestSequence.current ?? -Infinity),
				)
				.map((item) => item.id),
		);
		seenHumanMessageIds.current = humanMessageIds;
		lastSeenLatestSequence.current = snapshot.latestSequence;
		if (added.size > 0) setNewHumanMessageIds(added);
	}, [items, snapshot.latestSequence]);
	const grouped = useMemo(() => {
		const hiddenTurns = hiddenTimelineTurnIds(snapshot);
		return groupByTurn({ ...snapshot, items }).filter(
			(group) => !group.turnId || !hiddenTurns.has(group.turnId),
		);
	}, [snapshot, items]);
	const groups = useStableList(grouped, groupKey, sameGroup);
	const navigableGroups = useMemo(() => groups.filter(groupHasHumanPrompt), [groups]);
	const previews = useMemo(() => navigableGroups.map(groupPreview), [navigableGroups]);

	// Keep the full transcript mounted (selection/find still work), but measure
	// prompt positions only when content geometry changes, not on every scroll.
	const anchorGeometry = useRef<{ height: number; width: number; positions: number[] } | null>(null);
	const contentMutations = useRef<MutationObserver | null>(null);

	const updateScrollbar = useCallback(() => {
		const node = scroller.current;
		const track = scrollTrack.current;
		if (!node || !track) return;

		const maxScroll = Math.max(0, node.scrollHeight - node.clientHeight);
		const visible = maxScroll > 1;
		const trackHeight = track.clientHeight;
		const height = visible
			? Math.max(40, trackHeight * (node.clientHeight / node.scrollHeight))
			: trackHeight;
		const travel = Math.max(0, trackHeight - height);
		const fraction = maxScroll > 0 ? Math.min(1, Math.max(0, node.scrollTop / maxScroll)) : 0;
		if (contentMutations.current?.takeRecords().length) anchorGeometry.current = null;
		let geometry = anchorGeometry.current;
		if (!geometry || geometry.height !== node.scrollHeight || geometry.width !== node.clientWidth) {
			const viewportRect = node.getBoundingClientRect();
			const anchors = Array.from(
				scrollContent.current?.querySelectorAll<HTMLElement>("[data-chat-scroll-anchor]") ?? [],
			);
			geometry = {
				height: node.scrollHeight,
				width: node.clientWidth,
				positions: anchors.map((anchor) => {
					const rect = anchor.getBoundingClientRect();
					return rect.top - viewportRect.top + node.scrollTop + rect.height / 2;
				}),
			};
			anchorGeometry.current = geometry;
		}
		const positions = geometry.positions;
		// 8px is the existing 2px dash + 6px gap cadence.
		const markerGap = positions.length > 1 ? Math.min(8, (trackHeight - 12) / (positions.length - 1)) : 0;
		const markerStart = (trackHeight - markerGap * Math.max(0, positions.length - 1)) / 2;
		const markers = positions.map((contentY, index) => ({
			top: markerStart + index * markerGap,
			scrollTop: Math.min(maxScroll, Math.max(0, contentY - node.clientHeight / 2)),
			visible: contentY >= node.scrollTop && contentY <= node.scrollTop + node.clientHeight,
		}));
		const next = {
			visible,
			top: travel * fraction,
			height,
			percent: Math.round(fraction * 100),
			markers,
		};
		setScrollbar((current) =>
			current.visible === next.visible &&
			Math.abs(current.top - next.top) < 0.5 &&
			Math.abs(current.height - next.height) < 0.5 &&
			current.percent === next.percent &&
			current.markers.length === next.markers.length &&
			current.markers.every((marker, index) => {
				const candidate = next.markers[index];
				return (
					candidate !== undefined &&
					Math.abs(marker.top - candidate.top) < 0.5 &&
					Math.abs(marker.scrollTop - candidate.scrollTop) < 0.5 &&
					marker.visible === candidate.visible
				);
			})
				? current
				: next,
		);
	}, []);

	/**
	 * Size the trailing spacer so scrolling to the bottom parks the latest human
	 * prompt below a band of prior chat — matching Cursor, where a little of the
	 * previous turn stays visible above the new prompt instead of flush to the top.
	 * Shrinks naturally as that reply grows.
	 */
	const syncPromptSpacer = useCallback(() => {
		const node = scroller.current;
		const pad = promptSpacer.current;
		if (!node || !pad) return;

		const anchors = scrollContent.current?.querySelectorAll<HTMLElement>(
			"[data-chat-scroll-anchor]",
		);
		const anchor = anchors && anchors.length > 0 ? anchors[anchors.length - 1] : null;
		const nextHeight = anchor
			? promptSpacerHeight({
					viewportHeight: node.clientHeight,
					contentHeightWithoutSpacer: node.scrollHeight - pad.offsetHeight,
					anchorOffset:
						anchor.getBoundingClientRect().top - node.getBoundingClientRect().top + node.scrollTop,
					topInset: promptTopInset(node.clientHeight),
				})
			: 0;
		if (Math.abs(pad.offsetHeight - nextHeight) > 0.5) {
			pad.style.height = `${nextHeight}px`;
		}
	}, []);

	const syncScrollLayout = useCallback(() => {
		anchorGeometry.current = null;
		syncPromptSpacer();
		const node = scroller.current;
		if (node && pinnedRef.current) {
			node.scrollTop = node.scrollHeight;
		}
		updateScrollbar();
	}, [syncPromptSpacer, updateScrollbar]);

	useEffect(() => {
		syncScrollLayout();
	}, [pinned, snapshot.latestSequence, groups.length, messageEdit?.turnId, syncScrollLayout]);

	useEffect(() => {
		const content = scrollContent.current;
		const mutations = new MutationObserver(() => { anchorGeometry.current = null; });
		if (content) mutations.observe(content, {
			subtree: true, childList: true, characterData: true, attributes: true,
			attributeFilter: ["class", "style", "hidden", "open", "data-chat-scroll-anchor"],
		});
		contentMutations.current = mutations;
		return () => {
			mutations.disconnect();
			contentMutations.current = null;
			anchorGeometry.current = null;
		};
	}, []);

	useEffect(() => {
		syncScrollLayout();
		if (typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(syncScrollLayout);
		if (scroller.current) observer.observe(scroller.current);
		if (scrollContent.current) observer.observe(scrollContent.current);
		return () => observer.disconnect();
	}, [groups.length, syncScrollLayout]);

	function onScroll() {
		const node = scroller.current;
		if (!node) return;
		const distance = node.scrollHeight - node.scrollTop - node.clientHeight;
		setPinned(distance < 64);
		updateScrollbar();
	}

	function setScrollFromTrack(clientY: number) {
		const node = scroller.current;
		const track = scrollTrack.current;
		if (!node || !track) return;
		const rect = track.getBoundingClientRect();
		const travel = Math.max(1, track.clientHeight - scrollbar.height);
		const top = Math.min(travel, Math.max(0, clientY - rect.top - scrollbar.height / 2));
		node.scrollTop = (top / travel) * Math.max(0, node.scrollHeight - node.clientHeight);
		updateScrollbar();
	}

	function onScrollbarPointerDown(event: ReactPointerEvent<HTMLDivElement>) {
		if (!minimapEnabled) return;
		const track = scrollTrack.current;
		const node = scroller.current;
		if (!track || !node) return;
		const marker = (event.target as HTMLElement).closest<HTMLElement>("[data-chat-scroll-marker]");
		if (marker?.dataset.scrollTarget) {
			node.scrollTop = Number(marker.dataset.scrollTarget);
			onScroll();
			return;
		}
		setScrollFromTrack(event.clientY);
		drag.current = {
			pointerId: event.pointerId,
			startY: event.clientY,
			startScrollTop: node.scrollTop,
		};
		event.currentTarget.setPointerCapture(event.pointerId);
	}

	function onScrollbarPointerMove(event: ReactPointerEvent<HTMLDivElement>) {
		if (!minimapEnabled) return;
		const active = drag.current;
		const node = scroller.current;
		const track = scrollTrack.current;
		if (!node || !track) return;
		if (!active) {
			const pointerY = event.clientY - track.getBoundingClientRect().top;
			let nearest = 0;
			for (let index = 1; index < scrollbar.markers.length; index += 1) {
				if (
					Math.abs(scrollbar.markers[index]!.top - pointerY) <
					Math.abs(scrollbar.markers[nearest]!.top - pointerY)
				) {
					nearest = index;
				}
			}
			if (scrollbar.markers.length > 0) setHoveredMarker(nearest);
			return;
		}
		if (active.pointerId !== event.pointerId) return;
		const travel = Math.max(1, track.clientHeight - scrollbar.height);
		const maxScroll = Math.max(0, node.scrollHeight - node.clientHeight);
		node.scrollTop = active.startScrollTop + ((event.clientY - active.startY) / travel) * maxScroll;
		updateScrollbar();
	}

	function stopScrollbarDrag(event: ReactPointerEvent<HTMLDivElement>) {
		if (drag.current?.pointerId !== event.pointerId) return;
		drag.current = null;
		if (event.currentTarget.hasPointerCapture(event.pointerId)) {
			event.currentTarget.releasePointerCapture(event.pointerId);
		}
	}

	function onScrollbarWheel(event: ReactWheelEvent<HTMLDivElement>) {
		if (!minimapEnabled) return;
		const node = scroller.current;
		if (!node) return;
		event.preventDefault();
		node.scrollTop += event.deltaY;
		onScroll();
	}

	function onScrollbarKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
		if (!minimapEnabled) return;
		const node = scroller.current;
		if (!node) return;
		const maxScroll = Math.max(0, node.scrollHeight - node.clientHeight);
		const page = Math.max(48, node.clientHeight * 0.85);
		const next: Record<string, number> = {
			ArrowUp: node.scrollTop - 48,
			ArrowDown: node.scrollTop + 48,
			PageUp: node.scrollTop - page,
			PageDown: node.scrollTop + page,
			Home: 0,
			End: maxScroll,
		};
		if (!(event.key in next)) return;
		event.preventDefault();
		node.scrollTop = Math.min(maxScroll, Math.max(0, next[event.key]!));
		updateScrollbar();
	}

	if (items.length === 0 && !messageEdit && !turn) {
		return null;
	}

	return (
		<div className="relative min-h-0 flex-1">
			<div
				ref={scroller}
				onScroll={onScroll}
				className="chat-scroll-viewport cursor-chat-timeline h-full min-w-0 select-text overflow-x-hidden overflow-y-auto px-4 pt-5 pb-0"
				role="log"
				aria-live="polite"
				aria-label="Conversation"
			>
				<div ref={scrollContent} className="mx-auto flex w-full min-w-0 max-w-3xl flex-col gap-4.5">
					{hasOlder ? (
						<div className="flex justify-center pb-1">
							<Button
								type="button"
								size="sm"
								variant="ghost"
								disabled={loadingOlder}
								onClick={onLoadOlder}
								className="gap-1.5 text-muted-foreground"
							>
								{loadingOlder ? (
									<Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
								) : null}
								Load earlier messages
							</Button>
						</div>
					) : null}
					{groups.map((group) => {
						const retrySelected = !retryControl?.turnId || retryControl.turnId === group.turnId;
						const retry =
							group.turnId &&
							retryControl &&
							retryableTurns.has(group.turnId) &&
							groupHasHumanPrompt(group)
								? {
										onRetry: () => {
											void Promise.resolve(retryTurn(group.turnId as string)).catch(
												() => undefined,
											);
										},
										pending: retryControl.pending && retrySelected,
										error: retrySelected ? retryControl.error : undefined,
										disabled:
											Boolean(turn) ||
											Boolean(retryControl.pending) ||
											Boolean(newWorkDisabled),
									}
								: undefined;
						return (
							<div
								key={group.key}
								data-chat-scroll-anchor={groupHasHumanPrompt(group) ? "" : undefined}
							>
								<TurnGroup
									group={group}
									sessionId={snapshot.sessionId}
									apiBaseUrl={apiBaseUrl}
									onDecide={decide}
									onResolveInput={resolveInput}
									onRollback={rollback}
									onOpenFiles={onOpenFiles ? openFiles : undefined}
									onOpenFile={onOpenFile ? openFile : undefined}
									retry={retry}
									onEditHumanMessage={canEditHumanMessage ? editHumanMessage : undefined}
									messageEdit={messageEdit}
									onStartMessageEdit={startMessageEdit}
									onUpdateMessageEdit={updateMessageEdit}
									onCancelMessageEdit={cancelMessageEdit}
									onSubmitMessageEdit={submitMessageEdit}
									editPending={editPending}
									editBusy={Boolean(editBusy || newWorkDisabled)}
									editError={editError}
									branchPoints={branchPoints}
									editableTurns={editableTurns}
									newHumanMessageIds={newHumanMessageIds}
									onActivateBranch={canActivateBranch ? activateBranch : undefined}
									activateBranchPending={activateBranchPending}
									activateBranchError={activateBranchError}
									// Only a turn the provider actually accepted can be undone: a turn it
									// never saw holds no history to discard, and the daemon refuses it
									// rather than hiding rows the agent still remembers.
									canRollback={Boolean(onRollback && group.turnId && group.rollbackable)}
									busy={busy}
									queued={Boolean(group.turnId && queued.has(group.turnId))}
								/>
							</div>
						);
					})}
					{turn && !groups.some((group) => group.turnId === turn.id) ? (
						<TurnLiveStatus startedAt={turn.startedAt ?? turn.requestedAt} />
					) : null}
					{messageEdit && !editedMessageVisible ? (
						<div className="flex justify-end" data-chat-scroll-anchor="">
							<HumanMessageEditor
								text={messageEdit.text}
								content={messageEdit.content}
								pending={Boolean(editPending)}
								busy={Boolean(editBusy || newWorkDisabled)}
								error={editError}
								reconstructedContext={messageEdit.reconstructedContext}
								onDraftChange={updateMessageEdit}
								onCancel={cancelMessageEdit}
								onSend={submitMessageEdit}
							/>
						</div>
					) : null}
					{/* Pushes the latest prompt toward the top of the viewport when following,
					    then shrinks as the reply fills the space below it. */}
					<div
						ref={promptSpacer}
						aria-hidden="true"
						className="chat-prompt-spacer"
						data-testid="chat-prompt-spacer"
					/>
				</div>
			</div>

			<div
				ref={scrollTrack}
				role="scrollbar"
				data-testid="chat-conversation-minimap"
				tabIndex={minimapEnabled ? 0 : -1}
				aria-hidden={isInspectorOpen || undefined}
				aria-label="Conversation scrollbar"
				aria-orientation="vertical"
				aria-valuemin={0}
				aria-valuemax={100}
				aria-valuenow={scrollbar.percent}
				onPointerDown={onScrollbarPointerDown}
				onPointerMove={onScrollbarPointerMove}
				onPointerUp={stopScrollbarDrag}
				onPointerCancel={stopScrollbarDrag}
				onWheel={onScrollbarWheel}
				onKeyDown={onScrollbarKeyDown}
				onFocus={() => {
					if (!minimapEnabled || scrollbar.markers.length === 0) return;
					setHoveredMarker(
						Math.min(
							scrollbar.markers.length - 1,
							Math.round((scrollbar.percent / 100) * (scrollbar.markers.length - 1)),
						),
					);
				}}
				onBlur={() => setHoveredMarker(null)}
				onPointerLeave={() => setHoveredMarker(null)}
				className={cn(
					"group/scroll absolute inset-y-3 right-1 z-10 w-6 touch-none rounded-full outline-none transition-opacity focus-visible:ring-1 focus-visible:ring-logo-accent/60",
					minimapEnabled ? "cursor-pointer opacity-100" : "pointer-events-none opacity-0",
				)}
			>
				<div className="absolute inset-0 cursor-grab group-active/scroll:cursor-grabbing">
					{minimapEnabled
						? scrollbar.markers.map((marker, index) => {
								const distance =
									hoveredMarker === null
										? Number.POSITIVE_INFINITY
										: Math.abs(index - hoveredMarker);
								return (
									<span
										key={index}
										data-chat-scroll-marker=""
										data-scroll-target={marker.scrollTop}
										onPointerEnter={() => setHoveredMarker(index)}
										className="chat-scroll-marker-hit"
										style={{ top: marker.top }}
									>
										<span
											aria-hidden="true"
											className={cn(
												"chat-scroll-marker",
												marker.visible && "chat-scroll-marker-visible",
												distance === 0 && "chat-scroll-marker-active",
												distance === 1 && "chat-scroll-marker-adjacent",
												distance === 2 && "chat-scroll-marker-near",
											)}
										/>
									</span>
								);
							})
						: null}
				</div>

				{minimapEnabled &&
				hoveredMarker !== null &&
				scrollbar.markers[hoveredMarker] &&
				previews[hoveredMarker] ? (
					<div
						role="tooltip"
						className="chat-scroll-preview pointer-events-none absolute right-full z-20 mr-3 w-80 rounded-xl border border-border-strong bg-raised px-3.5 py-3 shadow-lg"
						style={{
							top: `clamp(4rem, ${scrollbar.markers[hoveredMarker].top}px, calc(100% - 4rem))`,
						}}
					>
						<strong className="line-clamp-1 text-sm font-medium leading-snug text-foreground">
							{previews[hoveredMarker].title}
						</strong>
						{previews[hoveredMarker].detail ? (
							<p className="mt-1.5 line-clamp-3 text-xs leading-relaxed text-muted-foreground">
								{previews[hoveredMarker].detail}
							</p>
						) : null}
					</div>
				) : null}
			</div>

			{!pinned ? (
				<Button
					type="button"
					size="icon-sm"
					variant="outline"
					aria-label="Jump to latest"
					title="Jump to latest"
					onClick={() => setPinned(true)}
					className="absolute bottom-3 left-1/2 size-12 -translate-x-1/2 rounded-full border-border-strong bg-raised p-0 text-foreground shadow-sm hover:bg-surface dark:bg-raised dark:hover:bg-surface"
				>
					<ArrowDown aria-hidden="true" className="size-5" />
				</Button>
			) : null}
		</div>
	);
}

/**
 * One turn, and the memo boundary that keeps a poll from re-rendering the whole
 * conversation. A turn is the right granularity because it is what changes: while
 * the agent works, one group grows and every other group is finished history.
 */
const TurnGroup = memo(function TurnGroup({
	group,
	sessionId,
	apiBaseUrl,
	onDecide,
	onResolveInput,
	onRollback,
	onOpenFiles,
	onOpenFile,
	onEditHumanMessage,
	messageEdit,
	onStartMessageEdit,
	onUpdateMessageEdit,
	onCancelMessageEdit,
	onSubmitMessageEdit,
	editPending,
	editBusy,
	editError,
	branchPoints,
	editableTurns,
	onActivateBranch,
	activateBranchPending,
	activateBranchError,
	canRollback,
	retry,
	busy,
	queued,
	newHumanMessageIds,
}: {
	group: TimelineGroup;
	sessionId: string;
	apiBaseUrl: string;
	onDecide: (requestId: string, decisionId: string) => void;
	onResolveInput: NonNullable<ChatWorkspaceProps["onResolveInput"]>;
	onRollback: (turnId: string) => void;
	onOpenFiles?: () => void;
	onOpenFile?: (path: string) => void;
	onEditHumanMessage?: (turnId: string, text: string) => Promise<unknown> | void;
	messageEdit?: MessageEditDraft;
	onStartMessageEdit: (message: ConversationMessage) => void;
	onUpdateMessageEdit: (text: string) => void;
	onCancelMessageEdit: () => void;
	onSubmitMessageEdit: (text: string) => Promise<void>;
	editPending?: boolean;
	editBusy?: boolean;
	editError?: string;
	branchPoints: Map<string, ConversationBranchPoint>;
	editableTurns: Set<string>;
	onActivateBranch?: (branchId: string) => Promise<unknown> | void;
	activateBranchPending?: boolean;
	activateBranchError?: string;
	/** The daemon would accept a rollback of this turn, so offer the affordance. */
	canRollback: boolean;
	/** Present only when this failed turn is eligible for a new attempt. */
	retry?: TurnOutcomeRetryControl;
	busy?: boolean;
	/** This turn was recorded but not sent, so its message can say so. */
	queued: boolean;
	newHumanMessageIds: ReadonlySet<string>;
}) {
	const runs = useMemo(
		() =>
			runsOf(
				group.liveProviderFailure
					? group.items.filter((item) => item.id !== group.liveProviderFailure?.id)
					: group.items,
			),
		[group.items, group.liveProviderFailure],
	);
	const copyableMessageId = group.outcome
		? [...group.items]
				.reverse()
				.find((item) => item.kind === "message" && item.role === "assistant")?.id
		: undefined;
	return (
		<div className="flex min-w-0 flex-col gap-2.5">
			{runs.map((run) =>
				run.kind === "activities" ? (
					<ActivityRun
						key={run.key}
						activities={run.items.filter(
							(item): item is ConversationActivity => item.kind === "activity",
						)}
					/>
				) : (
					<TimelineItem
						key={run.key}
						item={run.items[0]!}
						sessionId={sessionId}
						apiBaseUrl={apiBaseUrl}
						onDecide={onDecide}
						onResolveInput={onResolveInput}
						onEditHumanMessage={onEditHumanMessage}
						messageEdit={messageEdit}
						onStartMessageEdit={onStartMessageEdit}
						onUpdateMessageEdit={onUpdateMessageEdit}
						onCancelMessageEdit={onCancelMessageEdit}
						onSubmitMessageEdit={onSubmitMessageEdit}
						editPending={editPending}
						editBusy={editBusy}
						editError={editError}
						branchPoints={branchPoints}
						editableTurns={editableTurns}
						onActivateBranch={onActivateBranch}
						activateBranchPending={activateBranchPending}
						activateBranchError={activateBranchError}
						busy={busy}
						queued={queued}
						newHumanMessageIds={newHumanMessageIds}
						showCopy={run.items[0]?.id === copyableMessageId}
						onRollback={
							canRollback && run.items[0]?.id === copyableMessageId
								? () => onRollback(group.turnId as string)
								: undefined
						}
						durationMs={
							run.items[0]?.id === copyableMessageId ? group.outcome?.durationMs : undefined
						}
					/>
				),
			)}
			{/* Both of these are current state of the turn rather than steps in it, which
			    is why they sit at its end: a checklist that ticks itself off and a file
			    list that grows both change while the reader watches, and at the end of a
			    turn neither pushes anything the reader is already looking at. */}
			{group.plan ? <TurnPlan plan={group.plan} live={group.live} /> : null}
			{/* Above the outcome divider: the changed files are part of what the turn
			    did, and belong inside it rather than after it closes. */}
			{group.diff ? (
				<TurnChangedFiles
					diff={group.diff}
					live={group.live}
					items={group.items}
					onReview={onOpenFiles}
					onOpenFile={onOpenFile}
				/>
			) : null}
			{group.live ? (
				<TurnLiveStatus
					startedAt={group.liveStartedAt}
					blocked={group.blocked}
					providerFailure={group.liveProviderFailure}
				/>
			) : null}
			{/* No assistant prose to hang the undo / duration on — still offer them
			    before the outcome divider so a tool-only turn is not stuck without a
			    way back or a record of how long it took. */}
			{!copyableMessageId &&
			(canRollback || (group.outcome?.durationMs !== undefined && group.outcome.durationMs > 0)) ? (
				<div className="mt-2 flex h-[18px] items-center gap-0.5">
					{canRollback ? (
						<button
							type="button"
							onClick={() => onRollback(group.turnId as string)}
							aria-label="Roll back to here"
							title="Roll back to here"
							className="flex items-center rounded px-1.5 py-0.5 text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground"
						>
							<Undo2 aria-hidden="true" className="size-3" />
						</button>
					) : null}
					{group.outcome?.durationMs !== undefined && group.outcome.durationMs > 0 ? (
						<TurnDuration durationMs={group.outcome.durationMs} />
					) : null}
				</div>
			) : null}
			{group.outcome && group.outcome.state !== "completed" ? (
				<TurnOutcome
					state={group.outcome.state}
					error={group.outcome.error}
					retry={group.outcome.state === "failed" ? retry : undefined}
				/>
			) : null}
		</div>
	);
});

function TurnLiveStatus({
	startedAt,
	blocked,
	providerFailure,
}: {
	startedAt?: string;
	blocked?: boolean;
	providerFailure?: ConversationActivity;
}) {
	const [elapsed, setElapsed] = useState(() => elapsedSince(startedAt));

	useEffect(() => {
		if (blocked) return;
		const timer = setInterval(() => setElapsed(elapsedSince(startedAt)), 1000);
		return () => clearInterval(timer);
	}, [blocked, startedAt]);

	if (blocked) {
		return (
			<span role="alert" className="sr-only">
				The agent is waiting for your decision.
			</span>
		);
	}
	if (providerFailure) {
		return (
			<div
				role="status"
				aria-live="polite"
				className="flex min-h-8 items-start gap-2 px-1 py-1"
				data-testid="live-turn-status"
			>
				<TriangleAlert
					aria-hidden="true"
					className="mt-0.5 size-3.5 shrink-0 text-warning"
				/>
				<span className="flex min-w-0 flex-col gap-0.5">
					<strong className="text-xs font-medium text-warning">
						{providerFailure.summary}
					</strong>
					{providerFailure.detail?.text ? (
						<span className="text-[11px] leading-snug text-muted-foreground">
							{providerFailure.detail.text}
						</span>
					) : null}
				</span>
			</div>
		);
	}

	return (
		<div className="flex min-h-6 items-center gap-2 px-1 py-0.5" data-testid="live-turn-status">
			<Loader2
				aria-hidden="true"
				className="size-3 shrink-0 animate-spin text-status-working opacity-100"
			/>
			<span role="status" aria-live="polite" className="text-xs font-medium text-muted-foreground">
				Working for {elapsed}
			</span>
		</div>
	);
}

function elapsedSince(iso?: string): string {
	if (!iso) return "0s";
	const start = new Date(iso).getTime();
	if (Number.isNaN(start)) return "0s";
	const seconds = Math.max(0, Math.round((Date.now() - start) / 1000));
	if (seconds < 60) return `${seconds}s`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m`;
	return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

function TimelineItem({
	item,
	sessionId,
	apiBaseUrl,
	onDecide,
	onResolveInput,
	onEditHumanMessage,
	messageEdit,
	onStartMessageEdit,
	onUpdateMessageEdit,
	onCancelMessageEdit,
	onSubmitMessageEdit,
	editPending,
	editBusy,
	editError,
	branchPoints,
	editableTurns,
	onActivateBranch,
	activateBranchPending,
	activateBranchError,
	busy,
	queued,
	newHumanMessageIds,
	showCopy,
	onRollback,
	durationMs,
}: {
	item: ConversationItem;
	sessionId: string;
	apiBaseUrl: string;
	onDecide?: (requestId: string, decisionId: string) => void;
	onResolveInput?: ChatWorkspaceProps["onResolveInput"];
	onEditHumanMessage?: (turnId: string, text: string) => Promise<unknown> | void;
	messageEdit?: MessageEditDraft;
	onStartMessageEdit: (message: ConversationMessage) => void;
	onUpdateMessageEdit: (text: string) => void;
	onCancelMessageEdit: () => void;
	onSubmitMessageEdit: (text: string) => Promise<void>;
	editPending?: boolean;
	editBusy?: boolean;
	editError?: string;
	branchPoints: Map<string, ConversationBranchPoint>;
	editableTurns: Set<string>;
	onActivateBranch?: (branchId: string) => Promise<unknown> | void;
	activateBranchPending?: boolean;
	activateBranchError?: string;
	busy?: boolean;
	/**
	 * The enclosing turn was recorded but not yet sent, so a waiting message can say
	 * so. A group is one turn, so this holds for every item in it.
	 */
	queued?: boolean;
	newHumanMessageIds: ReadonlySet<string>;
	/** This is the final assistant response of a turn that has finished. */
	showCopy?: boolean;
	/** Undo this finished turn from the answer that owns its copy action. */
	onRollback?: () => void;
	/** Finished-turn duration; shown next to rollback on the final answer. */
	durationMs?: number;
	/** This message is the live edge of its turn, rather than an earlier fragment
	 * followed by tool activity. */
}) {
	if (item.kind === "message") {
		if (item.role === "assistant") {
			return (
				<AssistantMessage
					message={item}
					showCopy={showCopy}
					onRollback={onRollback}
					durationMs={durationMs}
				/>
			);
		}
		// A user-role message that did not come from this human is an automation or
		// worker relay, and is attributed differently.
		if (item.origin === "human") {
			const editAvailable = Boolean(
				item.editAvailable && item.turnId && editableTurns.has(item.turnId) && onEditHumanMessage,
			);
			const editing = Boolean(item.turnId && messageEdit?.turnId === item.turnId);
			return (
				<HumanMessage
					message={item}
					sessionId={sessionId}
					apiBaseUrl={apiBaseUrl}
					queued={queued}
					animateIn={newHumanMessageIds.has(item.id)}
					onEdit={editAvailable ? (_turnID, text) => onSubmitMessageEdit(text) : undefined}
					editing={editing}
					editText={editing ? messageEdit?.text : undefined}
					editReconstructedContext={editing && messageEdit?.reconstructedContext}
					onEditStart={editAvailable ? () => onStartMessageEdit(item) : undefined}
					onEditDraftChange={onUpdateMessageEdit}
					onEditCancel={onCancelMessageEdit}
					editPending={editPending}
					editBusy={editBusy}
					editError={editError}
					branchPoint={item.turnId ? branchPoints.get(item.turnId) : undefined}
					onActivateBranch={onActivateBranch}
					activateBranchPending={activateBranchPending}
					activateBranchError={activateBranchError}
				/>
			);
		}
		return <OriginMessage message={item} />;
	}
	if (item.activityKind === "approval") {
		// Pending approval owns the composer. Keeping it in the grouping model still
		// marks the live turn as blocked, while omitting the timeline card itself.
		if (item.status === "pending") return null;
		return <ApprovalCard activity={item} onDecide={onDecide} busy={busy} />;
	}
	if (item.activityKind === "user_input") {
		return <ElicitationCard activity={item} onResolve={onResolveInput} />;
	}
	if (isCompaction(item)) {
		return <CompactionMarker activity={item} />;
	}
	// Read by the event, not the kind: a steer is stored as a `system` activity
	// because that is AO's only durable write that can attach to a turn in flight,
	// but it is the user speaking and the timeline shows it that way.
	if (isSteer(item)) {
		return <SteerMessage activity={item} sessionId={sessionId} apiBaseUrl={apiBaseUrl} />;
	}
	// A plan whose turn AO never correlated — one from before this controller
	// started. The turn-level checklist cannot show it, so the row carries it.
	if (item.activityKind === "plan") {
		const plan = activityPlan(item);
		return plan ? <TurnPlan plan={plan} /> : <ActivityRow activity={item} />;
	}
	return <ActivityRow activity={item} />;
}

/* -------------------------------------------------------------------------- */
/* identity                                                                    */
/* -------------------------------------------------------------------------- */

const itemKey = (item: ConversationItem): string => item.id;
const groupKey = (group: TimelineGroup): string => group.key;

/**
 * Whether two groups say the same thing.
 *
 * A group carries more than its items: a turn's plan and its changed files are
 * current state that the daemon overwrites, and both move while the turn's items
 * stay exactly as they were. Comparing only the items would keep the previous
 * group object — and with it the previous plan — so a checklist would stop ticking
 * until something else in the turn happened to change.
 */
function sameGroup(a: TimelineGroup, b: TimelineGroup): boolean {
	return (
		a.anchor === b.anchor &&
		a.turnId === b.turnId &&
		a.live === b.live &&
		a.rollbackable === b.rollbackable &&
		sameContent(a.outcome, b.outcome) &&
		sameContent(a.diff, b.diff) &&
		sameContent(a.plan, b.plan) &&
		a.items.length === b.items.length &&
		// The items are already identity-stable by the time a group is compared, so a
		// reference check here is exact and avoids walking their contents twice.
		a.items.every((item, index) => item === b.items[index])
	);
}

/**
 * A callback whose identity survives its caller re-rendering.
 *
 * `useConversationCommands` returns fresh arrows every render and the preview
 * harness passes literals, so without this every memo boundary below would be
 * invalidated by the one prop that never meaningfully changes.
 */
function useStableCallback<Args extends unknown[], Result>(
	fn: ((...args: Args) => Result) | undefined,
): (...args: Args) => Result | undefined {
	const latest = useRef(fn);
	useEffect(() => {
		latest.current = fn;
	});
	// Only ever called from an event handler, which runs after the commit that
	// updated the ref — there is no render-phase caller to read a stale closure.
	return useCallback((...args: Args) => latest.current?.(...args), []);
}

/** Retain an unchanged JSON-shaped value across snapshot refreshes. */
function useStableValue<T>(value: T): T {
	const previous = useRef(value);
	if (!sameContent(previous.current, value)) previous.current = value;
	return previous.current;
}

type TimelineGroup = {
	key: string;
	turnId?: string;
	/** Where this group sits in the timeline: the lowest sequence it contains. */
	anchor: number;
	items: ConversationItem[];
	outcome?: {
		state: "completed" | "recovered" | "interrupted" | "failed";
		durationMs?: number;
		error?: string;
	};
	/** What the turn changed on disk, when the daemon reported anything. */
	diff?: TurnDiff;
	/** The agent's plan for this turn, when it made one. */
	plan?: ConversationPlan;
	/** The turn is still running, so its diff and plan can still change. */
	live?: boolean;
	liveStartedAt?: string;
	blocked?: boolean;
	/** A live provider failure replaces the generic Working label until the turn settles. */
	liveProviderFailure?: ConversationActivity;
	/** The provider accepted this turn, so there is history it can be asked to drop. */
	rollbackable?: boolean;
};

type GroupPreview = { title: string; detail?: string };

/** A turn-sized, plain-text preview for the minimap hover card. */
function groupPreview(group: TimelineGroup): GroupPreview {
	const userMessage = group.items.find(
		(item) => item.kind === "message" && item.role === "user" && item.origin === "human",
	);
	const assistantMessage = [...group.items]
		.reverse()
		.find(
			(item) => item.kind === "message" && item.role === "assistant" && item.text.trim() !== "",
		);
	const firstActivity = group.items.find(
		(item): item is ConversationActivity => item.kind === "activity",
	);
	const title = previewText(
		userMessage?.kind === "message"
			? userMessage.text
			: firstActivity?.summary || "Conversation update",
		120,
	);
	const detailSource =
		assistantMessage?.kind === "message"
			? assistantMessage.text
			: firstActivity?.detail?.text || firstActivity?.detail?.output || firstActivity?.summary;
	const detail = detailSource ? previewText(detailSource, 240) : undefined;
	return { title, detail: detail && detail !== title ? detail : undefined };
}

function groupHasHumanPrompt(group: TimelineGroup): boolean {
	return group.items.some(
		(item) => item.kind === "message" && item.role === "user" && item.origin === "human",
	);
}

// Retry correlation is daemon-owned rather than inferred from repeated text.
// Match it against turn ids in this snapshot before consuming an affordance.
function retrySourceTurnIds(snapshot: ConversationSnapshot): Set<string> {
	const turnIds = new Set(snapshot.turns.map((turn) => turn.id));
	const sources = new Set<string>();
	for (const turn of snapshot.turns) {
		if (turn.hasRetryAttempt) sources.add(turn.id);
		const source = turn.retryOfTurnId;
		if (source && turnIds.has(source)) sources.add(source);
	}
	return sources;
}

/** How tall the trailing spacer must be to park `anchorOffset` near the top when scrolled to the end. */
export function promptSpacerHeight({
	viewportHeight,
	contentHeightWithoutSpacer,
	anchorOffset,
	topInset = 12,
}: {
	viewportHeight: number;
	contentHeightWithoutSpacer: number;
	anchorOffset: number;
	topInset?: number;
}): number {
	const afterAnchor = Math.max(0, contentHeightWithoutSpacer - anchorOffset);
	return Math.max(0, Math.round(viewportHeight - topInset - afterAnchor));
}

/**
 * Leave a band of prior chat above the latest prompt (Cursor-style), not flush to
 * the top edge. Scales with the timeline viewport, with a floor so short panes
 * still keep a peek of history.
 */
export function promptTopInset(viewportHeight: number): number {
	return Math.max(80, Math.round(viewportHeight * 0.2));
}

function previewText(value: string, limit: number): string {
	const plain = value
		.replace(/```[\s\S]*?```/g, " code sample ")
		.replace(/\[([^\]]+)]\([^)]*\)/g, "$1")
		.replace(/[*_`#>~]+/g, " ")
		.replace(/\s+/g, " ")
		.trim();
	return plain.length > limit ? `${plain.slice(0, limit - 1).trimEnd()}…` : plain;
}

/**
 * Group items by the turn that produced them, so a completed turn can be closed
 * off with its outcome.
 *
 * A turn is one block, even when its items are not contiguous in sequence. That
 * matters because of the send queue: a message typed mid-turn is recorded
 * immediately, so its sequence lands before the answers to everything ahead of
 * it. Reading strictly by sequence would stack every queued question at the top
 * and every answer below, separating each question from its own reply.
 *
 * Sequence still decides order — a turn takes the position of its first item, and
 * items inside a turn stay in sequence order. Nothing is re-derived: this is the
 * daemon's ordering, grouped.
 *
 * Items with no turn (an automation relay that arrived between turns) form their
 * own group and keep their sequence position.
 */
function groupByTurn(snapshot: ConversationSnapshot): TimelineGroup[] {
	const byTurn = new Map(snapshot.turns.map((turn) => [turn.id, turn]));
	const groups: TimelineGroup[] = [];
	const groupForTurn = new Map<string, TimelineGroup>();

	for (const item of snapshot.items) {
		if (item.turnId === undefined) {
			// Consecutive turn-less items share one group rather than getting one each.
			// A provider can run a turn AO never dispatched — a compaction, or a turn
			// resumed inside the provider's own history — and every item it emits then
			// correlates to no AO turn. One group per item made `runsOf` see no two
			// adjacent activities, so a wall of tool calls stopped collapsing and the
			// conversation turned back into a log. Grouping must not depend on
			// correlation succeeding.
			const last = groups.at(-1);
			if (last && last.turnId === undefined) {
				last.items.push(item);
				continue;
			}
			groups.push({
				key: `loose-${item.sequence}`,
				anchor: item.sequence,
				items: [item],
			});
			continue;
		}
		const existing = groupForTurn.get(item.turnId);
		if (existing) {
			existing.items.push(item);
			continue;
		}
		const group: TimelineGroup = {
			key: `${item.turnId}-${item.sequence}`,
			turnId: item.turnId,
			anchor: item.sequence,
			items: [item],
		};
		groupForTurn.set(item.turnId, group);
		groups.push(group);
	}

	groups.sort((a, b) => a.anchor - b.anchor);

	for (const group of groups) {
		if (!group.turnId) continue;
		const turn = byTurn.get(group.turnId);
		if (!turn) continue;
		// The diff and the plan are attached whether or not the turn has finished: a
		// running turn's changed-file list growing, and its checklist ticking itself
		// off, are the useful parts.
		group.diff = turn.diff;
		group.plan = turn.plan?.steps.length ? turn.plan : undefined;
		group.live = turn.state === "running";
		group.liveStartedAt = turn.startedAt ?? turn.requestedAt;
		group.blocked = group.items.some(
			(item) =>
				item.kind === "activity" &&
				(item.activityKind === "approval" || item.activityKind === "user_input") &&
				item.status === "pending",
		);
		group.liveProviderFailure = [...group.items]
			.reverse()
			.find(
				(item): item is ConversationActivity =>
					item.kind === "activity" &&
					item.status === "running" &&
					item.detail?.event === "provider.failure",
			);
		if (turn.state === "running" || turn.state === "queued" || turn.state === "cancelled") continue;
		group.rollbackable = Boolean(turn.providerTurnId);
		group.outcome = {
			state: turn.state,
			durationMs:
				turn.completedAt && turn.startedAt
					? new Date(turn.completedAt).getTime() - new Date(turn.startedAt).getTime()
					: undefined,
			error: turn.errorMessage,
		};
	}

	return groups;
}
