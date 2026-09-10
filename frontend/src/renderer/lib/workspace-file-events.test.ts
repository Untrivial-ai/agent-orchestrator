import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { computeSseRetryDelayMs } from "./sse-backoff";

const { getApiBaseUrlMock, hasTrustedApiBaseUrlMock, subscribeApiBaseUrlMock, unsubscribeBaseUrlMock } = vi.hoisted(
	() => ({
		getApiBaseUrlMock: vi.fn(() => "http://127.0.0.1:3001"),
		hasTrustedApiBaseUrlMock: vi.fn(() => true),
		subscribeApiBaseUrlMock: vi.fn(),
		unsubscribeBaseUrlMock: vi.fn(),
	}),
);

vi.mock("./api-client", () => ({
	getApiBaseUrl: getApiBaseUrlMock,
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
	subscribeApiBaseUrl: subscribeApiBaseUrlMock,
}));

import { getWorkspaceFileConnectionState, subscribeWorkspaceFileChanges } from "./workspace-file-events";

let baseUrlListener: (() => void) | undefined;

class EventSourceStub {
	static instances: EventSourceStub[] = [];
	static throwNext = false;
	url: string;
	closed = false;
	readyState = 0;
	onopen: (() => void) | null = null;
	onerror: (() => void) | null = null;
	listeners = new Map<string, Set<() => void>>();

	constructor(url: string) {
		if (EventSourceStub.throwNext) {
			EventSourceStub.throwNext = false;
			throw new Error("connection setup failed");
		}
		this.url = url;
		EventSourceStub.instances.push(this);
	}

	addEventListener(type: string, listener: () => void) {
		const listeners = this.listeners.get(type) ?? new Set();
		listeners.add(listener);
		this.listeners.set(type, listeners);
	}

	dispatch(type: string) {
		for (const listener of this.listeners.get(type) ?? []) listener();
	}

	close() {
		this.closed = true;
		this.readyState = 2;
	}
}

function fakeQueryClient() {
	return { invalidateQueries: vi.fn() } as unknown as Parameters<typeof subscribeWorkspaceFileChanges>[1];
}

beforeEach(() => {
	EventSourceStub.instances = [];
	EventSourceStub.throwNext = false;
	baseUrlListener = undefined;
	getApiBaseUrlMock.mockReset().mockReturnValue("http://127.0.0.1:3001");
	hasTrustedApiBaseUrlMock.mockReset().mockReturnValue(true);
	subscribeApiBaseUrlMock.mockReset().mockImplementation((listener: () => void) => {
		baseUrlListener = listener;
		return unsubscribeBaseUrlMock;
	});
	unsubscribeBaseUrlMock.mockReset();
	(globalThis as unknown as { EventSource: unknown }).EventSource = EventSourceStub;
});

afterEach(() => {
	vi.useRealTimers();
	vi.restoreAllMocks();
	delete (globalThis as unknown as { EventSource?: unknown }).EventSource;
});

