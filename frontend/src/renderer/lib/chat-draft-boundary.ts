import { appI18n } from "../i18n/instance";
import {
	parseChatDraftBoundaryKinds,
	type ChatDraftBoundaryKind,
	type ChatDraftDialogCopy,
} from "../../shared/chat-draft-risk";

export {
	type ChatDraftBoundaryKind,
} from "../../shared/chat-draft-risk";

export type ChatDraftBoundarySource = "composer" | "inline-edit" | "queued-edit";

const EMPTY_BOUNDARIES: readonly ChatDraftBoundaryKind[] = Object.freeze([]);
const boundaries = new Map<
	string,
	Map<ChatDraftBoundarySource, readonly ChatDraftBoundaryKind[]>
>();
const boundarySnapshots = new Map<string, readonly ChatDraftBoundaryKind[]>();
const listeners = new Set<() => void>();

function emitBoundaryChange(): void {
	for (const listener of listeners) listener();
}

function sameBoundaryKinds(
	left: readonly ChatDraftBoundaryKind[] | undefined,
	right: readonly ChatDraftBoundaryKind[],
): boolean {
	return Boolean(left && left.length === right.length && left.every((kind, index) => kind === right[index]));
}

function normalizeBoundaryKinds(
	kinds: ChatDraftBoundaryKind | readonly ChatDraftBoundaryKind[] | undefined,
): readonly ChatDraftBoundaryKind[] {
	if (!kinds) return EMPTY_BOUNDARIES;
	return parseChatDraftBoundaryKinds(Array.isArray(kinds) ? kinds : [kinds]) ?? EMPTY_BOUNDARIES;
}

function refreshBoundarySnapshot(sessionId: string): void {
	const sessionBoundaries = boundaries.get(sessionId);
	const next = sessionBoundaries
		? (parseChatDraftBoundaryKinds([...sessionBoundaries.values()].flat()) ?? EMPTY_BOUNDARIES)
		: EMPTY_BOUNDARIES;
	const previous = boundarySnapshots.get(sessionId) ?? EMPTY_BOUNDARIES;
	if (sameBoundaryKinds(previous, next)) return;
	if (next.length === 0) {
		boundarySnapshots.delete(sessionId);
	} else {
		boundarySnapshots.set(sessionId, Object.freeze(next));
	}
	emitBoundaryChange();
}

export function setChatDraftBoundary(
	sessionId: string,
	source: ChatDraftBoundarySource,
	kinds: ChatDraftBoundaryKind | readonly ChatDraftBoundaryKind[] | undefined,
): void {
	if (!sessionId) return;
	const normalized = normalizeBoundaryKinds(kinds);
	const sessionBoundaries = boundaries.get(sessionId);
	if (normalized.length === 0) {
		if (!sessionBoundaries?.delete(source)) return;
		if (sessionBoundaries.size === 0) boundaries.delete(sessionId);
		refreshBoundarySnapshot(sessionId);
		return;
	}
	if (sameBoundaryKinds(sessionBoundaries?.get(source), normalized)) return;
	const next =
		sessionBoundaries ?? new Map<ChatDraftBoundarySource, readonly ChatDraftBoundaryKind[]>();
	next.set(source, Object.freeze([...normalized]));
	boundaries.set(sessionId, next);
	refreshBoundarySnapshot(sessionId);
}

export function getChatDraftBoundary(sessionId: string): ChatDraftBoundaryKind | undefined {
	const active = getChatDraftBoundaries(sessionId);
	// A known failed write is more serious than an in-flight write and provides
	// the more accurate discard copy when both composer slices are unsafe.
	if (active.includes("persistence-failed")) return "persistence-failed";
	return active[0];
}

/** Every distinct risk currently active for one logical Chat session. */
export function getChatDraftBoundaries(sessionId: string): readonly ChatDraftBoundaryKind[] {
	return boundarySnapshots.get(sessionId) ?? EMPTY_BOUNDARIES;
}

export function subscribeChatDraftBoundaries(listener: () => void): () => void {
	listeners.add(listener);
	return () => listeners.delete(listener);
}

export function confirmDiscardChatDraft(
	kind: ChatDraftBoundaryKind,
	confirm: (message: string) => boolean,
): boolean {
	return confirmDiscardChatDrafts([kind], confirm);
}

export function chatDraftDiscardWarning(
	kinds: Iterable<ChatDraftBoundaryKind>,
): string | undefined {
	const warnings = [...new Set(kinds)].map(chatDraftBoundaryCopy);
	if (warnings.length === 0) return undefined;
	return `${warnings.join("\n\n")}\n\n${appI18n.t("chat.draftDiscard.question")}`;
}

export function confirmDiscardChatDrafts(
	kinds: Iterable<ChatDraftBoundaryKind>,
	confirm: (message: string) => boolean,
): boolean {
	const warning = chatDraftDiscardWarning(kinds);
	return warning ? confirm(warning) : true;
}

export function chatDraftBoundaryCopy(kind: ChatDraftBoundaryKind): string {
	return appI18n.t(kind === "persistence-failed" ? "chat.draftDiscard.persistenceFailed" : "chat.draftDiscard.pendingAttachments");
}

export function chatDraftDialogCopy(kinds: Iterable<ChatDraftBoundaryKind>): ChatDraftDialogCopy {
	return {
		title: appI18n.t("chat.draftDiscard.nativeTitle"),
		message: appI18n.t("chat.draftDiscard.nativeMessage"),
		detail: [...new Set(kinds)].map(chatDraftBoundaryCopy).join("\n\n"),
		stay: appI18n.t("chat.draftDiscard.stay"),
		leave: appI18n.t("chat.draftDiscard.leaveAnyway"),
	};
}
