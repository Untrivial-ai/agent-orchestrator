import { endpointRank, type Endpoint, type EndpointKind } from "./endpoints";
import { RE_RACE_COOLDOWN_MS } from "./reRace";

/**
 * When to race again despite the current endpoint working fine.
 *
 * The failure-driven race in `reRace.ts` only ever moves the app *down* the
 * preference order: LAN dies, so pick up the tunnel. Nothing moves it back.
 * Observed on device — the phone held a Cloudflare connection for minutes with
 * a working LAN sitting unused, because a working endpoint never re-races.
 *
 * That is not just a wasted fast path. The tunnel poll is deliberately quick
 * (see pollInterval.ts), so staying there costs battery and cellular data; and
 * per docs/adr/0004-cloudflare-tunnel-for-remote-mobile-access.md the tunnel is
 * the one path a third party can observe, justified by it being used *only*
 * when nothing else works. Sticking to it after Wi-Fi returns quietly breaks
 * that.
 */

/**
 * How often to look for a better path while on a worse one.
 *
 * A minute is long enough that the extra races are negligible next to the 2s
 * tunnel poll they may end, and short enough that walking back into Wi-Fi
 * fixes itself before the user notices.
 */
export const UPGRADE_RACE_INTERVAL_MS = 60_000;

export type UpgradeRaceState = {
	/** Kind of the endpoint currently in use, or undefined for a migrated host. */
	currentKind: EndpointKind | undefined;
	/** Every endpoint the daemon last told us about. */
	known: Endpoint[];
	/** Epoch ms of the last race, however it was triggered. */
	lastRaceAt: number;
	now: number;
	/** The app just came back to the foreground. */
	resumed: boolean;
};

export function shouldRaceForUpgrade(state: UpgradeRaceState): boolean {
	// A host migrated from the single-server config has no recorded kind, so
	// there is nothing to compare against. Racing on a timer regardless would
	// churn the config object and tear down the live streams keyed on it.
	if (!state.currentKind) return false;

	const current = endpointRank(state.currentKind);
	const best = state.known.reduce(
		(rank, e) => Math.min(rank, endpointRank(e.kind)),
		Number.POSITIVE_INFINITY,
	);
	// Strictly better only. An unranked kind from a newer daemon sorts last, so
	// it never masquerades as an upgrade.
	if (best >= current) return false;

	// Waking up somewhere else is the case worth reacting to immediately: the
	// phone slept at home and woke on cellular, or the reverse. Waiting out a
	// full interval there leaves the app on the wrong path exactly while the
	// user is looking at it. The cooldown still applies, so flicking between
	// apps cannot thrash the connection.
	const since = state.now - state.lastRaceAt;
	return since >= (state.resumed ? RE_RACE_COOLDOWN_MS : UPGRADE_RACE_INTERVAL_MS);
}

/**
 * How often to evaluate the predicate above.
 *
 * The predicate does its own gating, so this only has to be fine-grained
 * enough not to add noticeable lag to whichever gate fires first.
 */
export const UPGRADE_RACE_CHECK_MS = 15_000;
