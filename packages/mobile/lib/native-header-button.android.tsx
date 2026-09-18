import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet } from "react-native";
import { useTheme } from "./ThemeProvider";
import type { NativeHeaderButtonIcon } from "./native-header-button";

const icons: Record<NativeHeaderButtonIcon, keyof typeof Feather.glyphMap> = {
	menu: "menu",
	bell: "bell",
	close: "x",
	check: "check",
	back: "chevron-left",
};

export function NativeHeaderButton({
	icon,
	label,
	onPress,
}: {
	icon: NativeHeaderButtonIcon;
	label: string;
	onPress: () => void;
}) {
	const t = useTheme();
	return (
		<Pressable
			testID={`header-${icon}`}
			accessibilityRole="button"
			accessibilityLabel={label}
			android_ripple={{ color: t.tintBlue, borderless: true, radius: 22 }}
			onPress={onPress}
			style={({ pressed }) => [
				styles.button,
				{ backgroundColor: pressed ? t.tintBlue : t.bgElevated, borderColor: t.borderDefault },
			]}
		>
			<Feather name={icons[icon]} size={22} color={t.textSecondary} />
		</Pressable>
	);
}

const styles = StyleSheet.create({
	button: {
		width: 44,
		height: 44,
		borderRadius: 22,
		borderWidth: StyleSheet.hairlineWidth,
		alignItems: "center",
		justifyContent: "center",
		overflow: "hidden",
	},
});
