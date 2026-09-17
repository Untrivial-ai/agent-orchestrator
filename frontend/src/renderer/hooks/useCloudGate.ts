/**
 * Daemon-resolved offering gates.
 *
 * While settings are still loading (or the daemon predates the gates), cloud
 * stays off and local stays on, so the UI never flashes cloud affordances at
 * a deployment that may not have them.
 */

import { useSettings } from "./useSettings";

export interface CloudGate {
	/** Whether the cloud offering is available (flag + entitled client + control plane). */
	cloudEnabled: boolean;
	/** Whether the local offering is available on this daemon. */
	localEnabled: boolean;
	/** Deployment client identity; empty when unset or still loading. */
	client: string;
}

export function useCloudGate(): CloudGate {
	const { settings } = useSettings();
	return {
		cloudEnabled: settings?.cloudEnabled ?? false,
		localEnabled: settings?.localEnabled ?? true,
		client: settings?.client ?? "",
	};
}
