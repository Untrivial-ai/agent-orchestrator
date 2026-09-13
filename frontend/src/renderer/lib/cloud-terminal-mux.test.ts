import { describe, expect, it } from "vitest";
import { createCloudTerminalMux } from "./cloud-terminal-mux";

// Minimal fake WebSocket: records its URL and every frame it sends, lets the
// test deliver frames, and reports OPEN so sendJSON works.
class FakeWebSocket {
	static OPEN = 1;
	readyState = FakeWebSocket.OPEN;
	url: string;
	sent: string[] = [];
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
	send(data: string) {
		this.sent.push(data);
	}
	close() {}
	emit(type: string, event: unknown) {
		this.listeners.get(type)?.forEach((fn) => fn(event));
	}
	deliver(message: object) {
		this.emit("message", { data: JSON.stringify(message) });
	}
}

const b64 = (s: string) => Buffer.from(s).toString("base64");
const paramOf = (ws: FakeWebSocket, key: string) =>
	new URL(ws.url.replace(/^ws/, "http")).searchParams.get(key);
const afterOf = (ws: FakeWebSocket) => paramOf(ws, "after");
const sentJSON = (ws: FakeWebSocket) => ws.sent.map((frame) => JSON.parse(frame));

function makeMux(cursor?: { value: number }) {
	return createCloudTerminalMux({
		wsBaseUrl: "wss://cp.example.com/api/cloud/v1",
		kind: "agent",
		mintTicket: async () => "ticket-1",
		cursor,
		WebSocketImpl: FakeWebSocket as unknown as typeof WebSocket,
	});
}

// Let the async connect() mint its ticket and dial the socket.
async function settle() {
	await Promise.resolve();
	await Promise.resolve();
}

describe("createCloudTerminalMux direct open", () => {
	it("opens its socket directly at construction with no agent-ready wait", async () => {
		FakeWebSocket.instances = [];
		const mux = makeMux();
		// The mux dials as soon as the ticket mint resolves — there is no SSE to
		// wait on. Nothing is created synchronously (the mint is awaited).
		expect(FakeWebSocket.instances).toHaveLength(0);
		await settle();
		expect(FakeWebSocket.instances).toHaveLength(1);
		const ws = FakeWebSocket.instances[0];
		expect(paramOf(ws, "kind")).toBe("agent");
		expect(afterOf(ws)).toBe("0");
		mux.dispose();
	});

	it("stays unopened on `starting` and opens the connection on `ready`", async () => {
		FakeWebSocket.instances = [];
		const mux = makeMux();
		await settle();
		const ws = FakeWebSocket.instances[0];
		let opened = 0;
		mux.onOpened("agent", () => {
			opened += 1;
		});
		// `starting` is the CP holding the pane while the terminal comes up: it
		// carries no readiness, so the hook stays in its "connecting" state.
		ws.deliver({ type: "starting", sequence: 0 });
		expect(opened).toBe(0);
		// `ready` is the server ack — the hook transitions to "attached".
		ws.deliver({ type: "ready", sequence: 0 });
		expect(opened).toBe(1);
		mux.dispose();
	});

	it("does not claim a size for a hidden 0×0 open, and sends the real size when visible", async () => {
		FakeWebSocket.instances = [];
		const mux = makeMux();
		await settle();
		const ws = FakeWebSocket.instances[0];
		ws.sent.length = 0;
		// A hidden/parked pane attaches at 0×0. The CP rejects a 0-dimension resize
		// and closes the socket, so the mux must send nothing.
		mux.open("agent", 0, 0);
		expect(ws.sent).toHaveLength(0);
		// Becoming visible (open for a visible pane, or resize on activation) sends
		// the real, non-zero grid so the PTY resizes and the TUI redraws at width.
		mux.resize("agent", 100, 30);
		expect(sentJSON(ws)).toEqual([{ type: "resize", columns: 100, rows: 30 }]);
		mux.dispose();
	});
});

describe("createCloudTerminalMux cursor resume", () => {
	it("advances a shared cursor on output and resumes a rebuilt mux from it", async () => {
		FakeWebSocket.instances = [];
		const cursor = { value: 0 };
		const mux1 = makeMux(cursor);
		await settle();
		const ws1 = FakeWebSocket.instances[0];
		expect(afterOf(ws1)).toBe("0"); // first connect starts at 0
		ws1.deliver({ type: "output", sequence: 5, data: b64("hi") });
		expect(cursor.value).toBe(5);
		mux1.dispose();

		// A fresh mux with the same cursor object sends after=5 (the CP decides
		// whether to honor it or reset to 0; the plumbing preserves the position).
		const mux2 = makeMux(cursor);
		await settle();
		const ws2 = FakeWebSocket.instances[1];
		expect(afterOf(ws2)).toBe("5");
		mux2.dispose();
	});

	it("on a reset frame drops the cursor to 0 and clears the pane", async () => {
		FakeWebSocket.instances = [];
		const cursor = { value: 42 };
		const mux = makeMux(cursor);
		await settle();
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
