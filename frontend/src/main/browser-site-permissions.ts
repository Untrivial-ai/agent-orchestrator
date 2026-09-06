import type { Session } from "electron";
import { browserSiteOrigin, type BrowserSitePermission } from "../shared/browser-site-settings";
import type { BrowserSiteSettingsStore } from "./browser-site-settings-store";

export type BrowserPermissionPrompt = (origin: string, permissions: BrowserSitePermission[]) => Promise<boolean>;

function permissionNames(permission: string, mediaTypes?: string[]): BrowserSitePermission[] {
	if (permission === "geolocation") return ["location"];
	if (permission === "notifications") return ["notifications"];
	if (permission !== "media" || !mediaTypes?.length || mediaTypes.some((type) => type !== "audio" && type !== "video")) return [];
	return [...new Set(mediaTypes.map((type) => type === "audio" ? "microphone" as const : "camera" as const))];
}

export function installBrowserSitePermissions(
	session: Pick<Session, "setPermissionCheckHandler" | "setPermissionRequestHandler">,
	scope: string,
	store?: BrowserSiteSettingsStore,
	prompt?: BrowserPermissionPrompt,
): void {
	session.setPermissionCheckHandler((contents, permission, requestingOrigin, details) => {
		if (!store) return false;
		const origin = browserSiteOrigin(requestingOrigin);
		if (!origin || (details?.embeddingOrigin && browserSiteOrigin(details.embeddingOrigin) !== origin) ||
			(contents && browserSiteOrigin(contents.getURL()) !== origin)) return false;
		const names = permissionNames(permission, details?.mediaType === "unknown"
			? ["audio", "video"] : details?.mediaType ? [details.mediaType] : undefined);
		const settings = store.get(scope, origin);
		return names.length > 0 && names.every((name) => settings[name] === "allow");
	});
	const pending = new Map<string, Promise<boolean>>();
	session.setPermissionRequestHandler((contents, permission, callback, details) => {
		if (!store) return callback(false);
		const origin = browserSiteOrigin(details?.requestingUrl);
		const names = permissionNames(permission, details && "mediaTypes" in details ? details.mediaTypes : undefined);
		if (!origin || !names.length || contents.isDestroyed() || browserSiteOrigin(contents.getURL()) !== origin) return callback(false);
		const settings = store.get(scope, origin);
		if (names.some((name) => settings[name] === "block")) return callback(false);
		if (names.every((name) => settings[name] === "allow")) return callback(true);
		if (!prompt) return callback(false);
		const key = `${origin}:${names.join(",")}`;
		let request = pending.get(key);
		if (!request) {
			request = prompt(origin, names).catch(() => false);
			pending.set(key, request);
			void request.finally(() => pending.delete(key));
		}
		void request.then((allowed) => callback(allowed && !contents.isDestroyed() &&
			browserSiteOrigin(contents.getURL()) === origin && names.every((name) => store.get(scope, origin)[name] !== "block")));
	});
}

export async function clearBrowserSiteData(session: Pick<Session, "cookies" | "clearStorageData">, origin: string): Promise<void> {
	if (browserSiteOrigin(origin) !== origin) throw new Error("Invalid site origin");
	const hostname = new URL(origin).hostname;
	// Electron's bulk cookie deletion can clear the entire registrable domain.
	// Remove only cookies that apply to this host, including shared parent cookies.
	const cookies = (await session.cookies.get({})).filter((cookie) => {
		const domain = (cookie.domain ?? "").replace(/^\./, "");
		return domain && (hostname === domain || ((cookie.hostOnly === false || cookie.domain?.startsWith(".")) && hostname.endsWith(`.${domain}`)));
	});
	await Promise.all(cookies.map((cookie) => session.cookies.remove(
		`${cookie.secure ? "https" : "http"}://${(cookie.domain ?? "").replace(/^\./, "")}${cookie.path || "/"}`, cookie.name,
	)));
	await session.clearStorageData({ origin, storages: ["filesystem", "indexdb", "localstorage", "serviceworkers", "cachestorage"] });
}
