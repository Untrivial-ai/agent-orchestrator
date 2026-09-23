// What `app/session/[id].tsx` shows for an id, and when it asks the daemon about
// one. Pure — no React Native or Expo imports — so every outcome is unit-testable,
// the same split as orchestratorView.ts / projectFilter.ts.
import type { DashboardSession, OrchestratorLink } from "../api";
import { isSessionGone, shouldKeepPolling } from "../connectionError";
import type { ConnStatus } from "../store";

export type RouteSession = DashboardSession | OrchestratorLink;

/** What `GET /sessions/{id}` has said about an id the board's lists do not hold. */
export type SessionLookup =
	| { state: "pending" }
	| { state: "found"; session: DashboardSession }
	| { state: "failed"; status: number | undefined };

/** A lookup together with the machine and id it was asked about. */
export type KeyedSessionLookup = { key: string; lookup: SessionLookup };

export type SessionRouteView =
	| { kind: "screen"; session: RouteSession }
	| { kind: "loading" }
	| { kind: "unpaired" }
	| { kind: "offline" }
	| { kind: "ended" }
	| { kind: "missing" }
	| { kind: "failed" };

const pending: SessionLookup = { state: "pending" };

export function sessionLookupKey(machine: string, id: string): string {
	return `${machine}|${id}`;
}

/**
 * The stored lookup, if it was asked about this machine and id. A response for
 * the session the screen showed before a param change, or from the machine the
 * phone was paired with before, is not an answer about this one.
 */
export function currentSessionLookup(stored: KeyedSessionLookup | null, key: string): SessionLookup {
	return stored !== null && stored.key === key ? stored.lookup : pending;
}

/**
 * Whether the daemon has answered for this id, so there is nothing to ask again.
 * A failure that never reached it, or that it answered with anything but a
 * 404/410, is still an open question.
 */
export function sessionLookupSettled(lookup: SessionLookup): boolean {
	return lookup.state === "found" || (lookup.state === "failed" && isSessionGone(lookup.status));
}

/**
 * Whether the route should send `GET /sessions/{id}` now.
 *
 * Only while the board's own poll holds an open connection to this machine.
 * Asking on "closed" as well cost an extra 401 on every app switch under a
 * rotated password. "open" is still only a tick's result, though: it can be a
 * poll interval behind a regenerated password, and a late tick from the machine
 * the phone just left can set it (fetchAll has no staleness guard). So a lookup
 * can still be rejected. `sessionRouteView` offers no Retry for a rejection, so a
 * tap cannot repeat it; the route asks again when `connection` next turns "open".
 *
 * `appActive`, because a tick already in flight when the app is backgrounded
 * still lands and sets "open", and every request carries the install id once it
 * is primed (`api.ts`, `cachedInstallId`), which keeps the device live on the
 * desktop after the user left (see `pollActiveRef` in store.tsx).
 *
 * `machineChanged` is true in the render where the paired machine changed. The
 * store restarts its poll from an effect that runs after this route's (React runs
 * a child's effects first), so in that render `connection` still describes the
 * previous machine.
 */
export function sessionLookupDue(args: {
	listed: boolean;
	configured: boolean | null;
	connection: ConnStatus;
	appActive: boolean;
	machineChanged: boolean;
	lookup: SessionLookup;
}): boolean {
	if (args.listed || args.configured !== true) return false;
	if (!args.appActive || args.machineChanged || args.connection !== "open") return false;
	return !sessionLookupSettled(args.lookup);
}

/**
 * The board's lists are the fast path, and they stay authoritative whenever they
 * hold the id: they are refreshed on every poll, a one-off lookup is not.
 *
 * They do not hold every session. `getSessions` keeps one orchestrator per
 * project (`api.ts`, `bestByProject`: the first live one in session-number order,
 * else the newest), so every other orchestrator is dropped, and a `needs_input`
 * notification from one of those still routes here. So a miss is only a
 * question — the daemon answers it, and only a 404/410 is "not found".
 *
 * An unlisted session that has ended is reported as ended, not opened. Workers
 * are listed whether or not they have ended, so what reaches this is an
 * orchestrator the list dropped — and the session screens offer Restore on an
 * ended session. A restored old orchestrator would be the first live one in
 * session-number order, which is the one `bestByProject` keeps for the phone and
 * the one the daemon names as orchestrator in new workers' prompts
 * (`activeOrchestratorSessionID`), displacing the orchestrator that replaced it.
 *
 * While the board is not connected, the route does not diagnose the link itself.
 * Telling a rotated tunnel from a rejected password from an unreachable machine
 * is the board's rule: `app/(tabs)/index.tsx` composes `describeConnectionFailure`
 * (`connectionError.ts`) with `tunnelMayHaveRotated` (`staleTunnel.ts`) over the
 * store's `errorStatus` and `activeEndpoints`, which this route does not hold, and
 * a second copy would drift. A rejected lookup (401/403/429) goes to that same
 * state instead of offering a retry: retrying a rejected password is what arms
 * the daemon's lockout, and retrying under one cannot succeed (`shouldKeepPolling`).
 *
 * `configured` is `null` until the store has resolved the saved machine's
 * endpoint; it is checked before the lists, which still hold a forgotten machine's
 * sessions. `loading` is the store's: true from each start of its poll until that
 * poll's first tick.
 */
export function sessionRouteView(args: {
	listed: RouteSession | undefined;
	configured: boolean | null;
	connection: ConnStatus;
	loading: boolean;
	lookup: SessionLookup;
}): SessionRouteView {
	if (args.configured === null) return { kind: "loading" };
	if (!args.configured) return { kind: "unpaired" };
	if (args.listed) return { kind: "screen", session: args.listed };
	const { lookup } = args;
	if (lookup.state === "found") {
		return lookup.session.isTerminated ? { kind: "ended" } : { kind: "screen", session: lookup.session };
	}
	if (lookup.state === "failed" && isSessionGone(lookup.status)) return { kind: "missing" };
	if (args.connection !== "open") {
		return args.loading || args.connection === "connecting" ? { kind: "loading" } : { kind: "offline" };
	}
	if (lookup.state === "pending") return { kind: "loading" };
	return shouldKeepPolling(lookup.status) ? { kind: "failed" } : { kind: "offline" };
}
