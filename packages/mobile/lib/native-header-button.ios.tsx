import { Host } from "@expo/ui";
import { Button } from "@expo/ui/swift-ui";
import {
	accessibilityIdentifier,
	buttonBorderShape,
	buttonStyle,
	controlSize,
	font,
	frame,
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
					// Pinned, not left to the control size: the bell glyph is taller than
					// the hamburger, and an intrinsic-size circle grew with it — so the
					// notifications button drew visibly bigger than the menu button.
					frame({ width: 44, height: 44 }),
					// One symbol size for every header glyph. Left to the control, the bell
					// draws optically heavier than the three-line menu icon even inside an
					// identical circle.
					font({ size: 17, weight: "semibold" }),
					labelStyle("iconOnly"),
					tint(t.textSecondary),
					accessibilityIdentifier(`header-${icon}`),
				]}
			/>
		</Host>
	);
}
