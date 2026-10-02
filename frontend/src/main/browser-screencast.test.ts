import { describe, expect, it, vi } from "vitest";
import { BrowserScreencast, jpegDimensions } from "./browser-screencast";

function jpeg(width = 640, height = 360): Buffer {
	const data = Buffer.alloc(21);
	data.set([0xff, 0xd8, 0xff, 0xc0, 0x00, 0x11, 0x08]);
	data.writeUInt16BE(height, 7);
	data.writeUInt16BE(width, 9);
	return data;
}

describe("BrowserScreencast", () => {
	it("reads JPEG dimensions without decoding the image", () => {
		expect(jpegDimensions(jpeg(1280, 720))).toEqual({ width: 1280, height: 720 });
	});

	it("starts bounded JPEG capture and acknowledges frames", async () => {
		let listener: ((event: unknown, method: string, params: Record<string, unknown>) => void) | undefined;
		const sendCommand = vi.fn(async () => undefined);
		const emit = vi.fn();
		const debug = {
			on: (_event: "message", next: typeof listener) => { listener = next; },
			off: vi.fn(),
			sendCommand,
		};
		const cast = new BrowserScreencast(debug, emit, vi.fn());
		await cast.start();
		expect(sendCommand).toHaveBeenCalledWith("Page.startScreencast", expect.objectContaining({ format: "jpeg", maxWidth: 1280, maxHeight: 720 }));
		listener?.({}, "Page.screencastFrame", { sessionId: 9, data: jpeg().toString("base64") });
		await vi.waitFor(() => expect(emit).toHaveBeenCalledWith(expect.objectContaining({ width: 640, height: 360 })));
		expect(sendCommand).toHaveBeenCalledWith("Page.screencastFrameAck", { sessionId: 9 });
		await cast.stop();
		expect(sendCommand).toHaveBeenCalledWith("Page.stopScreencast");
	});
});
