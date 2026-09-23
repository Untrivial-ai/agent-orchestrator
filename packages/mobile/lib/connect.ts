import { DEFAULT_CONFIG, type ServerConfig } from "./config";
import type { Endpoint } from "./endpoints";
import { mergeEndpoints } from "./mergeEndpoints";
import type { Host } from "./hosts";
import type { RaceOutcome } from "./race";

/**
 * Adapts a won endpoint into the shape the API layer already speaks.
 *
 * Everything downstream — api.ts, mux.ts, the terminal — is built on
 * ServerConfig, so the race hands back one of those rather than threading a new
 * type through every call site.
 */
export function configForEndpoint(endpoint: Endpoint, token: string, hostId = ""): ServerConfig {
	return {
		...DEFAULT_CONFIG,
		host: endpoint.host,
		httpPort: String(endpoint.port),
		secure: endpoint.secure,
		password: token,
		// Carried so per-machine state — the chat event cursor above all — keys on
		// the machine rather than whichever address won the race.
		...(hostId ? { hostId } : {}),
		// Carried so the poll can speed up on paths where the event stream
		// cannot deliver. See pollInterval.ts.
		endpointKind: endpoint.kind,
	};
}

export type ConnectResult =
	| { ok: true; config: ServerConfig; endpoint: Endpoint; hostId: string }
	| { ok: false; reason: "unknown-host" | "no-candidates" | "none-reachable" };

export type ConnectDeps = {
	findHost: (id: string) => Promise<Host | null>;
	race: (host: Host) => Promise<RaceOutcome>;
	/** Asks the daemon what it advertises now, over the endpoint that just won. */
	refreshEndpoints: (config: ServerConfig) => Promise<Endpoint[]>;
	saveEndpoints: (hostId: string, endpoints: Endpoint[]) => Promise<void>;
	/** Records the identity a machine reported that had none stored. */
	adoptIdentity: (oldId: string, hostId: string) => Promise<void>;
	touch: (hostId: string) => Promise<void>;
};

/**
 * Connects to a paired machine: race its endpoints, then refresh what we know
 * about it from whichever one won.
 *
 * The refresh is what makes the pairing self-healing. A tunnel hostname that
 * rotated, a LAN address that changed with the lease, a machine that gained
 * Tailscale — all of it is picked up on the next successful connect without the
 * user re-pairing.
 */
export async function connectHost(id: string, deps: ConnectDeps): Promise<ConnectResult> {
	const host = await deps.findHost(id);
	if (!host) return { ok: false, reason: "unknown-host" };

	const outcome = await deps.race(host);
	if (!outcome.ok) return { ok: false, reason: outcome.reason };

	const hostKeyForConfig = host.id === "" ? outcome.hostId : host.id;
	const config = configForEndpoint(outcome.endpoint, host.token, hostKeyForConfig);

	// A machine migrated from the single-server config has no id until it
	// connects once. This is where it learns one, and it is verified from here on.
	if (host.id === "" && outcome.hostId !== "") {
		await deps.adoptIdentity(host.id, outcome.hostId);
	}

	const hostKey = host.id === "" ? outcome.hostId : host.id;

	try {
		// Merged, not replaced: a kind the daemon omits — a tunnel mid-restart,
		// above all — is unknown rather than gone. See mergeEndpoints.
		await deps.saveEndpoints(
			hostKey,
			mergeEndpoints(host.endpoints, await deps.refreshEndpoints(config)),
		);
	} catch {
		// A failed refresh must not cost us a working connection: the endpoints
		// already stored are what got us here and remain good enough.
	}
	await deps.touch(hostKey);

	return { ok: true, config, endpoint: outcome.endpoint, hostId: hostKey };
}
