/**
 * Builds control-plane browser-proxy URLs for cloud sessions.
 *
 * The CP serves sandbox-local http(s) pages through
 * `/api/cloud/v1/orgs/{orgId}/sessions/{sessionId}/browser/{origin}/...`
 * so Electron's BrowserView can load VM localhost the same way the cloud web
 * iframe does. Origin is base64url(scheme://host[:port]) without padding.
 */

/** Encode an http(s) origin the same way cloud/internal/httpapi/browser_rewrite.go does. */
export function encodeCloudBrowserOrigin(origin: string): string {
	const bytes = new TextEncoder().encode(origin);
	let binary = "";
	for (const byte of bytes) binary += String.fromCharCode(byte);
	return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/**
 * Rewrite a sandbox-reachable http(s) URL into an absolute CP browser-proxy URL.
 * Non-http(s) values (about:, file:, data:) pass through unchanged.
 */
export function toCloudBrowserProxyUrl(
	baseUrl: string,
	orgId: string,
	sessionId: string,
	targetUrl: string,
): string {
	const trimmed = targetUrl.trim();
	if (!trimmed) return trimmed;
	let parsed: URL;
	try {
		parsed = new URL(trimmed);
	} catch {
		return trimmed;
	}
	if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return trimmed;
	const origin = `${parsed.protocol}//${parsed.host}`;
	const path = parsed.pathname.replace(/^\//, "");
	const prefix =
		`${baseUrl.replace(/\/+$/, "")}/api/cloud/v1/orgs/${encodeURIComponent(orgId)}` +
		`/sessions/${encodeURIComponent(sessionId)}/browser/${encodeCloudBrowserOrigin(origin)}/`;
	return `${prefix}${path}${parsed.search}${parsed.hash}`;
}

/** True when the URL already targets the session's CP browser proxy. */
export function isCloudBrowserProxyUrl(url: string, orgId: string, sessionId: string): boolean {
	try {
		const parsed = new URL(url);
		const marker =
			`/api/cloud/v1/orgs/${encodeURIComponent(orgId)}/sessions/${encodeURIComponent(sessionId)}/browser/`;
		return parsed.pathname.includes(marker);
	} catch {
		return false;
	}
}
