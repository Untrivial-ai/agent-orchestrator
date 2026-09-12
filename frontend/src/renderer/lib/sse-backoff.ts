// Shared reconnect pacing for the renderer's SSE consumers (#4323).
//
// EventSource rebuilds are scheduled by hand once the browser gives up
// (readyState CLOSED). A flat delay meant every stream in the app knocked on
// the same cadence forever: when the daemon returns a transient 5xx while
// setting a stream up — e.g. under the DB-lock contention in #3963 — all of
// them re-fail together, and the retry traffic is itself what keeps the daemon
// from recovering. Growing the delay gives it room, and the jitter spreads
// streams that failed in lockstep so they stop arriving as one burst.
//
// Mirrors the bounded backoff the main process already uses for its daemon
// links (see main/supervisor-link.ts), with a ceiling suited to a stream a
// user is waiting on rather than a socket held for the process lifetime.

/** Delay after the first failure, before growth or jitter. */
export const SSE_BACKOFF_INIT_MS = 5_000;
/** Ceiling for the grown delay. Jitter still applies below it. */
export const SSE_BACKOFF_MAX_MS = 60_000;

// 2^31 * 5s is already far past the ceiling; clamping keeps the exponent
// finite so a long-dead stream cannot reach Infinity or NaN.
const MAX_EXPONENT = 31;

/**
 * Delay before the next reconnect attempt, given how many consecutive
 * failures the stream has seen since it last opened successfully.
 *
 * Doubles from {@link SSE_BACKOFF_INIT_MS} to {@link SSE_BACKOFF_MAX_MS}, then
 * scales by jitter in `[0.5, 1)` so concurrent streams desynchronise. A
 * `failures` below 1, or not a finite number, is treated as the first failure.
 *
 * @param random Injectable for tests; defaults to `Math.random`.
 */
export function computeSseRetryDelayMs(failures: number, random: () => number = Math.random): number {
	const attempt = Number.isFinite(failures)
		? Math.min(Math.max(Math.floor(failures), 1), MAX_EXPONENT)
		: 1;
	const step = Math.min(SSE_BACKOFF_INIT_MS * 2 ** (attempt - 1), SSE_BACKOFF_MAX_MS);
	// Clamp the source too: a stub returning outside [0, 1) must not push the
	// delay past the ceiling or collapse it to zero.
	const jitter = 0.5 + Math.min(Math.max(random(), 0), 0.999_999) * 0.5;
	return Math.round(step * jitter);
}
