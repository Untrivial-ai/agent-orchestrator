import { Icon } from "@expo/ui";
import { Host } from "@expo/ui/swift-ui";
import type { SFSymbol } from "sf-symbols-typescript";
import type { NotificationTypeIconProps } from "./notification-type-icon.types";

const symbols: Record<NotificationTypeIconProps["icon"], SFSymbol> = {
	"message-circle": "bubble.left",
	"git-pull-request": "arrow.triangle.pull",
	"check-circle": "checkmark.circle",
	"x-circle": "xmark.circle",
	bell: "bell",
};

export function NotificationTypeIcon({ icon, color }: NotificationTypeIconProps) {
	return (
		<Host matchContents>
			<Icon name={symbols[icon]} size={14} color={color} />
		</Host>
	);
}
