// Bottom-inset arithmetic for the session screen's input dock. Pure so the rule
// is testable — getting it wrong is what made the dock jump twice whenever the
// keyboard appeared.
//
// The screen already reserves `kbHeight` as padding on its root view, which
// lifts everything above the keyboard. The dock must therefore add *nothing*
// more while the keyboard is up: the old code flipped its own padding from
// `insets.bottom` (34pt on a notched phone) to `8` at the same moment the root
// gained the keyboard height, so the two moves fought each other and the bar
// visibly kicked.
//
// With the keyboard down there is no root padding, so the dock owes the
// home-indicator inset itself.

/** Minimum breathing room under the dock on a device with no home indicator. */
export const MIN_DOCK_INSET = 8;

export function screenKeyboardAvoidance(platform: "android" | "ios", height: number) {
	return {
		showEvent: platform === "ios" ? "keyboardWillShow" as const : "keyboardDidShow" as const,
		hideEvent: platform === "ios" ? "keyboardWillHide" as const : "keyboardDidHide" as const,
		paddingBottom: height,
		rootStyle: { paddingBottom: height },
	};
}

export function dockInset(kbHeight: number, insetsBottom: number): number {
	// Keyboard up: the root view's padding already clears it. Anything here is
	// dead space between the dock and the keyboard.
	if (kbHeight > 0) return 0;
	return insetsBottom > 0 ? insetsBottom : MIN_DOCK_INSET;
}
