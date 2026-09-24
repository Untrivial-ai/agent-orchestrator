import Svg, { Circle, Path } from "react-native-svg";
import type { NotificationTypeIconProps } from "./notification-type-icon.types";

/**
 * The renderer's notification glyphs, drawn from lucide's own path data.
 *
 * Copied verbatim from lucide-react rather than approximated with an icon font:
 * a font set close enough to pick from is still a different drawing, and these
 * are four icons of two to six primitives each. Both platforms render this same
 * component, so iOS and Android can no longer disagree — they used to, iOS
 * having had its own SF Symbols map.
 *
 * lucide draws on a 24x24 grid with a 2px round stroke and no fill.
 */
const ICONS: Record<NotificationTypeIconProps["icon"], React.ReactNode> = {
	// MessageSquareDot
	"message-square-dot": (
		<>
			<Path d="M12.7 3H4a2 2 0 0 0-2 2v16.286a.71.71 0 0 0 1.212.502l2.202-2.202A2 2 0 0 1 6.828 19H20a2 2 0 0 0 2-2v-4.7" />
			<Circle cx="19" cy="6" r="3" />
		</>
	),
	// GitPullRequestArrow
	"git-pull-request-arrow": (
		<>
			<Circle cx="5" cy="6" r="3" />
			<Path d="M5 9v12" />
			<Circle cx="19" cy="18" r="3" />
			<Path d="m15 9-3-3 3-3" />
			<Path d="M12 6h5a2 2 0 0 1 2 2v7" />
		</>
	),
	// GitMerge
	"git-merge": (
		<>
			<Circle cx="18" cy="18" r="3" />
			<Circle cx="6" cy="6" r="3" />
			<Path d="M6 21V9a9 9 0 0 0 9 9" />
		</>
	),
	// GitPullRequestClosed
	"git-pull-request-closed": (
		<>
			<Circle cx="6" cy="6" r="3" />
			<Path d="M6 9v12" />
			<Path d="m21 3-6 6" />
			<Path d="m21 9-6-6" />
			<Path d="M18 11.5V15" />
			<Circle cx="18" cy="18" r="3" />
		</>
	),
	// Bell
	bell: (
		<>
			<Path d="M10.268 21a2 2 0 0 0 3.464 0" />
			<Path d="M3.262 15.326A1 1 0 0 0 4 17h16a1 1 0 0 0 .74-1.673C19.41 13.956 18 12.499 18 8A6 6 0 0 0 6 8c0 4.499-1.411 5.956-2.738 7.326" />
		</>
	),
};

export function NotificationTypeIcon({ icon, color, size = 15 }: NotificationTypeIconProps) {
	return (
		<Svg
			width={size}
			height={size}
			viewBox="0 0 24 24"
			fill="none"
			stroke={color}
			strokeWidth={2}
			strokeLinecap="round"
			strokeLinejoin="round"
		>
			{ICONS[icon]}
		</Svg>
	);
}
