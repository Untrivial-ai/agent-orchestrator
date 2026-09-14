import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workflowQueryKeys } from "./useWorkflowPlans";

type Provider = components["schemas"]["DomainProvider"];
type ProviderModel = components["schemas"]["DomainProviderModel"];

export function useProviders() {
	return useQuery({
		queryKey: workflowQueryKeys.providers(),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/providers");
			if (error) throw new Error(apiErrorMessage(error));
			return (data?.providers ?? []) as Provider[];
		},
	});
}

export function useProviderModels(providerId: string) {
	return useQuery({
		queryKey: workflowQueryKeys.providerModels(providerId),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/providers/{providerId}", {
				params: { path: { providerId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return (data?.models ?? []) as ProviderModel[];
		},
		enabled: !!providerId,
	});
}
