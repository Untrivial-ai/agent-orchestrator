import { Image, StyleSheet, Text, View } from "react-native";
import { backdropFor, harnessInitial } from "./harnessLogo";
import { logoFor } from "./harnessLogoAssets";
import type { Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";

// Fixed chip colours, deliberately NOT theme tokens: a white mark needs
// something dark behind it on the light theme *and* the dark one, so following
// the palette would defeat the purpose.
const DARK_CHIP = "#24272e";
const LIGHT_CHIP = "#ffffff";

/**
 * A harness's brand mark.
 *
 * Sits on a chip only when the mark needs one to stay visible (see
 * `backdropFor`) — most marks are colourful enough to render bare, and a chip
 * behind every one of them would turn a calm row of logos into a row of boxes.
 */
export function AgentLogo({ harness, size = 24 }: { harness?: string | null; size?: number }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const source = logoFor(harness);
	const polarity = backdropFor(harness);

	const chip =
		polarity === "needs-dark" ? DARK_CHIP : polarity === "needs-light" ? LIGHT_CHIP : "transparent";
	// The chip is padded so the mark doesn't run to its edge; a bare mark uses
	// the full box so the two render at the same optical size.
	const inset = polarity === "neutral" ? 0 : Math.round(size * 0.16);

	return (
		<View
			style={[
				styles.box,
				{ width: size, height: size, borderRadius: Math.round(size * 0.28), borderCurve: "continuous", backgroundColor: chip },
			]}
		>
			{source ? (
				<Image
					source={source}
					// `contain`, matching desktop's object-contain: the assets have mixed
					// aspect ratios (goose is 64×65, kimi 28×28, aider 128×128) and must
					// not be stretched to square.
					resizeMode="contain"
					style={{ width: size - inset * 2, height: size - inset * 2 }}
					accessibilityIgnoresInvertColors
				/>
			) : (
				<View style={[styles.fallback, { width: size, height: size, borderRadius: Math.round(size * 0.28), borderCurve: "continuous"}]}>
					<Text style={[styles.initial, { fontSize: Math.round(size * 0.5), color: t.textSecondary }]}>
						{harnessInitial(harness)}
					</Text>
				</View>
			)}
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		box: { alignItems: "center", justifyContent: "center", overflow: "hidden" },
		fallback: {
			alignItems: "center",
			justifyContent: "center",
			backgroundColor: t.bgSubtle,
			borderWidth: StyleSheet.hairlineWidth,
			borderColor: t.borderDefault,
		},
		initial: { fontWeight: "700" },
	});
