import type { ServerConfig } from "../config";

export function mobileBrowserRuntimeURL(cfg: ServerConfig, sessionId: string, deviceId: string): string {
	const scheme = cfg.secure ? "wss" : "ws";
	const host = cfg.host.trim().replace(/^[a-z][a-z0-9+.-]*:\/\//i, "").replace(/\/+$/, "");
	const query = `sessionId=${encodeURIComponent(sessionId)}&deviceId=${encodeURIComponent(deviceId)}`;
	return `${scheme}://${host}:${cfg.httpPort}/mobile-browser-runtime?${query}`;
}
