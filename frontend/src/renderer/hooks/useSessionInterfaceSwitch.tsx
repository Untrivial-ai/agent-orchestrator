import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { ConfirmDialog } from "../components/ConfirmDialog";
import {
	SessionInterfaceSwitchButton,
	SessionInterfaceSwitchDialog,
	SessionInterfaceSwitchMenuItem,
	SessionInterfaceTransitionNotice,
	interfaceTransitionOffersHistoryRecovery,
} from "../components/SessionInterfaceSwitch";
import {
	capturePendingFileAttachmentsForSession,
	discardCapturedPendingFileAttachments,
	type PendingFileAttachmentCapture,
} from "./useFileAttachments";
import { useSettings } from "./useSettings";
import { useCloudCp } from "./useCloudCp";
import { useCloudGate } from "./useCloudGate";
import {
	interfaceTransitionHasUnacknowledgedNotice,
	interfaceTransitionIsActive,
	interfaceTransitionNeedsRestart,
	useSessionInterfaceTransition,
	type SessionInterfaceContext,
} from "./useSessionInterfaceTransition";
import {
	chatDraftDiscardWarning,
	getChatDraftBoundaries,
	subscribeChatDraftBoundaries,
	type ChatDraftBoundaryKind,
} from "../lib/chat-draft-boundary";
import { sessionUiKey } from "../lib/hosts";
import type { WorkspaceSession } from "../types/workspace";
import type { ConversationWorkState } from "../components/chat/SessionChatSurface";

type Transition = components["schemas"]["SessionInterfaceTransition"];
type Mode = "chat" | "tui";
type HistoryPolicy = "strict" | "provider_history";
type Policy = "drain" | "interrupt";

type DialogScope = {
	owner: string;
	targetMode: Mode;
	historyPolicy?: HistoryPolicy;
	sourceBusy?: boolean;
	sourceWaitingForInput?: boolean;
};
type DraftDecision =
	| { kind: "safe" }
	| { kind: "cancelled" }
	| { kind: "confirmed"; pendingAttachments: PendingFileAttachmentCapture };
type PendingDraftDecision = {
	owner: string;
	promise: Promise<DraftDecision>;
	resolve: (decision: DraftDecision) => void;
};
type ChatLeaveLock = {
	owner: string;
	sessionId: string;
	requestId: number;
	previousTransitionId?: string;
	targetMode: "tui";
	policy: Policy;
	transitionId?: string;
	pendingAttachments?: PendingFileAttachmentCapture;
	needsReconciliation?: boolean;
};

function chatLeaveTransitionMatches(lock: ChatLeaveLock, transition: Transition | undefined): transition is Transition {
	return Boolean(
		transition &&
			transition.id !== lock.previousTransitionId &&
			transition.sessionId === lock.sessionId &&
			transition.sourceMode === "chat" &&
			transition.targetMode === lock.targetMode &&
			transition.policy === lock.policy,
	);
}

