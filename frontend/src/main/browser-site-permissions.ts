import type { Session, WebContents } from "electron";
import {
	browserSiteOrigin,
	type BrowserSitePermission,
	type BrowserSitePermissionDecisionValue,
} from "../shared/browser-site-settings";
import type { BrowserSiteSettingsStore } from "./browser-site-settings-store";

export type BrowserPermissionPrompt = (
	contents: WebContents,
	origin: string,
	permissions: BrowserSitePermission[],
) => Promise<BrowserSitePermissionDecisionValue>;

function permissionNames(permission: string, mediaTypes?: string[]): BrowserSitePermission[] {
	if (permission === "geolocation") return ["location"];
	if (permission === "notifications") return ["notifications"];
	if (permission !== "media" || !mediaTypes?.length || mediaTypes.some((type) => type !== "audio" && type !== "video")) return [];
	return [...new Set(mediaTypes.map((type) => type === "audio" ? "microphone" as const : "camera" as const))];
}

function requestOrigin(contents: { getURL: () => string }, details?: { requestingUrl?: string; securityOrigin?: string }): string | null {
	return browserSiteOrigin(details?.requestingUrl) ?? browserSiteOrigin(details?.securityOrigin) ?? browserSiteOrigin(contents.getURL());
}

export function installBrowserSitePermissions(
	session: Pick<Session, "setPermissionCheckHandler" | "setPermissionRequestHandler">,
	scope: string,
	store?: BrowserSiteSettingsStore,
	prompt?: BrowserPermissionPrompt,
): void {
	// Electron asks again after an "allow once" decision (for example when a
	// page enumerates devices after opening a microphone stream). Keep that
	// grant for this WebContents and origin without persisting it as a site
	// preference. Navigating to another origin cannot reuse it.
	const temporaryGrants = new WeakMap<object, Map<string, Set<BrowserSitePermission>>>();
	const hasTemporaryGrant = (contents: object | null, origin: string, name: BrowserSitePermission): boolean =>
		Boolean(contents && temporaryGrants.get(contents)?.get(origin)?.has(name));
	const grantTemporarily = (contents: object, origin: string, names: BrowserSitePermission[]): void => {
		let byOrigin = temporaryGrants.get(contents);
		if (!byOrigin) {
			byOrigin = new Map();
			temporaryGrants.set(contents, byOrigin);
		}
		const granted = byOrigin.get(origin) ?? new Set<BrowserSitePermission>();
		for (const name of names) granted.add(name);
		byOrigin.set(origin, granted);
	};
	const isAllowed = (contents: object | null, origin: string, name: BrowserSitePermission): boolean => {
		const setting = store?.get(scope, origin)[name];
		if (setting === "block") return false;
		return setting === "allow" || hasTemporaryGrant(contents, origin, name);
	};

	session.setPermissionCheckHandler((contents, permission, requestingOrigin, details) => {
		if (!store) return false;
		const origin = browserSiteOrigin(requestingOrigin) ?? browserSiteOrigin(details?.securityOrigin) ?? browserSiteOrigin(details?.requestingUrl);
		if (!origin || (details?.embeddingOrigin && browserSiteOrigin(details.embeddingOrigin) !== origin) ||
			(contents && browserSiteOrigin(contents.getURL()) !== origin)) return false;
		const names = permissionNames(permission, details?.mediaType === "unknown"
			? ["audio", "video"] : details?.mediaType ? [details.mediaType] : undefined);
		if (!names.length) return false;
		// Chromium uses an "unknown" media check while discovering devices. It
		// must not require camera permission when only the microphone was granted.
		return permission === "media" && details?.mediaType === "unknown"
			? names.some((name) => isAllowed(contents, origin, name))
			: names.every((name) => isAllowed(contents, origin, name));
	});
	const pending = new Map<string, Promise<BrowserSitePermissionDecisionValue>>();
	session.setPermissionRequestHandler((contents, permission, callback, details) => {
		if (!store) return callback(false);
		const origin = requestOrigin(contents, details);
		const names = permissionNames(permission, details && "mediaTypes" in details ? details.mediaTypes : undefined);
		if (!origin || !names.length || contents.isDestroyed() || browserSiteOrigin(contents.getURL()) !== origin) return callback(false);
		const settings = store.get(scope, origin);
		if (names.some((name) => settings[name] === "block")) return callback(false);
		const undecidedNames = names.filter((name) => settings[name] === "ask");
		if (!undecidedNames.length) return callback(true);
		if (!prompt) return callback(false);
		const key = `${contents.id}:${origin}:${undecidedNames.join(",")}`;
		let request = pending.get(key);
		if (!request) {
			request = prompt(contents, origin, undecidedNames).catch(() => "dismiss" as const);
			pending.set(key, request);
			void request.finally(() => pending.delete(key));
		}
		void request.then(async (decision) => {
			const isCurrent = () => !contents.isDestroyed() && browserSiteOrigin(contents.getURL()) === origin;
			if (decision === "dismiss" || !isCurrent()) return callback(false);
			if (decision === "block" || decision === "allow-always") {
				try {
					await Promise.all(undecidedNames.map((name) => store.set(
						scope, origin, name, decision === "block" ? "block" : "allow",
					)));
				} catch {
					return callback(false);
				}
				if (decision === "block") return callback(false);
			} else if (decision === "allow-once") {
				grantTemporarily(contents, origin, undecidedNames);
			}
			callback(isCurrent() && names.every((name) => store.get(scope, origin)[name] !== "block"));
		});
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
