import { describe, expect, it, vi } from "vitest";
import { bridgeResult, browserCommandScript, parseBrowserBridgeMessage, parseBrowserContentAppearance } from "./mobileBrowserBridge";

function runPageGet(property: "url" | "title" | "text", bodyText = "Example body") {
	let posted: string | undefined;
	const location = { href: "https://example.com/current" };
	const document = {
		title: "Example title",
		body: { innerText: bodyText, textContent: bodyText },
	};
	const window = {
		ReactNativeWebView: {
			postMessage(raw: string) {
				posted = raw;
			},
		},
	};
	const script = browserCommandScript({
		type: "command",
		requestId: "get-1",
		sessionId: "s1",
		action: "get",
		args: { property },
	});
	new Function("window", "document", "location", script)(window, document, location);
	return posted ? parseBrowserBridgeMessage(posted) : undefined;
}

function runElementGet(property: "text" | "value" | "checked") {
	let posted: string | undefined;
	const element = {
		tagName: "INPUT",
		type: "text",
		innerText: "Visible label",
		textContent: "Visible label",
		value: "input value",
		checked: true,
		isConnected: true,
		getBoundingClientRect: () => ({ width: 100, height: 24 }),
		getAttribute: () => null,
		matches: () => true,
	};
	const location = { href: "https://example.com/current" };
	const document = {
		title: "Example title",
		body: { innerText: "Example body", textContent: "Example body" },
		querySelectorAll: () => [element],
	};
	const window = {
		getComputedStyle: () => ({ visibility: "visible", display: "block" }),
		ReactNativeWebView: {
			postMessage(raw: string) {
				posted = raw;
			},
		},
	};
	const run = (action: string, args: Record<string, unknown>) => {
		const script = browserCommandScript({ type: "command", requestId: action, sessionId: "s1", action, args });
		new Function("window", "document", "location", script)(window, document, location);
	};
	run("snapshot", { interactive: true });
	run("get", { property, ref: "e1" });
	return posted ? parseBrowserBridgeMessage(posted) : undefined;
}

