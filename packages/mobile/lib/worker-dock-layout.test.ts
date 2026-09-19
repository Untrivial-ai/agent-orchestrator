import { describe, expect, it } from "vitest";
import { workerDockKeyboardLayout, workerDockVisibility, workerListBottomInset } from "./worker-dock-layout";

describe("worker dock keyboard layout", () => {
	it("anchors the controls directly above the keyboard", () => {
		expect(workerDockKeyboardLayout(336, 34, true)).toEqual({
			rootPaddingBottom: 0,
			dockBottom: 348,
		});
	});

	it("keeps a keyboard gap when adjustResize has already consumed the overlap", () => {
		expect(workerDockKeyboardLayout(0, 34, true)).toEqual({
			rootPaddingBottom: 0,
			dockBottom: 12,
		});
	});

	it("rests above the home indicator when the keyboard is hidden", () => {
		expect(workerDockKeyboardLayout(0, 34, false)).toEqual({
			rootPaddingBottom: 0,
			dockBottom: 46,
		});
	});

	it("reserves scroll room above the floating search dock", () => {
		expect(workerListBottomInset(348)).toBe(416);
		expect(workerListBottomInset(46)).toBe(114);
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
