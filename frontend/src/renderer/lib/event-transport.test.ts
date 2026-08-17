import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const {
	onStatusMock,
	removeStatusMock,
	getApiBaseUrlMock,
	hasTrustedApiBaseUrlMock,
	subscribeApiBaseUrlMock,
	unsubscribeBaseUrlMock,
} = vi.hoisted(() => ({
	onStatusMock: vi.fn(),
	removeStatusMock: vi.fn(),
	getApiBaseUrlMock: vi.fn(() => "http://127.0.0.1:3001"),
	hasTrustedApiBaseUrlMock: vi.fn(() => true),
	subscribeApiBaseUrlMock: vi.fn(),
	unsubscribeBaseUrlMock: vi.fn(),
}));

vi.mock("./bridge", () => ({
	aoBridge: {
		daemon: { onStatus: onStatusMock },
	},
}));

vi.mock("./api-client", () => ({
	getApiBaseUrl: getApiBaseUrlMock,
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
	subscribeApiBaseUrl: subscribeApiBaseUrlMock,
}));

import { createEventTransport } from "./event-transport";
import { getEventsConnectionState, setEventsConnectionState } from "./events-connection";

class EventSourceStub {
	static instances: EventSourceStub[] = [];
	url: string;
	closed = false;
	readyState = 0; // CONNECTING
	onopen: (() => void) | null = null;
	onerror: (() => void) | null = null;
	onmessage: (() => void) | null = null;
	listeners: string[] = [];
	handlers = new Map<string, (event: Event) => void>();
	constructor(url: string) {
		this.url = url;
		EventSourceStub.instances.push(this);
	}
	addEventListener(type: string, listener: (event: Event) => void) {
		this.listeners.push(type);
		this.handlers.set(type, listener);
	}
	emit(type: string, data: string) {
		this.handlers.get(type)?.({ data } as unknown as Event);
	}
	close() {
		this.closed = true;
		this.readyState = 2; // CLOSED
	}
}

function fakeQueryClient() {
	return { invalidateQueries: vi.fn() } as unknown as Parameters<typeof createEventTransport>[0];
}

beforeEach(() => {
	EventSourceStub.instances = [];
	onStatusMock.mockReset().mockReturnValue(removeStatusMock);
	removeStatusMock.mockReset();
	getApiBaseUrlMock.mockReset().mockReturnValue("http://127.0.0.1:3001");
	hasTrustedApiBaseUrlMock.mockReset().mockReturnValue(true);
	subscribeApiBaseUrlMock.mockReset().mockReturnValue(unsubscribeBaseUrlMock);
	unsubscribeBaseUrlMock.mockReset();
	setEventsConnectionState("idle");
	(globalThis as unknown as { EventSource: unknown }).EventSource = EventSourceStub;
});

afterEach(() => {
	delete (globalThis as unknown as { EventSource?: unknown }).EventSource;
});

