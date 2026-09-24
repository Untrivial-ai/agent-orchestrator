import { describe, expect, it } from "vitest";

import { fontScaleCap, microLabel, radius, space, type } from "./tokens";

// These tests are a regression fence, not a description of taste. A scale that
// stops ascending has been edited carelessly, and the anchors below are the
// values rewritten components are expected to land on — if one changes, the
// change should be deliberate enough to update the test.

describe("spacing scale", () => {
	it("ascends", () => {
		const steps = [space.xs, space.sm, space.md, space.lg, space.xl, space.xxl];
		expect(steps).toEqual([...steps].sort((a, b) => a - b));
		expect(new Set(steps).size).toBe(steps.length);
	});

	it("keeps the card gutter at the value cardShell already uses", () => {
		expect(space.md).toBe(12);
	});
});

describe("radius scale", () => {
	it("ascends", () => {
		const steps = [radius.sm, radius.md, radius.lg, radius.xl];
		expect(steps).toEqual([...steps].sort((a, b) => a - b));
		expect(new Set(steps).size).toBe(steps.length);
	});

	it("keeps the card radius at the value cardShell already uses", () => {
		expect(radius.md).toBe(12);
	});
});

describe("font scale caps", () => {
	it("lets text grow more as its job gets more important", () => {
		expect(fontScaleCap.chrome).toBeLessThan(fontScaleCap.title);
		expect(fontScaleCap.title).toBeLessThan(fontScaleCap.body);
	});

	// A cap tight enough to defeat the accessibility setting is worse than a
	// layout that bends, so nothing may be capped below 1.3.
	it("never caps so tightly that large type stops working", () => {
		for (const cap of Object.values(fontScaleCap)) {
			expect(cap).toBeGreaterThanOrEqual(1.3);
		}
	});
});

describe("type scale", () => {
	// The ramp is Apple's Dynamic Type scale rather than a vocabulary of
	// per-screen sizes, so the fence is its shape: sizes never decrease as the
	// roles get larger, and the anchors below are the steps the app reads at.
	const steps = [
		type.caption2,
		type.caption1,
		type.footnote,
		type.subheadline,
		type.callout,
		type.body,
		type.headline,
		type.title3,
		type.title2,
		type.title1,
		type.largeTitle,
	];

	it("never decreases as the roles get larger", () => {
		const sizes = steps.map((step) => step.fontSize);
		expect(sizes).toEqual([...sizes].sort((a, b) => a - b));
	});

	it("spans the scale the system uses at its ends", () => {
		expect(type.caption2.fontSize).toBe(11);
		expect(type.largeTitle.fontSize).toBe(34);
	});

	it("keeps the reading sizes where the app reads at them", () => {
		expect(type.body.fontSize).toBe(17);
		expect(type.footnote.fontSize).toBe(13);
	});

	it("gives micro-labels positive tracking and large titles negative", () => {
		// Optical tracking flips sign at the ends of the ramp; that, not the size,
		// is what makes a small label read as a label.
		expect(microLabel.letterSpacing).toBeGreaterThan(0);
		expect(type.body.letterSpacing).toBeLessThan(0);
	});
});
