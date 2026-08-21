import { afterEach, describe, expect, it, vi } from "vitest";
import * as sse from "./sse";

const { parseSseFrame, takeSseFrames } = sse;

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

	it("captures the event name of control frames", () => {
		const parsed = parseSseFrame(
			'event: events_cursor_reset\nid: 20000\ndata: {"requested":5,"after":20000}',
		);
		expect(parsed?.event).toBe("events_cursor_reset");
		expect(parsed?.seq).toBe(20000);
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

	it("fans a cursor reset out to every session's listeners", () => {
		const registry = sse.createConversationEventRegistry();
		const received: string[] = [];
		registry.subscribe("session-1", (next) => received.push(`1:${next.type}:${next.seq}`));
		registry.subscribe("session-2", (next) => received.push(`2:${next.type}:${next.seq}`));

		registry.publishReset(20000);

		expect(received).toEqual([
			"1:events_cursor_reset:20000",
			"2:events_cursor_reset:20000",
		]);
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
