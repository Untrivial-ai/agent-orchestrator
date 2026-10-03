/**
 * Lists the signed-in user's coding-agent connections (GET /me/providers).
 * Cloud agent credentials are personal: each user logs in to their harnesses
 * from the Harnesses settings page, and the connection runs their cloud
 * sessions in every org they belong to.
 */

import { useQuery } from "@tanstack/react-query";
import type { CloudCpProviderConnection } from "../lib/cloud-cp";
import { CLOUD_AGENT_PROVIDERS } from "../lib/cloud-agents";
import { useCloudCp } from "./useCloudCp";

export const providerConnectionsQueryKey = ["cloud-provider-connections"] as const;

export function useProviderConnections() {
	const { client, ready } = useCloudCp();
	return useQuery({
		queryKey: providerConnectionsQueryKey,
		enabled: ready,
		staleTime: 60_000,
		queryFn: async (): Promise<CloudCpProviderConnection[]> =>
			(await client.listUserProviderConnections()).providerConnections,
	});
}

/**
 * True when at least one coding-agent connection the control plane validated
 * exists. The personal list also holds non-agent credentials (a GitHub token),
 * which do not count.
 */
export function hasValidAgentConnection(connections: CloudCpProviderConnection[] | undefined): boolean {
	return (connections ?? []).some(
		(connection) =>
			connection.validationState === "valid" &&
			(CLOUD_AGENT_PROVIDERS as readonly string[]).includes(connection.provider),
	);
}
