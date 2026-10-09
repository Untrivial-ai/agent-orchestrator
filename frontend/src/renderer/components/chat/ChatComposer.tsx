import { useChatDraftTranslation } from "../../lib/chat-draft-messages";
/**
 * The Chat composer.
 *
 * Submitting is a typed send, not a keystroke: there is no notion of "press
 * Enter at the agent" here, and an empty message is never a way to nudge it.
 *
 * A message typed mid-turn is held by the daemon and sent when the turn ends,
 * because the agent is one conversation and cannot run a second turn alongside
 * the first. The placeholder says so rather than leaving the user to guess where
 * their text went, and the queued message stays visible only in the dock.
 *
 * The model, reasoning effort and approval controls belong here rather than in
 * settings because the provider takes all three per turn: choosing one changes the
 * next message and never restarts the agent.
 *
 * Three completions live in the editor — `/` for AO commands and the agent's own
 * skills, `@` for worktree files, and pasted or dropped files. Completed skills
 * and paths are atomic inline chips but serialize to the plain text the agent
 * expects. Enter sends by default; a UI preference reserves Enter for newlines
 * and sends with Cmd/Ctrl+Enter. Ordinary typing stays local to the editor instead
 * of rerendering the surrounding chat surface.
 *
 * Every affordance is conditional on being able to deliver. The `/` menu only opens
 * when the provider actually reported skills, and the attach control only appears
 * when a caller supplied somewhere to put the bytes — a control that cannot do what
 * it says should not be drawn.
 */

import {
	cloneElement,
	useCallback,
	useEffect,
	useId,
	useImperativeHandle,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
	isValidElement,
	memo,
	type ClipboardEvent,
	type DragEvent,
	type FormEvent,
	type KeyboardEvent,
	type ReactElement,
	type ReactNode,
	type Ref,
} from "react";
import { ArrowUp, CornerUpRight, Loader2, Plus, Square, X } from "lucide-react";
import { Button } from "../ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { ChatAnnotationSummary } from "./ChatAnnotationSummary";
import { cn } from "../../lib/utils";
import { useUiStore } from "../../stores/ui-store";
import { apiErrorCode, apiErrorMessage, getApiBaseUrl } from "../../lib/api-client";
import { ComposerSuggestMenu } from "./ComposerSuggestMenu";
import {
	ComposerEditor,
	type ComposerEditorHandle,
	type ComposerImage,
	type ComposerEditorSnapshot,
	type ComposerTrigger,
} from "./ComposerEditor";
import { moveHighlight, rankFiles, rankSkills, type Suggestion } from "./composerSuggest";
import {
	isSupportedImageAttachment,
	useFileAttachments,
	type FileAttachment,
	MAX_ATTACHMENTS,
	type FileAttachmentPayload,
} from "../../hooks/useFileAttachments";
import { File } from "lucide-react";
import type { ChatSkill, ChatSteerOutcome } from "../../types/conversation";
import {
	acknowledgeChatComposerMutation,
	beginChatComposerMutation,
	cancelChatComposerMutation,
	chatDraftScopeKey,
	chatQueuedAttachmentScopeKey,
	clearAcceptedChatComposer,
	clearRejectedChatComposerDelivery,
	clearUncertainChatComposerDelivery,
	finishChatComposerMutation,
	getChatComposerMutation,
	isChatComposerMutationCurrent,
	loadChatSessionDraft,
	markChatComposerDeliveryAccepted,
	prepareChatComposerDelivery,
	readChatSessionDraft,
	subscribeChatDraftRuntime,
	writeChatAttachments,
	writeChatExcerptReferences,
	writeChatComposerText,
	type ChatDraftMutationToken,
	type ChatComposerDelivery,
	type ChatDraftScope,
	type ChatDraftAttachment,
	type ChatDraftExcerptReference,
	type ChatDraftRetainedAttachment,
	type DraftClearResult,
} from "../../lib/chat-drafts";
import { attachmentURL, IMAGE_ATTACHMENT_PATH, isInlineImagePath, proseBesideImages } from "./messageAttachments";
import { setChatDraftBoundary } from "../../lib/chat-draft-boundary";
import { subscribeChatComposerReferences } from "../../lib/chat-context-bus";

// These responses precede AppendUserMessage. Provider/transport errors can
// follow durable acceptance and must keep the original delivery ID for recovery.
const DEFINITIVE_SEND_REJECTIONS = new Set([
	// Cloud validation failures reject the request before durable acceptance.
	"validation_error",
	"INVALID_BODY",
	"CHAT_MESSAGE_EMPTY",
	"INVALID_RESOURCE",
	"UNSUPPORTED_ATTACHMENT_TYPE",
	"INVALID_ATTACHMENT_DATA",
	"ATTACHMENT_TOO_LARGE",
	"TOO_MANY_ATTACHMENTS",
	"ATTACHMENTS_TOO_LARGE",
	"SESSION_NOT_FOUND",
	"SESSION_MODE_MISMATCH",
	"CHAT_CONTROLLER_NOT_READY",
	"CHAT_RESUME_FAILED",
	"CHAT_INTERFACE_TRANSITION",
	// Excerpts are verified before AppendUserMessage, so retrying the same
	// references can only fail again; return them to the draft for removal.
	"CHAT_EXCERPT_INVALID",
	"CHAT_EXCERPT_STALE",
]);

// Native image blocks are persisted with the chat turn and sent to the provider.
// Larger attachments still reach the agent through their staged workspace paths.
/** How long a sent draft stays hidden before it reappears if the daemon has not answered. */
const SEND_CONCEAL_MS = 3000;
const MAX_NATIVE_IMAGE_BYTES = 10 * 1024 * 1024;
const MAX_NATIVE_IMAGES_BYTES = 25 * 1024 * 1024;

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
		return "chat.draft.acceptedMessage";
	}
	return delivery.kind === "steer"
		? "chat.draft.steerRestart"
		: "chat.draft.sendRestart";
}
/** A retained server-owned attachment; image bytes stay in durable storage. */
export type StoredComposerAttachment = ChatDraftRetainedAttachment & { dataUrl?: string };
export type ChatComposerHandle = { focus(): void; isEmpty(): boolean };

