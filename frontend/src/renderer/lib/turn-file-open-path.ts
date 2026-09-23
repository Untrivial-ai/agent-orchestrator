import { fileChangeFiles, type ConversationItem } from "../types/conversation";

/**
 * Absolute paths and a worktree cwd gathered from the same turn's activities, so a
 * turn-diff basename can be shown like the Edited tooltip.
 */
export type TurnPathHints = {
	byBase: Map<string, string | undefined>;
	cwd?: string;
};

function fileBasename(path: string): string {
	const slash = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
	return slash >= 0 ? path.slice(slash + 1) : path;
}

export function looksAbsolutePath(path: string): boolean {
	return path.startsWith("/") || path.startsWith("~") || /^[A-Za-z]:[\\/]/.test(path);
}

function rememberTurnPathHint(byBase: Map<string, string | undefined>, absolutePath: string) {
	const base = fileBasename(absolutePath);
	if (!byBase.has(base)) {
		byBase.set(base, absolutePath);
		return;
	}
	if (byBase.get(base) !== absolutePath) byBase.set(base, undefined);
}

export function turnPathHints(items: ConversationItem[] | undefined): TurnPathHints {
	const byBase = new Map<string, string | undefined>();
	let cwd: string | undefined;
	if (!items?.length) return { byBase, cwd };

	for (const item of items) {
		if (item.kind !== "activity") continue;
		if (!cwd && item.detail?.cwd) cwd = item.detail.cwd;
		if (item.activityKind !== "file_change") continue;
		for (const file of fileChangeFiles(item)) {
			if (looksAbsolutePath(file.path)) rememberTurnPathHint(byBase, file.path);
			if (file.oldPath && looksAbsolutePath(file.oldPath)) rememberTurnPathHint(byBase, file.oldPath);
		}
	}
	return { byBase, cwd };
}

/** Prefer an absolute path from the turn; otherwise join the worktree cwd. */
export function resolveTurnFilePath(path: string, hints: TurnPathHints): string {
	if (looksAbsolutePath(path)) return path;
	const fromBasename = hints.byBase.get(fileBasename(path));
	if (fromBasename) return fromBasename;
	if (hints.cwd) {
		const rel = path.replace(/^\.\//, "");
		return `${hints.cwd.replace(/\/$/, "")}/${rel}`;
	}
	return path;
}

/** Strip a worktree root and keep enough suffix to disambiguate duplicate basenames. */
export function workspaceRelativeOpenPath(absolutePath: string, cwd?: string): string {
	const normalized = absolutePath.replace(/\\/g, "/");
	if (cwd) {
		const root = cwd.replace(/\\/g, "/").replace(/\/$/, "");
		if (normalized === root) return fileBasename(normalized);
		if (normalized.startsWith(`${root}/`)) {
			return normalized.slice(root.length + 1);
		}
	}
	const segments = normalized.split("/").filter(Boolean);
	if (segments.length >= 2) {
		return segments.slice(-2).join("/");
	}
	return fileBasename(normalized);
}

/** Workspace-relative path to open in the Files panel from a turn diff row. */
export function turnFileOpenPath(path: string, hints: TurnPathHints): string {
	const normalized = path.replace(/^\.\//, "");
	if (!looksAbsolutePath(normalized)) {
		const fromBasename = hints.byBase.get(fileBasename(normalized));
		if (fromBasename && looksAbsolutePath(fromBasename)) {
			return workspaceRelativeOpenPath(fromBasename, hints.cwd);
		}
		return normalized;
	}
	const resolved = resolveTurnFilePath(path, hints);
	if (!looksAbsolutePath(resolved)) return resolved.replace(/^\.\//, "");
	return workspaceRelativeOpenPath(resolved, hints.cwd);
}
