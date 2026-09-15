import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { UseQueryOptions } from "@tanstack/react-query";
import { useCallback, useEffect, useSyncExternalStore } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import {
	getWorkspaceFileConnectionState,
	subscribeWorkspaceFileChanges,
	subscribeWorkspaceFileConnectionState,
	type WorkspaceFileConnectionState,
} from "../lib/workspace-file-events";

export type WorkspaceCompareMode = "base" | "head_fallback";
export type WorkspaceFileSummary = Omit<components["schemas"]["WorkspaceFileSummary"], "editable" | "fileFingerprint"> & {
	editable?: boolean;
	previousPath?: string;
	fileFingerprint?: string;
};
export type WorkspaceFileSections = components["schemas"]["WorkspaceFileSections"];
export type WorkspaceCommitSummary = components["schemas"]["WorkspaceCommitSummary"];
export type WorkspaceSummary = components["schemas"]["WorkspaceSummary"];
export type WorkspaceFilesResponse = Omit<components["schemas"]["ListWorkspaceFilesResponse"], "files" | "sections" | "workspaceVersion"> & {
	compareMode?: WorkspaceCompareMode;
	files: WorkspaceFileSummary[];
	sections: {
		committed: WorkspaceFileSummary[];
		staged: WorkspaceFileSummary[];
		unstaged: WorkspaceFileSummary[];
		untracked: WorkspaceFileSummary[];
	};
	workspaceVersion?: string;
};
export type WorkspaceFileDetail = Omit<components["schemas"]["WorkspaceFileResponse"], "editable" | "fileFingerprint" | "workspaceVersion"> & {
	editable?: boolean;
	previousPath?: string;
	compareMode?: WorkspaceCompareMode;
	fileFingerprint?: string;
	workspaceVersion?: string;
};
export type WorkspaceDiffScope = components["schemas"]["WorkspaceDiffRequest"]["scope"];
export type WorkspaceDiffsResponse = components["schemas"]["WorkspaceDiffsResponse"];
export type WorkspaceFileRevision = components["schemas"]["WorkspaceFileRevisionResponse"];
export type WorkspaceFileSearchResponse = components["schemas"]["WorkspaceFileSearchResponse"];
export type FilesSource = { kind: "workspace" } | { kind: "pull_request"; number: number; label: string };

export const sessionWorkspaceFilesQueryKey = (sessionId: string) => ["session-workspace-files", sessionId] as const;
export const sessionPRFilesQueryKey = (sessionId: string, number: number) => ["session-pr-files", sessionId, number] as const;
const WORKSPACE_FILES_DEGRADED_REFETCH_MS = 30_000;

async function fetchSessionWorkspaceFiles(sessionId: string, errorMessage: string): Promise<WorkspaceFilesResponse> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/files", {
		params: { path: { sessionId } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	const response = (data ?? {
		sessionId,
		files: [],
		truncated: false,
		sections: { staged: [], unstaged: [], untracked: [], committed: [] },
		commits: [],
		summary: { files: 0, additions: 0, deletions: 0 },
	}) as WorkspaceFilesResponse;
	return {
		...response,
		commits: (response.commits ?? []).map((commit) => ({ ...commit, files: commit.files ?? [] })),
		files: response.files ?? [],
		sections: response.sections ?? { staged: [], unstaged: [], untracked: [], committed: [] },
	};
}

async function fetchSessionPRFiles(sessionId: string, number: number, errorMessage: string): Promise<WorkspaceFilesResponse> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr/{prNumber}/files", {
		params: { path: { sessionId, prNumber: number } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	return { ...data, sections: { staged: [], unstaged: [], untracked: [], committed: data?.files ?? [] }, commits: [] } as WorkspaceFilesResponse;
}

export const sessionWorkspaceFileQueryKey = (sessionId: string, path: string, scope: WorkspaceDiffScope = "combined", commitSha?: string) =>
	["session-workspace-file", sessionId, scope, commitSha ?? "", path] as const;

async function fetchSessionWorkspaceFile(sessionId: string, path: string, scope: WorkspaceDiffScope, errorMessage: string, commitSha?: string): Promise<WorkspaceFileDetail> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/file", {
		params: { path: { sessionId }, query: { path, section: scope === "combined" ? undefined : scope, commitSha } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	if (!data) throw new Error(errorMessage);
	return data as WorkspaceFileDetail;
}

async function fetchSessionPRFile(sessionId: string, number: number, path: string, errorMessage: string): Promise<WorkspaceFileDetail> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr/{prNumber}/file", {
		params: { path: { sessionId, prNumber: number }, query: { path } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	if (!data) throw new Error(errorMessage);
	return data as WorkspaceFileDetail;
}

// Shared so the diff view (expand-on-demand) and the plain read-only viewer
// always resolve to the same cache entry for a given (session, path).
export function sessionWorkspaceFileQueryOptions(sessionId: string, path: string, errorMessage = "Unable to load workspace file", scope: WorkspaceDiffScope = "combined", commitSha?: string) {
	return {
		queryKey: sessionWorkspaceFileQueryKey(sessionId, path, scope, commitSha),
		queryFn: () => fetchSessionWorkspaceFile(sessionId, path, scope, errorMessage, commitSha),
	};
}

export function sessionSourceFileQueryOptions(sessionId: string, source: FilesSource, path: string, errorMessage = "Unable to load file", scope: WorkspaceDiffScope = "combined", commitSha?: string): UseQueryOptions<WorkspaceFileDetail> {
	return source.kind === "workspace"
		? sessionWorkspaceFileQueryOptions(sessionId, path, errorMessage, scope, commitSha)
		: { queryKey: ["session-source-file", sessionId, "pull_request", source.number, path], queryFn: () => fetchSessionPRFile(sessionId, source.number, path, errorMessage) };
}

