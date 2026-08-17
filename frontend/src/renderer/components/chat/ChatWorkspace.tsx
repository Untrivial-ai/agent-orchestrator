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
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
	type CSSProperties,
	type KeyboardEvent as ReactKeyboardEvent,
	type PointerEvent as ReactPointerEvent,
	type ReactNode,
	type WheelEvent as ReactWheelEvent,
} from "react";
import {
	Archive,
	ArrowDown,
	CornerDownRight,
	GitBranch,
	Loader2,
	MessageSquare,
	TriangleAlert,
	Undo2,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { sameContent, useStableList } from "../../lib/stable-list";
import { getApiBaseUrl, subscribeApiBaseUrl } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import {
	TERMINAL_FONT_SIZE_DEFAULT,
	TERMINAL_FONT_SIZE_MAX,
	TERMINAL_FONT_SIZE_MIN,
} from "../../lib/design-tokens";
import { isLinuxPlatform, isMacPlatform } from "../../lib/platform";
import { handleTerminalTabListKeyDown } from "../../lib/terminal-tabs";
import type { ShellTerminal } from "../../hooks/useShellTerminals";
import { useUiStore } from "../../stores/ui-store";
import type { TerminalTarget } from "../../types/terminal";
import type { SessionKind, WorkspaceSession } from "../../types/workspace";
import { AgentAvatar } from "../AgentAvatar";
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
	TurnOutcome,
} from "./ChatTimelineItems";
import { HumanMessageEditor } from "./HumanMessageEditor";
import { ChatLinkProvider } from "./ChatMarkdown";
import { ChatComposer } from "./ChatComposer";
import { ActivityRun } from "./ActivityRun";
import { TurnPlan } from "./TurnPlan";
import { TurnSettingsBar } from "./TurnSettingsBar";
import { ElicitationCard } from "./ElicitationCard";
import {
	McpServerBanner,
	ReauthBanner,
	ThreadStateBanner,
} from "./ChatStatusBanners";
import {
	activeTurn,
	activityPlan,
	brokenMcpServers,
	can,
	isCompaction,
	isSteer,
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

const CHAT_FONT_SIZE_DEFAULT = 12;

// Reviewer panes share the terminal font-size preference with CenterPane, so a
// reviewer opened inside the Chat surface matches a reviewer opened in TUI mode.
const terminalFontSizeStorageKey = "ao.terminal.fontSize";
const WHEEL_ZOOM_THRESHOLD = 80;
const WHEEL_ZOOM_RESET_MS = 250;

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
};

