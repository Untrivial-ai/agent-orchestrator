import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { QueryClient, UseQueryOptions } from "@tanstack/react-query";
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
export type FilesSource = { kind: "workspace" } | { kind: "pull_request"; number: number; url: string; label: string; snapshot?: string };

export const sessionWorkspaceFilesQueryKey = (sessionId: string) => ["session-workspace-files", sessionId] as const;
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

async function fetchSessionPRFiles(sessionId: string, number: number, sourceUrl: string, errorMessage: string): Promise<WorkspaceFilesResponse> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr/{prNumber}/files", {
		params: { path: { sessionId, prNumber: number }, query: { sourceUrl } },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	return {
		...data,
		sections: { staged: [], unstaged: [], untracked: [], committed: data?.files ?? [] },
		commits: (data?.commits ?? []).map((commit) => ({ ...commit, files: commit.files ?? [] })),
	} as WorkspaceFilesResponse;
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

async function fetchSessionPRFile(sessionId: string, number: number, sourceUrl: string, path: string, previousPath: string, errorMessage: string, commitSha?: string): Promise<WorkspaceFileDetail> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr/{prNumber}/file", {
		params: { path: { sessionId, prNumber: number }, query: { path, previousPath, sourceUrl, commitSha } },
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

export function sessionSourceFileQueryOptions(sessionId: string, source: FilesSource, path: string, errorMessage = "Unable to load file", scope: WorkspaceDiffScope = "combined", commitSha?: string, previousPath = ""): UseQueryOptions<WorkspaceFileDetail> {
	return source.kind === "workspace"
		? sessionWorkspaceFileQueryOptions(sessionId, path, errorMessage, scope, commitSha)
		: { queryKey: ["session-source-file", sessionId, "pull_request", source.url, source.snapshot ?? "", commitSha ?? "", path], queryFn: () => fetchSessionPRFile(sessionId, source.number, source.url, path, previousPath, errorMessage, commitSha) };
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

export async function fetchPRFileRevision(sessionId: string, number: number, sourceUrl: string, path: string, side: "before" | "after", commitSha?: string): Promise<WorkspaceFileRevision> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/pr/{prNumber}/file/revision", {
		params: { path: { sessionId, prNumber: number }, query: { path, side, sourceUrl, commitSha } },
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
	return source.kind === "workspace"
		? sessionWorkspaceFileRevisionQueryOptions({ path, scope, sessionId, side, workspaceVersion, commitSha })
		: {
			queryKey: ["session-source-file-revision", sessionId, "pull_request", source.url, source.snapshot ?? "", commitSha ?? "", side, path] as const,
			queryFn: () => fetchPRFileRevision(sessionId, source.number, source.url, path, side, commitSha),
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
		: { queryKey: ["session-source-files", sessionId, "pull_request", source.url, source.snapshot ?? ""], queryFn: () => fetchSessionPRFiles(sessionId, source.number, source.url, errorMessage) };
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
// misleading zero while its first request starts. The same moment also
// preloads the default review's diffs, because that view is unmounted until
// the Files tab is selected.
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
	useEffect(() => {
		if (!sessionId || query.data === undefined) return;
		const data = queryClient.getQueryData<WorkspaceFilesResponse>(sessionWorkspaceFilesQueryKey(sessionId));
		if (!data) return;
		void prefetchDefaultWorkspaceReviewDiffs(queryClient, sessionId, data).catch(() => {});
	}, [query.data, query.dataUpdatedAt, queryClient, sessionId]);
	return sessionId ? query.data : undefined;
}

// Must match WorkspaceReviewPane: the Files tab requests these batches, with
// this context size, and then full contents for files whose patch ends on the
// last hunk. A mismatch leaves the tab on "Loading diff…".
const REVIEW_PREFETCH_BATCH_SIZE = 100;
const REVIEW_PREFETCH_BATCHES = 4;
const REVIEW_PREFETCH_EOF_MAX = 40;
const REVIEW_PREFETCH_EOF_MAX_BYTES = 128 * 1024;
const lastPrefetchedReview = new Map<string, string>();

function isDeferredReviewFile(file: WorkspaceFileSummary) {
	const name = file.path.split("/").pop()?.toLowerCase() ?? "";
	return file.size > 512 * 1024 || /^(package-lock\.json|pnpm-lock\.yaml|yarn\.lock|bun\.lockb?|go\.sum|cargo\.lock)$/.test(name);
}

function defaultReviewFiles(data: WorkspaceFilesResponse): { commitSha?: string; files: WorkspaceFileSummary[]; scope: WorkspaceDiffScope } {
	// The Files tab count can be seeded from a files array alone. Prefetch only
	// runs against a full workspace response.
	const sections = data.sections;
	if (!sections) return { scope: "combined", files: [] };
	if (sections.unstaged.length > 0) return { scope: "unstaged", files: sections.unstaged };
	if (sections.staged.length > 0) return { scope: "staged", files: sections.staged };
	const commit = data.commits?.[0];
	if (commit) return { scope: "committed", commitSha: commit.sha, files: commit.files ?? [] };
	const untracked = new Set(sections.untracked.map((file) => file.path));
	return {
		scope: "combined",
		files: (data.files ?? []).filter((file) => file.status !== "unmodified" && !untracked.has(file.path)),
	};
}

export async function prefetchDefaultWorkspaceReviewDiffs(queryClient: QueryClient, sessionId: string, data: WorkspaceFilesResponse) {
	const selection = defaultReviewFiles(data);
	const files = selection.files.filter((file) => !isDeferredReviewFile(file)).slice(0, REVIEW_PREFETCH_BATCH_SIZE * REVIEW_PREFETCH_BATCHES);
	if (files.length === 0) return;
	const token = `${data.workspaceVersion ?? ""}:${selection.scope}:${selection.commitSha ?? ""}:${files.map((file) => file.path).join("\n")}`;

	try {
		const { REVIEW_CONTEXT_LINES, endsAtLastHunk, patchIdentity } = await import("../components/diffs/trailingContext");
		const { parsePatchFiles } = await import("@pierre/diffs");
		const headKey = sessionWorkspaceDiffsQueryKey(sessionId, selection.scope, files.slice(0, REVIEW_PREFETCH_BATCH_SIZE).map((file) => file.path), REVIEW_CONTEXT_LINES, false, data.workspaceVersion, selection.commitSha);
		// Skip only while the diff cache is still warm. Garbage collection would
		// otherwise leave the tab spinning and this function unwilling to refill it.
		if (lastPrefetchedReview.get(sessionId) === token && queryClient.getQueryData(headKey)) return;
		lastPrefetchedReview.set(sessionId, token);
		const batches: WorkspaceFileSummary[][] = [];
		for (let index = 0; index < files.length; index += REVIEW_PREFETCH_BATCH_SIZE) batches.push(files.slice(index, index + REVIEW_PREFETCH_BATCH_SIZE));
		const responses = await Promise.all(batches.map((batch) => queryClient.fetchQuery({
			...sessionWorkspaceDiffsQueryOptions({
				contextLines: REVIEW_CONTEXT_LINES,
				paths: batch.map((file) => file.path),
				scope: selection.scope,
				sessionId,
				workspaceVersion: data.workspaceVersion,
				commitSha: selection.commitSha,
			}),
			staleTime: Infinity,
			gcTime: 30 * 60 * 1000,
		})));

		const filesByPath = new Map(files.map((file) => [file.path, file]));
		const endOfFile: { file: WorkspaceFileSummary; identity: string }[] = [];
		for (const response of responses) {
			for (const group of response.groups) {
				if (group.truncated || endOfFile.length >= REVIEW_PREFETCH_EOF_MAX) continue;
				const prefix = group.repository ? `${group.repository}/` : "";
				const parsed = parsePatchFiles(group.patch, `${data.workspaceVersion ?? ""}:${selection.scope}:${group.patch.length}`, true).flatMap((entry) => entry.files);
				for (const metadata of parsed) {
					if (endOfFile.length >= REVIEW_PREFETCH_EOF_MAX) break;
					if (prefix && !metadata.name.startsWith(prefix)) metadata.name = prefix + metadata.name;
					if (prefix && metadata.prevName && !metadata.prevName.startsWith(prefix)) metadata.prevName = prefix + metadata.prevName;
					const file = filesByPath.get(metadata.name);
					if (!file || file.binary || file.size > REVIEW_PREFETCH_EOF_MAX_BYTES || !endsAtLastHunk(metadata)) continue;
					endOfFile.push({ file, identity: patchIdentity(metadata) });
				}
			}
		}

		await Promise.all(endOfFile.map(async ({ file, identity }) => {
			const queryKey = ["files-review-end-of-file", sessionId, selection.scope, selection.commitSha ?? "", file.path, file.fileFingerprint ?? "", identity] as const;
			if (queryClient.getQueryData(queryKey)) return;
			try {
				const [before, after] = await Promise.all([
					fetchWorkspaceFileRevision({ commitSha: selection.commitSha, sessionId, path: file.path, scope: selection.scope, side: "before", workspaceVersion: data.workspaceVersion }),
					fetchWorkspaceFileRevision({ commitSha: selection.commitSha, sessionId, path: file.path, scope: selection.scope, side: "after", workspaceVersion: data.workspaceVersion }),
				]);
				if (before.binary || after.binary || before.truncated || after.truncated) return;
				const loaded = {
					oldFile: { name: file.previousPath || file.path, contents: before.content, cacheKey: before.revision },
					newFile: { name: file.path, contents: after.content, cacheKey: after.revision },
				};
				await queryClient.prefetchQuery({ queryKey, queryFn: () => loaded, retry: false, staleTime: Infinity, gcTime: 30 * 60 * 1000 });
			} catch {
				// Leave the cache empty so the review pane can fetch this file itself.
			}
		}));
	} catch {
		if (lastPrefetchedReview.get(sessionId) === token) lastPrefetchedReview.delete(sessionId);
	}
}
