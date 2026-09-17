import { describe, expect, it } from "vitest";
import { dockInset, MIN_DOCK_INSET, rootKeyboardPad, screenKeyboardAvoidance } from "./keyboardInset";
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

describe("rootKeyboardPad", () => {
	// The regression this exists for: React Native's Android keyboard event
	// reports `imeInsets.bottom - systemBars.bottom` (ReactRootView.java), i.e.
	// the keyboard height with the navigation bar already subtracted. Our root
	// view runs edge-to-edge underneath that nav bar, so padding by the reported
	// height alone left the dock short by exactly the nav-bar inset - half an
	// input row under gesture nav, a whole one under 3-button nav.
	it("adds the navigation-bar inset back on Android", () => {
		expect(rootKeyboardPad("android", 336, 24)).toBe(360);
		expect(rootKeyboardPad("android", 336, 48)).toBe(384);
	});

	it("reserves only the reported height on iOS, which already spans the home indicator", () => {
		expect(rootKeyboardPad("ios", 336, 34)).toBe(336);
	});

	it("reserves nothing on either platform while the keyboard is down", () => {
		expect(rootKeyboardPad("android", 0, 48)).toBe(0);
		expect(rootKeyboardPad("ios", 0, 34)).toBe(0);
	});

	it("handles a device with the navigation bar hidden", () => {
		expect(rootKeyboardPad("android", 336, 0)).toBe(336);
	});
});

describe("screenKeyboardAvoidance", () => {
	it("reserves the reported keyboard height plus the nav bar using Android's final keyboard events", () => {
		expect(screenKeyboardAvoidance("android", 336, 48)).toEqual({
			showEvent: "keyboardDidShow",
			hideEvent: "keyboardDidHide",
			paddingBottom: 384,
			rootStyle: { paddingBottom: 384 },
		});
	});

	it("collapses to nothing when the keyboard is down", () => {
		expect(screenKeyboardAvoidance("android", 0, 48).rootStyle).toEqual({ paddingBottom: 0 });
	});
});

// The dock sits inside the root view, so the root's padding is the only place
// the keyboard is accounted for. Together they must clear the keyboard exactly.
describe("root padding and dock inset together", () => {
	it("clear the full Android keyboard occupancy without double-counting", () => {
		const kb = 336; // as reported by RN: ime inset minus nav bar
		const nav = 48;
		expect(rootKeyboardPad("android", kb, nav) + dockInset(kb, nav)).toBe(kb + nav);
	});

	it("fall back to the safe-area inset with the keyboard down", () => {
		expect(rootKeyboardPad("android", 0, 48) + dockInset(0, 48)).toBe(48);
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
