import { useQuery } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type ClaudeProfile = { name: string; configDir: string };

export function useClaudeProfiles(enabled = true) {
	return useQuery({
		queryKey: ["claude-profiles"],
		enabled,
		staleTime: 30_000,
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/agents/claude-code/profiles");
			if (error) throw new Error(apiErrorMessage(error));
			return data?.profiles ?? [];
		},
	});
}
