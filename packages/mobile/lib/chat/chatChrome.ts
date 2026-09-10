import type { Theme } from "../theme";

export function jumpToLatestColors(t: Theme) {
	return {
		backgroundColor: t.bgElevated,
		foregroundColor: t.textPrimary,
	};
}

export function composerSurfaceStyle(t: Theme) {
	return {
		paddingHorizontal: 4,
		paddingTop: 8,
		paddingBottom: 14,
	};
}
