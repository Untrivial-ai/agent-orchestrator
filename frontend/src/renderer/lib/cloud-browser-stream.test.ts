import { afterEach, describe, expect, it, vi } from "vitest";
import { CloudBrowserStream, cloudBrowserViewerUrl } from "./cloud-browser-stream";

class FakeSocket {
	binaryType: BinaryType = "blob";
	readyState: number = WebSocket.CONNECTING;
	onopen: ((this: WebSocket, ev: Event) => unknown) | null = null;
	onmessage: ((this: WebSocket, ev: MessageEvent) => unknown) | null = null;
	onerror: ((this: WebSocket, ev: Event) => unknown) | null = null;
	onclose: ((this: WebSocket, ev: CloseEvent) => unknown) | null = null;
	sent: Array<string | ArrayBufferLike | Blob | ArrayBufferView> = [];
	closed = false;

	send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void { this.sent.push(data); }
	close(): void { this.closed = true; this.readyState = WebSocket.CLOSED; }
	open(): void { this.readyState = WebSocket.OPEN; this.onopen?.call(this as unknown as WebSocket, new Event("open")); }
	serverClose(): void {
		this.closed = true;
		this.readyState = WebSocket.CLOSED;
		this.onclose?.call(this as unknown as WebSocket, new CloseEvent("close"));
	}
	message(data: string | ArrayBuffer): void {
		this.onmessage?.call(this as unknown as WebSocket, new MessageEvent("message", { data }));
	}
}

function frame(epoch: bigint, sequence: bigint): ArrayBuffer {
	const target = new TextEncoder().encode("tab-1");
	const jpeg = Uint8Array.from([0xff, 0xd8, 0xff, 0xd9]);
	const bytes = new Uint8Array(35 + target.length + jpeg.length);
	bytes.set(new TextEncoder().encode("AOBR"), 0);
	bytes[4] = 1;
	bytes[5] = 1;
	const view = new DataView(bytes.buffer);
	view.setBigUint64(6, epoch);
	view.setBigUint64(14, sequence);
	view.setUint16(22, 800);
	view.setUint16(24, 600);
	view.setBigUint64(26, 8n);
	bytes[34] = target.length;
	bytes.set(target, 35);
	bytes.set(jpeg, 35 + target.length);
	return bytes.buffer;
}

afterEach(() => {
	vi.useRealTimers();
	vi.restoreAllMocks();
});

