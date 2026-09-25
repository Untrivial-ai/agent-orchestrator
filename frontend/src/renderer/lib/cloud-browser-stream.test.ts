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
	serverClose(code = 1006): void {
		this.closed = true;
		this.readyState = WebSocket.CLOSED;
		this.onclose?.call(this as unknown as WebSocket, new CloseEvent("close", { code }));
	}
	message(data: string | ArrayBuffer): void {
		this.onmessage?.call(this as unknown as WebSocket, new MessageEvent("message", { data }));
	}
}

function frame(epoch: bigint, sequence: bigint, width = 800, height = 600, targetId = "tab-1"): ArrayBuffer {
	const target = new TextEncoder().encode(targetId);
	const jpeg = Uint8Array.from([0xff, 0xd8, 0xff, 0xd9]);
	const bytes = new Uint8Array(35 + target.length + jpeg.length);
	bytes.set(new TextEncoder().encode("AOBR"), 0);
	bytes[4] = 1;
	bytes[5] = 1;
	const view = new DataView(bytes.buffer);
	view.setBigUint64(6, epoch);
	view.setBigUint64(14, sequence);
	view.setUint16(22, width);
	view.setUint16(24, height);
	view.setBigUint64(26, 8n);
	bytes[34] = target.length;
	bytes.set(target, 35);
	bytes.set(jpeg, 35 + target.length);
	return bytes.buffer;
}

function acknowledgeViewport(stream: CloudBrowserStream, socket: FakeSocket, width = 800, height = 600, minFrameSeq = 1): void {
	stream.setViewport(width, height);
	const control = JSON.parse(String(socket.sent.at(-1))) as { inputSeq: number };
	socket.message(JSON.stringify({ type: "viewport_ack", version: 1, streamEpoch: stream.getSnapshot().streamEpoch,
		width, height, inputSeq: control.inputSeq, minFrameSeq }));
}

afterEach(() => {
	vi.useRealTimers();
	vi.restoreAllMocks();
});

