import type { components } from "../../../api/schema";
import type { ChatDraftAttachment, ChatDraftExcerptReference } from "../../lib/chat-drafts";

export type SideDeliveryReceipt = {
	clientMessageId: string;
	request: components["schemas"]["SendSideQuestionRequest"];
	submitted: { text: string; attachments: ChatDraftAttachment[]; references: ChatDraftExcerptReference[] };
};

// Serialized only into the daemon's launch-scoped side store and Electron RAM.
export type SideChatDraft = {
	version: 1;
	text: string;
	attachments: ChatDraftAttachment[];
	references: ChatDraftExcerptReference[];
	pendingDelivery?: SideDeliveryReceipt;
};

export function emptySideChatDraft(): SideChatDraft {
	return { version: 1, text: "", attachments: [], references: [] };
}

export function parseSideChatDraft(raw: string): SideChatDraft {
	try {
		const value: unknown = JSON.parse(raw);
		if (value && typeof value === "object" && "version" in value && value.version === 1 &&
			"text" in value && typeof value.text === "string" && "attachments" in value &&
			Array.isArray(value.attachments) && "references" in value && Array.isArray(value.references)) {
			return value as SideChatDraft;
		}
	} catch { /* Earlier side drafts were plain text. */ }
	return { ...emptySideChatDraft(), text: raw };
}
