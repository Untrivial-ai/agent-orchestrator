import type { TextStyle, ViewStyle } from "react-native";
import { radius } from "./tokens";

export const headerActionStyle: ViewStyle = {
	// 44, matching every other glass circle in the app — this used to be 38×44,
	// which made the Android header button an oval next to round ones.
	width: 44,
	height: 44,
	aspectRatio: 1,
	// Half the 44pt height, expressed as the pill value so the target stays a
	// true circle if either dimension ever changes.
	borderRadius: radius.pill, borderCurve: "continuous",
	overflow: "hidden",
	flexShrink: 0,
	alignItems: "center",
	justifyContent: "center",
};

// Constrain vector-icon font glyphs to a square line box. Without an explicit
// line height, the font's ascender/descender metrics shift the visible glyph.
export const headerGlyphStyle: TextStyle = { fontFamily: "Geist_400Regular",
	width: 20,
	height: 20,
	lineHeight: 20,
	textAlign: "center",
	textAlignVertical: "center",
	includeFontPadding: false,
};
