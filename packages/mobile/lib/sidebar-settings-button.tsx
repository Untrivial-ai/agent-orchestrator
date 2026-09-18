import { Host, Row } from "@expo/ui";
import { SidebarDestinationIcon } from "./sidebar-destination-icon";
import { useTheme, useThemeState } from "./ThemeProvider";

export function SidebarSettingsButton({ active, onPress }: { active: boolean; onPress: () => void }) {
	const t = useTheme();
	const { scheme } = useThemeState();

	return (
		<Host style={{ width: 48, height: 48 }} colorScheme={scheme} seedColor={t.blue}>
			<Row
				alignment="center"
				onPress={onPress}
				testID="sidebar-settings"
				style={{
					width: 48,
					height: 48,
					borderRadius: 24,
					backgroundColor: active ? t.tintBlue : "transparent",
				}}
			>
				<SidebarDestinationIcon
					destination={{ id: "settings", label: "Settings", icon: "settings", href: "/settings" }}
					color={active ? t.blue : t.textSecondary}
				/>
			</Row>
		</Host>
	);
}
