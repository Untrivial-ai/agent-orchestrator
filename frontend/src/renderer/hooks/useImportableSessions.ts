import { useQuery } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";

const usePreviewData = import.meta.env.VITE_NO_ELECTRON === "1";
export interface ImportableSession {
	provider: string;
	nativeSessionId: string;
	title: string;
	cwd: string;
	branch?: string;
	lastActivity: string;
	messageCount: number;
	tokenCount: number;
	sizeBytes: number;
	alreadyImported: boolean;
}
export const importableSessionsQueryKey = ["importable-sessions"] as const;

// Discovery runs only for an explicitly opened project dialog. Cache recent
// results for a minute so reopening does not rescan the provider history.
export function useImportableSessions(projectId: string, enabled = true) {
	return useQuery({
		queryKey: [...importableSessionsQueryKey, projectId],
		queryFn: async ({ signal }) => {
			const { data, error } = await apiClient.GET(
				"/api/v1/sessions/importable",
				{
					params: { query: { projectId } },
					signal: AbortSignal.any([signal, AbortSignal.timeout(30_000)]),
				},
			);
			if (error)
				throw new Error(
					apiErrorMessage(error, "Failed to load importable sessions"),
				);
			return (data?.sessions ?? []) as ImportableSession[];
		},
		enabled: enabled && !!projectId && !usePreviewData,
		staleTime: 60_000,
		refetchOnWindowFocus: false,
		retry: false,
		throwOnError: false,
	});
}
