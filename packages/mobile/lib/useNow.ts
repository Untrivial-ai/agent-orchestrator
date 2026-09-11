import { useEffect, useState } from "react";
import { AppState } from "react-native";

import { shouldPoll } from "./appStatePoll";

/**
 * A clock for text that goes stale on its own — relative timestamps.
 *
 * One interval per screen rather than one per row: pass `now` down and let each
 * row compute its own label. `interval` should be the coarsest step the label
 * can take; a label can trail its true value by up to one tick.
 *
 * The clock stops while the app is backgrounded, on the same predicate as the
 * board poll. React Native does not suspend JS timers for us: iOS hands pending
 * timers to an NSTimer on the way out (RCTTiming's sleep timer) and Android's
 * JS thread never stops, so a clock left running would redraw every row of a
 * screen nobody is looking at, once a tick, for as long as the app lives.
 */
export function useNow(interval: number): number {
	const [now, setNow] = useState(() => Date.now());

	useEffect(() => {
		const tick = () => setNow(Date.now());
		// Undefined while the clock is stopped for the background. Kept in the
		// effect rather than in state so that leaving costs no render at all.
		let id: ReturnType<typeof setInterval> | undefined;
		const stop = () => {
			if (id !== undefined) clearInterval(id);
			id = undefined;
		};
		if (shouldPoll(AppState.currentState)) id = setInterval(tick, interval);
		const sub = AppState.addEventListener("change", (state) => {
			if (!shouldPoll(state)) {
				stop();
			} else if (id === undefined) {
				// Back from the background: the labels may be several ticks stale,
				// so read the clock now rather than at the first tick. `inactive`
				// never lands here — the timer kept running through it.
				tick();
				id = setInterval(tick, interval);
			}
		});
		return () => {
			stop();
			sub.remove();
		};
	}, [interval]);

	return now;
}

/** The step `relativeTime` moves in below an hour. */
export const MINUTE_MS = 60_000;
