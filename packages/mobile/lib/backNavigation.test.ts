import { describe, expect, it, vi } from "vitest";
import { backOr } from "./backNavigation";

/**
 * A navigator, faked. The rule only ever calls three methods, so this is enough
 * of one — and the spies are what the assertions watch.
 */
function router(canGoBack: boolean) {
	return {
		canGoBack: () => canGoBack,
		back: vi.fn(),
		replace: vi.fn(),
	};
}

describe("backOr", () => {
	it("goes back when the stack has somewhere to go", () => {
		const r = router(true);
		backOr(r);
		expect(r.back).toHaveBeenCalledOnce();
		expect(r.replace).not.toHaveBeenCalled();
	});

	// The reported bug: `router.back()` on an empty stack is an unhandled GO_BACK,
	// so the control did nothing and the screen had no way out. A deep link, or a
	// notification tap that cold-starts the app, is how a screen gets there.
	it("replaces with the board when there is nothing behind the screen", () => {
		const r = router(false);
		backOr(r);
		expect(r.replace).toHaveBeenCalledWith("/");
		expect(r.back).not.toHaveBeenCalled();
	});

	it("takes a fallback for a screen that belongs somewhere else", () => {
		const r = router(false);
		backOr(r, "/projects");
		expect(r.replace).toHaveBeenCalledWith("/projects");
	});

	it("prefers going back over the fallback even when one is given", () => {
		const r = router(true);
		backOr(r, "/projects");
		expect(r.back).toHaveBeenCalledOnce();
		expect(r.replace).not.toHaveBeenCalled();
	});
});
