import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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

import { subscribeWorkspaceFileChanges } from "./workspace-file-events";

class EventSourceStub {
	static instances: EventSourceStub[] = [];
	url: string;
	closed = false;
	readyState = 0;
	onopen: (() => void) | null = null;
	onerror: (() => void) | null = null;
	listeners = new Map<string, Set<() => void>>();

	constructor(url: string) {
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
	getApiBaseUrlMock.mockReset().mockReturnValue("http://127.0.0.1:3001");
	hasTrustedApiBaseUrlMock.mockReset().mockReturnValue(true);
	subscribeApiBaseUrlMock.mockReset().mockReturnValue(unsubscribeBaseUrlMock);
	unsubscribeBaseUrlMock.mockReset();
	(globalThis as unknown as { EventSource: unknown }).EventSource = EventSourceStub;
});

afterEach(() => {
	vi.useRealTimers();
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
});
