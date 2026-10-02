import { describe, expect, it, vi } from "vitest";

vi.mock("expo-router", () => ({ useNavigationContainerRef: vi.fn(), useRouter: vi.fn() }));

import { isPresentedRoute, presentedRouteDepth } from "./pageNavigation";

describe("opening pages outside sheets", () => {
	it("recognises every sheet and modal the root stack presents", () => {
		for (const name of ["sheets/review-actions", "sheets/conversation-actions", "settings", "spawn", "pair"]) expect(isPresentedRoute(name)).toBe(true);
		for (const name of ["(tabs)", "review/[sessionId]", "reviewer/[reviewId]", "shell/[handleId]", "notifications"]) expect(isPresentedRoute(name)).toBe(false);
	});

	it("dismisses the first presented route and everything pushed into it", () => {
		expect(presentedRouteDepth(["(tabs)", "review/[sessionId]"])).toBe(0);
		expect(presentedRouteDepth(["(tabs)", "review/[sessionId]", "sheets/review-actions"])).toBe(1);
		// A page already pushed into a sheet goes with it.
		expect(presentedRouteDepth(["(tabs)", "settings", "notifications", "review/[sessionId]"])).toBe(3);
	});
});
