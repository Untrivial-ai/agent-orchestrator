import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type ProviderQuota = components["schemas"]["ProviderQuotaResponse"];
export type QuotaHistoryPoint = components["schemas"]["QuotaHistoryPointResponse"];
export type QuotaAlert = components["schemas"]["QuotaAlertResponse"];

export const providerQuotaQueryKey = ["provider-quota"] as const;

async function fetchProviderQuota(): Promise<ProviderQuota[]> {
	const { data, error, response } = await apiClient.GET("/api/v1/usage/plans");
	if (error) throw new Error(apiErrorMessage(error, `Unable to load plan usage (${response.status})`));
	return data?.providers ?? [];
}

export function useProviderQuota() {
	return useQuery({
		queryKey: providerQuotaQueryKey,
		queryFn: fetchProviderQuota,
		refetchInterval: 60_000,
		retry: 1,
	});
}

export function useQuotaHistory(provider: string, accountId: string, enabled = true) {
	return useQuery({
		queryKey: [...providerQuotaQueryKey, provider, accountId, "history"],
		queryFn: async (): Promise<QuotaHistoryPoint[]> => {
			const { data, error, response } = await apiClient.GET(
				"/api/v1/usage/plans/{provider}/accounts/{accountId}/history",
				{ params: { path: { provider, accountId }, query: { hours: 168, limit: 500 } } },
			);
			if (error) throw new Error(apiErrorMessage(error, `Unable to load usage history (${response.status})`));
			return (data?.points ?? []).slice().reverse();
		},
		enabled,
		retry: 1,
		staleTime: 30_000,
	});
}

export function useRefreshProviderQuota(provider: string, accountId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async () => {
			const { data, error, response } = await apiClient.POST(
				"/api/v1/usage/plans/{provider}/accounts/{accountId}/refresh",
				{ params: { path: { provider, accountId } } },
			);
			if (error) throw new Error(apiErrorMessage(error, `Unable to refresh plan usage (${response.status})`));
			return data;
		},
		onSuccess: async () => {
			await queryClient.invalidateQueries({ queryKey: providerQuotaQueryKey });
		},
	});
}

export function useQuotaAlerts() {
	return useQuery({
		queryKey: [...providerQuotaQueryKey, "alerts"],
		queryFn: async (): Promise<QuotaAlert[]> => {
			const { data, error, response } = await apiClient.GET("/api/v1/usage/plans/alerts", {
				params: { query: { minutes: 10, limit: 100 } },
			});
			if (error) throw new Error(apiErrorMessage(error, `Unable to load quota alerts (${response.status})`));
			return data?.alerts ?? [];
		},
		refetchInterval: 30_000,
		retry: 1,
	});
}
