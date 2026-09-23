import { type Endpoint, endpointRank } from "./endpoints";

/** What an identity probe reports back about whoever answered. */
export type ProbeAnswer = { hostId: string };

export type ProbeFn = (endpoint: Endpoint, signal: AbortSignal) => Promise<ProbeAnswer>;

export type RaceOptions = {
	/**
	 * How long, after the first successful answer, to keep waiting for a
	 * better-ranked endpoint.
	 *
	 * Without it the race would have to wait for every probe, and one slow
	 * candidate — a sleeping host, a saturated link — would hold up every
	 * connect while a working tunnel sat idle. With it, preference wins when the
	 * difference is small and speed wins when it is not.
	 */
	graceMs?: number;
};

/** Long enough for a LAN answer to beat a tunnel that got a head start, short
 * enough that nobody notices it. */
export const DEFAULT_RACE_GRACE_MS = 250;

export type RaceOutcome =
	/** hostId is what actually answered, so a machine migrated without one can
	 * record the identity it just learned. */
	| { ok: true; endpoint: Endpoint; hostId: string }
	| { ok: false; reason: "no-candidates" | "none-reachable" };

/**
 * Probes every candidate at once and returns the best one that answers as the
 * machine we paired with.
 *
 * Concurrent rather than sequential: trying them in turn spends a full timeout
 * on a dead address while the user watches a spinner, and the dead address is
 * exactly what you have after changing networks.
 *
 * Every answer is checked against `expectedHostId` before the endpoint is
 * usable. A private address is not an identity — 192.168.1.42 exists on most
 * networks and is a different machine on each — so without this the app would
 * hand its bearer token to whatever device happens to hold the address it
 * remembers.
 *
 * An empty `expectedHostId` means the machine has no identity recorded yet: a
 * pairing migrated from the single-server config, which predates host ids. It
 * connects and adopts whatever answers, matching the behaviour before identity
 * existed, and is verified from its first connect onward.
 */
export async function raceEndpoints(
	endpoints: readonly Endpoint[],
	expectedHostId: string,
	probe: ProbeFn,
	options: RaceOptions = {},
): Promise<RaceOutcome> {
	if (endpoints.length === 0) return { ok: false, reason: "no-candidates" };
	const graceMs = options.graceMs ?? DEFAULT_RACE_GRACE_MS;

	const controller = new AbortController();
	let best: Endpoint | null = null;
	let bestHostId = "";
	let settled = 0;
	let done = false;
	let graceTimer: ReturnType<typeof setTimeout> | undefined;

	return new Promise<RaceOutcome>((resolve) => {
		const finish = () => {
			if (done) return;
			done = true;
			if (graceTimer) clearTimeout(graceTimer);
			// Whatever is still in flight has lost; stop it rather than leaving
			// sockets open on endpoints nobody is going to use.
			controller.abort();
			resolve(
				best
					? { ok: true, endpoint: best, hostId: bestHostId }
					: { ok: false, reason: "none-reachable" },
			);
		};

		for (const endpoint of endpoints) {
			probe(endpoint, controller.signal)
				.then((answer) => {
					// Answering as someone else is not an error to report — it is
					// simply not our machine, so the endpoint is discarded.
					if (expectedHostId !== "" && answer.hostId !== expectedHostId) return;
					if (!best || endpointRank(endpoint.kind) < endpointRank(best.kind)) {
						best = endpoint;
						bestHostId = answer.hostId;
					}
					// First success opens the grace window; a better-ranked answer
					// arriving inside it replaces this one.
					if (graceTimer === undefined) graceTimer = setTimeout(finish, graceMs);
					// Nothing ranked better can still arrive, so stop early.
					if (endpointRank(endpoint.kind) === 0) finish();
				})
				.catch(() => {})
				.finally(() => {
					settled += 1;
					if (settled === endpoints.length) finish();
				});
		}
	});
}
