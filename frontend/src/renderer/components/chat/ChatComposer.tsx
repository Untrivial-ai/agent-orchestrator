/**
 * The Chat composer.
 *
 * Submitting is a typed send, not a keystroke: there is no notion of "press
 * Enter at the agent" here, and an empty message is never a way to nudge it.
 *
 * A message typed mid-turn is held by the daemon and sent when the turn ends,
 * because the agent is one conversation and cannot run a second turn alongside
 * the first. The placeholder says so rather than leaving the user to guess where
 * their text went, and the queued message stays visible in the timeline.
 *
 * The model, reasoning effort and approval controls belong here rather than in
 * settings because the provider takes all three per turn: choosing one changes the
 * next message and never restarts the agent.
 *
 * Three completions live in the editor — `/` for AO commands and the agent's own
 * skills, `@` for worktree files, and pasted or dropped files. Completed skills
 * and paths are atomic inline chips but serialize to the plain text the agent
 * expects. The original keyboard contract remains: Enter sends, Shift+Enter makes
 * a newline, and ordinary typing stays local to the editor instead of rerendering
 * the surrounding chat surface.
 *
 * Every affordance is conditional on being able to deliver. The `/` menu only opens
 * when the provider actually reported skills, and the attach control only appears
 * when a caller supplied somewhere to put the bytes — a control that cannot do what
 * it says should not be drawn.
 */

import {
	cloneElement,
	isValidElement,
	useCallback,
	useEffect,
	useId,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
	type ClipboardEvent,
	type DragEvent,
	type FormEvent,
	type KeyboardEvent,
	type ReactNode,
} from "react";
import { ArrowUp, Command, CornerDownLeft, Loader2, Plus, Square, X } from "lucide-react";
import { Button } from "../ui/button";
import { cn } from "../../lib/utils";
import { ComposerSuggestMenu } from "./ComposerSuggestMenu";
import {
	ComposerEditor,
	type ComposerEditorHandle,
	type ComposerEditorSnapshot,
	type ComposerTrigger,
} from "./ComposerEditor";
import { moveHighlight, rankFiles, rankSkills, type Suggestion } from "./composerSuggest";
import {
	isSupportedImageAttachment,
	useFileAttachments,
	type FileAttachment,
	type FileAttachmentPayload,
} from "../../hooks/useFileAttachments";
import { File } from "lucide-react";
import type { ChatSkill, ChatSteerOutcome } from "../../types/conversation";
import {
	acknowledgeChatComposerMutation,
	beginChatComposerMutation,
	cancelChatComposerMutation,
	chatDraftScopeKey,
	clearAcceptedChatComposer,
	clearRejectedChatComposerDelivery,
	clearUncertainChatComposerDelivery,
	finishChatComposerMutation,
	getChatComposerMutation,
	markChatComposerDeliveryAccepted,
	prepareChatComposerDelivery,
	readChatSessionDraft,
	subscribeChatDraftRuntime,
	writeChatAttachments,
	writeChatComposerText,
	type ChatDraftMutationToken,
	type ChatComposerDelivery,
	type ChatDraftScope,
	type DraftClearResult,
} from "../../lib/chat-drafts";
import { setChatDraftBoundary } from "../../lib/chat-draft-boundary";

/**
 * Tell the agent to open the attached files. Mirrors the wording spawn uses for a task
 * brief, so the same instruction reaches the agent whether a file was attached at
 * spawn or mid-conversation.
 */
function withAttachmentReferences(text: string, paths: string[]): string {
	if (paths.length === 0) return text;
	const lead = text.trim() === "" ? "" : `${text}\n\n`;
	return `${lead}Attached files (read these files in the workspace):\n${paths.map((path) => `- ${path}`).join("\n")}`;
}

function restoredDeliveryNotice(delivery: ChatComposerDelivery | undefined): string | null {
	if (!delivery) return null;
	if (delivery.state === "accepted") {
		return "Message was accepted, but its local draft still needs to be cleared.";
	}
	return delivery.kind === "steer"
		? "AO can’t determine whether the agent may already have received this guidance before Chat restarted. Retry safely with the same delivery ID, or abandon recovery to edit it. Sending it again after abandonment may duplicate it."
		: "Message delivery wasn’t confirmed before Chat restarted. Retry safely to reuse the same delivery ID.";
}

