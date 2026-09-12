import { useEffect } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { codexAccountsQueryKey, mergeCodexAccounts, writeCodexAccounts } from "./codex-accounts-state";

export { codexAccountsQueryKey } from "./codex-accounts-state";

export type CodexAccountsResponse = components["schemas"]["CodexAccountsResponse"];
export type CodexAccount = components["schemas"]["CodexAccountResponse"];
export type CodexAccountLoginOperation = components["schemas"]["CodexAccountLoginResponse"];
export type CodexAccountLoginTerminalStart = components["schemas"]["OpenCodexAccountLoginTerminalResponse"];
export type CodexActiveLogin = components["schemas"]["CodexActiveLoginResponse"];
export type CodexAccountSwitch = components["schemas"]["CodexAccountSwitchResponse"];

export async function fetchCodexAccounts(): Promise<CodexAccountsResponse> {
	const { data, error } = await apiClient.GET("/api/v1/agents/codex/accounts");
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountsResponse;
}

export async function ensureCodexAccounts(accountIds: string[] = [], includeUsage = false): Promise<CodexAccountsResponse> {
	const { data, error } = await apiClient.POST("/api/v1/agents/codex/accounts/ensure", { body: { accountIds, includeUsage } });
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountsResponse;
}

export async function consumeCodexAccountResetCredit(accountId: string, idempotencyKey: string): Promise<CodexAccountsResponse> {
	const { data, error } = await apiClient.POST("/api/v1/agents/codex/accounts/{accountId}/reset-credit/consume", {
		params: { path: { accountId } },
		body: { idempotencyKey },
	});
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountsResponse;
}

export async function openCodexAccountLoginTerminal(): Promise<CodexAccountLoginTerminalStart> {
	const { data, error } = await apiClient.POST("/api/v1/agents/codex/accounts/login-terminal");
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountLoginTerminalStart;
}

export async function openCodexAccountReauthenticationTerminal(accountId: string): Promise<CodexAccountLoginTerminalStart> {
	const { data, error } = await apiClient.POST("/api/v1/agents/codex/accounts/{accountId}/login-terminal", {
		params: { path: { accountId } },
	});
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountLoginTerminalStart;
}

export async function logoutCodexAccount(accountId: string): Promise<CodexAccountsResponse> {
	const { data, error } = await apiClient.POST("/api/v1/agents/codex/accounts/{accountId}/logout", {
		params: { path: { accountId } },
	});
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountsResponse;
}

export async function deleteCodexAccount(accountId: string): Promise<CodexAccountsResponse> {
	const { data, error } = await apiClient.DELETE("/api/v1/agents/codex/accounts/{accountId}", {
		params: { path: { accountId } },
	});
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountsResponse;
}

export async function verifyCodexAccountLogin(operationId: string): Promise<CodexAccountLoginOperation> {
	const { data, error } = await apiClient.POST("/api/v1/agents/codex/accounts/login-operations/{operationId}/verify", { params: { path: { operationId } } });
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountLoginOperation;
}

export async function cancelCodexAccountLogin(operationId: string): Promise<CodexAccountLoginOperation> {
	const { data, error } = await apiClient.POST("/api/v1/agents/codex/accounts/login-operations/{operationId}/cancel", { params: { path: { operationId } } });
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountLoginOperation;
}

export async function startCodexAccountSwitch(targetAccountId: string, expectedAccountRevision: number, idempotencyKey: string): Promise<CodexAccountSwitch> {
	const { data, error } = await apiClient.POST("/api/v1/agents/codex/account-switches", { body: { targetAccountId, expectedAccountRevision, idempotencyKey } });
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountSwitch;
}

export async function recoverCodexAccountSwitch(switchId: string): Promise<CodexAccountSwitch> {
	const { data, error } = await apiClient.POST("/api/v1/agents/codex/account-switches/{switchId}/recover", { params: { path: { switchId } } });
	if (error) throw new Error(apiErrorMessage(error));
	return data as CodexAccountSwitch;
}

export const codexAccountsQueryOptions = {
	queryKey: codexAccountsQueryKey,
	queryFn: async ({ client }: { client: QueryClient }) => {
		const incoming = await fetchCodexAccounts();
		return mergeCodexAccounts(client.getQueryData<CodexAccountsResponse>(codexAccountsQueryKey), incoming, "replace");
	},
	retry: 1,
	staleTime: Number.POSITIVE_INFINITY,
};
export function useCodexAccountsQuery(enabled = true) { return useQuery({ ...codexAccountsQueryOptions, enabled }); }

export function useEnsureCodexAccounts(enabled = true): void {
	const queryClient = useQueryClient();
	useEffect(() => {
		if (!enabled) return;
		let active = true;
		const ensure = () => {
			const cached = queryClient.getQueryData(codexAccountsQueryKey);
			const ready = cached ? Promise.resolve() : queryClient.fetchQuery(codexAccountsQueryOptions).then(() => undefined).catch(() => undefined);
			void ready.then(() => ensureCodexAccounts()).then((next) => { if (active) writeCodexAccounts(queryClient, next, "replace"); }).catch(() => undefined);
		};
		ensure();
		const onFocus = () => ensure();
		const onVisibility = () => { if (document.visibilityState === "visible") ensure(); };
		window.addEventListener("focus", onFocus); document.addEventListener("visibilitychange", onVisibility);
		return () => { active = false; window.removeEventListener("focus", onFocus); document.removeEventListener("visibilitychange", onVisibility); };
	}, [enabled, queryClient]);
}
