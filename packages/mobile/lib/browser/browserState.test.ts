import { describe, expect, it } from "vitest";
import { browserLoadEnd, browserLoadError, browserLoadStart, browserNavigationChanged, initialBrowserState } from "./browserState";

describe("mobile browser state", () => {
	it("starts loading and clears stale errors", () => {
		const state = browserLoadStart({ ...initialBrowserState, error: "old" }, "https://example.com/");
		expect(state).toMatchObject({ url: "https://example.com/", loading: true, error: undefined });
	});

	it("records navigation flags without dropping existing fields", () => {
		const state = browserNavigationChanged({ ...initialBrowserState, title: "Old" }, { url: "https://example.com/", canGoBack: true });
		expect(state).toMatchObject({ title: "Old", url: "https://example.com/", canGoBack: true, canGoForward: false });
	});

	it("finishes loads and records errors", () => {
		const loaded = browserLoadEnd({ ...initialBrowserState, loading: true });
		expect(loaded.loading).toBe(false);
		const failed = browserLoadError(loaded, "HTTP 500");
		expect(failed).toMatchObject({ loading: false, error: "HTTP 500" });
	});
});
