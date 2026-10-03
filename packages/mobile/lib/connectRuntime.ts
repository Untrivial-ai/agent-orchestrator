/**
 * Production wiring for the endpoint race.
 *
 * UNVERIFIED. Everything this file calls is unit-tested — race.ts, hosts.ts,
 * connect.ts, connectionAction.ts — but this glue itself is not: it needs a
 * real device or simulator, because it depends on React Native's fetch,
 * AbortSignal and timer behaviour rather than the Node equivalents vitest
 * provides. Treat the shapes as correct and the runtime behaviour as claimed
 * until it has been run. See docs for the local end-to-end check.
 */
import { type ConnectDeps, connectHost, type ConnectResult } from "./connect";
import type { ServerConfig } from "./config";
import { type Endpoint, endpointBaseUrl } from "./endpoints";
import { shouldRetryProbe, TUNNEL_PROBE_RETRY_DELAY_MS } from "./probeRetry";
import { adoptHostIdentity, findHost, touchHost, updateHostEndpoints } from "./hosts";
import { IncompatibleHostVersionError, type ProbeAnswer, raceEndpoints } from "./race";

function identityHost(body: { hostId?: unknown; apiVersion?: unknown }): string {
	const hostId = typeof body.hostId === "string" ? body.hostId : "";
	if (hostId && body.apiVersion !== 1) throw new IncompatibleHostVersionError(hostId);
	return hostId;
}

/** How long a single endpoint gets to identify itself.
 *
 * Short on purpose. Every candidate is probed at once, so this is the ceiling
 * on the whole race, and a dead LAN address is the common case after changing
 * networks — waiting on it is exactly what the race exists to avoid. */
export const PROBE_TIMEOUT_MS = 3_000;

/**
 * Asks an endpoint which machine it is.
 *
 * Unauthenticated by design: the answer is what decides whether this endpoint
 * is safe to send a credential to, so it has to come first. See
 * docs/adr/0003-unauthenticated-identity-probe.md.
 */
export async function probeEndpoint(endpoint: Endpoint, signal: AbortSignal): Promise<ProbeAnswer> {
	// A tunnel hostname may simply not have propagated yet; see probeRetry.
	const startedAt = Date.now();
	for (;;) {
		try {
			return await probeOnce(endpoint, signal);
		} catch (e) {
			if (signal.aborted) throw e; // The race already has a winner.
			if (e instanceof IncompatibleHostVersionError) throw e;
			if (!shouldRetryProbe(endpoint.kind, Date.now() - startedAt)) throw e;
			await waitOrAbort(TUNNEL_PROBE_RETRY_DELAY_MS, signal);
		}
	}
}

/** Resolves when the delay elapses, or rejects as soon as the race is over. */
function waitOrAbort(ms: number, signal: AbortSignal): Promise<void> {
	return new Promise((resolve, reject) => {
		const timer = setTimeout(() => {
			signal.removeEventListener("abort", onAbort);
			resolve();
		}, ms);
		function onAbort() {
			clearTimeout(timer);
			reject(new Error("probe aborted"));
		}
		signal.addEventListener("abort", onAbort, { once: true });
	});
}

async function probeOnce(endpoint: Endpoint, signal: AbortSignal): Promise<ProbeAnswer> {
	const controller = new AbortController();
	const timeout = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
	// Abort when either the race picked a winner or our own timeout fired.
	const onOuterAbort = () => controller.abort();
	signal.addEventListener("abort", onOuterAbort);
	try {
		const res = await fetch(`${endpointBaseUrl(endpoint)}/api/v1/identity`, {
			method: "GET",
			signal: controller.signal,
		});
		if (!res.ok) throw new Error(`identity probe returned ${res.status}`);
		const hostId = identityHost((await res.json()) as { hostId?: unknown; apiVersion?: unknown });
		if (!hostId) {
			throw new Error("identity probe returned no host id");
		}
		return { hostId };
	} finally {
		clearTimeout(timeout);
		signal.removeEventListener("abort", onOuterAbort);
	}
}

/**
 * The host id at a plain address, for a connection made by hand rather than by
 * scanning. Same endpoint as the race's probe, without a candidate to race.
 */
