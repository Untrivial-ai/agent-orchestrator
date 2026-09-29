import type { Theme } from "../theme";
import { space } from "../tokens";

export function jumpToLatestColors(t: Theme) {
	return {
		backgroundColor: t.bgElevated,
		foregroundColor: t.textPrimary,
	};
}

export function composerSurfaceStyle(t: Theme) {
	return {
		paddingHorizontal: space.xxs,
		paddingTop: space.sm,
		paddingBottom: space.md,
	};
}

export function userMessageSurfaceStyle(t: Theme) {
	return {
		backgroundColor: t.bgElevated,
		foregroundColor: t.textPrimary,
	};
}
