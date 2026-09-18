import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet } from "react-native";
import { useTheme } from "./ThemeProvider";

export function SidebarSpawnButton({ onPress }: { onPress: () => void }) {
	const t = useTheme();
	return (
		<Pressable
			testID="sidebar-spawn-worker"
			accessibilityRole="button"
			accessibilityLabel="Spawn worker"
			android_ripple={{ color: t.tintBlue, borderless: true, radius: 24 }}
			onPress={onPress}
			style={({ pressed }) => [
				styles.button,
				{ backgroundColor: pressed ? t.tintBlue : t.bgElevated, borderColor: t.borderDefault },
			]}
		>
			<Feather name="plus" size={25} color={t.textSecondary} />
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
