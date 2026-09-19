import { describe, expect, it } from "vitest";
import { headerActionStyle, headerGlyphStyle } from "./headerAction";
import { radius } from "./tokens";

describe("native header actions", () => {
	it("centers the glyph in the constrained native header target", () => {
		expect(headerActionStyle).toMatchObject({ width: 44, height: 44, aspectRatio: 1, borderRadius: radius.pill, overflow: "hidden", alignItems: "center", justifyContent: "center" });
	});

	it("does not draw a second surface over the native header material", () => {
		expect(headerActionStyle).not.toHaveProperty("borderWidth");
		expect(headerActionStyle).not.toHaveProperty("borderColor");
		expect(headerActionStyle).not.toHaveProperty("backgroundColor");
	});

	it("centers the glyph in a fixed inner square", () => {
		expect(headerGlyphStyle).toMatchObject({
			width: 20,
			height: 20,
			lineHeight: 20,
			textAlign: "center",
			textAlignVertical: "center",
			includeFontPadding: false,
		});
	});
});