export function ChatComposer({
	onSend,
	busy,
	willQueue,
	disabled,
	settings,
	approval,
	skills = [],
	filePaths = [],
	filePathsTruncated,
	onStageAttachments,
	nativeImages,
	onSteer,
	onInterrupt,
	canSteer,
	steerPending,
	steerRefusal,
	draftSeed,
	commandError,
	attachedTop = false,
	queuedDock,
	onCompact,
	compacting,
	compactUnavailable,
	compactBlocked,
	autoFocusKey,
	autoFocus = true,
	draftSessionId,
	draftSessionIncarnation,
	acceptedClientMessageIds,
}: {
	onSend: (
		text: string,
		attachments?: FileAttachmentPayload[],
		clientMessageId?: string,
	) => void | Promise<unknown>;
	settings?: ReactNode;
	/** A provider decision that temporarily replaces ordinary message entry. */
	approval?: ReactNode;
	/** A send is in flight. */
	busy?: boolean;
	/** The agent is mid-turn, so this message is held until the turn ends. */
	willQueue?: boolean;
	disabled?: boolean;
	/** The provider's skills. Empty leaves `/` an ordinary character. */
	skills?: ChatSkill[];
	/** Worktree-relative paths offered for `@`. Empty leaves `@` ordinary. */
	filePaths?: string[];
	/** The path list was capped, so the menu says so rather than implying it is all. */
	filePathsTruncated?: boolean;
	/**
	 * Writes staged files into the worktree and answers with the paths the agent
	 * can open. Absent means files cannot be delivered, and no attach control is
	 * offered at all.
	 */
	onStageAttachments?: (attachments: FileAttachmentPayload[]) => Promise<string[]>;
	/** Send the same staged bytes as native ACP image blocks when negotiated. */
	nativeImages?: boolean;
	/**
	 * Deliver this text into the turn already running. Absent means the harness
	 * cannot steer and the choice is never offered.
	 */
	onSteer?: (text: string, clientMessageId?: string) => Promise<ChatSteerOutcome | void>;
	/** Stop the turn already running when there is no draft to send. */
	onInterrupt?: () => void;
	/** A turn is actually running, so there is something to steer into. */
	canSteer?: boolean;
	steerPending?: boolean;
	/** Why the last steer was refused. */
	steerRefusal?: string;
	/** A selected history message to load into the composer as a new draft. */
	draftSeed?: { id: string; text: string };
	/** A failed send, approval, interrupt, or settings mutation. */
	commandError?: string;
	/** A queued-message dock owns the shared rounded top edge. */
	attachedTop?: boolean;
	/** Queued messages rendered above the composer. */
	queuedDock?: ReactNode;
	/** Run AO's built-in `/compact` command instead of sending it to the agent. */
	onCompact?: () => void | Promise<unknown>;
	/** The provider is already compacting this conversation. */
	compacting?: boolean;
	/** A typed provider refusal from the last compaction attempt. */
	compactUnavailable?: string;
	/** A running turn must be stopped before its history can be compacted. */
	compactBlocked?: boolean;
	/** Changes when the owning chat surface should reclaim composer focus. */
	autoFocusKey?: string;
	/** Whether this composer is currently visible and should take focus. */
	autoFocus?: boolean;
	/** Durable AO session identity used to scope unsent composer state. */
	draftSessionId?: string;
	/** Immutable daemon identity for this exact incarnation of the session id. */
	draftSessionIncarnation?: string;
	/** Client ids already present in daemon-authoritative conversation history. */
	acceptedClientMessageIds?: ReadonlySet<string>;
}) {
	const draftScope = useMemo<ChatDraftScope | undefined>(
		() =>
			draftSessionId
				? {
						sessionId: draftSessionId,
						incarnation: draftSessionIncarnation ?? draftSessionId,
					}
				: undefined,
		[draftSessionId, draftSessionIncarnation],
	);
	const draftScopeKey = draftScope ? chatDraftScopeKey(draftScope) : undefined;
	const [hasText, setHasText] = useState(false);
	const hasTextRef = useRef(false);
	const [trigger, setTrigger] = useState<ComposerTrigger>();
	/**
	 * The trigger position the user dismissed with Escape. Held so the menu stays
	 * shut for the completion they rejected, while a new `/` or `@` still opens one.
	 */
	const [dismissedKey, setDismissedKey] = useState<string | null>(null);
	const dismissedKeyRef = useRef<string | null>(null);
	const [highlighted, setHighlighted] = useState(0);
	const highlightedRef = useRef(0);
	const [isComposing, setIsComposing] = useState(false);
	const [dragging, setDragging] = useState(false);
	const [sendError, setSendError] = useState<string | null>(null);
	const [steerOutcomeNotice, setSteerOutcomeNotice] = useState<string | null>(null);
	const [deliveryRecoveryNotice, setDeliveryRecoveryNotice] = useState<string | null>(null);
	const [deliveryUncertain, setDeliveryUncertain] = useState(
		() =>
			draftScope !== undefined &&
			readChatSessionDraft(draftScope).composer.delivery?.state === "dispatching",
	);
	const [textDraftPersistenceError, setTextDraftPersistenceError] = useState<string | null>(null);
	const [attachmentDraftPersistenceError, setAttachmentDraftPersistenceError] = useState<
		string | null
	>(null);
	const [submitting, setSubmitting] = useState(false);
	const [durableDelivery, setDurableDelivery] = useState<ChatComposerDelivery | undefined>(() =>
		draftScope
			? readChatSessionDraft(draftScope).composer.delivery
			: undefined,
	);
	// The DOM event is the source of truth while React catches up with the draft
	// transition. This keeps Enter-after-fast-typing from observing stale state.
	const textRef = useRef("");
	/**
	 * What Enter does while the agent is working.
	 *
	 * Queueing is the safe default and matches `ao send`: the daemon records the
	 * message durably and dispatches it when the current turn finishes. Steering is
	 * timing-sensitive and changes the running turn, so it stays an explicit choice.
	 */

	const editor = useRef<ComposerEditorHandle>(null);
	const filePicker = useRef<HTMLInputElement>(null);
	const menuId = useId();
	const previousTrigger = useRef<ComposerTrigger | undefined>(undefined);
	const triggerRef = useRef<ComposerTrigger | undefined>(undefined);
	const automaticDeliveryRecoveryAttempted = useRef<string | undefined>(undefined);
	const restoredSeedKey = useRef<string | undefined>(undefined);
	const restoredSessionId = useRef<string | undefined>(undefined);
	const persistedDraft = useMemo(
		() => (draftScope ? readChatSessionDraft(draftScope) : undefined),
		[draftScope],
	);
	const subscribeComposerMutation = useCallback(
		(listener: () => void) => subscribeChatDraftRuntime(draftScope ?? "", listener),
		[draftScope],
	);
	const getComposerMutation = useCallback(
		() => getChatComposerMutation(draftScope ?? ""),
		[draftScope],
	);
	const composerMutation = useSyncExternalStore(
		subscribeComposerMutation,
		getComposerMutation,
		getComposerMutation,
	);
	const [appliedAcceptanceSequence, setAppliedAcceptanceSequence] = useState(0);
	const composerRevision = useRef(persistedDraft?.composer.revision ?? 0);
	const synchronouslyClearedDeliveryRevision = useRef<number | undefined>(undefined);
	const restoredAttachments = useMemo<FileAttachment[]>(
		() =>
			persistedDraft?.composer.attachments.map((attachment) => ({
				id: attachment.id,
				name: attachment.name,
				mimeType: attachment.mimeType,
				bytes: attachment.bytes,
				stagedPath: attachment.path,
			})) ?? [],
		[persistedDraft],
	);
	const persistAttachments = useCallback(
		(attachments: FileAttachment[]) => {
			if (!draftScope) return;
			const result = writeChatAttachments(
				draftScope,
				attachments.flatMap((attachment) =>
					attachment.stagedPath
						? [{
								id: attachment.id,
								path: attachment.stagedPath,
								name: attachment.name,
								mimeType: attachment.mimeType,
								bytes: attachment.bytes,
							}]
						: [],
				),
			);
			composerRevision.current = result.draft.composer.revision;
			setAttachmentDraftPersistenceError(
				result.ok
					? null
					: "Draft couldn’t be saved. Keep this chat open or copy it before leaving.",
			);
		},
		[draftScope],
	);
	const prepareAttachments = useCallback(
		async (attachments: FileAttachment[]): Promise<FileAttachment[]> => {
			if (!onStageAttachments) throw new Error("Attachment staging is unavailable");
			const paths = await onStageAttachments(
				attachments.flatMap(({ mimeType, data }) =>
					data ? [{ mimeType, data }] : [],
				),
			);
			if (paths.length !== attachments.length) {
				throw new Error("Attachment staging returned an incomplete result");
			}
			return attachments.map((attachment, index) => ({
				...attachment,
				stagedPath: paths[index],
			}));
		},
		[onStageAttachments],
	);
	useEffect(() => {
		if (restoredSessionId.current === draftScopeKey) return;
		restoredSessionId.current = draftScopeKey;
		synchronouslyClearedDeliveryRevision.current = undefined;
		const currentDraft = draftScope ? readChatSessionDraft(draftScope) : undefined;
		composerRevision.current = currentDraft?.composer.revision ?? 0;
		setDurableDelivery(currentDraft?.composer.delivery);
		setAppliedAcceptanceSequence(0);
		setTextDraftPersistenceError(null);
		setDeliveryRecoveryNotice(restoredDeliveryNotice(currentDraft?.composer.delivery));
		setDeliveryUncertain(currentDraft?.composer.delivery?.state === "dispatching");
		setAttachmentDraftPersistenceError(null);
		automaticDeliveryRecoveryAttempted.current = undefined;
	}, [draftScope, draftScopeKey]);
	const fileAttachments = useFileAttachments({
		initialAttachments: restoredAttachments,
		initialKey: draftScopeKey,
		prepareAttachments: onStageAttachments ? prepareAttachments : undefined,
		onAttachmentsChange: persistAttachments,
	});
	const canAttach = Boolean(onStageAttachments);

	const slashCommands = useMemo<ChatSkill[]>(() => {
		if (!onCompact || compactUnavailable === "This agent cannot compact its history") return skills;
		return [
			{
				name: "compact",
				displayName: "compact",
				description: "Summarize earlier history to reclaim context",
				source: "AO",
			},
			...skills.filter((skill) => skill.name !== "compact"),
		];
	}, [compactUnavailable, onCompact, skills]);

	const suggestionsFor = useCallback((currentTrigger?: ComposerTrigger): Suggestion[] => {
		if (!currentTrigger || currentTrigger.key === dismissedKeyRef.current) return [];
		// An empty candidate list is the whole reason the sigil stays ordinary: with
		// no commands or skills there is nothing to open, so `/` types a slash.
		if (currentTrigger.kind === "skill") {
			return rankSkills(slashCommands, currentTrigger.query);
		}
		return rankFiles(filePaths, currentTrigger.query);
	}, [slashCommands, filePaths]);

	const suggestions: Suggestion[] = useMemo(
		() => suggestionsFor(trigger),
		[trigger, dismissedKey, suggestionsFor],
	);

	const menuOpen = suggestions.length > 0;
	// Clamped rather than trusted: the list re-ranks on every keystroke, so the
	// index from the previous list can point past the end of this one.
	const activeIndex = Math.min(highlighted, suggestions.length - 1);

	const staged = fileAttachments.attachments.length > 0;
	const hasDraft = hasText || staged;
	const acceptedMutationWaiting = Boolean(
		composerMutation.accepted &&
			composerMutation.accepted.sequence > appliedAcceptanceSequence,
	);
	const acceptedClearFailed = composerMutation.accepted?.result.ok === false;
	const draftMutationPending =
		submitting || composerMutation.pending || acceptedMutationWaiting || Boolean(durableDelivery);
	const canRecoverDelivery = Boolean(
		durableDelivery &&
			(!busy || durableDelivery.state === "accepted") &&
			!disabled &&
			!steerPending &&
			!submitting &&
			!composerMutation.pending,
	);
	const canAbandonUncertainSteer = Boolean(
		deliveryUncertain &&
			durableDelivery?.kind === "steer" &&
			durableDelivery.state === "dispatching" &&
			draftScope &&
			!submitting,
	);
	const canSend =
		(hasText || staged) &&
		!busy &&
		!disabled &&
		!steerPending &&
		!draftMutationPending &&
			!acceptedClearFailed &&
			!durableDelivery &&
			!fileAttachments.preparing;
	const sendActionEnabled = canSend || canRecoverDelivery;
	const sendActionLabel = durableDelivery
		? durableDelivery.state === "accepted"
			? "Finish clearing accepted message"
			: "Retry message safely"
		: "Send message";
	const canStopTurn = Boolean(willQueue && onInterrupt && !disabled && !hasDraft);
	// Steering delivers text only, so a draft carrying files cannot take that
	// path. Treating it as unavailable — rather than steering the text and
	// dropping the files, or refusing on an empty body with attachments staged —
	// keeps the armed state something the composer can actually honour.
	const canSteerDraft = Boolean(canSteer && onSteer) && !staged;
	const sendHint = menuOpen
		? "Enter to insert"
		: willQueue && canSteerDraft
			? "⏎ queue · ⌘⏎ steer"
			: willQueue
				? "⏎ queue"
				: "Enter to send";
	const persistedText = persistedDraft?.composer.text;
	const draftSeedId = draftSeed?.id ?? (draftScopeKey ? `session:${draftScopeKey}` : undefined);
	const draftSeedText = draftSeed?.text ?? persistedText;
	const draftPersistenceError =
		textDraftPersistenceError ?? attachmentDraftPersistenceError;

	useEffect(() => {
		if (!draftSessionId) return;
		const deliveryWasSynchronouslyCleared = Boolean(
			durableDelivery &&
				synchronouslyClearedDeliveryRevision.current === durableDelivery.revision,
		);
		setChatDraftBoundary(
			draftSessionId,
			"composer",
			[
				...(draftPersistenceError && !deliveryWasSynchronouslyCleared
					? (["persistence-failed"] as const)
					: []),
				...(durableDelivery && !deliveryWasSynchronouslyCleared
					? (["pending-delivery"] as const)
					: []),
				...(fileAttachments.preparing ? (["pending-attachments"] as const) : []),
			],
		);
	}, [draftPersistenceError, draftSessionId, durableDelivery, fileAttachments.preparing]);

	useEffect(
		() => () => {
			if (draftSessionId) setChatDraftBoundary(draftSessionId, "composer", undefined);
		},
		[draftSessionId],
	);

	const focusEditor = useCallback(() => {
		if (!autoFocus || disabled) return;
		editor.current?.focus();
	}, [autoFocus, disabled]);

	useEffect(() => {
		focusEditor();
	}, [autoFocusKey, focusEditor]);

	useEffect(() => {
		if (!autoFocus) return;

		const onWindowFocus = () => focusEditor();
		const onVisibilityChange = () => {
			if (document.visibilityState === "visible") focusEditor();
		};

		window.addEventListener("focus", onWindowFocus);
		document.addEventListener("visibilitychange", onVisibilityChange);
		return () => {
			window.removeEventListener("focus", onWindowFocus);
			document.removeEventListener("visibilitychange", onVisibilityChange);
		};
	}, [autoFocus, focusEditor]);

	const clearEditorView = useCallback(() => {
		textRef.current = "";
		hasTextRef.current = false;
		setHasText(false);
		setTrigger(undefined);
		triggerRef.current = undefined;
		previousTrigger.current = undefined;
		dismissedKeyRef.current = null;
		setDismissedKey(null);
		highlightedRef.current = 0;
		setHighlighted(0);
		editor.current?.clear();
	}, []);

	const applyAcceptedDraftResult = useCallback(
		(result: DraftClearResult) => {
			setDurableDelivery(result.draft.composer.delivery);
			if (!result.ok) {
				setDeliveryRecoveryNotice(
					"Message was accepted, but its local draft still needs to be cleared.",
				);
				setTextDraftPersistenceError(
					"Message was accepted, but its local draft couldn’t be cleared. Retry clearing before leaving; AO will not send it again.",
				);
				return false;
			}
			composerRevision.current = result.draft.composer.revision;
			setTextDraftPersistenceError(null);
			setDeliveryRecoveryNotice(null);
			if (!result.cleared) return false;
			clearEditorView();
			fileAttachments.clear();
			return true;
		},
		[clearEditorView, fileAttachments],
	);

	const clearAcceptedDraft = useCallback(
		(acceptedRevision: number, mutationToken?: ChatDraftMutationToken) => {
			if (!draftScope) {
				clearEditorView();
				fileAttachments.clear();
				return true;
			}
			const result = clearAcceptedChatComposer(draftScope, acceptedRevision);
			// A successful durable clear can synchronously trigger a replacement
			// surface before React applies the acceptance receipt. Release the route
			// boundary first so cleared UI never exposes stale unsafe-draft state.
			if (result.ok && result.cleared && draftSessionId) {
				synchronouslyClearedDeliveryRevision.current = acceptedRevision;
				setChatDraftBoundary(draftSessionId, "composer", undefined);
			}
			if (mutationToken) {
				finishChatComposerMutation(
					draftScope,
					mutationToken,
					acceptedRevision,
					result,
				);
				return result.ok && result.cleared;
			}
			return applyAcceptedDraftResult(result);
		},
		[
			applyAcceptedDraftResult,
			clearEditorView,
			draftScope,
			draftSessionId,
			fileAttachments,
		],
	);

	const acceptAndClearDurableDelivery = useCallback(
		(delivery: ChatComposerDelivery, mutationToken?: ChatDraftMutationToken) => {
			if (!draftScope) return clearAcceptedDraft(delivery.revision, mutationToken);
			const accepted = markChatComposerDeliveryAccepted(
				draftScope,
				delivery.clientMessageId,
				delivery.revision,
			);
			if (!accepted.ok) {
				if (mutationToken) cancelChatComposerMutation(draftScope, mutationToken);
				setDurableDelivery(delivery);
				setTextDraftPersistenceError(
					"Message acceptance couldn’t be recorded locally. Retry safely to reconcile it with the same delivery ID.",
				);
				return false;
			}
			const acceptedDelivery = accepted.draft.composer.delivery ?? {
				...delivery,
				state: "accepted" as const,
			};
			setDurableDelivery(acceptedDelivery);
			return clearAcceptedDraft(delivery.revision, mutationToken);
		},
		[clearAcceptedDraft, draftScope],
	);

	const abandonUncertainSteer = useCallback(() => {
		if (!draftScope || !durableDelivery || durableDelivery.kind !== "steer") return;
		const result = clearUncertainChatComposerDelivery(
			draftScope,
			durableDelivery.clientMessageId,
			durableDelivery.revision,
		);
		setDurableDelivery(result.draft.composer.delivery);
		if (!result.ok) {
			setTextDraftPersistenceError(
				"Recovery couldn’t be abandoned because its local record could not be cleared. Nothing will be resent automatically.",
			);
			return;
		}
		setDeliveryUncertain(false);
		setTextDraftPersistenceError(null);
		setDeliveryRecoveryNotice(null);
		setSteerOutcomeNotice(
			"Recovery was abandoned. The earlier guidance may already have been delivered; sending this draft now may duplicate it.",
		);
	}, [draftScope, durableDelivery]);

	useEffect(() => {
		const accepted = composerMutation.accepted;
		if (!accepted || accepted.sequence <= appliedAcceptanceSequence) return;
		applyAcceptedDraftResult(accepted.result);
		setAppliedAcceptanceSequence(accepted.sequence);
		if (draftScope) acknowledgeChatComposerMutation(draftScope, accepted.sequence);
	}, [
		appliedAcceptanceSequence,
		applyAcceptedDraftResult,
		composerMutation.accepted,
		draftScope,
	]);

	useEffect(() => {
		// A concurrent replacement can render this memoized seed, remain disconnected,
		// and commit only after another surface accepted and cleared the draft. Read the
		// session record again at the effect/commit boundary so acknowledgement cannot
		// turn the runtime snapshot into an ABA that resurrects accepted text or staged
		// attachment descriptors.
		const committedDraft = draftScope ? readChatSessionDraft(draftScope) : undefined;
		if (committedDraft) {
			fileAttachments.reconcilePersistedAttachments(
				committedDraft.composer.attachments.map((attachment) => ({
					id: attachment.id,
					name: attachment.name,
					mimeType: attachment.mimeType,
					bytes: attachment.bytes,
					stagedPath: attachment.path,
				})),
			);
		}
		const committedSeedText =
			draftSeed?.text ??
			(committedDraft ? committedDraft.composer.text : draftSeedText);
		if (committedSeedText === undefined) {
			restoredSeedKey.current = undefined;
			return;
		}
		const seedKey = JSON.stringify([draftSeedId, committedSeedText]);
		if (restoredSeedKey.current === seedKey) return;
		restoredSeedKey.current = seedKey;
		textRef.current = committedSeedText;
		hasTextRef.current = committedSeedText.trim().length > 0;
		setHasText(hasTextRef.current);
		editor.current?.setText(committedSeedText);
		dismissedKeyRef.current = null;
		setDismissedKey(null);
		highlightedRef.current = 0;
		setHighlighted(0);
		setSendError(null);
		setSteerOutcomeNotice(null);
		// A history action intentionally creates a new draft and must be persisted.
		// A session restore is already durable; writing it again here needlessly
		// changes the accepted-send revision during mount.
		if (draftScope && draftSeed) {
			const result = writeChatComposerText(draftScope, committedSeedText);
			composerRevision.current = result.draft.composer.revision;
			setTextDraftPersistenceError(
				result.ok
					? null
					: "Draft couldn’t be saved. Keep this chat open or copy it before leaving.",
			);
		}
	}, [
		draftScope,
		draftSeed,
		draftSeedId,
		draftSeedText,
		fileAttachments.reconcilePersistedAttachments,
	]);

	useEffect(() => {
		if (!durableDelivery || !draftScope) return;
		const observedSteer =
			durableDelivery.kind === "steer" &&
			acceptedClientMessageIds?.has(durableDelivery.clientMessageId);
		if (durableDelivery.state !== "accepted" && !observedSteer) return;
		if (automaticDeliveryRecoveryAttempted.current === durableDelivery.clientMessageId) return;
		automaticDeliveryRecoveryAttempted.current = durableDelivery.clientMessageId;
		acceptAndClearDurableDelivery(durableDelivery);
	}, [
		acceptAndClearDurableDelivery,
		acceptedClientMessageIds,
		draftScope,
		durableDelivery,
	]);

	const approvalActive = Boolean(approval);
	const previousApprovalActive = useRef(approvalActive);
	useEffect(() => {
		const wasActive = previousApprovalActive.current;
		previousApprovalActive.current = approvalActive;
		if (!wasActive || approvalActive) return;
		editor.current?.setText(textRef.current);
	}, [approvalActive]);

	const onEditorChange = useCallback((snapshot: ComposerEditorSnapshot) => {
		textRef.current = snapshot.text;
		if (draftScope) {
			const result = writeChatComposerText(draftScope, snapshot.text);
			composerRevision.current = result.draft.composer.revision;
			// A disabled Lexical editor can still publish an internal state update
			// while its editability changes. It must not erase the recovery notice
			// for a durable delivery that still owns this exact composer revision.
			if (!result.draft.composer.delivery) {
				setTextDraftPersistenceError(
					result.ok
						? null
						: "Draft couldn’t be saved. Keep this chat open or copy it before leaving.",
				);
			}
		}
		if (hasTextRef.current !== snapshot.hasText) {
			hasTextRef.current = snapshot.hasText;
			setHasText(snapshot.hasText);
		}

		const previous = previousTrigger.current;
		const next = snapshot.trigger;
		triggerRef.current = next;
		if (
			previous?.key !== next?.key ||
			previous?.end !== next?.end ||
			previous?.query !== next?.query ||
			previous?.kind !== next?.kind
		) {
			highlightedRef.current = 0;
			setHighlighted(0);
			previousTrigger.current = next;
			setTrigger(next);
		}
		if (dismissedKeyRef.current && next?.key !== dismissedKeyRef.current) {
			dismissedKeyRef.current = null;
			setDismissedKey(null);
		}
	}, [draftScope]);

	const pick = useCallback((value: string) => {
		const currentTrigger = triggerRef.current;
		if (!currentTrigger) return;
		editor.current?.insertToken(currentTrigger, value);
		triggerRef.current = undefined;
		previousTrigger.current = undefined;
		setTrigger(undefined);
		highlightedRef.current = 0;
		setHighlighted(0);
		dismissedKeyRef.current = null;
		setDismissedKey(null);
	}, []);

	useEffect(() => {
		if (isComposing || !trigger || trigger.kind !== "skill" || trigger.key === dismissedKey) return;
		const query = trigger.query.toLowerCase();
		if (!query) return;
		const exact = slashCommands.find((skill) => skill.name.toLowerCase() === query);
		if (!exact) return;

		// Do not eagerly accept a skill whose full name is also the start of another
		// skill. The user must still be able to type `/review-pr` when `/review`
		// exists; Enter remains available to accept the shorter exact match.
		const hasLongerPrefix = slashCommands.some((skill) => {
			const name = skill.name.toLowerCase();
			return name.length > query.length && name.startsWith(query);
		});
		if (!hasLongerPrefix) pick(exact.name);
	}, [dismissedKey, isComposing, pick, slashCommands, trigger]);

	const completeFromEditor = useCallback(
		(snapshot: ComposerEditorSnapshot, key: "Enter" | "Tab"): string | undefined => {
			const currentTrigger = snapshot.trigger;
			if (!currentTrigger) return undefined;
			if (key === "Enter" && snapshot.text.trim() === "/compact" && onCompact) {
				return undefined;
			}
			const matches = suggestionsFor(currentTrigger);
			const chosen = matches[Math.min(highlightedRef.current, matches.length - 1)];
			if (!chosen) return undefined;
			triggerRef.current = currentTrigger;
			highlightedRef.current = 0;
			dismissedKeyRef.current = null;
			return chosen.value;
		},
		[onCompact, suggestionsFor],
	);

	async function submit(event?: FormEvent, forceSteer?: boolean) {
		event?.preventDefault();
		const currentText = textRef.current;
		const recoveringDelivery = durableDelivery;
		const canSubmitNow =
			((currentText.trim().length > 0 || staged) || Boolean(recoveringDelivery)) &&
			(!busy || recoveringDelivery?.state === "accepted") &&
			!disabled &&
			!steerPending &&
			!submitting &&
			!composerMutation.pending &&
			(!draftMutationPending || Boolean(recoveringDelivery)) &&
			(Boolean(recoveringDelivery) ||
				getChatComposerMutation(draftScope ?? "").accepted?.result.ok !== false) &&
			!fileAttachments.preparing;
		if (!canSubmitNow) return;
		setSendError(null);
		setSteerOutcomeNotice(null);

		const shouldSteer = forceSteer ?? false;
		const body = currentText.trim();
		const acceptedPaths = fileAttachments.attachments.flatMap((attachment) =>
			attachment.stagedPath ? [attachment.stagedPath] : [],
		);

		if (!recoveringDelivery && body === "/compact" && onCompact) {
			const mutationToken = draftScope
				? beginChatComposerMutation(draftScope)
				: undefined;
			if (draftScope && !mutationToken) return;
			let mutationFinished = false;
			setSubmitting(true);
			try {
				if (compactBlocked) {
					setSendError("Stop the current turn before compacting.");
					return;
				}
				if (compacting) {
					setSendError("Conversation history is already being compacted.");
					return;
				}
				if (compactUnavailable) {
					setSendError(compactUnavailable);
					return;
				}
				await onCompact();
				clearAcceptedDraft(composerRevision.current, mutationToken);
				mutationFinished = true;
				setDismissedKey(null);
				setHighlighted(0);
			} catch {
				setSendError("Conversation history could not be compacted. Try again.");
			} finally {
				if (draftScope && mutationToken && !mutationFinished) {
					cancelChatComposerMutation(draftScope, mutationToken);
				}
				setSubmitting(false);
			}
			return;
		}

		if (!draftScope) {
			setSubmitting(true);
			try {
				if (shouldSteer && onSteer) {
					if (body === "") return;
					await onSteer(body);
				} else if (staged && onStageAttachments) {
					if (acceptedPaths.length !== fileAttachments.attachments.length) {
						setSendError("The files are not durably available. Nothing was sent.");
						return;
					}
					const message = withAttachmentReferences(body, acceptedPaths);
					const nativePayloads = fileAttachments
						.toPayload()
						.filter((attachment) => isSupportedImageAttachment(attachment.mimeType));
					if (nativeImages && nativePayloads.length > 0) await onSend(message, nativePayloads);
					else await onSend(message);
				} else {
					await onSend(body);
				}
				clearEditorView();
				fileAttachments.clear();
			} catch {
				setSendError(
					staged
						? "Message not sent. Your draft and attachments were kept so you can retry."
						: "Message not sent. Your draft was kept so you can retry.",
				);
			} finally {
				setSubmitting(false);
			}
			return;
		}

		if (!recoveringDelivery && staged && acceptedPaths.length !== fileAttachments.attachments.length) {
			setSendError("The files are not durably available. Nothing was sent.");
			return;
		}
		const requestText = recoveringDelivery
			? recoveringDelivery.requestText
			: shouldSteer
				? body
				: withAttachmentReferences(body, acceptedPaths);
		const prepared = prepareChatComposerDelivery(draftScope, {
			kind: recoveringDelivery?.kind ?? (shouldSteer ? "steer" : "send"),
			composerText: currentText,
			attachments: fileAttachments.attachments.flatMap((attachment) =>
				attachment.stagedPath
					? [{
							id: attachment.id,
							path: attachment.stagedPath,
							name: attachment.name,
							mimeType: attachment.mimeType,
							bytes: attachment.bytes,
						}]
					: [],
			),
			requestText,
			clientMessageId: recoveringDelivery?.clientMessageId ?? crypto.randomUUID(),
		});
		if (!prepared.ok) {
			setTextDraftPersistenceError(
				"This exact draft and its recovery ID couldn’t be saved locally. Nothing was sent. Restore local storage and try again.",
			);
			return;
		}
		const delivery = prepared.mutation;
		synchronouslyClearedDeliveryRevision.current = undefined;
		setDeliveryUncertain(false);
		composerRevision.current = prepared.draft.composer.revision;
		setDurableDelivery(delivery);
		setTextDraftPersistenceError(null);
		setDeliveryRecoveryNotice(
			delivery.state === "accepted"
				? "Message was accepted, but its local draft still needs to be cleared."
				: null,
		);
		if (delivery.state === "accepted") {
			acceptAndClearDurableDelivery(delivery);
			return;
		}

		const mutationToken = beginChatComposerMutation(draftScope);
		if (!mutationToken) return;
		let mutationFinished = false;
		setSubmitting(true);
		try {
			if (delivery.kind === "steer") {
				if (!onSteer) throw new Error("Steering is unavailable");
				const outcome = await onSteer(delivery.requestText, delivery.clientMessageId);
				if (outcome?.status === "not-accepted") {
					const cleared = clearRejectedChatComposerDelivery(
						draftScope,
						delivery.clientMessageId,
						delivery.revision,
					);
					setDurableDelivery(cleared.draft.composer.delivery);
					composerRevision.current = cleared.draft.composer.revision;
					if (cleared.ok) {
						setTextDraftPersistenceError(null);
						setDeliveryRecoveryNotice(null);
						setSteerOutcomeNotice(outcome.reason);
					} else {
						setTextDraftPersistenceError(
							"The steer was refused, but its local recovery record couldn’t be cleared. Nothing will be resent automatically.",
						);
					}
					return;
				}
			} else {
				const nativePayloads = prepared.recovered
					? []
					: fileAttachments
							.toPayload()
							.filter((attachment) => isSupportedImageAttachment(attachment.mimeType));
				await onSend(
					delivery.requestText,
					nativeImages && nativePayloads.length > 0 ? nativePayloads : undefined,
					delivery.clientMessageId,
				);
			}
			acceptAndClearDurableDelivery(delivery, mutationToken);
			mutationFinished = true;
			setDismissedKey(null);
			setHighlighted(0);
		} catch {
			setDeliveryUncertain(true);
			setDeliveryRecoveryNotice(
				delivery.kind === "steer"
					? "AO can’t determine whether the agent may already have received this guidance. Retry safely with the same delivery ID, or abandon recovery to edit it. Sending it again after abandonment may duplicate it."
					: "Message delivery wasn’t confirmed. Retry safely to reuse the same delivery ID; your draft remains locked until it is reconciled.",
			);
		} finally {
			if (!mutationFinished) cancelChatComposerMutation(draftScope, mutationToken);
			setSubmitting(false);
		}
	}

	function onEditorEnter(
		snapshot: ComposerEditorSnapshot,
		event: globalThis.KeyboardEvent,
	): boolean {
		textRef.current = snapshot.text;
		const wantsSteer = (event.metaKey || event.ctrlKey) && canSteerDraft;
		void submit(undefined, wantsSteer);
		return true;
	}

	function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
		if (event.nativeEvent.isComposing) return;
		if (event.key === "Enter" && event.shiftKey) return;
		// Read Lexical directly here. A fast typist can press Enter before React has
		// rendered the state update for the last character, while the editor state is
		// already current. Using the rendered `menuOpen` in that window sent the draft
		// instead of accepting the visible completion.
		const liveSnapshot = editor.current?.getSnapshot();
		if (liveSnapshot) textRef.current = liveSnapshot.text;
		const liveTrigger = liveSnapshot?.trigger;
		const liveSuggestions = suggestionsFor(liveTrigger);
		if (liveSuggestions.length > 0) {
			// `/compact` takes no arguments. Once its exact name is present, Enter
			// executes it directly instead of merely accepting the highlighted row and
			// requiring a second Enter on the inserted trailing space.
			if (event.key === "Enter" && textRef.current.trim() === "/compact" && onCompact) {
				event.preventDefault();
				void submit();
				return;
			}
			// Only these keys are taken while the menu is open. Everything else falls
			// through to the editor, which is what keeps typing from being swallowed.
			if (event.key === "ArrowDown" || event.key === "ArrowUp") {
				event.preventDefault();
				const next = moveHighlight(
					Math.min(highlightedRef.current, liveSuggestions.length - 1),
					event.key === "ArrowDown" ? 1 : -1,
					liveSuggestions.length,
				);
				highlightedRef.current = next;
				setHighlighted(next);
				return;
			}
			if (event.key === "Enter" || event.key === "Tab") {
				event.preventDefault();
				triggerRef.current = liveTrigger;
				const chosen = liveSuggestions[Math.min(highlightedRef.current, liveSuggestions.length - 1)];
				if (chosen) pick(chosen.value);
				return;
			}
			if (event.key === "Escape") {
				event.preventDefault();
				// Stopped here so Escape closes the menu rather than travelling on to
				// whatever surface is hosting the composer.
				event.stopPropagation();
				dismissedKeyRef.current = liveTrigger?.key ?? null;
				setDismissedKey(dismissedKeyRef.current);
				return;
			}
		}

		// Enter queues; Shift+Enter makes a newline; Cmd/Ctrl+Enter steers.
		if (event.key !== "Enter") return;
		if (event.shiftKey) return;
		event.preventDefault();
		const wantsSteer = (event.metaKey || event.ctrlKey) && canSteerDraft;
		void submit(undefined, wantsSteer);
	}

	function onPaste(event: ClipboardEvent<HTMLDivElement>) {
		if (!canAttach || fileAttachments.preparing || draftMutationPending) return;
		const clipboard = event.clipboardData;
		const files = Array.from(clipboard?.files ?? []);
		if (files.length === 0) return;
		// The paste is only claimed when there is no text alongside the image: a copy
		// carrying both should still paste its text.
		const hasText = typeof clipboard?.getData === "function" && clipboard.getData("text/plain") !== "";
		if (!hasText) event.preventDefault();
		void fileAttachments.addFiles(files);
	}

	function onDrop(event: DragEvent<HTMLFormElement>) {
		setDragging(false);
		if (!canAttach || fileAttachments.preparing || draftMutationPending) return;
		const files = Array.from(event.dataTransfer?.files ?? []);
		if (files.length === 0) return;
		event.preventDefault();
		event.stopPropagation();
		void fileAttachments.addFiles(files);
	}

	const [metaHeld, setMetaHeld] = useState(false);
	useEffect(() => {
		const onKey = (e: globalThis.KeyboardEvent) => setMetaHeld(e.metaKey || e.ctrlKey);
		const onBlur = () => setMetaHeld(false);
		window.addEventListener("keydown", onKey);
		window.addEventListener("keyup", onKey);
		window.addEventListener("blur", onBlur);
		return () => {
			window.removeEventListener("keydown", onKey);
			window.removeEventListener("keyup", onKey);
			window.removeEventListener("blur", onBlur);
		};
	}, []);
	const activeDelivery = metaHeld && canSteerDraft ? "steer" : "queue";

	const attachmentError =
		fileAttachments.error ??
		draftPersistenceError ??
		deliveryRecoveryNotice ??
		sendError ??
		commandError;
	const deliveryChoice =
		canSteer && onSteer ? <DeliveryChoice value={activeDelivery} disabled={steerPending} /> : null;
	const settingsNode =
		settings && deliveryChoice && isValidElement(settings)
			? cloneElement(settings, undefined, deliveryChoice)
			: settings;

	if (approval) {
		return (
			<>
				{queuedDock}
				<form
					onSubmit={(event) => event.preventDefault()}
					data-attached-top={attachedTop || undefined}
					className="cursor-chat-composer relative flex flex-col gap-1.5 border border-border-strong px-3 py-3 transition-[background,border-color,box-shadow]"
				>
					{approval}
					{commandError ? (
						<p role="alert" className="px-1.5 text-[11px] leading-snug text-destructive">
							{commandError}
						</p>
					) : null}
				</form>
			</>
		);
	}

	return (
		<>
			{queuedDock}
			<form
				// Clicking send while Cmd/Ctrl is held has to mean what the indicator
				// beside it says. Reading the same armed state the chip paints keeps the
				// pointer and keyboard paths from disagreeing about where the message goes.
				onSubmit={(event) => void submit(event, activeDelivery === "steer")}
				onDragOver={(event) => {
					if (!canAttach) return;
					event.preventDefault();
					setDragging(true);
				}}
				onDragLeave={() => setDragging(false)}
				onDropCapture={onDrop}
				// The border colors for rest, hover, focus and drag are one set of states
				// on one surface, so they are declared together in CSS rather than half
				// here and half there.
				data-dragging={dragging || undefined}
				data-attached-top={attachedTop || undefined}
				onClick={(e) => {
					if (
						e.target === e.currentTarget ||
						!(e.target as HTMLElement).closest("button, a, [role='option'], ul")
					) {
						editor.current?.focus();
					}
				}}
				className="cursor-chat-composer relative flex cursor-text flex-col gap-1.5 border px-3 pt-3 pb-3 transition-[background,border-color,box-shadow]"
			>
				{menuOpen && trigger ? (
					<ComposerSuggestMenu
						id={menuId}
						kind={trigger.kind}
						items={suggestions}
						highlighted={activeIndex}
						onPick={pick}
						truncated={trigger?.kind === "file" && filePathsTruncated}
					/>
				) : null}

				{staged ? (
					<ul className="flex flex-wrap gap-1.5" aria-label="Attached files">
						{fileAttachments.attachments.map((file) => (
							<li
								key={file.id}
								className="flex items-center gap-1.5 rounded border border-border bg-background py-0.5 pl-0.5 pr-1"
							>
								{file.dataUrl ? (
									<img src={file.dataUrl} alt="" className="size-6 rounded-sm object-cover" />
								) : (
									<div className="flex size-6 items-center justify-center rounded-sm bg-surface">
										<File aria-hidden="true" className="size-3.5 text-muted-foreground" />
									</div>
								)}
								<span
									className="max-w-[120px] truncate text-[11px] text-muted-foreground"
									title={file.name}
								>
									{file.name}
								</span>
								<button
									type="button"
									onClick={() => fileAttachments.remove(file.id)}
									disabled={disabled || draftMutationPending || fileAttachments.preparing}
									aria-label={`Remove ${file.name}`}
									className="text-muted-foreground hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
								>
									<X aria-hidden="true" className="size-3" />
								</button>
							</li>
						))}
					</ul>
				) : null}

				<ComposerEditor
					ref={editor}
					disabled={disabled || draftMutationPending}
					label="Message the agent"
					placeholder={
						disabled
							? "The controller is not connected"
							: willQueue
								? "Agent is working — this sends when it finishes"
								: "Message the agent…"
					}
					menuOpen={menuOpen}
					menuId={menuId}
					activeIndex={activeIndex}
					onChange={onEditorChange}
					onComplete={completeFromEditor}
					onEnter={onEditorEnter}
					onCompositionChange={setIsComposing}
					onKeyDown={onKeyDown}
					onPaste={onPaste}
				/>

				{attachmentError ? (
					<p role="alert" className="px-1.5 text-[11px] leading-snug text-destructive">
						{attachmentError}
					</p>
				) : null}
				{canAbandonUncertainSteer ? (
					<div className="px-1.5">
						<Button
							type="button"
							variant="outline"
							size="sm"
							onClick={abandonUncertainSteer}
						>
							Abandon recovery
						</Button>
					</div>
				) : null}
				{fileAttachments.preparing ? (
					<p role="status" className="px-1.5 text-[11px] leading-snug text-muted-foreground">
						Saving attachments… Wait before leaving this chat.
					</p>
				) : null}

				{/* A refused steer is an ordinary outcome, not a failure: the text is still
			    in the box and the message says which of "send it instead" and "try again
			    in a moment" applies. */}
				{steerRefusal ?? steerOutcomeNotice ? (
					<p role="status" className="px-1.5 text-[11px] leading-snug text-warning">
						{steerRefusal ?? steerOutcomeNotice}
					</p>
				) : null}

				<div className="flex h-7 items-center gap-1.5">
					<div role="group" aria-label="Message tools" className="flex min-w-0 flex-1 items-center gap-0.5">
						{canAttach ? (
							<>
								<input
									ref={filePicker}
									type="file"
									multiple
									hidden
									onChange={(event) => {
										if (!disabled && !draftMutationPending && !fileAttachments.preparing) {
											void fileAttachments.addFiles(Array.from(event.target.files ?? []));
										}
										// Cleared so picking the same file twice still fires a change.
										event.target.value = "";
									}}
								/>
								<Button
									type="button"
									variant="ghost"
									size="icon-sm"
									disabled={disabled || draftMutationPending || fileAttachments.preparing}
									onClick={() => filePicker.current?.click()}
									aria-label="Attach a file"
									title="Attach a file"
									className="size-7 shrink-0 rounded-full p-0 text-muted-foreground hover:bg-white/5! hover:text-foreground"
								>
									<Plus aria-hidden="true" className="size-3.5 text-muted-foreground" />
								</Button>
							</>
						) : null}
						{settingsNode}
						{!settings && deliveryChoice}
					</div>

					<div role="group" aria-label="Send message controls" className="flex h-7 shrink-0 items-center">
						<Button
							type={canStopTurn ? "button" : "submit"}
							variant="ghost"
							size="icon-sm"
							disabled={canStopTurn ? false : !sendActionEnabled}
							onClick={canStopTurn ? onInterrupt : undefined}
							aria-label={canStopTurn ? "Stop turn" : sendActionLabel}
							// The destination Enter is armed with used to be spelled out beside
							// the button. The row reads better without a line of prose in it, but
							// the fact is not decoration, so it moves onto the control it
							// describes rather than being dropped.
							title={canStopTurn ? "Stop turn" : durableDelivery ? sendActionLabel : sendHint}
							className={cn(
								"size-7 rounded-full border-transparent focus-visible:ring-ring/40",
								canStopTurn || sendActionEnabled
									? "bg-foreground text-background hover:bg-foreground/90 hover:text-background dark:hover:bg-foreground/90 dark:hover:text-background"
									: "bg-primary text-primary-foreground",
							)}
						>
							{canStopTurn ? (
								<Square aria-hidden="true" className="size-2.5 fill-current" />
							) : steerPending ? (
								<Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
							) : (
								<ArrowUp aria-hidden="true" className="size-3.5" />
							)}
						</Button>
					</div>
				</div>
			</form>
		</>
	);
}

