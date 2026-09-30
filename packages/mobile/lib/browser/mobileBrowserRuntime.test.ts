import { describe, expect, it } from "vitest";
import type { ServerConfig } from "../config";
import { mobileBrowserRuntimeURL } from "./mobileBrowserRuntimeUrl";

const config: ServerConfig = {
	host: "https://192.168.1.20/",
	httpPort: "3011",
	muxPort: "3011",
	secure: false,
	password: "secret",
};

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
