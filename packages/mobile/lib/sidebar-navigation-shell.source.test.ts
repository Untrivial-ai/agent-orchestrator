import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("./sidebar-navigation-shell.tsx", import.meta.url), "utf8");
const androidSource = readFileSync(new URL("./sidebar-navigation-shell.android.tsx", import.meta.url), "utf8");

describe("sidebar page separation", () => {
	it("uses a border without adding a drawer shadow", () => {
		expect(source).toContain("styles.contentSurfaceOpen");
		expect(source).not.toContain("boxShadow");
		expect(source).toMatch(/contentSurfaceOpen:\s*\{[^}]*overflow:\s*\"hidden\"/s);
		expect(source).toMatch(/contentSurfaceOpen:\s*\{[^}]*borderWidth:\s*StyleSheet\.hairlineWidth/s);
	});

	it("keeps Recent Workers directly below the destination tabs", () => {
		expect(source).not.toContain("height: 294");
		expect(androidSource).not.toContain("height: 294");
	});

	it("uses the same compact drawer width on iOS and Android", () => {
		for (const shell of [source, androidSource]) {
			expect(shell).toContain("Math.min(width * 0.76, 320)");
		}
	});

	it("scales the drawer contents into place with the drawer's native animation", () => {
		for (const shell of [source, androidSource]) {
			// Both shells carried an identical copy of the AccessibilityInfo effect;
			// it now lives in useReducedMotion, which Dot consumes too, so the
			// setting reaches every animation rather than only the drawer.
			expect(shell).toContain("const reduceMotion = useReducedMotion();");
			expect(shell).not.toContain("AccessibilityInfo");
			expect(shell).toContain("const sidebarContentTransform = {");
			expect(shell).toContain("outputRange: [0.86, 1]");
			expect(shell).toContain("outputRange: [0.96, 1]");
			expect(shell).toContain("outputRange: [8, 0]");
			expect(shell).toMatch(/styles\.sidebar,\s*sidebarContentTransform/s);
		}
	});

	it("floats drawer actions above Recent Workers instead of reserving a footer", () => {
		for (const shell of [source, androidSource]) {
			expect(shell).toMatch(/sidebarActions:\s*\{[^}]*position:\s*"absolute"/s);
			expect(shell).toContain("bottom: insets.bottom + 10");
			expect(shell).toContain("paddingBottom: insets.bottom + 76");
		}
	});

	it("uses a filled pin, rather than a star, for pinned sessions in the drawer", () => {
		expect(source).toContain('<Icon name="pin.fill"');
		expect(source).toContain("rotationEffect(28)");
		expect(source).not.toContain(">★</RNText>");
		// The drawer draws the desktop's Lucide Pin now, not a FontAwesome pushpin.
		expect(androidSource).toContain('<Feather name="pin"');
		expect(androidSource).not.toContain("FontAwesome");
		expect(androidSource).toContain('rotate: "28deg"');
		expect(androidSource).not.toContain('name="star"');
	});
});
