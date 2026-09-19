import { Icon, RNHostView } from "@expo/ui";
import { View } from "react-native";
import type { SFSymbol } from "sf-symbols-typescript";
import { NotificationTypeIcon } from "./notification-type-icon";
import { WorkersIcon } from "./workers-icon";
import type { SidebarDestination, SidebarDestinationId } from "./sidebar-navigation";

/**
 * Outline when idle, filled when selected — the iOS tab-bar idiom, which the
 * drawer was not using. Workers was `bolt.horizontal.circle`, which reads as
 * throughput rather than agents.
 */
const symbols: Record<Exclude<SidebarDestinationId, "prs" | "agents">, { idle: SFSymbol; active: SFSymbol }> = {
	projects: { idle: "folder", active: "folder.fill" },
	settings: { idle: "gearshape", active: "gearshape.fill" },
};

export function SidebarDestinationIcon({
	destination,
	color,
	active = false,
}: {
	destination: SidebarDestination;
	color: string;
	active?: boolean;
}) {
	// Pull requests draw the renderer's own lucide glyph, the one the
	// notifications and the PR page use, rather than an SF Symbol that only
	// resembles it. It is a React Native view, so it needs a host inside SwiftUI.
	if (destination.id === "prs" || destination.id === "agents") {
		return (
			<RNHostView matchContents>
				<View style={{ width: 21, height: 21, alignItems: "center", justifyContent: "center" }}>
					{destination.id === "agents" ? (
						<WorkersIcon size={20} color={color} />
					) : (
						<NotificationTypeIcon icon="git-pull-request-arrow" size={20} color={color} />
					)}
				</View>
			</RNHostView>
		);
	}
	const symbol = symbols[destination.id];
	return <Icon name={active ? symbol.active : symbol.idle} size={21} color={color} />;
}
