import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { subscribeWorkspaceFileChanges } from "../lib/workspace-file-events";

export type WorkspaceCompareMode = "base" | "head_fallback";
export type WorkspaceFileSummary = components["schemas"]["WorkspaceFileSummary"] & {
	previousPath?: string;
};
export type WorkspaceFilesResponse = components["schemas"]["ListWorkspaceFilesResponse"] & {
	compareMode?: WorkspaceCompareMode;
};
export type WorkspaceFileDetail = components["schemas"]["WorkspaceFileResponse"] & {
	previousPath?: string;
	compareMode?: WorkspaceCompareMode;
};

export const sessionWorkspaceFilesQueryKey = (sessionId: string) => ["session-workspace-files", sessionId] as const;

async function fetchSessionWorkspaceFiles(sessionId: string, errorMessage: string): Promise<WorkspaceFilesResponse> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/files", {
		params: { path: { sessionId } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	return (data ?? { sessionId, files: [], truncated: false }) as WorkspaceFilesResponse;
}

export const sessionWorkspaceFileQueryKey = (sessionId: string, path: string) =>
	["session-workspace-file", sessionId, path] as const;

async function fetchSessionWorkspaceFile(sessionId: string, path: string, errorMessage: string): Promise<WorkspaceFileDetail> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/file", {
		params: { path: { sessionId }, query: { path } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	if (!data) throw new Error(errorMessage);
	return data as WorkspaceFileDetail;
}

// Shared so the diff view (expand-on-demand) and the plain read-only viewer
// always resolve to the same cache entry for a given (session, path).
export function sessionWorkspaceFileQueryOptions(sessionId: string, path: string, errorMessage = "Unable to load workspace file") {
	return {
		queryKey: sessionWorkspaceFileQueryKey(sessionId, path),
		queryFn: () => fetchSessionWorkspaceFile(sessionId, path, errorMessage),
	};
}

// Shared so SessionFileExplorer (full fetch + polling) and SessionInspector
// (eager fetch + live invalidation) always resolve to the same cache entry.
export function sessionWorkspaceFilesQueryOptions(sessionId: string, errorMessage = "Unable to load workspace files") {
	return {
		queryKey: sessionWorkspaceFilesQueryKey(sessionId),
		queryFn: () => fetchSessionWorkspaceFiles(sessionId, errorMessage),
		// SSE (subscribeWorkspaceFileChanges) already invalidates this query
		// immediately on real filesystem changes and on reconnect, triggering
		// an instant refetch regardless of this interval. Polling is only a
		// safety net for missed/dropped SSE events, so it can stay slow.
		refetchInterval: 30_000,
	};
}

export function isChangedWorkspaceFile(file: WorkspaceFileSummary): boolean {
	return file.status !== "unmodified";
}

// Keep the lightweight summary query warm while the inspector is open. The
// Files view then mounts against current cache data instead of flashing a
// misleading zero while its first request starts.
export function useSessionWorkspaceFilesChangedCount(sessionId: string | undefined): number | undefined {
	const queryClient = useQueryClient();
	const query = useQuery({
		...sessionWorkspaceFilesQueryOptions(sessionId ?? ""),
		enabled: Boolean(sessionId),
		// Live invalidations keep the inactive tab fresh; polling starts only
		// when the full Files view is visible.
		refetchInterval: false,
		select: (data: WorkspaceFilesResponse) => data.files.filter(isChangedWorkspaceFile).length,
	});
	useEffect(() => {
		if (!sessionId) return;
		return subscribeWorkspaceFileChanges(sessionId, queryClient);
	}, [queryClient, sessionId]);
	return sessionId ? query.data : undefined;
}
