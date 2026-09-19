import { Host } from "@expo/ui";
import { Group, Image } from "@expo/ui/swift-ui";
import {
	accessibilityIdentifier,
	accessibilityLabel,
	frame,
	onTapGesture,
} from "@expo/ui/swift-ui/modifiers";
import { glassCircle } from "./glass";
import { useTheme, useThemeState } from "./ThemeProvider";
import { iconSize } from "./tokens";
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

// Every glass circle is this size. Not a preference: the four corner controls
// were three different sizes, and the reason was that a SwiftUI glass *button*
// reports an intrinsic size — it grew with its glyph, and `frame` on the button
// did not constrain the material behind it.
//
// So the material is ours now, drawn on a Group we size, with the gesture on the
// same group. Geometry comes from this file, not from the control's content.
export const GLASS_CIRCLE_SIZE = 44;
const GLYPH_SIZE = iconSize.lg;

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
		<Host style={{ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }} colorScheme={scheme} seedColor={t.accent}>
			<Group
				modifiers={[
					frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }),
					glassCircle(),
					onTapGesture(() => onPress()),
					accessibilityLabel(label),
					accessibilityIdentifier(`header-${icon}`),
				]}
			>
				<Image
					systemName={systemImage(icon)}
					size={GLYPH_SIZE}
					color={t.textSecondary}
					modifiers={[frame({ width: GLYPH_SIZE, height: GLYPH_SIZE })]}
				/>
			</Group>
		</Host>
	);
}
