import { Host } from "@expo/ui";
import { Button, Image } from "@expo/ui/swift-ui";
import {
	accessibilityIdentifier,
	accessibilityLabel,
	frame,
	glassEffect,
	tint,
} from "@expo/ui/swift-ui/modifiers";
import { useTheme, useThemeState } from "./ThemeProvider";

export function SidebarSpawnButton({ onPress }: { onPress: () => void }) {
	const t = useTheme();
	const { scheme } = useThemeState();

	return (
		<Host style={{ width: 48, height: 48 }} colorScheme={scheme}>
			<Button
				onPress={onPress}
				modifiers={[
					frame({ width: 48, height: 48 }),
					glassEffect({ glass: { variant: "regular", interactive: true }, shape: "circle" }),
					tint(t.textSecondary),
					accessibilityLabel("Spawn worker"),
					accessibilityIdentifier("sidebar-spawn-worker"),
				]}
			>
				<Image systemName="plus" size={20} color={t.textSecondary} />
			</Button>
		</Host>
	);
}
