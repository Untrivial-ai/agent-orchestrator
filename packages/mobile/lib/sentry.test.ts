import { describe, expect, it } from "vitest";

import { captureMobileApiError, captureMobileException, initMobileSentry } from "./sentry";

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
