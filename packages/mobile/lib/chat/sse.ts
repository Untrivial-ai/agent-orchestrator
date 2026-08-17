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
};

export function createConversationEventRegistry(): ConversationEventRegistry {
	const listeners = new Map<string, Set<(event: ConversationEvent) => void>>();
	return {
		subscribe(sessionId, listener) {
			const sessionListeners = listeners.get(sessionId) ?? new Set();
			sessionListeners.add(listener);
			listeners.set(sessionId, sessionListeners);
			return () => {
				sessionListeners.delete(listener);
				if (sessionListeners.size === 0) listeners.delete(sessionId);
			};
		},
		publish(event) {
			if (!event.sessionId) return;
			for (const listener of listeners.get(event.sessionId) ?? []) listener(event);
		},
	};
}

const CURSOR_PERSIST_DELAY_MS = 500;

export function createCursorPersister(
	persist: (cursor: number) => void | Promise<void>,
): { update(cursor: number): void; replace(cursor: number): void; flush(): void } {
	let latest = 0;
	let persisted = 0;
	let timer: ReturnType<typeof setTimeout> | undefined;

	const save = (cursor: number) => {
		try {
			const result = persist(cursor);
			if (result) void result.catch(() => {});
		} catch {
			// Cursor persistence is an optimization. Durable replay remains authoritative.
		}
	};
	const commit = () => {
		timer = undefined;
		if (latest <= persisted) return;
		persisted = latest;
		save(latest);
	};

	return {
		update(cursor) {
			latest = Math.max(latest, cursor);
			if (!timer) timer = setTimeout(commit, CURSOR_PERSIST_DELAY_MS);
		},
		replace(cursor) {
			if (timer) clearTimeout(timer);
			timer = undefined;
			latest = cursor;
			persisted = cursor;
			save(cursor);
		},
		flush() {
			if (timer) clearTimeout(timer);
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
