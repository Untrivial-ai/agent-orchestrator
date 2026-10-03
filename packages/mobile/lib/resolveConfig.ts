import type { ConnectResult } from "./connect";
import { loadConfig, saveConfig, type ServerConfig } from "./config";
import type { Host } from "./hosts";
import { activeHost, migrateLegacyConfig } from "./hosts";
import { connectToHost, type ConnectOptions } from "./connectRuntime";
import { IncompatibleHostVersionError } from "./race";

export type ResolveDeps = {
	migrate: () => Promise<void>;
	/** The machine to talk to: an explicit selection, else the most recent. */
	activeHost: () => Promise<Host | null>;
	connect: (hostId: string) => Promise<ConnectResult>;
	loadLegacyConfig: () => Promise<ServerConfig>;
	/** Writes the winning endpoint back to storage. */
	persist: (config: ServerConfig) => Promise<void>;
};

/**
 * Works out which address the app should be talking to right now.
 *
 * Races the active machine's endpoints, so the app lands on whichever of its
 * addresses currently works — LAN at home, the tunnel from anywhere else —
 * without the user choosing. "Active" is an explicit selection where one has
 * been made, and the most recently used machine otherwise.
 *
 * An unreachable selected machine remains paired, but has no usable config:
 * reusing a cached address would send its credential without a fresh identity
 * check, possibly to a different machine now holding that address.
 */
export async function resolveActiveConfig(deps: ResolveDeps): Promise<ServerConfig | null> {
	let host: Host | null;
	try {
		// Before looking for machines, bring any pre-existing single-server
		// pairing into the list — otherwise an upgrading user looks unpaired.
		await deps.migrate();
		host = await deps.activeHost();
	} catch {
		// We cannot know which machine was selected; do not guess from a
		// credential cached for some other machine.
		return null;
	}
	if (host) {
		try {
			const result = await deps.connect(host.id);
			if (result.ok) {
				// Persist the winner. Long-lived surfaces — the terminal mux above
				// all — read the stored config directly rather than the store's
				// copy, so without this a phone that raced onto Tailscale after
				// losing Wi-Fi would leave them pointed at the dead LAN address:
				// REST recovers, the terminal does not.
				await deps.persist(result.config);
				return result.config;
			}
			if (result.reason === "incompatible") throw new IncompatibleHostVersionError(host.id);
		} catch (error) {
			if (error instanceof IncompatibleHostVersionError) throw error;
			// An unavailable selected host must not silently become another host.
		}
	}
	return host ? null : await deps.loadLegacyConfig();
}

/** The production dependency set. */
export function runtimeResolveDeps(options?: ConnectOptions): ResolveDeps {
	return {
		migrate: migrateLegacyConfig,
		activeHost,
		connect: (hostId) => connectToHost(hostId, options),
		loadLegacyConfig: loadConfig,
		persist: saveConfig,
	};
}
