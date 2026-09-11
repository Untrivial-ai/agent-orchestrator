import { useCallback } from "react";

/**
 * Focus hand-off between a menu that is closing and the surface its selected
 * item opened.
 *
 * Radix restores focus after a menu closes, but it defers that restore by a
 * tick (FocusScope dispatches its unmount handler from a `setTimeout`). When
 * the selected item opened a dialog, the dialog has already claimed the caret
 * by then, and the late restore yanks it back out, so the user lands in a
 * composer with no cursor in it.
 *
 * A menu that closes without opening anything leaves focus on `document.body`,
 * so "something outside the menu already holds focus" is a reliable signal that
 * another surface owns the caret. Only then is the restore skipped; Escape and
 * plain item selection keep Radix's normal return-to-trigger behaviour that
 * keyboard users depend on.
 *
 * Skipping it would stop there, except that the dialog cannot return focus for
 * us: Radix captured its return target when the dialog mounted, and that was
 * the menu item, which no longer exists. So the return target is captured when
 * the menu opens and handed back once the surface that borrowed the caret lets
 * go of it.
 */

// Only one menu is open at a time, so a single slot each is enough. `pending`
// also holds the surface that borrowed the caret, so the hand-back can tell
// "the dialog closed and dropped focus" from "focus moved on deliberately".
let menuReturnTarget: HTMLElement | null = null;
let capturedForMenu: HTMLElement | null = null;
let pending: { borrower: Element; target: HTMLElement } | null = null;

/**
 * Remember, while the menu is open, where focus should end up once it is done
 * with it. A dropdown returns to its trigger, which is only discoverable by its
 * `aria-controls` link while the menu is open; a context menu has no such
 * trigger, so it falls back to whatever the user was on before right-clicking.
 * Both are the element Radix itself would have returned to.
 */
export function useMenuReturnTarget<T extends HTMLElement>() {
	return useCallback((menu: T | null) => {
		// The node attaches when the menu opens and detaches when it closes. Radix
		// recomposes its own refs on every render, so this fires repeatedly for the
		// same element; only the first call is the moment the menu opened.
		if (!menu || capturedForMenu === menu) return;
		capturedForMenu = menu;

		// Synchronously, because a context menu takes the caret for itself during
		// the same commit: read it now and the element the user was actually on is
		// still focused, read it a tick later and the menu itself is. Never accept
		// something inside the menu, which would leave a target that is detached by
		// the time there is anything to hand back.
		const active = document.activeElement;
		menuReturnTarget =
			active instanceof HTMLElement && active !== document.body && !menu.contains(active)
				? active
				: null;

		// A dropdown has somewhere better to go: its trigger, which is only
		// discoverable through the `aria-controls` link while the menu is open, and
		// only once the commit that opened it has landed.
		queueMicrotask(() => {
			if (capturedForMenu !== menu || !menu.id) return;
			const trigger = menu.ownerDocument.querySelector<HTMLElement>(
				`[aria-controls="${CSS.escape(menu.id)}"]`,
			);
			if (trigger) menuReturnTarget = trigger;
		});
	}, []);
}

export function keepFocusOnOpenedSurface(event: Event) {
	if (event.defaultPrevented) return;
	const active = document.activeElement;
	if (!active || active === document.body) return;
	const menu = event.currentTarget ?? event.target;
	if (menu instanceof Node && menu.contains(active)) return;
	event.preventDefault();
	returnFocusWhenSurfaceCloses(active);
}

function returnFocusWhenSurfaceCloses(borrower: Element) {
	if (!menuReturnTarget) return;
	// The overlay, not the focused field: the user tabbing between the dialog's
	// own controls is not the caret finding a home somewhere else.
	pending = {
		borrower: borrower.closest("[role='dialog'], [role='alertdialog']") ?? borrower,
		target: menuReturnTarget,
	};
	// Re-adding an identical listener is a no-op, so this stays a single listener
	// that is removed again as soon as nothing is pending.
	document.addEventListener("focusout", handleFocusRelease, true);
}

function clearPending() {
	pending = null;
	document.removeEventListener("focusout", handleFocusRelease, true);
}

function handleFocusRelease(event: FocusEvent) {
	const current = pending;
	if (!current) {
		clearPending();
		return;
	}
	if (event.relatedTarget !== null) {
		// Focus moved to a real element. Inside the surface that borrowed it, the
		// user is still working there; anywhere else, the caret has a home and
		// there is nothing left to hand back.
		if (!current.borrower.contains(event.relatedTarget as Node)) clearPending();
		return;
	}
	// Two frames, not one tick: the surface the dialog was covering gets first
	// claim on the caret through its own effects. A worker terminal, for one,
	// re-enables input as the dialog closes and focuses itself, and it is a
	// better landing spot than the menu trigger. This hand-back is only the
	// fallback for focus that would otherwise be stranded on `document.body`.
	requestAnimationFrame(() =>
		requestAnimationFrame(() => {
			if (pending !== current) return;
			// Focus can also be dropped while the surface is still open, by a blur
			// somewhere else on the page. Nothing to hand back yet, so keep waiting.
			if (current.borrower.isConnected) return;
			// Decided either way: never leave the target armed for an unrelated
			// focus loss later on.
			clearPending();
			if (document.activeElement !== document.body) return;
			if (current.target.isConnected) current.target.focus();
		}),
	);
}

/** Runs the caller's own handler first, then the hand-off guard. */
export function composeMenuCloseAutoFocus(handler?: (event: Event) => void) {
	return (event: Event) => {
		handler?.(event);
		keepFocusOnOpenedSurface(event);
	};
}
