import { glassEffect } from "@expo/ui/swift-ui/modifiers";
import { radius } from "./tokens";

/**
 * The app's Liquid Glass recipes.
 *
 * Glass is native here (`@expo/ui` renders a real SwiftUI material), so these
 * helpers only decide the three things the JS side is responsible for:
 *
 *   1. **Shape.** A material reads as one surface only when its shape matches
 *      the view it wraps — a circle on an icon button, a capsule on a field,
 *      a rounded rect on a panel. Passing the shape and its radius together is
 *      what stops the two from disagreeing.
 *   2. **Interactivity.** `interactive` is for controls the finger lands on; a
 *      field the user types into is not one, and animating it on touch reads as
 *      a missed tap.
 *   3. **One layer, over real content.** Glass blurs what sits behind it, so it
 *      belongs on chrome floating over the list, never stacked on another glass
 *      surface or on an opaque fill — there it would just be a grey box.
 *
 * Tint stays out of the recipes on purpose: it carries state, and every call
 * site passes the color that means something there (`accent` for the selected
 * control, `textSecondary` for the rest).
 */

/** A rounded-rect panel: composer bars, docked search, grouped controls. */
export function glassPanel(cornerRadius: number = radius.lg, tint?: string) {
	return glassEffect({
		glass: { variant: "regular", interactive: true, ...(tint ? { tint } : {}) },
		shape: "roundedRectangle",
		cornerRadius,
	});
}

/** A capsule field, radius derived from its height so it can never look oval. */
export function glassField(height: number) {
	return glassEffect({
		glass: { variant: "regular", interactive: false },
		shape: "roundedRectangle",
		cornerRadius: height / 2,
	});
}

/**
 * A round glass surface.
 *
 * `interactive` gives the material its own press response — right when the
 * gesture sits on the same view as the glass (the header buttons). Pass `false`
 * when the tap target is a *child* of the glass instead: an interactive material
 * takes the touch itself, and the control underneath only sees taps that land on
 * its own content, which is why the dock's filter menu opened intermittently.
 *
 * Prefer keeping it interactive and sizing the child to the whole shape: a
 * control draws *over* the material, so a full-size label wins the hit test and
 * the press response survives. Turning interactivity off is the fallback, and it
 * costs the scale-and-brighten animation.
 */
export function glassCircle(tint?: string, interactive = true) {
	return glassEffect({
		glass: { variant: "regular", interactive, ...(tint ? { tint } : {}) },
		shape: "circle",
	});
}
