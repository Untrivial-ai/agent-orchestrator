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
 * The material is drawn as one rounded rect at a fixed corner radius, and it
 * sizes itself to the space it is given rather than to a height measured in JS.
 * That matters when the pill changes height — typing a second line, or the field
 * re-measuring as the keyboard closes — because a measured height that arrives a
 * frame late (or not at all) leaves the glass drawn at the old size, which reads
 * as the composer's contents coming loose from their background.
 */
export const composerGlassSupported = parseInt(String(Platform.Version), 10) >= 26;

export function ComposerGlass({ radius }: { radius: number }) {
	const t = useTheme();
	const { scheme } = useThemeState();
	if (!composerGlassSupported) return null;
	return (
		<View pointerEvents="none" style={StyleSheet.absoluteFill}>
			<Host style={StyleSheet.absoluteFill} colorScheme={scheme} seedColor={t.accent}>
				{/* `max` bounds rather than a fixed height: the frame claims whatever the
				    host has, so the material always matches the pill it sits behind. The
				    spacer is what gives that frame something to lay out — SwiftUI will
				    not size an empty group. */}
				<Group modifiers={[frame({ maxWidth: 2000, maxHeight: 2000 }), glassPanel(radius, undefined, false)]}>
					<Spacer />
				</Group>
			</Host>
		</View>
	);
}
