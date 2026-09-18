import { Host } from "@expo/ui";
import { Button } from "@expo/ui/swift-ui";
import {
	accessibilityIdentifier,
	buttonBorderShape,
	buttonStyle,
	controlSize,
	labelStyle,
	tint,
} from "@expo/ui/swift-ui/modifiers";
import { useTheme, useThemeState } from "./ThemeProvider";
import type { NativeHeaderButtonIcon } from "./native-header-button";

const systemImage = (icon: NativeHeaderButtonIcon) =>
	icon === "menu"
		? "line.3.horizontal"
		: icon === "close"
			? "xmark"
			: icon === "check"
				? "checkmark"
				: icon === "back"
					? "chevron.left"
					: "bell";

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
		<Host style={{ width: 44, height: 44 }} colorScheme={scheme} seedColor={t.accent}>
			<Button
				label={label}
				systemImage={systemImage(icon)}
				onPress={onPress}
				modifiers={[
					buttonStyle("glass"),
					controlSize("large"),
					buttonBorderShape("circle"),
					labelStyle("iconOnly"),
					tint(t.textSecondary),
					accessibilityIdentifier(`header-${icon}`),
				]}
			/>
		</Host>
	);
}
