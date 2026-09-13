/**
 * The central surface for a chat-mode session.
 *
 * Mounted by SessionView when the session's persisted mode is `chat`. It owns the
 * conversation query and command wiring so ChatWorkspace stays a pure view of a
 * snapshot — which is what lets the same component render fixtures in the dev
 * preview and live data here.
 */

import { AlertTriangle, CheckCircle2, Loader2, X } from "lucide-react";
import { memo, useEffect, useRef, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	findActiveAgentSwitch,
	isTerminalAgentSwitch,
	selectDurableAgentSwitch,
	useAgentSwitches,
} from "../../hooks/useAgentSwitches";
import { useObservedAgentSwitchLifecycle } from "../../hooks/useObservedAgentSwitchLifecycle";
import { useAgentSwitchPresentationVisibility, useAgentSwitchRouteVisibility } from "../../hooks/useAgentSwitchVisibility";
import { useSwitchAgentState } from "../../hooks/useSwitchAgent";
import {
	useConversation,
	useConversationCommands,
	useConversationConfigOptions,
	useConversationModels,
	useConversationSkills,
	useStageAttachments,
	useWorkspaceFilePaths,
} from "../../hooks/useConversation";
import { useAgentSwitchProviderCatalogs } from "../../hooks/useAgentSwitchProviderCatalogs";
import { useRememberProjectPermissions } from "../../hooks/useRememberProjectPermissions";
import { useSessionBrowserLink } from "../../hooks/useSessionBrowserLink";
import { isWebLink, isWorkspaceHtmlLink } from "../../lib/external-link-policy";
import type { ShellTerminal } from "../../hooks/useShellTerminals";
import {
	deriveAgentSwitchPresentation,
	agentSwitchVisibilityPresentationKind,
	type AgentSwitchPresentation,
} from "../../lib/agent-switch-presentation";
import { cn } from "../../lib/utils";
import type { Theme } from "../../stores/ui-store";
import { can } from "../../types/conversation";
import type { ConversationSnapshot } from "../../types/conversation";
import type { TerminalTarget } from "../../types/terminal";
import type { AgentSwitchSummary, WorkspaceSession } from "../../types/workspace";
import { AgentSwitchProgressTrack } from "../AgentSwitchProgressTrack";
import { ChatWorkspace } from "./ChatWorkspace";

export interface ConversationWorkState {
	controllerBusy: boolean;
	hasRunningTurn: boolean;
	queuedTurnCount: number;
}

