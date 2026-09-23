import { STALE_AFTER_MS } from "./screenState";
import { useNow } from "./useNow";

/**
 * How often staleness is re-evaluated.
 *
 * Coarse on purpose. Every component that calls this hook re-renders on each
 * tick, so the interval is the price of the banner: 15s means a list goes from
 * "live" to "stale" within 15s of crossing the threshold, which is well inside
 * the time it takes someone to read a row and act on it, while costing four
 * renders a minute in one small component rather than in the board.
 */
export const STALENESS_TICK_MS = 15_000;

export type Staleness = {
	/** Whether the data on screen has outlived the threshold. */
	stale: boolean;
	/** Age of the data in milliseconds. 0 when nothing has ever synced. */
	ageMs: number;
};

/**
 * How old the data on screen is.
 *
 * Takes a getter rather than a timestamp so the store can keep the value in a
 * ref: a timestamp passed through context would change identity on every
 * successful poll and re-render every consumer. The getter is stable, so only
 * the component that calls this hook re-renders, and only on the tick.
 *
 * Call this from the banner, never from a screen that renders a list — the
 * whole point of the ref is that the list does not re-render on a clock.
 */
export function useStaleness(getLastSyncAt: () => number, thresholdMs = STALE_AFTER_MS): Staleness {
	const now = useNow(STALENESS_TICK_MS);
	const lastSyncAt = getLastSyncAt();

	// Nothing has ever landed, so there is no data to call stale. The screen is
	// in its loading or error state, which already says more than a banner could.
	if (!lastSyncAt) return { stale: false, ageMs: 0 };

	// Clamped because a device clock that moves backwards (NTP correction, manual
	// change) would otherwise report a negative age and read as freshly synced.
	const ageMs = Math.max(0, now - lastSyncAt);
	return { stale: ageMs >= thresholdMs, ageMs };
}