export async function probeIdentity(cfg: {
	host: string;
	httpPort: string;
	secure?: boolean;
}): Promise<string> {
	const base = `${cfg.secure ? "https" : "http"}://${cfg.host}:${cfg.httpPort}`;
	const res = await fetch(`${base}/api/v1/identity`, { method: "GET" });
	if (!res.ok) throw new Error(`identity probe returned ${res.status}`);
	return identityHost((await res.json()) as { hostId?: unknown; apiVersion?: unknown });
}

/** On a host mismatch (or ambiguous auth rejection), re-race verified endpoints. */
export async function rejectedEndpointNeedsRace(cfg: ServerConfig, status: number | undefined): Promise<boolean> {
	// A 429 is a lockout, and an unidentified legacy config cannot be checked.
	if (!cfg.hostId) return false;
	if (status === 421) return true;
	if (status !== 401 && status !== 403) return false;
	try {
		// The rejected URL just answered, so one bounded probe is enough here.
		const answer = await probeOnce({
			kind: cfg.endpointKind ?? "lan",
			host: cfg.host,
			port: Number(cfg.httpPort),
			secure: !!cfg.secure,
		}, new AbortController().signal);
		return answer.hostId !== cfg.hostId;
	} catch {
		return true;
	}
}

/**
 * How long the post-race endpoint refresh may take.
 *
 * The refresh runs inside the launch resolution, so without a bound a slow
 * network held the app in "connecting" for as long as the OS let a request
 * hang — after the race had already found a working endpoint. A refresh that
 * does not land in time is simply skipped; the stored endpoints still work.
 */
export const ENDPOINT_REFRESH_TIMEOUT_MS = 5_000;

/**
 * Re-reads what the daemon advertises now.
 *
 * Deliberately GET /api/v1/endpoints and not /api/v1/mobile/status: the mobile
 * control routes are 404'd on the LAN listener, which is the only listener a
 * phone can reach.
 */
async function fetchAdvertisedEndpoints(config: ServerConfig): Promise<Endpoint[]> {
	const controller = new AbortController();
	const timeout = setTimeout(() => controller.abort(), ENDPOINT_REFRESH_TIMEOUT_MS);
	try {
		const base = `${config.secure ? "https" : "http"}://${config.host}:${config.httpPort}`;
		const res = await fetch(`${base}/api/v1/endpoints`, {
			headers: {
				...(config.password ? { Authorization: `Bearer ${config.password}` } : {}),
				...(config.hostId ? { "X-AO-Expected-Host-ID": config.hostId } : {}),
			},
			signal: controller.signal,
		});
		if (!res.ok) throw new Error(`endpoint refresh returned ${res.status}`);
		const body = (await res.json()) as { endpoints?: Endpoint[] };
		return body.endpoints ?? [];
	} finally {
		clearTimeout(timeout);
	}
}

export type ConnectOptions = {
	/**
	 * Whether to re-read the daemon's advertised endpoints after the race. On by
	 * default. The refresh is authenticated, so a stale password spends a failed
	 * attempt towards the daemon's lockout; a caller about to send its own
	 * authenticated request (Settings' Test connection) turns it off so one tap
	 * costs one attempt.
	 */
	refreshEndpoints?: boolean;
};

/** The production dependency set for connectHost. */
export function runtimeConnectDeps(options: ConnectOptions = {}): ConnectDeps {
	return {
		findHost,
		race: (host) => raceEndpoints(host.endpoints, host.id, probeEndpoint),
		// Skipped as nothing advertised: merged, that leaves the stored list as is.
		refreshEndpoints: (config) =>
			options.refreshEndpoints === false
				? Promise.resolve([])
				: fetchAdvertisedEndpoints(config),
		saveEndpoints: updateHostEndpoints,
		adoptIdentity: adoptHostIdentity,
		touch: touchHost,
	};
}

/** Connects to a paired machine using the real network and storage. */
export function connectToHost(hostId: string, options?: ConnectOptions): Promise<ConnectResult> {
	return connectHost(hostId, runtimeConnectDeps(options));
}