describe("subscribeWorkspaceFileChanges", () => {
	it("shares one daemon stream until the final Files view unmounts", () => {
		const queryClient = fakeQueryClient();
		const unsubscribeRail = subscribeWorkspaceFileChanges("session/a", queryClient);
		const unsubscribeMaximized = subscribeWorkspaceFileChanges("session/a", queryClient);

		expect(EventSourceStub.instances).toHaveLength(1);
		expect(EventSourceStub.instances[0].url).toBe(
			"http://127.0.0.1:3001/api/v1/sessions/session%2Fa/workspace/events",
		);

		unsubscribeRail();
		expect(EventSourceStub.instances[0].closed).toBe(false);
		unsubscribeMaximized();
		expect(EventSourceStub.instances[0].closed).toBe(true);
		expect(unsubscribeBaseUrlMock).toHaveBeenCalledTimes(1);
	});

	it("coalesces filesystem events and invalidates the list plus visible details", () => {
		vi.useFakeTimers();
		const queryClient = fakeQueryClient();
		const unsubscribe = subscribeWorkspaceFileChanges("sess-1", queryClient);
		const source = EventSourceStub.instances[0];

		source.dispatch("workspace_changed");
		source.dispatch("workspace_changed");
		vi.advanceTimersByTime(149);
		expect(queryClient.invalidateQueries).not.toHaveBeenCalled();
		vi.advanceTimersByTime(1);

		expect(queryClient.invalidateQueries).toHaveBeenCalledTimes(3);
		expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["session-workspace-files", "sess-1"] });
		expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["session-workspace-file", "sess-1"] });
		expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["session-workspace-tree", "sess-1"] });
		unsubscribe();
	});

	it("keeps one retry pending when another connect trigger arrives", () => {
		vi.useFakeTimers();
		vi.spyOn(Math, "random").mockReturnValue(0.5);
		EventSourceStub.throwNext = true;
		const unsubscribe = subscribeWorkspaceFileChanges("sess-retry", fakeQueryClient());

		expect(EventSourceStub.instances).toHaveLength(0);
		baseUrlListener?.();
		expect(EventSourceStub.instances).toHaveLength(0);

		// Backoff, not a flat 5s: the first retry is the initial step scaled by
		// the mocked jitter (see sse-backoff.ts).
		const firstRetryMs = computeSseRetryDelayMs(1, () => 0.5);
		vi.advanceTimersByTime(firstRetryMs - 1);
		expect(EventSourceStub.instances).toHaveLength(0);
		vi.advanceTimersByTime(1);
		expect(EventSourceStub.instances).toHaveLength(1);
		unsubscribe();
	});

	it("waits longer after each consecutive failure instead of a flat interval", () => {
		vi.useFakeTimers();
		vi.spyOn(Math, "random").mockReturnValue(0.5);
		const unsubscribe = subscribeWorkspaceFileChanges("sess-growth", fakeQueryClient());

		// Fail the stream repeatedly and record the gap the retry actually waited.
		const waits: number[] = [];
		for (let failure = 1; failure <= 4; failure += 1) {
			const source = EventSourceStub.instances.at(-1)!;
			source.readyState = 2;
			source.onerror?.();

			const before = EventSourceStub.instances.length;
			const scheduled = computeSseRetryDelayMs(failure, () => 0.5);
			// One tick short of the scheduled delay, nothing has reconnected yet.
			vi.advanceTimersByTime(scheduled - 1);
			expect(EventSourceStub.instances).toHaveLength(before);
			vi.advanceTimersByTime(1);
			expect(EventSourceStub.instances).toHaveLength(before + 1);
			waits.push(scheduled);
		}

		// This is the regression: on the old flat interval every wait was 5s.
		expect(waits).toEqual([...waits].sort((a, b) => a - b));
		expect(new Set(waits).size).toBe(waits.length);
		expect(waits.at(-1)!).toBeGreaterThan(waits[0]);
		unsubscribe();
	});

	it("does not let the browser's own retries inflate the scheduled delay", () => {
		vi.useFakeTimers();
		vi.spyOn(Math, "random").mockReturnValue(0.5);
		const unsubscribe = subscribeWorkspaceFileChanges("sess-native", fakeQueryClient());

		// readyState CONNECTING means the browser is retrying by itself; those
		// errors drive the degraded label but are not rebuilds we scheduled, so
		// they must not advance the backoff exponent.
		const source = EventSourceStub.instances.at(-1)!;
		for (let i = 0; i < 10; i += 1) {
			source.readyState = 0;
			source.onerror?.();
		}
		expect(EventSourceStub.instances).toHaveLength(1);

		// The first failure we actually schedule a retry for must still wait the
		// initial delay, not the 60s ceiling.
		source.readyState = 2;
		source.onerror?.();
		const firstRetryMs = computeSseRetryDelayMs(1, () => 0.5);
		vi.advanceTimersByTime(firstRetryMs - 1);
		expect(EventSourceStub.instances).toHaveLength(1);
		vi.advanceTimersByTime(1);
		expect(EventSourceStub.instances).toHaveLength(2);
		unsubscribe();
	});

	it("drops back to the initial delay once the stream opens again", () => {
		vi.useFakeTimers();
		vi.spyOn(Math, "random").mockReturnValue(0.5);
		const unsubscribe = subscribeWorkspaceFileChanges("sess-reset", fakeQueryClient());

		// Three failures, so the delay has grown well past the initial step.
		for (let failure = 1; failure <= 3; failure += 1) {
			const source = EventSourceStub.instances.at(-1)!;
			source.readyState = 2;
			source.onerror?.();
			vi.advanceTimersByTime(computeSseRetryDelayMs(failure, () => 0.5));
		}

		// A successful open must clear the accumulated failures.
		const recovered = EventSourceStub.instances.at(-1)!;
		recovered.readyState = 1;
		recovered.onopen?.();

		recovered.readyState = 2;
		recovered.onerror?.();
		const before = EventSourceStub.instances.length;
		const firstRetryMs = computeSseRetryDelayMs(1, () => 0.5);
		vi.advanceTimersByTime(firstRetryMs - 1);
		expect(EventSourceStub.instances).toHaveLength(before);
		vi.advanceTimersByTime(1);
		expect(EventSourceStub.instances).toHaveLength(before + 1);
		unsubscribe();
	});

	it("reports degraded after three completed connection failures", () => {
		vi.useFakeTimers();
		vi.spyOn(Math, "random").mockReturnValue(0.5);
		const unsubscribe = subscribeWorkspaceFileChanges("sess-degraded", fakeQueryClient());

		for (let failure = 0; failure < 3; failure += 1) {
			const source = EventSourceStub.instances.at(-1)!;
			source.readyState = 2;
			source.onerror?.();
			// Each retry waits longer than the last, so advance by the delay this
			// failure count actually schedules rather than a fixed 5s.
			if (failure < 2) vi.advanceTimersByTime(computeSseRetryDelayMs(failure + 1, () => 0.5));
		}

		expect(getWorkspaceFileConnectionState("sess-degraded")).toBe("degraded");
		unsubscribe();
	});

	it("degrades after repeated native reconnect failures and recovers on open", () => {
		const unsubscribe = subscribeWorkspaceFileChanges("sess-native-retry", fakeQueryClient());
		const source = EventSourceStub.instances[0];

		source.onopen?.();
		expect(getWorkspaceFileConnectionState("sess-native-retry")).toBe("connected");

		source.readyState = 0;
		for (let failure = 0; failure < 3; failure += 1) {
			source.onerror?.();
			expect(getWorkspaceFileConnectionState("sess-native-retry")).toBe(failure < 2 ? "connecting" : "degraded");
		}

		source.onopen?.();
		expect(getWorkspaceFileConnectionState("sess-native-retry")).toBe("connected");
		unsubscribe();
	});

	it("uses degraded polling when EventSource is unavailable", () => {
		delete (globalThis as unknown as { EventSource?: unknown }).EventSource;

		const unsubscribe = subscribeWorkspaceFileChanges("sess-no-eventsource", fakeQueryClient());

		expect(getWorkspaceFileConnectionState("sess-no-eventsource")).toBe("degraded");
		unsubscribe();
	});
});
