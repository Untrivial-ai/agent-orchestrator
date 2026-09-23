/**
 * The chat surface's decision pills.
 *
 * An approval and a docked question are the same thing to the reader — the turn
 * waiting on an answer — so they wear the same chrome. These are deliberately
 * not the shadcn `Button`: that primitive is `rounded-md` at the form-control
 * height, and every one of these call sites would have to override its radius,
 * height, type scale, and press transform to reach the pill. Keeping the idiom
 * in one place is what makes it a shared pattern rather than a coincidence.
 */

/** A secondary decision: Cancel, Skip, Decline, Back, and deny-style choices. */
export const QUIET_ACTION_PILL =
	"inline-flex h-7 items-center gap-1.5 rounded-full border border-border-strong bg-background/20 px-2.5 text-[12.5px] text-foreground/90 transition-colors hover:bg-interactive-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:pointer-events-none disabled:opacity-50";

/** The accent fill both the whole pill and a split pill's shell are drawn from. */
const ACCENT_BODY = "h-7 rounded-full bg-logo-accent text-logo-accent-foreground shadow-sm";

/** The label row: the part that is actually pressed, in either arrangement. */
const ACCENT_LABEL =
	"inline-flex items-center gap-1.5 px-2.5 text-[12.5px] transition-colors hover:bg-logo-accent-bright focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50";

/** The region's one primary action, as a single self-contained button. */
export const ACCENT_ACTION_PILL = `${ACCENT_BODY} ${ACCENT_LABEL}`;

/** The shell of a split primary action: one fill holding several interiors. */
export const ACCENT_ACTION_SHELL = `flex overflow-hidden ${ACCENT_BODY}`;

/** One pressable interior of a split primary action. */
export const ACCENT_ACTION_SEGMENT = ACCENT_LABEL;
