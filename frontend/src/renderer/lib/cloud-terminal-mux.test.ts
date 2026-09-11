import { afterEach, describe, expect, it, vi } from "vitest";
import { createCloudTerminalMux } from "./cloud-terminal-mux";

class FakeCloudSocket {
	static OPEN = 1;
	static instances: FakeCloudSocket[] = [];
	readyState = 0;
	sent: string[] = [];
	private listeners: Record<string, Array<(event: unknown) => void>> = {};

	constructor(public url: string) {
		FakeCloudSocket.instances.push(this);
	}

	addEventListener(type: string, listener: (event: unknown) => void) {
		(this.listeners[type] ??= []).push(listener);
	}

	send(message: string) {
		this.sent.push(message);
	}

	close() {}

	emitClose(code: number, reason = "") {
		this.listeners.close?.forEach((listener) => listener({ code, reason }));
	}
}

describe("createCloudTerminalMux", () => {
	afterEach(() => {
		FakeCloudSocket.instances = [];
		vi.restoreAllMocks();
	});

	it("surfaces a policy close instead of retrying it as a transport drop", async () => {
		const mux = createCloudTerminalMux({
			wsBaseUrl: "wss://cloud.example.test/api/cloud/v1",
			kind: "agent",
			mintTicket: async () => "ticket",
			WebSocketImpl: FakeCloudSocket as unknown as typeof WebSocket,
		});
		await vi.waitFor(() => expect(FakeCloudSocket.instances).toHaveLength(1));
		const errors: string[] = [];
		const states: string[] = [];
		mux.onError("session", (message) => errors.push(message));
		mux.onConnectionChange((state) => states.push(state));

		FakeCloudSocket.instances[0].emitClose(1008, "terminal process unavailable");

		expect(errors).toEqual(["terminal process unavailable"]);
		expect(states).toEqual(["closed"]);
	});

	it("keeps a transient service-restart close reconnectable", async () => {
		const mux = createCloudTerminalMux({
			wsBaseUrl: "wss://cloud.example.test/api/cloud/v1",
			kind: "workspace",
			mintTicket: async () => "ticket",
			WebSocketImpl: FakeCloudSocket as unknown as typeof WebSocket,
		});
		await vi.waitFor(() => expect(FakeCloudSocket.instances).toHaveLength(1));
		const errors: string[] = [];
		const exits: boolean[] = [];
		const states: string[] = [];
		mux.onError("session", (message) => errors.push(message));
		mux.onExit("session", () => exits.push(true));
		mux.onConnectionChange((state) => states.push(state));

		FakeCloudSocket.instances[0].emitClose(1013, "workspace terminal is restarting");

		expect(errors).toEqual([]);
		expect(exits).toEqual([]);
		expect(states).toEqual(["closed"]);
	});
});
