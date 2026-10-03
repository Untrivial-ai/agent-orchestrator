import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: { getItem: vi.fn(), setItem: vi.fn() },
}));

import AsyncStorage from "@react-native-async-storage/async-storage";
import { browserSessionUrlKey, loadBrowserSessionUrl, saveBrowserSessionUrl } from "./browserSessionStore";

describe("mobile browser session URL storage", () => {
	beforeEach(() => vi.clearAllMocks());

	it("scopes history by host, port, and session", () => {
		expect(browserSessionUrlKey("192.168.1.2", 3011, "worker/a")).toBe("ao.mobileBrowser.lastUrl.v1.192.168.1.2.3011.worker%2Fa");
	});

	it("restores only web URLs", async () => {
		vi.mocked(AsyncStorage.getItem).mockResolvedValueOnce("https://example.com/path");
		expect(await loadBrowserSessionUrl("key")).toBe("https://example.com/path");
		vi.mocked(AsyncStorage.getItem).mockResolvedValueOnce("javascript:alert(1)");
		expect(await loadBrowserSessionUrl("key")).toBeUndefined();
	});

	it("persists a normalized web URL and ignores unsupported schemes", async () => {
		await saveBrowserSessionUrl("key", "https://example.com");
		expect(AsyncStorage.setItem).toHaveBeenCalledWith("key", "https://example.com/");
		await saveBrowserSessionUrl("key", "file:///tmp/index.html");
		expect(AsyncStorage.setItem).toHaveBeenCalledTimes(1);
	});
});
