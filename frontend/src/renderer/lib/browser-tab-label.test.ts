import { describe, expect, it } from "vitest";
import { browserTabLabel } from "./browser-tab-label";

// The test setup (src/renderer/test/setup.ts) boots i18n in English, so:
//   appI18n.t("browser.newTab")   === "New tab"
//   appI18n.t("browser.blankPage") === "Blank page"

describe("browserTabLabel", () => {
	// ─── empty / blank URL ───────────────────────────────────────────────────
	describe("blank URL", () => {
		it("returns i18n new-tab fallbacks when both title and URL are empty", () => {
			const result = browserTabLabel("", "");
			expect(result.title).toBe("New tab");
			expect(result.subtitle).toBe("Blank page");
		});

		it("keeps the provided title but uses i18n subtitle when URL is empty", () => {
			const result = browserTabLabel("My Tab", "");
			expect(result.title).toBe("My Tab");
			expect(result.subtitle).toBe("Blank page");
		});

		it("trims whitespace-only titles and falls back to i18n new-tab title", () => {
			const result = browserTabLabel("   ", "");
			expect(result.title).toBe("New tab");
			expect(result.subtitle).toBe("Blank page");
		});
	});

	// ─── standard HTTP(S) URLs ───────────────────────────────────────────────
	describe("http / https URLs", () => {
		it("extracts host as subtitle for an http URL with a path", () => {
			const result = browserTabLabel("", "http://localhost:3000/dashboard");
			expect(result.subtitle).toBe("localhost:3000");
			expect(result.title).toBe("localhost:3000");
		});

		it("extracts host as subtitle for an https URL", () => {
			const result = browserTabLabel("", "https://example.com/some/path?q=1");
			expect(result.subtitle).toBe("example.com");
			expect(result.title).toBe("example.com");
		});

		it("respects a provided title over the fallback subtitle", () => {
			const result = browserTabLabel("My App", "https://myapp.dev/");
			expect(result.title).toBe("My App");
			expect(result.subtitle).toBe("myapp.dev");
		});

		it("trims whitespace from the provided title", () => {
			const result = browserTabLabel("  Trimmed  ", "https://example.com/");
			expect(result.title).toBe("Trimmed");
			expect(result.subtitle).toBe("example.com");
		});

		it("includes the port in the subtitle when port is non-standard", () => {
			const result = browserTabLabel("", "http://127.0.0.1:8080/api/health");
			expect(result.subtitle).toBe("127.0.0.1:8080");
		});
	});

	// ─── file: URIs ──────────────────────────────────────────────────────────
	describe("file: URIs", () => {
		it("extracts the last path segment as subtitle for a file URI", () => {
			const result = browserTabLabel("", "file:///Users/dev/project/index.html");
			expect(result.subtitle).toBe("index.html");
			expect(result.title).toBe("index.html");
		});

		it("extracts filename as subtitle and preserves explicit title for file URIs", () => {
			const result = browserTabLabel("Home", "file:///Users/dev/project/index.html");
			expect(result.title).toBe("Home");
			expect(result.subtitle).toBe("index.html");
		});

		it("extracts the deepest folder name when path ends with a slash", () => {
			// pathname.split("/").filter(Boolean).at(-1) returns "html"
			const result = browserTabLabel("", "file:///var/www/html/");
			expect(result.subtitle).toBe("html");
		});

		it("falls back to the raw URL when file path has no non-empty segments", () => {
			// "file:///" -> pathname "/", split("/").filter(Boolean) = [] -> .at(-1) = undefined -> url
			const result = browserTabLabel("", "file:///");
			expect(result.subtitle).toBe("file:///");
		});
	});

	// ─── other protocols ─────────────────────────────────────────────────────
	describe("other protocols", () => {
		it("uses host as subtitle for ftp URLs", () => {
			const result = browserTabLabel("", "ftp://files.example.com/pub");
			expect(result.subtitle).toBe("files.example.com");
		});
	});

	// ─── malformed / invalid URLs ────────────────────────────────────────────
	describe("malformed or invalid URLs", () => {
		it("uses the raw URL as both title and subtitle when URL is unparseable", () => {
			const result = browserTabLabel("", "invalid-url-string");
			expect(result.title).toBe("invalid-url-string");
			expect(result.subtitle).toBe("invalid-url-string");
		});

		it("respects an explicit title over the raw URL fallback for malformed URLs", () => {
			const result = browserTabLabel("Custom Title", "not a valid url");
			expect(result.title).toBe("Custom Title");
			expect(result.subtitle).toBe("not a valid url");
		});

		it("treats a bare hostname without a protocol as malformed", () => {
			const result = browserTabLabel("", "example.com");
			expect(result.title).toBe("example.com");
			expect(result.subtitle).toBe("example.com");
		});

		it("documents current behavior for javascript: URLs (host is empty string)", () => {
			// new URL("javascript:alert(1)") is valid; parsed.host === "" (non-file protocol)
			// subtitle = parsed.host = "" — title falls back to "" too since cleanTitle is ""
			const result = browserTabLabel("", "javascript:alert(1)");
			expect(result.subtitle).toBe("");
		});
	});
});
