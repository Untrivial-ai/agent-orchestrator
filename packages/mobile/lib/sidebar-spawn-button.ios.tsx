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

export function SidebarSpawnButton({ onPress }: { onPress: () => void }) {
	const t = useTheme();
	const { scheme } = useThemeState();

	return (
		<Host style={{ width: 44, height: 44 }} colorScheme={scheme}>
			<Button
				onPress={onPress}
				modifiers={[
					frame({ width: 44, height: 44 }),
					glassCircle(),
					tint(t.textSecondary),
					accessibilityLabel("Spawn worker"),
					accessibilityIdentifier("sidebar-spawn-worker"),
				]}
			>
				<Image systemName="plus" size={iconSize.lg} color={t.textSecondary} />
			</Button>
		</Host>
	);
}
