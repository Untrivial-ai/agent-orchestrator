import { describe, expect, it } from "vitest";

import {
	BREATHE_MS,
	DRAWER_SPRING,
	motionDurations,
	shouldAnimateLayout,
	shouldBreathe,
} from "./motion";

describe("motionDurations", () => {
	it("zeroes every duration under reduced motion", () => {
		expect(motionDurations(true)).toEqual({
			breathe: 0,
			layout: 0,
			banner: 0,
			crossfade: 0,
			tint: 0,
		});
	});

	it("keeps the dot's existing 1200ms pulse when motion is allowed", () => {
		expect(motionDurations(false).breathe).toBe(BREATHE_MS);
		expect(BREATHE_MS).toBe(1200);
	});

	it("keeps every animation short enough to read through", () => {
		const durations = motionDurations(false);
		for (const [name, ms] of Object.entries(durations)) {
			if (name === "breathe") continue; // a loop, not a transition
			expect(ms).toBeLessThanOrEqual(250);
			expect(ms).toBeGreaterThan(0);
		}
	});
});

describe("reduce-motion decisions", () => {
	it("stops layout transitions", () => {
		expect(shouldAnimateLayout(false)).toBe(true);
		expect(shouldAnimateLayout(true)).toBe(false);
	});

	// Regression: a zero-duration loop is a busy loop, so the breathing dot has to
	// be prevented from starting rather than sped up to nothing.
	it("never starts the breathing loop under reduced motion", () => {
		expect(shouldBreathe(false, true)).toBe(true);
		expect(shouldBreathe(true, true)).toBe(false);
		expect(shouldBreathe(false, false)).toBe(false);
		expect(shouldBreathe(true, false)).toBe(false);
	});
});

describe("drawer spring", () => {
	// Lifted from the existing shell; a change here changes how the drawer feels.
	it("matches the shell's existing spring", () => {
		expect(DRAWER_SPRING).toEqual({ damping: 24, stiffness: 240, mass: 0.8 });
	});
});
