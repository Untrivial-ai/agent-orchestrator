import type { ConnectResult } from "./connect";
import { loadConfig, saveConfig, type ServerConfig } from "./config";
import type { Host } from "./hosts";
import { activeHost, migrateLegacyConfig } from "./hosts";
import { connectToHost } from "./connectRuntime";

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
 * Always returns something. The rest of the app is built around having a
 * config, so every failure path degrades to the last stored one rather than
 * returning nothing, which would look like being unpaired. A machine that
 * cannot be reached is a connection problem for the UI to report, not a reason
 * to forget it.
 */
export async function resolveActiveConfig(deps: ResolveDeps): Promise<ServerConfig | null> {
	try {
		// Before looking for machines, bring any pre-existing single-server
		// pairing into the list — otherwise an upgrading user looks unpaired.
		await deps.migrate();

		const host = await deps.activeHost();
		if (host) {
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
		}
	} catch {
		// Falling through to the stored config: a resolution failure must not
		// leave the app with no connection at all.
	}
	return await deps.loadLegacyConfig();
}

/** The production dependency set. */
export function runtimeResolveDeps(): ResolveDeps {
	return {
		migrate: migrateLegacyConfig,
		activeHost,
		connect: connectToHost,
		loadLegacyConfig: loadConfig,
		persist: saveConfig,
	};
}
