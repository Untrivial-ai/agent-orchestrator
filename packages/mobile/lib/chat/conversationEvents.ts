import AsyncStorage from "@react-native-async-storage/async-storage";
import { useEffect } from "react";
import { isConfigured, type ServerConfig } from "../config";
import { streamGlobalConversationEvents } from "./api";
import { eventCursorKey, initialCursorFor } from "./eventCursor";
import {
	createConversationEventRegistry,
	createCursorPersister,
	type ConversationEvent,
} from "./sse";

const RECONNECT_MIN_MS = 1_000;
const RECONNECT_MAX_MS = 15_000;
const registry = createConversationEventRegistry();

export function subscribeConversationEvents(
	sessionId: string,
	listener: (event: ConversationEvent) => void,
): () => void {
	return registry.subscribe(sessionId, listener);
}

/** Own the daemon's global CDC stream for the lifetime of the configured app. */
export function useConversationEventTransport(cfg: ServerConfig | null): void {
	useEffect(() => {
		if (!cfg || !isConfigured(cfg)) return;
		let stopped = false;
		let controller: AbortController | undefined;
		const cursorKey = eventCursorKey(cfg);
		const cursorPersister = createCursorPersister((cursor) =>
			AsyncStorage.setItem(cursorKey, String(cursor)),
		);
		const run = async () => {
			// Missing or unusable starts at head, not zero: a fresh client has no
			// reason to replay the whole change log to reach the live tail.
			let cursor = initialCursorFor(await AsyncStorage.getItem(cursorKey));
			let delay = RECONNECT_MIN_MS;
			while (!stopped) {
				controller = new AbortController();
				try {
					cursor = await streamGlobalConversationEvents(
						cfg,
						cursor,
						controller.signal,
						(event) => {
							cursor = Math.max(cursor, event.seq);
							cursorPersister.update(cursor);
							registry.publish(event);
						},
						(resetCursor) => {
							cursor = resetCursor;
							cursorPersister.replace(resetCursor);
						},
						{
							// Only the chat screen currently on top subscribes, so most frames
							// have no reader — and during a cold-start replay, when no chat is
							// open at all, none of them do. Those only need to move the cursor.
							wantsPayload: () => registry.hasListeners(),
							onCursorAdvance: (seq) => {
								cursor = Math.max(cursor, seq);
								cursorPersister.update(cursor);
							},
						},
					);
					delay = RECONNECT_MIN_MS;
				} catch {
					if (stopped || controller.signal.aborted) return;
				}
				await wait(delay);
				delay = Math.min(RECONNECT_MAX_MS, delay * 2);
			}
		};
		void run();
		return () => {
			stopped = true;
			controller?.abort();
			cursorPersister.flush();
		};
	}, [cfg]);
}

function wait(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}