export interface ChatWorkspaceProps {
	snapshot: ConversationSnapshot;
	/** The session title from the sidebar (matches what users see in the left sidebar) */
	sessionTitle?: string;
	/** The AO role using this shared conversation surface. */
	sessionRole?: SessionKind;
	/** Session-level actions owned above the conversation surface. */
	headerActions?: ReactNode;
	/** Suppress a transient stopped snapshot while a mode handoff installs Chat. */
	controllerTransitioning?: boolean;
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
	/** The in-place agent-switch control. Owned by the surface (which owns query
	    wiring) so this pure view stays renderable without query providers. */
	switchAgentControl?: ReactNode;
	/** Receives the body element the agent-switch dialog anchors to. */
	switchDialogContainer?: (node: HTMLDivElement | null) => void;
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
	onSteer?: (text: string) => Promise<unknown>;
	/** Promote one already queued turn into the running turn. */
	onPromoteQueuedTurn?: (turnId: string) => Promise<unknown>;
	steerPending?: boolean;
	/** Why the last steer was refused, from the daemon's typed answer. */
	steerRefusal?: string;
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
	controllerTransitioning,
	reviewerTerminal,
	onOpenReviewerTerminal,
	session,
	reviewerTarget,
	onSelectChat,
	shellTerminals,
	shellTarget,
	onSelectShellTerminal,
	onCloseShellTerminal,
	onRenameShellTerminal,
	switchAgentControl,
	switchDialogContainer,
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
	onPromoteQueuedTurn,
	steerPending,
	steerRefusal,
	onReloadMcpServers,
	reloadingMcpServers,
	mcpReloadError,
}: ChatWorkspaceProps) {
	const turn = activeTurn(snapshot);
	// Selection is durable UI state; availability only controls whether the tab is
	// offered. Keeping these separate preserves a selected reviewer while an active
	// session temporarily becomes terminated and later returns.
	const reviewerActive = Boolean(reviewerTarget && session);
	const shellActive = Boolean(shellTarget && session);
	const queuedMessages = useMemo(() => {
		const messagesByTurn = new Map(
			snapshot.items
				.filter(
					(item): item is ConversationMessage =>
						item.kind === "message" && item.role === "user" && item.origin === "human" && Boolean(item.turnId),
				)
				.map((message) => [message.turnId as string, message]),
		);
		return snapshot.turns.flatMap((queuedTurn) => {
			if (queuedTurn.state !== "queued") return [];
			const message = messagesByTurn.get(queuedTurn.id);
			return message ? [{ turnId: queuedTurn.id, message }] : [];
		});
	}, [snapshot.items, snapshot.turns]);
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
	const selectAdjacentTab = useCallback((direction: -1 | 1) => {
		const tabs = [
			{ kind: "chat" as const },
			...(reviewerTerminal ? [{ kind: "reviewer" as const }] : []),
			...(shellTerminals ?? []).map((shell) => ({ kind: "shell" as const, handleId: shell.handleId })),
		];
		if (tabs.length <= 1) return;
		const activeIndex = shellActive
			? tabs.findIndex((tab) => tab.kind === "shell" && tab.handleId === shellTarget?.handleId)
			: reviewerActive
				? tabs.findIndex((tab) => tab.kind === "reviewer")
				: 0;
		const currentIndex = activeIndex >= 0 ? activeIndex : 0;
		const next = tabs[(currentIndex + direction + tabs.length) % tabs.length];
		if (!next) return;
		if (next.kind === "chat") {
			onSelectChat?.();
			return;
		}
		if (next.kind === "reviewer") {
			if (reviewerTerminal) onOpenReviewerTerminal?.(reviewerTerminal);
			return;
		}
		onSelectShellTerminal?.(next.handleId);
	}, [
		onOpenReviewerTerminal,
		onSelectChat,
		onSelectShellTerminal,
		reviewerActive,
		reviewerTerminal,
		shellActive,
		shellTarget,
		shellTerminals,
	]);
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
		aoBridge.app.setCloseShellTerminalShortcutEnabled(
			Boolean(shellTarget && onCloseShellTerminal),
		);
		return () => aoBridge.app.setCloseShellTerminalShortcutEnabled(false);
	}, [onCloseShellTerminal, shellTarget]);

	// Offered only while the agent is idle. The daemon refuses a rollback mid-turn,
	// and a control that exists to be refused is worse than one that waits.
	const rollbackTarget = onRollback && !turn ? (id: string) => setConfirming(id) : undefined;
	const discarded = snapshot.turns.filter((t) => t.rolledBack).length;

	const brokenServers = useMemo(() => brokenMcpServers(snapshot), [snapshot]);
	const editHumanMessage = can(snapshot, "fork") ? onEditMessage : undefined;

	return (
		<section
			ref={surfaceRef}
			aria-label="Chat"
			className="cursor-chat-surface flex h-full min-h-0 flex-col [font-size:var(--chat-font-size)]"
			data-session-mode={snapshot.mode}
			data-session-role={sessionRole}
			style={{ "--chat-font-size": `${CHAT_FONT_SIZE_DEFAULT}px` } as CSSProperties}
		>
			<ChatHeader
				snapshot={snapshot}
				sessionTitle={sessionTitle}
				reviewerTerminal={reviewerTerminal}
				onOpenReviewerTerminal={onOpenReviewerTerminal}
				reviewerActive={reviewerActive}
				onSelectChat={onSelectChat}
				shellTerminals={shellTerminals}
				shellActiveHandleId={shellActive ? shellTarget?.handleId : undefined}
				onSelectShellTerminal={onSelectShellTerminal}
				onCloseShellTerminal={onCloseShellTerminal}
				onRenameShellTerminal={onRenameShellTerminal}
				onTabsKeyDown={handleChatTabsKeyDown}
				switchAgentControl={switchAgentControl}
				headerActions={headerActions}
				inline={isFullscreen}
				topbarBounds={topbarBounds}
			/>
			{/* The body host anchors the agent-switch dialog and holds whichever tab is
			    active: the reviewer pane, a shell pane, or the chat timeline. The
			    container ref is handed up so the surface (not this pure view) owns the
			    dialog's state. */}
			<div
				className="relative flex min-h-0 flex-1 flex-col"
				ref={switchDialogContainer}
			>
			{reviewerTarget && session ? (
				<div
					aria-label="Reviewer terminal"
					className="relative min-h-0 flex-1"
					data-testid="chat-reviewer-panel"
					onWheelCapture={handleWheelZoom}
					role="tabpanel"
				>
					<div
						className="h-full min-h-0 pl-2"
						data-testid="chat-reviewer-terminal"
					>
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
					<div className="h-full min-h-0 pl-2" data-testid="chat-shell-terminal">
						<TerminalPane
							daemonReady={Boolean(daemonReady)}
							fontSize={terminalFontSize}
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
				inert={reviewerActive || shellActive ? true : undefined}
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
					onResume={onResumeAgent}
					resuming={resumingAgent}
					resumeError={resumeError}
					onOpenShell={onOpenShell}
					openingShell={openingShell}
					shellError={shellError}
				/>
				{snapshot.threadState ? <ThreadStateBanner threadState={snapshot.threadState} /> : null}
				<McpServerBanner
					servers={brokenServers}
					onReload={onReloadMcpServers}
					reloading={reloadingMcpServers}
					turnInFlight={Boolean(turn)}
					error={mcpReloadError}
				/>
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
						onEditHumanMessage={editHumanMessage}
						editPending={editMessagePending}
						editBusy={Boolean(turn)}
						editError={editMessageError}
						onActivateBranch={onActivateBranch}
						activateBranchPending={activateBranchPending}
						activateBranchError={activateBranchError}
					/>
				</ChatLinkProvider>

				<div className="cursor-chat-composer-dock shrink-0 px-4 pb-3 pt-2">
					<div className="mx-auto flex w-full max-w-3xl flex-col gap-2">
						{discarded > 0 ? <RolledBackNotice count={discarded} /> : null}
						{snapshot.branchedFromEarlierMessage ? (
							<p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
								<GitBranch aria-hidden="true" className="size-3 shrink-0" />
								Conversation branched; worktree files were left unchanged.
							</p>
						) : null}
						{turn?.state === "running" && queuedMessages.length > 0 ? (
							<QueuedMessageDock
								messages={queuedMessages}
								onSteer={onPromoteQueuedTurn}
							/>
						) : null}
						<ChatComposer
							attachedTop={turn?.state === "running" && queuedMessages.length > 0}
							onSend={(text, attachments) => onSend?.(text, attachments)}
							onInterrupt={turn ? onInterrupt : undefined}
							commandError={commandError}
							settings={
								onChooseSettings || onChooseConfigOption ? (
									<TurnSettingsBar
										models={models ?? []}
										settings={snapshot.settings}
										reroute={snapshot.modelReroute}
										onChange={onChooseSettings}
										configOptions={configOptions ?? []}
										onChangeConfigOption={onChooseConfigOption}
										configPending={configOptionPending}
										error={configOptionError}
										disabled={
											snapshot.controller.state === "stopped" || configOptionPending
										}
									/>
								) : null
							}
							footerAction={
								<CompactButton
									onCompact={onCompact}
									compacting={compacting}
									unavailable={compactUnavailable}
									turnInFlight={Boolean(turn)}
									compactedAt={snapshot.compactedAt}
								/>
							}
							busy={busy}
							willQueue={Boolean(turn)}
							disabled={snapshot.controller.state === "stopped"}
							skills={skills}
							filePaths={filePaths}
							filePathsTruncated={filePathsTruncated}
							onStageAttachments={onStageAttachments}
							nativeImages={nativeImages}
							// Steering is only meaningful into a turn that is running. A queued turn
							// has not reached the provider, so there is nothing to steer.
							onSteer={onSteer}
							canSteer={Boolean(onSteer) && turn?.state === "running"}
							steerPending={steerPending}
							steerRefusal={steerRefusal}
							onCompact={onCompact}
							compacting={compacting}
							compactUnavailable={compactUnavailable}
							compactBlocked={Boolean(turn)}
						/>
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
							about anything you or it said later. Files it already changed in the worktree are
							left exactly as they are; only the conversation is rolled back. This cannot be
							undone.
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

function CompactButton({
	onCompact,
	compacting,
	unavailable,
	turnInFlight,
	compactedAt,
}: {
	onCompact?: () => void;
	compacting?: boolean;
	unavailable?: string;
	turnInFlight?: boolean;
	compactedAt?: string;
}) {
	if (!onCompact) return null;
	if (unavailable === "This agent cannot compact its history") {
		return <span className="text-[11px] text-muted-foreground">{unavailable}</span>;
	}
	const title = turnInFlight
		? "Finish or stop the current turn before compacting"
		: compactedAt
			? `Summarize earlier history to reclaim context. Last compacted ${new Date(compactedAt).toLocaleString()}.`
			: "Summarize earlier history to reclaim context";
	return (
		<Button
			type="button"
			size="sm"
			variant="ghost"
			onClick={onCompact}
			disabled={compacting || turnInFlight}
			title={title}
			aria-label="Compact conversation history"
			className="h-5 gap-1 px-1.5 text-[11px]"
		>
			{compacting ? <Loader2 aria-hidden="true" className="size-3 animate-spin" /> : <Archive aria-hidden="true" className="size-3" />}
			{compacting ? "Compacting…" : "Compact"}
		</Button>
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
				? { kind: "activities", key: `run-${item.sequence}`, items: [item] }
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
	reviewerTerminal,
	onOpenReviewerTerminal,
	reviewerActive,
	onSelectChat,
	shellTerminals,
	shellActiveHandleId,
	onSelectShellTerminal,
	onCloseShellTerminal,
	onRenameShellTerminal,
	onTabsKeyDown,
	switchAgentControl,
	headerActions,
	inline,
	topbarBounds,
}: {
	snapshot: ConversationSnapshot;
	sessionTitle?: string;
	reviewerTerminal?: { handleId: string; harness: string };
	onOpenReviewerTerminal?: (target: { handleId: string; harness: string }) => void;
	/** The reviewer tab is selected; the chat tab is the clickable alternative. */
	reviewerActive?: boolean;
	/** Return the tab strip to the chat tab. */
	onSelectChat?: () => void;
	/** This session's standalone shells, as tabs after the reviewer. */
	shellTerminals?: ShellTerminal[];
	/** The selected shell tab, if any. */
	shellActiveHandleId?: string;
	onSelectShellTerminal?: (handleId: string) => void;
	onCloseShellTerminal?: (handleId: string) => void;
	onRenameShellTerminal?: (handleId: string, title: string) => void;
	onTabsKeyDown?: (event: ReactKeyboardEvent<HTMLDivElement>) => void;
	/** The in-place agent-switch control, same entry point as the terminal pane. */
	switchAgentControl?: ReactNode;
	headerActions?: ReactNode;
	/** Fullscreen content cannot see the normal topbar portal outside its subtree. */
	inline?: boolean;
	topbarBounds: TopbarBounds;
}) {
	const label = sessionTitle || snapshot.title || snapshot.sessionId;
	// The chat tab is "selected" only when neither terminal pane is the body.
	const timelineActive = !reviewerActive && !shellActiveHandleId;
	// Match CenterPane: when the sidebar is off-canvas, the fixed TitlebarNav
	// cluster sits over the session tab strip. Terminal already reserves that
	// space; chat must too or the back/forward buttons land on the tab label.
	const isSidebarOpen = useUiStore((state) => state.isSidebarOpen);
	const header = (
		<header className="flex h-inspector-tabs w-full shrink-0 items-stretch bg-sidebar">
			<div className="session-topbar-surface flex min-w-0 flex-1" data-testid="session-workspace-topbar">
				<div
					className={cn(
						"flex min-w-0 shrink items-center pr-3",
						!isSidebarOpen && isMac && "session-topbar-titlebar-clearance-mac",
						!isSidebarOpen && isLinux && "session-topbar-titlebar-clearance-linux",
					)}
					data-testid="session-terminal-region"
					style={{ width: topbarBounds.width > 0 ? topbarBounds.width : "100%" }}
				>
					<div
						aria-label="Chat tabs"
						className="scrollbar-none flex h-full min-w-flex-min flex-1 items-center overflow-x-auto"
						onKeyDown={onTabsKeyDown ?? handleTerminalTabListKeyDown}
						role="tablist"
					>
						<span
							data-terminal-role="primary"
							className={cn(
								"group relative inline-flex min-w-shell-tab-min self-stretch items-center gap-1.5 border-r border-border px-3",
								timelineActive
									? "bg-overlay text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground/80"
									: "text-muted-foreground hover:bg-raised hover:text-foreground",
							)}
						>
							<AgentAvatar className="size-icon-base" decorative provider={snapshot.harness} />
							<button
								aria-current={timelineActive ? true : undefined}
								aria-label={label}
								aria-selected={timelineActive}
								className={cn(
									"inline-flex min-w-flex-min max-w-shell-tab-max items-center gap-1.5 text-control font-medium leading-none transition-colors",
									timelineActive ? "text-foreground" : "text-muted-foreground",
								)}
								onClick={timelineActive ? undefined : onSelectChat}
								role="tab"
								tabIndex={timelineActive || (!reviewerTerminal && !shellTerminals?.length) ? 0 : -1}
								title={label}
								type="button"
							>
								<span className="truncate">{label}</span>
							</button>
						</span>
						{reviewerTerminal ? (
							<span
								className={cn(
									"group relative inline-flex min-w-shell-tab-min self-stretch items-center gap-1.5 border-r border-border px-3",
									reviewerActive
										? "bg-overlay text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground/80"
										: "text-muted-foreground hover:bg-raised hover:text-foreground",
								)}
							>
								<AgentAvatar className="size-icon-base" decorative provider={reviewerTerminal.harness} />
								<button
									aria-current={reviewerActive ? true : undefined}
									aria-label="Reviewer"
									aria-selected={Boolean(reviewerActive)}
									className={cn(
										"inline-flex min-w-flex-min max-w-shell-tab-max items-center gap-1.5 text-control font-medium leading-none",
										reviewerActive ? "text-foreground" : "text-muted-foreground",
									)}
									onClick={() => onOpenReviewerTerminal?.(reviewerTerminal)}
									role="tab"
									tabIndex={reviewerActive ? 0 : -1}
									title={reviewerTerminal.harness}
									type="button"
								>
									<span className="truncate">Reviewer</span>
								</button>
							</span>
						) : null}
						{/* The same shared shell tab the terminal pane strip and the
						    standalone terminals screen use, so all three never drift. */}
						{(shellTerminals ?? []).map((shell) => (
							<ShellTerminalTab
								key={shell.handleId}
								appearance="connected"
								isActive={shell.handleId === shellActiveHandleId}
								onClose={() => onCloseShellTerminal?.(shell.handleId)}
								onRename={
									onRenameShellTerminal
										? (title) => onRenameShellTerminal(shell.handleId, title)
										: undefined
								}
								onSelect={() => onSelectShellTerminal?.(shell.handleId)}
								shell={shell}
							/>
						))}
					</div>
				</div>
				<div className="ml-auto flex shrink-0 items-center gap-1 px-3" data-testid="session-action-region">
					{switchAgentControl}
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
		connecting: { title: "Connecting to the agent…", tone: "text-muted-foreground" },
		recovering: { title: "Reconnecting to the agent", tone: "text-warning" },
		stopped: { title: "The agent controller stopped", tone: "text-destructive" },
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
				<Loader2 aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 animate-spin text-muted-foreground" />
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
								<Button type="button" size="sm" variant="outline" onClick={onResume} disabled={resuming}>
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
	onEditHumanMessage,
	editPending,
	editBusy,
	editError,
	onActivateBranch,
	activateBranchPending,
	activateBranchError,
}: {
	snapshot: ConversationSnapshot;
	hasOlder?: boolean;
	loadingOlder?: boolean;
	onLoadOlder?: () => void;
	onDecide?: (requestId: string, decisionId: string) => void;
	onResolveInput?: ChatWorkspaceProps["onResolveInput"];
	busy?: boolean;
	onRollback?: (turnId: string) => void;
	onEditHumanMessage?: (turnId: string, text: string) => Promise<unknown> | void;
	editPending?: boolean;
	editBusy?: boolean;
	editError?: string;
	onActivateBranch?: (branchId: string) => Promise<unknown> | void;
	activateBranchPending?: boolean;
	activateBranchError?: string;
}) {
	const scroller = useRef<HTMLDivElement>(null);
	const scrollContent = useRef<HTMLDivElement>(null);
	const scrollTrack = useRef<HTMLDivElement>(null);
	const drag = useRef<{ pointerId: number; startY: number; startScrollTop: number } | null>(null);
	const [pinned, setPinned] = useState(true);
	const [hoveredMarker, setHoveredMarker] = useState<number | null>(null);
	const [messageEdit, setMessageEdit] = useState<MessageEditDraft>();
	const turn = activeTurn(snapshot);
	const [scrollbar, setScrollbar] = useState({
		visible: false,
		top: 0,
		height: 40,
		percent: 0,
		markers: [] as Array<{ top: number; scrollTop: number; visible: boolean }>,
	});
	const queued = useMemo(() => queuedTurnIds(snapshot), [snapshot]);
	const decide = useStableCallback(onDecide);
	const resolveInput = useStableCallback(onResolveInput);
	const rollback = useStableCallback(onRollback);
	const apiBaseUrl = useSyncExternalStore(subscribeApiBaseUrl, getApiBaseUrl, getApiBaseUrl);
	const editHumanMessage = useStableCallback(onEditHumanMessage);
	const activateBranch = useStableCallback(onActivateBranch);
	const canEditHumanMessage = Boolean(onEditHumanMessage);
	const canActivateBranch = Boolean(onActivateBranch);
	const branchPoints = useMemo(
		() => new Map((snapshot.branchPoints ?? []).map((point) => [point.turnId, point])),
		[snapshot.branchPoints],
	);
	const editableTurns = useMemo(
		() => new Set(snapshot.turns.filter((turn) => turn.providerTurnId).map((turn) => turn.id)),
		[snapshot.turns],
	);

	useEffect(() => setMessageEdit(undefined), [snapshot.sessionId]);

	const startMessageEdit = useCallback((message: ConversationMessage) => {
		if (!message.turnId) return;
		setMessageEdit({
			turnId: message.turnId,
			text: message.text,
			content: message.content ?? [],
		});
	}, []);
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
	const editedMessageVisible = Boolean(
		messageEdit &&
			items.some(
				(item) => item.kind === "message" && item.role === "user" && item.turnId === messageEdit.turnId,
			),
	);
	const grouped = useMemo(() => groupByTurn({ ...snapshot, items }), [snapshot, items]);
	const groups = useStableList(grouped, groupKey, sameGroup);
	const navigableGroups = useMemo(() => groups.filter(groupHasHumanPrompt), [groups]);
	const previews = useMemo(() => navigableGroups.map(groupPreview), [navigableGroups]);

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
		const viewportRect = node.getBoundingClientRect();
		const anchors = Array.from(
			scrollContent.current?.querySelectorAll<HTMLElement>("[data-chat-scroll-anchor]") ?? [],
		);
		const markerGap = anchors.length > 1 ? Math.min(18, (trackHeight - 12) / (anchors.length - 1)) : 0;
		const markerStart = (trackHeight - markerGap * Math.max(0, anchors.length - 1)) / 2;
		const markers = anchors.map((anchor, index) => {
			const rect = anchor.getBoundingClientRect();
			const contentY = rect.top - viewportRect.top + node.scrollTop + rect.height / 2;
			return {
				top: markerStart + index * markerGap,
				scrollTop: Math.min(maxScroll, Math.max(0, contentY - node.clientHeight / 2)),
				visible: contentY >= node.scrollTop && contentY <= node.scrollTop + node.clientHeight,
			};
		});
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

	useEffect(() => {
		if (!pinned) return;
		const node = scroller.current;
		if (node) {
			node.scrollTop = node.scrollHeight;
			updateScrollbar();
		}
	}, [pinned, snapshot.latestSequence, updateScrollbar]);

	useEffect(() => {
		updateScrollbar();
		if (typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(updateScrollbar);
		if (scroller.current) observer.observe(scroller.current);
		if (scrollContent.current) observer.observe(scrollContent.current);
		return () => observer.disconnect();
	}, [groups.length, updateScrollbar]);

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
		if (!scrollbar.visible) return;
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
		const node = scroller.current;
		if (!node) return;
		event.preventDefault();
		node.scrollTop += event.deltaY;
		onScroll();
	}

	function onScrollbarKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
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
		return <EmptyState harness={snapshot.harness} />;
	}

	return (
		<div className="relative min-h-0 flex-1">
			<div
				ref={scroller}
				onScroll={onScroll}
				className="chat-scroll-viewport cursor-chat-timeline h-full min-w-0 select-text overflow-x-hidden overflow-y-auto px-4 py-5"
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
								{loadingOlder ? <Loader2 aria-hidden="true" className="size-3.5 animate-spin" /> : null}
								Load earlier messages
							</Button>
						</div>
					) : null}
					{groups.map((group) => (
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
								onEditHumanMessage={canEditHumanMessage ? editHumanMessage : undefined}
								messageEdit={messageEdit}
								onStartMessageEdit={startMessageEdit}
								onUpdateMessageEdit={updateMessageEdit}
								onCancelMessageEdit={cancelMessageEdit}
								onSubmitMessageEdit={submitMessageEdit}
								editPending={editPending}
								editBusy={editBusy}
								editError={editError}
								branchPoints={branchPoints}
								editableTurns={editableTurns}
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
					))}
					{turn && !groups.some((group) => group.turnId === turn.id) ? (
						<TurnLiveStatus startedAt={turn.startedAt ?? turn.requestedAt} />
					) : null}
					{messageEdit && !editedMessageVisible ? (
						<div className="flex justify-end" data-chat-scroll-anchor="">
							<HumanMessageEditor
								text={messageEdit.text}
								content={messageEdit.content}
								pending={Boolean(editPending)}
								busy={Boolean(editBusy)}
								error={editError}
								onDraftChange={updateMessageEdit}
								onCancel={cancelMessageEdit}
								onSend={submitMessageEdit}
							/>
						</div>
					) : null}
				</div>
			</div>

			<div
				ref={scrollTrack}
				role="scrollbar"
				tabIndex={scrollbar.visible ? 0 : -1}
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
					if (scrollbar.markers.length === 0) return;
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
					"group/scroll absolute inset-y-3 right-1 z-10 w-4 touch-none rounded-full outline-none transition-opacity focus-visible:ring-1 focus-visible:ring-logo-accent/60",
					scrollbar.visible ? "cursor-pointer opacity-100" : "pointer-events-none opacity-0",
				)}
			>
				<div className="absolute inset-0 cursor-grab group-active/scroll:cursor-grabbing">
					{scrollbar.markers.map((marker, index) => {
						const distance = hoveredMarker === null ? Number.POSITIVE_INFINITY : Math.abs(index - hoveredMarker);
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
					})}
				</div>

				{hoveredMarker !== null && scrollbar.markers[hoveredMarker] && previews[hoveredMarker] ? (
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
					className="absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full bg-raised shadow-sm hover:bg-surface dark:bg-raised dark:hover:bg-surface"
				>
					<ArrowDown aria-hidden="true" className="size-4" />
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
	busy,
	queued,
}: {
	group: TimelineGroup;
	sessionId: string;
	apiBaseUrl: string;
	onDecide: (requestId: string, decisionId: string) => void;
	onResolveInput: NonNullable<ChatWorkspaceProps["onResolveInput"]>;
	onRollback: (turnId: string) => void;
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
	busy?: boolean;
	/** This turn was recorded but not sent, so its message can say so. */
	queued: boolean;
}) {
	const runs = useMemo(() => runsOf(group.items), [group.items]);
	const copyableMessageId = group.outcome
		? [...group.items]
				.reverse()
				.find((item) => item.kind === "message" && item.role === "assistant")?.id
		: undefined;
	const latestItemId = group.items.at(-1)?.id;
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
						showCopy={run.items[0]?.id === copyableMessageId}
						showStreamingIndicator={group.live && run.items[0]?.id === latestItemId}
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
			{group.diff ? <TurnChangedFiles diff={group.diff} live={group.live} /> : null}
			{group.live ? (
				<TurnLiveStatus startedAt={group.liveStartedAt} blocked={group.blocked} />
			) : null}
			{group.outcome ? (
				<TurnOutcome
					state={group.outcome.state}
					durationMs={group.outcome.durationMs}
					error={group.outcome.error}
					onRollback={canRollback ? () => onRollback(group.turnId as string) : undefined}
				/>
			) : null}
		</div>
	);
});

