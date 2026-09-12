import { afterEach, describe, expect, it, vi } from "vitest";
import * as sse from "./sse";

const { parseSseFrame, takeSseFrames, createConversationEventRegistry, LISTENER_GRACE_MS } = sse;

describe("mobile conversation SSE", () => {
	it("keeps an incomplete tail while reading multiple LF frames", () => {
		const result = takeSseFrames("id: 1\ndata: {\"seq\":1}\n\nid: 2\ndata: {\"seq\":2}\n\nid: 3\nda");
		expect(result.frames).toHaveLength(2);
		expect(result.remainder).toBe("id: 3\nda");
	});

	it("accepts CRLF boundaries from proxies", () => {
		const result = takeSseFrames("id: 4\r\ndata: {\"seq\":4}\r\n\r\n");
		expect(result.frames).toEqual(["id: 4\r\ndata: {\"seq\":4}"]);
		expect(parseSseFrame(result.frames[0])?.seq).toBe(4);
	});

	it("uses the SSE id when old daemons omit seq and ignores malformed data", () => {
		expect(parseSseFrame('id: 9\ndata: {"projectId":"p","type":"session_updated"}')?.seq).toBe(9);
		expect(parseSseFrame("id: 10\ndata: nope")).toBeUndefined();
	});

	// The cursor only needs the `id:` line. Skipping JSON.parse for frames nobody is
	// subscribed to is what keeps a 200k-event replay off the JS thread's critical path.
	it("reads a frame's sequence without parsing its payload", () => {
		expect(sse.readSseFrameSeq('id: 42\ndata: {"seq":42,"projectId":"p"}')).toBe(42);
		expect(sse.readSseFrameSeq("id: 43\r\ndata: nonsense-that-is-never-parsed")).toBe(43);
		expect(sse.readSseFrameSeq("data: {}")).toBeUndefined();
	});
});

describe("conversation cursor persistence", () => {
	afterEach(() => vi.useRealTimers());

	it("persists only the newest cursor once per event burst", () => {
		vi.useFakeTimers();
		const persisted: number[] = [];
		const createPersister = (
			sse as unknown as {
				createCursorPersister?: (persist: (cursor: number) => void) => {
					update(cursor: number): void;
				};
			}
		).createCursorPersister;
		const persister = createPersister?.((cursor) => persisted.push(cursor));

		persister?.update(1);
		persister?.update(3);
		persister?.update(2);
		expect(persisted).toEqual([]);
		vi.advanceTimersByTime(500);
		expect(persisted).toEqual([3]);
	});

	it("flushes the newest pending cursor during cleanup", () => {
		vi.useFakeTimers();
		const persisted: number[] = [];
		const createPersister = (
			sse as unknown as {
				createCursorPersister?: (persist: (cursor: number) => void) => {
					update(cursor: number): void;
					flush(): void;
				};
			}
		).createCursorPersister;
		const persister = createPersister?.((cursor) => persisted.push(cursor));

		persister?.update(7);
		persister?.flush();
		vi.runAllTimers();
		expect(persisted).toEqual([7]);
	});

	// A large cold-start replay saturates the JS thread, and the 500ms debounce is a
	// macrotask — the very thing a saturated thread never gets to. Progress therefore
	// has to commit on event count too, or a replay that is interrupted (or crashes the
	// app) resumes from zero on the next launch and repeats forever.
	it("persists progress by event count while timers are starved", () => {
		vi.useFakeTimers();
		const persisted: number[] = [];
		const persister = sse.createCursorPersister((cursor) => { persisted.push(cursor); });

		for (let seq = 1; seq <= sse.CURSOR_PERSIST_EVENTS; seq++) persister.update(seq);

		// No timer has been advanced: the count alone must have committed.
		expect(persisted).toEqual([sse.CURSOR_PERSIST_EVENTS]);
	});

	it("replaces a higher persisted cursor when the daemon reports a reset", () => {
		vi.useFakeTimers();
		const persisted: number[] = [];
		const persister = sse.createCursorPersister((cursor) => { persisted.push(cursor); });

		persister.update(100);
		vi.advanceTimersByTime(500);
		persister.replace(0);
		persister.update(1);
		vi.advanceTimersByTime(500);

		expect(persisted).toEqual([100, 0, 1]);
	});
});

