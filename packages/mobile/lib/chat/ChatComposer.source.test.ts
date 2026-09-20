import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

function source(relativePath: string): string {
	return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), "utf8");
}

const composer = source("./ChatComposer.tsx");

/** The body of one entry in the composer's stylesheet. */
function styleRule(name: string): string {
	const match = composer.match(new RegExp(`\\n\\t${name}: \\{([^}]*)\\}`));
	expect(match, `${name} rule`).not.toBeNull();
	return match?.[1] ?? "";
}

describe("chat composer pill", () => {
	// The row is one capsule: attach, field, dictation and the one filled control
	// that commits. A border plus a fill made it read as a box with buttons in it.
	it("draws a single borderless capsule", () => {
		const pill = styleRule("composer");
		expect(pill).toContain("borderRadius: COMPOSER_RADIUS");
		expect(composer).toContain("const COMPOSER_RADIUS = 28");
		expect(pill).not.toContain("borderWidth");
		expect(pill).not.toContain("borderColor");
		// No fill under the glass: an opaque pill under a material is the one
		// arrangement that turns it grey. Android, which has no material, keeps the
		// elevated fill.
		expect(pill).toContain('backgroundColor: composerGlassSupported ? "transparent" : t.bgElevated');
	});

	it("lays the glass behind the row, sized by the pill itself", () => {
		expect(composer).toContain("<ComposerGlass radius={COMPOSER_RADIUS} />");
		// No height measured in JS: a measurement that arrives late leaves the
		// material drawn at the pill's previous size, which is the composer looking
		// like its contents came loose from their background.
		expect(composer).not.toContain("pillHeight");
		// `false`: the material must not take the touch. The field is inside this
		// pill, and an interactive material only passes taps that land on its own
		// content — typing would stop working.
		expect(source("./composer-glass.ios.tsx")).toContain("glassPanel(radius, undefined, false)");
		expect(source("./composer-glass.ios.tsx")).toContain("frame({ maxWidth: 2000, maxHeight: 2000 })");
	});

	// Two discs side by side have no hierarchy; the send button is the only shape
	// that commits, so dictation is a bare glyph here.
	it("keeps dictation a glyph and the send a filled circle", () => {
		expect(composer).toContain('<MicKey variant="plain"');
		expect(styleRule("send")).toContain("borderRadius: 22");
		expect(styleRule("send")).toContain("backgroundColor: t.accent");
	});

	it("opens the row with a plus rather than a paperclip", () => {
		expect(source("./ChatAttachmentMenu.tsx")).toContain('name="plus"');
	});
});
