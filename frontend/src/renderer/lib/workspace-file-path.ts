import type { WorkspaceFileSummary } from "../hooks/useSessionWorkspaceFiles";

function normalizeWorkspacePath(path: string): string {
	return path.trim().replace(/^\.\//, "").replace(/\\/g, "/");
}

function fileBasename(path: string): string {
	const slash = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
	return slash >= 0 ? path.slice(slash + 1) : path;
}

/**
 * Map a chat/turn path onto the workspace-relative path the Files API expects.
 * Turn diffs often carry basenames or absolute worktree paths; the workspace
 * file list carries repo-relative paths.
 */
export function matchWorkspaceFilePath(
	rawPath: string,
	files: readonly WorkspaceFileSummary[],
): string {
	const normalized = normalizeWorkspacePath(rawPath);
	if (!normalized) return rawPath;

	const exact =
		files.find((file) => file.path === rawPath) ??
		files.find((file) => file.path === normalized);
	if (exact) return exact.path;

	const suffix = files.find(
		(file) => file.path.endsWith(`/${normalized}`) || file.path.endsWith(`/${rawPath}`),
	);
	if (suffix) return suffix.path;

	const base = fileBasename(normalized);
	const byBase = files.filter((file) => fileBasename(file.path) === base);
	if (byBase.length === 1) return byBase[0]!.path;

	return normalized;
}
