// Bottom-inset arithmetic for the session screen's input dock. Pure so the rule
// is testable — getting it wrong is what made the dock jump twice whenever the
// keyboard appeared. The root reserves the keyboard (see `rootKeyboardPad`),
// so the dock contributes nothing while it is visible.
//
// With the keyboard down there is no root padding, so the dock owes the
// home-indicator inset itself.

/** Minimum breathing room under the dock on a device with no home indicator. */
export const MIN_DOCK_INSET = 8;

/**
 * Breathing room between the dock and the top edge of the keyboard.
 *
 * Not zero: the root view clears exactly the keyboard's height, so without this
 * the pill's bottom edge *is* the keyboard's top edge. iOS' own sheets leave a
 * gap there, and the flush version read as the bar being cut off by the
 * keyboard rather than sitting above it.
 */
export const KEYBOARD_DOCK_GAP = 8;

// KeyboardAvoidingView measures against the window while this screen begins
// below a native-stack header. Supplying that stack's measured header height
// reconciles those coordinate spaces without assuming a particular iPhone,
// orientation, status bar, or Dynamic Type size.
export function keyboardVerticalOffset(headerHeight: number): number {
	return Math.max(0, headerHeight);
}

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

export function dockInset(kbHeight: number, insetsBottom: number, keyboardVisible = kbHeight > 0): number {
	// `keyboardVisible` covers Android's adjustResize path, where the window may
	// already have shifted before a non-zero height is reported.
	if (keyboardVisible) return KEYBOARD_DOCK_GAP;
	return insetsBottom > 0 ? insetsBottom : MIN_DOCK_INSET;
}
