/**
 * Adapters that project cloud control-plane workspace/PR payloads into the
 * desktop inspector's local-daemon shapes so SessionFileExplorer, diff panes,
 * and SCM summary can render without a parallel UI.
 */

import type {
	CloudCpPullRequestSummary,
	CloudCpWorkspaceDiff,
	CloudCpWorkspaceDiffFile,
	CloudCpWorkspaceFile,
	CloudCpWorkspaceFileStatus,
} from "./cloud-cp";
import type { SessionPRSummary } from "../hooks/useSessionScmSummary";
import type {
	WorkspaceDiffScope,
	WorkspaceDiffsResponse,
	WorkspaceFileDetail,
	WorkspaceFilesResponse,
	WorkspaceFileSummary,
} from "../hooks/useSessionWorkspaceFiles";
import type { PRState, PullRequestFacts } from "../types/workspace";

const LOCAL_FILE_STATUSES = new Set(["unmodified", "modified", "added", "deleted", "renamed"]);

function toLocalFileStatus(
	status: CloudCpWorkspaceFileStatus,
): WorkspaceFileSummary["status"] {
	if (status === "untracked" || status === "copied" || status === "changed") {
		return status === "untracked" ? "added" : "modified";
	}
	if (LOCAL_FILE_STATUSES.has(status)) return status as WorkspaceFileSummary["status"];
	return "modified";
}

function fingerprint(path: string, status: string, additions: number, deletions: number, size = 0): string {
	return `${path}:${status}:${additions}:${deletions}:${size}`;
}

function toFileSummary(file: CloudCpWorkspaceDiffFile): WorkspaceFileSummary {
	const status = toLocalFileStatus(file.status);
	return {
		path: file.path,
		previousPath: file.oldPath,
		status,
		additions: file.additions,
		deletions: file.deletions,
		binary: file.binary,
		editable: status !== "deleted" && !file.binary,
		size: 0,
		fileFingerprint: fingerprint(file.path, status, file.additions, file.deletions),
	};
}

/** Map a cloud workspace.diff payload into the Files tab list/sections model. */
export function cloudDiffToWorkspaceFilesResponse(
	sessionId: string,
	diff: CloudCpWorkspaceDiff,
): WorkspaceFilesResponse {
	const untrackedPaths = new Set(diff.untrackedFiles ?? []);
	const files = (diff.files ?? []).map(toFileSummary);
	const untracked = files.filter((file) => untrackedPaths.has(file.path));
	const tracked = files.filter((file) => !untrackedPaths.has(file.path));
	// The worker collapses staged+unstaged into one status per path. Prefer the
	// combined working tree in the UI when both patch sides exist; otherwise put
	// tracked changes in unstaged so the review pane has a non-empty section.
	const hasStagedPatch = Boolean(diff.staged?.trim());
	const hasUnstagedPatch = Boolean(diff.unstaged?.trim());
	const sections = {
		committed: [] as WorkspaceFileSummary[],
		staged: hasStagedPatch && !hasUnstagedPatch ? tracked : [],
		unstaged: hasUnstagedPatch || !hasStagedPatch ? tracked : [],
		untracked,
	};
	if (hasStagedPatch && hasUnstagedPatch) {
		// Both sides exist: leave staged/unstaged empty so the pane uses combined.
		sections.staged = [];
		sections.unstaged = [];
	}
	const additions = files.reduce((sum, file) => sum + file.additions, 0);
	const deletions = files.reduce((sum, file) => sum + file.deletions, 0);
	return {
		sessionId,
		files,
		truncated: Boolean(diff.truncated?.combined || diff.truncated?.stats),
		sections,
		commits: [],
		summary: { files: files.length, additions, deletions },
		compareBaseRef: diff.diffBaseRef,
		compareBaseSha: diff.diffBaseSha,
		workspaceVersion: diff.diffBaseSha || "cloud",
	};
}

function patchForScope(diff: CloudCpWorkspaceDiff, scope: WorkspaceDiffScope): string {
	if (scope === "staged") return diff.staged ?? "";
	if (scope === "unstaged") return diff.unstaged ?? "";
	return diff.combined || [diff.staged, diff.unstaged].filter(Boolean).join("\n");
}

