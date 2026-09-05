import { useEffect, useState } from "react";
import { AppState } from "react-native";

/**
 * A clock for text that goes stale on its own — relative timestamps.
 *
 * One interval per screen rather than one per row: pass `now` down and let each
 * row compute its own label. `interval` should be the coarsest step the label
 * can take; a label can trail its true value by up to one tick.
 */
export function useNow(interval: number): number {
	const [now, setNow] = useState(() => Date.now());

	useEffect(() => {
		const id = setInterval(() => setNow(Date.now()), interval);
		// Timers do not fire while backgrounded, so re-read on the way back in.
		const sub = AppState.addEventListener("change", (state) => {
			if (state === "active") setNow(Date.now());
		});
		return () => {
			clearInterval(id);
			sub.remove();
		};
	}, [interval]);

	return now;
}

/** The step `relativeTime` moves in below an hour. */
export const MINUTE_MS = 60_000;
