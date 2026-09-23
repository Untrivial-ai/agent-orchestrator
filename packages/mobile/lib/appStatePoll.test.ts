import { describe, expect, it } from "vitest";
import { shouldPoll } from "./appStatePoll";

describe("shouldPoll", () => {
	it("polls while the app is active", () => {
		expect(shouldPoll("active")).toBe(true);
	});

	it("keeps polling through the iOS inactive blip", () => {
		// Control Centre, the app switcher, and a Face ID prompt all report
		// "inactive". Treating it as offline would flicker the desktop dot.
		expect(shouldPoll("inactive")).toBe(true);
	});

	it("stops polling once backgrounded", () => {
		expect(shouldPoll("background")).toBe(false);
	});
});
