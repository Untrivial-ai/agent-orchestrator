import { afterEach, describe, expect, it, vi } from "vitest";
import { MobileBrowserUrlWaits } from "./mobileBrowserUrlWait";

afterEach(() => vi.useRealTimers());

describe("native mobile browser URL waits", () => {
	it("resolves from a navigation update after the original document is replaced", async () => {
		const waits = new MobileBrowserUrlWaits();
		const result = waits.wait("wait-1", "example.com/destination", "https://example.com/start", 1_000);

		// This update comes from the native WebView navigation callback rather than
		// the JavaScript bridge belonging to the document that initiated the wait.
		waits.update("https://example.com/destination?from=link");

		await expect(result).resolves.toEqual({
			ok: true,
			result: { matched: true, url: "https://example.com/destination?from=link" },
		});
	});

	it("preserves URL substring matching and resolves immediately for the current page", async () => {
		const waits = new MobileBrowserUrlWaits();
		await expect(waits.wait("wait-1", "/ready", "https://example.com/ready?id=1")).resolves.toEqual({
			ok: true,
			result: { matched: true, url: "https://example.com/ready?id=1" },
		});
	});

	it("times out with the existing wait error contract", async () => {
		vi.useFakeTimers();
		const waits = new MobileBrowserUrlWaits();
		const result = waits.wait("wait-1", "/never", "https://example.com/start", 250);
		await vi.advanceTimersByTimeAsync(250);
		await expect(result).resolves.toEqual({
			ok: false,
			error: { code: "WAIT_TIMEOUT", message: "Timed out waiting for page condition." },
		});
	});
});
