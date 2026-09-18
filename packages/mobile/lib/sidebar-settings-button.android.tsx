import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet } from "react-native";
import { useTheme } from "./ThemeProvider";

export function SidebarSettingsButton({ active, onPress }: { active: boolean; onPress: () => void }) {
	const t = useTheme();
	return (
		<Pressable
			testID="sidebar-settings"
			accessibilityRole="button"
			accessibilityLabel="Settings"
			android_ripple={{ color: t.tintBlue, borderless: true, radius: 24 }}
			onPress={onPress}
			style={({ pressed }) => [
				styles.button,
				{
					backgroundColor: active || pressed ? t.tintBlue : t.bgElevated,
					borderColor: active ? t.blue : t.borderDefault,
				},
			]}
		>
			<Feather name="settings" size={23} color={active ? t.blue : t.textSecondary} />
		</Pressable>
	);
}

const styles = StyleSheet.create({
	button: {
		width: 48,
		height: 48,
		borderRadius: 24,
		borderWidth: StyleSheet.hairlineWidth,
		alignItems: "center",
		justifyContent: "center",
		overflow: "hidden",
	},
});
