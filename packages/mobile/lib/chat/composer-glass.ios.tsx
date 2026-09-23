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
 * It fills the space it is given rather than being handed a height. Sizing it from
 * a number that moves — a measured height, or the space SwiftUI happens to propose
 * mid-animation — has twice left it drawn smaller than the pill behind it, which
 * is why it was pinned to a constant for a while. Filling needs no number at all:
 * the pill owns its height, the host fills the pill, and the material fills the
 * host, so the field can grow under a long message and the material grows with it.
 */
export const composerGlassSupported = parseInt(String(Platform.Version), 10) >= 26;

export function ComposerGlass({ radius }: { radius: number }) {
	const t = useTheme();
	const { scheme } = useThemeState();
	if (!composerGlassSupported) return null;
	return (
		<View pointerEvents="none" style={StyleSheet.absoluteFill}>
			<Host style={StyleSheet.absoluteFill} colorScheme={scheme} seedColor={t.accent}>
				{/* The frame comes first — the material is drawn in the space the frame
				    claims — and the spacer is what gives that frame something to lay
				    out: SwiftUI will not size an empty group. No height: the host already
				    fills the pill, so an unbounded frame resolves to exactly that. */}
				<Group modifiers={[frame({ maxWidth: 2000, maxHeight: 2000 }), glassPanel(radius, undefined, false)]}>
					<Spacer />
				</Group>
			</Host>
		</View>
	);
}
