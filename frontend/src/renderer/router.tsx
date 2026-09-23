import { createHashHistory, createRouter, useRouterState } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import { DaemonStartupLoader } from "./components/DaemonStartupLoader";
import { routeTree } from "./routeTree.gen";

export function AppPendingFallback() {
	const isInitialLoad = useRouterState({ select: (state) => state.resolvedLocation === undefined });
	return isInitialLoad ? <DaemonStartupLoader /> : null;
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
		// Always re-run loaders when a route is preloaded or visited so React
		// Query's cache is the single source of truth for staleness.
		defaultPreloadStaleTime: 0,
		scrollRestoration: true,
	});
}
