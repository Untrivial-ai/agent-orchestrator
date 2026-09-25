import { useCallback, useEffect, useMemo, useRef, useState, type ReactElement } from "react";
import { useQueries } from "@tanstack/react-query";
import { parsePatchFiles, type CodeViewItem, type FileDiffMetadata } from "@pierre/diffs";
import { CodeView } from "@pierre/diffs/react";
import { ChevronRight, ChevronsDownUp, ChevronsUpDown, FileCode2, GitCommitHorizontal, MessageSquarePlus, Pencil } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
	fetchWorkspaceFileRevision,
	sessionWorkspaceDiffsQueryOptions,
	type WorkspaceDiffScope,
	type WorkspaceCommitSummary,
	type WorkspaceFilesResponse,
	type WorkspaceFileSummary,
} from "../../hooks/useSessionWorkspaceFiles";
import { cn } from "../../lib/utils";
import { statusLabel, statusTone } from "../../lib/workspace-file-status";
import { useUiStore } from "../../stores/ui-store";
import { type FileOpenOptions } from "../FileContentPane";
import { PanelMessage, RetryButton, FileAnnotationComposer, LineFeedbackButtonControl, type FileAnnotationModel } from "../WorkspaceDiffView";
import { VscodeGoToFileIcon } from "../icons/VscodeGoToFileIcon";
import { WorkspaceEntryIcon } from "../WorkspaceEntryIcon";
import { Button } from "../ui/button";
import { Checkbox } from "../ui/checkbox";
import { MENU_TRIGGER_CHROME } from "../ui/option-menu";
import { SettingsMenuTrigger } from "../settings/SettingsMenuTrigger";
import { Popover, PopoverAnchor, PopoverContent } from "../ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { formatTimeTerse } from "../../lib/format-time";
import { AO_PIERRE_FILES_REVIEW_CSS, AO_PIERRE_SURFACE_CSS } from "./pierreTheme";
import { REVIEW_CONTEXT_LINES, diffContentVersion, endsAtLastHunk, hydratedCopy, patchIdentity } from "./trailingContext";
import { usePersistentGutterUtility } from "./usePersistentGutterUtility";

const PATCH_BATCH_SIZE = 100;
const parsedPatchCache = new Map<string, FileDiffMetadata[]>();
const MAX_PARSED_GROUPS = 24;
const workingScopeOrder = ["unstaged", "staged"] as const;
const SOURCE_CONTROL = MENU_TRIGGER_CHROME;
// Height of the custom per-file header row below (h-9).
const FILE_HEADER_HEIGHT_PX = 36;
// Files whose patch proves they end at the last hunk get their full contents up
// front, so their diff has no dead "More unchanged context may be available"
// row. Bounded so a big review doesn't fetch every file; beyond these limits the
// row stays and still loads on click.
const MAX_END_OF_FILE_PREFETCHES = 40;
const END_OF_FILE_PREFETCH_MAX_BYTES = 128 * 1024;
// Longest a first display waits for those contents before showing patch-only diffs.
const END_OF_FILE_HOLD_MS = 1500;

export type ReviewSourceMenu = {
	/** Short description of the current review source, shown on the trigger. */
	label: string;
	scopes: { key: string; label: string; selected: boolean; select: () => void }[];
	commits: { sha: string; subject: string; timestamp: string; selected: boolean; select: () => void }[];
};

function chunked<T>(items: readonly T[], size: number): T[][] {
	const chunks: T[][] = [];
	for (let index = 0; index < items.length; index += size) chunks.push(items.slice(index, index + size));
	return chunks;
}

function patchCacheKey(workspaceVersion: string | undefined, scope: WorkspaceDiffScope, commitSha: string | undefined, repository: string | undefined, patch: string) {
	return `${workspaceVersion ?? "legacy"}:${scope}:${commitSha ?? "working"}:${repository ?? "root"}:${patch.length}:${patch.slice(0, 80)}:${patch.slice(-80)}`;
}

function parseGroupPatch(workspaceVersion: string | undefined, scope: WorkspaceDiffScope, commitSha: string | undefined, repository: string | undefined, patch: string) {
	const key = patchCacheKey(workspaceVersion, scope, commitSha, repository, patch);
	const cached = parsedPatchCache.get(key);
	if (cached) return cached;
	const prefix = repository ? `${repository}/` : "";
	const files = parsePatchFiles(patch, key, true).flatMap((entry) => entry.files);
	for (const file of files) {
		if (prefix && !file.name.startsWith(prefix)) file.name = prefix + file.name;
		if (prefix && file.prevName && !file.prevName.startsWith(prefix)) file.prevName = prefix + file.prevName;
	}
	parsedPatchCache.set(key, files);
	if (parsedPatchCache.size > MAX_PARSED_GROUPS) {
		const oldest = parsedPatchCache.keys().next().value;
		if (oldest) parsedPatchCache.delete(oldest);
	}
	return files;
}

function sectionFiles(data: WorkspaceFilesResponse, scope: WorkspaceDiffScope): WorkspaceFileSummary[] {
	if (scope === "combined") {
		const untrackedPaths = new Set(data.sections.untracked.map((file) => file.path));
		return data.files.filter((file) => file.status !== "unmodified" && !untrackedPaths.has(file.path));
	}
	return data.sections[scope];
}