describe("createEventTransport", () => {
	it("opens a single SSE connection to the current base URL on connect", () => {
		createEventTransport(fakeQueryClient()).connect();

		expect(EventSourceStub.instances).toHaveLength(1);
		expect(EventSourceStub.instances[0].url).toBe("http://127.0.0.1:3001/api/v1/events");
		// All CDC event types plus onmessage are wired up.
		expect(EventSourceStub.instances[0].listeners).toContain("session_updated");
		expect(EventSourceStub.instances[0].onmessage).toBeTypeOf("function");
	});

	it("does not reconnect when a daemon status keeps the same base URL", () => {
		createEventTransport(fakeQueryClient()).connect();
		const onStatusHandler = onStatusMock.mock.calls[0][0] as () => void;

		onStatusHandler();

		expect(EventSourceStub.instances).toHaveLength(1);
	});

	it("closes the old connection and reconnects when the base URL changes", () => {
		createEventTransport(fakeQueryClient()).connect();
		const first = EventSourceStub.instances[0];
		const onStatusHandler = onStatusMock.mock.calls[0][0] as () => void;

		getApiBaseUrlMock.mockReturnValue("http://127.0.0.1:3099");
		onStatusHandler();

		expect(first.closed).toBe(true);
		expect(EventSourceStub.instances).toHaveLength(2);
		expect(EventSourceStub.instances[1].url).toBe("http://127.0.0.1:3099/api/v1/events");
	});

	it("closes the source and skips reconnecting when the base URL is untrusted", () => {
		createEventTransport(fakeQueryClient()).connect();
		const first = EventSourceStub.instances[0];
		const onStatusHandler = onStatusMock.mock.calls[0][0] as () => void;

		hasTrustedApiBaseUrlMock.mockReturnValue(false);
		onStatusHandler();

		expect(first.closed).toBe(true);
		expect(EventSourceStub.instances).toHaveLength(1);
		expect(getEventsConnectionState()).toBe("disconnected");
	});

	it("debounces workspace and session invalidation after a status change", () => {
		vi.useFakeTimers();
		try {
			const queryClient = fakeQueryClient();
			createEventTransport(queryClient).connect();
			const onStatusHandler = onStatusMock.mock.calls[0][0] as () => void;

			onStatusHandler();
			expect(queryClient.invalidateQueries).not.toHaveBeenCalled();
			vi.advanceTimersByTime(200);
			expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["workspaces"] });
			expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["session-agent-switches"] });
			expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["session-scm-summary"] });
			expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["session-usage"] });
		} finally {
			vi.useRealTimers();
		}
	});

	it("invalidates only the named conversation for conversation CDC", () => {
		vi.useFakeTimers();
		try {
			const queryClient = fakeQueryClient();
			createEventTransport(queryClient).connect();
			EventSourceStub.instances[0].emit(
				"session_updated",
				JSON.stringify({
					seq: 42,
					projectId: "proj-1",
					sessionId: "chat-1",
					type: "session_updated",
					payload: {
						id: "chat-1",
						sessionId: "chat-1",
						conversationId: "conv-1",
						activity: "active",
						isTerminated: false,
					},
					createdAt: "2026-08-04T15:15:14Z",
				}),
			);

			vi.advanceTimersByTime(200);
			expect(queryClient.invalidateQueries).toHaveBeenCalledWith({
				queryKey: ["conversation", "chat-1"],
			});
			expect(queryClient.invalidateQueries).not.toHaveBeenCalledWith({ queryKey: ["workspaces"] });
			expect(queryClient.invalidateQueries).not.toHaveBeenCalledWith({
				queryKey: ["session-scm-summary"],
			});
		} finally {
			vi.useRealTimers();
		}
	});

	it("invalidates the named interface transition status for transition CDC", () => {
		vi.useFakeTimers();
		try {
			const queryClient = fakeQueryClient();
			createEventTransport(queryClient).connect();
			EventSourceStub.instances[0].emit(
				"session_updated",
				JSON.stringify({
					seq: 43,
					projectId: "proj-1",
					sessionId: "session-1",
					type: "session_updated",
					payload: {
						id: "session-1",
						interfaceTransitionId: "transition-1",
						interfaceTransitionPhase: "recovery_required",
					},
					createdAt: "2026-08-13T08:00:00Z",
				}),
			);

			vi.advanceTimersByTime(200);
			expect(queryClient.invalidateQueries).toHaveBeenCalledWith({
				queryKey: ["session-interface-transition", "session-1"],
			});
		} finally {
			vi.useRealTimers();
		}
	});

	it("tears down the source and the daemon listener on disconnect", () => {
		const disconnect = createEventTransport(fakeQueryClient()).connect();

		disconnect();

		expect(EventSourceStub.instances[0].closed).toBe(true);
		expect(removeStatusMock).toHaveBeenCalledTimes(1);
	});

	it("is a no-op when EventSource is unavailable", () => {
		delete (globalThis as unknown as { EventSource?: unknown }).EventSource;

		expect(() => createEventTransport(fakeQueryClient()).connect()).not.toThrow();
		expect(EventSourceStub.instances).toHaveLength(0);
	});

	it("marks the stream connected on open and disconnected on error", () => {
		createEventTransport(fakeQueryClient()).connect();
		const source = EventSourceStub.instances[0];

		source.readyState = 1; // OPEN
		source.onopen?.();
		expect(getEventsConnectionState()).toBe("connected");

		source.readyState = 0; // CONNECTING — browser is auto-retrying
		source.onerror?.();
		expect(getEventsConnectionState()).toBe("disconnected");

		source.readyState = 1;
		source.onopen?.();
		expect(getEventsConnectionState()).toBe("connected");
	});

	it("rebuilds a source the browser abandoned after the retry delay", () => {
		vi.useFakeTimers();
		try {
			createEventTransport(fakeQueryClient()).connect();
			const source = EventSourceStub.instances[0];

			source.readyState = 2; // CLOSED — EventSource gave up for good
			source.onerror?.();

			expect(EventSourceStub.instances).toHaveLength(1);
			vi.advanceTimersByTime(5_000);
			expect(EventSourceStub.instances).toHaveLength(2);
			expect(EventSourceStub.instances[1].url).toBe("http://127.0.0.1:3001/api/v1/events");
		} finally {
			vi.useRealTimers();
		}
	});

	it("reconnects when the API base URL changes out-of-band", () => {
		createEventTransport(fakeQueryClient()).connect();
		expect(subscribeApiBaseUrlMock).toHaveBeenCalledTimes(1);
		const onBaseUrlChange = subscribeApiBaseUrlMock.mock.calls[0][0] as () => void;
		const first = EventSourceStub.instances[0];

		getApiBaseUrlMock.mockReturnValue("http://127.0.0.1:4555");
		onBaseUrlChange();

		expect(first.closed).toBe(true);
		expect(EventSourceStub.instances).toHaveLength(2);
		expect(EventSourceStub.instances[1].url).toBe("http://127.0.0.1:4555/api/v1/events");
	});

	it("resets the connection state and unsubscribes on disconnect", () => {
		const disconnect = createEventTransport(fakeQueryClient()).connect();
		const source = EventSourceStub.instances[0];
		source.readyState = 1;
		source.onopen?.();
		expect(getEventsConnectionState()).toBe("connected");

		disconnect();

		expect(getEventsConnectionState()).toBe("idle");
		expect(unsubscribeBaseUrlMock).toHaveBeenCalledTimes(1);
	});
});
