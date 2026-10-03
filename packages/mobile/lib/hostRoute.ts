import type { ServerConfig } from "./config";

/** A bare local ID is never enough to open or act on another machine. */
export function hostRouteMatches(routeHostId: string | undefined, currentHostId: string | undefined): boolean {
	return Boolean(routeHostId && currentHostId && routeHostId === currentHostId);
}

/** An explicit deep link keeps its source; a generic modal captures its opening machine. */
export function openingHostId(routeHostId: string | undefined, currentHostId: string | undefined): string | undefined {
	return routeHostId || currentHostId;
}

export function spawnHostMatches(args: {
	openedHostId: string | undefined;
	currentHostId: string | undefined;
	routeProjectId?: string;
	routeHostId?: string;
}): boolean {
	// A legacy link carrying only a project ID has no safe machine provenance.
	return (!args.routeProjectId || Boolean(args.routeHostId))
		&& hostRouteMatches(args.openedHostId, args.currentHostId);
}

/** Never render an old preview URL using a new endpoint's bearer credential. */
export function previewForConfig<T>(
	loaded: { config: ServerConfig; value: T } | null,
	config: ServerConfig | null,
	routeHostId: string | undefined,
): T | null {
	return loaded && loaded.config === config && hostRouteMatches(routeHostId, config?.hostId)
		? loaded.value
		: null;
}
