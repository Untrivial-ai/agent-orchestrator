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
 * Accept the address exactly as people commonly copy it from the desktop.
 *
 * The form has separate host and port fields, but a copied address naturally
 * arrives as `192.168.1.42:3011`. Leaving the port in `host` makes callers build
 * `http://192.168.1.42:3011:3011`, which can sit pending until the request
 * timeout. Split an explicit port out while preserving the separate port when
 * the host does not include one.
 */
export function normalizeManualConfig(cfg: ServerConfig): ServerConfig {
	let address = cfg.host.trim().replace(/^[a-z][a-z0-9+.-]*:\/\//i, "");
	address = address.split(/[/?#]/, 1)[0] ?? "";

	let host = address;
	let httpPort = cfg.httpPort.trim();
	if (address.startsWith("[")) {
		const bracket = address.indexOf("]");
		if (bracket !== -1) {
			host = address.slice(0, bracket + 1);
			const explicitPort = address.slice(bracket + 1).match(/^:(\d+)$/)?.[1];
			if (explicitPort) httpPort = explicitPort;
		}
	} else {
		const colon = address.lastIndexOf(":");
		// Only split a single colon. Unbracketed IPv6 has several and should be
		// left intact rather than being mistaken for host:port.
		if (colon > 0 && address.indexOf(":") === colon) {
			const explicitPort = address.slice(colon + 1);
			if (/^\d+$/.test(explicitPort)) {
				host = address.slice(0, colon);
				httpPort = explicitPort;
			}
		}
	}

	return { ...cfg, host, httpPort };
}

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
	const normalized = normalizeManualConfig(cfg);
	const secure = normalized.secure === true;
	return {
		kind: secure ? "tailscale" : "lan",
		host: normalized.host,
		port: Number(normalized.httpPort) || 3011,
		secure,
	};
}
