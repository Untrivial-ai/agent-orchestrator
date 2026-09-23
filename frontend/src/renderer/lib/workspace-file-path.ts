import type { WorkspaceFileSummary } from "../hooks/useSessionWorkspaceFiles";

function normalizeWorkspacePath(path: string): string {
	return path.trim().replace(/^\.\//, "").replace(/\\/g, "/");
}

function decodePath(path: string): string {
	try {
		return decodeURIComponent(path);
	} catch {
		return path;
	}
}

function hasNonFileScheme(reference: string): boolean {
	const trimmed = reference.trim();
	return (
		trimmed.startsWith("//") ||
		(/^[A-Za-z][A-Za-z\d+.-]*:/.test(trimmed) &&
			!/^file:\/\//i.test(trimmed) &&
			!/^\/?[A-Za-z]:[\\/]/.test(trimmed))
	);
}

/** Remove markdown/editor location syntax while retaining the referenced file. */
export function normalizeWorkspaceFileReference(reference: string): string {
	let path = reference.trim();
	if (/^file:\/\//i.test(path)) {
		try {
			path = new URL(path).pathname;
			// file:///C:/path is the URL form of a Windows drive path.
			if (/^\/[A-Za-z]:\//.test(path)) path = path.slice(1);
		} catch {
			return "";
		}
	}
	path = decodePath(path)
		.replace(/#L\d+(?:C\d+)?(?:-L?\d+(?:C\d+)?)?$/i, "")
		.replace(/:\d+(?::\d+)?$/, "");
	return normalizeWorkspacePath(path);
}

function fileBasename(path: string): string {
	const slash = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
	return slash >= 0 ? path.slice(slash + 1) : path;
}

/** Resolve a file reference only when it identifies a known workspace file. */
export function findWorkspaceFilePath(
	rawPath: string,
	paths: readonly string[],
): string | undefined {
	if (hasNonFileScheme(rawPath)) return undefined;
	const normalized = normalizeWorkspaceFileReference(rawPath);
	if (!normalized) return undefined;

	const exact = paths.find((path) => normalizeWorkspacePath(path) === normalized);
	if (exact) return exact;

	const suffixes = paths.filter((path) => {
		const candidate = normalizeWorkspacePath(path);
		return normalized.endsWith(`/${candidate}`) || candidate.endsWith(`/${normalized}`);
	});
	if (suffixes.length === 1) return suffixes[0];

	const base = fileBasename(normalized);
	const byBase = paths.filter((path) => fileBasename(normalizeWorkspacePath(path)) === base);
	return byBase.length === 1 ? byBase[0] : undefined;
}

/** A link-shaped local path can still target a new file absent from a stale catalog. */
export function explicitWorkspaceFilePath(reference: string): string | undefined {
	if (!reference.trim() || reference.trim().startsWith("#") || hasNonFileScheme(reference)) return undefined;
	const normalized = normalizeWorkspaceFileReference(reference);
	if (!normalized) return undefined;
	if (
		/^\//.test(normalized) ||
		/^\.\.\//.test(normalized) ||
		/^~\//.test(normalized) ||
		/^[A-Za-z]:\//.test(normalized) ||
		normalized.includes("/")
	) {
		return normalized;
	}
	return undefined;
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
	const normalized = normalizeWorkspaceFileReference(rawPath);
	if (!normalized) return rawPath;

	const exact = files.find((file) => normalizeWorkspacePath(file.path) === normalized);
	if (exact) return exact.path;

	const suffix = files.find((file) => {
		const candidate = normalizeWorkspacePath(file.path);
		return normalized.endsWith(`/${candidate}`) || candidate.endsWith(`/${normalized}`);
	});
	if (suffix) return suffix.path;

	const base = fileBasename(normalized);
	const byBase = files.filter((file) => fileBasename(normalizeWorkspacePath(file.path)) === base);
	if (byBase.length === 1) return byBase[0]!.path;

	return normalized;
}
