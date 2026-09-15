import { useQuery, type QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type CodexMaintenanceStatus = components["schemas"]["SysteminstallCodexMaintenanceStatus"];
export type CodexInstallJob = components["schemas"]["InstallJob"];
export type CodexOwnershipKind = CodexMaintenanceStatus["ownership"];

export const codexMaintenanceQueryKey = ["codex-maintenance"] as const;

// A Settings visit should see a fresh advisory, but repeated renders (or a
// background tab) must not repeatedly shell out on the backend; this mirrors
// the 60s TTL the service itself already enforces server-side.
const CODEX_MAINTENANCE_STALE_TIME_MS = 60_000;

export async function fetchCodexMaintenance(): Promise<CodexMaintenanceStatus> {
	const { data, error } = await apiClient.GET("/api/v1/agents/codex/maintenance");
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not load the Codex update advisory."));
	return data;
}

export async function startCodexUpdate(expectedOwnership?: CodexOwnershipKind): Promise<CodexInstallJob> {
	const { data, error } = await apiClient.POST("/api/v1/agents/codex/maintenance/update", {
		body: expectedOwnership ? { expectedOwnership } : undefined,
	});
	// Throw the raw API error (not a wrapped Error) so callers can still read
	// its structured `.code` via apiErrorCode(), e.g. to detect
	// CODEX_OWNERSHIP_CHANGED. This matches closeShellTerminal's convention
	// in useShellTerminals.ts.
	if (error || !data) throw error ?? new Error("Could not start the Codex update.");
	return data;
}

export const codexMaintenanceQueryOptions = {
	queryKey: codexMaintenanceQueryKey,
	queryFn: () => fetchCodexMaintenance(),
	retry: 1,
	staleTime: CODEX_MAINTENANCE_STALE_TIME_MS,
};

export function useCodexMaintenanceQuery(enabled = true) {
	return useQuery({ ...codexMaintenanceQueryOptions, enabled });
}

export function invalidateCodexMaintenance(queryClient: QueryClient): Promise<void> {
	return queryClient.invalidateQueries({ queryKey: codexMaintenanceQueryKey });
}