const HTTP_LINK_PATTERN = /https?:\/\/[^\s<>()\[\]{}"']+/i;
const autoOpenedAgentMessageKeys = new Set<string>();

function cleanExtractedLink(value: string): string {
	return value.replace(/[.,!?;:`\\]+$/, "");
}

function firstBrowserLink(text: string, workspacePaths: string[]): string | undefined {
	const candidates: Array<{ index: number; value: string }> = [];
	const webMatch = HTTP_LINK_PATTERN.exec(text);
	if (webMatch) candidates.push({ index: webMatch.index, value: cleanExtractedLink(webMatch[0]) });
	const markdownLink = /\[[^\]]+\]\(([^)\s]+)\)/.exec(text);
	if (markdownLink?.[1]) candidates.push({ index: markdownLink.index, value: cleanExtractedLink(markdownLink[1]) });
	for (const candidate of candidates.sort((a, b) => a.index - b.index)) {
		if (isWebLink(candidate.value) || isWorkspaceHtmlLink(candidate.value, workspacePaths)) return candidate.value;
	}
	return undefined;
}

export const SessionChatSurface = memo(function SessionChatSurface({
	session,
	reviewerTerminal,
	onOpenReviewerTerminal,
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
	onOpenShell,
	openingShell,
	shellError,
	onOpenFiles,
	onOpenFile,
	onOpenLinkInBrowser,
	headerActions,
	sessionTabAction,
	sessionTabActionWide = false,
	tabStripAction,
	handoffDialogOpen = false,
	workspaceTabs,
	workspaceTabActions,
	workspaceActiveTabKey,
	workspaceFileActive,
	auxiliaryTabOrder,
	onAuxiliaryTabOrderChange,
	controllerTransitioning,
	newWorkDisabled,
	onConversationWorkChange,
}: {
	session: WorkspaceSession;
	reviewerTerminal?: { handleId: string; harness: string };
	onOpenReviewerTerminal?: (target: { handleId: string; harness: string }) => void;
	onSessionRenamed?: () => void | Promise<void>;
	reviewerTarget?: Extract<TerminalTarget, { kind: "reviewer" }>;
	onSelectChat?: () => void;
	/** This session's standalone shells, rendered as tabs in the chat header. */
	shellTerminals?: ShellTerminal[];
	/** The selected shell pane, if any. Mirrors reviewerTarget. */
	shellTarget?: Extract<TerminalTarget, { kind: "shell" }>;
	onSelectShellTerminal?: (handleId: string) => void;
	onCloseShellTerminal?: (handleId: string) => void;
	onRenameShellTerminal?: (handleId: string, title: string) => void;
	daemonReady?: boolean;
	theme?: Theme;
	onOpenShell?: () => void;
	openingShell?: boolean;
	shellError?: string;
	/** Opens the Files inspector from a turn's changed-files Review control. */
	onOpenFiles?: () => void;
	/** Opens the Files inspector focused on one changed path. */
	onOpenFile?: (path: string) => void;
	/** Opens a chat link in the active blank tab or a new tab in this session's AO Browser. */
	onOpenLinkInBrowser?: (uri: string) => Promise<void>;
	headerActions?: ReactNode;
	sessionTabAction?: ReactNode;
	sessionTabActionWide?: boolean;
	tabStripAction?: ReactNode;
	handoffDialogOpen?: boolean;
	workspaceTabs?: Array<{ key: string; content: ReactNode; onSelect: () => void }>;
	workspaceTabActions?: ReactNode;
	workspaceActiveTabKey?: string;
	/** A file overlay hides the chat surface, so it must not acknowledge switch UI. */
	workspaceFileActive?: boolean;
	/** Session-owned order shared with the terminal UI surface. */
	auxiliaryTabOrder?: string[];
	onAuxiliaryTabOrderChange?: (keys: string[]) => void;
	/** The target controller is being installed by an interface handoff. */
	controllerTransitioning?: boolean;
	/** An interface handoff fences new agent work while current-turn decisions remain available. */
	newWorkDisabled?: boolean;
	/** Reports accepted Chat work that must inform an interface-switch policy choice. */
	onConversationWorkChange?: (state: ConversationWorkState) => void;
}) {
	const {
		snapshot: queriedSnapshot,
		isLoading,
		unavailable,
		error,
		hasOlder,
		isLoadingOlder,
		loadOlder,
	} = useConversation(session.id);
	// Route props can move to the destination before the old query observer drops
	// its data. Treat that snapshot as unknown everywhere, especially at the work
	// boundary that decides whether switching to Terminal needs user consent.
	const snapshot = queriedSnapshot?.sessionId === session.id ? queriedSnapshot : undefined;
	const commands = useConversationCommands(session.id);
	const projectPermissions = useRememberProjectPermissions(session.workspaceId, snapshot?.harness);
	const {
		acknowledgeAcceptedTurn,
		acknowledgeLocalEcho,
		localEchos = [],
		pendingAcceptedTurnId,
	} = commands;
	const conversationWorkKnown = Boolean(snapshot);
	const acceptedLocalTurnObserved = Boolean(
		pendingAcceptedTurnId && snapshot?.turns.some((turn) => turn.id === pendingAcceptedTurnId),
	);
	const acceptedLocalWorkPending = Boolean(pendingAcceptedTurnId && !acceptedLocalTurnObserved);
	const controllerBusy =
		snapshot?.controller?.state === "busy" || commands.busy || acceptedLocalWorkPending;
	const hasRunningTurn = Boolean(snapshot?.turns.some((turn) => turn.state === "running"));
	const queuedTurnCount = snapshot?.turns.filter((turn) => turn.state === "queued").length ?? 0;
	useEffect(() => {
		if (acceptedLocalTurnObserved && pendingAcceptedTurnId) {
			acknowledgeAcceptedTurn(pendingAcceptedTurnId);
		}
	}, [acceptedLocalTurnObserved, acknowledgeAcceptedTurn, pendingAcceptedTurnId]);
	useEffect(() => {
		if (!snapshot) return;
		const durableHumanTurnIds = new Set(
			snapshot.items.flatMap((item) =>
				item.kind === "message" && item.role === "user" && item.origin === "human" && item.turnId
					? [item.turnId]
					: [],
			),
		);
		for (const echo of localEchos) {
			if (echo.turnId && durableHumanTurnIds.has(echo.turnId)) acknowledgeLocalEcho?.(echo.turnId);
		}
	}, [acknowledgeLocalEcho, localEchos, snapshot]);
	useEffect(() => {
		if (!conversationWorkKnown) return;
		onConversationWorkChange?.({ controllerBusy, hasRunningTurn, queuedTurnCount });
	}, [controllerBusy, conversationWorkKnown, hasRunningTurn, onConversationWorkChange, queuedTurnCount]);
	const targetChatControllerReady =
		snapshot?.harness === session.provider &&
		(snapshot.controller?.state === "ready" || snapshot.controller?.state === "busy");
	// Mode commits before the target controller starts. A cached ready snapshot
	// can also outlive the source, so wait for the handoff's final snapshot refresh.
	const controllerCatalogsEnabled = targetChatControllerReady && !controllerTransitioning && !newWorkDisabled;
	// Agent-switch presentation for the chat surface progress track and input locks.
	const switchMutation = useSwitchAgentState(session.id);
	const agentSwitches = useAgentSwitches(session.id).data ?? [];
	const activeHistorySwitch = findActiveAgentSwitch(agentSwitches);
	const selectedDurableAgentSwitch = selectDurableAgentSwitch(
		session.activeAgentSwitch,
		agentSwitches,
	);
	const admissionAgentSwitch: AgentSwitchSummary | undefined =
		switchMutation.isPending && switchMutation.input
			? {
				agentHandoffStatus: "not_attempted",
				fromHarness: switchMutation.input.session.provider,
				id: `admission:${switchMutation.input.idempotencyKey}`,
				state: "preparing_handoff",
				targetHarness: switchMutation.input.targetHarness,
			}
			: undefined;
	const {
		dismissFailure: dismissAgentSwitchFailure,
		dismissedFailureSwitchId,
		isObserved: isAgentSwitchObserved,
		isRetired: isAgentSwitchRetired,
		observedTerminalSwitch,
		settle: settleAgentSwitch,
		transientSuccessNotice,
		transientSuccessSwitchId,
	} = useObservedAgentSwitchLifecycle({
		sessionId: session.id,
		agentSwitches,
		nonterminalCandidates: [
			session.activeAgentSwitch,
			activeHistorySwitch,
			selectedDurableAgentSwitch,
			admissionAgentSwitch,
		],
	});
	const durableAgentSwitch =
		selectedDurableAgentSwitch && !isAgentSwitchRetired(selectedDurableAgentSwitch.id)
			? selectedDurableAgentSwitch
			: undefined;
	const agentSwitch = durableAgentSwitch ?? admissionAgentSwitch ?? observedTerminalSwitch;
	useAgentSwitchRouteVisibility(`session/${session.id}`, agentSwitch && agentSwitch.state !== "completed" && agentSwitch.state !== "failed" ? "active" : "history", undefined, false);
	const switchPresentation = agentSwitch
		? deriveAgentSwitchPresentation({
				agentSwitch,
				activityState: session.activity?.state,
				currentHarness: session.provider,
				isTerminated: Boolean(session.isTerminated),
				// The shared presentation uses a live terminal handle as its TUI
				// takeover proof. Chat has no terminal runtime, so its equivalent is
				// the structured controller reaching a dispatchable state.
				terminalHandleId: targetChatControllerReady ? "chat-controller" : undefined,
			})
		: undefined;
	const agentSwitching = Boolean(
		switchMutation.isPending ||
			(switchPresentation?.outcome === "in_progress" ||
				switchPresentation?.outcome === "recovery"),
	);
	const observedSettledSwitchId =
		agentSwitch &&
		(switchPresentation?.outcome === "success" || switchPresentation?.outcome === "failure") &&
		isAgentSwitchObserved(agentSwitch.id)
			? agentSwitch.id
			: undefined;
	const latestTerminalSwitch = agentSwitches.find(isTerminalAgentSwitch);
	const controllerOwnedTerminalSwitch =
		targetChatControllerReady &&
		latestTerminalSwitch &&
		((latestTerminalSwitch.state === "completed" &&
			latestTerminalSwitch.targetHarness === session.provider) ||
			(latestTerminalSwitch.state === "failed" &&
				latestTerminalSwitch.fromHarness === session.provider))
			? latestTerminalSwitch
			: undefined;
	// Catalog ownership follows the live controller epoch, not whether this mount
	// happened to observe the switch in progress. A sub-second switch can arrive
	// first as terminal history and still needs its outgoing cache reconciled.
	const providerCatalogSettledSwitchId =
		observedSettledSwitchId ?? controllerOwnedTerminalSwitch?.id;
	const catalogsEnabled = useAgentSwitchProviderCatalogs({
		sessionId: session.id,
		agentSwitching,
		settledSwitchId: providerCatalogSettledSwitchId,
	});
	const configOptions = useConversationConfigOptions(
		session.id,
		Boolean(controllerCatalogsEnabled && catalogsEnabled && snapshot && can(snapshot, "config_options")),
	);
	// A provider config catalog may cover only model, only mode, or both.
	// Suppress native controls only for dimensions the provider catalog replaces;
	// a model-only catalog must not hide the Approvals control.
	const providerOptions = configOptions.options ?? [];
	const hasProviderMode = providerOptions.some(
		(option) => option.category === "mode" || option.id === "mode",
	);
	const hasProviderModel = providerOptions.some(
		(option) => option.category === "model" || option.id === "model",
	);
	// Only asked for once the conversation is actually readable: the catalog comes
	// from the live controller, so there is nothing to fetch before then.
	const { models } = useConversationModels(
		session.id,
		Boolean(controllerCatalogsEnabled && catalogsEnabled && snapshot) && !hasProviderModel,
	);
	const { skills } = useConversationSkills(
		session.id,
		Boolean(controllerCatalogsEnabled && catalogsEnabled && snapshot),
	);
	const { paths, truncated } = useWorkspaceFilePaths(session.id, Boolean(snapshot));
	const stageAttachments = useStageAttachments(session.id);
	const openLinkInBrowser = useSessionBrowserLink(session, onOpenLinkInBrowser, paths);
	const conversationBaselineReady = useRef(false);
	useEffect(() => {
		if (!snapshot || isLoading) return;
		const isInitialSnapshot = !conversationBaselineReady.current;
		const latestUserMessage = snapshot.items
			.filter((item) => item.kind === "message" && item.role === "user")
			.at(-1);
		// Do not surprise users by opening links from history when a session is first
		// mounted. The exception is the current turn: a fast agent can finish before
		// the first conversation request resolves, so its response is already present
		// in the initial snapshot and must not be mistaken for old history.
		conversationBaselineReady.current = true;
		for (const item of snapshot.items) {
			if (item.kind !== "message" || item.role !== "assistant" || item.streaming) continue;
			if (isInitialSnapshot && (!latestUserMessage || item.sequence <= latestUserMessage.sequence)) continue;
			const messageKey = `${session.id}:${item.id}`;
			if (autoOpenedAgentMessageKeys.has(messageKey)) continue;
			const url = firstBrowserLink(item.text, paths);
			if (url) {
				autoOpenedAgentMessageKeys.add(messageKey);
				openLinkInBrowser(url);
			}
		}
	}, [isLoading, openLinkInBrowser, paths, snapshot]);
	const observedSuccessfulSwitch = Boolean(
		agentSwitch &&
			observedSettledSwitchId === agentSwitch.id &&
			switchPresentation?.outcome === "success",
	);
	useEffect(() => {
		if (!observedSuccessfulSwitch || !agentSwitch || !switchPresentation) return;
		settleAgentSwitch(agentSwitch, switchPresentation);
	}, [agentSwitch, observedSuccessfulSwitch, settleAgentSwitch, switchPresentation]);
	const shownSwitchPresentation =
		switchPresentation?.outcome === "failure" && dismissedFailureSwitchId === agentSwitch?.id
			? undefined
			: switchPresentation?.outcome === "success"
				? transientSuccessSwitchId === agentSwitch?.id
					? transientSuccessNotice?.presentation
					: undefined
				: switchPresentation ?? transientSuccessNotice?.presentation;
	const switchLocksChat = Boolean(
		switchPresentation?.lockAgentTerminal && !switchPresentation.allowSourceInput,
	);
	const renderShellFallback = Boolean(shellTarget && session);
	const renderSnapshot =
		snapshot ??
		(renderShellFallback
			? unavailableConversationSnapshot(session)
			: undefined);
	const visibilityPresentationKind = agentSwitchVisibilityPresentationKind(shownSwitchPresentation);
	useAgentSwitchPresentationVisibility({
		localRouteKey: `session/${session.id}`,
		agentSwitch,
		presentationKind: visibilityPresentationKind,
		visible: Boolean(
			shownSwitchPresentation &&
				(!isLoading || renderShellFallback) &&
				(!unavailable || renderShellFallback) &&
				!error &&
				renderSnapshot &&
				!workspaceFileActive,
		),
	});

	if (isLoading && !renderShellFallback) {
		return (
			<Centered>
				<Loader2 aria-hidden="true" className="size-4 animate-spin text-muted-foreground" />
				<span className="text-xs text-muted-foreground">Loading conversation…</span>
			</Centered>
		);
	}

	// A chat session whose controller has not started yet, or whose agent cannot
	// run Chat is a state to explain rather than an error to spin on. A compatible
	// session may switch interfaces, but retrying this failed controller by itself
	// cannot change the answer.
	if (unavailable && !renderShellFallback) {
		return (
			<Centered>
				<AlertTriangle aria-hidden="true" className="size-4 text-warning" />
				<strong className="text-sm text-foreground">Conversation unavailable</strong>
				<p className="max-w-sm text-center text-xs leading-relaxed text-muted-foreground">
					{unavailable.message}
				</p>
				<p className="max-w-sm text-center text-xs leading-relaxed text-muted-foreground">
					The worktree is untouched. Open a shell from the inspector to work in it directly.
				</p>
			</Centered>
		);
	}

	if (error || !renderSnapshot) {
		return (
			<Centered>
				<AlertTriangle aria-hidden="true" className="size-4 text-destructive" />
				<p className="max-w-sm text-center text-xs leading-relaxed text-muted-foreground">
					{error ?? "Could not load this conversation."}
				</p>
			</Centered>
		);
	}

	return (
		<div className="relative h-full min-h-0">
			<ChatWorkspace
				key={session.id}
				snapshot={renderSnapshot}
				agentInputDisabled={switchLocksChat || handoffDialogOpen}
				newWorkDisabled={newWorkDisabled}
				onLinkOpen={openLinkInBrowser}
				sessionTitle={session.title}
				sessionRole={session.kind}
				session={session}
				onSessionRenamed={onSessionRenamed}
				reviewerTerminal={reviewerTerminal}
				onOpenReviewerTerminal={onOpenReviewerTerminal}
				reviewerTarget={reviewerTarget}
				onSelectChat={onSelectChat}
				shellTerminals={shellTerminals}
				shellTarget={shellTarget}
				onSelectShellTerminal={onSelectShellTerminal}
				onCloseShellTerminal={onCloseShellTerminal}
				onRenameShellTerminal={onRenameShellTerminal}
				daemonReady={daemonReady}
				theme={theme}
				headerActions={headerActions}
				sessionTabAction={sessionTabAction}
				sessionTabActionWide={sessionTabActionWide}
				tabStripAction={tabStripAction}
				workspaceTabs={workspaceTabs}
				workspaceTabActions={workspaceTabActions}
				workspaceActiveTabKey={workspaceActiveTabKey}
				auxiliaryTabOrder={auxiliaryTabOrder}
				onAuxiliaryTabOrderChange={onAuxiliaryTabOrderChange}
				controllerTransitioning={controllerTransitioning}
				hasOlder={hasOlder}
				loadingOlder={isLoadingOlder}
				onLoadOlder={loadOlder}
				busy={commands.busy}
				onSend={(text, attachments, clientMessageId) =>
					commands.send({ text, attachments, clientMessageId })}
				commandError={commands.error}
				onDecide={commands.resolve}
				onResolveInput={commands.resolveInput}
				onInterrupt={commands.interrupt}
				onResumeAgent={() => {
					void commands.resumeAgent().catch(() => {});
				}}
				resumingAgent={commands.resumingAgent}
				resumeError={commands.resumeError}
				onOpenShell={onOpenShell}
				openingShell={openingShell}
				shellError={shellError}
				models={models}
				onChooseSettings={hasProviderMode ? undefined : commands.chooseSettings}
				onRememberPermissions={can(renderSnapshot, "config_options") && !configOptions.loaded
					? undefined : projectPermissions.remember}
				rememberPermissionsPending={projectPermissions.pending}
				rememberPermissionsError={projectPermissions.error}
				rememberedPermissionMode={projectPermissions.savedMode}
				configOptions={configOptions.options}
				onChooseConfigOption={configOptions.setOption}
				configOptionPending={configOptions.pending || commands.choosingSettings}
				configOptionError={configOptions.error}
				onCompact={commands.compact}
				compacting={commands.compacting}
				compactUnavailable={commands.compactUnavailable}
				onRollback={commands.rollback}
				rollbackPending={commands.rollbackPending}
				rollbackError={commands.rollbackError}
				onOpenFiles={onOpenFiles}
				onOpenFile={onOpenFile}
				retryControl={commands.retryControl}
				onEditMessage={commands.editMessage}
				editMessagePending={commands.editMessagePending}
				editMessageError={commands.editMessageError}
				onActivateBranch={commands.activateBranch}
				activateBranchPending={commands.activateBranchPending}
				activateBranchError={commands.activateBranchError}
				skills={skills}
				filePaths={paths}
				filePathsTruncated={truncated}
				localEchos={localEchos}
				onStageAttachments={stageAttachments}
				nativeImages={can(renderSnapshot, "images")}
				// Gated on what the daemon advertises, so the control is never drawn for a
				// harness that cannot steer. The refusal check stays as a backstop: it
				// covers the window before the controller reports, and it is the last word
				// afterwards, since the capability is a property of the driver.
				onSteer={can(renderSnapshot, "steer") && !commands.steerUnsupported ? commands.steer : undefined}
				sendPending={commands.sendPending}
				steerPending={commands.steerPending}
				steerRefusal={commands.steerRefusal}
				onPromoteQueuedTurn={
					can(renderSnapshot, "steer") && !commands.steerUnsupported
						? commands.promoteQueuedTurn
						: undefined
				}
				onEditQueuedTurn={commands.editQueuedTurn}
				onCancelQueuedTurn={commands.cancelQueuedTurn}
				onReorderQueuedTurns={commands.reorderQueuedTurns}
				promoteQueuedTurnPendingTurnId={commands.promoteQueuedTurnPendingTurnId}
				cancelQueuedTurnPendingTurnId={commands.cancelQueuedTurnPendingTurnId}
				editQueuedTurnPendingTurnId={commands.editQueuedTurnPendingTurnId}
				onReloadMcpServers={
					!can(renderSnapshot, "mcp_reload") || commands.mcpReloadUnsupported
						? undefined
						: () => {
								// The rejection is already held by the mutation and rendered from
								// `mcpReloadError`; rethrowing it would only add a console error.
								void commands.reloadMcpServers().catch(() => {});
							}
				}
				reloadingMcpServers={commands.reloadingMcpServers}
				mcpReloadError={commands.mcpReloadError}
			/>
			{shownSwitchPresentation ? (
				<ChatAgentSwitchStatus
					auxiliaryActive={Boolean(reviewerTarget || shellTarget)}
					onDismiss={
						shownSwitchPresentation.outcome === "failure" && agentSwitch
							? () => dismissAgentSwitchFailure(agentSwitch.id)
							: undefined
					}
					presentation={shownSwitchPresentation}
				/>
			) : null}
		</div>
	);
});

function ChatAgentSwitchStatus({
	auxiliaryActive,
	onDismiss,
	presentation,
}: {
	auxiliaryActive: boolean;
	onDismiss?: () => void;
	presentation: AgentSwitchPresentation;
}) {
	const { t } = useTranslation();
	const fullOverlay = presentation.lockAgentTerminal && !presentation.allowSourceInput && !auxiliaryActive;
	const warning = presentation.outcome === "failure" || presentation.outcome === "recovery";
	const success = presentation.outcome === "success";
	const inProgress = presentation.outcome === "in_progress";
	return (
		<div
			aria-busy={inProgress && presentation.animate ? true : undefined}
			aria-live="polite"
			className={cn(
				"pointer-events-none z-20 flex",
				fullOverlay
					? "absolute inset-0 items-center justify-center bg-background/75 backdrop-blur-[1px]"
					: "absolute inset-x-3 top-3 justify-center",
			)}
			data-outcome={presentation.outcome}
			data-testid="chat-agent-switch-status"
			role="status"
		>
			<div
				className={cn(
					"pointer-events-auto relative flex w-full max-w-lg items-start gap-3 rounded-lg border bg-surface/95 px-4 py-3 text-left shadow-lg",
					onDismiss && "pr-11",
					success
						? "border-success/40"
						: warning
							? presentation.tone === "danger"
								? "border-danger/40"
								: "border-warning/40"
							: "border-border",
				)}
			>
				{success ? (
					<CheckCircle2 aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-success" />
				) : warning ? (
					<AlertTriangle
						aria-hidden="true"
						className={cn(
							"mt-0.5 size-4 shrink-0",
							presentation.tone === "danger" ? "text-danger" : "text-warning",
						)}
					/>
				) : (
					<Loader2 aria-hidden="true" className="mt-0.5 size-4 shrink-0 animate-spin text-status-working" />
				)}
				<div className="min-w-0 flex-1">
					<strong className="block text-sm text-foreground">
						{t(presentation.titleKey, presentation.values)}
					</strong>
					<p className="mt-0.5 text-pretty text-xs leading-relaxed text-muted-foreground">
						{t(presentation.descriptionKey, presentation.values)}
					</p>
					{inProgress ? <AgentSwitchProgressTrack stage={presentation.stage} /> : null}
				</div>
				{onDismiss ? (
					<button
						aria-label={t("common.close")}
						className="absolute right-2 top-2 grid size-7 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50"
						onClick={onDismiss}
						type="button"
					>
						<X aria-hidden="true" className="size-icon-sm" />
					</button>
				) : null}
			</div>
		</div>
	);
}

function unavailableConversationSnapshot(session: WorkspaceSession): ConversationSnapshot {
	return {
		conversationId: session.id,
		sessionId: session.id,
		harness: session.provider,
		mode: "chat",
		controller: { state: "stopped", error: "Conversation unavailable" },
		latestSequence: 0,
		oldestSequence: 0,
		hasMoreBefore: false,
		activeBranchId: "branch-root",
		branchPoints: [],
		settings: {},
		mcpServers: [],
		capabilities: [],
		turns: [],
		items: [],
	};
}

function Centered({ children }: { children: React.ReactNode }) {
	return (
		<div className="flex h-full flex-col items-center justify-center gap-2 bg-background px-6">
			{children}
		</div>
	);
}
