export const SET_CHAT_DRAFT_RISK_CHANNEL = "chat-draft:set-risk";

export const CHAT_DRAFT_BOUNDARY_KINDS = [
	"persistence-failed",
	"pending-delivery",
	"pending-attachments",
] as const;

export type ChatDraftBoundaryKind = (typeof CHAT_DRAFT_BOUNDARY_KINDS)[number];

export const CHAT_DRAFT_BOUNDARY_COPY: Record<ChatDraftBoundaryKind, string> = {
	"pending-attachments":
		"Attachments are still being saved. Leaving now will discard any files AO has not finished writing to the worktree. Wait for saving to finish.",
	"pending-delivery":
		"A Chat delivery recovery is still pending. Its recovery ID is saved, but finish or explicitly retry recovery before leaving this chat.",
	"persistence-failed":
		"This Chat draft could not be saved locally. Leaving now will discard the unsaved changes. Copy the draft before leaving.",
};

export function isChatDraftBoundaryKind(value: unknown): value is ChatDraftBoundaryKind {
	return typeof value === "string" && CHAT_DRAFT_BOUNDARY_KINDS.includes(value as ChatDraftBoundaryKind);
}

/** Parse an untrusted IPC payload into one canonical risk set. */
export function parseChatDraftBoundaryKinds(
	value: unknown,
): readonly ChatDraftBoundaryKind[] | undefined {
	if (!Array.isArray(value) || !value.every(isChatDraftBoundaryKind)) return undefined;
	const requested = new Set(value);
	return CHAT_DRAFT_BOUNDARY_KINDS.filter((kind) => requested.has(kind));
}
