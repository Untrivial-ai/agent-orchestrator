import { describe, expect, it } from "vitest";
import { dockInset, MIN_DOCK_INSET, screenKeyboardAvoidance } from "./keyboardInset";
import { CONTROL_KEYS } from "./keys";

describe("dockInset", () => {
	// The regression this exists for: the dock used to keep its own padding while
	// the root view was already padded by the keyboard height, so opening the
	// keyboard moved the bar twice in opposite directions.
	it("owes nothing while the keyboard is up", () => {
		expect(dockInset(336, 34)).toBe(0);
		expect(dockInset(1, 34)).toBe(0);
	});

	it("carries the home-indicator inset while the keyboard is down", () => {
		expect(dockInset(0, 34)).toBe(34);
	});

	it("falls back to a minimum on a device with no home indicator", () => {
		expect(dockInset(0, 0)).toBe(MIN_DOCK_INSET);
	});
});

describe("screenKeyboardAvoidance", () => {
	it("reserves the reported keyboard height using Android's final keyboard events", () => {
		expect(screenKeyboardAvoidance("android", 336)).toEqual({
			showEvent: "keyboardDidShow",
			hideEvent: "keyboardDidHide",
			paddingBottom: 336,
			rootStyle: { paddingBottom: 336 },
		});
	});
});

describe("CONTROL_KEYS", () => {
	// The row divides its width between exactly these, with no wrapping — a ninth
	// key would silently make them too narrow to hit.
	it("is exactly eight keys", () => {
		expect(CONTROL_KEYS).toHaveLength(8);
	});

	it("sends the bytes a PTY expects", () => {
		const seq = Object.fromEntries(CONTROL_KEYS.map((k) => [k.label, k.seq]));
		expect(seq.esc).toBe("\x1b");
		expect(seq.tab).toBe("\t");
		expect(seq["^C"]).toBe("\x03");
		expect(seq["↵"]).toBe("\r");
		// CSI cursor keys: ESC [ A/B/C/D
		expect(seq["↑"]).toBe("\x1b[A");
		expect(seq["↓"]).toBe("\x1b[B");
		expect(seq["→"]).toBe("\x1b[C");
		expect(seq["←"]).toBe("\x1b[D");
	});

	// Every one of these would be stripped by SanitizeControlChars on the REST
	// route, which is why the key row writes to the mux instead.
	it("carries only control bytes, never plain text", () => {
		// Every sequence begins with a C0 control byte (ESC, TAB, ETX, CR).
		for (const k of CONTROL_KEYS) {
			expect(k.seq.charCodeAt(0)).toBeLessThan(0x20);
		}
	});

	it("labels every key for accessibility", () => {
		for (const k of CONTROL_KEYS) expect(k.hint.length).toBeGreaterThan(0);
	});
});
