import { useCallback, useEffect, useRef, useState } from "react";
import { getSessionPR, type SessionPRSummary } from "./api";
import { useApp } from "./store";

// Lazy, cached loader for the rich PR view.
//
// The board poll (GET /sessions) carries PR *facts* only — number, state, ci,
// review, mergeability — with no title, branches, author or diff stats. Those
// live on GET /sessions/{id}/pr, one request per session.
//
// So it is fetched here rather than in the store: the store polls every 8s, and
// re-fetching N PR summaries on every tick would be a real battery and latency
// cost over Tailscale for data that changes on the order of minutes. Instead
// each session is fetched once, cached for the life of the screen, and only
// re-fetched when the user pulls to refresh.
//
// The cache lives in a ref, not state: the effect below reads "what do we
// already have" while it runs, and a state value there would be a stale closure
// whenever the session list changed mid-flight. Renders are driven by an
// explicit version bump instead.

// Enough parallelism to load a screenful quickly without opening thirty sockets
// at once on a phone.
const BATCH = 6;

export type PRSummaryLookup = {
	/** The rich summary for one PR, or undefined until it has loaded. */
	summaryFor(sessionId: string, number: number): SessionPRSummary | undefined;
	/** Re-fetch everything — wired to pull-to-refresh. */
	reload(): void;
};

export function usePRSummaries(sessionIds: string[]): PRSummaryLookup {
	const { config } = useApp();
	const cache = useRef<Record<string, SessionPRSummary[]>>({});
	const inFlight = useRef<Set<string>>(new Set());
	const [, bump] = useState(0);
	// Incremented by reload(). It is a dependency of the effect, which is what
	// makes a refresh actually re-run the fetch — clearing the cache alone did
	// nothing, because neither `key` nor `config` changes on a pull-to-refresh.
	const [generation, setGeneration] = useState(0);
	const fetchedGeneration = useRef(-1);

	// Keyed on the ids, not the array: a tick that changes any session field
	// yields a new array even when the id set is unchanged.
	const key = sessionIds.join(",");

	useEffect(() => {
		if (!config) return;
		// A reload re-fetches everything; otherwise only what was never loaded.
		const force = fetchedGeneration.current !== generation;
		fetchedGeneration.current = generation;
		const targets = sessionIds.filter(
			(id) => id && !inFlight.current.has(id) && (force || cache.current[id] === undefined),
		);
		if (targets.length === 0) return;
		for (const id of targets) inFlight.current.add(id);

		let cancelled = false;
		void (async () => {
			for (let i = 0; i < targets.length; i += BATCH) {
				if (cancelled) return;
				const chunk = targets.slice(i, i + BATCH);
				const results = await Promise.all(
					chunk.map(async (id) => {
						try {
							return [id, await getSessionPR(config, id)] as const;
						} catch {
							// null means "this attempt failed", which is not the same as
							// "this session has no PRs" — see below.
							return [id, null] as const;
						}
					}),
				);
				if (cancelled) return;
				for (const [id, prs] of results) {
					if (prs) cache.current[id] = prs;
					// A failed refresh keeps whatever was already on screen. Blanking the
					// cards back to their thin fallback because one request timed out is
					// worse than showing detail that is a few seconds stale.
					else cache.current[id] ??= [];
					inFlight.current.delete(id);
				}
				bump((n) => n + 1);
			}
		})();

		return () => {
			cancelled = true;
			// Release anything this run claimed but never resolved, or a re-run
			// would skip those sessions forever.
			for (const id of targets) inFlight.current.delete(id);
		};
		// `sessionIds` is covered by `key`; the cache is a ref by design.
	}, [key, config, generation]);

	const reload = useCallback(() => setGeneration((g) => g + 1), []);

	// Reads the ref, so it stays correct without being re-created; the bump above
	// is what re-renders the list to pick up new entries.
	const summaryFor = useCallback(
		(sessionId: string, number: number) => cache.current[sessionId]?.find((p) => p.number === number),
		[],
	);

	return { summaryFor, reload };
}
