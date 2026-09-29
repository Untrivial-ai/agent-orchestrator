import { Pressable, StyleSheet, Text, View } from "react-native";
import type { Theme } from "../theme";
import { haptics } from "../haptics";
import { CONTROL_KEYS } from "./keys";
import { useThemedStyles } from "../ThemeProvider";
import { space, type } from "../tokens";

// The control-key row: one fixed row of eight keys that divide the width
// between them.
//
// It used to be a `flexWrap` row that also held zoom, the mic and two mode
// toggles, two of which carried `marginLeft: "auto"` — so turning a toggle on
// reflowed the whole second line. Fixed count, fixed height, `flex: 1` each:
// this row is pixel-identical in every state the screen can be in.
export function KeyRow({ onKey }: { onKey: (seq: string) => void }) {
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.row}>
			{CONTROL_KEYS.map((k) => (
				<Pressable
					key={k.label}
					accessibilityRole="button"
					accessibilityLabel={k.hint}
					onPress={() => {
						haptics.tap();
						onKey(k.seq);
					}}
					style={({ pressed }) => [styles.key, pressed && styles.keyPressed]}
				>
					{/* One line, always: eight keys share the width, and at large text sizes
					    two-line labels ("es / c") broke the row's fixed geometry. Scaling is
					    capped and the label shrinks to fit rather than wrapping. */}
					<Text
						style={styles.keyText}
						numberOfLines={1}
						maxFontSizeMultiplier={1.4}
						adjustsFontSizeToFit
						minimumFontScale={0.6}
					>
						{k.label}
					</Text>
				</Pressable>
			))}
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
	row: {
		flexDirection: "row",
		gap: space.xxs,
		paddingHorizontal: space.sm,
		paddingVertical: space.xs,
	},
	key: {
		flex: 1,
		backgroundColor: t.bgElevated,
		borderWidth: 1,
		borderColor: t.borderDefault,
		borderRadius: 8, borderCurve: "continuous",
		paddingVertical: space.sm,
		alignItems: "center",
		justifyContent: "center",
	},
	keyPressed: { backgroundColor: t.accentTint, borderColor: t.accent },
	keyText: { color: t.textPrimary, fontFamily: t.fontMono, fontSize: type.subheadline.fontSize },
});
