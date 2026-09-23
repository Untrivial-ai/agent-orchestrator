// What a list screen should be showing right now.
//
// Workers, Projects and PRs each re-derive this inline today, and they disagree:
// one offers a pairing button where the others offer nothing, and all three put
// their failure copy inside `ListEmptyComponent` — so a screen that already has
// rows keeps showing them with no hint that the poll behind them is failing. A
// populated list whose data is an hour old looks exactly like a live one.
//
// The fix is to make "stale" a state a screen can be in *while showing rows*,
// which is what `kind: "stale"` below is for. This module decides WHICH state;
// connectionError.ts still owns WHAT the words are.
//
// Free of React Native imports so it is unit-testable, the same split the
// codebase already uses for prView.ts and pushStatus.ts.

/**
 * How long after the last successful fetch a populated list stops being trusted.
 *
 * The board polls every 8s on a direct connection and every 2s through a tunnel,
 * so 30s is several missed ticks — long enough not to flicker on one dropped
 * request, short enough that nobody acts on a genuinely dead list.
 */
export const STALE_AFTER_MS = 30_000;

export type ScreenState =
	/** No desktop paired yet. The screen owns the whole viewport. */
	| { kind: "unconfigured" }
	/** First load, nothing cached to show yet. */
	| { kind: "loading" }
	/** The fetch failed and there is nothing cached to fall back on. */
	| { kind: "error" }
	/** The fetch succeeded and there is genuinely nothing to show. */
	| { kind: "empty" }
	/** Rows are on screen but the data behind them is no longer trustworthy. */
	| { kind: "stale"; ageMs: number }
	/** Rows are on screen and current. */
	| { kind: "ready" };

export type ScreenStateInput = {
	configured: boolean;
	loading: boolean;
	/** Rows currently renderable, cached ones included. */
	itemCount: number;
	/** Whether the last fetch attempt failed. */
	error: boolean;
	/** Milliseconds since the last successful fetch, or null if there never was one. */
	staleForMs?: number | null;
	staleAfterMs?: number;
};

/**
 * The screen's single state, in priority order.
 *
 * The ordering is the whole design:
 *
 *  - Pairing outranks everything — an unpaired app has nothing to be loading or
 *    failing about.
 *  - Loading only wins while there is nothing cached. Once rows exist, a refresh
 *    must never blank them out; that is what pull-to-refresh already indicates.
 *  - A failure with rows behind it is `stale`, not `error`. This is the case the
 *    screens get wrong today: showing the failure would throw away usable data,
 *    and showing nothing would hide the failure.
 *  - Staleness by age is checked even without an error, because a poll that
 *    silently stopped is indistinguishable from one that never fired.
 */
export function screenStateFor({
	configured,
	loading,
	itemCount,
	error,
	staleForMs = null,
	staleAfterMs = STALE_AFTER_MS,
}: ScreenStateInput): ScreenState {
	if (!configured) return { kind: "unconfigured" };

	const hasItems = itemCount > 0;

	if (loading && !hasItems) return { kind: "loading" };
	if (error && !hasItems) return { kind: "error" };
	if (error && hasItems) return { kind: "stale", ageMs: staleForMs ?? 0 };
	if (!hasItems) return { kind: "empty" };
	if (staleForMs !== null && staleForMs >= staleAfterMs) {
		return { kind: "stale", ageMs: staleForMs };
	}
	return { kind: "ready" };
}

/**
 * How old the data is, for a banner that has to say so in a few characters.
 *
 * Uses the same m/h/d vocabulary as relativeTime so the banner and the
 * timestamps in the rows beneath it do not disagree about how time is spelled.
 * Below a minute it says "moments" rather than "0m", because a banner claiming
 * zero age while telling you the data is stale reads as a bug.
 */
export function staleAgeLabel(ageMs: number): string {
	const secs = Math.max(0, Math.round(ageMs / 1000));
	if (secs < 60) return "moments ago";
	const mins = Math.floor(secs / 60);
	if (mins < 60) return `${mins}m ago`;
	const hours = Math.floor(mins / 60);
	if (hours < 24) return `${hours}h ago`;
	return `${Math.floor(hours / 24)}d ago`;
}

/** Whether the screen should render its list at all. */
export function showsList(state: ScreenState): boolean {
	return state.kind === "ready" || state.kind === "stale";
}

/** Whether a stale-data banner belongs above the list. */
export function showsStaleBanner(state: ScreenState): boolean {
	return state.kind === "stale";
}
