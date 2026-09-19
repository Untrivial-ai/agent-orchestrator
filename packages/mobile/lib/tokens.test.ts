import { describe, expect, it } from "vitest";

import { fontScaleCap, radius, space, type } from "./tokens";

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
	// Lifted verbatim from worker-list-row.tsx `title`, so a rewritten row that
	// adopts the token is a no-op rather than a silent restyle.
	it("matches the existing list-row title exactly", () => {
		expect(type.rowTitle).toEqual({
			fontSize: 16,
			lineHeight: 21,
			fontWeight: "600",
			letterSpacing: -0.15,
		});
	});

	it("matches the existing screen title exactly", () => {
		expect(type.title).toEqual({ fontSize: 26, fontWeight: "800", letterSpacing: -0.5 });
	});

	it("descends from title to micro", () => {
		const sizes = [
			type.title.fontSize,
			type.sheetTitle.fontSize,
			type.emptyTitle.fontSize,
			type.rowTitle.fontSize,
			type.body.fontSize,
			type.meta.fontSize,
			type.micro.fontSize,
		];
		expect(sizes).toEqual([...sizes].sort((a, b) => b - a));
	});

	it("gives the eyebrow its tracking, which is what separates it from micro", () => {
		expect(type.eyebrow.fontSize).toBe(type.micro.fontSize);
		expect(type.eyebrow.letterSpacing).toBe(1.2);
	});
});
