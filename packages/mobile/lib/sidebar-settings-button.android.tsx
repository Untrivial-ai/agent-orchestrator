import { Feather } from "./icons";
import { Pressable, StyleSheet } from "react-native";
import { useTheme } from "./ThemeProvider";
import { iconSize, radius } from "./tokens";

export function SidebarSettingsButton({ active, onPress }: { active: boolean; onPress: () => void }) {
	const t = useTheme();
	return (
		<Pressable
			testID="sidebar-settings"
			accessibilityRole="button"
			accessibilityLabel="Settings"
			android_ripple={{ color: t.accentTint, borderless: true, radius: 24 }}
			onPress={onPress}
			style={({ pressed }) => [
				styles.button,
				{
					backgroundColor: pressed ? t.accentTint : t.bgElevatedHover,
					borderColor: active ? t.accent : t.borderStrong,
				},
			]}
		>
			<Feather name="settings" size={iconSize.xl} color={active ? t.accent : t.textSecondary} />
		</Pressable>
	);
}

const styles = StyleSheet.create({
	button: {
		width: 44,
		height: 44,
		// A circle, like the spawn button beside it and the glass circles on iOS.
		// At 20 the pair read as two different controls.
		borderRadius: radius.pill, borderCurve: "continuous",
		borderWidth: StyleSheet.hairlineWidth,
		alignItems: "center",
		justifyContent: "center",
		overflow: "hidden",
	},
});