describe("CloudBrowserStream", () => {
	it("builds a ticket-only WebSocket URL", () => {
		const url = new URL(cloudBrowserViewerUrl("https://cloud.example/", "org 1", "session/1", "secret"));
		expect(url.protocol).toBe("wss:");
		expect(url.pathname).toBe("/api/cloud/v1/orgs/org%201/sessions/session%2F1/browser-view/stream");
		expect(url.searchParams.get("ticket")).toBe("secret");
	});

	it("reuses one socket, applies ordered state, and paints only newer frames", async () => {
		const socket = new FakeSocket();
		const issue = vi.fn().mockResolvedValue({ ticket: "ticket", expiresIn: 300, protocolVersion: 1, canOperate: true });
		const onIdle = vi.fn();
		vi.spyOn(URL, "createObjectURL").mockReturnValueOnce("blob:first").mockReturnValueOnce("blob:second");
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example", orgId: "org", sessionId: "session",
			client: { createBrowserViewerTicket: issue }, createSocket: () => socket, onIdle,
		});
		stream.retain();
		stream.retain();
		await Promise.resolve();
		await Promise.resolve();
		expect(issue).toHaveBeenCalledOnce();
		socket.open();
		socket.message(JSON.stringify({
			type: "state", version: 1, streamEpoch: 4, url: "https://example.test/",
			title: "Example", activeTabId: "tab-1", owner: "agent",
			dialogOpen: true, dialogType: "prompt", dialogText: "Name?", dialogPrompt: "Ada",
			tabs: [{ id: "tab-1", url: "https://example.test/", title: "Example", active: true }],
		}));
		const invalidTarget = frame(4n, 1n);
		new Uint8Array(invalidTarget)[35] = 0xff;
		socket.message(invalidTarget);
		socket.message(frame(4n, 2n));
		socket.message(frame(4n, 1n));
		expect(stream.getSnapshot()).toMatchObject({
			status: "ready", streamEpoch: 4, frameSequence: 2, frameUrl: "blob:first",
			url: "https://example.test/", owner: "agent", canOperate: true,
			dialogOpen: true, dialogType: "prompt", dialogText: "Name?", dialogPrompt: "Ada",
		});
		expect(URL.revokeObjectURL).not.toHaveBeenCalled();

		socket.message(JSON.stringify({ type: "state", version: 1, streamEpoch: 5, activeTabId: "tab-1" }));
		socket.message(frame(4n, 3n));
		expect(stream.getSnapshot().streamEpoch).toBe(5);
		expect(stream.getSnapshot().frameUrl).toBe("blob:first");
		socket.message(frame(5n, 1n));
		expect(stream.getSnapshot()).toMatchObject({ streamEpoch: 5, frameSequence: 1, frameUrl: "blob:second" });
		expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:first");

		stream.release();
		expect(socket.closed).toBe(false);
		vi.useFakeTimers();
		stream.release();
		vi.advanceTimersByTime(499);
		expect(socket.closed).toBe(false);
		vi.advanceTimersByTime(1);
		expect(socket.closed).toBe(true);
		expect(URL.revokeObjectURL).toHaveBeenLastCalledWith("blob:second");
		expect(stream.getSnapshot().frameUrl).toBe("");
		expect(onIdle).toHaveBeenCalledOnce();
	});

	it("stops automatic retries for fatal ticket errors and supports an explicit retry", async () => {
		const sockets: FakeSocket[] = [];
		const denied = Object.assign(new Error("Browser viewer unavailable"), {
			code: "not_found",
			requestId: "request-1",
		});
		const issue = vi.fn()
			.mockRejectedValueOnce(denied)
			.mockResolvedValueOnce({ ticket: "ticket", expiresIn: 300, protocolVersion: 1, canOperate: true });
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example",
			orgId: "org",
			sessionId: "session",
			client: { createBrowserViewerTicket: issue },
			createSocket: () => {
				const socket = new FakeSocket();
				sockets.push(socket);
				return socket;
			},
		});

		stream.retain();
		await Promise.resolve();
		await Promise.resolve();
		expect(stream.getSnapshot()).toMatchObject({
			status: "fatal",
			error: "Browser viewer unavailable",
			errorRequestId: "request-1",
		});
		expect(issue).toHaveBeenCalledOnce();

		stream.retryNow();
		await Promise.resolve();
		await Promise.resolve();
		expect(issue).toHaveBeenCalledTimes(2);
		expect(sockets).toHaveLength(1);
		expect(stream.getSnapshot()).toMatchObject({ status: "connecting", error: "", errorRequestId: "" });
		stream.dispose();
	});

	it("acknowledges tab commands and returns actionable rejection errors", async () => {
		const socket = new FakeSocket();
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example",
			orgId: "org",
			sessionId: "session",
			client: {
				createBrowserViewerTicket: vi.fn().mockResolvedValue({
					ticket: "ticket", expiresIn: 300, protocolVersion: 1, canOperate: true,
				}),
			},
			createSocket: () => socket,
		});
		stream.retain();
		await Promise.resolve();
		await Promise.resolve();
		socket.open();
		socket.message(JSON.stringify({ type: "state", version: 1, streamEpoch: 1 }));
		socket.message(JSON.stringify({ type: "viewport_ack", version: 1, streamEpoch: 1 }));

		const close = stream.request({ type: "tab", operation: "close", tabId: "tab-2" });
		const closeControl = JSON.parse(String(socket.sent.at(-1))) as { inputSeq: number; operation: string; tabId: string };
		expect(closeControl).toMatchObject({ operation: "close", tabId: "tab-2" });
		socket.message(JSON.stringify({
			type: "input_ack", version: 1, streamEpoch: 1, inputSeq: closeControl.inputSeq,
		}));
		await expect(close).resolves.toBeUndefined();

		const select = stream.request({ type: "tab", operation: "select", tabId: "tab-1" });
		const selectControl = JSON.parse(String(socket.sent.at(-1))) as { inputSeq: number };
		socket.message(JSON.stringify({
			type: "input_rejected", version: 1, streamEpoch: 1,
			inputSeq: selectControl.inputSeq, code: "BROWSER_AGENT_CONTROL_ACTIVE", owner: "agent",
		}));
		await expect(select).rejects.toThrow("The session agent is controlling the browser. Try again in a moment.");
		expect(stream.getSnapshot()).toMatchObject({
			owner: "agent",
			error: "The session agent is controlling the browser. Try again in a moment.",
		});
		stream.dispose();
	});

	it("rejects an acknowledged command immediately when the stream disconnects", async () => {
		const socket = new FakeSocket();
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example",
			orgId: "org",
			sessionId: "session",
			client: {
				createBrowserViewerTicket: vi.fn().mockResolvedValue({
					ticket: "ticket", expiresIn: 300, protocolVersion: 1, canOperate: true,
				}),
			},
			createSocket: () => socket,
		});
		stream.retain();
		await Promise.resolve();
		await Promise.resolve();
		socket.open();
		socket.message(JSON.stringify({ type: "state", version: 1, streamEpoch: 1 }));
		socket.message(JSON.stringify({ type: "viewport_ack", version: 1, streamEpoch: 1 }));

		const close = stream.request({ type: "tab", operation: "close", tabId: "tab-2" });
		socket.serverClose();
		await expect(close).rejects.toThrow("The browser viewer is reconnecting.");
		stream.dispose();
	});

	it("reconnects an open viewer that never delivers its first frame", async () => {
		vi.useFakeTimers();
		const sockets: FakeSocket[] = [];
		const issue = vi.fn().mockResolvedValue({
			ticket: "ticket",
			expiresIn: 300,
			protocolVersion: 1,
			canOperate: true,
		});
		vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:recovered");
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example",
			orgId: "org",
			sessionId: "session",
			client: { createBrowserViewerTicket: issue },
			createSocket: () => {
				const socket = new FakeSocket();
				sockets.push(socket);
				return socket;
			},
		});

		stream.retain();
		await Promise.resolve();
		await Promise.resolve();
		sockets[0]!.open();
		await vi.advanceTimersByTimeAsync(30_000);
		expect(sockets[0]!.closed).toBe(true);
		expect(stream.getSnapshot()).toMatchObject({
			status: "connecting",
			error: "The browser stream did not deliver a frame.",
		});

		await vi.advanceTimersByTimeAsync(250);
		await Promise.resolve();
		expect(sockets).toHaveLength(2);
		sockets[1]!.open();
		sockets[1]!.message(frame(2n, 1n));
		await vi.advanceTimersByTimeAsync(30_000);
		expect(sockets[1]!.closed).toBe(false);
		expect(stream.getSnapshot()).toMatchObject({ status: "ready", frameUrl: "blob:recovered" });
		stream.dispose();
	});

	it("measures bounded viewer latency, reconnects, and never replays input", async () => {
		vi.useFakeTimers();
		let now = 0;
		const capture = vi.fn();
		const sockets: FakeSocket[] = [];
		const issue = vi.fn().mockResolvedValue({ ticket: "ticket", expiresIn: 300, protocolVersion: 1, canOperate: true });
		vi.spyOn(URL, "createObjectURL").mockReturnValueOnce("blob:first").mockReturnValueOnce("blob:second");
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example",
			orgId: "org",
			sessionId: "session",
			client: { createBrowserViewerTicket: issue },
			createSocket: () => {
				const socket = new FakeSocket();
				sockets.push(socket);
				return socket;
			},
			now: () => now,
			capture,
		});

		stream.retain();
		await Promise.resolve();
		await Promise.resolve();
		expect(sockets).toHaveLength(1);
		sockets[0]!.open();
		sockets[0]!.message(JSON.stringify({ type: "state", version: 1, streamEpoch: 1 }));
		sockets[0]!.message(JSON.stringify({ type: "viewport_ack", version: 1, streamEpoch: 1 }));
		now = 100;
		sockets[0]!.message(frame(1n, 1n));
		now = 110;
		stream.reportPaint(1, 7.6, 1.6);
		expect(capture).toHaveBeenCalledWith("ao.renderer.cloud_browser_first_frame", {
			elapsed_ms: 110,
			relay_to_paint_ms: 10,
			decode_ms: 8,
			paint_ms: 2,
			frame_bytes: 4,
			width: 800,
			height: 600,
			reconnect: false,
		});

		now = 200;
		expect(stream.send({ type: "input", kind: "text", text: "value" })).toBe(true);
		now = 225;
		sockets[0]!.message(JSON.stringify({
			type: "input_ack", version: 1, streamEpoch: 1, inputSeq: 1, minFrameSeq: 2,
		}));
		expect(capture).toHaveBeenCalledWith("ao.renderer.cloud_browser_input_ack", {
			elapsed_ms: 25,
			input_kind: "input",
		});
		now = 260;
		sockets[0]!.message(frame(1n, 2n));
		now = 270;
		stream.reportPaint(2, 5, 1);
		expect(capture).toHaveBeenCalledWith("ao.renderer.cloud_browser_input_frame", {
			elapsed_ms: 70,
			input_kind: "input",
		});

		now = 300;
		sockets[0]!.serverClose();
		await vi.advanceTimersByTimeAsync(250);
		await Promise.resolve();
		expect(sockets).toHaveLength(2);
		expect(sockets[1]!.sent).toEqual([]);
		sockets[1]!.open();
		sockets[1]!.message(JSON.stringify({ type: "state", version: 1, streamEpoch: 2 }));
		now = 400;
		sockets[1]!.message(frame(2n, 1n));
		now = 410;
		stream.reportPaint(1, 4, 1);
		expect(capture).toHaveBeenCalledWith("ao.renderer.cloud_browser_first_frame", expect.objectContaining({
			elapsed_ms: 110,
			reconnect: true,
		}));
		expect(issue).toHaveBeenCalledTimes(2);
		stream.dispose();
	});
});
