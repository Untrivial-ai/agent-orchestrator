// Sheets are native `formSheet` routes now, and a route can't be handed an
// `onSelect` callback the way a component could. The opener parks its callback
// here and passes the key as a route param; the sheet route looks it up when a
// choice is made.
//
// Deliberately a module-level map rather than context: the opener and the sheet
// are separate screens in the navigator, so there is no shared React subtree to
// hang a provider on. Keys are released when the route unmounts (including a
// swipe-dismiss with no selection), so a closure can't outlive its sheet.

const handlers = new Map<string, (value: unknown) => void>();
let seq = 0;

/** Parks `fn` and returns the key to pass along as a route param. */
export function parkSheetResult<T>(fn: (value: T) => void): string {
	const key = `sheet-${++seq}`;
	handlers.set(key, fn as (value: unknown) => void);
	return key;
}

/** The parked callback for `key`, or undefined if it was already released. */
export function takeSheetResult<T>(key?: string): ((value: T) => void) | undefined {
	if (!key) return undefined;
	return handlers.get(key) as ((value: T) => void) | undefined;
}

export function releaseSheetResult(key?: string): void {
	if (key) handlers.delete(key);
}

/** Test seam — the map is module state, so suites must be able to reset it. */
export function resetSheetResults(): void {
	handlers.clear();
	seq = 0;
}

/**
 * Route + params for opening the project sheet. Kept here beside `parkSheetResult`
 * so callers can't forget to park the callback, and so the "0"/"1" param encoding
 * (route params are strings) lives in one place rather than at every call site.
 */
export function projectSheetRoute(opts: {
	selected: string;
	onSelect: (id: string) => void;
	includeAll?: boolean;
	title?: string;
	subtitle?: string;
}) {
	return {
		pathname: "/sheets/project" as const,
		params: {
			resultKey: parkSheetResult(opts.onSelect),
			selected: opts.selected,
			includeAll: opts.includeAll === false ? "0" : "1",
			...(opts.title ? { title: opts.title } : {}),
			...(opts.subtitle ? { subtitle: opts.subtitle } : {}),
		},
	};
}

export function agentSheetRoute(opts: { selected: string; onSelect: (id: string) => void; allowed?: string[]; mode?: "chat" | "tui" }) {
	return {
		pathname: "/sheets/agent" as const,
		params: {
			resultKey: parkSheetResult(opts.onSelect),
			selected: opts.selected,
			...(opts.allowed ? { allowed: opts.allowed.join("|") } : {}),
			...(opts.mode ? { mode: opts.mode } : {}),
		},
	};
}

export function modelSheetRoute(opts: { agentId: string; projectId: string; selected: string; onSelect: (id: string) => void }) {
	return {
		pathname: "/sheets/model" as const,
		params: {
			resultKey: parkSheetResult(opts.onSelect),
			agentId: opts.agentId,
			projectId: opts.projectId,
			selected: opts.selected,
		},
	};
}

export function connectSheetRoute(onConnected: () => void) {
	return { pathname: "/sheets/connect" as const, params: { resultKey: parkSheetResult(onConnected) } };
}

/**
 * Route + params for the store-update nudge. One parked callback carries both
 * outcomes: taking the update, or dismissing it (which is what the snooze
 * counter counts, so the opener has to hear about it).
 */
export function storeUpdateSheetRoute(opts: {
	version?: string;
	storeConfirmed: boolean;
	onAction: (action: "update" | "dismiss") => void;
}) {
	return {
		pathname: "/sheets/store-update" as const,
		params: {
			resultKey: parkSheetResult(opts.onAction),
			// Route params are strings, so the flag is encoded here rather than at
			// the call site — same reason as projectSheetRoute's "0"/"1".
			storeConfirmed: opts.storeConfirmed ? "1" : "0",
			...(opts.version ? { version: opts.version } : {}),
		},
	};
}
