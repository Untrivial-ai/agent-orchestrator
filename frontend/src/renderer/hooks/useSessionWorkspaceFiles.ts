import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { UseQueryOptions } from "@tanstack/react-query";
import { useCallback, useEffect, useSyncExternalStore } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage, getApiBaseUrl } from "../lib/api-client";
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
export type WorkspaceFilesResponse = Omit<components["schemas"]["ListWorkspaceFilesResponse"], "files" | "sections" | "workspaceVersion" | "degraded" | "degradedCode"> & {
	compareMode?: WorkspaceCompareMode;
	files: WorkspaceFileSummary[];
	sections: {
		committed: WorkspaceFileSummary[];
		staged: WorkspaceFileSummary[];
		unstaged: WorkspaceFileSummary[];
		untracked: WorkspaceFileSummary[];
	};
	workspaceVersion?: string;
	degraded?: boolean;
	degradedCode?: string;
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
export type FilesSource =
	| { kind: "workspace" }
	| { kind: "pull_request"; number: number; url: string; label: string; snapshot?: string }
	| { kind: "artifact" };

export const sessionWorkspaceFilesQueryKey = (sessionId: string) => ["session-workspace-files", sessionId] as const;
const WORKSPACE_FILES_DEGRADED_REFETCH_MS = 30_000;
const MAX_ARTIFACT_TEXT_BYTES = 256 * 1024;

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

async function fetchSessionPRFiles(sessionId: string, number: number, sourceUrl: string, errorMessage: string): Promise<WorkspaceFilesResponse> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr/{prNumber}/files", {
		params: { path: { sessionId, prNumber: number }, query: { sourceUrl } },
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

async function fetchSessionPRFile(sessionId: string, number: number, sourceUrl: string, path: string, previousPath: string, errorMessage: string): Promise<WorkspaceFileDetail> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr/{prNumber}/file", {
		params: { path: { sessionId, prNumber: number }, query: { path, previousPath, sourceUrl } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	if (!data) throw new Error(errorMessage);
	return data as WorkspaceFileDetail;
}

// Artifact files live in the session's artifact directory, outside the git
// workspace, so they have no diff/status and aren't reachable through the
// workspace-files endpoint. `/preview/files/*` already serves any file rooted
// under either the workspace or the artifact dir (the `__ao_artifacts__/`
// prefix picks the latter) as raw bytes — the same route the Browser preview
// uses for HTML artifacts, just fetched directly here instead of navigated to.
function artifactPreviewFileUrl(sessionId: string, path: string): string {
	const encodedPath = path.split("/").map(encodeURIComponent).join("/");
	return `${getApiBaseUrl()}/api/v1/sessions/${encodeURIComponent(sessionId)}/preview/files/__ao_artifacts__/${encodedPath}?raw=true`;
}

async function fetchSessionArtifactFile(sessionId: string, path: string, errorMessage: string): Promise<WorkspaceFileDetail> {
	const response = await fetch(artifactPreviewFileUrl(sessionId, path));
	if (!response.ok) throw new Error(errorMessage);
	const { binary, content, size, truncated } = await readArtifactTextResponse(response);
	return {
		additions: 0,
		binary,
		content: binary ? "" : content,
		contentTruncated: truncated,
		deleted: false,
		deletions: 0,
		diff: "",
		diffTruncated: false,
		editable: false,
		fileFingerprint: "",
		path,
		sessionId,
		size,
		status: "unmodified",
		workspaceVersion: "",
	};
}

async function readArtifactTextResponse(response: Response): Promise<{ binary: boolean; content: string; size: number; truncated: boolean }> {
	const headerSize = Number.parseInt(response.headers.get("content-length") ?? "", 10);
	if (!response.body && Number.isFinite(headerSize) && headerSize > MAX_ARTIFACT_TEXT_BYTES) {
		return { binary: false, content: "", size: headerSize, truncated: true };
	}
	const { bytes, size, truncated } = await readBoundedResponseBytes(response, MAX_ARTIFACT_TEXT_BYTES);
	const decoded = decodeArtifactText(bytes, truncated);
	return { ...decoded, size, truncated };
}

async function readBoundedResponseBytes(response: Response, limit: number): Promise<{ bytes: Uint8Array; size: number; truncated: boolean }> {
	if (!response.body) {
		const buffer = await response.arrayBuffer();
		const bytes = new Uint8Array(buffer);
		return { bytes: bytes.slice(0, limit), size: bytes.byteLength, truncated: bytes.byteLength > limit };
	}

	const reader = response.body.getReader();
	const chunks: Uint8Array[] = [];
	let total = 0;
	let truncated = false;
	while (true) {
		const { done, value } = await reader.read();
		if (done) break;
		const remaining = limit - total;
		if (value.byteLength > remaining) {
			if (remaining > 0) chunks.push(value.slice(0, remaining));
			total += value.byteLength;
			truncated = true;
			await reader.cancel();
			break;
		}
		chunks.push(value);
		total += value.byteLength;
	}

	const kept = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0);
	const bytes = new Uint8Array(kept);
	let offset = 0;
	for (const chunk of chunks) {
		bytes.set(chunk, offset);
		offset += chunk.byteLength;
	}
	return { bytes, size: total, truncated };
}

