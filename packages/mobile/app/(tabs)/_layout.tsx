import { Stack } from "expo-router/stack";
import { SidebarNavigationShell } from "../../lib/sidebar-navigation-shell";

export default function SidebarLayout() {
	return (
		<SidebarNavigationShell>
			<Stack screenOptions={{ headerShown: false, animation: "none" }} />
		</SidebarNavigationShell>
	);
}
