import { queryOptions, useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

type Schemas = components["schemas"];
export type ProviderAccount = Schemas["ProviderAccountView"];
export type ProviderLogin = Schemas["ProviderLoginResponse"];
export type ProviderLoginRequest = Schemas["ProviderLoginRequest"];
export type AccountAction = Schemas["ProviderAccountAction"];
export const PROVIDERS = [
	{ id: "codex", name: "Codex", agent: "codex", agentName: "Codex", key: "sk-…", baseUrl: "https://api.openai.com/v1" },
	{ id: "claude", name: "Claude", agent: "claude-code", agentName: "Claude Code", key: "sk-ant-…", baseUrl: "https://api.anthropic.com" },
] as const;
export const providerAccountsKey = ["provider-accounts"] as const;
export const providerAccountsCatalogueKey = ["provider-accounts", "catalogue"] as const;
export const accountProvider = (agent: string) => PROVIDERS.find((provider) => provider.agent === agent)?.id ?? "";
// The model-catalogue scope of one account. Keep in sync with ports.ModelCatalogAccountScope in the daemon.
export const accountModelScope = (accountId: string) => `@account:${accountId}`;
export const percentLeft = (fraction: number) => Math.max(0, Math.min(100, Math.round(fraction * 100)));
// What is left of an account's shortest usage window, or null when not reported.
export function accountHeadroom(account: ProviderAccount): number | null {
	const window = account.usage?.status === "available" ? account.usage.windows?.[0] : undefined;
	return window ? percentLeft(window.remainingFraction) : null;
}
function unwrap<T>({ data, error }: { data?: T; error?: unknown }): T {
	if (error) throw Object.assign(new Error(apiErrorMessage(error)), { code: (error as { code?: string }).code });
	return data as T;
}
// refresh re-reads this computer's own logins and every account's sign-in state first.
export const fetchProviderAccounts = async (includeUsage = true, refresh = false) =>
	unwrap(await apiClient.GET("/api/v1/provider-accounts", { params: { query: { includeUsage, refresh } } }));
export function useProviderAccounts(enabled = true, includeUsage = true) {
	return useQuery({
		queryKey: includeUsage ? providerAccountsKey : providerAccountsCatalogueKey,
		queryFn: () => fetchProviderAccounts(includeUsage),
		enabled,
		refetchInterval: 30000,
		retry: 1,
	});
}
// One change to an account. The answer is the accounts without usage, plus resetOutcome for a reset.
export const accountAction = async (accountId: string, body: AccountAction) =>
	unwrap(await apiClient.POST("/api/v1/provider-accounts/{accountId}/actions", { params: { path: { accountId } }, body }));
export const sessionAccountQueryOptions = (sessionId: string) => queryOptions({
	queryKey: ["session-provider-account", sessionId],
	queryFn: async () => unwrap(await apiClient.GET("/api/v1/provider-accounts/sessions/{sessionId}", { params: { path: { sessionId } } })),
});
export const startProviderLogin = async (body: ProviderLoginRequest) => unwrap(await apiClient.POST("/api/v1/provider-accounts/login", { body }));
export const fetchProviderLogin = async (loginId: string) =>
	unwrap(await apiClient.GET("/api/v1/provider-accounts/login/{loginId}", { params: { path: { loginId } } }));
export const cancelProviderLogin = async (loginId: string) =>
	unwrap(await apiClient.DELETE("/api/v1/provider-accounts/login/{loginId}", { params: { path: { loginId } } }));