function TurnLiveStatus({ startedAt, blocked }: { startedAt?: string; blocked?: boolean }) {
	const [elapsed, setElapsed] = useState(() => elapsedSince(startedAt));

	useEffect(() => {
		if (blocked) return;
		const timer = setInterval(() => setElapsed(elapsedSince(startedAt)), 1000);
		return () => clearInterval(timer);
	}, [blocked, startedAt]);

	return (
		<div className="flex min-h-6 items-center gap-2 px-1 py-0.5" data-testid="live-turn-status">
			{blocked ? (
				<span role="alert" className="sr-only">
					The agent is waiting for your decision.
				</span>
			) : null}
			{blocked ? (
				<TriangleAlert aria-hidden="true" className="size-3.5 shrink-0 text-warning" />
			) : (
				<Loader2
					aria-hidden="true"
					className="size-3 shrink-0 animate-spin text-status-working opacity-100"
				/>
			)}
			<span
				role={blocked ? undefined : "status"}
				aria-live={blocked ? undefined : "polite"}
				className={cn("text-xs font-medium", blocked ? "text-warning" : "text-muted-foreground")}
			>
				{blocked ? "Waiting for your decision" : `Working for ${elapsed}`}
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
	showCopy,
	showStreamingIndicator,
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
	/** This is the final assistant response of a turn that has finished. */
	showCopy?: boolean;
	/** This message is the live edge of its turn, rather than an earlier fragment
	 * followed by tool activity. */
	showStreamingIndicator?: boolean;
}) {
	if (item.kind === "message") {
		if (item.role === "assistant") {
			return (
				<AssistantMessage
					message={item}
					showCopy={showCopy}
					showStreamingIndicator={showStreamingIndicator}
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
					onEdit={editAvailable ? (_turnID, text) => onSubmitMessageEdit(text) : undefined}
					editing={editing}
					editText={editing ? messageEdit?.text : undefined}
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
		return <SteerMessage activity={item} />;
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

type TimelineGroup = {
	key: string;
	turnId?: string;
	/** Where this group sits in the timeline: the lowest sequence it contains. */
	anchor: number;
	items: ConversationItem[];
	outcome?: { state: "completed" | "interrupted" | "failed"; durationMs?: number; error?: string };
	/** What the turn changed on disk, when the daemon reported anything. */
	diff?: TurnDiff;
	/** The agent's plan for this turn, when it made one. */
	plan?: ConversationPlan;
	/** The turn is still running, so its diff and plan can still change. */
	live?: boolean;
	liveStartedAt?: string;
	blocked?: boolean;
	/** The provider accepted this turn, so there is history it can be asked to drop. */
	rollbackable?: boolean;
};

type GroupPreview = { title: string; detail?: string };

/** A turn-sized, plain-text preview for the minimap hover card. */
function groupPreview(group: TimelineGroup): GroupPreview {
	const userMessage = group.items.find(
		(item) => item.kind === "message" && item.role === "user" && item.origin === "human",
	);
	const assistantMessage = [...group.items].reverse().find(
		(item) => item.kind === "message" && item.role === "assistant" && item.text.trim() !== "",
	);
	const firstActivity = group.items.find(
		(item): item is ConversationActivity => item.kind === "activity",
	);
	const title = previewText(
		userMessage?.kind === "message" ? userMessage.text : firstActivity?.summary || "Conversation update",
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
	return group.items.some((item) => item.kind === "message" && item.role === "user" && item.origin === "human");
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
			groups.push({ key: `loose-${item.sequence}`, anchor: item.sequence, items: [item] });
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
		if (turn.state === "running" || turn.state === "queued") continue;
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

function EmptyState({ harness }: { harness: string }) {
	return (
		<div className="flex min-h-0 flex-1 items-center justify-center px-6">
			<div className="flex max-w-sm flex-col items-center gap-2 text-center">
				<MessageSquare aria-hidden="true" className="size-6 text-muted-foreground" />
				<strong className="text-sm font-medium text-foreground">Start the conversation</strong>
				<p className="text-xs leading-relaxed text-muted-foreground">
					This session talks to {harness} over a structured connection. Tool calls, file changes,
					and approvals appear here as they happen. The Terminal tab opens a plain shell in the
					same worktree — it is not a second copy of the agent.
				</p>
			</div>
		</div>
	);
}

/* -------------------------------------------------------------------------- */

function QueuedMessageDock({
	messages,
	onSteer,
}: {
	messages: Array<{ turnId: string; message: ConversationMessage }>;
	onSteer?: (turnId: string) => Promise<unknown>;
}) {
	const [steeringTurnId, setSteeringTurnId] = useState<string>();
	const [errors, setErrors] = useState<Record<string, string>>({});

	async function steer(turnId: string) {
		if (!onSteer || steeringTurnId) return;
		setSteeringTurnId(turnId);
		setErrors((current) => {
			const next = { ...current };
			delete next[turnId];
			return next;
		});
		try {
			await onSteer(turnId);
		} catch {
			setErrors((current) => ({
				...current,
				[turnId]: "Could not steer this message. It remains queued.",
			}));
		} finally {
			setSteeringTurnId(undefined);
		}
	}

	return (
		<div
			className="-mb-2 max-h-40 overflow-y-auto rounded-t-[10px] border border-b-0 border-border-strong bg-surface"
			data-testid="queued-message-dock"
		>
			{messages.map(({ turnId, message }) => {
				const steering = steeringTurnId === turnId;
				return (
					<div
						key={turnId}
						className="border-b border-border px-3 py-2 last:border-b-0"
						data-testid={`queued-message-${turnId}`}
					>
						<div className="flex min-h-7 min-w-0 items-center gap-2">
							<CornerDownRight aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />
							<span className="min-w-0 flex-1 truncate text-sm text-foreground" title={message.text}>
								{message.text}
							</span>
							{onSteer ? (
								<Button
									type="button"
									size="sm"
									variant="ghost"
									disabled={Boolean(steeringTurnId)}
									onClick={() => void steer(turnId)}
								>
									<CornerDownRight aria-hidden="true" className="size-3" />
									{steering ? "Steering…" : "Steer"}
								</Button>
							) : null}
						</div>
						{errors[turnId] ? (
							<p role="status" className="mt-1 text-[11px] text-warning">
								{errors[turnId]}
							</p>
						) : null}
					</div>
				);
			})}
		</div>
	);
}
