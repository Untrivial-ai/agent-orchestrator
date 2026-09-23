export type ConversationEvent = {
	seq: number;
	projectId: string;
	sessionId?: string;
	type: string;
	payload?: { conversationId?: string; [key: string]: unknown };
	createdAt: string;
};

export type ConversationEventRegistry = {
	subscribe(sessionId: string, listener: (event: ConversationEvent) => void): () => void;
	publish(event: ConversationEvent): void;
	/** Whether any session has a listener, i.e. whether payloads are worth parsing. */
	hasListeners(): boolean;
};

/**
 * How long after the last listener leaves the stream keeps taking payloads.
 *
 * A skipped payload is unrecoverable: the cursor advances past it and the
 * stream never resends it. Skipping is right for a cold start with no chat
 * open, but a chat screen re-subscribing — which a network change causes, since
 * the config changes and effects re-run — leaves a gap in which live events
 * would be dropped. The reply then never appears until the screen is closed and
 * reopened. The grace covers that gap while still letting a genuinely idle app
 * skip a large backlog.
 */
export const LISTENER_GRACE_MS = 30_000;

export function createConversationEventRegistry(
	now: () => number = Date.now,
): ConversationEventRegistry {
	const listeners = new Map<string, Set<(event: ConversationEvent) => void>>();
	// 0 means nothing has ever subscribed, so a cold start still skips.
	let lastListenerAt = 0;
	return {
		subscribe(sessionId, listener) {
			const sessionListeners = listeners.get(sessionId) ?? new Set();
			sessionListeners.add(listener);
			listeners.set(sessionId, sessionListeners);
			lastListenerAt = now();
			return () => {
				sessionListeners.delete(listener);
				if (sessionListeners.size === 0) listeners.delete(sessionId);
				lastListenerAt = now();
			};
		},
		hasListeners() {
			if (listeners.size > 0) return true;
			if (lastListenerAt === 0) return false;
			return now() - lastListenerAt < LISTENER_GRACE_MS;
		},
		publish(event) {
			if (!event.sessionId) return;
			for (const listener of listeners.get(event.sessionId) ?? []) listener(event);
		},
	};
}

const CURSOR_PERSIST_DELAY_MS = 500;

/**
 * Events after which progress commits regardless of the debounce timer.
 *
 * The timer alone is not enough. A large cold-start replay keeps the JS thread
 * busy, and `setTimeout` is a macrotask — so under exactly the conditions where
 * saving progress matters most, the debounce never fires. A replay interrupted
 * before its first commit then restarts from the same cursor on the next launch,
 * forever. Counting events is immune to that, because it runs inline.
 */
export const CURSOR_PERSIST_EVENTS = 256;

export function createCursorPersister(
	persist: (cursor: number) => void | Promise<void>,
): { update(cursor: number): void; replace(cursor: number): void; flush(): void } {
	let latest = 0;
	let persisted = 0;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let sinceCommit = 0;

	const save = (cursor: number) => {
		try {
			const result = persist(cursor);
			if (result) void result.catch(() => {});
		} catch {
			// Cursor persistence is an optimization. Durable replay remains authoritative.
		}
	};
	const commit = () => {
		if (timer) clearTimeout(timer);
		timer = undefined;
		sinceCommit = 0;
		if (latest <= persisted) return;
		persisted = latest;
		save(latest);
	};

	return {
		update(cursor) {
			latest = Math.max(latest, cursor);
			// Whichever comes first: a quiet moment (the debounce) or enough events
			// that we refuse to risk losing the progress (the count).
			if (++sinceCommit >= CURSOR_PERSIST_EVENTS) {
				commit();
				return;
			}
			if (!timer) timer = setTimeout(commit, CURSOR_PERSIST_DELAY_MS);
		},
		replace(cursor) {
			if (timer) clearTimeout(timer);
			timer = undefined;
			sinceCommit = 0;
			latest = cursor;
			persisted = cursor;
			save(cursor);
		},
		flush() {
			commit();
		},
	};
}

/** Pull complete LF or CRLF SSE frames while preserving an incomplete tail. */
export function takeSseFrames(buffer: string): { frames: string[]; remainder: string } {
	const frames: string[] = [];
	let remainder = buffer;
	let boundary = /\r?\n\r?\n/.exec(remainder);
	while (boundary) {
		frames.push(remainder.slice(0, boundary.index));
		remainder = remainder.slice(boundary.index + boundary[0].length);
		boundary = /\r?\n\r?\n/.exec(remainder);
	}
	return { frames, remainder };
}

/**
 * The frame's sequence from its `id:` line alone, without touching `data:`.
 *
 * Advancing the cursor is all a frame is worth when nothing is subscribed to its
 * session, and that is the common case: the app subscribes only to the chat it
 * currently has open. Parsing the payload anyway is what makes a large replay
 * expensive, so this is the cheap path.
 */
export function readSseFrameSeq(frame: string): number | undefined {
	for (const raw of frame.split("\n")) {
		if (!raw.startsWith("id:")) continue;
		const seq = Number(raw.slice(3).trim());
		return Number.isFinite(seq) ? seq : undefined;
	}
	return undefined;
}

export function parseSseFrame(frame: string): ConversationEvent | undefined {
	let id = 0;
	const data: string[] = [];
	for (const raw of frame.replace(/\r/g, "").split("\n")) {
		if (raw.startsWith("id:")) id = Number(raw.slice(3).trim());
		else if (raw.startsWith("data:")) data.push(raw.slice(5).trimStart());
	}
	if (data.length === 0) return undefined;
	try {
		const event = JSON.parse(data.join("\n")) as ConversationEvent;
		if (!Number.isFinite(event.seq)) event.seq = id;
		return event;
	} catch {
		return undefined;
	}
}