/** Serve the worker's unified patch as one diff group for the review pane. */
export function cloudDiffToWorkspaceDiffsResponse(
	sessionId: string,
	diff: CloudCpWorkspaceDiff,
	scope: WorkspaceDiffScope,
	paths: readonly string[],
): WorkspaceDiffsResponse {
	const wanted = new Set(paths);
	const patch = patchForScope(diff, scope);
	const includedPaths = (diff.files ?? [])
		.map((file) => file.path)
		.filter((path) => wanted.size === 0 || wanted.has(path));
	return {
		sessionId,
		workspaceVersion: diff.diffBaseSha || "cloud",
		groups: [
			{
				patch,
				truncated: Boolean(diff.truncated?.combined),
				includedPaths,
				deferred: [],
				errors: [],
			},
		],
	};
}

export function cloudFileToWorkspaceFileDetail(
	sessionId: string,
	file: CloudCpWorkspaceFile,
	status: WorkspaceFileSummary["status"] = "modified",
): WorkspaceFileDetail {
	return {
		sessionId,
		path: file.path,
		content: file.content,
		status,
		editable: status !== "deleted",
		binary: false,
		deleted: status === "deleted",
		size: file.size,
		additions: 0,
		deletions: 0,
		diff: "",
		diffTruncated: false,
		fileFingerprint: fingerprint(file.path, status, 0, 0, file.size),
		contentTruncated: false,
	};
}

export function cloudPullRequestToFacts(pr: CloudCpPullRequestSummary): PullRequestFacts {
	return {
		url: pr.url,
		number: pr.number,
		state: pr.state as PRState,
		ci: pr.ci?.state ?? "unknown",
		review: pr.review?.decision ?? "none",
		mergeability: pr.mergeability?.state ?? "unknown",
		reviewComments: Boolean(pr.review?.hasUnresolvedHumanComments),
		updatedAt: pr.updatedAt,
	};
}

/** Map CP pull-request summaries into the desktop SCM summary DTO. */
export function cloudPullRequestToSessionSummary(pr: CloudCpPullRequestSummary): SessionPRSummary {
	return {
		url: pr.url,
		htmlUrl: pr.htmlUrl || pr.url,
		number: pr.number,
		title: pr.title,
		state: pr.state,
		provider: pr.provider === "gitlab" ? "gitlab" : "github",
		repo: pr.repository,
		author: pr.author,
		sourceBranch: pr.sourceBranch,
		targetBranch: pr.targetBranch,
		headSha: pr.headSha,
		additions: pr.additions,
		deletions: pr.deletions,
		changedFiles: pr.changedFiles,
		ci: {
			state: (pr.ci?.state as SessionPRSummary["ci"]["state"]) || "unknown",
			failingChecks: (pr.ci?.failingChecks ?? []).map((check) => ({
				name: check.name,
				status: check.status === "cancelled" ? "cancelled" : "failed",
				conclusion: check.conclusion,
				url: check.url,
			})),
			autoInjectCI: false,
		},
		review: {
			decision: (pr.review?.decision as SessionPRSummary["review"]["decision"]) || "none",
			hasUnresolvedHumanComments: Boolean(pr.review?.hasUnresolvedHumanComments),
			unresolvedBy: [],
			reviews: [],
		},
		mergeability: {
			state: (pr.mergeability?.state as SessionPRSummary["mergeability"]["state"]) || "unknown",
			reasons: pr.mergeability?.reasons ?? [],
			prUrl: pr.mergeability?.pullRequestUrl || pr.url,
			conflictFiles: (pr.mergeability?.conflictFiles ?? []).map((file) => ({
				path: file.path,
				url: file.url,
			})),
		},
		stateChangedAt: pr.stateChangedAt,
		createdAt: pr.createdAt,
		updatedAt: pr.updatedAt,
		observedAt: pr.observedAt,
		ciObservedAt: pr.ciObservedAt,
		reviewObservedAt: pr.reviewObservedAt,
	};
}