/** The admission, draft, and settlement boundaries shared by local, remote, and Cloud sessions. */
export function useSessionInterfaceSwitch(sessionId: string, session: WorkspaceSession | undefined, context?: SessionInterfaceContext) {
	const { t } = useTranslation();
	const hostId = typeof context === "string" ? context : undefined;
	const cloud = context && typeof context === "object" ? context : undefined;
	const isCloud = Boolean(cloud);
	const { client: cloudCpClient } = useCloudCp(isCloud);
	const { cloudEnabled } = useCloudGate(isCloud);
	const owner = sessionUiKey(sessionId, hostId);
	const currentOwnerRef = useRef(owner);
	useEffect(() => {
		currentOwnerRef.current = owner;
		return () => { currentOwnerRef.current = ""; };
	}, [owner]);
	const interfaceSwitch = useSessionInterfaceTransition(isCloud ? sessionId : session?.id, context);
	const [dialogScope, setDialogScope] = useState<DialogScope>();
	const [conversationWork, setConversationWork] = useState<ConversationWorkState & { owner?: string }>({
		controllerBusy: false,
		hasRunningTurn: false,
		queuedTurnCount: 0,
	});
	const onConversationWorkChange = useCallback((next: ConversationWorkState) => {
		setConversationWork((current) =>
			current.owner === owner &&
			current.controllerBusy === next.controllerBusy &&
			current.hasRunningTurn === next.hasRunningTurn &&
			current.queuedTurnCount === next.queuedTurnCount
				? current
				: { owner, ...next },
		);
	}, [owner]);

	const [confirmedDraftDiscard, setConfirmedDraftDiscard] = useState<{
		owner: string;
		transitionId: string;
		pendingAttachments: PendingFileAttachmentCapture;
	}>();
	const [chatLeaveLock, setChatLeaveLock] = useState<ChatLeaveLock>();
	const chatLeaveRequestIdRef = useRef(0);
	const pendingDraftDecisionRef = useRef<PendingDraftDecision | undefined>(undefined);
	const [draftConfirmation, setDraftConfirmation] = useState<{
		owner: string;
		boundaries: readonly ChatDraftBoundaryKind[];
	}>();
	const draftBoundaries = useSyncExternalStore(
		subscribeChatDraftBoundaries,
		() => getChatDraftBoundaries(owner),
		() => getChatDraftBoundaries(owner),
	);
	const confirmUnsafeDraftLeave = useCallback((): Promise<DraftDecision> => {
		const active = getChatDraftBoundaries(owner);
		if (active.length === 0) return Promise.resolve({ kind: "safe" });
		const pending = pendingDraftDecisionRef.current;
		if (pending?.owner === owner) return pending.promise;
		if (pending) pending.resolve({ kind: "cancelled" });
		let resolve!: (decision: DraftDecision) => void;
		const promise = new Promise<DraftDecision>((settle) => { resolve = settle; });
		pendingDraftDecisionRef.current = { owner, promise, resolve };
		setDraftConfirmation({ owner, boundaries: [...active] });
		return promise;
	}, [owner]);
	const settleUnsafeDraftLeave = useCallback((confirmed: boolean) => {
		const pending = pendingDraftDecisionRef.current;
		if (!pending) return;
		pendingDraftDecisionRef.current = undefined;
		setDraftConfirmation((current) => current?.owner === pending.owner ? undefined : current);
		pending.resolve(confirmed
			? { kind: "confirmed", pendingAttachments: capturePendingFileAttachmentsForSession(pending.owner) }
			: { kind: "cancelled" });
	}, []);
	useEffect(() => () => {
		const pending = pendingDraftDecisionRef.current;
		if (pending?.owner !== owner) return;
		pendingDraftDecisionRef.current = undefined;
		pending.resolve({ kind: "cancelled" });
	}, [owner]);
	useEffect(() => setConfirmedDraftDiscard(undefined), [owner]);

	useEffect(() => {
		if (!chatLeaveLock) return;
		if (chatLeaveLock.owner !== owner) {
			setChatLeaveLock((current) => current?.requestId === chatLeaveLock.requestId ? undefined : current);
			return;
		}
		const transition = interfaceSwitch.transition;
		if (!chatLeaveLock.transitionId) {
			if (chatLeaveTransitionMatches(chatLeaveLock, transition)) {
				setChatLeaveLock((current) => current?.requestId === chatLeaveLock.requestId
					? { ...current, transitionId: transition.id, needsReconciliation: false }
					: current);
			}
			return;
		}
		if (chatLeaveLock.pendingAttachments) {
			setConfirmedDraftDiscard({
				owner,
				transitionId: chatLeaveLock.transitionId,
				pendingAttachments: chatLeaveLock.pendingAttachments,
			});
			setChatLeaveLock((current) => current?.requestId === chatLeaveLock.requestId
				? { ...current, pendingAttachments: undefined }
				: current);
		}
		if (session?.mode !== "chat") {
			setChatLeaveLock((current) => current?.requestId === chatLeaveLock.requestId ? undefined : current);
			return;
		}
		if (!transition || transition.id !== chatLeaveLock.transitionId) return;
		if (transition.phase === "failed" || transition.phase === "cancelled" || transition.phase === "recovery_required") {
			setChatLeaveLock((current) => current?.requestId === chatLeaveLock.requestId ? undefined : current);
		}
	}, [chatLeaveLock, interfaceSwitch.transition, owner, session?.mode]);
	useEffect(() => {
		if (!chatLeaveLock?.needsReconciliation || chatLeaveLock.transitionId || chatLeaveLock.owner !== owner) return;
		let active = true;
		let retryTimer: number | undefined;
		const reconcile = async () => {
			try {
				const status = await interfaceSwitch.refreshStatus();
				if (!active) return;
				setChatLeaveLock((current) => {
					if (current?.requestId !== chatLeaveLock.requestId) return current;
					return chatLeaveTransitionMatches(current, status?.transition)
						? { ...current, transitionId: status.transition.id, needsReconciliation: false }
						: undefined;
				});
			} catch {
				if (active) retryTimer = window.setTimeout(() => void reconcile(), 1_000);
			}
		};
		void reconcile();
		return () => {
			active = false;
			if (retryTimer !== undefined) window.clearTimeout(retryTimer);
		};
	}, [chatLeaveLock, interfaceSwitch.refreshStatus, owner]);
	useEffect(() => {
		if (!confirmedDraftDiscard || confirmedDraftDiscard.owner !== owner) return;
		const transition = interfaceSwitch.transition;
		if (!transition || transition.id !== confirmedDraftDiscard.transitionId) return;
		switch (transition.phase) {
			case "completed":
				discardCapturedPendingFileAttachments(confirmedDraftDiscard.pendingAttachments);
				setConfirmedDraftDiscard(undefined);
				break;
			case "failed":
			case "cancelled":
			case "recovery_required":
				setConfirmedDraftDiscard(undefined);
				break;
		}
	}, [confirmedDraftDiscard, interfaceSwitch.transition, owner]);

	const activeTransition = interfaceTransitionIsActive(interfaceSwitch.transition);
	const cloudDrainWaiting = Boolean(isCloud && (
		(interfaceSwitch.starting && interfaceSwitch.startingPolicy === "drain") ||
		(interfaceSwitch.transition?.policy === "drain" &&
			["requested", "preflighting", "draining"].includes(interfaceSwitch.transition.phase))
	));
	const cloudLoader = Boolean(isCloud && !cloudDrainWaiting && (
		interfaceSwitch.starting || activeTransition || interfaceSwitch.settling ||
		(interfaceSwitch.transition?.phase === "completed" && session?.mode !== interfaceSwitch.transition.targetMode)
	));
	const hasNotice = interfaceTransitionHasUnacknowledgedNotice(interfaceSwitch.transition) &&
		!(isCloud && session?.mode === "tui" &&
			interfaceSwitch.transition?.errorCode === "SOURCE_DRAIN_FAILED" &&
			interfaceSwitch.transition.targetMode === "chat");
	const historyRecoveryNotice = hasNotice && interfaceTransitionOffersHistoryRecovery(interfaceSwitch.transition);
	const restartRequiredNotice = interfaceTransitionNeedsRestart(interfaceSwitch.transition);
	const chatLeaveLocked = Boolean(chatLeaveLock?.owner === owner && session?.mode === "chat");
	const controllerTransitioning = Boolean(session?.mode === "chat" && (
		(chatLeaveLocked && !cloudDrainWaiting) || (interfaceSwitch.starting && !cloudDrainWaiting) ||
		(interfaceSwitch.transition?.targetMode === "tui" &&
			((activeTransition && !cloudDrainWaiting) || interfaceSwitch.transition.phase === "completed")) ||
		(interfaceSwitch.transition?.targetMode === "chat" &&
			(activeTransition || interfaceSwitch.settling))
	));
	const target = (activeTransition ? interfaceSwitch.transition?.targetMode : interfaceSwitch.status?.targetMode)
		?? (session?.mode === "chat" ? "tui" : "chat");
	const newWorkDisabled = Boolean(session?.mode === "chat" && (
		(interfaceSwitch.starting && target === "tui") ||
		(interfaceSwitch.transition?.targetMode === "tui" && (activeTransition || interfaceSwitch.settling))
	));
	const dialogOpen = Boolean(dialogScope && session && dialogScope.owner === owner && dialogScope.targetMode === target);
	useEffect(() => setDialogScope(undefined), [owner, target]);
	const selectedWork = conversationWork.owner === owner ? conversationWork : undefined;
	const chatToTerminalNeedsPolicy = Boolean(session?.mode === "chat" && target === "tui" && (
		!selectedWork || selectedWork.controllerBusy || selectedWork.hasRunningTurn || selectedWork.queuedTurnCount
	));
	const busy = Boolean(session && (
		session.status === "working" || session.status === "needs_input" ||
		session.activity?.state === "active" || session.activity?.state === "waiting_input" ||
		session.activity?.state === "blocked" || chatToTerminalNeedsPolicy
	));
	const waitingForInput = Boolean(session && (
		session.status === "needs_input" || session.activity?.state === "waiting_input" ||
		session.activity?.state === "blocked"
	));
	const chatToTerminal = session?.mode === "chat" && target === "tui";
	const cloudTerminalToChat = isCloud && session?.mode === "tui" && target === "chat";
	const begin = useCallback(async (
		policy: Policy,
		targetMode: Mode,
		scope?: DialogScope,
		historyPolicy: HistoryPolicy = "strict",
	) => {
		const decision = chatToTerminal && getChatDraftBoundaries(owner).length > 0
			? await confirmUnsafeDraftLeave()
			: ({ kind: "safe" } satisfies DraftDecision);
		if (decision.kind === "cancelled") return;
		const requestId = chatToTerminal ? (chatLeaveRequestIdRef.current += 1) : undefined;
		if (requestId !== undefined) setChatLeaveLock({
			owner,
			sessionId,
			requestId,
			previousTransitionId: interfaceSwitch.transition?.id,
			targetMode: "tui",
			policy,
			pendingAttachments: decision.kind === "confirmed" ? decision.pendingAttachments : undefined,
		});
		try {
			const response = await interfaceSwitch.start({ targetMode, policy, historyPolicy });
			if (requestId !== undefined) setChatLeaveLock((current) =>
				current?.requestId === requestId && response?.transition?.id
					? { ...current, transitionId: response.transition.id, needsReconciliation: false }
					: current?.requestId === requestId ? { ...current, needsReconciliation: true } : current);
			if (scope) setDialogScope((current) => current === scope ? undefined : current);
		} catch {
			if (requestId !== undefined) setChatLeaveLock((current) =>
				current?.requestId === requestId ? { ...current, needsReconciliation: true } : current);
		}
	}, [chatToTerminal, confirmUnsafeDraftLeave, interfaceSwitch, owner, sessionId]);
	const request = useCallback(() => {
		interfaceSwitch.resetStartError();
		if (cloudTerminalToChat && !busy && session?.cloud) {
			// The Cloud list can lag terminal input. Confirm the current activity
			// before stopping a controller without asking the user first.
			void cloudCpClient.getSession(session.cloud.orgId, session.id).then(({ session: latest }) => {
				if (currentOwnerRef.current !== owner) return;
				if (["active", "waiting_input", "blocked"].includes(latest.activityState) ||
					latest.status === "working" || latest.status === "needs_input") {
					setDialogScope({
						owner, targetMode: target, sourceBusy: true,
						sourceWaitingForInput: latest.activityState === "waiting_input" ||
							latest.activityState === "blocked" || latest.status === "needs_input",
					});
					return;
				}
				void begin("interrupt", target);
			}).catch(() => {
				if (currentOwnerRef.current === owner) setDialogScope({ owner, targetMode: target });
			});
			return;
		}
		if (!busy) {
			void begin(cloudTerminalToChat ? "interrupt" : "drain", target);
			return;
		}
		if (session) setDialogScope({ owner, targetMode: target });
	}, [begin, busy, cloudCpClient, cloudTerminalToChat, interfaceSwitch, owner, session, target]);
	const choosePolicy = useCallback((policy: Policy) => {
		if (!session || !dialogScope || dialogScope.owner !== owner || dialogScope.targetMode !== target) {
			setDialogScope(undefined);
			return;
		}
		void begin(policy, dialogScope.targetMode, dialogScope, dialogScope.historyPolicy);
	}, [begin, dialogScope, owner, session, target]);
	const retry = useCallback((historyPolicy: HistoryPolicy) => {
		const failed = interfaceSwitch.transition;
		if (!session || !failed || failed.sessionId !== session.id || failed.targetMode !== target) return;
		interfaceSwitch.resetStartError();
		if (!busy) {
			void begin(cloudTerminalToChat ? "interrupt" : "drain", failed.targetMode, undefined, historyPolicy);
			return;
		}
		setDialogScope({ owner, targetMode: failed.targetMode, historyPolicy });
	}, [begin, busy, cloudTerminalToChat, interfaceSwitch, owner, session, target]);

	const { settings } = useSettings(hostId, !isCloud);
	const chatHarnesses = settings?.chatHarnesses ?? [];
	const unsupported = interfaceSwitch.status?.reasonCode === "CHAT_UNSUPPORTED" ||
		(!isCloud && target === "chat" && session !== undefined && chatHarnesses.length > 0 && !chatHarnesses.includes(session.provider));
	const blockedReason = interfaceSwitch.status?.reasonCode === "INTERFACE_HANDOFF_UNSUPPORTED"
		? t("session.interfaceHandoffUnsupported", {
			defaultValue: "This agent can't switch a running terminal session to chat. Start a new chat session instead.",
		})
		: undefined;
	const showAction = Boolean(!unsupported && (isCloud
		? cloudEnabled && sessionId
		: interfaceSwitch.status || interfaceSwitch.isLoading || interfaceSwitch.statusError));
	const disabledReason = interfaceSwitch.isLoading
		? "Checking whether this agent can switch interfaces…"
		: blockedReason || interfaceSwitch.status?.reason || interfaceSwitch.statusError;
	const inlineStatus = session && showAction && (cloudDrainWaiting || (!isCloud && activeTransition)) ? <SessionInterfaceSwitchButton
		target={target}
		supported
		disabledReason={disabledReason}
		pending={interfaceSwitch.starting || activeTransition}
		transition={interfaceSwitch.transition}
		cancelling={interfaceSwitch.cancelling}
		cancelError={interfaceSwitch.cancelError}
		onClick={request}
		onCancel={() => { void interfaceSwitch.cancel().catch(() => {}); }}
	/> : null;
	const menuItem = session && showAction && !activeTransition ? <SessionInterfaceSwitchMenuItem
		target={target}
		supported={Boolean(interfaceSwitch.status?.supported) && (isCloud || !chatLeaveLocked)}
		disabledReason={disabledReason}
		pending={interfaceSwitch.starting || chatLeaveLocked}
		onClick={request}
	/> : null;
	const notice = <>
		{interfaceSwitch.startError && !dialogOpen && !historyRecoveryNotice && !restartRequiredNotice ? <div
			role="alert"
			className="absolute left-1/2 top-3 z-20 flex w-[min(34rem,calc(100%-1.5rem))] -translate-x-1/2 items-start gap-3 rounded-lg border border-destructive/40 bg-popover px-3 py-2.5 text-xs shadow-md"
		>
			<div className="min-w-0 flex-1">
				<p className="font-medium">{t("session.interfaceSwitchFailed")}</p>
				<p className="mt-1 break-words text-muted-foreground">{interfaceSwitch.startError}</p>
			</div>
			<button type="button" aria-label={t("session.dismissInterfaceSwitchError")} className="shrink-0 rounded px-1 text-muted-foreground hover:text-foreground" onClick={interfaceSwitch.resetStartError}>{t("session.dismissInterfaceSwitchNotice")}</button>
		</div> : null}
		{(!interfaceSwitch.startError || historyRecoveryNotice || restartRequiredNotice) && hasNotice ? <SessionInterfaceTransitionNotice
			transition={interfaceSwitch.transition}
			dismissing={interfaceSwitch.acknowledgingNotice}
			dismissError={interfaceSwitch.acknowledgeNoticeError}
			onDismiss={() => {
				const transitionId = interfaceSwitch.transition?.id;
				if (transitionId) void interfaceSwitch.acknowledgeNotice(transitionId).catch(() => {});
			}}
			onSwitchWithInterrupt={() => {
				interfaceSwitch.resetStartError();
				const targetMode = interfaceSwitch.transition?.targetMode;
				if (targetMode) void begin("interrupt", targetMode);
			}}
			interrupting={interfaceSwitch.starting}
			onRetry={() => retry("strict")}
			onUseProviderHistory={() => retry("provider_history")}
			recoveryError={interfaceSwitch.startError}
			retrying={interfaceSwitch.starting}
		/> : null}
	</>;
	const dialogs = <>
		<SessionInterfaceSwitchDialog
			open={dialogOpen}
			target={dialogScope?.targetMode ?? target}
			requireExplicitTerminalStop={cloudTerminalToChat && !(busy || dialogScope?.sourceBusy)}
			waitingForInput={waitingForInput || dialogScope?.sourceWaitingForInput}
			busy={interfaceSwitch.starting}
			error={interfaceSwitch.startError}
			onOpenChange={(open) => { if (!open) setDialogScope(undefined); }}
			onChoose={choosePolicy}
		/>
		<ConfirmDialog
			open={draftConfirmation?.owner === owner}
			title={t("chat.draftDiscard.title")}
			description={<p className="whitespace-pre-line">{chatDraftDiscardWarning(draftConfirmation?.boundaries ?? [])}</p>}
			confirmLabel={t("chat.draftDiscard.leave")}
			destructive
			onConfirm={() => settleUnsafeDraftLeave(true)}
			onOpenChange={(open) => { if (!open) settleUnsafeDraftLeave(false); }}
		/>
	</>;

	return {
		activeTransition,
		agentInputDisabled: Boolean((interfaceSwitch.starting || activeTransition) && !cloudDrainWaiting && session?.mode === "tui"),
		cloudLoader,
		confirmUnsafeDraftLeave,
		controllerTransitioning,
		dialogs,
		draftBoundaries,
		inlineStatus,
		menuItem,
		newWorkDisabled,
		notice,
		onConversationWorkChange,
		renderedMode: interfaceSwitch.transition?.phase === "failed" ? interfaceSwitch.transition.sourceMode : session?.mode,
		target,
		unsupported,
	};
}
