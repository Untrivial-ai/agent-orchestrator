import type { BrowserProfile } from "./browser-profiles";

export const BROWSER_IMPORT_MAX_SOURCE_PROFILES = 16;
export const BROWSER_IMPORT_MAX_HISTORY_ENTRIES = 5_000;
export const BROWSER_IMPORT_MAX_COOKIES = 20_000;

export type BrowserImportFamily = "chromium" | "firefox";
export type BrowserImportCookieSupport = "supported" | "partial" | "unsupported";
export type BrowserImportCookieSupportReason =
	| "firefox-plaintext"
	| "chromium-encryption-partial"
	| "chromium-encryption-unsupported";

export type BrowserImportSourceProfile = {
	id: string;
	name: string;
	default: boolean;
};

export type BrowserImportSource = {
	id: string;
	name: string;
	family: BrowserImportFamily;
	profiles: BrowserImportSourceProfile[];
	cookieSupport: BrowserImportCookieSupport;
	cookieSupportReason: BrowserImportCookieSupportReason;
	historySupport: true;
};

export type BrowserImportDiscovery = {
	sources: BrowserImportSource[];
};

export type BrowserImportDestination =
	| { mode: "separate"; names: Record<string, string> }
	| { mode: "merge"; name: string };

export type BrowserImportRequest = {
	requestId: string;
	sourceId: string;
	profileIds: string[];
	includeCookies: boolean;
	includeHistory: boolean;
	destination: BrowserImportDestination;
};

export type BrowserImportWarningCode =
	| "cookie-database-missing"
	| "history-database-missing"
	| "isolated-cookies-skipped"
	| "cookie-limit-truncated"
	| "history-limit-truncated"
	| "encrypted-cookies-skipped"
	| "expired-cookies-skipped"
	| "invalid-cookies-skipped"
	| "cookie-attributes-defaulted"
	| "cookie-write-failed";

export type BrowserImportWarning = {
	code: BrowserImportWarningCode;
	count?: number;
};

export type BrowserImportResultEntry = {
	sourceProfileNames: string[];
	destinationProfile: BrowserProfile;
	importedCookies: number;
	skippedCookies: number;
	importedHistoryEntries: number;
	warnings: BrowserImportWarning[];
};

export type BrowserImportResult = {
	sourceName: string;
	entries: BrowserImportResultEntry[];
};

export type BrowserImportProgress = {
	requestId: string;
	phase: "preparing" | "reading" | "importing";
	completed: number;
	total: number;
};

export type BrowserHistorySuggestion = {
	url: string;
	title?: string;
};
