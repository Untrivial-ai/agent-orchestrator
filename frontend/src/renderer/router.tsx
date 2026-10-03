import { createHashHistory, createRouter, ErrorComponent, useRouterState } from "@tanstack/react-router";
import type { ErrorComponentProps } from "@tanstack/react-router";
import { isCancelledError } from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import { DaemonStartupLoader } from "./components/DaemonStartupLoader";
import { routeTree } from "./routeTree.gen";

export function AppPendingFallback() {
	const isInitialLoad = useRouterState({ select: (state) => state.resolvedLocation === undefined });
	return isInitialLoad ? <DaemonStartupLoader /> : null;
}

// A query cancelled by navigation/teardown loses its race against unmount and
// lands in the router boundary; rendering the stock crash screen for it would
// blank the whole window over a benign cancellation. Render nothing instead —
// the boundary's resetKey clears the error state on the next navigation.
export function RouteErrorFallback({ error }: ErrorComponentProps) {
	if (isCancelledError(error) || (error instanceof Error && error.name === "AbortError")) {
		return null;
	}
	return <ErrorComponent error={error} />;
}

// Hash history is required for Electron's file:// renderer origin — browser
// history would break on hard reload since there is no server to serve paths.
export function createAppRouter(queryClient: QueryClient) {
	return createRouter({
		history: createHashHistory(),
		routeTree,
		context: { queryClient },
		defaultPreload: "intent",
		// Parent route loaders probe the daemon before ShellLayout can mount.
		// Render the same viewport-wide startup screen during that gap so the
		// native window never exposes an empty frame before its shell appears.
		// Once a location has resolved, later route loads must not bring startup
		// branding back over an already-running app.
		defaultPendingComponent: AppPendingFallback,
		defaultPendingMs: 0,
		// A cancelled query losing the teardown race must not blank the window.
		defaultErrorComponent: RouteErrorFallback,
		// Always re-run loaders when a route is preloaded or visited so React
		// Query's cache is the single source of truth for staleness.
		defaultPreloadStaleTime: 0,
		scrollRestoration: true,
	});
}
