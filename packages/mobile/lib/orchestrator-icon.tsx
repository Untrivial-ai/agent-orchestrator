import Svg, { Circle, Path } from "react-native-svg";

/**
 * The renderer's orchestrator mark, ported from `components/icons.tsx`: one
 * parent node fanning out to three children, in lucide's 24x24 round-stroke
 * style. Copied path for path so the orchestrator reads as the same thing on
 * the phone as it does on the desktop.
 */
export function OrchestratorIcon({ size = 18, color }: { size?: number; color: string }) {
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
			<Circle cx="12" cy="4" r="2" />
			<Circle cx="5" cy="20" r="2" />
			<Circle cx="12" cy="20" r="2" />
			<Circle cx="19" cy="20" r="2" />
			<Path d="M12 6v12" />
			<Path d="M5 11h14" />
			<Path d="M5 11v7" />
			<Path d="M19 11v7" />
		</Svg>
	);
}
