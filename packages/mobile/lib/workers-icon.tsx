import Svg, { Path } from "react-native-svg";

/**
 * The Workers mark: lucide's Layers, from its own path data — the same icon set
 * the desktop renderer and the drawer's Pull Requests glyph draw from, so both
 * platforms show one drawing. Many runs stacked side by side is what the Workers
 * page is.
 */
export function WorkersIcon({ size = 20, color }: { size?: number; color: string }) {
	return (
		<Svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke={color} strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
			<Path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83Z" />
			<Path d="m22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65" />
			<Path d="m22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65" />
		</Svg>
	);
}
