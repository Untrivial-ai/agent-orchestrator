/**
 * Leaving a screen that may have nothing behind it.
 *
 * `router.back()` dispatches GO_BACK, and a navigator with an empty stack does
 * not handle it: the control does nothing and the app surfaces "The action
 * 'GO_BACK' was not handled by any navigator". That is a dead end with no way
 * out — the screen has no other exit, so the only way off is to kill the app.
 *
 * Every screen is reachable that way now. A deep link (`aomobile://notifications`),
 * a notification tap that cold-starts the app, and a sheet route opened directly
 * all arrive with no history, while the same screen opened from the board has
 * one. So the rule is: go back when there is somewhere to go, land on a named
 * screen when there is not.
 *
 * Kept as one function rather than repeated at each call site — it had already
 * been written out three times by hand, and three other exits were missing it.
 */

/** The router surface this needs, so the rule can be tested without a navigator. */
export type BackRouter = {
	canGoBack(): boolean;
	back(): void;
	replace(href: string): void;
};

/**
 * Goes back, or replaces the current route with `fallback` when the stack is
 * empty. `/` is the board, which is where every one of these screens was opened
 * from in the first place.
 */
export function backOr(router: BackRouter, fallback = "/"): void {
	if (router.canGoBack()) router.back();
	else router.replace(fallback);
}
