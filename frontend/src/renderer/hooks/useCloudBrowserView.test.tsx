import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetCloudBrowserStreamsForTest, useCloudBrowserView } from "./useCloudBrowserView";

const cloudCp = vi.hoisted(() => ({
	baseUrl: "https://cloud.example",
	client: {
		createBrowserViewerTicket: vi.fn(),
	},
	ready: true,
}));

vi.mock("./useCloudCp", () => ({
	useCloudCp: () => cloudCp,
}));

class FakeWebSocket {
	static readonly CONNECTING = 0;
	static readonly OPEN = 1;
	static readonly CLOSING = 2;
	static readonly CLOSED = 3;

	binaryType: BinaryType = "blob";
	readyState = FakeWebSocket.CONNECTING;
	onopen: ((event: Event) => unknown) | null = null;
	onmessage: ((event: MessageEvent) => unknown) | null = null;
	onerror: ((event: Event) => unknown) | null = null;
	onclose: ((event: CloseEvent) => unknown) | null = null;
	readonly sent: string[] = [];

	constructor(readonly url: string) {
		sockets.push(this);
	}

	send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
		this.sent.push(String(data));
	}

	close(): void {
		this.readyState = FakeWebSocket.CLOSED;
	}

	open(): void {
		this.readyState = FakeWebSocket.OPEN;
		this.onopen?.(new Event("open"));
	}

	message(control: Record<string, unknown>): void {
		this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(control) }));
	}
}

const sockets: FakeWebSocket[] = [];

beforeEach(() => {
	sockets.length = 0;
	cloudCp.client.createBrowserViewerTicket.mockReset();
	cloudCp.client.createBrowserViewerTicket.mockResolvedValue({
		ticket: "ticket",
		expiresIn: 300,
		protocolVersion: 1,
		canOperate: true,
	});
	vi.stubGlobal("WebSocket", FakeWebSocket);
});

afterEach(() => {
	act(() => resetCloudBrowserStreamsForTest());
	vi.unstubAllGlobals();
});

describe("useCloudBrowserView", () => {
	it("waits for tab acknowledgements and surfaces a rejected control", async () => {
		const { result } = renderHook(() => useCloudBrowserView({
			orgId: "org",
			sessionId: "session",
			active: true,
		}));
		await waitFor(() => expect(sockets).toHaveLength(1));
		const socket = sockets[0]!;
		act(() => {
			socket.open();
			socket.message({ type: "state", version: 1, streamEpoch: 1 });
		});
		vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:frame");
		vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
		act(() => {
			result.current.cloudSurface!.setViewport(800, 600);
			const viewport = JSON.parse(socket.sent.at(-1)!);
			socket.message({ ...viewport, type: "viewport_ack", minFrameSeq: 1 });
			const bytes = new Uint8Array(40);
			bytes.set(new TextEncoder().encode("AOBR")); bytes[4] = 1; bytes[5] = 1; bytes[34] = 1; bytes[35] = 116;
			const header = new DataView(bytes.buffer);
			header.setBigUint64(6, 1n); header.setBigUint64(14, 1n); header.setUint16(22, 800); header.setUint16(24, 600);
			socket.onmessage?.(new MessageEvent("message", { data: bytes.buffer }));
			result.current.cloudSurface!.reportPaint(1, 0, 0);
		});

		let close!: Promise<void>;
		act(() => {
			close = result.current.closeTab("tab-2");
		});
		const closeControl = JSON.parse(socket.sent.at(-1)!) as { inputSeq: number; operation: string; tabId: string };
		expect(closeControl).toMatchObject({ operation: "close", tabId: "tab-2" });
		await act(async () => {
			socket.message({
				type: "input_ack",
				version: 1,
				streamEpoch: 1,
				inputSeq: closeControl.inputSeq,
			});
			await close;
		});
		expect(result.current.tabNotice).toBe("");

		let select!: Promise<void>;
		act(() => {
			select = result.current.selectTab("tab-1");
		});
		const selectControl = JSON.parse(socket.sent.at(-1)!) as { inputSeq: number };
		await act(async () => {
			socket.message({
				type: "input_rejected",
				version: 1,
				streamEpoch: 1,
				inputSeq: selectControl.inputSeq,
				code: "BROWSER_AGENT_CONTROL_ACTIVE",
				owner: "agent",
			});
			await select;
		});
		expect(result.current.tabNotice).toBe(
			"The session agent is controlling the browser. Try again in a moment.",
		);
	});
});
