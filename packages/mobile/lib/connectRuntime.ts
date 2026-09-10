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
import { type Endpoint, endpointBaseUrl } from "./endpoints";
import { shouldRetryProbe, TUNNEL_PROBE_RETRY_DELAY_MS } from "./probeRetry";
import { adoptHostIdentity, findHost, touchHost, updateHostEndpoints } from "./hosts";
import { type ProbeAnswer, raceEndpoints } from "./race";

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
		const body = (await res.json()) as { hostId?: unknown };
		if (typeof body.hostId !== "string" || body.hostId === "") {
			throw new Error("identity probe returned no host id");
		}
		return { hostId: body.hostId };
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
	const body = (await res.json()) as { hostId?: unknown };
	return typeof body.hostId === "string" ? body.hostId : "";
}

/**
 * Re-reads what the daemon advertises now.
 *
 * Deliberately GET /api/v1/endpoints and not /api/v1/mobile/status: the mobile
 * control routes are 404'd on the LAN listener, which is the only listener a
 * phone can reach.
 */
async function fetchAdvertisedEndpoints(base: string, token: string): Promise<Endpoint[]> {
	const res = await fetch(`${base}/api/v1/endpoints`, {
		headers: token ? { Authorization: `Bearer ${token}` } : {},
	});
	if (!res.ok) throw new Error(`endpoint refresh returned ${res.status}`);
	const body = (await res.json()) as { endpoints?: Endpoint[] };
	return body.endpoints ?? [];
}

/** The production dependency set for connectHost. */
export function runtimeConnectDeps(): ConnectDeps {
	return {
		findHost,
		race: (host) => raceEndpoints(host.endpoints, host.id, probeEndpoint),
		refreshEndpoints: (config) =>
			fetchAdvertisedEndpoints(
				`${config.secure ? "https" : "http"}://${config.host}:${config.httpPort}`,
				config.password,
			),
		saveEndpoints: updateHostEndpoints,
		adoptIdentity: adoptHostIdentity,
		touch: touchHost,
	};
}

/** Connects to a paired machine using the real network and storage. */
export function connectToHost(hostId: string): Promise<ConnectResult> {
	return connectHost(hostId, runtimeConnectDeps());
}
