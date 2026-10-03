import { describe, expect, it } from "vitest";
import { browserJPEGDataURI, browserLiveURL, containBrowserFrame, decodeBrowserFrame } from "./browserLiveProtocol";

describe("browser live transport", () => {
	it("builds the authenticated listener URL without putting the password in it", () => {
		const config = { host: "192.168.1.5", httpPort: "3011", password: "secret" };
		const url = browserLiveURL(config, "worker/a");
		expect(url).toBe("ws://192.168.1.5:3011/api/v1/sessions/worker%2Fa/browser/live");
		expect(url).not.toContain("secret");
	});

	it("decodes the versioned binary JPEG envelope", () => {
		const bytes = new Uint8Array(25);
		const view = new DataView(bytes.buffer);
		bytes[0] = 1;
		bytes[1] = 1;
		view.setBigUint64(2, 7n);
		view.setBigUint64(10, 123n);
		view.setUint16(18, 640);
		view.setUint16(20, 360);
		bytes.set([0xff, 0xd8, 0xff], 22);
		const frame = decodeBrowserFrame(bytes.buffer);
		expect(frame).toMatchObject({ sequence: 7n, capturedAtMs: 123n, width: 640, height: 360 });
		expect([...new Uint8Array(frame.jpeg)]).toEqual([0xff, 0xd8, 0xff]);
	});

	it("fits the desktop frame without stretching it", () => {
		expect(containBrowserFrame({ width: 390, height: 600 }, { width: 1280, height: 720 })).toEqual({
			left: 0,
			top: 190.3125,
			width: 390,
			height: 219.375,
		});
	});

	it("encodes JPEG bytes as a React Native image data URI", () => {
		expect(browserJPEGDataURI(Uint8Array.from([0xff, 0xd8, 0xff]).buffer)).toBe("data:image/jpeg;base64,/9j/");
	});
});
