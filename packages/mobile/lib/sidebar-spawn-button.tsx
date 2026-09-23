import { Button, Host } from "@expo/ui";
import { useTheme, useThemeState } from "./ThemeProvider";

export function SidebarSpawnButton({ onPress }: { onPress: () => void }) {
	const t = useTheme();
	const { scheme } = useThemeState();

	return (
		<Host style={{ width: 48, height: 48 }} colorScheme={scheme} seedColor={t.accent}>
			<Button
				label="+"
				onPress={onPress}
				testID="sidebar-spawn-worker"
				variant="outlined"
				style={{ width: 48, height: 48, borderRadius: 20, backgroundColor: "transparent" }}
			/>
		</Host>
	);
}
