const RESTING_GAP = 12;
const KEYBOARD_GAP = 12;
const DOCK_HEIGHT = 52;
const LIST_GAP = 16;

export function workerDockKeyboardLayout(
	keyboardHeight: number,
	safeAreaBottom: number,
	keyboardVisible = keyboardHeight > 0,
) {
	return {
		rootPaddingBottom: 0,
		// With adjustResize, Android has already shortened the root by the time
		// keyboardDidShow fires. The measured overlap is then zero even though the
		// IME is visible. Keep visibility as a separate fact so the dock still gets
		// a small breathing gap instead of re-applying the home/navigation inset.
		dockBottom: keyboardVisible ? keyboardHeight + KEYBOARD_GAP : safeAreaBottom + RESTING_GAP,
	};
}

/** Lets the final result scroll completely above the floating search dock. */
export function workerListBottomInset(dockBottom: number): number {
	return dockBottom + DOCK_HEIGHT + LIST_GAP;
}

export function workerDockVisibility(searchOpen: boolean) {
	return searchOpen
		? { showControls: false, showSearch: true, showSpawn: false }
		: { showControls: true, showSearch: false, showSpawn: true };
}
