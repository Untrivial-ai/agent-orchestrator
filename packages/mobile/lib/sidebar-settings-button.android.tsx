import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet } from "react-native";
import { useTheme } from "./ThemeProvider";
import { iconSize, type } from "./tokens";

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
					backgroundColor: active || pressed ? t.accentTint : t.bgElevated,
					borderColor: active ? t.accent : t.borderDefault,
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
		borderRadius: 20, borderCurve: "continuous",
		borderWidth: StyleSheet.hairlineWidth,
		alignItems: "center",
		justifyContent: "center",
		overflow: "hidden",
	},
});