export const sessionWorkspaceDiffsQueryKey = (
	sessionId: string,
	scope: WorkspaceDiffScope,
	paths: readonly string[],
	contextLines: number,
	ignoreWhitespace: boolean,
	workspaceVersion?: string,
	commitSha?: string,
) => ["session-workspace-diffs", sessionId, scope, commitSha ?? "", paths, contextLines, ignoreWhitespace, workspaceVersion ?? ""] as const;

export function sessionWorkspaceDiffsQueryOptions({
	contextLines = 3,
	errorMessage = "Unable to load workspace changes",
	ignoreWhitespace = false,
	paths,
	scope,
	sessionId,
	workspaceVersion,
	commitSha,
}: {
	contextLines?: number;
	errorMessage?: string;
	ignoreWhitespace?: boolean;
	paths: readonly string[];
	scope: WorkspaceDiffScope;
	sessionId: string;
	workspaceVersion?: string;
	commitSha?: string;
}) {
	return {
		queryKey: sessionWorkspaceDiffsQueryKey(sessionId, scope, paths, contextLines, ignoreWhitespace, workspaceVersion, commitSha),
		queryFn: async (): Promise<WorkspaceDiffsResponse> => {
			const { data, error } = await apiClient.POST("/api/v1/sessions/{sessionId}/workspace/diffs", {
				params: { path: { sessionId } },
				body: { commitSha, contextLines, ignoreWhitespace, paths: [...paths], scope, workspaceVersion },
			});
			if (error) throw new Error(apiErrorMessage(error, errorMessage));
			if (!data) throw new Error(errorMessage);
			return data;
		},
	};
}

export async function fetchWorkspaceFileRevision({
	errorMessage = "Unable to load file revision",
	expectedRevision,
	path,
	scope,
	sessionId,
	side,
	workspaceVersion,
	commitSha,
}: {
	errorMessage?: string;
	expectedRevision?: string;
	path: string;
	scope: WorkspaceDiffScope;
	sessionId: string;
	side: "before" | "after";
	workspaceVersion?: string;
	commitSha?: string;
}): Promise<WorkspaceFileRevision> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/file/revision", {
		params: { path: { sessionId }, query: { path, scope, side, workspaceVersion, expectedRevision, commitSha } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	if (!data) throw new Error(errorMessage);
	return data;
}

export function sessionWorkspaceFileRevisionQueryOptions({
	path,
	scope,
	sessionId,
	side,
	workspaceVersion,
	commitSha,
}: {
	path: string;
	scope: WorkspaceDiffScope;
	sessionId: string;
	side: "before" | "after";
	workspaceVersion?: string;
	commitSha?: string;
}) {
	return {
		queryKey: ["session-workspace-file-revision", sessionId, scope, commitSha ?? "", side, path, workspaceVersion ?? ""] as const,
		queryFn: () => fetchWorkspaceFileRevision({ sessionId, path, scope, side, workspaceVersion, commitSha }),
	};
}

export async function updateSessionWorkspaceFile({
	content,
	expectedFileFingerprint,
	path,
	sessionId,
}: {
	content: string;
	expectedFileFingerprint: string;
	path: string;
	sessionId: string;
}): Promise<WorkspaceFileDetail> {
	const { data, error } = await apiClient.PUT("/api/v1/sessions/{sessionId}/workspace/file", {
		params: { path: { sessionId } },
		body: { content, expectedFileFingerprint, path },
	});
	if (error) throw new Error(apiErrorMessage(error, "Unable to save workspace file"));
	if (!data) throw new Error("Unable to save workspace file");
	return data as WorkspaceFileDetail;
}

export function sessionWorkspaceSearchQueryOptions(sessionId: string, query: string, errorMessage = "Unable to search workspace files") {
	return {
		queryKey: ["session-workspace-search", sessionId, query] as const,
		queryFn: async (): Promise<WorkspaceFileSearchResponse> => {
			const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/search", {
				params: { path: { sessionId }, query: { query, limit: 100 } },
			});
			if (error) throw new Error(apiErrorMessage(error, errorMessage));
			if (!data) throw new Error(errorMessage);
			return data;
		},
	};
}

// Shared so SessionFileExplorer and SessionInspector resolve to the same cache
// entry while SSE invalidation remains the normal refresh path.
export function sessionWorkspaceFilesQueryOptions(sessionId: string, errorMessage = "Unable to load workspace files") {
	return {
		queryKey: sessionWorkspaceFilesQueryKey(sessionId),
		queryFn: () => fetchSessionWorkspaceFiles(sessionId, errorMessage),
	};
}

export function sessionSourceFilesQueryOptions(sessionId: string, source: FilesSource, errorMessage = "Unable to load files"): UseQueryOptions<WorkspaceFilesResponse> {
	return source.kind === "workspace"
		? sessionWorkspaceFilesQueryOptions(sessionId, errorMessage)
		: { queryKey: ["session-source-files", sessionId, "pull_request", source.number], queryFn: () => fetchSessionPRFiles(sessionId, source.number, errorMessage) };
}

export function workspaceFilesRefetchInterval(state: WorkspaceFileConnectionState): false | number {
	return state === "degraded" ? WORKSPACE_FILES_DEGRADED_REFETCH_MS : false;
}

export function useWorkspaceFileConnectionState(sessionId: string): WorkspaceFileConnectionState {
	const subscribe = useCallback(
		(listener: () => void) => subscribeWorkspaceFileConnectionState(sessionId, listener),
		[sessionId],
	);
	const getSnapshot = useCallback(() => getWorkspaceFileConnectionState(sessionId), [sessionId]);
	return useSyncExternalStore(subscribe, getSnapshot);
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