function initialReviewSelection(data: WorkspaceFilesResponse): { commitSha?: string; scope: WorkspaceDiffScope } {
	const workingScope = workingScopeOrder.find((scope) => data.sections[scope].length > 0);
	if (workingScope) return { scope: workingScope };
	if (data.commits[0]) return { scope: "committed", commitSha: data.commits[0].sha };
	return { scope: "combined" };
}

function isDeferredByDefault(file: WorkspaceFileSummary) {
	const name = file.path.split("/").pop()?.toLowerCase() ?? "";
	return file.size > 512 * 1024 || /^(package-lock\.json|pnpm-lock\.yaml|yarn\.lock|bun\.lockb?|go\.sum|cargo\.lock)$/.test(name);
}

function canOpenRendered(file: WorkspaceFileSummary) {
	return !file.binary && file.status !== "deleted" && /\.(md|markdown)$/i.test(file.path);
}

type ViewedRecord = Record<string, string>;

function useViewedFiles(sessionId: string, selectionKey: string, files: readonly WorkspaceFileSummary[]) {
	const key = `ao.files.viewed.${sessionId}.${selectionKey}`;
	const [records, setRecords] = useState<ViewedRecord>(() => {
		try {
			return JSON.parse(window.localStorage.getItem(key) ?? "{}") as ViewedRecord;
		} catch {
			return {};
		}
	});
	useEffect(() => {
		try {
			setRecords(JSON.parse(window.localStorage.getItem(key) ?? "{}") as ViewedRecord);
		} catch {
			setRecords({});
		}
	}, [key]);
	const viewed = useMemo(
		() => new Set(files.filter((file) => records[file.path] === (file.fileFingerprint ?? "legacy")).map((file) => file.path)),
		[files, records],
	);
	const toggle = useCallback(
		(file: WorkspaceFileSummary) => {
			setRecords((current) => {
				const next = { ...current };
				if (next[file.path] === (file.fileFingerprint ?? "legacy")) delete next[file.path];
				else next[file.path] = file.fileFingerprint ?? "legacy";
				window.localStorage.setItem(key, JSON.stringify(next));
				return next;
			});
		},
		[key],
	);
	return { viewed, toggle };
}

