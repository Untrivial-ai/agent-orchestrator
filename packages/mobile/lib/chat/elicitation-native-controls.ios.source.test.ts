import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("./elicitation-native-controls.ios.tsx", import.meta.url), "utf8");

describe("iOS elicitation controls", () => {
	it("keeps choices visually subordinate to the question", () => {
		expect(source).toContain('font({ size: 14, weight: "semibold" })');
		expect(source).toContain('font({ size: 12, weight: "regular" })');
		expect(source).toContain("size={iconSize.md}");
		expect(source).toContain("minHeight: 56");
	});

	it("uses the same native glass material as the worker controls", () => {
		// The recipe module is the single place the glass material, shape and
		// interactivity are decided, so this asserts the shared call and not a
		// literal that every control would otherwise have to repeat.
		expect(source).toContain("glassPanel()");
		expect(source).not.toContain("glassEffect(");
		expect(source).not.toContain("background(t.bgSubtle)");
	});

	it("keeps the answer field and actions at conversation scale", () => {
		expect(source).toContain('style={{ width: "100%", height: 46 }}');
		expect(source).toContain('frame({ maxWidth: 1000, height: 44, alignment: "leading" })');
		expect(source).toContain('font({ size: 16 })');
		expect(source).toContain('controlSize("regular")');
		expect(source).toContain("const resolvedWidth = width ?? (primary ? 76 : 58)");
		expect(source).toContain("style={{ width: resolvedWidth, height: 36 }}");
		expect(source).toContain("frame({ width: resolvedWidth, height: 36 })");
	});
});
