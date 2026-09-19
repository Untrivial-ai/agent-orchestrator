import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useSyncExternalStore } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import {
	cloudDiffToWorkspaceDiffsResponse,
	cloudDiffToWorkspaceFilesResponse,
	cloudFileToWorkspaceFileDetail,
} from "../lib/cloud-inspector-adapters";
import type { CloudInspectorTarget } from "../lib/cloud-inspector-target";
import {
	getWorkspaceFileConnectionState,
	subscribeWorkspaceFileChanges,
	subscribeWorkspaceFileConnectionState,
	type WorkspaceFileConnectionState,
} from "../lib/workspace-file-events";
import { createRendererCloudCpClient } from "./useCloudCp";

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

export const sessionWorkspaceFilesQueryKey = (sessionId: string, cloudOrgId?: string) =>
	["session-workspace-files", sessionId, cloudOrgId ?? "local"] as const;
const WORKSPACE_FILES_DEGRADED_REFETCH_MS = 30_000;
/** Cloud workspace ops have no local SSE; poll while the Files tab is open. */
const CLOUD_WORKSPACE_FILES_REFETCH_MS = 10_000;

async function fetchLocalSessionWorkspaceFiles(sessionId: string, errorMessage: string): Promise<WorkspaceFilesResponse> {
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

async function fetchCloudSessionWorkspaceFiles(
	sessionId: string,
	cloud: CloudInspectorTarget,
	_errorMessage: string,
): Promise<WorkspaceFilesResponse> {
	const client = createRendererCloudCpClient(cloud.baseUrl);
	const diff = await client.getWorkspaceDiff(cloud.orgId, sessionId);
	return cloudDiffToWorkspaceFilesResponse(sessionId, diff);
}

async function fetchSessionWorkspaceFiles(
	sessionId: string,
	errorMessage: string,
	cloud?: CloudInspectorTarget,
): Promise<WorkspaceFilesResponse> {
	if (cloud) return fetchCloudSessionWorkspaceFiles(sessionId, cloud, errorMessage);
	return fetchLocalSessionWorkspaceFiles(sessionId, errorMessage);
}

export const sessionWorkspaceFileQueryKey = (
	sessionId: string,
	path: string,
	scope: WorkspaceDiffScope = "combined",
	commitSha?: string,
	cloudOrgId?: string,
) => ["session-workspace-file", sessionId, cloudOrgId ?? "local", scope, commitSha ?? "", path] as const;

async function fetchSessionWorkspaceFile(
	sessionId: string,
	path: string,
	scope: WorkspaceDiffScope,
	errorMessage: string,
	commitSha?: string,
	cloud?: CloudInspectorTarget,
): Promise<WorkspaceFileDetail> {
	if (cloud) {
		const client = createRendererCloudCpClient(cloud.baseUrl);
		const file = await client.readWorkspaceFile(cloud.orgId, sessionId, path);
		return cloudFileToWorkspaceFileDetail(sessionId, file);
	}
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/file", {
		params: { path: { sessionId }, query: { path, section: scope === "combined" ? undefined : scope, commitSha } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	if (!data) throw new Error(errorMessage);
	return data as WorkspaceFileDetail;
}

// Shared so the diff view (expand-on-demand) and the plain read-only viewer
// always resolve to the same cache entry for a given (session, path).
export function sessionWorkspaceFileQueryOptions(
	sessionId: string,
	path: string,
	errorMessage = "Unable to load workspace file",
	scope: WorkspaceDiffScope = "combined",
	commitSha?: string,
	cloud?: CloudInspectorTarget,
) {
	return {
		queryKey: sessionWorkspaceFileQueryKey(sessionId, path, scope, commitSha, cloud?.orgId),
		queryFn: () => fetchSessionWorkspaceFile(sessionId, path, scope, errorMessage, commitSha, cloud),
	};
}

export const sessionWorkspaceDiffsQueryKey = (
	sessionId: string,
	scope: WorkspaceDiffScope,
	paths: readonly string[],
	contextLines: number,
	ignoreWhitespace: boolean,
	workspaceVersion?: string,
	commitSha?: string,
	cloudOrgId?: string,
) =>
	[
		"session-workspace-diffs",
		sessionId,
		cloudOrgId ?? "local",
		scope,
		commitSha ?? "",
		paths,
		contextLines,
		ignoreWhitespace,
		workspaceVersion ?? "",
	] as const;

export function sessionWorkspaceDiffsQueryOptions({
	cloud,
	contextLines = 3,
	errorMessage = "Unable to load workspace changes",
	ignoreWhitespace = false,
	paths,
	scope,
	sessionId,
	workspaceVersion,
	commitSha,
}: {
	cloud?: CloudInspectorTarget;
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
		queryKey: sessionWorkspaceDiffsQueryKey(
			sessionId,
			scope,
			paths,
			contextLines,
			ignoreWhitespace,
			workspaceVersion,
			commitSha,
			cloud?.orgId,
		),
		queryFn: async (): Promise<WorkspaceDiffsResponse> => {
			if (cloud) {
				const client = createRendererCloudCpClient(cloud.baseUrl);
				const diff = await client.getWorkspaceDiff(cloud.orgId, sessionId);
				return cloudDiffToWorkspaceDiffsResponse(sessionId, diff, scope, paths);
			}
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
	cloud,
	errorMessage = "Unable to load file revision",
	expectedRevision,
	path,
	scope,
	sessionId,
	side,
	workspaceVersion,
	commitSha,
}: {
	cloud?: CloudInspectorTarget;
	errorMessage?: string;
	expectedRevision?: string;
	path: string;
	scope: WorkspaceDiffScope;
	sessionId: string;
	side: "before" | "after";
	workspaceVersion?: string;
	commitSha?: string;
}): Promise<WorkspaceFileRevision> {
	if (cloud) {
		// Cloud only exposes the working-tree file; treat "after" as current content
		// and "before" as missing so split compare degrades to a single pane.
		if (side === "before") {
			return {
				sessionId,
				path,
				side,
				workspaceVersion: workspaceVersion ?? "cloud",
				size: 0,
				exists: false,
				binary: false,
				truncated: false,
				content: "",
			};
		}
		const client = createRendererCloudCpClient(cloud.baseUrl);
		const file = await client.readWorkspaceFile(cloud.orgId, sessionId, path);
		return {
			sessionId,
			path,
			side,
			workspaceVersion: workspaceVersion ?? "cloud",
			size: file.size,
			exists: true,
			binary: false,
			truncated: false,
			content: file.content,
		};
	}
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/file/revision", {
		params: { path: { sessionId }, query: { path, scope, side, workspaceVersion, expectedRevision, commitSha } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	if (!data) throw new Error(errorMessage);
	return data;
}

export function sessionWorkspaceFileRevisionQueryOptions({
	cloud,
	path,
	scope,
	sessionId,
	side,
	workspaceVersion,
	commitSha,
}: {
	cloud?: CloudInspectorTarget;
	path: string;
	scope: WorkspaceDiffScope;
	sessionId: string;
	side: "before" | "after";
	workspaceVersion?: string;
	commitSha?: string;
}) {
	return {
		queryKey: [
			"session-workspace-file-revision",
			sessionId,
			cloud?.orgId ?? "local",
			scope,
			commitSha ?? "",
			side,
			path,
			workspaceVersion ?? "",
		] as const,
		queryFn: () => fetchWorkspaceFileRevision({ sessionId, path, scope, side, workspaceVersion, commitSha, cloud }),
	};
}

export async function updateSessionWorkspaceFile({
	cloud,
	content,
	expectedFileFingerprint,
	path,
	sessionId,
}: {
	cloud?: CloudInspectorTarget;
	content: string;
	expectedFileFingerprint: string;
	path: string;
	sessionId: string;
}): Promise<WorkspaceFileDetail> {
	if (cloud) {
		const client = createRendererCloudCpClient(cloud.baseUrl);
		const file = await client.writeWorkspaceFile(cloud.orgId, sessionId, { path, content });
		return cloudFileToWorkspaceFileDetail(sessionId, file);
	}
	const { data, error } = await apiClient.PUT("/api/v1/sessions/{sessionId}/workspace/file", {
		params: { path: { sessionId } },
		body: { content, expectedFileFingerprint, path },
	});
	if (error) throw new Error(apiErrorMessage(error, "Unable to save workspace file"));
	if (!data) throw new Error("Unable to save workspace file");
	return data as WorkspaceFileDetail;
}

export function sessionWorkspaceSearchQueryOptions(
	sessionId: string,
	query: string,
	errorMessage = "Unable to search workspace files",
	cloud?: CloudInspectorTarget,
) {
	return {
		queryKey: ["session-workspace-search", sessionId, cloud?.orgId ?? "local", query] as const,
		queryFn: async (): Promise<WorkspaceFileSearchResponse> => {
			if (cloud) {
				// Cloud has no search endpoint yet; fall back to changed-file paths from diff.
				const files = await fetchCloudSessionWorkspaceFiles(sessionId, cloud, errorMessage);
				const needle = query.trim().toLowerCase();
				const results = files.files
					.filter((file) => !needle || file.path.toLowerCase().includes(needle))
					.slice(0, 100)
					.map((file) => ({
						path: file.path,
						status: file.status,
						size: file.size,
						binary: file.binary,
						fileFingerprint: file.fileFingerprint ?? "",
					}));
				return { sessionId, query: needle, results, truncated: false };
			}
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
export function sessionWorkspaceFilesQueryOptions(
	sessionId: string,
	errorMessage = "Unable to load workspace files",
	cloud?: CloudInspectorTarget,
) {
	return {
		queryKey: sessionWorkspaceFilesQueryKey(sessionId, cloud?.orgId),
		queryFn: () => fetchSessionWorkspaceFiles(sessionId, errorMessage, cloud),
	};
}

export function workspaceFilesRefetchInterval(
	state: WorkspaceFileConnectionState,
	cloud?: CloudInspectorTarget,
): false | number {
	if (cloud) return CLOUD_WORKSPACE_FILES_REFETCH_MS;
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
export function useSessionWorkspaceFilesChangedCount(
	sessionId: string | undefined,
	cloud?: CloudInspectorTarget,
): number | undefined {
	const queryClient = useQueryClient();
	const query = useQuery({
		...sessionWorkspaceFilesQueryOptions(sessionId ?? "", "Unable to load workspace files", cloud),
		enabled: Boolean(sessionId),
		// Live invalidations keep the inactive tab fresh; polling starts only
		// when the full Files view is visible. Cloud has no SSE, so poll lightly.
		refetchInterval: cloud ? CLOUD_WORKSPACE_FILES_REFETCH_MS : false,
		select: (data: WorkspaceFilesResponse) => data.files.filter(isChangedWorkspaceFile).length,
	});
	useEffect(() => {
		if (!sessionId || cloud) return;
		return subscribeWorkspaceFileChanges(sessionId, queryClient);
	}, [cloud, queryClient, sessionId]);
	return sessionId ? query.data : undefined;
}
