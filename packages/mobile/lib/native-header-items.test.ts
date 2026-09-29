import { describe, expect, it, vi } from "vitest";

// iOS 26 draws a shared glass background behind every bar-button item. Our
// controls are the material themselves, so the two stacked and the button came
// out with a second, squarer outline — the whole reason this helper exists.
let platformOS = "ios";
vi.mock("react-native", () => ({
	Platform: {
		get OS() {
			return platformOS;
		},
	},
}));

const { glassHeaderControl } = await import("./native-header-items");

const control = { type: "element" } as never;

describe("glassHeaderControl", () => {
	it("declares an iOS control as an item that hides the shared background", () => {
		platformOS = "ios";
		const options = glassHeaderControl("right", control);
		expect(options.headerRight).toBeUndefined();
		expect(options.unstable_headerRightItems?.({} as never)).toEqual([
			{ type: "custom", element: control, hidesSharedBackground: true },
		]);
	});

	it("replaces the back button rather than joining it", () => {
		platformOS = "ios";
		const options = glassHeaderControl("left", control);
		expect(options.headerLeft).toBeUndefined();
		expect(options.headerBackVisible).toBe(false);
		expect(options.unstable_headerLeftItems?.({} as never)).toHaveLength(1);
	});

	it("clears the item list when vacating a slot", () => {
		// An empty array is a list that renders nothing and outranks
		// `headerRight`, so a screen that swaps its glass control for a plain one
		// (the terminal's browser toggles) would be left with a blank bar.
		platformOS = "ios";
		const options = glassHeaderControl("right");
		expect(options.unstable_headerRightItems).toBeUndefined();
		expect(options.headerRight).toBeUndefined();
	});

	it("keeps the plain slots on Android, which has no shared background", () => {
		platformOS = "android";
		const right = glassHeaderControl("right", control);
		expect(right.headerRight?.({} as never)).toBe(control);
		expect(right.unstable_headerRightItems).toBeUndefined();
		expect(right.headerBackVisible).toBeUndefined();
		const left = glassHeaderControl("left");
		expect(left.headerLeft).toBeUndefined();
		expect(left.unstable_headerLeftItems).toBeUndefined();
	});
});
