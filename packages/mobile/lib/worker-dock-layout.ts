const RESTING_GAP = 12;
const KEYBOARD_GAP = 12;
/**
 * The dock's gap above the keyboard, which is not the gap it keeps at rest.
 *
 * Leaves less room than `RESTING_GAP` on purpose: the field is 44pt inside a
 * 52pt dock, so it carries 4pt of the dock's own height below it, and matching
 * the two gaps left the field reading as further from the keys than it is.
 */
const KEYBOARD_DOCK_GAP = 8;
const DOCK_HEIGHT = 52;
const LIST_GAP = 16;

/** Where the dock sits with the keyboard down. */
export function workerDockRestingBottom(safeAreaBottom: number): number {
	return safeAreaBottom + RESTING_GAP;
}

/**
 * How far above that resting line the keyboard pushes the dock.
 *
 * A distance, not a flag: the dock holds its resting position and rides the
 * keyboard's own animation by this much, so it never waits for the keyboard to
 * finish moving. Deriving its position from a visibility flag is what left the
 * dock's buttons hanging where the keyboard had been and then snapping back —
 * the flag turns over when the keyboard has finished hiding.
 */
export function workerDockLift(keyboardHeight: number, safeAreaBottom: number): number {
	// Called from an animated style, which runs on the UI thread: without this the
	// UI runtime tries to call back into JS and the screen dies with "tried to
	// synchronously call a Remote Function".
	"worklet";
	const resting = safeAreaBottom + RESTING_GAP;
	const target = keyboardHeight + KEYBOARD_DOCK_GAP;
	return Math.max(target - resting, 0);
}

export function workerDockKeyboardLayout(
	keyboardHeight: number,
	safeAreaBottom: number,
	keyboardVisible = keyboardHeight > 0,
) {
	const resting = workerDockRestingBottom(safeAreaBottom);
	return {
		rootPaddingBottom: 0,
		// With adjustResize, Android has already shortened the root by the time
		// keyboardDidShow fires. The measured overlap is then zero even though the
		// IME is visible. Keep visibility as a separate fact so the dock still gets
		// a small breathing gap instead of re-applying the home/navigation inset.
		dockBottom: keyboardVisible ? keyboardHeight + KEYBOARD_GAP : resting,
		restingBottom: resting,
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
