import type { Session } from "electron";
import { expect, it, vi } from "vitest";
import { clearBrowserSiteData, installBrowserSitePermissions } from "./browser-site-permissions";
import { BrowserSiteSettingsStore } from "./browser-site-settings-store";

it("enforces media-specific settings in both handlers and blocks unrelated origins and permissions", async () => {
	const store = new BrowserSiteSettingsStore("unused-temporary-settings");
	await store.set("temporary", "https://example.com", "camera", "allow");
	await store.set("temporary", "https://example.com", "location", "allow");
	await store.set("temporary", "https://example.com", "notifications", "allow");
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
		expect(check(contents, "geolocation", "https://example.com", {})).toBe(true);
		expect(check(null, "notifications", "https://example.com", {})).toBe(true);
		expect(check(null, "media", "https://example.com", { mediaType: "video", embeddingOrigin: "https://evil.example" })).toBe(false);
		request(contents, "media", callback, { ...details, mediaTypes: ["audio", "video"] });
		expect(callback).toHaveBeenLastCalledWith(false);
	request(contents, "media", callback, { ...details, requestingUrl: "https://other.example" });
		expect(callback).toHaveBeenLastCalledWith(false);
	request(contents, "display-capture", callback, details);
		expect(callback).toHaveBeenLastCalledWith(false);
	request(contents, "geolocation", callback, { requestingUrl: "https://example.com/page" });
		expect(callback).toHaveBeenLastCalledWith(true);
	request(contents, "notifications", callback, { requestingUrl: "https://example.com/page" });
		expect(callback).toHaveBeenLastCalledWith(true);
});

it("asks only for configured permissions and rejects a pending grant after navigation", async () => {
	const store = new BrowserSiteSettingsStore("unused-temporary-settings");
	await store.set("temporary", "https://example.com", "notifications", "ask");
	let resolve!: (decision: "dismiss" | "block" | "allow-once" | "allow-always") => void;
	const prompt = vi.fn(() => new Promise<"dismiss" | "block" | "allow-once" | "allow-always">((done) => { resolve = done; }));
	const session = { setPermissionCheckHandler: vi.fn(), setPermissionRequestHandler: vi.fn() };
	installBrowserSitePermissions(session, "temporary", store, prompt);
	const request = session.setPermissionRequestHandler.mock.calls[0][0];
	let url = "https://example.com";
	const callback = vi.fn();
	request({ getURL: () => url, isDestroyed: () => false }, "notifications", callback, { requestingUrl: url });
		expect(prompt).toHaveBeenCalledWith(expect.any(Object), url, ["notifications"]);
	url = "https://other.example";
	resolve("allow-once");
	await vi.waitFor(() => expect(callback).toHaveBeenCalledWith(false));
});

it("supports microphone-only discovery and keeps allow-once grants for later checks", async () => {
	const store = new BrowserSiteSettingsStore("unused-temporary-settings");
	await store.set("temporary", "https://example.com", "microphone", "allow");
	const prompt = vi.fn(async () => "allow-once" as const);
	const session = { setPermissionCheckHandler: vi.fn(), setPermissionRequestHandler: vi.fn() };
	installBrowserSitePermissions(session, "temporary", store, prompt);
	const check = session.setPermissionCheckHandler.mock.calls[0][0];
	const request = session.setPermissionRequestHandler.mock.calls[0][0];
	const contents = { getURL: () => "https://example.com/test", isDestroyed: () => false };
	const callback = vi.fn();

	// Device enumeration may arrive as an unknown media check. A microphone
	// grant must be sufficient without also granting the camera.
	expect(check(contents, "media", "https://example.com", { mediaType: "unknown" })).toBe(true);
	expect(check(contents, "media", "https://example.com", { mediaType: "audio" })).toBe(true);
	expect(check(contents, "media", "https://example.com", { mediaType: "video" })).toBe(false);
	request(contents, "media", callback, { securityOrigin: "https://example.com", mediaTypes: ["audio"] });
	expect(callback).toHaveBeenLastCalledWith(true);
	expect(prompt).not.toHaveBeenCalled();

	await store.set("temporary", "https://example.com", "microphone", "ask");
	callback.mockClear();
	request(contents, "media", callback, { requestingUrl: "https://example.com/test", mediaTypes: ["audio"] });
	await vi.waitFor(() => expect(callback).toHaveBeenLastCalledWith(true));
	expect(prompt).toHaveBeenCalledWith(contents, "https://example.com", ["microphone"]);
	expect(check(contents, "media", "https://example.com", { mediaType: "audio" })).toBe(true);

	// A later explicit block immediately revokes the temporary grant.
	await store.set("temporary", "https://example.com", "microphone", "block");
	expect(check(contents, "media", "https://example.com", { mediaType: "audio" })).toBe(false);
});

