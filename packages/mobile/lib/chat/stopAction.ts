import { conversationErrorCode } from "./conversationErrors";

/** Run one already-confirmed Stop scope. A stale scope is never retried: the
 * authoritative refresh must reach the UI so the user reviews and confirms the
 * changed queue themselves. */
export async function runConversationStop(
	queuedTurnIds: string[],
	interrupt: (queuedTurnIds: string[]) => Promise<void>,
	refresh: () => Promise<void>,
): Promise<void> {
	try {
		await interrupt(queuedTurnIds);
	} catch (cause) {
		if (conversationErrorCode(cause) === "CHAT_QUEUE_SCOPE_CHANGED") {
			await refresh();
		}
		throw cause;
	}
}
