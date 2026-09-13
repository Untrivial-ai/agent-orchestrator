import { describe, expect, it } from "vitest";
import { createCloudTerminalMux } from "./cloud-terminal-mux";

// Minimal fake WebSocket: records its URL, lets the test deliver frames, and
// reports OPEN so sendJSON works.
class FakeWebSocket {
	static OPEN = 1;
	readyState = FakeWebSocket.OPEN;
	url: string;
	private listeners = new Map<string, Set<(event: unknown) => void>>();
	constructor(url: string) {
		this.url = url;
		FakeWebSocket.instances.push(this);
	}
	static instances: FakeWebSocket[] = [];
	addEventListener(type: string, fn: (event: unknown) => void) {
		const set = this.listeners.get(type) ?? new Set();
		set.add(fn);
		this.listeners.set(type, set);
	}
	send() {}
	close() {}
	emit(type: string, event: unknown) {
		this.listeners.get(type)?.forEach((fn) => fn(event));
	}
	deliver(message: object) {
		this.emit("message", { data: JSON.stringify(message) });
	}
}

const b64 = (s: string) => Buffer.from(s).toString("base64");
const afterOf = (ws: FakeWebSocket) => new URL(ws.url.replace(/^ws/, "http")).searchParams.get("after");

function makeMux(cursor?: { value: number }) {
	return createCloudTerminalMux({
		wsBaseUrl: "wss://cp.example.com/api/cloud/v1",
		kind: "agent",
		mintTicket: async () => "ticket-1",
		cursor,
		WebSocketImpl: FakeWebSocket as unknown as typeof WebSocket,
	});
}

describe("createCloudTerminalMux cursor resume", () => {
	it("advances a shared cursor on output and resumes a rebuilt mux from it", async () => {
		FakeWebSocket.instances = [];
		const cursor = { value: 0 };
		const mux1 = makeMux(cursor);
		await Promise.resolve();
		await Promise.resolve();
		const ws1 = FakeWebSocket.instances[0];
		expect(afterOf(ws1)).toBe("0"); // first connect starts at 0
		ws1.deliver({ type: "output", sequence: 5, data: b64("hi") });
		expect(cursor.value).toBe(5);
		mux1.dispose();

		// A fresh mux with the same cursor object must resume from 5, not 0.
		const mux2 = makeMux(cursor);
		await Promise.resolve();
		await Promise.resolve();
		const ws2 = FakeWebSocket.instances[1];
		expect(afterOf(ws2)).toBe("5");
		mux2.dispose();
	});

	it("on a reset frame drops the cursor to 0 and clears the pane", async () => {
		FakeWebSocket.instances = [];
		const cursor = { value: 42 };
		const mux = makeMux(cursor);
		await Promise.resolve();
		await Promise.resolve();
		const ws = FakeWebSocket.instances[0];
		const chunks: string[] = [];
		mux.onData("agent", (bytes) => chunks.push(new TextDecoder().decode(bytes)));
		ws.deliver({ type: "reset" });
		expect(cursor.value).toBe(0);
		// A clear-screen + scrollback-wipe sequence is emitted to the pane.
		expect(chunks.join("")).toContain("\x1b[2J");
		mux.dispose();
	});
});
