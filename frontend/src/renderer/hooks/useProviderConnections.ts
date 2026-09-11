/**
 * Lists the org's coding-agent provider connections (GET
 * /orgs/{orgId}/provider-connections). Used by the onboarding gate to decide
 * whether to prompt for a credential, and invalidated by the credential dialog
 * after a successful connect.
 */

import { useQuery } from "@tanstack/react-query";
import type { CloudCpProviderConnection } from "../lib/cloud-cp";
import { useCloudCp } from "./useCloudCp";

export function providerConnectionsQueryKey(orgId: string) {
	return ["cloud-provider-connections", orgId] as const;
}

export function useProviderConnections(orgId: string | undefined) {
	const { client, ready } = useCloudCp();
	return useQuery({
		queryKey: providerConnectionsQueryKey(orgId ?? ""),
		enabled: ready && orgId !== undefined,
		staleTime: 60_000,
		queryFn: async (): Promise<CloudCpProviderConnection[]> => {
			const { providerConnections } = await client.listProviderConnections(orgId as string);
			return providerConnections;
		},
	});
}

export const userProviderConnectionsQueryKey = ["cloud-user-provider-connections"] as const;

export function useUserProviderConnections(enabled = true) {
	const { client, ready } = useCloudCp();
	return useQuery({
		queryKey: userProviderConnectionsQueryKey,
		enabled: ready && enabled,
		staleTime: 60_000,
		queryFn: async (): Promise<CloudCpProviderConnection[]> => {
			const { providerConnections } = await client.listUserProviderConnections();
			return providerConnections;
		},
	});
}

/** True when the org has at least one connection the control plane validated. */
export function hasValidAgentConnection(connections: CloudCpProviderConnection[] | undefined): boolean {
	return (connections ?? []).some(
		(connection) =>
			(connection.provider === "claude-code" || connection.provider === "codex" || connection.provider === "cursor") &&
			connection.validationState === "valid",
	);
}