function decodeArtifactText(bytes: Uint8Array, truncated: boolean): { binary: boolean; content: string } {
	if (bytes.includes(0)) return { binary: true, content: "" };
	try {
		return { binary: false, content: new TextDecoder("utf-8", { fatal: true }).decode(bytes) };
	} catch {
		if (truncated) {
			for (let trim = 1; trim < 4 && trim < bytes.byteLength; trim += 1) {
				try {
					return { binary: false, content: new TextDecoder("utf-8", { fatal: true }).decode(bytes.slice(0, bytes.byteLength - trim)) };
				} catch {
					// Invalid UTF-8 away from the bounded suffix is handled below as binary.
				}
			}
		}
		return { binary: true, content: "" };
	}
}

export function sessionArtifactFileQueryOptions(sessionId: string, path: string, errorMessage = "Unable to load artifact"): UseQueryOptions<WorkspaceFileDetail> {
	return {
		queryKey: ["session-artifact-file", sessionId, path],
		queryFn: () => fetchSessionArtifactFile(sessionId, path, errorMessage),
	};
}

// Shared so the diff view (expand-on-demand) and the plain read-only viewer
// always resolve to the same cache entry for a given (session, path).
export function sessionWorkspaceFileQueryOptions(sessionId: string, path: string, errorMessage = "Unable to load workspace file", scope: WorkspaceDiffScope = "combined", commitSha?: string) {
	return {
		queryKey: sessionWorkspaceFileQueryKey(sessionId, path, scope, commitSha),
		queryFn: () => fetchSessionWorkspaceFile(sessionId, path, scope, errorMessage, commitSha),
	};
}

export function sessionSourceFileQueryOptions(sessionId: string, source: FilesSource, path: string, errorMessage = "Unable to load file", scope: WorkspaceDiffScope = "combined", commitSha?: string, previousPath = ""): UseQueryOptions<WorkspaceFileDetail> {
	if (source.kind === "workspace") return sessionWorkspaceFileQueryOptions(sessionId, path, errorMessage, scope, commitSha);
	if (source.kind === "artifact") return sessionArtifactFileQueryOptions(sessionId, path, errorMessage);
	return { queryKey: ["session-source-file", sessionId, "pull_request", source.url, source.snapshot ?? "", path], queryFn: () => fetchSessionPRFile(sessionId, source.number, source.url, path, previousPath, errorMessage) };
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

export async function fetchPRFileRevision(sessionId: string, number: number, sourceUrl: string, path: string, side: "before" | "after"): Promise<WorkspaceFileRevision> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr/{prNumber}/file/revision", {
		params: { path: { sessionId, prNumber: number }, query: { path, side, sourceUrl } },
	});
	if (error || !data) throw new Error(apiErrorMessage(error, "Unable to load pull request file revision"));
	return data as WorkspaceFileRevision;
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

export function sessionSourceFileRevisionQueryOptions({
	path,
	scope,
	sessionId,
	side,
	source,
	workspaceVersion,
	commitSha,
}: {
	path: string;
	scope: WorkspaceDiffScope;
	sessionId: string;
	side: "before" | "after";
	source: FilesSource;
	workspaceVersion?: string;
	commitSha?: string;
}): UseQueryOptions<WorkspaceFileRevision> {
	if (source.kind === "workspace") return sessionWorkspaceFileRevisionQueryOptions({ path, scope, sessionId, side, workspaceVersion, commitSha });
	if (source.kind === "pull_request") {
		return {
			queryKey: ["session-source-file-revision", sessionId, "pull_request", source.url, source.snapshot ?? "", side, path] as const,
			queryFn: () => fetchPRFileRevision(sessionId, source.number, source.url, path, side),
		};
	}
	// Artifacts have no split before/after comparison — they're not diffed
	// against anything, just standalone output files. Callers gate this query
	// with `enabled: detail.deleted || detail.contentTruncated`, neither of
	// which an artifact ever sets, so the queryFn below never actually runs;
	// it still needs to type-check and exist, since options are constructed
	// unconditionally before `enabled` is evaluated.
	return {
		queryKey: ["session-source-file-revision", sessionId, "artifact", side, path] as const,
		queryFn: (): Promise<WorkspaceFileRevision> => {
			throw new Error("Artifact sources do not support file revisions");
		},
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
	if (source.kind === "workspace") return sessionWorkspaceFilesQueryOptions(sessionId, errorMessage);
	if (source.kind === "pull_request") {
		return { queryKey: ["session-source-files", sessionId, "pull_request", source.url, source.snapshot ?? ""], queryFn: () => fetchSessionPRFiles(sessionId, source.number, source.url, errorMessage) };
	}
	// Artifact directory listings come from the session summary artifactFiles
	// contract. The queryFn still needs to exist and type-check even though the
	// Files explorer disables this query while the artifact source is active.
	return {
		queryKey: ["session-source-files", sessionId, "artifact"] as const,
		queryFn: (): Promise<WorkspaceFilesResponse> => {
			throw new Error("Artifact sources do not support directory listing");
		},
	};
}

export function workspaceFilesRefetchInterval(state: WorkspaceFileConnectionState, degraded = false): false | number {
	return state === "degraded" || degraded ? WORKSPACE_FILES_DEGRADED_REFETCH_MS : false;
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
