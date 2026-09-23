import { duration, press, spring } from "./tokens";

/**
 * The app's motion language, in one place.
 *
 * Two halves, deliberately:
 *
 *   - **Named durations** for the transitions specific to this app — a board row
 *     moving between sections, a banner arriving above a list, a paged question
 *     sliding out. Each is a number lifted from the screen that first needed it,
 *     so a change here is a visible decision rather than a drive-by edit.
 *   - **The ladders and hooks** — the duration/easing/press steps in `tokens.ts`
 *     plus the press and entrance hooks every shared control uses.
 *
 * Two rules decide everything here:
 *
 *   - **Fast for the frequent.** Anything the finger triggers repeatedly gets a
 *     ≤150ms transition on transform or opacity only, so it composites on the
 *     GPU and never delays the next tap.
 *   - **Motion is never the only signal.** Every animated state change in this
 *     app also changes color, an icon or a label, so the interface still reads
 *     with motion switched off — which is exactly what happens when the user has
 *     Reduce Motion on, where these helpers become no-ops.
 *
 * Free of React Native imports so the rules stay unit-testable under Node; the
 * hooks that need `Animated` live in motionHooks.ts, and the hook that reads the
 * OS setting lives in useReducedMotion.ts.
 */

/** `Dot`'s pulse half-period. */
export const BREATHE_MS = 1200;

/**
 * One full turn of a progress spinner.
 *
 * Slow enough to read as motion rather than a flicker, fast enough that a lap
 * never looks like a stall. Linear, because an eased turn appears to hitch at
 * every pass.
 */
export const SPIN_MS = 900;

/**
 * The sidebar drawer's spring.
 *
 * Note for a future Reanimated port: these are `Animated.spring` parameters. The
 * companion flick threshold in sidebar-gesture.ts is expressed in PanResponder's
 * px/ms, while Reanimated reports px/s — that constant must be converted, not
 * copied.
 */
export const DRAWER_SPRING = { damping: 24, stiffness: 240, mass: 0.8 } as const;

/** Fallback used when the keyboard event carries no duration. */
export const KEYBOARD_FALLBACK_MS = 250;

/** A row moving between sections, or a list re-laying out. */
export const LAYOUT_MS = 160;
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
 * Still the longest of the three, because this one is meant to be *followed*:
 * the point is to see which row moved and where it went. Kept tight all the same —
 * folding a section moves a whole run of rows, and the eye reads the destination
 * long before a longer curve would have finished.
 */
export const ROW_MOVE_MS = 170;
/** A row arriving in or leaving a section. Shorter, so it never outlasts the move. */
export const ROW_ENTER_MS = 120;

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

/**
 * Whether a spinner should turn.
 *
 * The same rule as the breathing dot, and for the same reason: Reduce Motion
 * means the loop never starts, rather than starting and being cancelled. A
 * continuous rotation is the thing that setting is most often asking us to stop.
 */
export function shouldSpin(reduced: boolean, spinning: boolean): boolean {
	return spinning && !reduced;
}

export { duration, press, spring };
