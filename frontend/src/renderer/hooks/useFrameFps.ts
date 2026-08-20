import { useEffect, useRef, useState } from "react";

/**
 * Live frame-rate estimate for the emulator stream.
 *
 * Counts frame arrivals over a sliding one-second window and decays to 0 once
 * frames stop arriving, so the badge shows the actual delivery rate (including
 * stalls) rather than an ever-growing average. The identity of `frame` is the
 * signal: every committed stream/screenshot frame triggers a recompute.
 */
export function useFrameFps(frame: { data: string; mimeType: string } | null): number {
	const arrivals = useRef<number[]>([]);
	const [fps, setFps] = useState(0);

	useEffect(() => {
		if (!frame) return;
		const now = Date.now();
		const recent = [...arrivals.current, now].filter((stamp) => now - stamp <= 1000);
		arrivals.current = recent;
		setFps(recent.length);
		// `frame` identity carries the update; its payload only matters for
		// deciding whether a frame arrived.
	}, [frame]);

	// Decay the count when frames stop so a stalled stream reads 0 instead of
	// a stale "last burst" figure.
	useEffect(() => {
		const id = setInterval(() => {
			const now = Date.now();
			arrivals.current = arrivals.current.filter((stamp) => now - stamp <= 1000);
			setFps(arrivals.current.length);
		}, 500);
		return () => clearInterval(id);
	}, []);

	return fps;
}