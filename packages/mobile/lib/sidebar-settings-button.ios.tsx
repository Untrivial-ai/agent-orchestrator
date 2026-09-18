import { Host } from "@expo/ui";
import { Button, Image } from "@expo/ui/swift-ui";
import {
	accessibilityIdentifier,
	accessibilityLabel,
	frame,
	tint,
} from "@expo/ui/swift-ui/modifiers";
import { glassCircle } from "./glass";
import { useTheme, useThemeState } from "./ThemeProvider";
import { iconSize, type } from "./tokens";

export function SidebarSettingsButton({ active, onPress }: { active: boolean; onPress: () => void }) {
	const t = useTheme();
	const { scheme } = useThemeState();

	return (
		<Host style={{ width: 48, height: 48 }} colorScheme={scheme}>
			<Button
				onPress={onPress}
				modifiers={[
					frame({ width: 48, height: 48 }),
					glassCircle(active ? t.accentTint : undefined),
					tint(active ? t.accent : t.textSecondary),
					accessibilityLabel("Settings"),
					accessibilityIdentifier("sidebar-settings"),
				]}
			>
				<Image systemName="gearshape" size={iconSize.lg} color={active ? t.accent : t.textSecondary} />
			</Button>
		</Host>
	);
}
