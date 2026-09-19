import { Host } from "@expo/ui";
import { Button, Group, Image } from "@expo/ui/swift-ui";
import {
	accessibilityIdentifier,
	accessibilityLabel,
	buttonStyle,
	frame,
} from "@expo/ui/swift-ui/modifiers";
import { glassCircle } from "./glass";
import { GLASS_CIRCLE_SIZE } from "./native-header-button.ios";
import { useTheme, useThemeState } from "./ThemeProvider";
import { iconSize } from "./tokens";

export function SidebarSpawnButton({ onPress }: { onPress: () => void }) {
	const t = useTheme();
	const { scheme } = useThemeState();

	return (
		<Host style={{ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }} colorScheme={scheme}>
			{/* Material on a group we size, so the circle cannot take its size from
			    the glyph inside it — see the note in native-header-button.ios.tsx. */}
			<Group modifiers={[frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }), glassCircle()]}>
				<Button
					onPress={onPress}
					modifiers={[
						buttonStyle("plain"),
						frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }),
						accessibilityLabel("Spawn worker"),
						accessibilityIdentifier("sidebar-spawn-worker"),
					]}
				>
					<Image systemName="plus" size={iconSize.lg} color={t.textSecondary} />
				</Button>
			</Group>
		</Host>
	);
}
