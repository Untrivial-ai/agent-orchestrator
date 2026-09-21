import { StyleSheet, View } from "react-native";
import { useTheme } from "../ThemeProvider";

/**
 * The glass behind the idle mic, drawn in plain views off iOS.
 *
 * Android has no system glass material, so this approximates one: a translucent
 * fill that lets the composer show through, a brighter top half for the sheen,
 * and a light hairline edge. It sits behind the mic's Pressable and never takes
 * a touch — hold-to-talk and double-tap stay on the Pressable.
 */
export function MicGlass({ size, radius }: { size: number; radius: number }) {
	const t = useTheme();
	return (
		<View
			pointerEvents="none"
			style={[
				StyleSheet.absoluteFill,
				{
					width: size,
					height: size,
					borderRadius: radius,
					overflow: "hidden",
					backgroundColor: t.bgSubtle,
					borderWidth: StyleSheet.hairlineWidth,
					borderColor: t.borderStrong,
				},
			]}
		>
			<View style={{ height: size / 2, backgroundColor: t.bgSubtle }} />
		</View>
	);
}
