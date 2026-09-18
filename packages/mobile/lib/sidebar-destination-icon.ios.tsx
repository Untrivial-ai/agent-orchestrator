import { Icon } from "@expo/ui";
import type { SFSymbol } from "sf-symbols-typescript";
import type { SidebarDestination, SidebarDestinationId } from "./sidebar-navigation";

const symbols: Record<SidebarDestinationId, SFSymbol> = {
	projects: "folder",
	agents: "bolt.horizontal.circle",
	prs: "arrow.triangle.pull",
	settings: "gearshape",
};

export function SidebarDestinationIcon({
	destination,
	color,
}: {
	destination: SidebarDestination;
	color: string;
}) {
	return <Icon name={symbols[destination.id]} size={21} color={color} />;
}
