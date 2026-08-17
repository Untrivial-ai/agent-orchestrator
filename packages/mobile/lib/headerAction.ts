import type { TextStyle, ViewStyle } from "react-native";

export const headerActionStyle: ViewStyle = {
	width: 38,
	height: 44,
	aspectRatio: 1,
	borderRadius: 22,
	overflow: "hidden",
	flexShrink: 0,
	alignItems: "center",
	justifyContent: "center",
};

// Constrain vector-icon font glyphs to a square line box. Without an explicit
// line height, the font's ascender/descender metrics shift the visible glyph.
export const headerGlyphStyle: TextStyle = {
	width: 20,
	height: 20,
	lineHeight: 20,
	textAlign: "center",
	textAlignVertical: "center",
	includeFontPadding: false,
};
