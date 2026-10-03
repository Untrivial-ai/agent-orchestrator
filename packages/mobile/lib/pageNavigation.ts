import { useNavigationContainerRef, useRouter, type Href } from "expo-router";
import { useCallback } from "react";

// Routes the root stack presents as a sheet or modal (app/_layout.tsx). A page
// pushed while one of these is in the stack is presented inside it, so a review
// opened from a push tap or a sheet action landed as a bottom sheet.
const PRESENTED_ROUTES = new Set(["settings", "spawn", "pair"]);

export function isPresentedRoute(name: string): boolean {
	return PRESENTED_ROUTES.has(name) || name.startsWith("sheets/");
}

/** How many routes to dismiss so nothing presented remains on the stack. */
export function presentedRouteDepth(routeNames: readonly string[]): number {
	const first = routeNames.findIndex(isPresentedRoute);
	return first < 0 ? 0 : routeNames.length - first;
}

// Longer than the native formSheet dismissal, so the page is pushed onto the
// stack underneath rather than into a sheet that is still on its way out.
const SHEET_DISMISS_MS = 350;

/**
 * Opens a full-screen page (review, reviewer chat, reviewer terminal) as a
 * page, never inside a sheet: any presented sheet or modal is dismissed first.
 */
export function useOpenPage() {
	const router = useRouter();
	const navigation = useNavigationContainerRef();
	return useCallback((href: Href) => {
		const routes = navigation.isReady() ? navigation.getRootState()?.routes ?? [] : [];
		const depth = presentedRouteDepth(routes.map((route) => route.name));
		if (!depth) {
			router.push(href);
			return;
		}
		router.dismiss(depth);
		setTimeout(() => router.push(href), SHEET_DISMISS_MS);
	}, [navigation, router]);
}
