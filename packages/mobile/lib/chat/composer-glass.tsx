import type { ReactElement } from "react";

/**
 * Android has no Liquid Glass, and no system material to approximate one with
 * that would survive a multiline field growing under it. The composer keeps the
 * elevated fill there — see `ChatComposer`'s pill styles.
 */
export const composerGlassSupported = false;

export function ComposerGlass(_props: { radius: number }): ReactElement | null {
	return null;
}