describe("CloudBrowserStream", () => {
	it("acknowledges DevTools, fences old frames and inputs, and resets on reconnect", async () => {
		vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:frame");
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		const socket = new FakeSocket();
		const stream = new CloudBrowserStream({ baseUrl: "https://cloud.example", orgId: "org", sessionId: "session",
			client: { createBrowserViewerTicket: vi.fn().mockResolvedValue({ ticket: "ticket", canOperate: true }) }, createSocket: () => socket });
		stream.retain();
		await Promise.resolve(); await Promise.resolve();
		socket.open();
		const state = (extra = {}) => socket.message(JSON.stringify({ type: "state", version: 1, streamEpoch: 1,
			targetId: "tab-1", activeTabId: "tab-1", devtoolsSupported: true, ...extra }));
		state();
		acknowledgeViewport(stream, socket);
		socket.message(frame(1n, 1n));
		stream.reportPaint(1, 0, 0, "blob:frame");
		const opening = stream.request({ type: "devtools", operation: "open" });
		const control = JSON.parse(String(socket.sent.at(-1)));
		expect(control).toMatchObject({ type: "devtools", operation: "open" });
		state({ targetId: "inspector", devtoolsOpen: true });
		socket.message(JSON.stringify({ type: "input_ack", version: 1, streamEpoch: 1, inputSeq: control.inputSeq }));
		await expect(opening).resolves.toBeUndefined();
		expect(stream.getSnapshot()).toMatchObject({ activeTabId: "tab-1", targetId: "inspector", devtoolsOpen: true, viewportPending: true });
		expect(stream.send({ type: "input", kind: "text", text: "too soon" })).toBe(false);
		socket.message(frame(1n, 2n));
		expect(stream.getSnapshot().frameSequence).toBe(1);
		acknowledgeViewport(stream, socket, 800, 600, 3);
		socket.message(frame(1n, 3n, 800, 600, "inspector"));
		stream.reportPaint(3, 0, 0, "blob:frame");
		expect(stream.send({ type: "input", kind: "text", text: "1+1" })).toBe(true);
		expect(JSON.parse(String(socket.sent.at(-1)))).toMatchObject({ targetId: "inspector" });
		const closing = stream.request({ type: "devtools", operation: "close" });
		const rejected = expect(closing).rejects.toThrow("unavailable");
		socket.message(JSON.stringify({ type: "input_rejected", version: 1, streamEpoch: 1,
			inputSeq: JSON.parse(String(socket.sent.at(-1))).inputSeq, message: "DevTools unavailable" }));
		await rejected;
		socket.message(JSON.stringify({ type: "hello", version: 1, streamEpoch: 2 }));
		expect(stream.getSnapshot()).toMatchObject({ devtoolsOpen: false, devtoolsSupported: false, targetId: "", viewportPending: true });
		expect(stream.send({ type: "devtools", operation: "open" })).toBe(false);
		stream.dispose();
	});

	it("does not send DevTools commands from a read-only viewer", async () => {
		const socket = new FakeSocket();
		const stream = new CloudBrowserStream({ baseUrl: "https://cloud.example", orgId: "org", sessionId: "session",
			client: { createBrowserViewerTicket: vi.fn().mockResolvedValue({ ticket: "ticket", canOperate: false }) }, createSocket: () => socket });
		stream.retain();
		await Promise.resolve(); await Promise.resolve();
		socket.open();
		socket.message(JSON.stringify({ type: "state", version: 1, streamEpoch: 1, devtoolsSupported: true, devtoolsOpen: true }));
		expect(stream.send({ type: "devtools", operation: "close" })).toBe(false);
		expect(socket.sent).toHaveLength(0);
		stream.dispose();
	});

	it("does not reclaim a viewer replaced by another window until explicitly retried", async () => {
		vi.useFakeTimers();
		const socket = new FakeSocket();
		const issue = vi.fn().mockResolvedValue({ ticket: "ticket", canOperate: true });
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example", orgId: "org", sessionId: "session",
			client: { createBrowserViewerTicket: issue }, createSocket: () => socket,
		});
		stream.retain();
		await Promise.resolve(); await Promise.resolve();
		socket.open();
		socket.serverClose(4001);
		await vi.advanceTimersByTimeAsync(60_000);
		expect(issue).toHaveBeenCalledOnce();
		expect(stream.getSnapshot()).toMatchObject({ status: "fatal", canOperate: false });
		expect(stream.getSnapshot().error).toContain("another window");
		stream.retryNow();
		await Promise.resolve(); await Promise.resolve();
		expect(issue).toHaveBeenCalledTimes(2);
		stream.dispose();
	});

	it("accepts a painted viewport while newer frames are still arriving", async () => {
		const socket = new FakeSocket();
		let url = 0;
		vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:frame-${++url}`);
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example", orgId: "org", sessionId: "session",
			client: { createBrowserViewerTicket: vi.fn().mockResolvedValue({ ticket: "ticket", canOperate: true }) },
			createSocket: () => socket,
		});
		stream.retain();
		await Promise.resolve(); await Promise.resolve();
		socket.open();
		socket.message(JSON.stringify({ type: "hello", version: 1, streamEpoch: 1 }));
		acknowledgeViewport(stream, socket);
		socket.message(frame(1n, 1n));
		socket.message(frame(1n, 2n));
		stream.reportPaint(1, 80, 1, "blob:frame-1");
		expect(stream.getSnapshot().viewportPending).toBe(false);
		expect(stream.send({ type: "input", kind: "text", text: "working" })).toBe(true);
		socket.message(JSON.stringify({ type: "hello", version: 1, streamEpoch: 2 }));
		acknowledgeViewport(stream, socket);
		socket.message(frame(2n, 2n));
		stream.reportPaint(2, 80, 1, "blob:frame-2");
		expect(stream.getSnapshot().viewportPending).toBe(true);
		stream.reportPaint(2, 80, 1, "blob:frame-3");
		expect(stream.getSnapshot().viewportPending).toBe(false);
		stream.dispose();
	});

	it("keeps the viewer attached when resizing loses control ownership", async () => {
		const socket = new FakeSocket();
		vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:frame");
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example", orgId: "org", sessionId: "session",
			client: { createBrowserViewerTicket: vi.fn().mockResolvedValue({ ticket: "ticket", canOperate: true }) },
			createSocket: () => socket,
		});
		stream.retain();
		await Promise.resolve(); await Promise.resolve();
		socket.open();
		socket.message(JSON.stringify({ type: "hello", version: 1, streamEpoch: 1 }));
		stream.setViewport(800, 600);
		const resize = JSON.parse(String(socket.sent.at(-1)));
		socket.message(JSON.stringify({ ...resize, type: "input_rejected", code: "BROWSER_AGENT_CONTROL_ACTIVE", owner: "agent" }));
		expect(socket.closed).toBe(false);
		expect(stream.getSnapshot()).toMatchObject({ status: "waiting", owner: "agent", viewportPending: true });
		socket.message(JSON.stringify({ type: "control_owner", version: 1, streamEpoch: 1, owner: "idle" }));
		// The surface resubmits its dimensions when control ownership changes.
		acknowledgeViewport(stream, socket);
		socket.message(frame(1n, 1n)); stream.reportPaint(1, 0, 0, stream.getSnapshot().frameUrl);
		expect(stream.getSnapshot().viewportPending).toBe(false);
		stream.dispose();
	});

	it.each(["BROWSER_INPUT_REJECTED", "BROWSER_INPUT_RATE_EXCEEDED"])("makes rejected resize recoverable (%s)", async (code) => {
		vi.useFakeTimers();
		const sockets: FakeSocket[] = [];
		vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:frame");
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example", orgId: "org", sessionId: "session",
			client: { createBrowserViewerTicket: vi.fn().mockResolvedValue({ ticket: "ticket", canOperate: true }) },
			createSocket: () => { const socket = new FakeSocket(); sockets.push(socket); return socket; },
		});
		stream.retain();
		await Promise.resolve(); await Promise.resolve();
		const socket = sockets[0]!;
		socket.open();
		socket.message(JSON.stringify({ type: "hello", version: 1, streamEpoch: 1 }));
		acknowledgeViewport(stream, socket);
		socket.message(frame(1n, 1n)); stream.reportPaint(1, 0, 0, stream.getSnapshot().frameUrl);
		stream.setViewport(1000, 800);
		const resize = JSON.parse(String(socket.sent.at(-1)));
		socket.message(JSON.stringify({ ...resize, type: "input_rejected", code }));
		const failure = stream.getSnapshot().error;
		expect(failure).toContain("Reconnect the browser");
		expect(socket.closed).toBe(true);
		socket.message(frame(1n, 2n, 1000, 800));
		await vi.advanceTimersByTimeAsync(35_000);
		expect(sockets).toHaveLength(1);
		expect(stream.getSnapshot()).toMatchObject({ status: "fatal", error: failure, viewportPending: true });
		expect(stream.send({ type: "input", kind: "text", text: "no" })).toBe(false);
		stream.retryNow();
		await Promise.resolve(); await Promise.resolve();
		const recovered = sockets[1]!;
		recovered.open();
		recovered.message(JSON.stringify({ type: "hello", version: 1, streamEpoch: 2 }));
		acknowledgeViewport(stream, recovered, 1000, 800);
		recovered.message(frame(2n, 1n, 1000, 800));
		expect(stream.send({ type: "input", kind: "text", text: "no" })).toBe(false);
		stream.reportPaint(1, 0, 0, stream.getSnapshot().frameUrl);
		expect(stream.send({ type: "input", kind: "text", text: "yes" })).toBe(true);
		stream.dispose();
	});

	it.each(["control epoch", "frame epoch", "control version", "frame version"])("reports incompatible worker %s without retry loops", async (kind) => {
		vi.useFakeTimers();
		const socket = new FakeSocket();
		const issue = vi.fn().mockResolvedValue({ ticket: "ticket", canOperate: true });
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example", orgId: "org", sessionId: "session",
			client: { createBrowserViewerTicket: issue }, createSocket: () => socket,
		});
		stream.retain();
		await Promise.resolve(); await Promise.resolve();
		socket.open();
		if (kind.startsWith("control")) {
			socket.message(JSON.stringify({ type: "hello", version: kind.endsWith("version") ? 2 : 1,
				streamEpoch: kind.endsWith("epoch") ? 1790200000000000000 : 1 }));
		} else {
			const payload = frame(kind.endsWith("epoch") ? 1790200000000000000n : 1n, 1n);
			if (kind.endsWith("version")) new Uint8Array(payload)[4] = 2;
			socket.message(payload);
		}
		expect(stream.getSnapshot()).toMatchObject({ status: "fatal", canOperate: false, frameSequence: 0 });
		expect(stream.getSnapshot().error).toContain("updated worker image");
		expect(socket.closed).toBe(true);
		await vi.advanceTimersByTimeAsync(35_000);
		expect(issue).toHaveBeenCalledOnce();
		stream.dispose();
	});

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
		vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:viewport");
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		acknowledgeViewport(stream, socket);
		socket.message(frame(1n, 1n));
		stream.reportPaint(1, 0, 0, stream.getSnapshot().frameUrl);

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
		vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:viewport");
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		acknowledgeViewport(stream, socket);
		socket.message(frame(1n, 1n));
		stream.reportPaint(1, 0, 0, stream.getSnapshot().frameUrl);

		const close = stream.request({ type: "tab", operation: "close", tabId: "tab-2" });
		socket.serverClose();
		await expect(close).rejects.toThrow("The browser viewer is reconnecting.");
		stream.dispose();
	});

	it("gates pointer input until the latest acknowledged viewport is painted", async () => {
		const socket = new FakeSocket();
		vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:frame");
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		const stream = new CloudBrowserStream({
			baseUrl: "https://cloud.example", orgId: "org", sessionId: "session",
			client: { createBrowserViewerTicket: vi.fn().mockResolvedValue({ ticket: "ticket", canOperate: true }) },
			createSocket: () => socket,
		});
		stream.retain();
		await Promise.resolve(); await Promise.resolve();
		socket.open();
		socket.message(JSON.stringify({ type: "hello", version: 1, streamEpoch: 1 }));
		acknowledgeViewport(stream, socket);
		socket.message(frame(1n, 1n)); stream.reportPaint(1, 0, 0, stream.getSnapshot().frameUrl);
		expect(stream.getSnapshot().viewportPending).toBe(false);
		acknowledgeViewport(stream, socket, 1000, 800, 2);
		expect(stream.send({ type: "input", kind: "pointerDown", x: 10, y: 10 })).toBe(false);
		socket.message(frame(1n, 2n)); stream.reportPaint(2, 0, 0, stream.getSnapshot().frameUrl);
		expect(stream.getSnapshot().viewportPending).toBe(true);
		socket.message(frame(1n, 3n, 1000, 800));
		expect(stream.getSnapshot().viewportPending).toBe(true);
		stream.reportPaint(3, 0, 0, stream.getSnapshot().frameUrl);
		expect(stream.getSnapshot().viewportPending).toBe(false);
		stream.setViewport(800, 600);
		const oldResize = JSON.parse(String(socket.sent.at(-1)));
		acknowledgeViewport(stream, socket, 1000, 800, 4);
		socket.message(JSON.stringify({ ...oldResize, type: "viewport_ack", minFrameSeq: 1 }));
		stream.reportPaint(3, 0, 0, stream.getSnapshot().frameUrl);
		expect(stream.getSnapshot().viewportPending).toBe(true);
		socket.message(frame(1n, 4n, 1000, 800)); stream.reportPaint(4, 0, 0, stream.getSnapshot().frameUrl);
		expect(stream.getSnapshot().viewportPending).toBe(false);
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
		acknowledgeViewport(stream, sockets[0]!);
		now = 100;
		sockets[0]!.message(frame(1n, 1n));
		now = 110;
		stream.reportPaint(1, 7.6, 1.6, stream.getSnapshot().frameUrl);
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
			type: "input_ack", version: 1, streamEpoch: 1, inputSeq: 2, minFrameSeq: 2,
		}));
		expect(capture).toHaveBeenCalledWith("ao.renderer.cloud_browser_input_ack", {
			elapsed_ms: 25,
			input_kind: "input",
		});
		now = 260;
		sockets[0]!.message(frame(1n, 2n));
		now = 270;
		stream.reportPaint(2, 5, 1, stream.getSnapshot().frameUrl);
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
		stream.reportPaint(1, 4, 1, stream.getSnapshot().frameUrl);
		expect(capture).toHaveBeenCalledWith("ao.renderer.cloud_browser_first_frame", expect.objectContaining({
			elapsed_ms: 110,
			reconnect: true,
		}));
		expect(issue).toHaveBeenCalledTimes(2);
		stream.dispose();
	});
});
