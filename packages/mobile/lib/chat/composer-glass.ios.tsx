import { Host } from "@expo/ui";
import { Group, Spacer } from "@expo/ui/swift-ui";
import { frame } from "@expo/ui/swift-ui/modifiers";
import { Platform, StyleSheet, View } from "react-native";
import { glassPanel } from "../glass";
import { useTheme, useThemeState } from "../ThemeProvider";

/**
 * The chat composer's Liquid Glass, drawn natively behind the row.
 *
 * The row itself stays React Native — it holds a multiline field, the
 * suggestion handling, attachments and the delivery model — so the material
 * cannot wrap it the way `glassField` wraps the dock's search field. It is
 * layered behind instead, and the pill drops its own fill so the glass has no
 * opaque surface under it to turn grey.
 *
 * The material is drawn at the pill's resting size, passed in as a constant.
 * Nothing about it is measured or animated: a native material sized from a
 * number that moves (a measured height, or the space SwiftUI happens to propose
 * mid-animation) has twice been left behind by the keyboard's own animation and
 * drawn smaller than the pill it sits behind. The pill is one line tall by
 * design, so the material has one size to know.
 */
export const composerGlassSupported = parseInt(String(Platform.Version), 10) >= 26;

export function ComposerGlass({ height, radius }: { height: number; radius: number }) {
	const t = useTheme();
	const { scheme } = useThemeState();
	if (!composerGlassSupported) return null;
	return (
		<View pointerEvents="none" style={StyleSheet.absoluteFill}>
			<Host style={StyleSheet.absoluteFill} colorScheme={scheme} seedColor={t.accent}>
				{/* The frame comes first — the material is drawn in the space the frame
				    claims — and the spacer is what gives that frame something to lay
				    out: SwiftUI will not size an empty group. */}
				<Group modifiers={[frame({ height, maxWidth: 2000 }), glassPanel(radius, undefined, false)]}>
					<Spacer />
				</Group>
			</Host>
		</View>
	);
}
