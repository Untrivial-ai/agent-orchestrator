import { RNHostView } from "@expo/ui";
import { Feather } from "@expo/vector-icons";
import type { NotificationTypeIconProps } from "./notification-type-icon.types";

export function NotificationTypeIcon({ icon, color }: NotificationTypeIconProps) {
	return (
		<RNHostView matchContents>
			<Feather name={icon} size={14} color={color} />
		</RNHostView>
	);
}
