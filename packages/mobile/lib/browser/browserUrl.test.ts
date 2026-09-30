import { describe, expect, it } from "vitest";
import type { ServerConfig } from "../config";
import { displayBrowserUrl, inAppWebNavigation, isHttpUrl, normalizeBrowserInput, shouldAttachPreviewAuth } from "./browserUrl";

const cfg: ServerConfig = { host: "192.168.1.10", httpPort: "3011", muxPort: "14801", secure: false, password: "secret" };

describe("mobile browser URL helpers", () => {
	it("keeps X's Safari-forcing redirect inside the webview", () => {
		expect(inAppWebNavigation("x-safari-https://redirect.x.com/?ct=rw-null")).toBe("https://redirect.x.com/?ct=rw-null");
		expect(inAppWebNavigation("mailto:test@example.com")).toBeUndefined();
	});

	it("normalizes bare hosts to HTTPS URLs", () => {
		const result = normalizeBrowserInput("example.com/docs", cfg.host);
		expect(result.ok).toBe(true);
		if (result.ok) expect(result.url.href).toBe("https://example.com/docs");
	});

	it("keeps explicit HTTP and HTTPS URLs", () => {
		const http = normalizeBrowserInput("http://example.com:5173/", cfg.host);
		const https = normalizeBrowserInput("https://example.com/path", cfg.host);
		expect(http.ok && http.url.href).toBe("http://example.com:5173/");
		expect(https.ok && https.url.href).toBe("https://example.com/path");
	});

	it("rewrites loopback hosts to the paired AO host", () => {
		const localhost = normalizeBrowserInput("http://localhost:5173/", cfg.host);
		const loopback = normalizeBrowserInput("http://127.0.0.1:3000/app", cfg.host);
		expect(localhost.ok && localhost.url.href).toBe("http://192.168.1.10:5173/");
		expect(loopback.ok && loopback.url.href).toBe("http://192.168.1.10:3000/app");
	});

	it("rejects unsupported schemes", () => {
		expect(normalizeBrowserInput("javascript:alert(1)", cfg.host)).toMatchObject({ ok: false, reason: "unsupported_scheme" });
		expect(normalizeBrowserInput("file:///tmp/index.html", cfg.host)).toMatchObject({ ok: false, reason: "unsupported_scheme" });
	});

	it("attaches auth only to AO preview-files URLs", () => {
		expect(shouldAttachPreviewAuth("http://192.168.1.10:3011/api/v1/sessions/s1/preview/files/index.html", cfg)).toBe(true);
		expect(shouldAttachPreviewAuth("http://192.168.1.10:3011/api/v1/sessions/s1", cfg)).toBe(false);
		expect(shouldAttachPreviewAuth("http://example.com/api/v1/sessions/s1/preview/files/index.html", cfg)).toBe(false);
		expect(shouldAttachPreviewAuth("http://192.168.1.10:5173/", cfg)).toBe(false);
	});

	it("formats display URLs without leaking unsupported parsing assumptions", () => {
		expect(displayBrowserUrl("https://www.example.com/path?x=1")).toBe("example.com/path?x=1");
		expect(displayBrowserUrl("not a url")).toBe("not a url");
		expect(isHttpUrl("https://example.com")).toBe(true);
		expect(isHttpUrl("mailto:test@example.com")).toBe(false);
	});
});
