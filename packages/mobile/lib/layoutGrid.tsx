import { useEffect, useState } from "react";
import { Dimensions, StyleSheet, Text, View } from "react-native";
import { space } from "./tokens";

/**
 * A developer layout grid, toggled from Settings → Developer.
 *
 * Design review kept landing on "this looks a pixel off" with no way to settle
 * it from a screenshot. This draws the measurements the app actually uses — a
 * 4pt grid with the 44pt control lines emphasised — over whatever is on screen,
 * so a mismatch is a visible one rather than a feeling.
 *
 * Dev-only: `__DEV__` gates both the overlay's render and the settings row that
 * turns it on, so it cannot reach a release build.
 */

const MINOR = space.xxs; // 4
const MAJOR = 44; // control size: header, dock and drawer circles

let enabled = false;
const listeners = new Set<(value: boolean) => void>();

export function toggleLayoutGrid(next?: boolean): void {
	enabled = next ?? !enabled;
	for (const listener of listeners) listener(enabled);
}

export function useLayoutGrid(): boolean {
	const [value, setValue] = useState(enabled);
	useEffect(() => {
		listeners.add(setValue);
		return () => {
			listeners.delete(setValue);
		};
	}, []);
	return value;
}

export function LayoutGrid() {
	const active = useLayoutGrid();
	if (!__DEV__ || !active) return null;

	const { width, height } = Dimensions.get("window");
	const verticals = Array.from({ length: Math.floor(width / MINOR) + 1 }, (_, index) => index * MINOR);
	const horizontals = Array.from({ length: Math.floor(height / MINOR) + 1 }, (_, index) => index * MINOR);

	return (
		<View pointerEvents="none" style={StyleSheet.absoluteFill}>
			{verticals.map((x) => (
				<View key={`v${x}`} style={[styles.line, { left: x, width: x % MAJOR === 0 ? 1 : StyleSheet.hairlineWidth, opacity: x % MAJOR === 0 ? 0.55 : 0.18, top: 0, bottom: 0 }]} />
			))}
			{horizontals.map((y) => (
				<View key={`h${y}`} style={[styles.line, { top: y, height: y % MAJOR === 0 ? 1 : StyleSheet.hairlineWidth, opacity: y % MAJOR === 0 ? 0.55 : 0.18, left: 0, right: 0 }]} />
			))}
			<View style={styles.captionWrap}>
				<Text style={styles.caption}>{`${MINOR}pt grid · bold lines = ${MAJOR}pt controls`}</Text>
			</View>
		</View>
	);
}

const styles = StyleSheet.create({
	line: { position: "absolute", backgroundColor: "#ff2d55" },
	captionWrap: {
		position: "absolute",
		top: 70,
		alignSelf: "center",
		backgroundColor: "rgba(0,0,0,0.65)",
		borderRadius: 6,
		paddingHorizontal: 8,
		paddingVertical: 3,
	},
	caption: {
		color: "#ffffff",
		fontSize: 11,
		fontWeight: "700",
		letterSpacing: 0.4,
	},
});