describe("mobile browser bridge protocol", () => {
	it("embeds commands as JSON rather than executable text", () => {
		const script = browserCommandScript({
			type: "command",
			requestId: "r1",
			sessionId: "s1",
			action: "fill",
			args: { ref: "e1", value: "'); alert(1); ('" },
		});
		expect(script).toContain("window.__aoMobileBrowserBridge.run(");
		expect(script).toContain(JSON.stringify("'); alert(1); ('"));
	});

	it("returns structured ref metadata for one-command act matching", () => {
		expect(browserCommandScript({ type: "command", requestId: "r1", sessionId: "s1", action: "snapshot" }))
			.toContain("refs: refInfo");
	});

	it("never exposes password values as snapshot names", () => {
		let posted: string | undefined;
		const passwordElement = (labels: Array<{ textContent: string }>, value: string) => ({
			tagName: "INPUT",
			type: "password",
			value,
			labels,
			isConnected: true,
			getBoundingClientRect: () => ({ width: 100, height: 24 }),
			getAttribute: () => null,
			matches: () => true,
		});
		const labeled = passwordElement([{ textContent: "Account password" }], "secret-one");
		const unlabeled = passwordElement([], "secret-two");
		const window = {
			getComputedStyle: () => ({ visibility: "visible", display: "block" }),
			ReactNativeWebView: {
				postMessage(raw: string) {
					posted = raw;
				},
			},
		};
		const document = { querySelectorAll: () => [labeled, unlabeled] };
		const script = browserCommandScript({
			type: "command",
			requestId: "snapshot-passwords",
			sessionId: "s1",
			action: "snapshot",
			args: { interactive: true },
		});
		new Function("window", "document", "location", script)(window, document, { href: "https://example.com" });

		const result = parseBrowserBridgeMessage(posted ?? "")?.result;
		expect(result?.text).toContain('textbox "Account password" [ref=e1]');
		expect(result?.text).toContain('textbox "Password" [ref=e2]');
		expect(JSON.stringify(result)).not.toContain("secret-one");
		expect(JSON.stringify(result)).not.toContain("secret-two");
	});

	it("returns only the requested get property in the CLI value shape", () => {
		expect(runPageGet("url")?.result).toEqual({ value: "https://example.com/current" });
		expect(runPageGet("title")?.result).toEqual({ value: "Example title" });
		expect(runElementGet("text")?.result).toEqual({ value: "Visible label" });
		expect(runElementGet("value")?.result).toEqual({ value: "input value" });
		expect(runElementGet("checked")?.result).toEqual({ value: true });
	});

	it("keeps page reads at the 20,000-character limit instead of snapshot-label length", () => {
		const body = "word ".repeat(6_000);
		const value = runPageGet("text", body)?.result?.value;
		expect(typeof value).toBe("string");
		expect((value as string).length).toBe(20_000);
		expect((value as string).length).toBeGreaterThan(240);
	});

	it("reports native key presses as unsupported instead of claiming synthetic success", () => {
		let posted: string | undefined;
		const window = {
			ReactNativeWebView: {
				postMessage(raw: string) {
					posted = raw;
				},
			},
		};
		const script = browserCommandScript({
			type: "command",
			requestId: "press-1",
			sessionId: "s1",
			action: "press",
			args: { key: "Control+A" },
		});
		new Function("window", "document", "location", script)(window, {}, { href: "https://example.com" });
		const message = posted ? parseBrowserBridgeMessage(posted) : undefined;
		expect(message && bridgeResult(message)).toEqual({
			ok: false,
			error: {
				code: "BROWSER_ACTION_UNSUPPORTED",
				message: "Native key presses are not available on the mobile browser surface.",
			},
		});
	});

	it("types at the input selection and replaces selected text", () => {
		let posted: string | undefined;
		class FakeInput {
			tagName = "INPUT";
			type = "text";
			isConnected = true;
			selectionStart = 1;
			selectionEnd = 4;
			private currentValue = "hello";
			get value() { return this.currentValue; }
			set value(value: string) { this.currentValue = value; }
			focus() {}
			setSelectionRange(start: number, end: number) {
				this.selectionStart = start;
				this.selectionEnd = end;
			}
			dispatchEvent() { return true; }
			getBoundingClientRect() { return { width: 100, height: 24 }; }
			getAttribute() { return null; }
			matches() { return true; }
		}
		class FakeTextArea extends FakeInput {}
		const element = new FakeInput();
		const window = {
			getComputedStyle: () => ({ visibility: "visible", display: "block" }),
			ReactNativeWebView: {
				postMessage(raw: string) {
					posted = raw;
				},
			},
		};
		const document = {
			querySelectorAll: () => [element],
		};
		vi.stubGlobal("HTMLInputElement", FakeInput);
		vi.stubGlobal("HTMLTextAreaElement", FakeTextArea);
		try {
			const run = (action: string, args: Record<string, unknown>) => {
				const script = browserCommandScript({ type: "command", requestId: action, sessionId: "s1", action, args });
				new Function("window", "document", "location", script)(window, document, { href: "https://example.com" });
			};
			run("snapshot", { interactive: true });
			run("type", { ref: "e1", text: "X" });

			expect(element.value).toBe("hXo");
			expect([element.selectionStart, element.selectionEnd]).toEqual([2, 2]);
			expect(parseBrowserBridgeMessage(posted ?? "")?.result).toEqual({ typed: "e1" });
		} finally {
			vi.unstubAllGlobals();
		}
	});

	it("resets the DOM-stable quiet interval whenever the document mutates", async () => {
		vi.useFakeTimers();
		let posted: string | undefined;
		let mutated: (() => void) | undefined;
		const disconnect = vi.fn();
		vi.stubGlobal("MutationObserver", class {
			constructor(callback: () => void) {
				mutated = callback;
			}
			observe() {}
			disconnect() {
				disconnect();
			}
		});
		try {
			const window = {
				ReactNativeWebView: {
					postMessage(raw: string) {
						posted = raw;
					},
				},
			};
			const document = {
				documentElement: {},
				body: { innerText: "" },
			};
			const script = browserCommandScript({
				type: "command",
				requestId: "wait-1",
				sessionId: "s1",
				action: "wait",
				args: { stableMs: 100, timeoutMs: 1_000 },
			});
			new Function("window", "document", "location", script)(window, document, { href: "https://example.com" });

			await vi.advanceTimersByTimeAsync(90);
			mutated?.();
			await vi.advanceTimersByTimeAsync(20);
			expect(posted).toBeUndefined();

			await vi.advanceTimersByTimeAsync(90);
			expect(parseBrowserBridgeMessage(posted ?? "")?.result).toEqual({
				matched: true,
				url: "https://example.com",
			});
			expect(disconnect).toHaveBeenCalledOnce();
		} finally {
			vi.useRealTimers();
			vi.unstubAllGlobals();
		}
	});

	it("accepts only tagged correlated results", () => {
		expect(parseBrowserBridgeMessage("{}")).toBeUndefined();
		const message = parseBrowserBridgeMessage(JSON.stringify({
			__aoMobileBrowser: true,
			requestId: "r1",
			ok: true,
			result: { text: "Save" },
		}));
		expect(message?.requestId).toBe("r1");
		expect(message && bridgeResult(message)).toEqual({ ok: true, result: { text: "Save" } });
	});

	it("preserves structured command failures", () => {
		const message = parseBrowserBridgeMessage(JSON.stringify({
			__aoMobileBrowser: true,
			requestId: "r2",
			ok: false,
			error: { code: "STALE_REFERENCE", message: "snapshot again" },
		}));
		expect(message && bridgeResult(message)).toEqual({
			ok: false,
			error: { code: "STALE_REFERENCE", message: "snapshot again" },
		});
	});

	it("accepts only valid page appearance reports", () => {
		expect(parseBrowserContentAppearance(JSON.stringify({ __aoMobileBrowserAppearance: "light" }))).toBe("light");
		expect(parseBrowserContentAppearance(JSON.stringify({ __aoMobileBrowserAppearance: "dark" }))).toBe("dark");
		expect(parseBrowserContentAppearance(JSON.stringify({ __aoMobileBrowserAppearance: "sepia" }))).toBeUndefined();
	});
});
