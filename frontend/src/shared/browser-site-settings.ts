export const BROWSER_SITE_PERMISSIONS = ["camera", "microphone", "location", "notifications"] as const;
export type BrowserSitePermission = typeof BROWSER_SITE_PERMISSIONS[number];
export type BrowserSitePermissionSetting = "allow" | "ask" | "block";
export type BrowserSitePermissions = Record<BrowserSitePermission, BrowserSitePermissionSetting>;

export type BrowserSiteTarget = {
	viewId: string;
	tabId: string;
	profileId: string | null;
	origin: string;
};

export type BrowserSiteSettings = BrowserSiteTarget & { permissions: BrowserSitePermissions };
export type BrowserSitePermissionInput = BrowserSiteTarget & {
	permission: BrowserSitePermission;
	setting: BrowserSitePermissionSetting;
};

export type BrowserSitePermissionRequest = {
	requestId: string;
	viewId: string;
	tabId: string;
	origin: string;
	permissions: BrowserSitePermission[];
};

export type BrowserSitePermissionDecisionValue = "dismiss" | "block" | "allow-once" | "allow-always";

export type BrowserSitePermissionDecision = {
	requestId: string;
	viewId: string;
	decision: BrowserSitePermissionDecisionValue;
};

export function browserSiteOrigin(value: unknown): string | null {
	if (typeof value !== "string" || value.length > 4096) return null;
	try {
		const url = new URL(value);
		return url.protocol === "http:" || url.protocol === "https:" ? url.origin : null;
	} catch {
		return null;
	}
}

export function defaultBrowserSitePermissions(): BrowserSitePermissions {
	return { camera: "ask", microphone: "ask", location: "ask", notifications: "ask" };
}
