import { afterEach, describe, expect, it, vi } from "vitest";
import type { ServerConfig } from "../config";
import { MobileBrowserRuntimeClient } from "./mobileBrowserRuntime";
import { mobileBrowserRuntimeURL } from "./mobileBrowserRuntimeUrl";

vi.mock("../config", () => ({ authHeaders: () => ({ Authorization: "Bearer test" }) }));

const config: ServerConfig = {
	host: "https://192.168.1.20/",
	httpPort: "3011",
	muxPort: "3011",
	secure: false,
	password: "secret",
};

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

describe("mobileBrowserRuntimeURL", () => {
	it("targets the paired daemon and escapes target identity", () => {
		expect(mobileBrowserRuntimeURL(config, "session/a", "phone one")).toBe(
			"ws://192.168.1.20:3011/mobile-browser-runtime?sessionId=session%2Fa&deviceId=phone%20one",
		);
	});

	it("uses wss for secure endpoints", () => {
		expect(mobileBrowserRuntimeURL({ ...config, secure: true }, "s1", "d1")).toMatch(/^wss:/);
	});
});

describe("MobileBrowserRuntimeClient socket lifecycle", () => {
	it("ignores a delayed close from a socket replaced after stop and restart", async () => {
		vi.useFakeTimers();
		class FakeWebSocket {
			static instances: FakeWebSocket[] = [];
			static OPEN = 1;
			readyState = FakeWebSocket.OPEN;
			onopen: (() => void) | null = null;
			onmessage: ((event: { data: string }) => void) | null = null;
			onerror: (() => void) | null = null;
			onclose: (() => void) | null = null;
			constructor() { FakeWebSocket.instances.push(this); }
			close() { this.readyState = 3; }
			send() {}
		}
		vi.stubGlobal("WebSocket", FakeWebSocket);
		const statuses: boolean[] = [];
		const client = new MobileBrowserRuntimeClient(config, "s1", "phone", {
			execute: async () => ({ ok: true, result: {} }),
			onStatus: (connected) => statuses.push(connected),
		});

		client.start();
		const first = FakeWebSocket.instances[0];
		client.stop();
		client.start();
		const second = FakeWebSocket.instances[1];
		second.onopen?.();
		const statusCount = statuses.length;

		first.onclose?.();
		await vi.advanceTimersByTimeAsync(1_000);

		expect(FakeWebSocket.instances).toHaveLength(2);
		expect(statuses).toHaveLength(statusCount);
		client.stop();
	});
});
