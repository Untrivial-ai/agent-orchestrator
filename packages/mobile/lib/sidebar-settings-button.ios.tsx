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

export function SidebarSettingsButton({ active, onPress }: { active: boolean; onPress: () => void }) {
	const t = useTheme();
	const { scheme } = useThemeState();

	return (
		<Host style={{ width: 48, height: 48 }} colorScheme={scheme}>
			<Button
				onPress={onPress}
				modifiers={[
					frame({ width: 48, height: 48 }),
					glassEffect({
						glass: { variant: "regular", interactive: true, tint: active ? t.tintBlue : undefined },
						shape: "circle",
					}),
					tint(active ? t.blue : t.textSecondary),
					accessibilityLabel("Settings"),
					accessibilityIdentifier("sidebar-settings"),
				]}
			>
				<Image systemName="gearshape" size={20} color={active ? t.blue : t.textSecondary} />
			</Button>
		</Host>
	);
}
