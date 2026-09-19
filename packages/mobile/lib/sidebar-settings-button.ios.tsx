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

export function SidebarSettingsButton({ active, onPress }: { active: boolean; onPress: () => void }) {
	const t = useTheme();
	const { scheme } = useThemeState();

	return (
		<Host style={{ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }} colorScheme={scheme}>
			{/* Same shape rule as the other circles: the material is drawn on a group we
			    size, and the active state tints the material itself. */}
			<Group modifiers={[frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }), glassCircle(active ? t.accentTint : undefined)]}>
				<Button
					onPress={onPress}
					modifiers={[
						buttonStyle("plain"),
						frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }),
						accessibilityLabel("Settings"),
						accessibilityIdentifier("sidebar-settings"),
					]}
				>
					<Image systemName="gearshape" size={iconSize.lg} color={active ? t.accent : t.textSecondary} />
				</Button>
			</Group>
		</Host>
	);
}
