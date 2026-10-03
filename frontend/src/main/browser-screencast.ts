import type { BrowserLiveFrame } from "./browser-live-types";

type DebuggerLike = {
	on(event: "message", listener: (event: unknown, method: string, params: Record<string, unknown>) => void): unknown;
	off?(event: "message", listener: (event: unknown, method: string, params: Record<string, unknown>) => void): unknown;
	removeListener?(event: "message", listener: (event: unknown, method: string, params: Record<string, unknown>) => void): unknown;
	sendCommand(method: string, params?: Record<string, unknown>): Promise<unknown>;
};

export class BrowserScreencast {
	private sequence = 0n;
	private stopped = false;
	private readonly listener: (event: unknown, method: string, params: Record<string, unknown>) => void;

	constructor(
		private readonly debug: DebuggerLike,
		private readonly emit: (frame: BrowserLiveFrame) => void,
		private readonly fail: (code: string, message: string) => void,
	) {
		this.listener = (_event, method, params) => {
			if (method !== "Page.screencastFrame" || this.stopped) return;
			void this.consume(params);
		};
	}

	async start(): Promise<void> {
		this.debug.on("message", this.listener);
		try {
			await this.debug.sendCommand("Page.enable");
			await this.debug.sendCommand("Page.startScreencast", {
				format: "jpeg",
				quality: 70,
				maxWidth: 1280,
				maxHeight: 720,
				everyNthFrame: 1,
			});
		} catch (error) {
			this.removeListener();
			throw error;
		}
	}

	async stop(): Promise<void> {
		if (this.stopped) return;
		this.stopped = true;
		this.removeListener();
		try {
			await this.debug.sendCommand("Page.stopScreencast");
		} catch {
			// The tab/debugger may already be gone; local teardown still wins.
		}
	}

	private removeListener() {
		if (this.debug.off) this.debug.off("message", this.listener);
		else this.debug.removeListener?.("message", this.listener);
	}

	private async consume(params: Record<string, unknown>): Promise<void> {
		const sessionId = typeof params.sessionId === "number" ? params.sessionId : undefined;
		if (sessionId === undefined) return;
		try {
			const encoded = typeof params.data === "string" ? params.data : "";
			const jpeg = Buffer.from(encoded, "base64");
			if (jpeg.byteLength === 0 || jpeg.byteLength > 2 << 20) throw new Error("Browser frame is empty or too large");
			const dimensions = jpegDimensions(jpeg);
			this.sequence += 1n;
			this.emit({
				sequence: this.sequence,
				capturedAtMs: BigInt(Date.now()),
				width: dimensions.width,
				height: dimensions.height,
				jpeg,
			});
		} catch (error) {
			this.fail("BROWSER_CAPTURE_INVALID_FRAME", error instanceof Error ? error.message : "Browser frame was invalid");
		} finally {
			try {
				await this.debug.sendCommand("Page.screencastFrameAck", { sessionId });
			} catch {
				// A detached target will be handled by the owning browser lifecycle.
			}
		}
	}
}

export function jpegDimensions(data: Buffer): { width: number; height: number } {
	if (data.byteLength < 4 || data[0] !== 0xff || data[1] !== 0xd8) throw new Error("Browser frame is not JPEG");
	let offset = 2;
	while (offset + 8 < data.byteLength) {
		if (data[offset] !== 0xff) { offset += 1; continue; }
		const marker = data[offset + 1];
		offset += 2;
		if (marker === 0xd8 || marker === 0xd9) continue;
		if (offset + 2 > data.byteLength) break;
		const length = data.readUInt16BE(offset);
		if (length < 2 || offset + length > data.byteLength) break;
		if ([0xc0, 0xc1, 0xc2, 0xc3, 0xc5, 0xc6, 0xc7, 0xc9, 0xca, 0xcb, 0xcd, 0xce, 0xcf].includes(marker)) {
			const height = data.readUInt16BE(offset + 3);
			const width = data.readUInt16BE(offset + 5);
			if (width < 1 || height < 1) break;
			return { width, height };
		}
		offset += length;
	}
	throw new Error("Browser JPEG dimensions are unavailable");
}
