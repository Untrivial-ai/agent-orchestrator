/**
 * AO-owned browser profile data. The renderer may use profile IDs, but it must
 * never receive filesystem paths or raw Electron partition names.
 */

export const BROWSER_PROFILE_REGISTRY_VERSION = 1 as const;
export const BROWSER_PROFILE_NAME_MAX_LENGTH = 64;
export const BROWSER_PROFILE_MAX_COUNT = 32;
export const BROWSER_PROFILE_MAX_BINDINGS = 1_024;
export const BROWSER_PROFILE_MAX_SESSION_ID_LENGTH = 160;
export const BROWSER_PROFILE_REGISTRY_FILE_NAME = "browser-profiles.json";

const UUID_PATTERN =
	/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const UNSAFE_RECORD_KEYS = new Set(["__proto__", "constructor", "prototype"]);

export type BrowserProfileId = string;

export type BrowserProfile = {
	id: BrowserProfileId;
	name: string;
	createdAt: string;
	updatedAt: string;
};

export type BrowserProfileBinding = {
	profileId: BrowserProfileId;
	updatedAt: string;
};

export type BrowserProfileRegistry = {
	version: typeof BROWSER_PROFILE_REGISTRY_VERSION;
	profiles: BrowserProfile[];
	bindings: Record<string, BrowserProfileBinding>;
};

export type BrowserProfileStoreError = {
	code: "BROWSER_PROFILE_STORE_CORRUPT" | "BROWSER_PROFILE_STORE_UNAVAILABLE";
	message: string;
};

export type BrowserProfileListState = {
	profiles: BrowserProfile[];
	error?: BrowserProfileStoreError;
};

export type BrowserProfileViewState = {
	viewId: string;
	profileId: BrowserProfileId | null;
	profileName?: string;
	/** True when the selection is the per-worker memory-only profile. */
	temporary: boolean;
};

export type BrowserMenuBounds = {
	x: number;
	y: number;
	width: number;
	height: number;
};

export type BrowserProfileMenuLabels = {
	temporary: string;
	manage: string;
	switchTitle: string;
	switchMessage: string;
	switchDetail: string;
	cancel: string;
	confirm: string;
};

export type BrowserProfileMenuInput = {
	viewId: string;
	bounds: BrowserMenuBounds;
	labels: BrowserProfileMenuLabels;
};

export function isBrowserProfileId(value: unknown): value is BrowserProfileId {
	return typeof value === "string" && UUID_PATTERN.test(value);
}

export function normalizeBrowserProfileId(value: unknown): BrowserProfileId | null {
	return isBrowserProfileId(value) ? value.toLowerCase() : null;
}

/**
 * Names are display-only. They are deliberately not used in a path or an
 * Electron partition, but control characters are still rejected so a profile
 * cannot forge terminal/UI output.
 */
export function normalizeBrowserProfileName(value: unknown): string | null {
	if (typeof value !== "string") return null;
	const name = value.trim();
	if (
		name.length === 0 ||
		name.length > BROWSER_PROFILE_NAME_MAX_LENGTH ||
		/[\u0000-\u001f\u007f]/u.test(name)
	) {
		return null;
	}
	return name;
}

export function isValidBrowserProfileSessionId(value: unknown): value is string {
	return (
		typeof value === "string" &&
		value.length > 0 &&
		value.length <= BROWSER_PROFILE_MAX_SESSION_ID_LENGTH &&
		!UNSAFE_RECORD_KEYS.has(value) &&
		!Object.hasOwn(Object.prototype, value) &&
		!/[\u0000-\u001f\u007f]/u.test(value)
	);
}

/** The only partition constructor used by AO-owned named profiles. */
export function browserProfilePartition(id: BrowserProfileId): string {
	const normalized = normalizeBrowserProfileId(id);
	if (!normalized) throw new Error("Invalid browser profile ID");
	return `persist:ao-browser-profile-${normalized}`;
}
