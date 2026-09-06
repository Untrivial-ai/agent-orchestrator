import type { Session } from "electron";
import { expect, it, vi } from "vitest";
import { clearBrowserSiteData, installBrowserSitePermissions } from "./browser-site-permissions";
import { BrowserSiteSettingsStore } from "./browser-site-settings-store";

it("enforces media-specific settings in both handlers and blocks unrelated origins and permissions", async () => {
	const store = new BrowserSiteSettingsStore("unused-temporary-settings");
	await store.set("temporary", "https://example.com", "camera", "allow");
	const session = { setPermissionCheckHandler: vi.fn(), setPermissionRequestHandler: vi.fn() };
	installBrowserSitePermissions(session, "temporary", store);
	const check = session.setPermissionCheckHandler.mock.calls[0][0];
	const request = session.setPermissionRequestHandler.mock.calls[0][0];
	const contents = { getURL: () => "https://example.com/page", isDestroyed: () => false };
	const callback = vi.fn();
	const details = { requestingUrl: "https://example.com/page", mediaTypes: ["video"] };
	request(contents, "media", callback, details);
		expect(callback).toHaveBeenLastCalledWith(true);
		expect(check(contents, "media", "https://example.com", { mediaType: "video" })).toBe(true);
		expect(check(contents, "media", "https://example.com", { mediaType: "audio" })).toBe(false);
		expect(check(null, "media", "https://example.com", { mediaType: "video", embeddingOrigin: "https://evil.example" })).toBe(false);
		request(contents, "media", callback, { ...details, mediaTypes: ["audio", "video"] });
		expect(callback).toHaveBeenLastCalledWith(false);
	request(contents, "media", callback, { ...details, requestingUrl: "https://other.example" });
		expect(callback).toHaveBeenLastCalledWith(false);
	request(contents, "display-capture", callback, details);
		expect(callback).toHaveBeenLastCalledWith(false);
});

it("asks only for configured permissions and rejects a pending grant after navigation", async () => {
	const store = new BrowserSiteSettingsStore("unused-temporary-settings");
	await store.set("temporary", "https://example.com", "notifications", "ask");
	let resolve!: (allow: boolean) => void;
	const prompt = vi.fn(() => new Promise<boolean>((done) => { resolve = done; }));
	const session = { setPermissionCheckHandler: vi.fn(), setPermissionRequestHandler: vi.fn() };
	installBrowserSitePermissions(session, "temporary", store, prompt);
	const request = session.setPermissionRequestHandler.mock.calls[0][0];
	let url = "https://example.com";
	const callback = vi.fn();
	request({ getURL: () => url, isDestroyed: () => false }, "notifications", callback, { requestingUrl: url });
		expect(prompt).toHaveBeenCalledWith(url, ["notifications"]);
	url = "https://other.example";
	resolve(true);
	await vi.waitFor(() => expect(callback).toHaveBeenCalledWith(false));
});

it("clears only applicable cookies and origin-scoped storage, never the whole profile", async () => {
	const remove = vi.fn(async () => undefined);
	const clearStorageData = vi.fn(async () => undefined);
	const session = { cookies: { get: async () => [
		{ name: "current", domain: "app.example.com", hostOnly: true, secure: true, path: "/account" },
		{ name: "shared", domain: ".example.com", hostOnly: false, secure: true, path: "/" },
		{ name: "sibling", domain: "other.example.com", hostOnly: true, path: "/" },
		{ name: "unrelated", domain: ".unrelated.com", hostOnly: false, path: "/" },
	], remove }, clearStorageData } as unknown as Pick<Session, "cookies" | "clearStorageData">;
	await clearBrowserSiteData(session, "https://app.example.com");
		expect(remove.mock.calls).toEqual([
		["https://app.example.com/account", "current"], ["https://example.com/", "shared"],
	]);
		expect(clearStorageData).toHaveBeenCalledWith({ origin: "https://app.example.com", storages: ["filesystem", "indexdb", "localstorage", "serviceworkers", "cachestorage"] });
	await expect(clearBrowserSiteData(session, "file:///local")).rejects.toThrow("Invalid site origin");
});
