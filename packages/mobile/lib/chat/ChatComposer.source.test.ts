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
		expect(pill).toContain("borderRadius: 28");
		expect(pill).not.toContain("borderWidth");
		expect(pill).not.toContain("borderColor");
		expect(pill).toContain("backgroundColor: t.bgElevated");
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
