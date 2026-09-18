import { Button, Host } from "@expo/ui";
import { useTheme, useThemeState } from "./ThemeProvider";

export type NativeHeaderButtonIcon = "menu" | "bell" | "close" | "check" | "back";

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
	const { scheme } = useThemeState();
	return (
		<Host style={{ width: 44, height: 44 }} colorScheme={scheme} seedColor={t.blue}>
			<Button
				label={icon === "menu" ? "☰" : icon === "close" ? "×" : icon === "check" ? "✓" : icon === "back" ? "‹" : "◉"}
				onPress={onPress}
				variant="outlined"
				testID={`header-${icon}`}
				style={{ width: 44, height: 44, borderRadius: 22 }}
			/>
		</Host>
	);
}
