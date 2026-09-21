// Motion constants and the reduce-motion decision, in one place.
//
// Today the only animations are `Dot`'s breathing loop, the sidebar drawer
// spring, and the keyboard LayoutAnimation — each carrying its own numbers. As
// rows start animating between sections and banners start sliding in, those
// numbers need to agree, and every one of them needs a reduce-motion answer.
//
// Free of React Native imports so the rules are unit-testable; the hook that
// reads the OS setting lives separately (useReducedMotion), the same split as
// pushStatus.ts vs the screens that consume it.

/** `Dot`'s pulse half-period. Lifted from ui.tsx — the existing 1200ms timing. */
export const BREATHE_MS = 1200;

/**
 * The sidebar drawer's spring. Lifted verbatim from sidebar-navigation-shell.
 *
 * Note for a future Reanimated port: these are `Animated.spring` parameters. The
 * companion flick threshold in sidebar-gesture.ts is expressed in PanResponder's
 * px/ms, while Reanimated reports px/s — that constant must be converted, not
 * copied.
 */
export const DRAWER_SPRING = { damping: 24, stiffness: 240, mass: 0.8 } as const;

/** Fallback used when the keyboard event carries no duration. Lifted from the board. */
export const KEYBOARD_FALLBACK_MS = 250;

// New values, introduced with the motion work rather than lifted. Kept short:
// these run while the user is waiting to read something.
/** A row moving between sections, or a list re-laying out. */
export const LAYOUT_MS = 220;
/** A banner entering or leaving above a list. */
export const BANNER_MS = 180;
/** Swapping content in place — filter changes, destination changes. */
export const CROSSFADE_MS = 140;

/**
 * One paged question leaving while the next arrives, in the direction of the
 * swipe. Longer than a crossfade because the eye is following a direction here,
 * not just a change of content.
 */
export const PAGE_SLIDE_MS = 200;
/** A status colour changing on a rail or dot. */
export const TINT_MS = 200;
/**
 * A board row moving between sections — pinned, or promoted by a delivery event.
 *
 * Longer than LAYOUT_MS because this one is meant to be *followed*: the point is
 * to see which row moved and where it went. Too fast and it is the teleport it
 * replaces.
 */
export const ROW_MOVE_MS = 260;
/** A row arriving in or leaving a section. Shorter, so it never outlasts the move. */
export const ROW_ENTER_MS = 180;

export type MotionDurations = {
	breathe: number;
	layout: number;
	banner: number;
	crossfade: number;
	tint: number;
};

/**
 * Every duration, zeroed when the user has asked for reduced motion.
 *
 * Zero rather than "skip the animation" so callers stay branch-free: a zero
 * duration lands the view in its final state on the next frame, which is what
 * reduce-motion asks for, without each call site growing an `if`.
 */
export function motionDurations(reduced: boolean): MotionDurations {
	if (reduced) {
		return { breathe: 0, layout: 0, banner: 0, crossfade: 0, tint: 0 };
	}
	return {
		breathe: BREATHE_MS,
		layout: LAYOUT_MS,
		banner: BANNER_MS,
		crossfade: CROSSFADE_MS,
		tint: TINT_MS,
	};
}

/**
 * Whether a layout transition should run at all.
 *
 * Distinct from a zero duration: a looping animation (the breathing dot) must
 * not start in the first place, because a zero-duration loop is a busy loop.
 */
export function shouldAnimateLayout(reduced: boolean): boolean {
	return !reduced;
}

/** Whether the breathing status dot should loop. */
export function shouldBreathe(reduced: boolean, breathing: boolean): boolean {
	return breathing && !reduced;
}