it("persists explicit allow and block decisions", async () => {
	const store = new BrowserSiteSettingsStore("unused-temporary-settings");
	await store.set("temporary", "https://example.com", "microphone", "ask");
	const prompt = vi.fn(async (): Promise<"dismiss" | "block" | "allow-once" | "allow-always"> => "allow-always");
	const session = { setPermissionCheckHandler: vi.fn(), setPermissionRequestHandler: vi.fn() };
	installBrowserSitePermissions(session, "temporary", store, prompt);
	const request = session.setPermissionRequestHandler.mock.calls[0][0];
	const contents = { id: 7, getURL: () => "https://example.com/test", isDestroyed: () => false };
	const callback = vi.fn();

	request(contents, "media", callback, { requestingUrl: contents.getURL(), mediaTypes: ["audio"] });
	await vi.waitFor(() => expect(callback).toHaveBeenCalledWith(true));
	expect(store.get("temporary", "https://example.com").microphone).toBe("allow");
	expect(prompt).toHaveBeenCalledOnce();

	await store.set("temporary", "https://example.com", "location", "ask");
	prompt.mockResolvedValueOnce("block");
	callback.mockClear();
	request(contents, "geolocation", callback, { requestingUrl: contents.getURL() });
	await vi.waitFor(() => expect(callback).toHaveBeenCalledWith(false));
	expect(store.get("temporary", "https://example.com").location).toBe("block");
});

it.each([
	["camera", "media", { requestingUrl: "https://example.com/page", mediaTypes: ["video"] }, { mediaType: "video" }],
	["microphone", "media", { requestingUrl: "https://example.com/page", mediaTypes: ["audio"] }, { mediaType: "audio" }],
	["location", "geolocation", { requestingUrl: "https://example.com/page" }, {}],
	["notifications", "notifications", { requestingUrl: "https://example.com/page" }, {}],
] as const)("supports an allow-once request for %s", async (sitePermission, electronPermission, details, checkDetails) => {
	const store = new BrowserSiteSettingsStore("unused-temporary-settings");
	const prompt = vi.fn(async () => "allow-once" as const);
	const session = { setPermissionCheckHandler: vi.fn(), setPermissionRequestHandler: vi.fn() };
	installBrowserSitePermissions(session, "temporary", store, prompt);
	const check = session.setPermissionCheckHandler.mock.calls[0][0];
	const request = session.setPermissionRequestHandler.mock.calls[0][0];
	const contents = { id: 11, getURL: () => "https://example.com/page", isDestroyed: () => false };
	const callback = vi.fn();

	request(contents, electronPermission, callback, details);
	await vi.waitFor(() => expect(callback).toHaveBeenCalledWith(true));
	expect(prompt).toHaveBeenCalledWith(contents, "https://example.com", [sitePermission]);
	expect(check(contents, electronPermission, "https://example.com", checkDetails)).toBe(true);
	callback.mockClear();
	request(contents, electronPermission, callback, details);
	expect(callback).toHaveBeenCalledWith(true);
	expect(prompt).toHaveBeenCalledOnce();
	await store.set("temporary", "https://example.com", sitePermission, "block");
	request(contents, electronPermission, callback, details);
	expect(callback).toHaveBeenLastCalledWith(false);
	expect(check(contents, electronPermission, "https://example.com", checkDetails)).toBe(false);
});

it("changes only undecided permissions in a combined media request", async () => {
	const store = new BrowserSiteSettingsStore("unused-temporary-settings");
	await store.set("temporary", "https://example.com", "camera", "allow");
	await store.set("temporary", "https://example.com", "microphone", "ask");
	const prompt = vi.fn(async () => "block" as const);
	const session = { setPermissionCheckHandler: vi.fn(), setPermissionRequestHandler: vi.fn() };
	installBrowserSitePermissions(session, "temporary", store, prompt);
	const request = session.setPermissionRequestHandler.mock.calls[0][0];
	const contents = { id: 12, getURL: () => "https://example.com/page", isDestroyed: () => false };
	const callback = vi.fn();

	request(contents, "media", callback, { requestingUrl: contents.getURL(), mediaTypes: ["video", "audio"] });
	await vi.waitFor(() => expect(callback).toHaveBeenCalledWith(false));
	expect(prompt).toHaveBeenCalledWith(contents, "https://example.com", ["microphone"]);
	expect(store.get("temporary", "https://example.com")).toMatchObject({ camera: "allow", microphone: "block" });
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