export function WorkspaceReviewPane({
	annotation,
	data,
	filter,
	onBrowseAll,
	onOpenFile,
	onSourceMenuChange,
	sessionId,
	split,
}: {
	annotation: FileAnnotationModel;
	/**
	 * When set, the scope/commit choices are handed to the parent's source menu
	 * (one dropdown for "what am I reviewing") instead of rendering inline.
	 */
	onSourceMenuChange?: (menu: ReviewSourceMenu | null) => void;
	data: WorkspaceFilesResponse;
	filter: string;
	onBrowseAll: () => void;
	onOpenFile?: (path: string, options?: FileOpenOptions) => void;
	sessionId: string;
	split: boolean;
}) {
	const { t } = useTranslation();
	const resolvedTheme = useUiStore((state) => state.resolvedTheme);
	const initialSelection = useMemo(() => initialReviewSelection(data), [data]);
	const [scope, setScope] = useState<WorkspaceDiffScope>(() => initialSelection.scope);
	const [selectedCommitSha, setSelectedCommitSha] = useState<string | undefined>(() => initialSelection.commitSha);
	const [commitBrowserOpen, setCommitBrowserOpen] = useState(false);
	const [collapsedPaths, setCollapsedPaths] = useState<Set<string>>(() => new Set());
	const [loadedDeferredPaths, setLoadedDeferredPaths] = useState<Set<string>>(() => new Set());
	const [activeBatchCount, setActiveBatchCount] = useState(4);
	const reviewRef = useRef<HTMLDivElement>(null);
	const gutterHover = usePersistentGutterUtility(reviewRef);

	const selectedCommit = useMemo(
		() => data.commits.find((commit) => commit.sha === selectedCommitSha),
		[data.commits, selectedCommitSha],
	);
	const visibleWorkingScopes = useMemo(
		() => workingScopeOrder.filter((entry) => data.sections[entry].length > 0),
		[data.sections],
	);
	const combinedWorkingCount = sectionFiles(data, "combined").length;
	const showCombinedWorkingSource = visibleWorkingScopes.length === 0
		&& data.sections.committed.length === 0
		&& data.commits.length === 0
		&& !data.compareBaseSha
		&& !data.compareBaseRef
		&& combinedWorkingCount > 0;
	const workingSourceOptions: WorkspaceDiffScope[] = showCombinedWorkingSource ? ["combined"] : [...visibleWorkingScopes];
	useEffect(() => {
		if (scope === "committed" && selectedCommit) return;
		if (scope === "combined" && showCombinedWorkingSource) return;
		if (scope !== "committed" && scope !== "combined" && data.sections[scope].length > 0) return;
		const next = initialReviewSelection(data);
		setScope(next.scope);
		setSelectedCommitSha(next.commitSha);
	}, [data, initialSelection, scope, selectedCommit, showCombinedWorkingSource]);

	const allFiles = useMemo(
		() => scope === "committed" && selectedCommit ? selectedCommit.files : sectionFiles(data, scope),
		[data, scope, selectedCommit],
	);
	const reviewSelectionKey = selectedCommit ? `commit:${selectedCommit.sha}` : scope;
	const normalizedFilter = filter.trim().toLowerCase();
	const files = useMemo(
		() => (normalizedFilter ? allFiles.filter((file) => `${file.path} ${file.previousPath ?? ""}`.toLowerCase().includes(normalizedFilter)) : allFiles),
		[allFiles, normalizedFilter],
	);
	const { viewed, toggle: toggleViewed } = useViewedFiles(sessionId, reviewSelectionKey, allFiles);

	useEffect(() => {
		setCollapsedPaths(new Set(files.filter(isDeferredByDefault).map((file) => file.path)));
		setLoadedDeferredPaths(new Set());
		setActiveBatchCount(4);
	}, [data.workspaceVersion, reviewSelectionKey]);

	const requestedFiles = useMemo(
		() => files.filter((file) => !isDeferredByDefault(file) || loadedDeferredPaths.has(file.path)),
		[files, loadedDeferredPaths],
	);
	const batches = useMemo(() => chunked(requestedFiles.map((file) => file.path), PATCH_BATCH_SIZE), [requestedFiles]);
	const patchQueries = useQueries({
		queries: batches.map((paths, index) => ({
			...sessionWorkspaceDiffsQueryOptions({
				// endsAtLastHunk reads the trailing context, so request exactly what it assumes.
				contextLines: REVIEW_CONTEXT_LINES,
				errorMessage: t("files.error.loadWorkspace"),
				paths,
				scope,
				sessionId,
				workspaceVersion: data.workspaceVersion,
				commitSha: selectedCommit?.sha,
			}),
			enabled: !commitBrowserOpen && paths.length > 0 && index < activeBatchCount,
			staleTime: Infinity,
		})),
	});
	useEffect(() => {
		const active = patchQueries.slice(0, activeBatchCount);
		if (active.length < activeBatchCount || active.some((query) => query.isPending || query.isFetching)) return;
		if (activeBatchCount < batches.length) setActiveBatchCount((current) => Math.min(current + 4, batches.length));
	}, [activeBatchCount, batches.length, patchQueries]);

	const { metadataByPath, endOfFilePaths } = useMemo(() => {
		const result = new Map<string, FileDiffMetadata>();
		const endOfFile = new Set<string>();
		for (const query of patchQueries) {
			for (const group of query.data?.groups ?? []) {
				try {
					for (const metadata of parseGroupPatch(query.data?.workspaceVersion, scope, selectedCommit?.sha, group.repository, group.patch)) {
						result.set(metadata.name, metadata);
						// A truncated group can cut its last patch short, which would look
						// like that file ending early.
						if (!group.truncated && endsAtLastHunk(metadata)) endOfFile.add(metadata.name);
					}
				} catch {
					// The group retains its retry/error surface below; one malformed patch
					// must not prevent other repositories from rendering.
				}
			}
		}
		return { metadataByPath: result, endOfFilePaths: endOfFile };
	}, [patchQueries, scope, selectedCommit?.sha]);
	const serverDeferredByPath = useMemo(() => {
		const result = new Map<string, string>();
		for (const query of patchQueries) {
			for (const group of query.data?.groups ?? []) {
				for (const deferred of group.deferred) result.set(deferred.path, deferred.reason);
			}
		}
		return result;
	}, [patchQueries]);
	// A batch still in flight (or still queued behind activeBatchCount) is the
	// only reason a requested file can legitimately have no patch yet. Once its
	// batch settles, a file with no diff is a failure the user can retry or step
	// around, not a load that will finish on its own.
	const pendingDiffPaths = useMemo(() => {
		const result = new Set<string>();
		batches.forEach((paths, index) => {
			const query = patchQueries[index];
			if (!query || query.isPending || query.isFetching) for (const path of paths) result.add(path);
		});
		return result;
	}, [batches, patchQueries]);

	const summaryById = useMemo(() => new Map(files.map((file) => [`${reviewSelectionKey}:${file.path}`, file])), [files, reviewSelectionKey]);

	const loadDiffFiles = useCallback(
		async (metadata: FileDiffMetadata) => {
			// Pierre may hand this callback a normalized metadata object rather than
			// the exact object stored in our parse cache, so resolve by stable path.
			const file = files.find((candidate) => candidate.path === metadata.name);
			if (!file) throw new Error(t("files.error.loadFile"));
			const [before, after] = await Promise.all([
				fetchWorkspaceFileRevision({ commitSha: selectedCommit?.sha, sessionId, path: file.path, scope, side: "before", workspaceVersion: data.workspaceVersion }),
				fetchWorkspaceFileRevision({ commitSha: selectedCommit?.sha, sessionId, path: file.path, scope, side: "after", workspaceVersion: data.workspaceVersion }),
			]);
			if (before.binary || after.binary || before.truncated || after.truncated) throw new Error(t("files.error.loadFile"));
			const newFile = { name: file.path, contents: after.content, cacheKey: after.revision };
			if (metadata.type === "rename-pure") return { oldFile: null, newFile };
			return { oldFile: { name: file.previousPath || file.path, contents: before.content, cacheKey: before.revision }, newFile };
		},
		[data.workspaceVersion, files, scope, selectedCommit?.sha, sessionId, t],
	);

	// Keyed by patch content (not workspace version), so a refresh that leaves a
	// file's diff unchanged reuses the contents instead of flashing the row back.
	const endOfFileFiles = useMemo(
		() => files.filter((file) => endOfFilePaths.has(file.path) && file.size <= END_OF_FILE_PREFETCH_MAX_BYTES).slice(0, MAX_END_OF_FILE_PREFETCHES),
		[endOfFilePaths, files],
	);
	const endOfFileContents = useQueries({
		queries: endOfFileFiles.map((file) => {
			const metadata = metadataByPath.get(file.path);
			return {
				queryKey: ["files-review-end-of-file", sessionId, scope, selectedCommit?.sha ?? "", file.path, file.fileFingerprint ?? "", metadata ? patchIdentity(metadata) : ""] as const,
				queryFn: () => {
					if (!metadata) throw new Error(t("files.error.loadFile"));
					return loadDiffFiles(metadata);
				},
				enabled: metadata != null,
				retry: false,
				staleTime: Infinity,
			};
		}),
	});

	// While a file's full contents load, its patch-only diff would flash Pierre's
	// trailing row, so it isn't shown: a file already on screen keeps its previous
	// diff, and a first display waits (showing "Loading diff") so the list doesn't
	// shift as files arrive. END_OF_FILE_HOLD_MS caps the wait if a request stalls.
	const shownDiffsRef = useRef(new Map<string, FileDiffMetadata>());
	const endOfFileLoading = endOfFileContents.some((query) => query.isLoading);
	const [endOfFileHoldExpired, setEndOfFileHoldExpired] = useState(false);
	useEffect(() => {
		if (!endOfFileLoading) {
			setEndOfFileHoldExpired(false);
			return;
		}
		const timer = window.setTimeout(() => setEndOfFileHoldExpired(true), END_OF_FILE_HOLD_MS);
		return () => window.clearTimeout(timer);
	}, [endOfFileLoading]);

	const { items, holdingForEndOfFile } = useMemo(
		() => {
			const shown = shownDiffsRef.current;
			const itemId = (path: string) => `${reviewSelectionKey}:${path}`;
			const endOfFile = new Map<string, FileDiffMetadata | "loading">();
			endOfFileFiles.forEach((file, index) => {
				const metadata = metadataByPath.get(file.path);
				const query = endOfFileContents[index];
				const hydrated = metadata && query?.data ? hydratedCopy(metadata, query.data) : null;
				if (hydrated) endOfFile.set(file.path, hydrated);
				else if (query?.isLoading && !endOfFileHoldExpired) endOfFile.set(file.path, shown.get(itemId(file.path)) ?? "loading");
			});
			let holding = false;
			const next = files.flatMap((file): CodeViewItem<"feedback">[] => {
				if (file.binary) return [];
				const metadata = metadataByPath.get(file.path);
				if (!metadata) return [];
				const endOfFileDiff = endOfFile.get(file.path);
				if (endOfFileDiff === "loading") {
					holding = true;
					return [];
				}
				const fileDiff = endOfFileDiff ?? metadata;
				const collapsed = collapsedPaths.has(file.path);
				const fileAnnotationActive = annotation.target?.surface !== "focused" && annotation.target?.path === file.path && annotation.target.side === "file";
				const activeTarget = annotation.target?.surface !== "focused" && annotation.target?.path === file.path && annotation.target.side !== "file"
					? annotation.target
					: null;
				return [{
					id: itemId(file.path),
					type: "diff",
					fileDiff,
					collapsed,
					annotations: activeTarget?.line != null ? [{
						lineNumber: activeTarget.line,
						side: activeTarget.side === "old" ? "deletions" : "additions",
						metadata: "feedback",
					}] : undefined,
					// CodeView only re-reads an item when its version changes: the content
					// part lets changed or newly hydrated diffs through, the low bits carry
					// the collapsed/annotation state.
					version: diffContentVersion(fileDiff) * 8 + (collapsed ? 1 : 0) + (activeTarget ? 2 : 0) + (fileAnnotationActive ? 4 : 0),
				}];
			});
			// Nothing from this review is on screen yet: hold the whole list rather
			// than inserting the held files later.
			const firstDisplay = !files.some((file) => shown.has(itemId(file.path)));
			return { items: holding && firstDisplay ? [] : next, holdingForEndOfFile: holding };
		},
		[annotation.target, collapsedPaths, endOfFileContents, endOfFileFiles, endOfFileHoldExpired, files, metadataByPath, reviewSelectionKey],
	);
	useEffect(() => {
		shownDiffsRef.current = new Map(items.flatMap((item) => (item.type === "diff" ? [[item.id, item.fileDiff] as const] : [])));
	}, [items]);

	const beginLineAnnotation = useCallback((itemId: string, lineNumber: number, side: "deletions" | "additions") => {
		const file = summaryById.get(itemId);
		if (!file) return;
		annotation.begin({
			path: file.path,
			previousPath: file.previousPath,
			side: side === "deletions" ? "old" : "new",
			line: lineNumber,
			oldLine: side === "deletions" ? lineNumber : undefined,
			newLine: side === "additions" ? lineNumber : undefined,
			scope,
			workspaceVersion: data.workspaceVersion,
			fileFingerprint: file.fileFingerprint,
			surface: "review",
		});
	}, [annotation, data.workspaceVersion, scope, summaryById]);
	const toggleCollapsed = useCallback((path: string) => {
		if (annotation.target?.surface === "review" && annotation.target.path === path) annotation.cancel();
		setCollapsedPaths((current) => {
			const next = new Set(current);
			if (next.has(path)) next.delete(path);
			else next.add(path);
			return next;
		});
	}, [annotation]);
	const collapseAll = useCallback(() => {
		if (annotation.target?.surface === "review") annotation.cancel();
		setCollapsedPaths(new Set(files.map((file) => file.path)));
	}, [annotation, files]);
	const expandAll = useCallback(() => {
		setLoadedDeferredPaths(new Set(files.filter(isDeferredByDefault).map((file) => file.path)));
		setCollapsedPaths(new Set());
	}, [files]);
	const allFilesCollapsed = files.length > 0 && files.every((file) => collapsedPaths.has(file.path));
	const toggleAll = allFilesCollapsed ? expandAll : collapseAll;
	const selectCommit = useCallback((commit: WorkspaceCommitSummary) => {
		if ((scope !== "committed" || selectedCommitSha !== commit.sha) && annotation.target?.surface === "review") annotation.cancel();
		setSelectedCommitSha(commit.sha);
		setScope("committed");
		setCommitBrowserOpen(false);
	}, [annotation, scope, selectedCommitSha]);
	const selectScope = useCallback((nextScope: WorkspaceDiffScope) => {
		if (nextScope !== scope && annotation.target?.surface === "review") annotation.cancel();
		setScope(nextScope);
		setSelectedCommitSha(undefined);
		setCommitBrowserOpen(false);
	}, [annotation, scope]);

	const retryAll = () => patchQueries.forEach((query) => void query.refetch());
	const firstError = patchQueries.find((query) => query.error)?.error;
	const groupError = patchQueries.flatMap((query) => query.data?.groups ?? []).flatMap((group) => group.errors ?? [])[0];
	const loading = patchQueries.some((query) => query.isPending);
	const viewedCount = allFiles.filter((file) => viewed.has(file.path)).length;
	const fileOpenContext = selectedCommit ? { commitSha: selectedCommit.sha, scope } : { scope };
	const workingSourceLabel = (entry: WorkspaceDiffScope) => entry === "combined" ? t("files.reviewChanges") : t(`files.section.${entry}`);
	const hasAnyReviewFiles = data.files.some((file) => file.status !== "unmodified")
		|| workingScopeOrder.some((entry) => data.sections[entry].length > 0)
		|| data.commits.some((commit) => commit.files.length > 0);
	const commitHashForButton = (selectedCommit?.sha ?? data.commits[0]?.sha)?.slice(0, 7);
	const showReviewScopeSwitcher = hasAnyReviewFiles && workingSourceOptions.length > 0;

	// Handlers change with the annotation model; read them through a ref so the
	// published menu only changes when what it shows changes.
	const menuActionsRef = useRef({ selectCommit, selectScope });
	menuActionsRef.current = { selectCommit, selectScope };
	const sourceMenu = useMemo<ReviewSourceMenu>(() => {
		const label = (entry: WorkspaceDiffScope) => entry === "combined" ? t("files.reviewChanges") : t(`files.section.${entry}`);
		const scopeEntries: WorkspaceDiffScope[] = showCombinedWorkingSource ? ["combined"] : [...visibleWorkingScopes];
		return {
			label: selectedCommit ? selectedCommit.sha.slice(0, 7) : label(scope),
			scopes: showReviewScopeSwitcher
				? scopeEntries.map((entry) => ({ key: entry, label: label(entry), selected: scope === entry, select: () => menuActionsRef.current.selectScope(entry) }))
				: [],
			commits: data.commits.map((commit) => ({ sha: commit.sha, subject: commit.subject, timestamp: commit.timestamp, selected: scope === "committed" && selectedCommit?.sha === commit.sha, select: () => menuActionsRef.current.selectCommit(commit) })),
		};
	}, [data, scope, selectedCommit, showCombinedWorkingSource, showReviewScopeSwitcher, t, visibleWorkingScopes]);
	useEffect(() => {
		onSourceMenuChange?.(sourceMenu);
	}, [onSourceMenuChange, sourceMenu]);
	useEffect(() => () => onSourceMenuChange?.(null), [onSourceMenuChange]);
	const totalAdditions = allFiles.reduce((sum, file) => sum + file.additions, 0);
	const totalDeletions = allFiles.reduce((sum, file) => sum + file.deletions, 0);
	// Scope + Commits pickers wear the same trigger chrome as the source picker
	// they sit next to; an unselected scope stays a quiet ghost.
	const sourceControls = (
		<>
			{showReviewScopeSwitcher ? workingSourceOptions.map((entry) => (
				<button
					aria-pressed={scope === entry}
					className={cn(SOURCE_CONTROL, "h-control-md shrink-0 bg-transparent text-xs", scope === entry ? "text-foreground" : "text-muted-foreground")}
					disabled={!entry}
					key={entry}
					onClick={() => selectScope(entry)}
					type="button"
				>
					{workingSourceLabel(entry)}
				</button>
			)) : null}
			<SettingsMenuTrigger
				aria-expanded={commitBrowserOpen}
				aria-pressed={scope === "committed"}
				className={cn("h-control-md min-w-0 shrink bg-transparent text-xs", scope === "committed" || commitBrowserOpen ? "text-foreground" : "text-muted-foreground")}
				data-state={commitBrowserOpen ? "open" : "closed"}
				disabled={data.commits.length === 0}
				onClick={() => setCommitBrowserOpen((open) => !open)}
			>
				<GitCommitHorizontal aria-hidden="true" className="size-icon-sm" />
				<span className="shrink-0">{t("files.commits")}</span>
				{commitHashForButton ? <span className="min-w-0 truncate text-caption text-passive">{commitHashForButton}</span> : null}
			</SettingsMenuTrigger>
		</>
	);

	return (
		<div
			className="flex h-full min-h-0 flex-col"
			onPointerLeave={gutterHover.onPointerLeave}
			onPointerMove={gutterHover.onPointerMove}
			ref={reviewRef}
		>
			{/* Context row (like a VCS "Committed ▾ <subject> +x −y" bar): what is
			    being reviewed on the left, review progress on the right. Trailing
			    controls sit on the Files header's action columns (18px / 50px from
			    the right edge), so the right side reads as one aligned column. */}
			<div className="flex h-8 shrink-0 items-center gap-2 border-b border-border pb-1 pl-3 pr-1">
				{onSourceMenuChange ? null : <div className="flex shrink-0 items-center rounded-md bg-[var(--color-bg-settings-trigger)]">{sourceControls}</div>}
				{commitBrowserOpen ? (
					<span className="min-w-0 truncate text-caption text-muted-foreground">{t("files.selectCommit")}</span>
				) : (
					<>
						<span className="min-w-0 truncate text-xs text-foreground" title={selectedCommit?.subject}>
							{selectedCommit ? selectedCommit.subject : workingSourceLabel(scope)}
						</span>
						{selectedCommit ? <span className="shrink-0 text-caption text-passive">{selectedCommit.sha.slice(0, 7)}</span> : null}
						<span className="flex shrink-0 items-center gap-1.5 text-caption tabular-nums">
							<span className="text-success">+{totalAdditions}</span>
							<span className="text-error">−{totalDeletions}</span>
						</span>
						<div className="ml-auto flex shrink-0 items-center gap-1 text-caption text-muted-foreground">
							<span className="tabular-nums">{t("files.reviewProgress", { total: allFiles.length, viewed: viewedCount })}</span>
							<HeaderActionTooltip label={t(allFilesCollapsed ? "files.expandAll" : "files.collapseAll")}>
								<Button aria-label={t(allFilesCollapsed ? "files.expandAll" : "files.collapseAll")} onClick={toggleAll} size="icon-sm" type="button" variant="ghost">
									{allFilesCollapsed ? <ChevronsUpDown aria-hidden="true" /> : <ChevronsDownUp aria-hidden="true" />}
								</Button>
							</HeaderActionTooltip>
						</div>
					</>
				)}
			</div>
			{commitBrowserOpen ? (
				<CommitBrowser
					commits={data.commits}
					filter={filter}
					onSelect={selectCommit}
					selectedSha={selectedCommit?.sha}
				/>
			) : (
				<>
			{firstError ? <PanelMessage action={<RetryButton onClick={retryAll} />}>{firstError.message}</PanelMessage> : null}
			{groupError ? <PanelMessage action={<RetryButton onClick={retryAll} />}>{groupError.message}</PanelMessage> : null}
			{(loading || holdingForEndOfFile) && items.length === 0 ? <PanelMessage compact>{t("files.loadingDiff")}</PanelMessage> : null}
			{files.length === 0 ? <PanelMessage action={allFiles.length === 0 ? <Button onClick={onBrowseAll}>{t("files.browseAll")}</Button> : undefined} compact>{allFiles.length === 0 ? t(hasAnyReviewFiles ? "files.noneInSource" : "files.noneChanged") : t("files.noFilterMatches")}</PanelMessage> : null}
			<div className="min-h-0 flex-1 overflow-hidden">
				{items.length > 0 ? (
					<CodeView<"feedback">
						className="ao-pierre-surface board-scrollbar h-full min-h-0 select-text overflow-y-auto overscroll-contain"
						disableWorkerPool={typeof Worker === "undefined"}
						items={items}
						options={{
							collapsedContextThreshold: 8,
							diffIndicators: "classic",
							diffStyle: split ? "split" : "unified",
							enableGutterUtility: true,
							expansionLineCount: 20,
							hunkSeparators: "line-info",
							lineDiffType: "word-alt",
							layout: { gap: 4, paddingBottom: 8, paddingTop: 0 },
							// Must match the custom file header height (h-9 = 36px). Pierre's default
							// (44px) reserves the difference and pushes the header down by 8px.
							itemMetrics: { diffHeaderHeight: FILE_HEADER_HEIGHT_PX },
							lineHoverHighlight: "line",
							loadDiffFiles,
							maxLineDiffLength: 400,
							onPostRender: gutterHover.restoreAfterRender,
							overflow: "wrap",
							stickyHeaders: true,
							theme: { dark: "github-dark", light: "github-light" },
							themeType: resolvedTheme,
							tokenizeMaxLength: 200_000,
							tokenizeMaxLineLength: 2_000,
							unsafeCSS: AO_PIERRE_SURFACE_CSS + AO_PIERRE_FILES_REVIEW_CSS,
						}}
						renderAnnotation={() => <FileAnnotationComposer annotation={annotation} />}
						renderGutterUtility={(getHoveredLine, item) => (
							<LineFeedbackButtonControl
								gutter
								label={t("files.addFeedback")}
								onClick={() => {
									const line = getHoveredLine();
									if (!line) return;
									const side = "side" in line ? line.side : undefined;
									if (side === "additions" || side === "deletions") beginLineAnnotation(item.id, line.lineNumber, side);
								}}
							/>
						)}
						renderCustomHeader={(item) => {
							const file = summaryById.get(item.id);
							if (!file) return null;
							const isViewed = viewed.has(file.path);
							const isCollapsed = collapsedPaths.has(file.path);
							const renderedAvailable = canOpenRendered(file);
							const fileAnnotationActive = annotation.target?.surface !== "focused" && annotation.target?.path === file.path && annotation.target.side === "file";
							return (
								<Popover onOpenChange={(open) => { if (!open) annotation.cancel(); }} open={fileAnnotationActive}>
									<div className="relative bg-background">
										{/* The whole row toggles the file (the chevron just rotates);
										    name + stats on the left, every action grouped on the right
										    with "viewed" pinned to the far edge. */}
										<PopoverAnchor asChild>
											<div className="group/file-header flex h-9 min-w-0 cursor-pointer items-center gap-2 pl-4 pr-1.5 hover:bg-interactive-hover/40" onClick={() => toggleCollapsed(file.path)}>
												<ChevronRight aria-hidden="true" className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform duration-150 group-hover/file-header:text-foreground", !isCollapsed && "rotate-90")} />
												<WorkspaceEntryIcon className="size-icon-xl" kind="file" name={file.path.split("/").pop() ?? file.path} />
												<div className="flex min-w-0 shrink items-baseline gap-3">
													<button
														aria-expanded={!isCollapsed}
														aria-label={isCollapsed ? t("files.expandFile", { file: file.path }) : t("files.collapseFile", { file: file.path })}
														className="flex min-w-0 shrink items-baseline text-left text-[length:var(--font-size-base)] outline-none focus-visible:underline"
														title={file.path}
														type="button"
													>
														{file.path.includes("/") ? <span className="min-w-0 truncate text-muted-foreground">{file.path.slice(0, file.path.lastIndexOf("/") + 1)}</span> : null}
														<span className="max-w-full shrink-0 truncate text-foreground">{file.path.slice(file.path.lastIndexOf("/") + 1)}</span>
													</button>
													<span className="flex shrink-0 items-baseline gap-2.5 text-xs tabular-nums">
														<span className={cn("font-semibold", statusTone[file.status])}>{statusLabel[file.status]}</span>
														<span className="flex items-baseline gap-1.5">
															<span className="text-success">+{file.additions}</span>
															<span className="text-error">−{file.deletions}</span>
														</span>
													</span>
												</div>
												{/* Every action is always visible in a 24px slot, 8px apart: the same
												    32px pitch as the Files header buttons, so the checkbox and feedback
												    button sit under the header's trailing columns. */}
												<div className="ml-auto flex shrink-0 items-center gap-2 pl-2" onClick={(event) => event.stopPropagation()}>
													{file.editable && file.fileFingerprint ? (
														<HeaderActionTooltip label={t("files.editFile")}>
															<Button aria-label={t("files.editFile")} className="size-6 text-muted-foreground hover:text-foreground" onClick={() => onOpenFile?.(file.path, { editing: true, mode: "file", scope })} size="icon-sm" type="button" variant="ghost"><Pencil aria-hidden="true" className="size-icon-sm" /></Button>
														</HeaderActionTooltip>
													) : null}
													<HeaderActionTooltip label={renderedAvailable ? t("files.openRichPreview") : t("files.openFullFileGeneric")}>
														<Button aria-label={renderedAvailable ? t("files.openRichPreview") : t("files.openFullFileGeneric")} className="size-6 text-muted-foreground hover:text-foreground" onClick={() => onOpenFile?.(file.path, { ...fileOpenContext, mode: renderedAvailable ? "rendered" : "file" })} size="icon-sm" type="button" variant="ghost"><FileCode2 aria-hidden="true" className="size-icon-sm" /></Button>
													</HeaderActionTooltip>
													{onOpenFile ? (
														<HeaderActionTooltip label={t("files.openDiffInCenter")}>
															<Button aria-label={t("files.openDiffInCenter")} className="size-6 text-muted-foreground hover:text-foreground" onClick={() => onOpenFile(file.path, { ...fileOpenContext, mode: "diff" })} size="icon-sm" type="button" variant="ghost"><VscodeGoToFileIcon aria-hidden="true" className="size-icon-sm" /></Button>
														</HeaderActionTooltip>
													) : null}
													<HeaderActionTooltip label={t("files.addFeedback")}>
														<Button aria-label={t("files.addFeedback")} aria-pressed={fileAnnotationActive} className={cn("size-6 text-muted-foreground hover:text-foreground", fileAnnotationActive && "bg-interactive-active text-foreground")} onClick={() => annotation.begin({ path: file.path, previousPath: file.previousPath, side: "file", scope, surface: "review", workspaceVersion: data.workspaceVersion, fileFingerprint: file.fileFingerprint })} size="icon-sm" type="button" variant="ghost"><MessageSquarePlus aria-hidden="true" className="size-icon-sm" /></Button>
													</HeaderActionTooltip>
													<HeaderActionTooltip label={isViewed ? t("files.markUnviewed", { file: file.path }) : t("files.markViewed", { file: file.path })}>
														{/* A 24px slot like the buttons beside it keeps the checkbox
														    centred on the header's trailing action column. */}
														<span className="grid size-6 place-items-center">
															<Checkbox
																aria-label={isViewed ? t("files.markUnviewed", { file: file.path }) : t("files.markViewed", { file: file.path })}
																checked={isViewed}
																className="size-4 border border-muted-foreground/70 bg-transparent"
																onCheckedChange={() => toggleViewed(file)}
																style={isViewed ? { backgroundColor: "#fff", borderColor: "#fff", color: "#000" } : undefined}
															/>
														</span>
													</HeaderActionTooltip>
												</div>
											</div>
										</PopoverAnchor>
									</div>
									{/* Whole-file feedback floats under the header's right edge, like the
									    browser's comment box: portaled above the list so no later file
									    header can cover it, and the same in split and unified views. It
									    stays open on outside clicks so a draft isn't lost; the close
									    button, Esc, or this file's feedback button close it. */}
									<PopoverContent
										align="end"
										aria-label={t("files.addFeedback")}
										className="w-[min(32rem,var(--radix-popover-trigger-width))] rounded-2xl border-0 bg-transparent p-0"
										hideWhenDetached
										onInteractOutside={(event) => event.preventDefault()}
										onOpenAutoFocus={(event) => event.preventDefault()}
										side="bottom"
										sideOffset={0}
									>
										<FileAnnotationComposer annotation={annotation} />
									</PopoverContent>
								</Popover>
							);
						}}
						style={{ height: "100%" }}
					/>
				) : null}
				{files.filter((file) => file.binary || !metadataByPath.has(file.path)).map((file) => {
					const deferred = isDeferredByDefault(file) && !loadedDeferredPaths.has(file.path);
					const serverDeferredReason = serverDeferredByPath.get(file.path);
					const pending = pendingDiffPaths.has(file.path);
					const unavailable = !file.binary && !deferred && !serverDeferredReason && !pending;
					return (
					<div className="m-2 flex items-center gap-2 rounded-md border border-border bg-surface p-3" key={file.path}>
						<FileCode2 aria-hidden="true" className="text-passive" />
						<div className="min-w-0 flex-1"><p className="truncate text-xs">{file.path}</p><p className="text-caption text-muted-foreground">{file.binary ? t("files.binaryUnavailable") : deferred ? t("files.deferredDiff") : serverDeferredReason ? t("files.diffUnavailableReason", { reason: serverDeferredReason }) : pending ? t("files.loadingDiff") : t("files.diffUnavailable")}</p></div>
						{deferred ? <Button onClick={() => setLoadedDeferredPaths((current) => new Set(current).add(file.path))} size="sm" type="button" variant="outline">{t("files.loadDiff")}</Button> : null}
						{unavailable ? <RetryButton onClick={retryAll} /> : null}
						<Button onClick={() => onOpenFile?.(file.path, { ...fileOpenContext, mode: "file" })} size="sm" type="button" variant="outline">{t("files.fileView")}</Button>
					</div>
					);
				})}
			</div>
				</>
			)}
		</div>
	);
}

