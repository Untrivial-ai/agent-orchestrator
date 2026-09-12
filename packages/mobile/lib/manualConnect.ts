import type { ServerConfig } from "./config";
import type { Endpoint } from "./endpoints";
import type { Host } from "./hosts";

export type AdoptManualDeps = {
	/** The machine's reported host id, or "" when it does not report one. */
	identity: (cfg: ServerConfig) => Promise<string>;
	saveHost: (host: Host) => Promise<void>;
	setActiveHost: (id: string) => Promise<void>;
};

/**
 * Turns a hand-entered address into a paired machine.
 *
 * ManualConnectSheet used to write only the legacy ServerConfig. That worked
 * when there was one server; with a host list it does not, because resolution
 * reconnects the *active machine* on the next launch and migration skips once
 * any host exists. The manual connection was therefore replaced by whatever was
 * there before, seconds after the user made it.
 *
 * Storing it and selecting it is the whole fix: the address the user typed is a
 * machine like any other, and the one they just chose to talk to.
 */
export async function adoptManualConnection(
	cfg: ServerConfig,
	deps: AdoptManualDeps,
): Promise<string> {
	const hostId = await deps.identity(cfg);
	await deps.saveHost({
		id: hostId,
		name: cfg.host,
		platform: "",
		endpoints: [manualEndpoint(cfg)],
		token: cfg.password,
		lastConnected: Date.now(),
	});
	// Even without an id: the selection is by list position, and leaving it
	// unset would resolve back to the previous machine on the next launch.
	await deps.setActiveHost(hostId);
	return hostId;
}

/**
 * A typed address is one endpoint, not a list — the daemon's own list replaces
 * this on the first successful connect. TLS by hand means the tailnet path;
 * everything else was plain LAN, which mirrors how migration reads the old
 * single-server config.
 */
function manualEndpoint(cfg: ServerConfig): Endpoint {
	const secure = cfg.secure === true;
	return {
		kind: secure ? "tailscale" : "lan",
		host: cfg.host.trim(),
		port: Number(cfg.httpPort) || 3011,
		secure,
	};
}