describe("conversation event subscriptions", () => {
	it("publishes an event only to listeners for its session", () => {
		const createRegistry = (
			sse as unknown as {
				createConversationEventRegistry?: () => {
					subscribe(sessionId: string, listener: (event: sse.ConversationEvent) => void): () => void;
					publish(event: sse.ConversationEvent): void;
				};
			}
		).createConversationEventRegistry;
		const registry = createRegistry?.();
		const received: string[] = [];
		registry?.subscribe("session-1", () => received.push("session-1"));
		registry?.subscribe("session-2", () => received.push("session-2"));

		registry?.publish(event("session-2", 4));

		expect(received).toEqual(["session-2"]);
	});

	it("reports whether anything is listening, so the stream can skip parsing", () => {
		let now = 1_000_000;
		const registry = sse.createConversationEventRegistry(() => now);
		expect(registry.hasListeners()).toBe(false);

		const unsubscribe = registry.subscribe("session-1", () => {});
		expect(registry.hasListeners()).toBe(true);

		// Unsubscribing no longer flips this straight to false. A skipped payload
		// is unrecoverable, and a chat screen re-subscribing across a network
		// change would otherwise drop the events that arrive in the gap. It goes
		// false once the grace has passed with nothing listening.
		unsubscribe();
		expect(registry.hasListeners()).toBe(true);

		now += sse.LISTENER_GRACE_MS + 1;
		expect(registry.hasListeners()).toBe(false);
	});

	it("stops publishing after a listener unsubscribes", () => {
		const createRegistry = (
			sse as unknown as {
				createConversationEventRegistry?: () => {
					subscribe(sessionId: string, listener: (event: sse.ConversationEvent) => void): () => void;
					publish(event: sse.ConversationEvent): void;
				};
			}
		).createConversationEventRegistry;
		const registry = createRegistry?.();
		const received: number[] = [];
		const unsubscribe = registry?.subscribe("session-1", (next) => received.push(next.seq));
		unsubscribe?.();

		registry?.publish(event("session-1", 5));

		expect(received).toEqual([]);
	});
});

function event(sessionId: string, seq: number): sse.ConversationEvent {
	return {
		seq,
		projectId: "project-1",
		sessionId,
		type: "session_updated",
		payload: { conversationId: `conversation-${sessionId}` },
		createdAt: "2026-08-11T00:00:00Z",
	};
}

describe("registry listener grace", () => {
	// A payload skipped because nothing was listening is gone for good: the
	// cursor moves past it and the stream never resends it. That is fine for a
	// cold start with no chat open, but a chat screen re-subscribing — which is
	// exactly what a network change causes, as the config changes and effects
	// re-run — leaves a window where live events are dropped. The symptom is a
	// reply that never arrives until the screen is closed and reopened.
	it("still wants payloads briefly after the last listener goes", () => {
		let now = 1_000_000;
		const registry = createConversationEventRegistry(() => now);
		const off = registry.subscribe("s1", () => {});
		off();

		expect(registry.hasListeners()).toBe(true);
	});

	it("stops wanting payloads once nothing has listened for a while", () => {
		let now = 1_000_000;
		const registry = createConversationEventRegistry(() => now);
		const off = registry.subscribe("s1", () => {});
		off();
		now += LISTENER_GRACE_MS + 1;

		expect(registry.hasListeners()).toBe(false);
	});

	// A cold start where no chat has ever been opened must still skip the
	// backlog, which is what the optimisation is for.
	it("does not want payloads when nothing has ever subscribed", () => {
		const registry = createConversationEventRegistry(() => 1_000_000);
		expect(registry.hasListeners()).toBe(false);
	});

	it("wants payloads while a listener is active", () => {
		const registry = createConversationEventRegistry(() => 1_000_000);
		registry.subscribe("s1", () => {});
		expect(registry.hasListeners()).toBe(true);
	});
});
