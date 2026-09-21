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

/**
 * Repo-qualify a turn changed-files row for display, but only when the answer is
 * unambiguous. The turn diff stores the provider's path verbatim, so a file changed
 * in a multi-repo workspace arrives as a bare `workspace-test.txt`, while the
 * workspace file list is repo-qualified (`alpha/workspace-test.txt`). When exactly
 * one changed file matches the row we show that qualified path; when two repos
 * changed a same-named file the row is genuinely ambiguous from this data, so we
 * keep the bare path rather than name the wrong repo.
 *
 * Unlike `matchWorkspaceFilePath` (the click resolver, which must always return a
 * best-effort open target) this returns the row unchanged on any ambiguity, so the
 * label never claims a repository the file may not live in.
 */
export function qualifyTurnDiffPath(
	rawPath: string,
	changedFiles: readonly WorkspaceFileSummary[],
): string {
	const normalized = normalizeWorkspacePath(rawPath);
	if (!normalized) return rawPath;

	if (changedFiles.some((file) => file.path === normalized)) return normalized;

	const matches = changedFiles.filter((file) => file.path.endsWith(`/${normalized}`));
	return matches.length === 1 ? matches[0]!.path : normalized;
}
