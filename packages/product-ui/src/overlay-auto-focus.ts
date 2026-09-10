import { type RefObject, useEffect } from "react";

/**
 * Put the caret in an overlay's primary field the moment the overlay opens, and
 * keep it there while whatever opened the overlay finishes tearing down.
 *
 * The plain `autoFocus` attribute is not enough when the overlay was opened
 * from a Radix menu item. The menu is still mounted for that commit and its
 * focus trap pulls focus back out of the field React just focused, so the user
 * lands in a dialog with no cursor and has to click the field. Re-asserting on
 * the next couple of frames wins that race, and doing it only while focus sits
 * outside the overlay means a user who deliberately tabbed or clicked to
 * another control keeps it.
 */
export function useOverlayAutoFocus(ref: RefObject<HTMLElement | null>, enabled = true) {
	useEffect(() => {
		const element = ref.current;
		if (!enabled || !element) return;

		const doc = element.ownerDocument;
		// The overlay, not the field, is the boundary: focus that moved to the
		// agent picker or the Start button is the user's, focus on `body` or on
		// the menu trigger behind the overlay is the race we are fixing.
		const overlay = element.closest("[role='dialog']") ?? element;
		const claim = () => {
			const active = doc.activeElement;
			if (active && active !== doc.body && overlay.contains(active)) return;
			element.focus();
		};

		claim();
		let remaining = 2;
		let frame = requestAnimationFrame(function reassert() {
			claim();
			remaining -= 1;
			if (remaining > 0) frame = requestAnimationFrame(reassert);
		});
		return () => cancelAnimationFrame(frame);
	}, [enabled, ref]);
}
