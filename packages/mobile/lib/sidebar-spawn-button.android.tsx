import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet } from "react-native";
import { useTheme } from "./ThemeProvider";
import { iconSize, radius, type } from "./tokens";

export function SidebarSpawnButton({ onPress }: { onPress: () => void }) {
	const t = useTheme();
	return (
		<Pressable
			testID="sidebar-spawn-worker"
			accessibilityRole="button"
			accessibilityLabel="Spawn worker"
			android_ripple={{ color: t.accentTint, borderless: true, radius: 24 }}
			onPress={onPress}
			style={({ pressed }) => [
				styles.button,
				{ backgroundColor: pressed ? t.accentTint : t.bgElevated, borderColor: t.borderDefault },
			]}
		>
			<Feather name="plus" size={iconSize.xl} color={t.textSecondary} />
		</Pressable>
	);
}

const styles = StyleSheet.create({
	button: {
		width: 44,
		height: 44,
		borderRadius: radius.pill, borderCurve: "continuous",
		borderWidth: StyleSheet.hairlineWidth,
		alignItems: "center",
		justifyContent: "center",
		overflow: "hidden",
	},
});
