import { describe, expect, it } from "vitest";
import { sanitizeRendererCapture } from "./sentry-main";

describe("sanitizeRendererCapture", () => {
	it("scrubs local paths and loopback URLs at the Electron-main boundary", () => {
		const input = {
			consentGeneration: "generation-1",
			kind: "exception",
			level: "error",
			message: "failed at http://127.0.0.1:3001/api and C:\\Users\\alice\\repo\\file.ts",
			tags: {
				operation: "GET http://localhost:3001/api/v1/sessions",
				category: "file:///home/alice/private.txt",
			},
		} as const;

		const sanitized = sanitizeRendererCapture(input);

		expect(sanitized).toEqual({
			consentGeneration: "generation-1",
			kind: "exception",
			level: "error",
			message: "failed at [redacted-url] and [redacted-path]",
			tags: {
				operation: "GET [redacted-url]",
				category: "[redacted-url]",
			},
		});
		expect(input.message).toContain("C:\\Users\\alice");
	});

	it("redacts full paths containing spaces or delimiter-like text", () => {
		const base = { consentGeneration: "generation-1", kind: "exception" } as const;

		expect(sanitizeRendererCapture({ ...base, message: "open /Users/alice/build error logs/x.ts" })?.message).toBe(
			"open [redacted-path]",
		);
		expect(sanitizeRendererCapture({ ...base, message: "open /home/alice/a: b/c.md" })?.message).toBe(
			"open [redacted-path]",
		);
		expect(sanitizeRendererCapture({ ...base, message: "open C:\\Users\\alice\\My Documents\\secret.txt" })?.message).toBe(
			"open [redacted-path]",
		);
		expect(sanitizeRendererCapture({ ...base, message: "open file:///home/alice/My Notes/private.txt" })?.message).toBe(
			"open [redacted-url]",
		);
		expect(sanitizeRendererCapture({ ...base, message: "load app://renderer/My Project/index.html" })?.message).toBe(
			"load [redacted-url]",
		);
	});

	it("rejects unknown fields, tags, kinds, and levels", () => {
		const base = { consentGeneration: "generation-1", kind: "message", message: "safe" };
		expect(sanitizeRendererCapture({ ...base, rawError: "secret" })).toBeNull();
		expect(sanitizeRendererCapture({ ...base, tags: { request_id: "identifier" } })).toBeNull();
		expect(sanitizeRendererCapture({ ...base, kind: "trace" })).toBeNull();
		expect(sanitizeRendererCapture({ ...base, level: "debug" })).toBeNull();
	});
});
