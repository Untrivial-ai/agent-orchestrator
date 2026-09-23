/** Shared inspector width limits — single source for SessionView + useResizable. */

export const INSPECTOR_SEPARATOR_RESERVE_PX = 8;
export const WORKSPACE_ABSOLUTE_MIN_PX = 300;

/** Live pixel ceiling for the inspector given the session-split's available width. */
export function inspectorMaxWidthPx(
	availableWidth?: number,
	maxPercent = 55,
	chatMinWidth = 560,
): number | undefined {
	if (!Number.isFinite(availableWidth) || !availableWidth || availableWidth <= 0) return undefined;
	const percentageCap = Math.floor((availableWidth * maxPercent) / 100);
	const readableChatCap = Math.max(WORKSPACE_ABSOLUTE_MIN_PX, availableWidth - chatMinWidth);
	return Math.min(availableWidth, percentageCap, readableChatCap);
}

/** CSS max-width expression bound on `#session-workspace` as `--session-inspector-max-width`. */
export function inspectorMaxWidthCss(maxPercent: number, chatMinWidth: number): string {
	return `min(${maxPercent}%, max(${WORKSPACE_ABSOLUTE_MIN_PX}px, calc(100% - ${chatMinWidth}px)))`;
}
