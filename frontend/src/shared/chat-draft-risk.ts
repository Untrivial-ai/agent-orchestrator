export const SET_CHAT_DRAFT_RISK_CHANNEL = "chat-draft:set-risk";

export const CHAT_DRAFT_BOUNDARY_KINDS = [
	"persistence-failed",
	"pending-attachments",
] as const;

export type ChatDraftBoundaryKind = (typeof CHAT_DRAFT_BOUNDARY_KINDS)[number];

/** Resolved renderer translations, also used by Electron's native confirmation. */
export type ChatDraftDialogCopy = {
	title: string;
	message: string;
	detail: string;
	stay: string;
	leave: string;
};

export function parseChatDraftDialogCopy(value: unknown): ChatDraftDialogCopy | undefined {
	if (!value || typeof value !== "object") return undefined;
	const copy = value as Record<string, unknown>;
	if (
		typeof copy.title !== "string" || typeof copy.message !== "string" ||
		typeof copy.detail !== "string" || typeof copy.stay !== "string" ||
		typeof copy.leave !== "string"
	) return undefined;
	return { title: copy.title, message: copy.message, detail: copy.detail, stay: copy.stay, leave: copy.leave };
}

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
