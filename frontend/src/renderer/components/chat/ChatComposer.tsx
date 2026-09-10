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
	useCallback,
	useEffect,
	useId,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
	isValidElement,
	memo,
	type ClipboardEvent,
	type DragEvent,
	type FormEvent,
	type KeyboardEvent,
	type ReactElement,
	type ReactNode,
} from "react";
import { ArrowUp, Loader2, Plus, Square, X } from "lucide-react";
import { Button } from "../ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { cn } from "../../lib/utils";
import { apiErrorMessage } from "../../lib/api-client";
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
	MAX_ATTACHMENTS,
	type FileAttachmentPayload,
} from "../../hooks/useFileAttachments";
import { File } from "lucide-react";
import type { ChatSkill } from "../../types/conversation";

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

/** A retained server-owned attachment; image bytes stay in durable storage. */
export type StoredComposerAttachment = {
	id: string;
	name: string;
	path?: string;
	dataUrl?: string;
	contentIndex?: number;
	contentType?: string;
};

export const ChatComposer = memo(function ChatComposer({
	onSend,
	busy,
	willQueue,
	disabled,
	disabledPlaceholder,
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
	sendPending,
	steerPending,
	steerRefusal,
	draftSeed,
	editingQueuedTurnId,
	onCancelQueuedEdit,
	savingQueuedEditPending,
	commandError,
	attachedTop = false,
	queuedDock,
	onCompact,
	compacting,
	compactUnavailable,
	compactBlocked,
	autoFocusKey,
	autoFocus = true,
}: {
	onSend: (text: string, attachments?: FileAttachmentPayload[], retainedContent?: number[]) => void | Promise<unknown>;
	settings?: ReactNode;
	/** A provider decision that temporarily replaces ordinary message entry. */
	approval?: ReactNode;
	/** A send is in flight. */
	busy?: boolean;
	/** The agent is mid-turn, so this message is held until the turn ends. */
	willQueue?: boolean;
	disabled?: boolean;
	/** Explains why message entry is temporarily blocked. */
	disabledPlaceholder?: string;
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
	onSteer?: (text: string, attachments?: FileAttachmentPayload[]) => Promise<unknown>;
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
	draftSeed?: { id: string; text: string; attachments?: StoredComposerAttachment[] };
	/** A queued turn being edited in the composer instead of the dock. */
	editingQueuedTurnId?: string;
	onCancelQueuedEdit?: () => void;
	/** The queued edit mutation is in flight for the turn being edited. */
	savingQueuedEditPending?: boolean;
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
}) {
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
	const [submitting, setSubmitting] = useState(false);
	const [steerNextRequest, setSteerNextRequest] = useState(0);
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
	const stagedDelivery = useRef<{ signature: string; paths: string[] } | null>(null);
	const submitInFlight = useRef<Promise<void> | null>(null);
	// Disabling the active editor can move focus to the document body. Remember
	// keyboard-origin submissions so focus can return once the editor is enabled.
	const restoreFocusAfterSubmission = useRef(false);
	const menuId = useId();
	const hadQueuedDockRef = useRef(Boolean(queuedDock));
	const previousTrigger = useRef<ComposerTrigger | undefined>(undefined);
	const triggerRef = useRef<ComposerTrigger | undefined>(undefined);

	const ordinaryAttachments = useFileAttachments();
	const editAttachments = useFileAttachments();
	const fileAttachments = editingQueuedTurnId ? editAttachments : ordinaryAttachments;
	const [retainedAttachments, setRetainedAttachments] = useState<StoredComposerAttachment[]>(draftSeed?.attachments ?? []);
	const visibleRetainedAttachments = editingQueuedTurnId ? retainedAttachments : [];
	const ordinaryDraftRef = useRef("");
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

	const staged = fileAttachments.attachments.length > 0 || visibleRetainedAttachments.length > 0;
	const controlsDisabled = Boolean(disabled || submitting);
	const hasDraft = hasText || staged;
	const savingQueuedEdit = Boolean(editingQueuedTurnId);
	const canSend =
		(hasText || staged) &&
		!controlsDisabled &&
		!steerPending &&
		!savingQueuedEditPending &&
		(savingQueuedEdit || !busy);
	const canStopTurn = Boolean(
		willQueue && onInterrupt && !controlsDisabled && !hasDraft && !savingQueuedEdit,
	);
	// Cmd/Ctrl+Enter remains an intentionally quiet power-user path for steering
	// the current draft into the running turn. The visible hint stays queue-only.
	const canSteerDraft = Boolean(canSteer && onSteer) && !savingQueuedEdit;
	const canSteerNext =
		Boolean(canSteer && onSteer) &&
		!controlsDisabled &&
		!hasDraft &&
		!savingQueuedEdit &&
		Boolean(queuedDock);
	const sendHint = menuOpen
		? "Enter to insert"
		: savingQueuedEdit
			? "⏎ save edit"
			: willQueue
				? "⏎ queue"
				: "Enter to send";
	const draftSeedId = draftSeed?.id;
	const draftSeedText = draftSeed?.text;
	const queuedDockWithSteer = isValidElement(queuedDock)
		? cloneElement(
				queuedDock as ReactElement<{
					canSteerNext?: boolean;
					steerNextRequest?: number;
					disabled?: boolean;
				}>,
				{ canSteerNext, steerNextRequest, disabled: submitting },
			)
		: queuedDock;

	const focusEditor = useCallback(() => {
		if (!autoFocus || disabled) return;
		editor.current?.focus();
	}, [autoFocus, disabled]);

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

	const clearEditor = useCallback(() => {
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

	const previousEditingQueuedTurnIdRef = useRef(editingQueuedTurnId);
	const previousEditSeedIdRef = useRef(draftSeedId);
	useLayoutEffect(() => {
		const previous = previousEditingQueuedTurnIdRef.current;
		if (previous === editingQueuedTurnId && previousEditSeedIdRef.current === draftSeedId) return;
		previousEditSeedIdRef.current = draftSeedId;
		previousEditingQueuedTurnIdRef.current = editingQueuedTurnId;
		stagedDelivery.current = null;
		editAttachments.clear();
		setRetainedAttachments(draftSeed?.attachments ?? []);
		if (!previous && editingQueuedTurnId) {
			ordinaryDraftRef.current = textRef.current;
		} else if (previous && !editingQueuedTurnId) {
			const text = ordinaryDraftRef.current;
			textRef.current = text;
			hasTextRef.current = text.trim().length > 0;
			setHasText(hasTextRef.current);
			editor.current?.setText(text);
			setSendError(null);
		}
	}, [draftSeed, draftSeedId, editingQueuedTurnId, editAttachments.clear]);

	useEffect(() => {
		if (draftSeedText === undefined) return;
		textRef.current = draftSeedText;
		hasTextRef.current = draftSeedText.trim().length > 0;
		setHasText(hasTextRef.current);
		editor.current?.setText(draftSeedText);
		dismissedKeyRef.current = null;
		setDismissedKey(null);
		highlightedRef.current = 0;
		setHighlighted(0);
		setSendError(null);
	}, [draftSeedId, draftSeedText]);

	const onEditorChange = useCallback((snapshot: ComposerEditorSnapshot) => {
		textRef.current = snapshot.text;
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
	}, []);

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
		const currentText = textRef.current;
		const body = currentText.trim();

		// `/compact` is a local AO command. It must refuse while a turn is running
		// without being blocked by the ordinary busy send gate.
		if (body === "/compact" && onCompact && !editingQueuedTurnId) {
			setSendError(null);
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
			try {
				await onCompact();
			} catch {
				setSendError("Conversation history could not be compacted. Try again.");
				return;
			}
			clearEditor();
			setDismissedKey(null);
			setHighlighted(0);
			return;
		}

		// Paste/drop reads finish asynchronously. Settle them before deciding whether
		// this submission has attachments, or immediate Enter can steer the text and
		// leave the image behind when its FileReader completes.
		const attachmentPayloads = await fileAttachments.toSettledPayload();
		const hasAttachments = attachmentPayloads.length > 0;
		const imageCount = visibleRetainedAttachments.filter((attachment) => attachment.contentType === "image").length
			+ (nativeImages ? attachmentPayloads.filter((attachment) => isSupportedImageAttachment(attachment.mimeType)).length : 0);
		if (imageCount > MAX_ATTACHMENTS) {
			setSendError(`You can attach up to ${MAX_ATTACHMENTS} images.`);
			return;
		}
		const canSubmitNow =
			(body.length > 0 || hasAttachments || visibleRetainedAttachments.length > 0) &&
			!disabled &&
			!steerPending &&
			!savingQueuedEditPending &&
			(editingQueuedTurnId || !busy);
		if (!canSubmitNow) {
			if (
				(body.length > 0 || hasAttachments) &&
				busy &&
				!disabled &&
				!steerPending &&
				!savingQueuedEditPending &&
				!editingQueuedTurnId
			) {
				setSendError("Still sending the previous message. Try again in a moment.");
			}
			return;
		}
		setSendError(null);

		const shouldSteer = forceSteer ?? false;
		const retainedPaths = visibleRetainedAttachments.flatMap((attachment) => attachment.path ? [attachment.path] : []);
		let message = withAttachmentReferences(body, retainedPaths);
		let nativePayloads: FileAttachmentPayload[] = [];
		if (hasAttachments) {
			if (!onStageAttachments) {
				setSendError("The files could not be attached. Nothing was sent.");
				return;
			}
			// Staged before delivery so a failed write is reported instead of a
			// message that claims attachments the agent cannot open.
			let paths: string[];
			const signature = fileAttachments.attachmentSignature();
			try {
				if (stagedDelivery.current?.signature === signature) {
					paths = stagedDelivery.current.paths;
				} else {
					paths = await onStageAttachments(attachmentPayloads);
					stagedDelivery.current = { signature, paths };
				}
			} catch {
				setSendError("The files could not be attached. Nothing was sent.");
				return;
			}
			message = withAttachmentReferences(body, [...retainedPaths, ...paths]);
			nativePayloads = attachmentPayloads.filter((attachment) =>
				isSupportedImageAttachment(attachment.mimeType),
			);
		}

		// Steering keeps the draft and attachments in the box until the provider has
		// taken them. The turn is already running, so a refusal is a real possibility —
		// and clearing early would lose context the user intended to send.
		if (shouldSteer && onSteer && !editingQueuedTurnId) {
			try {
				if (nativeImages && nativePayloads.length > 0) await onSteer(message, nativePayloads);
				else await onSteer(message);
			} catch {
				// The refusal is the daemon's typed answer and the surface renders it from
				// `steerRefusal`; keep the draft for an ordinary Enter queue retry.
				return;
			}
			stagedDelivery.current = null;
			if (hasAttachments) fileAttachments.clear();
			clearEditor();
			setDismissedKey(null);
			setHighlighted(0);
			return;
		}

		try {
			if (editingQueuedTurnId) {
				const retainedContent = visibleRetainedAttachments.flatMap((attachment) =>
					attachment.contentIndex === undefined ? [] : [attachment.contentIndex],
				).sort((a, b) => a - b);
				await onSend(message, nativeImages && nativePayloads.length ? nativePayloads : undefined, retainedContent);
				// Exiting edit mode restores the separate ordinary draft. Do not clear it
				// from this submission's continuation after the parent changes modes.
				return;
			}
			if (nativeImages && nativePayloads.length > 0) await onSend(message, nativePayloads);
			else await onSend(message);
		} catch (error) {
			setSendError(
				editingQueuedTurnId
					? apiErrorMessage(error, "Could not save that queued message edit. Your draft was kept.")
					: hasAttachments
						? "Message not sent. Your draft and attachments were kept so you can retry."
						: "Message not sent. Your draft was kept so you can retry.",
			);
			return;
		}
		if (hasAttachments) {
			stagedDelivery.current = null;
			fileAttachments.clear();
		}

		clearEditor();
		setDismissedKey(null);
		setHighlighted(0);
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

			if (canSteerNext && !textRef.current.trim() && !fileAttachments.hasPendingReads()) {
				setSteerNextRequest((request) => request + 1);
				return true;
			}
			const wantsSteer = (event.metaKey || event.ctrlKey) && canSteerDraft;
			void submit(undefined, wantsSteer);
			return true;
		},
		[canSteerDraft, canSteerNext, fileAttachments, onCompact, pick, suggestionsFor],
	);

	function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
		if (event.nativeEvent.isComposing) return;
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

		if (event.key === "Escape" && editingQueuedTurnId && onCancelQueuedEdit && !submitInFlight.current) {
			event.preventDefault();
			onCancelQueuedEdit();
		}
	}

	function onPaste(event: ClipboardEvent<HTMLDivElement>) {
		if (!canAttach || submitInFlight.current) return;
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
		if (!canAttach || submitInFlight.current) return;
		const files = Array.from(event.dataTransfer?.files ?? []);
		if (files.length === 0) return;
		event.preventDefault();
		event.stopPropagation();
		void fileAttachments.addFiles(files);
	}

	// Keep the hidden Cmd/Ctrl steering shortcut available for the send-button path
	// without rerendering the composer for every modifier key event.
	const modifierHeldRef = useRef(false);
	useEffect(() => {
		const onKey = (event: globalThis.KeyboardEvent) => {
			modifierHeldRef.current = event.metaKey || event.ctrlKey;
		};
		const onBlur = () => {
			modifierHeldRef.current = false;
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

	const attachmentError = fileAttachments.error ?? sendError ?? commandError;
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
				{form}
			</div>
		);

	if (approval) {
		return withQueueStack(
			<form
				onSubmit={(event) => event.preventDefault()}
				data-attached-top={attachedTop && !queuedDock ? true : undefined}
				className="cursor-chat-composer relative flex flex-col gap-1.5 border px-3 py-3"
			>
				{approval}
				{commandError ? (
					<p role="alert" className="px-1.5 text-[11px] leading-snug text-destructive">
						{commandError}
					</p>
				) : null}
			</form>,
		);
	}

	return withQueueStack(
		<form
			// Cmd/Ctrl steering remains available as a quiet power-user action.
			onSubmit={(event) => void submit(event, modifierHeldRef.current && canSteerDraft)}
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
				data-attached-top={attachedTop && !queuedDock ? true : undefined}
				onClick={(e) => {
					if (controlsDisabled) return;
					if (
						e.target === e.currentTarget ||
						!(e.target as HTMLElement).closest("button, a, [role='option'], ul")
					) {
						editor.current?.focus();
					}
				}}
				className="cursor-chat-composer relative flex cursor-text flex-col gap-1.5 border px-3 pt-3 pb-3"
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

				{editingQueuedTurnId ? (
					<div className="flex items-center justify-between text-xs text-muted-foreground">
						<span>Editing queued message</span>
						<button
							type="button"
							disabled={controlsDisabled}
							onClick={onCancelQueuedEdit}
							className="rounded px-1.5 py-0.5 hover:text-foreground focus-visible:outline focus-visible:outline-ring"
						>
							Cancel edit
						</button>
					</div>
				) : null}
				{staged ? (
					<ul className="flex flex-wrap gap-1.5" aria-label="Attached files">
						{[...visibleRetainedAttachments, ...fileAttachments.attachments].map((file) => (
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
									onClick={() => {
									if (submitInFlight.current) return;
									if (visibleRetainedAttachments.some((attachment) => attachment.id === file.id)) {
										setRetainedAttachments((current) => current.filter((attachment) => attachment.id !== file.id));
									} else fileAttachments.remove(file.id);
									}}
									disabled={controlsDisabled}
									aria-label={`Remove ${file.name}`}
									className="text-muted-foreground hover:text-foreground"
								>
									<X aria-hidden="true" className="size-3" />
								</button>
							</li>
						))}
					</ul>
				) : null}

				<ComposerEditor
					ref={editor}
					disabled={controlsDisabled}
					label="Message the agent"
					placeholder={
						disabledPlaceholder ?? (disabled
							? "The controller is not connected"
							: willQueue
								? "Agent is working — this sends when it finishes"
								: "Message the agent…")
					}
					menuOpen={menuOpen}
					menuId={menuId}
					activeIndex={activeIndex}
					onChange={onEditorChange}
					onComplete={completeFromEditor}
					onEnterKey={handleEnterKey}
					onCompositionChange={setIsComposing}
					onKeyDown={onKeyDown}
					onPaste={onPaste}
				/>

				{attachmentError ? (
					<p role="alert" className="px-1.5 text-[11px] leading-snug text-destructive">
						{attachmentError}
					</p>
				) : null}

				{/* A refused steer is an ordinary outcome, not a failure: the text is still
			    in the box and the message says which of "send it instead" and "try again
			    in a moment" applies. */}
				{steerRefusal ? (
					<p role="status" className="px-1.5 text-[11px] leading-snug text-warning">
						{steerRefusal}
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
										if (!submitInFlight.current) {
											void fileAttachments.addFiles(Array.from(event.target.files ?? []));
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
												disabled={controlsDisabled}
												onClick={() => filePicker.current?.click()}
												aria-label="Attach a file"
												className="size-7 shrink-0 rounded-full p-0 text-muted-foreground hover:bg-white/5! hover:text-foreground"
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
						<Tooltip>
							<TooltipTrigger asChild>
								<span className="inline-flex">
									<Button
										type={canStopTurn ? "button" : "submit"}
										variant="ghost"
										size="icon-sm"
										disabled={canStopTurn ? false : !canSend}
										onClick={canStopTurn ? onInterrupt : undefined}
										aria-label={canStopTurn ? "Stop turn" : "Send message"}
										className={cn(
											"size-7 rounded-full border-transparent focus-visible:ring-ring/40",
											canStopTurn || canSend
												? "bg-foreground text-background hover:bg-foreground/90 hover:text-background dark:hover:bg-foreground/90 dark:hover:text-background"
												: "bg-primary text-primary-foreground",
										)}
									>
										{canStopTurn ? (
											<Square aria-hidden="true" className="size-2.5 fill-current" />
										) : submitting || steerPending || savingQueuedEditPending || sendPending ? (
											<Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
										) : (
											<ArrowUp aria-hidden="true" className="size-3.5" />
										)}
									</Button>
								</span>
							</TooltipTrigger>
							<TooltipContent side="bottom">{canStopTurn ? "Stop turn" : sendHint}</TooltipContent>
						</Tooltip>
					</div>
				</div>
			</form>,
	);
});