/**
 * Where the next message goes while the agent is working.
 *
 * Two words rather than a switch, because the difference is not a preference — it is
 * two different things happening to what the user typed. Steering joins the turn in
 * flight: the agent keeps its context, its reasoning and the command it has running,
 * and decides for itself what to abandon. Queueing waits for the turn to end and
 * then starts a new one from a cold start. For someone who has just realized they
 * asked for the wrong thing, that is the whole difference, so both are named and the
 * active choice is exposed visually and through `aria-pressed`.
 */
export function DeliveryChoice({ value, disabled }: { value: "steer" | "queue"; disabled?: boolean }) {
	return (
		// A group, not a status: these chips label what each keystroke will do,
		// they are not a live region announcing an event. `status` also made this
		// shadow the steer refusal below, which is the real one.
		<div
			role="group"
			aria-label="Where this message goes while the agent is working"
			className="flex h-7 shrink-0 items-center gap-1"
		>
			<span
				className={cn(
					"inline-flex h-7 items-center gap-1.5 rounded-lg px-3 text-sm leading-none transition-colors",
					disabled && "opacity-50",
					value === "queue" ? "bg-white/5 text-foreground" : "text-muted-foreground",
				)}
			>
				<span className="inline-flex items-center gap-0.5 text-muted-foreground">
					<CornerDownLeft aria-hidden="true" className="size-3" />
				</span>
				Queue
			</span>
			<span
				className={cn(
					"inline-flex h-7 items-center gap-1.5 rounded-lg px-3 text-sm leading-none transition-colors",
					disabled && "opacity-50",
					value === "steer" ? "bg-white/5 text-foreground" : "text-muted-foreground",
				)}
			>
				<span className="inline-flex items-center gap-1 text-muted-foreground">
					<Command aria-hidden="true" className="size-2.5" />
					<CornerDownLeft aria-hidden="true" className="size-3" />
				</span>
				Steer
			</span>
		</div>
	);
}
