import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workspaceQueryKey } from "./useWorkspaceQuery";

const usePreviewData = import.meta.env.VITE_NO_ELECTRON === "1";

// ImportableSession mirrors the daemon's ImportableSessionView: one agent
// conversation found on disk that can be imported as a resumable AO session.
export interface ImportableSession {
	provider: string;
	nativeSessionId: string;
	title: string;
	cwd: string;
	branch?: string;
	lastActivity: string;
	messageCount: number;
	sizeBytes: number;
	alreadyImported: boolean;
	// Import verdict from the transcript's content. Trivial conversations are
	// withheld by the daemon and never appear here.
	meaning?: "meaningful" | "ambiguous";
}

export const importableSessionsQueryKey = ["importable-sessions"] as const;

async function fetchImportable(days: number, projectId?: string): Promise<ImportableSession[]> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/importable", {
		params: { query: projectId ? { days, projectId } : { days } },
	});
	if (error) throw new Error(apiErrorMessage(error, "Failed to load importable sessions"));
	return (data?.sessions ?? []) as ImportableSession[];
}

// useImportableSessions lists agent conversations on disk that can be imported.
// A projectId narrows the list to that project's own history. The query is
// disabled in preview (no-Electron) mode where there is no daemon.
export function useImportableSessions(days = 60, enabled = true, projectId?: string) {
	return useQuery({
		queryKey: [...importableSessionsQueryKey, days, projectId ?? "all"],
		queryFn: () => fetchImportable(days, projectId),
		enabled: enabled && !usePreviewData,
		throwOnError: false,
	});
}

export interface ImportSessionInput {
	provider: string;
	nativeSessionId: string;
}

// useImportSession imports one discovered conversation. On success it refreshes
// the workspace list (so the new session appears) and the importable list (so
// the imported row flips to "already imported").
export function useImportSession() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (input: ImportSessionInput) => {
			const { data, error } = await apiClient.POST("/api/v1/sessions/import", { body: input });
			if (error) throw new Error(apiErrorMessage(error, "Import failed"));
			return data;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			void queryClient.invalidateQueries({ queryKey: importableSessionsQueryKey });
		},
	});
}
