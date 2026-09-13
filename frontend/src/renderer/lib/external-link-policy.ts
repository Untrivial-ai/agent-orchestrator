import { aoBridge } from "./bridge";

export function isWebLink(url: string): boolean {
	try {
		const { protocol } = new URL(url);
		return protocol === "http:" || protocol === "https:";
	} catch {
		return false;
	}
}

/** Cloudflare challenges do not reliably complete in embedded Electron views. */
export function requiresSystemBrowser(url: string): boolean {
	try {
		const parsed = new URL(url.includes("://") ? url : `https://${url}`);
		const host = parsed.hostname.toLowerCase();
		return host === "dash.cloudflare.com" || host.endsWith(".dash.cloudflare.com");
	} catch {
		return false;
	}
}

export async function openLinkInSystemBrowser(url: string): Promise<void> {
	try {
		await aoBridge.app.openExternal(url);
	} catch (error) {
		console.warn("Unable to open link in system browser", error);
	}
}

export function handleModifierLinkClick(event: MouseEvent): void {
	if (event.button !== 0 || !event.altKey || event.defaultPrevented) return;
	const anchor = event.target instanceof Element ? event.target.closest("a[href]") : null;
	const href = anchor?.getAttribute("href");
	if (!href) return;

	let url: URL;
	try {
		url = new URL(href, window.location.href);
	} catch {
		return;
	}
	if (!["http:", "https:"].includes(url.protocol) || url.origin === window.location.origin) return;

	event.preventDefault();
	void openLinkInSystemBrowser(url.href);
}