export const ChatComposer = memo(function ChatComposer({
	focusRef,
	onSend,
	busy,
	willQueue,
	queuePlaceholder,
	disabled,
	disabledPlaceholder,
	starting,
	settings,
	approval,
	elicitation,
	skills = [],
	filePaths = [],
	filePathsTruncated,
	onStageAttachments,
	nativeImages,
	onSteer,
	showSteerButton,
	onInterrupt,
	canSteer,
	sendPending,
	steerPending,
	steerRefusal,
	draftSeed,
	editingQueuedTurnId,
	onCancelQueuedEdit,
	onQueuedDraftChange,
	queuedDraftScope,
	onQueuedAttachmentsChange,
	onQueuedRetainedAttachmentsChange,
	savingQueuedEditPending,
	queuedEditRecovery,
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
	assetBaseUrl,
	remoteHost = false,
	assetSessionId,
	acceptedClientMessageIds,
	emptyPlaceholder,
	onSelectAnnotation,
}: {
	onSelectAnnotation?: (annotation: { text: string; messageId?: string; revision?: number }) => void;
	focusRef?: Ref<ChatComposerHandle>;
	onSend: (
		text: string,
		attachments?: FileAttachmentPayload[],
		clientMessageId?: string,
		retainedContent?: number[],
		excerpts?: ChatDraftExcerptReference[],
	) => void | Promise<unknown>;
	settings?: ReactNode;
	/** A provider decision that temporarily replaces ordinary message entry. */
	approval?: ReactNode;
	/** A provider question, docked above the composer until it is answered. */
	elicitation?: ReactNode;
	/** A send is in flight. */
	busy?: boolean;
	/** The agent is mid-turn, so this message is held until the turn ends. */
	willQueue?: boolean;
	/** Replaces the default queueing hint, e.g. while the agent is still starting. */
	queuePlaceholder?: string;
	disabled?: boolean;
	/** Explains why message entry is temporarily blocked. */
	disabledPlaceholder?: string;
	starting?: boolean;
	/** A contextual prompt shown before an otherwise empty conversation begins. */
	emptyPlaceholder?: string;
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
	onSteer?: (text: string, attachments?: FileAttachmentPayload[], clientMessageId?: string, recoverOnly?: boolean) => Promise<ChatSteerOutcome | void>;
	/** Expose steering as a separate action while a Cloud turn is running. */
	showSteerButton?: boolean;
	/** Stop the turn already running when there is no draft to send. */
	onInterrupt?: () => void;
	/** A turn is actually running, so there is something to steer into. */
	canSteer?: boolean;
	/** A send mutation is in flight for this session. */
	sendPending?: boolean;
	steerPending?: boolean;
	/** Why the last steer was refused. */
	steerRefusal?: string;
	/** A selected history message to load into the composer as a new draft. */
	draftSeed?: { id: string; text: string; attachments?: StoredComposerAttachment[]; stagedAttachments?: ChatDraftAttachment[] };
	/** A queued turn being edited in the composer instead of the dock. */
	editingQueuedTurnId?: string;
	onCancelQueuedEdit?: () => void;
	onQueuedDraftChange?: (text: string) => void;
	queuedDraftScope?: ChatDraftScope;
	onQueuedAttachmentsChange?: (attachments: ChatDraftAttachment[]) => void;
	onQueuedRetainedAttachmentsChange?: (attachments: ChatDraftRetainedAttachment[]) => void;
	/** The queued edit mutation is in flight for the turn being edited. */
	savingQueuedEditPending?: boolean;
	/** Keep the exact queued edit immutable until its saved delivery ID is reconciled. */
	queuedEditRecovery?: boolean;
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
	/** Host-specific proxy origin for staged attachment reads. */
	assetBaseUrl?: string;
	/** The session belongs to a remote host, even if its proxy is disconnected. */
	remoteHost?: boolean;
	/** Daemon wire session ID when draft storage uses a host-scoped identity. */
	assetSessionId?: string;
	/** Client ids already present in daemon-authoritative conversation history. */
	acceptedClientMessageIds?: ReadonlySet<string>;
}) {
	const translateDraft = useChatDraftTranslation();
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
	const attachmentScopeKey = queuedDraftScope
		? chatQueuedAttachmentScopeKey(queuedDraftScope, draftSeed?.id ?? "undefined")
		: draftScopeKey;
	const boundarySessionId = draftSessionId ?? queuedDraftScope?.sessionId;
	const [retainedAttachments, setRetainedAttachments] = useState<StoredComposerAttachment[]>(draftSeed?.attachments ?? []);
	const visibleRetainedAttachments = editingQueuedTurnId ? retainedAttachments : [];
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
	const chatSendKeyMode = useUiStore((state) => state.chatSendKeyMode);
	const enterSends = chatSendKeyMode === "enter";
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
	const [steerNextRequest, setSteerNextRequest] = useState(0);
	// Cmd/Ctrl held anywhere in the window turns a send-button click into a steer.
	const [modifierHeld, setModifierHeld] = useState(false);
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
	const submitInFlight = useRef<Promise<void> | null>(null);
	// Disabling the active editor can move focus to the document body. Remember
	// keyboard-origin submissions so focus can return once the editor is enabled.
	const restoreFocusAfterSubmission = useRef(false);
	const menuId = useId();
	const hadQueuedDockRef = useRef(Boolean(queuedDock));
	const previousTrigger = useRef<ComposerTrigger | undefined>(undefined);
	const triggerRef = useRef<ComposerTrigger | undefined>(undefined);
	const automaticDeliveryRecoveryAttempted = useRef<string | undefined>(undefined);
	// Code selected in a file or diff view ("Ask in chat") arrives here as a
	// reference chip. Only the session's ordinary prompt accepts it; a queued-turn
	// edit has no draftSessionId and stays out of the way.
	useEffect(() => {
		if (!draftSessionId) return;
		return subscribeChatComposerReferences(draftSessionId, (reference) => {
			editor.current?.insertReference(reference.path, reference.display, reference.wire);
			// The Chat surface may only now be coming forward from behind a file tab.
			window.requestAnimationFrame(() => window.requestAnimationFrame(() => editor.current?.focus()));
		});
	}, [draftSessionId]);
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
	const optimisticSend = Boolean(durableDelivery?.draft && composerMutation.pending);
	const contextReferences = draftScope
		? readChatSessionDraft(draftScope).composer.excerpts ?? []
		: [];
	const [appliedAcceptanceSequence, setAppliedAcceptanceSequence] = useState(0);
	const composerRevision = useRef(persistedDraft?.composer.revision ?? 0);
	const unsavedText = useRef<{ text: string; revision: number } | undefined>(undefined);
	const synchronouslyClearedDeliveryRevision = useRef<number | undefined>(undefined);
	const restoredAttachments = useMemo<FileAttachment[]>(
		() =>
			(persistedDraft?.composer.delivery?.draft?.attachments ?? persistedDraft?.composer.attachments ?? draftSeed?.stagedAttachments)?.map((attachment) => ({
				id: attachment.id,
				name: attachment.name,
				mimeType: attachment.mimeType,
				bytes: attachment.bytes,
				stagedPath: attachment.path,
			})) ?? [],
		[persistedDraft, draftSeed?.stagedAttachments],
	);
	const persistAttachments = useCallback(
		(attachments: FileAttachment[]) => {
			const descriptors = attachments.flatMap((attachment) => attachment.stagedPath
				? [{ id: attachment.id, path: attachment.stagedPath, name: attachment.name, mimeType: attachment.mimeType, bytes: attachment.bytes }]
				: []);
			if (!draftScope) {
				onQueuedAttachmentsChange?.(descriptors);
				return;
			}
			const sentAttachments = readChatSessionDraft(draftScope).composer.delivery?.draft?.attachments;
			const result = writeChatAttachments(draftScope, descriptors.filter((attachment) =>
				!sentAttachments?.some((sent) => sent.id === attachment.id)));
			if (result.ok) composerRevision.current = result.draft.composer.revision;
			setAttachmentDraftPersistenceError(
				result.ok
					? null
					: "chat.draft.saveFailed",
			);
		},
		[draftScope, onQueuedAttachmentsChange],
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
		unsavedText.current = undefined;
		setDurableDelivery(currentDraft?.composer.delivery);
		setAppliedAcceptanceSequence(0);
		setSendError(null);
		setSteerOutcomeNotice(null);
		setTextDraftPersistenceError(null);
		setDeliveryRecoveryNotice(restoredDeliveryNotice(currentDraft?.composer.delivery));
		setDeliveryUncertain(currentDraft?.composer.delivery?.state === "dispatching");
		setAttachmentDraftPersistenceError(null);
		automaticDeliveryRecoveryAttempted.current = undefined;
	}, [draftScope, draftScopeKey]);
	const fileAttachments = useFileAttachments({
		initialAttachments: restoredAttachments,
		initialKey: attachmentScopeKey,
		prepareAttachments: onStageAttachments ? prepareAttachments : undefined,
		onAttachmentsChange: persistAttachments,
	});
	const canAttach = Boolean(onStageAttachments) && !queuedEditRecovery;

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

	const staged = fileAttachments.attachments.length > 0 || visibleRetainedAttachments.length > 0;
	const nextDraftHasChanged = Boolean(durableDelivery?.draft && composerRevision.current > durableDelivery.revision + 1);
	const stripAttachments = (optimisticSend || nextDraftHasChanged ? [] : [...visibleRetainedAttachments, ...fileAttachments.attachments]).map((file) => {
		const path = "stagedPath" in file ? file.stagedPath : "path" in file ? file.path : undefined;
		const assetOrigin = remoteHost ? assetBaseUrl : assetBaseUrl ?? getApiBaseUrl();
		const preview = file.dataUrl ?? (path && assetOrigin !== undefined && IMAGE_ATTACHMENT_PATH.test(path)
			? attachmentURL(assetOrigin, assetSessionId ?? boundarySessionId ?? "", path) : undefined);
		return { file, path, preview };
	});
	const composerImages: ComposerImage[] = stripAttachments.flatMap(({ file, path, preview }) =>
		path && IMAGE_ATTACHMENT_PATH.test(path) ? [{ path, name: file.name, src: preview }] : []);
	const controlsDisabled = Boolean(disabled || submitting);
	const hasDraft = hasText || staged || contextReferences.length > 0;
	const savingQueuedEdit = Boolean(editingQueuedTurnId);
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
		!savingQueuedEditPending &&
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
	// A plain send already shows its echo in the timeline. Its text stays held in the
	// editor until the daemon accepts, so a rejection or lost response can restore it,
	// but it is not drawn meanwhile: the composer reads as sent, not stranded.
	const sendInFlight = Boolean(
		submitting &&
			!staged &&
			!deliveryUncertain &&
			durableDelivery?.kind === "send" &&
			durableDelivery.state === "dispatching",
	);
	// The request has no timeout. If the daemon stalls, bring the text back so a stuck
	// send never reads as a vanished draft.
	const [concealExpired, setConcealExpired] = useState(false);
	useEffect(() => {
		if (!sendInFlight) {
			setConcealExpired(false);
			return;
		}
		const timer = window.setTimeout(() => setConcealExpired(true), SEND_CONCEAL_MS);
		return () => window.clearTimeout(timer);
	}, [sendInFlight]);
	const sendConcealed = sendInFlight && !concealExpired;
	const canSend =
		(hasText || staged) &&
		(savingQueuedEdit || !busy) &&
		!disabled &&
		!steerPending &&
		!savingQueuedEditPending &&
		!draftMutationPending &&
			!acceptedClearFailed &&
			!durableDelivery &&
			!fileAttachments.preparing;
	const sendActionEnabled = canSend || canRecoverDelivery;
	// A plain send while a turn runs is queued, so the button says so instead of
	// looking identical to an immediate send.
	const queuesDraft = Boolean(willQueue && !savingQueuedEdit && !queuedEditRecovery);
	// Cmd/Ctrl+Enter or Cmd/Ctrl+click steers the current draft into the running
	// turn; the send button only shows it while the modifier is held. When Enter
	// inserts newlines, Cmd/Ctrl+Enter is the send key, so the modifier delivers
	// through the ordinary send/queue path instead of steering.
	// Steering has no excerpt payload, so attached excerpts would be cleared with
	// the accepted draft without reaching the agent. Queue those drafts instead.
	const canSteerDraft =
		Boolean(canSteer && onSteer) && !savingQueuedEdit && contextReferences.length === 0;
	const steersDraft = enterSends && modifierHeld && canSteerDraft;
	const showsSteer = steersDraft && !durableDelivery && !queuedEditRecovery;
	const sendActionLabel = translateDraft(durableDelivery && !submitting
		? durableDelivery.state === "accepted"
			? "chat.draft.clearMessage"
			: "chat.draft.retryMessage"
		: queuedEditRecovery
			? "chat.draft.retryEdit"
			: showsSteer ? "Steer message" : queuesDraft ? "Queue message" : "Send message");
	const canStopTurn = Boolean(
		willQueue && onInterrupt && !controlsDisabled && !hasDraft && !savingQueuedEdit,
	);
	const canSteerNext =
		Boolean(canSteer && onSteer) &&
		!controlsDisabled &&
		!hasDraft &&
		!savingQueuedEdit &&
		Boolean(queuedDock);
	const sendKeyHint = enterSends ? "Enter" : "Cmd/Ctrl+Enter";
	const newlineHint = enterSends ? "Shift+Enter for newline" : "Enter for newline";
	const sendHint = showsSteer && !menuOpen
		? "Steer into running turn"
		: `${sendKeyHint} to ${menuOpen ? "insert" : savingQueuedEdit ? "save edit" : willQueue ? "queue" : "send"}; ${newlineHint}`;
	const persistedText = persistedDraft?.composer.text;
	const draftSeedId = draftSeed?.id ?? (draftScopeKey ? `session:${draftScopeKey}` : undefined);
	const draftSeedText = draftSeed?.text ?? persistedText;
	const draftPersistenceError =
		textDraftPersistenceError ?? attachmentDraftPersistenceError;

	useEffect(() => {
		if (!boundarySessionId) return;
		const deliveryWasSynchronouslyCleared = Boolean(
			durableDelivery &&
				synchronouslyClearedDeliveryRevision.current === durableDelivery.revision,
		);
		setChatDraftBoundary(
			boundarySessionId,
			"composer",
			[
				...(draftPersistenceError && !deliveryWasSynchronouslyCleared
					? (["persistence-failed"] as const)
					: []),

				...(fileAttachments.preparing ? (["pending-attachments"] as const) : []),
			],
		);
	}, [draftPersistenceError, boundarySessionId, durableDelivery, fileAttachments.preparing]);

	useEffect(
		() => () => {
			if (boundarySessionId) setChatDraftBoundary(boundarySessionId, "composer", undefined);
		},
		[boundarySessionId],
	);
	const queuedDockWithSteer = isValidElement<{
		canSteerNext?: boolean;
		steerNextRequest?: number;
		disabled?: boolean;
	}>(queuedDock)
		? cloneElement(
				queuedDock,
				{ canSteerNext, steerNextRequest, disabled: submitting || queuedDock.props.disabled },
			)
		: queuedDock;

	const focusEditor = useCallback(() => {
		if (!autoFocus || disabled) return;
		editor.current?.focus();
	}, [autoFocus, disabled]);
	useImperativeHandle(focusRef, () => ({ focus: focusEditor, isEmpty: () => !hasDraft }), [focusEditor, hasDraft]);

	useEffect(() => {
		focusEditor();
	}, [autoFocusKey, focusEditor]);

	const hasQueuedDock = Boolean(queuedDock);
	const restoreFocusAfterQueueAppears =
		hasQueuedDock &&
		!hadQueuedDockRef.current &&
		typeof document !== "undefined" &&
		document.activeElement?.getAttribute("aria-label") === "Message the agent";
	useLayoutEffect(() => {
		hadQueuedDockRef.current = hasQueuedDock;
		if (restoreFocusAfterQueueAppears) editor.current?.focus();
	}, [hasQueuedDock, restoreFocusAfterQueueAppears]);

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
					"chat.draft.acceptedMessage",
				);
				setTextDraftPersistenceError(
					"chat.draft.clearMessageFailed",
				);
				return false;
			}
			if (!unsavedText.current) composerRevision.current = result.draft.composer.revision;
			setTextDraftPersistenceError(unsavedText.current ? "chat.draft.saveFailed" : null);
			setDeliveryRecoveryNotice(null);
			// A receipt from the old surface must not clear this surface's unsaved draft.
			if (!unsavedText.current && !result.cleared) {
				textRef.current = result.draft.composer.text;
				hasTextRef.current = textRef.current.trim().length > 0;
				setHasText(hasTextRef.current);
				if (editor.current?.getSnapshot().text !== textRef.current) editor.current?.setText(textRef.current);
			} else if (!unsavedText.current && editor.current?.getSnapshot().text !== "") {
				clearEditorView();
			}
			if (result.draft.composer.attachments.length === 0) {
				fileAttachments.clear();
			} else {
				for (const attachment of fileAttachments.getAttachments()) {
					if (!result.draft.composer.attachments.some((kept) => kept.id === attachment.id)) {
						fileAttachments.remove(attachment.id);
					}
				}
			}
			return result.cleared && !unsavedText.current;
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
			const saved = unsavedText.current
				? writeChatComposerText(draftScope, unsavedText.current.text, undefined, unsavedText.current.revision)
				: undefined;
			if (saved?.ok && saved.draft.composer.text === unsavedText.current?.text) unsavedText.current = undefined;
			const result: DraftClearResult = saved?.ok === false
				? { ok: false, cleared: false, draft: saved.draft }
				: clearAcceptedChatComposer(draftScope, acceptedRevision, undefined,
					fileAttachments.getAttachments().map((attachment) => attachment.id));
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
					"chat.draft.recordMessageFailed",
				);
				return false;
			}
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
				"chat.draft.abandonFailed",
			);
			return;
		}
		setDeliveryUncertain(false);
		setTextDraftPersistenceError(null);
		setDeliveryRecoveryNotice(null);
		setSteerOutcomeNotice(
			"chat.draft.abandonedSteer",
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
		if (unsavedText.current || (composerMutation.pending && restoredSeedKey.current !== undefined)) return;
		const loadedDraft = draftScope ? loadChatSessionDraft(draftScope) : undefined;
		if (loadedDraft?.ok === false) return;
		const committedDraft = loadedDraft?.draft;
		// Keep this surface's native bytes for retry; only a new surface needs descriptors.
		if (committedDraft && (!committedDraft.composer.delivery?.draft || restoredSeedKey.current === undefined)) {
			fileAttachments.reconcilePersistedAttachments(
				(committedDraft.composer.delivery?.draft?.attachments ?? committedDraft.composer.attachments).map((attachment) => ({
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
			(committedDraft
				? committedDraft.composer.text || (!getChatComposerMutation(draftScope ?? "").pending
					? committedDraft.composer.delivery?.draft?.text ?? ""
					: "")
				: draftSeedText);
		if (committedSeedText === undefined) {
			restoredSeedKey.current = undefined;
			return;
		}
		const seedKey = editingQueuedTurnId ? draftSeedId : JSON.stringify([draftSeedId, committedSeedText]);
		if (restoredSeedKey.current === seedKey) return;
		restoredSeedKey.current = seedKey;
		textRef.current = committedSeedText;
		hasTextRef.current = committedSeedText.trim().length > 0;
		setHasText(hasTextRef.current);
		if (editor.current?.getSnapshot().text !== committedSeedText) {
			editor.current?.setText(committedSeedText);
			dismissedKeyRef.current = null;
			setDismissedKey(null);
			highlightedRef.current = 0;
			setHighlighted(0);
		}
		if (draftSeed) {
			setSendError(null);
			setSteerOutcomeNotice(null);
		}
		// A history action intentionally creates a new draft and must be persisted.
		// A session restore is already durable; writing it again here needlessly
		// changes the accepted-send revision during mount.
		if (draftScope && draftSeed) {
			const result = writeChatComposerText(draftScope, committedSeedText);
			if (result.ok) composerRevision.current = result.draft.composer.revision;
			setTextDraftPersistenceError(
				result.ok
					? null
					: "chat.draft.saveFailed",
			);
		}
	}, [
		composerMutation.pending,
		draftScope,
		draftSeed,
		draftSeedId,
		draftSeedText,
		editingQueuedTurnId,
		fileAttachments.reconcilePersistedAttachments,
	]);

	useEffect(() => {
		if (!durableDelivery || !draftScope) return;
		// The live send owns completion until its receipt has been applied.
		if (composerMutation.pending || composerMutation.accepted) return;
		// A replacement can commit after another surface cleared its rendered seed.
		const current = loadChatSessionDraft(draftScope);
		// A failed read cannot prove that the delivery was cleared.
		const delivery = current.ok ? current.draft.composer.delivery : durableDelivery;
		if (!delivery || delivery.clientMessageId !== durableDelivery.clientMessageId) {
			setDurableDelivery(delivery);
			return;
		}
		const observedSteer =
			delivery.kind === "steer" &&
			acceptedClientMessageIds?.has(delivery.clientMessageId);
		if (delivery.state !== "accepted" && !observedSteer) return;
		if (automaticDeliveryRecoveryAttempted.current === delivery.clientMessageId) return;
		automaticDeliveryRecoveryAttempted.current = delivery.clientMessageId;
		acceptAndClearDurableDelivery(delivery);
	}, [
		acceptAndClearDurableDelivery,
		acceptedClientMessageIds,
		composerMutation.pending,
		composerMutation.accepted,
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

	const previousEditingQueuedTurnIdRef = useRef(editingQueuedTurnId);
	useEffect(() => {
		const previous = previousEditingQueuedTurnIdRef.current;
		previousEditingQueuedTurnIdRef.current = editingQueuedTurnId;
		if (previous && !editingQueuedTurnId) {
			clearEditorView();
		}
	}, [clearEditorView, editingQueuedTurnId]);

	const onEditorChange = useCallback((snapshot: ComposerEditorSnapshot) => {
		// Changing Lexical's editability can publish the optimistic visual clear.
		if (optimisticSend && !snapshot.text && !textRef.current) return;
		const previousText = textRef.current;
		textRef.current = snapshot.text;
		onQueuedDraftChange?.(snapshot.text);
		if (draftScope && snapshot.text !== previousText) {
			const result = writeChatComposerText(draftScope, snapshot.text);
			if (result.ok) composerRevision.current = result.draft.composer.revision;
			unsavedText.current = result.ok ? undefined : { text: snapshot.text, revision: composerRevision.current };
			// A disabled Lexical editor can still publish an internal state update
			// while its editability changes. It must not erase the recovery notice
			// for a durable delivery that still owns this exact composer revision.
			if (!result.ok || !result.draft.composer.delivery) {
				setTextDraftPersistenceError(
					result.ok
						? null
						: "chat.draft.saveFailed",
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
	}, [draftScope, onQueuedDraftChange, optimisticSend]);

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

	useLayoutEffect(() => {
		if (submitting || !restoreFocusAfterSubmission.current) return;
		restoreFocusAfterSubmission.current = false;
		if (typeof document === "undefined") return;
		const active = document.activeElement;
		// Restore focus only when disabling the editor caused the blur. Do not steal
		// focus if the user deliberately moved to another control while awaiting.
		if (active === document.body || active === null) editor.current?.focus();
	}, [submitting]);

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

	function submit(event?: FormEvent, forceSteer?: boolean): Promise<void> {
		event?.preventDefault();
		// React cannot publish the next busy prop until after this event returns. A
		// second Enter in that gap joins the accepted submission instead of opening a
		// second transport whose local admission rejection would look like a real
		// provider failure.
		if (submitInFlight.current) return submitInFlight.current;
		restoreFocusAfterSubmission.current =
			typeof document !== "undefined" &&
			document.activeElement?.getAttribute("aria-label") === "Message the agent";
		setSubmitting(true);
		const pending = performSubmit(forceSteer);
		submitInFlight.current = pending;
		const release = () => {
			if (submitInFlight.current !== pending) return;
			submitInFlight.current = null;
			setSubmitting(false);
		};
		void pending.then(release, release);
		return pending;
	}

	async function performSubmit(forceSteer?: boolean) {
		// Own the scoped draft before file reading or staging can yield. A replacement
		// surface observes this reservation and cannot edit underneath this send.
		const mutationToken = draftScope && !savingQueuedEdit
			? beginChatComposerMutation(draftScope)
			: undefined;
		if (draftScope && !savingQueuedEdit && !mutationToken) return;
		try {
			await performClaimedSubmit(forceSteer, mutationToken);
		} finally {
			if (draftScope && mutationToken) cancelChatComposerMutation(draftScope, mutationToken);
		}
	}

	async function performClaimedSubmit(forceSteer?: boolean, mutationToken?: ChatDraftMutationToken) {
		let currentText = textRef.current;
		let body = currentText.trim();
		const recoveringDelivery = durableDelivery;
		const sendNativeImages = recoveringDelivery?.nativeImages ?? Boolean(nativeImages);
		setSendError(null);
		setSteerOutcomeNotice(null);

		if (!recoveringDelivery && !savingQueuedEdit && body === "/compact" && onCompact) {
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

		await fileAttachments.toSettledPayload();
		// A replacement hook can still have staging work owned by the old surface.
		if (fileAttachments.hasPendingReads()) return;
		const settledAttachments = fileAttachments.getAttachments();
		const settledPaths = settledAttachments.flatMap((attachment) =>
			attachment.stagedPath ? [attachment.stagedPath] : []);
		const attachedPaths = [
			...visibleRetainedAttachments.flatMap((attachment) => attachment.path ? [attachment.path] : []),
			...settledPaths,
		];
		// Staging that just settled has filled its inline chips, so read the text the
		// user sees now. First drop chips whose image is gone (an undo can restore
		// one); typed paths are the user's text and stay.
		editor.current?.pruneImages(attachedPaths);
		currentText = textRef.current;
		body = proseBesideImages(currentText.trim(), attachedPaths);
		const hasAttachments = settledAttachments.length > 0 || visibleRetainedAttachments.length > 0;
		const canSubmitNow =
			(body.length > 0 || hasAttachments || contextReferences.length > 0 || Boolean(recoveringDelivery)) &&
			(!busy || savingQueuedEdit || recoveringDelivery?.state === "accepted") &&
			!disabled && !steerPending && !savingQueuedEditPending &&
			!composerMutation.pending &&
			(!draftMutationPending || Boolean(recoveringDelivery)) &&
			(Boolean(recoveringDelivery) ||
				getChatComposerMutation(draftScope ?? "").accepted?.result.ok !== false);
		if (!canSubmitNow) {
			if (busy && !disabled && !recoveringDelivery && !savingQueuedEdit) {
				setSendError("Still sending the previous message. Try again in a moment.");
			}
			return;
		}
		if (hasAttachments && settledPaths.length !== settledAttachments.length) {
			setSendError("chat.draft.filesUnavailable");
			return;
		}
		const shouldSteer = Boolean(forceSteer && !savingQueuedEdit && contextReferences.length === 0);
		const message = withAttachmentReferences(body, attachedPaths) || (contextReferences.length > 0 ? `Use the attached ${contextReferences.length} chat excerpt(s) as context` : "");
		// Ordinary delivery reserves its exact draft before these staged reads await.
		// Queue editors use their existing owner/revision CAS before mutation.
		const attachmentScope = queuedDraftScope ?? draftScope;
		const retainedNativeImageCount = visibleRetainedAttachments.filter((item) => item.contentType === "image").length;
		let nativeImageBytes = 0;
		const deliveryAttachments = recoveringDelivery?.draft?.attachments.map<FileAttachment>((attachment) =>
			settledAttachments.find((staged) => staged.id === attachment.id) ?? {
				...attachment, stagedPath: attachment.path,
			});
		const nativeImageAttachments = (deliveryAttachments ?? settledAttachments).filter((attachment) => {
			// Retained image sizes are server-owned and unknown here. Keep new images
			// as workspace files so an edit cannot exceed the native 25 MiB budget.
			if (!sendNativeImages || retainedNativeImageCount > 0 || !isSupportedImageAttachment(attachment.mimeType) ||
				attachment.bytes > MAX_NATIVE_IMAGE_BYTES ||
				nativeImageBytes + attachment.bytes > MAX_NATIVE_IMAGES_BYTES) return false;
			nativeImageBytes += attachment.bytes;
			return true;
		});
		let nativePayloads = nativeImageAttachments.flatMap(({ mimeType, data }) =>
			data ? [{ mimeType, data }] : []);
		const restoreNativePayloads = async (): Promise<boolean> => {
			if (!attachmentScope || !sendNativeImages || recoveringDelivery?.kind === "steer" || recoveringDelivery?.state === "accepted") return true;
			const restored: FileAttachmentPayload[] = [];
			try {
				for (const attachment of nativeImageAttachments) {
					if (attachment.data) {
						restored.push({ mimeType: attachment.mimeType, data: attachment.data });
						continue;
					}
					if (!attachment.stagedPath) throw new Error("Missing staged attachment");
					const assetOrigin = remoteHost ? assetBaseUrl : assetBaseUrl ?? getApiBaseUrl();
					if (assetOrigin === undefined) throw new Error("Remote host disconnected");
					const response = await fetch(attachmentURL(assetOrigin, assetSessionId ?? attachmentScope.sessionId, attachment.stagedPath));
					if (!response.ok) throw new Error("Could not read staged attachment");
					const blob = await response.blob();
					const data = await new Promise<string>((resolve, reject) => {
						const reader = new FileReader();
						reader.onload = () => resolve(String(reader.result).split(",")[1] ?? "");
						reader.onerror = () => reject(new Error("Could not read staged attachment"));
						reader.readAsDataURL(blob);
					});
					restored.push({ mimeType: attachment.mimeType, data });
				}
				nativePayloads = restored;
				return true;
			} catch {
				setSendError("chat.draft.readAttachmentFailed");
				return false;
			}
		};
		if ((!draftScope || savingQueuedEdit) && !await restoreNativePayloads()) return;

		if (savingQueuedEdit && nativePayloads.length + retainedNativeImageCount > MAX_ATTACHMENTS) {
			setSendError(`You can attach up to ${MAX_ATTACHMENTS} images.`);
			return;
		}
		if (!draftScope || savingQueuedEdit) {
			setSubmitting(true);
			// A plain-text send has a local timeline echo — clear the editor immediately
			// so the user sees one acknowledgement rather than their draft stranded until
			// the daemon round-trip completes. Attachments retain the retry path.
			const clearForLocalEcho = !shouldSteer && !savingQueuedEdit && nativePayloads.length === 0;
			try {
				if (clearForLocalEcho) clearEditorView();
				if (shouldSteer && onSteer) {
					const outcome = nativePayloads.length > 0
						? await onSteer(message, nativePayloads)
						: await onSteer(message);
					if (outcome?.status === "not-accepted") {
						setSteerOutcomeNotice(outcome.reason);
						return;
					}
				} else if (savingQueuedEdit) {
					await onSend(message, nativePayloads.length ? nativePayloads : undefined, undefined,
						visibleRetainedAttachments.flatMap((item) => item.contentIndex === undefined ? [] : [item.contentIndex]));
					// The parent leaves this editor only after proving durable cleanup.
					return;
				} else if (nativePayloads.length > 0) {
					await onSend(message, nativePayloads);
				} else {
					await onSend(message);
				}
				if (!clearForLocalEcho) clearEditorView();
				fileAttachments.clear();
			} catch (error) {
				if (clearForLocalEcho && !textRef.current) {
					textRef.current = currentText;
					hasTextRef.current = currentText.trim().length > 0;
					setHasText(hasTextRef.current);
					editor.current?.setText(currentText);
				}
				setSendError(
					savingQueuedEdit
						? apiErrorMessage(error, "chat.draft.queueSaveFailed")
						: staged
						? "chat.draft.sendAttachmentsFailed"
						: "chat.draft.sendFailed",
				);
			} finally {
				setSubmitting(false);
			}
			return;
		}

		const requestText = recoveringDelivery
			? recoveringDelivery.requestText
			: message;
		const prepared = prepareChatComposerDelivery(draftScope, {
			kind: recoveringDelivery?.kind ?? (shouldSteer ? "steer" : "send"),
			optimistic: !shouldSteer,
			nativeImages: sendNativeImages,
			composerText: currentText,
			attachments: settledAttachments.flatMap((attachment) =>
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
			excerpts: recoveringDelivery?.excerpts ?? contextReferences,
			requestText,
			clientMessageId: recoveringDelivery?.clientMessageId ?? crypto.randomUUID(),
		});
		if (!prepared.ok) {
			setTextDraftPersistenceError(
				"chat.draft.prepareFailed",
			);
			return;
		}
		const delivery = prepared.mutation;
		if (!prepared.recovered && delivery.draft) clearEditorView();
		synchronouslyClearedDeliveryRevision.current = undefined;
		setDeliveryUncertain(false);
		if (!prepared.recovered) {
			composerRevision.current = prepared.draft.composer.revision;
			unsavedText.current = undefined;
		}
		setDurableDelivery(delivery);
		setTextDraftPersistenceError(null);
		setDeliveryRecoveryNotice(
			delivery.state === "accepted"
				? "chat.draft.acceptedMessage"
				: null,
		);
		if (delivery.state === "accepted") {
			acceptAndClearDurableDelivery(delivery);
			return;
		}

		if (!mutationToken) return;
		let mutationFinished = false;
		setSubmitting(true);
		try {
			if (!await restoreNativePayloads()) return;
			if (!isChatComposerMutationCurrent(draftScope, mutationToken)) return;
			if (delivery.kind === "steer") {
				if (!onSteer) throw new Error("Steering is unavailable");
				const outcome = await onSteer(delivery.requestText, prepared.recovered || nativePayloads.length === 0 ? undefined : nativePayloads, delivery.clientMessageId, prepared.recovered);
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
							"chat.draft.clearRefusedSteerFailed",
						);
					}
					return;
				}
			} else {
				const deliveryAttachments =
					sendNativeImages && nativePayloads.length > 0 ? nativePayloads : undefined;
				if (delivery.excerpts?.length) {
					await onSend(
						delivery.requestText,
						deliveryAttachments,
						delivery.clientMessageId,
						undefined,
						delivery.excerpts,
					);
				} else {
					await onSend(delivery.requestText, deliveryAttachments, delivery.clientMessageId);
				}
			}
			acceptAndClearDurableDelivery(delivery, mutationToken);
			mutationFinished = true;
		} catch (error) {
			if (delivery.kind === "send" && !textRef.current) {
				textRef.current = delivery.draft?.text ?? currentText;
				hasTextRef.current = textRef.current.trim().length > 0;
				setHasText(hasTextRef.current);
				editor.current?.setText(textRef.current);
			}
			// Refusal of a retry says nothing about a previous attempt whose response
			// was lost. Only an initial, definitively unaccepted send can be edited.
			if (
				delivery.kind === "send" && !prepared.recovered &&
				DEFINITIVE_SEND_REJECTIONS.has(apiErrorCode(error) ?? "")
			) {
				const cleared = unsavedText.current
					? { ok: false, draft: readChatSessionDraft(draftScope) }
					: clearRejectedChatComposerDelivery(draftScope, delivery.clientMessageId, delivery.revision);
				setDurableDelivery(cleared.draft.composer.delivery);
				if (cleared.ok && !cleared.draft.composer.delivery) {
					composerRevision.current = cleared.draft.composer.revision;
					textRef.current = cleared.draft.composer.text;
					hasTextRef.current = textRef.current.trim().length > 0;
					setHasText(hasTextRef.current);
					editor.current?.setText(textRef.current);
				}
				setDeliveryUncertain(false);
				setDeliveryRecoveryNotice(null);
				setTextDraftPersistenceError(cleared.ok ? null : "chat.draft.saveFailed");
				setSendError(apiErrorMessage(error));
				return;
			}
			setDeliveryUncertain(true);
			setDeliveryRecoveryNotice(
				delivery.kind === "steer"
					? "chat.draft.steerUncertain"
					: "chat.draft.sendUncertain",
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
		return handleEnterKey(event);
	}

	const handleEnterKey = useCallback(
		(event: globalThis.KeyboardEvent): boolean => {
			const liveSnapshot = editor.current?.getSnapshot();
			if (liveSnapshot) textRef.current = liveSnapshot.text;
			const liveTrigger = liveSnapshot?.trigger;
			const liveSuggestions = suggestionsFor(liveTrigger);
			if (liveSuggestions.length > 0) {
				if (textRef.current.trim() === "/compact" && onCompact) {
					void submit();
					return true;
				}
				triggerRef.current = liveTrigger;
				const chosen = liveSuggestions[Math.min(highlightedRef.current, liveSuggestions.length - 1)];
				if (chosen) pick(chosen.value);
				return true;
			}

			if (enterSends && canSteerNext && !textRef.current.trim() && !fileAttachments.hasPendingReads()) {
				setSteerNextRequest((request) => request + 1);
				return true;
			}
			const wantsSteer = enterSends && (event.metaKey || event.ctrlKey) && canSteerDraft;
			void submit(undefined, wantsSteer);
			return true;
		},
		[canSteerDraft, canSteerNext, enterSends, fileAttachments, onCompact, pick, suggestionsFor],
	);

	function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
		if (event.nativeEvent.isComposing || event.keyCode === 229) return;
		// Enter is handled in Lexical before a newline is inserted; this handler is
		// only for menu navigation and escape while a completion menu is open.
		const liveSnapshot = editor.current?.getSnapshot();
		if (liveSnapshot) textRef.current = liveSnapshot.text;
		const liveTrigger = liveSnapshot?.trigger;
		const liveSuggestions = suggestionsFor(liveTrigger);
		if (liveSuggestions.length > 0) {
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
			if (event.key === "Escape") {
				event.preventDefault();
				event.stopPropagation();
				dismissedKeyRef.current = liveTrigger?.key ?? null;
				setDismissedKey(dismissedKeyRef.current);
				return;
			}
		}

		if (event.key === "Escape" && editingQueuedTurnId && onCancelQueuedEdit && !queuedEditRecovery && !submitInFlight.current) {
			event.preventDefault();
			onCancelQueuedEdit();
		}
	}

	// Images get an inline chip only beside prose that can refer to them.
	// Image-only drafts already show every image in the attachment strip.
	function attachFiles(files: File[]) {
		// Reserve the spot now: staging can take a while, and the user keeps typing.
		const reservation = proseBesideImages(textRef.current.trim(), composerImages.map((image) => image.path)) &&
			files.some((file) => file.type.startsWith("image/"))
			? editor.current?.reserveImages()
			: undefined;
		void fileAttachments.addFiles(files).then((added) => {
			if (!reservation) return;
			editor.current?.fillImages(reservation, added.flatMap((attachment) =>
				attachment.stagedPath && isInlineImagePath(attachment.stagedPath) ? [attachment.stagedPath] : []));
		});
	}

	function onPaste(event: ClipboardEvent<HTMLDivElement>) {
		if (!canAttach || fileAttachments.preparing || draftMutationPending || submitInFlight.current) return;
		const clipboard = event.clipboardData;
		const files = Array.from(clipboard?.files ?? []);
		if (files.length === 0) return;
		// The paste is only claimed when there is no text alongside the image: a copy
		// carrying both should still paste its text.
		const hasText = typeof clipboard?.getData === "function" && clipboard.getData("text/plain") !== "";
		if (!hasText) event.preventDefault();
		attachFiles(files);
	}

	function onDrop(event: DragEvent<HTMLFormElement>) {
		setDragging(false);
		if (!canAttach || fileAttachments.preparing || draftMutationPending || submitInFlight.current) return;
		const files = Array.from(event.dataTransfer?.files ?? []);
		if (files.length === 0) return;
		event.preventDefault();
		event.stopPropagation();
		attachFiles(files);
	}

	// Only modifier transitions re-render: React bails out when the value is unchanged.
	useEffect(() => {
		const onKey = (event: globalThis.KeyboardEvent) => {
			setModifierHeld(event.metaKey || event.ctrlKey);
		};
		const onBlur = () => {
			setModifierHeld(false);
		};
		window.addEventListener("keydown", onKey);
		window.addEventListener("keyup", onKey);
		window.addEventListener("blur", onBlur);
		return () => {
			window.removeEventListener("keydown", onKey);
			window.removeEventListener("keyup", onKey);
			window.removeEventListener("blur", onBlur);
		};
	}, []);

	const attachmentError =
		fileAttachments.error ??
		draftPersistenceError ??
		(optimisticSend ? null : deliveryRecoveryNotice) ??
		sendError ??
		(fileAttachments.attachments.some((file) => !file.data && !file.stagedPath)
			? "chat.draft.filesUnavailable" : null) ??
		commandError;
	const withQueueStack = (form: ReactElement) =>
		(
			<div className="relative mx-auto flex w-full max-w-3xl flex-col">
				{queuedDock ? (
				<div
					className="cursor-chat-composer-queue queue-dock-enter relative z-10 mx-auto mb-2 w-[calc(100%-2rem)]"
					data-testid="queued-composer-dock"
				>
					{queuedDockWithSteer}
				</div>
				) : null}
				{elicitation ? (
					<div
						className="queue-dock-enter relative z-10 mx-auto mb-2 w-[calc(100%-2rem)]"
						data-testid="elicitation-composer-dock"
					>
						{elicitation}
					</div>
				) : null}
				{form}
			</div>
		);

	if (approval) {
		return withQueueStack(
			<form
				onSubmit={(event) => event.preventDefault()}
				data-attached-top={attachedTop && !queuedDock && !elicitation ? true : undefined}
				className="cursor-chat-composer relative flex flex-col gap-1.5 px-3 py-3"
			>
				{approval}
				{commandError ? (
					<p role="alert" className="px-1.5 text-xs leading-snug text-destructive">
						{commandError}
					</p>
				) : null}
			</form>,
		);
	}

	return withQueueStack(
		<form
			// Cmd/Ctrl steering remains available as a quiet power-user action.
			onSubmit={(event) => void submit(event, steersDraft)}
				onDragOver={(event) => {
					if (!canAttach || submitInFlight.current) return;
					event.preventDefault();
					setDragging(true);
				}}
				onDragLeave={() => setDragging(false)}
				onDropCapture={onDrop}
				// The border colors for rest, hover, focus and drag are one set of states
				// on one surface, so they are declared together in CSS rather than half
				// here and half there.
				data-dragging={dragging || undefined}
				data-starting={starting || undefined}
				aria-busy={starting || undefined}
				data-attached-top={attachedTop && !queuedDock && !elicitation ? true : undefined}
			onClick={(e) => {
					if (controlsDisabled) return;
					if ((e.target as HTMLElement).closest('[contenteditable="true"]')) return;
					// The focusable context tooltip must keep its focus on click.
					if (
						e.target === e.currentTarget ||
						!(e.target as HTMLElement).closest("button, a, [role='option'], [data-context-meter], ul")
					) {
						editor.current?.focus();
					}
				}}
				className="cursor-chat-composer relative flex cursor-text flex-col gap-1.5 px-3 pt-3 pb-3"
			>
				<span aria-hidden="true" className="chat-composer-startup-shimmer" />
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

				{editingQueuedTurnId ? (
					<div className="flex items-center justify-between text-xs text-muted-foreground">
						<span>Editing queued message</span>
						<button
							type="button"
							disabled={controlsDisabled || queuedEditRecovery}
							onClick={onCancelQueuedEdit}
							className="rounded px-1.5 py-0.5 hover:text-foreground focus-visible:outline focus-visible:outline-ring"
						>
							Cancel edit
						</button>
					</div>
				) : null}
				{staged ? (
					<ul className="flex flex-wrap gap-1.5" aria-label="Attached files">
						{stripAttachments.map(({ file, path, preview }) => {
							return (
							<li
								key={file.id}
								className="flex items-center gap-1.5 rounded bg-interactive-hover py-0.5 pl-0.5 pr-1"
							>
								{preview ? (
									<img src={preview} alt="" className="size-6 rounded-sm object-cover" />
								) : (
									<div className="flex size-6 items-center justify-center rounded-sm bg-surface">
										<File aria-hidden="true" className="size-3.5 text-muted-foreground" />
									</div>
								)}
								<span
									className="max-w-[120px] truncate text-xs text-muted-foreground"
									title={file.name}
								>
									{file.name}
								</span>
								<button
									type="button"
									onClick={() => {
									if (submitInFlight.current) return;
									if (path) editor.current?.removeImage(path);
									if (visibleRetainedAttachments.some((attachment) => attachment.id === file.id)) {
										const next = retainedAttachments.filter((attachment) => attachment.id !== file.id);
										setRetainedAttachments(next);
										onQueuedRetainedAttachmentsChange?.(next);
									} else fileAttachments.remove(file.id);
									}}
									disabled={controlsDisabled || queuedEditRecovery || draftMutationPending || fileAttachments.preparing}
									aria-label={`Remove ${file.name}`}
									className="text-muted-foreground hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
								>
									<X aria-hidden="true" className="size-3" />
								</button>
							</li>
							);
						})}
					</ul>
				) : null}
				{contextReferences.length > 0 ? (
					<ChatAnnotationSummary
						annotations={contextReferences}
						onSelect={onSelectAnnotation}
						disabled={controlsDisabled || draftMutationPending}
						onRemove={(excerpt) => {
							if (!draftScope || submitInFlight.current || !excerpt.id) return;
							const result = writeChatExcerptReferences(
								draftScope,
								contextReferences.filter((item) => item.id !== excerpt.id),
							);
							composerRevision.current = result.draft.composer.revision;
							setTextDraftPersistenceError(result.ok ? null : "chat.draft.saveFailed");
						}}
					/>
				) : null}

				<ComposerEditor
					ref={editor}
					images={composerImages}
					disabled={Boolean(disabled || queuedEditRecovery || (!optimisticSend && (submitting || draftMutationPending)))}
					enterSends={enterSends}
					concealed={sendConcealed && !optimisticSend}
					label="Message the agent"
					placeholder={
						disabledPlaceholder ?? (disabled
							? "The controller is not connected"
							: willQueue
								? (queuePlaceholder ?? "Next message you send will be queued")
								: emptyPlaceholder ?? "Message the agent")
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
					<p role="alert" className="px-1.5 text-xs leading-snug text-destructive">
						{translateDraft(attachmentError)}
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
							{translateDraft("chat.draft.abandon")}
						</Button>
					</div>
				) : null}
				{fileAttachments.preparing ? (
					<p role="status" className="px-1.5 text-xs leading-snug text-muted-foreground">
						Saving attachments… Wait before leaving this chat.
					</p>
				) : null}

				{/* A refused steer is an ordinary outcome, not a failure: the text is still
			    in the box and the message says which of "send it instead" and "try again
			    in a moment" applies. */}
				{steerRefusal ?? steerOutcomeNotice ? (
					<p role="status" className="px-1.5 text-xs leading-snug text-warning">
						{translateDraft(steerRefusal ?? steerOutcomeNotice)}
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
									disabled={controlsDisabled}
									onChange={(event) => {
										if (!disabled && !submitInFlight.current && !draftMutationPending && !fileAttachments.preparing) {
											attachFiles(Array.from(event.target.files ?? []));
										}
										// Cleared so picking the same file twice still fires a change.
										event.target.value = "";
									}}
								/>
								<Tooltip>
									<TooltipTrigger asChild>
										<span className="inline-flex">
											<Button
												type="button"
												variant="ghost"
												size="icon-sm"
												disabled={controlsDisabled || queuedEditRecovery || draftMutationPending || fileAttachments.preparing}
												onClick={() => filePicker.current?.click()}
												aria-label="Attach a file"
												className="size-7 shrink-0 rounded-full p-0 text-muted-foreground hover:bg-interactive-active! hover:text-foreground"
											>
												<Plus aria-hidden="true" className="size-3.5 text-muted-foreground" />
											</Button>
										</span>
									</TooltipTrigger>
									<TooltipContent side="bottom">Attach a file</TooltipContent>
								</Tooltip>
							</>
						) : null}
						{settings}
					</div>

					<div role="group" aria-label="Send message controls" className="flex h-7 shrink-0 items-center">
						{showSteerButton && canSteerDraft && (hasText || staged) ? (
							<Button type="button" variant="ghost" size="sm" disabled={!sendActionEnabled} onClick={() => void submit(undefined, true)} aria-label="Steer into running turn" className="h-7 px-2 text-xs">
								Steer
							</Button>
						) : null}
						<Tooltip>
							<TooltipTrigger asChild>
								<span className="inline-flex">
									<Button
										type={canStopTurn ? "button" : "submit"}
										variant="ghost"
										size="icon-sm"
										disabled={canStopTurn ? false : !sendActionEnabled}
										onClick={canStopTurn ? onInterrupt : undefined}
										aria-label={canStopTurn ? "Stop turn" : sendActionLabel}
										className={cn(
											"size-7 rounded-full border-transparent focus-visible:ring-ring/40",
											canStopTurn || sendActionEnabled
												? "bg-foreground text-background hover:bg-foreground/90 hover:text-background dark:hover:bg-foreground/90 dark:hover:text-background"
												: "bg-primary text-primary-foreground",
										)}
									>
										{canStopTurn ? (
											<Square aria-hidden="true" className="size-2.5 fill-current" />
										) : submitting || steerPending || savingQueuedEditPending || sendPending ? (
											<Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
										) : showsSteer ? (
											<CornerUpRight aria-hidden="true" className="size-3.5" />
										) : (
											<ArrowUp aria-hidden="true" className="size-3.5" />
										)}
									</Button>
								</span>
							</TooltipTrigger>
							<TooltipContent side="bottom">{canStopTurn ? "Stop turn" : durableDelivery ? sendActionLabel : sendHint}</TooltipContent>
						</Tooltip>
					</div>
				</div>
			</form>,
	);
});
