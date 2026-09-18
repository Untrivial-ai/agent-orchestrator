import { useQuery } from "@tanstack/react-query";
import type { CloudCpAvailableAgent } from "../lib/cloud-cp";
import { useCloudCp } from "./useCloudCp";

export function cloudAvailableAgentsQueryKey(orgId: string) {
	return ["cloud", "agents", "available", orgId] as const;
}

export function useCloudAvailableAgents(orgId: string | undefined, enabled = true) {
	const { client, ready } = useCloudCp();
	return useQuery({
		queryKey: cloudAvailableAgentsQueryKey(orgId ?? ""),
		enabled: enabled && ready && orgId !== undefined,
		queryFn: async (): Promise<CloudCpAvailableAgent[]> => {
			const response = await client.getAvailableAgents(orgId as string);
			return response.agents;
		},
	});
}
