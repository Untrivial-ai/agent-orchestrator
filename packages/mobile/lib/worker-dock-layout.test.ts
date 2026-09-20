import { describe, expect, it } from "vitest";
import { workerDockKeyboardLayout, workerDockLift, workerDockRestingBottom, workerDockVisibility, workerListBottomInset } from "./worker-dock-layout";

describe("worker dock keyboard layout", () => {
	it("anchors the controls directly above the keyboard", () => {
		expect(workerDockKeyboardLayout(336, 34, true)).toEqual({
			rootPaddingBottom: 0,
			dockBottom: 348,
			restingBottom: 46,
		});
	});

	it("keeps a keyboard gap when adjustResize has already consumed the overlap", () => {
		expect(workerDockKeyboardLayout(0, 34, true)).toEqual({
			rootPaddingBottom: 0,
			dockBottom: 12,
			restingBottom: 46,
		});
	});

	it("rests above the home indicator when the keyboard is hidden", () => {
		expect(workerDockKeyboardLayout(0, 34, false)).toEqual({
			rootPaddingBottom: 0,
			dockBottom: 46,
			restingBottom: 46,
		});
	});

	it("reserves scroll room above the floating search dock", () => {
		expect(workerListBottomInset(348)).toBe(416);
		expect(workerListBottomInset(46)).toBe(114);
	});
});

// The dock is positioned from a distance now, not a visibility flag: the flag
// turns over when the keyboard has finished hiding, which left the buttons
// hanging where the keyboard had been and then snapping back.
describe("worker dock lift", () => {
	it("is the keyboard's height above the resting line", () => {
		expect(workerDockRestingBottom(34) + workerDockLift(336, 34)).toBe(336 + 12);
	});

	it("is zero once the keyboard is at or below the resting line", () => {
		expect(workerDockLift(34, 34)).toBe(0);
		expect(workerDockLift(0, 34)).toBe(0);
	});

	it("never lifts by the home indicator alone", () => {
		expect(workerDockLift(20, 34)).toBe(0);
	});
});

describe("worker dock visibility", () => {
	it("replaces both dock actions with search while search is active", () => {
		expect(workerDockVisibility(true)).toEqual({
			showControls: false,
			showSearch: true,
			showSpawn: false,
		});
	});

	it("restores the two dock actions after search closes", () => {
		expect(workerDockVisibility(false)).toEqual({
			showControls: true,
			showSearch: false,
			showSpawn: true,
		});
	});
});