function CommitBrowser({ commits, filter, onSelect, selectedSha }: { commits: readonly WorkspaceCommitSummary[]; filter: string; onSelect: (commit: WorkspaceCommitSummary) => void; selectedSha?: string }) {
	const { t } = useTranslation();
	const normalizedFilter = filter.trim().toLowerCase();
	const visibleCommits = normalizedFilter
		? commits.filter((commit) => `${commit.subject} ${commit.author} ${commit.sha} ${commit.files.map((file) => file.path).join(" ")}`.toLowerCase().includes(normalizedFilter))
		: commits;
	return (
		<ul className="board-scrollbar min-h-0 flex-1 overflow-y-auto overscroll-contain" aria-label={t("files.commitHistory")}>
			{visibleCommits.length === 0 ? <PanelMessage compact>{commits.length === 0 ? t("files.noCommits") : t("files.noFilterMatches")}</PanelMessage> : null}
			{visibleCommits.map((commit) => (
				<li key={commit.sha}>
				<button
					aria-current={selectedSha === commit.sha ? "true" : undefined}
					className={cn("group block w-full border-b border-border px-3 py-3 text-left transition-col hover:bg-interactive-hover", selectedSha === commit.sha && "bg-interactive-selected")}
					onClick={() => onSelect(commit)}
					type="button"
				>
					<div className="flex min-w-0 items-start gap-2">
						<GitCommitHorizontal aria-hidden="true" className="mt-0.5 size-icon-sm shrink-0 text-passive" />
						<div className="min-w-0 flex-1">
							<p className="line-clamp-2 text-xs font-medium text-foreground">{commit.subject}</p>
							<p className="mt-1 flex min-w-0 items-center gap-1.5 text-caption text-muted-foreground">
								<span className="truncate">{commit.author}</span>
								<span aria-hidden="true">·</span>
								<span className="shrink-0">{formatTimeTerse(commit.timestamp)}</span>
								<span aria-hidden="true">·</span>
								<span className="shrink-0">{commit.sha.slice(0, 7)}</span>
							</p>
						</div>
						<span className="shrink-0 text-caption text-passive">{t("files.count", { count: commit.files.length })}</span>
					</div>
					<div className="mt-2 space-y-1 pl-5">
						{commit.files.slice(0, 5).map((file) => (
							<div className="flex min-w-0 items-center gap-2 text-2xs text-muted-foreground" key={`${commit.sha}:${file.path}`}>
								<span className={cn("w-3 shrink-0 font-semibold", statusTone[file.status])}>{statusLabel[file.status]}</span>
								<span className="truncate">{file.path}</span>
							</div>
						))}
						{commit.files.length > 5 ? <p className="text-caption text-passive">{t("files.moreFiles", { count: commit.files.length - 5 })}</p> : null}
					</div>
				</button>
				</li>
			))}
		</ul>
	);
}

function HeaderActionTooltip({ children, label }: { children: ReactElement; label: string }) {
	return (
		<Tooltip>
			<TooltipTrigger asChild>{children}</TooltipTrigger>
			<TooltipContent side="bottom">{label}</TooltipContent>
		</Tooltip>
	);
}
