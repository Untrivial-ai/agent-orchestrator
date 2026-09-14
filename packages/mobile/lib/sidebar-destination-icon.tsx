import { RNHostView } from "@expo/ui";
import { Feather } from "@expo/vector-icons";
import type { SidebarDestination } from "./sidebar-navigation";

export function SidebarDestinationIcon({
	destination,
	color,
}: {
	destination: SidebarDestination;
	color: string;
}) {
	return (
		<RNHostView matchContents>
			<Feather name={destination.icon} size={21} color={color} />
		</RNHostView>
	);
}
