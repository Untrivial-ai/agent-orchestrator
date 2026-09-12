import { useCallback, useEffect, useMemo, useRef, useState, type ReactElement } from "react";
import { useQueries } from "@tanstack/react-query";
import { parsePatchFiles, type CodeViewItem, type FileDiffMetadata } from "@pierre/diffs";
import { CodeView } from "@pierre/diffs/react";
import { ChevronDown, ChevronRight, ChevronsDownUp, ChevronsUpDown, FileCode2, GitCommitHorizontal, MessageSquarePlus, Pencil } from "lucide-react";
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
import { Button } from "../ui/button";
import { Checkbox } from "../ui/checkbox";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { formatTimeTerse } from "../../lib/format-time";
import { AO_PIERRE_SURFACE_CSS } from "./pierreTheme";
import { usePersistentGutterUtility } from "./usePersistentGutterUtility";

const PATCH_BATCH_SIZE = 100;
const parsedPatchCache = new Map<string, FileDiffMetadata[]>();
const MAX_PARSED_GROUPS = 24;
const workingScopeOrder = ["unstaged", "staged"] as const;

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
	sessionId,
	split,
}: {
	annotation: FileAnnotationModel;
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

	const metadataByPath = useMemo(() => {
		const result = new Map<string, FileDiffMetadata>();
		for (const query of patchQueries) {
			for (const group of query.data?.groups ?? []) {
				try {
					for (const metadata of parseGroupPatch(query.data?.workspaceVersion, scope, selectedCommit?.sha, group.repository, group.patch)) {
						result.set(metadata.name, metadata);
					}
				} catch {
					// The group retains its retry/error surface below; one malformed patch
					// must not prevent other repositories from rendering.
				}
			}
		}
		return result;
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

	const summaryById = useMemo(() => new Map(files.map((file) => [`${reviewSelectionKey}:${file.path}`, file])), [files, reviewSelectionKey]);
	const items = useMemo<CodeViewItem<"feedback">[]>(
		() =>
			files.flatMap((file) => {
				if (file.binary) return [];
				const metadata = metadataByPath.get(file.path);
				if (!metadata) return [];
				const collapsed = collapsedPaths.has(file.path);
				const fileAnnotationActive = annotation.target?.surface !== "focused" && annotation.target?.path === file.path && annotation.target.side === "file";
				const activeTarget = annotation.target?.surface !== "focused" && annotation.target?.path === file.path && annotation.target.side !== "file"
					? annotation.target
					: null;
				return [{
					id: `${reviewSelectionKey}:${file.path}`,
					type: "diff",
					fileDiff: metadata,
					collapsed,
					annotations: activeTarget?.line != null ? [{
						lineNumber: activeTarget.line,
						side: activeTarget.side === "old" ? "deletions" : "additions",
						metadata: "feedback",
					}] : undefined,
					version: (collapsed ? 1 : 0) + (activeTarget ? 2 : 0) + (fileAnnotationActive ? 4 : 0),
				}];
			}),
		[annotation.target, collapsedPaths, files, metadataByPath, reviewSelectionKey],
	);

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

	return (
		<div
			className="flex h-full min-h-0 flex-col"
			onPointerLeave={gutterHover.onPointerLeave}
			onPointerMove={gutterHover.onPointerMove}
			ref={reviewRef}
		>
			<div className="flex shrink-0 flex-wrap items-center gap-1 border-b border-border bg-surface px-2 py-1.5">
				{showReviewScopeSwitcher ? workingSourceOptions.map((entry) => (
					<Button
						aria-pressed={scope === entry}
						disabled={!entry}
						key={entry}
						onClick={() => selectScope(entry)}
						size="sm"
						type="button"
						variant={scope === entry ? "secondary" : "ghost"}
					>
						{workingSourceLabel(entry)}
						<span className="text-caption text-passive">{sectionFiles(data, entry).length}</span>
					</Button>
				)) : null}
				<Button aria-expanded={commitBrowserOpen} aria-pressed={scope === "committed"} className="gap-1.5" disabled={data.commits.length === 0} onClick={() => setCommitBrowserOpen((open) => !open)} size="sm" type="button" variant={scope === "committed" ? "secondary" : "ghost"}>
					<GitCommitHorizontal aria-hidden="true" className="size-icon-sm" />
					<span>{t("files.commits")}</span>
					{commitHashForButton ? <span className="text-caption text-passive">{commitHashForButton}</span> : null}
				</Button>
				{!commitBrowserOpen ? <div className="ml-auto flex items-center gap-1 text-caption text-muted-foreground">
					<span>{t("files.reviewProgress", { total: allFiles.length, viewed: viewedCount })}</span>
					<HeaderActionTooltip label={t(allFilesCollapsed ? "files.expandAll" : "files.collapseAll")}>
						<Button aria-label={t(allFilesCollapsed ? "files.expandAll" : "files.collapseAll")} onClick={toggleAll} size="icon-sm" type="button" variant="ghost">
							{allFilesCollapsed ? <ChevronsUpDown aria-hidden="true" /> : <ChevronsDownUp aria-hidden="true" />}
						</Button>
					</HeaderActionTooltip>
				</div> : <span className="ml-auto text-caption text-muted-foreground">{t("files.selectCommit")}</span>}
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
			{loading && items.length === 0 ? <PanelMessage compact>{t("files.loadingDiff")}</PanelMessage> : null}
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
							unsafeCSS: AO_PIERRE_SURFACE_CSS,
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
								<div className="relative bg-surface">
									<div className="flex h-10 min-w-0 items-center gap-2 border-b border-border px-2">
										<Button
											aria-label={isCollapsed ? t("files.expandFile", { file: file.path }) : t("files.collapseFile", { file: file.path })}
											onClick={() => toggleCollapsed(file.path)}
											size="icon-sm"
											type="button"
											variant="ghost"
										>
											{isCollapsed ? <ChevronRight aria-hidden="true" className="size-icon-sm" /> : <ChevronDown aria-hidden="true" className="size-icon-sm" />}
										</Button>
										<span className={cn("font-mono text-xs font-semibold", statusTone[file.status])}>{statusLabel[file.status]}</span>
										<button
											aria-label={isCollapsed ? t("files.expandFile", { file: file.path }) : t("files.collapseFile", { file: file.path })}
											className="min-w-0 flex-1 truncate text-left font-mono text-xs hover:underline"
											onClick={() => toggleCollapsed(file.path)}
											title={file.path}
											type="button"
										>
											{file.path}
										</button>
										<span className="text-caption text-success">+{file.additions}</span>
										<span className="text-caption text-error">−{file.deletions}</span>
										<div className="flex shrink-0 items-center">
											{file.editable && file.fileFingerprint ? (
												<HeaderActionTooltip label={t("files.editFile")}>
											<Button aria-label={t("files.editFile")} className="size-6" onClick={(event) => { event.stopPropagation(); onOpenFile?.(file.path, { editing: true, mode: "file", scope }); }} size="icon-sm" type="button" variant="ghost"><Pencil aria-hidden="true" className="size-icon-sm" /></Button>
												</HeaderActionTooltip>
											) : null}
											<HeaderActionTooltip label={t("files.addFeedback")}>
												<Button aria-label={t("files.addFeedback")} className="size-6" onClick={(event) => { event.stopPropagation(); annotation.begin({ path: file.path, previousPath: file.previousPath, side: "file", scope, surface: "review", workspaceVersion: data.workspaceVersion, fileFingerprint: file.fileFingerprint }); }} size="icon-sm" type="button" variant="ghost"><MessageSquarePlus aria-hidden="true" className="size-icon-sm" /></Button>
											</HeaderActionTooltip>
											<HeaderActionTooltip label={renderedAvailable ? t("files.openRichPreview") : t("files.openFullFileGeneric")}>
										<Button aria-label={renderedAvailable ? t("files.openRichPreview") : t("files.openFullFileGeneric")} className="size-6" onClick={() => onOpenFile?.(file.path, { ...fileOpenContext, mode: renderedAvailable ? "rendered" : "file" })} size="icon-sm" type="button" variant="ghost"><FileCode2 aria-hidden="true" className="size-icon-sm" /></Button>
											</HeaderActionTooltip>
											{onOpenFile ? (
												<HeaderActionTooltip label={t("files.openDiffInCenter")}>
											<Button aria-label={t("files.openDiffInCenter")} className="size-6" onClick={() => onOpenFile(file.path, { ...fileOpenContext, mode: "diff" })} size="icon-sm" type="button" variant="ghost"><VscodeGoToFileIcon aria-hidden="true" className="size-icon-sm" /></Button>
												</HeaderActionTooltip>
											) : null}
											<HeaderActionTooltip label={isViewed ? t("files.markUnviewed", { file: file.path }) : t("files.markViewed", { file: file.path })}>
												<Checkbox
													aria-label={isViewed ? t("files.markUnviewed", { file: file.path }) : t("files.markViewed", { file: file.path })}
													checked={isViewed}
													className="size-4 border border-muted-foreground/70 bg-transparent"
													onCheckedChange={() => toggleViewed(file)}
													style={isViewed ? { backgroundColor: "#fff", borderColor: "#fff", color: "#000" } : undefined}
												/>
											</HeaderActionTooltip>
										</div>
									</div>
									{fileAnnotationActive ? <div className="absolute right-2 top-full z-50 w-[min(32rem,calc(100%-1rem))] overflow-hidden rounded-md border border-border bg-surface shadow-xl"><FileAnnotationComposer annotation={annotation} /></div> : null}
								</div>
							);
						}}
						style={{ height: "100%" }}
					/>
				) : null}
				{files.filter((file) => file.binary || !metadataByPath.has(file.path)).map((file) => {
					const deferred = isDeferredByDefault(file) && !loadedDeferredPaths.has(file.path);
					const serverDeferredReason = serverDeferredByPath.get(file.path);
					return (
					<div className="m-2 flex items-center gap-2 rounded-md border border-border bg-surface p-3" key={file.path}>
						<FileCode2 aria-hidden="true" className="text-passive" />
						<div className="min-w-0 flex-1"><p className="truncate font-mono text-xs">{file.path}</p><p className="text-caption text-muted-foreground">{file.binary ? t("files.binaryUnavailable") : deferred ? t("files.deferredDiff") : serverDeferredReason ? t("files.diffUnavailableReason", { reason: serverDeferredReason }) : t("files.loadingDiff")}</p></div>
						{deferred ? <Button onClick={() => setLoadedDeferredPaths((current) => new Set(current).add(file.path))} size="sm" type="button" variant="outline">{t("files.loadDiff")}</Button> : null}
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
								<span className="shrink-0 font-mono">{commit.sha.slice(0, 7)}</span>
							</p>
						</div>
						<span className="shrink-0 text-caption text-passive">{t("files.count", { count: commit.files.length })}</span>
					</div>
					<div className="mt-2 space-y-1 pl-5">
						{commit.files.slice(0, 5).map((file) => (
							<div className="flex min-w-0 items-center gap-2 font-mono text-2xs text-muted-foreground" key={`${commit.sha}:${file.path}`}>
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
