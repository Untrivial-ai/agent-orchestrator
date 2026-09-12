// Bottom-inset arithmetic for the session screen's input dock. Pure so the rule
// is testable — getting it wrong is what made the dock jump twice whenever the
// keyboard appeared.
//
// The screen already reserves the keyboard (see `rootKeyboardPad`) as padding
// on its root view, which lifts everything above the keyboard. The dock must
// therefore add *nothing* more while the keyboard is up: the old code flipped its own padding from
// `insets.bottom` (34pt on a notched phone) to `8` at the same moment the root
// gained the keyboard height, so the two moves fought each other and the bar
// visibly kicked.
//
// With the keyboard down there is no root padding, so the dock owes the
// home-indicator inset itself.

/** Minimum breathing room under the dock on a device with no home indicator. */
export const MIN_DOCK_INSET = 8;

/**
 * Bottom padding the root view owes to clear the keyboard.
 *
 * `height` is whatever the platform's keyboard event reported, and the two
 * platforms report different things:
 *
 * - iOS measures from the bottom of the screen, so the value already spans the
 *   home indicator. Reserve it as-is.
 * - Android reports `imeInsets.bottom - systemBars.bottom` (see
 *   `checkForKeyboardEvents` in ReactRootView.java) — the keyboard height with
 *   the navigation bar already subtracted. But our root view runs edge-to-edge
 *   underneath that nav bar, so the keyboard actually covers the full
 *   `imeInsets.bottom`. Padding by the reported height alone left the dock
 *   short by exactly the nav-bar inset, which is why the input row was cut in
 *   half under gesture nav (~24dp) and hidden outright under 3-button nav
 *   (48dp). Add the inset back.
 */
export function rootKeyboardPad(
	platform: "android" | "ios",
	height: number,
	insetsBottom: number,
): number {
	if (height <= 0) return 0;
	return platform === "android" ? height + insetsBottom : height;
}

export function screenKeyboardAvoidance(
	platform: "android" | "ios",
	height: number,
	insetsBottom: number,
) {
	const paddingBottom = rootKeyboardPad(platform, height, insetsBottom);
	return {
		showEvent: platform === "ios" ? "keyboardWillShow" as const : "keyboardDidShow" as const,
		hideEvent: platform === "ios" ? "keyboardWillHide" as const : "keyboardDidHide" as const,
		paddingBottom,
		rootStyle: { paddingBottom },
	};
}

export function dockInset(kbHeight: number, insetsBottom: number): number {
	// Keyboard up: the root view's padding (rootKeyboardPad) already clears the
	// keyboard *and* the nav bar beneath it. Anything here is dead space between
	// the dock and the keyboard.
	if (kbHeight > 0) return 0;
	return insetsBottom > 0 ? insetsBottom : MIN_DOCK_INSET;
}
