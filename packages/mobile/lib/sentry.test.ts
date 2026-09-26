import { describe, expect, it } from "vitest";

import { captureMobileApiError, captureMobileException, initMobileSentry, scrubMobileTelemetryText } from "./sentry";

// No SDK installed yet: init must resolve and captures must never throw.
describe("sentry — SDK not installed", () => {
	it("captures are no-ops before init", () => {
		expect(() => captureMobileException(new Error("boom"), { category: "native_crash" })).not.toThrow();
		expect(() => captureMobileApiError("/api/v1/sessions/123", "http_5xx", 503)).not.toThrow();
	});

	it("init resolves without the SDK and captures stay no-ops", async () => {
		await expect(initMobileSentry({ release: "0.0.0@1" })).resolves.toBeUndefined();
		expect(() => captureMobileException(new Error("boom"), { category: "native_crash" })).not.toThrow();
		expect(() => captureMobileApiError("/api/v1/sessions/123", "http_5xx", 503)).not.toThrow();
	});
});

describe("mobile Sentry scrubbing", () => {
	it("redacts full paths containing spaces or delimiter-like text", () => {
		expect(scrubMobileTelemetryText("open /Users/alice/build error logs/x.ts")).toBe("open [redacted-path]");
		expect(scrubMobileTelemetryText("open /home/alice/a: b/c.md")).toBe("open [redacted-path]");
		expect(scrubMobileTelemetryText("read /data/data/com.ao/files/My Notes/private.txt")).toBe("read [redacted-path]");
	});
});
