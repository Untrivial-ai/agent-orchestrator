import { useCallback, useEffect, useMemo, useRef, useState, type ReactElement } from "react";
import { useQueries } from "@tanstack/react-query";
import { parsePatchFiles, type CodeViewItem, type FileDiffMetadata } from "@pierre/diffs";
import { CodeView } from "@pierre/diffs/react";
import { ChevronDown, ChevronRight, ChevronsDownUp, ChevronsUpDown, FileCode2, MessageSquarePlus, PanelTopOpen, Pencil } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
	fetchWorkspaceFileRevision,
	sessionWorkspaceDiffsQueryOptions,
	type WorkspaceDiffScope,
	type WorkspaceFilesResponse,
	type WorkspaceFileSummary,
} from "../../hooks/useSessionWorkspaceFiles";
import { cn } from "../../lib/utils";
import { statusLabel, statusTone } from "../../lib/workspace-file-status";
import { useUiStore } from "../../stores/ui-store";
import { PanelMessage, RetryButton, FileAnnotationComposer, LineFeedbackButtonControl, type FileAnnotationModel } from "../WorkspaceDiffView";
import { Button } from "../ui/button";
import { Checkbox } from "../ui/checkbox";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { AO_PIERRE_SURFACE_CSS } from "./pierreTheme";
import { usePersistentGutterUtility } from "./usePersistentGutterUtility";

const PATCH_BATCH_SIZE = 100;
const parsedPatchCache = new Map<string, FileDiffMetadata[]>();
const MAX_PARSED_GROUPS = 24;
const sectionOrder: WorkspaceDiffScope[] = ["unstaged", "staged", "untracked", "committed"];

function chunked<T>(items: readonly T[], size: number): T[][] {
	const chunks: T[][] = [];
	for (let index = 0; index < items.length; index += size) chunks.push(items.slice(index, index + size));
	return chunks;
}

function patchCacheKey(workspaceVersion: string | undefined, scope: WorkspaceDiffScope, repository: string | undefined, patch: string) {
	return `${workspaceVersion ?? "legacy"}:${scope}:${repository ?? "root"}:${patch.length}:${patch.slice(0, 80)}:${patch.slice(-80)}`;
}

function parseGroupPatch(workspaceVersion: string | undefined, scope: WorkspaceDiffScope, repository: string | undefined, patch: string) {
	const key = patchCacheKey(workspaceVersion, scope, repository, patch);
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
	if (scope === "combined") return data.files.filter((file) => file.status !== "unmodified");
	return data.sections[scope];
}

function availableScopes(data: WorkspaceFilesResponse): WorkspaceDiffScope[] {
	const available = sectionOrder.filter((scope) => sectionFiles(data, scope).length > 0);
	return available.length > 0 ? available : ["combined"];
}

function isDeferredByDefault(file: WorkspaceFileSummary) {
	const name = file.path.split("/").pop()?.toLowerCase() ?? "";
	return file.size > 512 * 1024 || /^(package-lock\.json|pnpm-lock\.yaml|yarn\.lock|bun\.lockb?|go\.sum|cargo\.lock)$/.test(name);
}

function canOpenRendered(file: WorkspaceFileSummary) {
	return !file.binary && file.status !== "deleted" && /\.(md|markdown)$/i.test(file.path);
}

type ViewedRecord = Record<string, string>;

function useViewedFiles(sessionId: string, scope: WorkspaceDiffScope, files: readonly WorkspaceFileSummary[]) {
	const key = `ao.files.viewed.${sessionId}.${scope}`;
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
	onEditFile,
	onOpenFile,
	onOpenFileInCenter,
	sessionId,
	split,
}: {
	annotation: FileAnnotationModel;
	data: WorkspaceFilesResponse;
	filter: string;
	onBrowseAll: () => void;
	onEditFile?: (path: string) => void;
	onOpenFile?: (path: string, mode?: "diff" | "file" | "rendered") => void;
	onOpenFileInCenter?: (path: string) => void;
	sessionId: string;
	split: boolean;
}) {
	const { t } = useTranslation();
	const resolvedTheme = useUiStore((state) => state.resolvedTheme);
	const scopes = useMemo(() => availableScopes(data), [data]);
	const [scope, setScope] = useState<WorkspaceDiffScope>(() => scopes[0]);
	const [collapsedPaths, setCollapsedPaths] = useState<Set<string>>(() => new Set());
	const [loadedDeferredPaths, setLoadedDeferredPaths] = useState<Set<string>>(() => new Set());
	const [activeBatchCount, setActiveBatchCount] = useState(4);
	const reviewRef = useRef<HTMLDivElement>(null);
	const gutterHover = usePersistentGutterUtility(reviewRef);

	useEffect(() => {
		if (!scopes.includes(scope)) setScope(scopes[0]);
	}, [scope, scopes]);

	const allFiles = useMemo(() => sectionFiles(data, scope), [data, scope]);
	const normalizedFilter = filter.trim().toLowerCase();
	const files = useMemo(
		() => (normalizedFilter ? allFiles.filter((file) => `${file.path} ${file.previousPath ?? ""}`.toLowerCase().includes(normalizedFilter)) : allFiles),
		[allFiles, normalizedFilter],
	);
	const { viewed, toggle: toggleViewed } = useViewedFiles(sessionId, scope, allFiles);

	useEffect(() => {
		setCollapsedPaths(new Set(files.filter(isDeferredByDefault).map((file) => file.path)));
		setLoadedDeferredPaths(new Set());
		setActiveBatchCount(4);
	}, [scope, data.workspaceVersion]);

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
			}),
			enabled: paths.length > 0 && index < activeBatchCount,
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
					for (const metadata of parseGroupPatch(query.data?.workspaceVersion, scope, group.repository, group.patch)) {
						result.set(metadata.name, metadata);
					}
				} catch {
					// The group retains its retry/error surface below; one malformed patch
					// must not prevent other repositories from rendering.
				}
			}
		}
		return result;
	}, [patchQueries, scope]);
	const serverDeferredByPath = useMemo(() => {
		const result = new Map<string, string>();
		for (const query of patchQueries) {
			for (const group of query.data?.groups ?? []) {
				for (const deferred of group.deferred) result.set(deferred.path, deferred.reason);
			}
		}
		return result;
	}, [patchQueries]);

	const summaryById = useMemo(() => new Map(files.map((file) => [`${scope}:${file.path}`, file])), [files, scope]);
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
					id: `${scope}:${file.path}`,
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
		[annotation.target, collapsedPaths, files, metadataByPath, scope],
	);

	const loadDiffFiles = useCallback(
		async (metadata: FileDiffMetadata) => {
			// Pierre may hand this callback a normalized metadata object rather than
			// the exact object stored in our parse cache, so resolve by stable path.
			const file = files.find((candidate) => candidate.path === metadata.name);
			if (!file) throw new Error(t("files.error.loadFile"));
			const [before, after] = await Promise.all([
				fetchWorkspaceFileRevision({ sessionId, path: file.path, scope, side: "before", workspaceVersion: data.workspaceVersion }),
				fetchWorkspaceFileRevision({ sessionId, path: file.path, scope, side: "after", workspaceVersion: data.workspaceVersion }),
			]);
			if (before.binary || after.binary || before.truncated || after.truncated) throw new Error(t("files.error.loadFile"));
			const newFile = { name: file.path, contents: after.content, cacheKey: after.revision };
			if (metadata.type === "rename-pure") return { oldFile: null, newFile };
			return { oldFile: { name: file.previousPath || file.path, contents: before.content, cacheKey: before.revision }, newFile };
		},
		[data.workspaceVersion, files, scope, sessionId, t],
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
	const selectScope = useCallback((nextScope: WorkspaceDiffScope) => {
		if (nextScope !== scope && annotation.target?.surface === "review") annotation.cancel();
		setScope(nextScope);
	}, [annotation, scope]);

	const retryAll = () => patchQueries.forEach((query) => void query.refetch());
	const firstError = patchQueries.find((query) => query.error)?.error;
	const groupError = patchQueries.flatMap((query) => query.data?.groups ?? []).flatMap((group) => group.errors ?? [])[0];
	const loading = patchQueries.some((query) => query.isPending);
	const viewedCount = allFiles.filter((file) => viewed.has(file.path)).length;

	if (allFiles.length === 0) {
		return (
			<PanelMessage action={<Button onClick={onBrowseAll}>{t("files.browseAll")}</Button>}>
				{t("files.noneChanged")}
			</PanelMessage>
		);
	}

	return (
		<div
			className="flex h-full min-h-0 flex-col"
			onPointerLeave={gutterHover.onPointerLeave}
			onPointerMove={gutterHover.onPointerMove}
			ref={reviewRef}
		>
			<div className="flex shrink-0 flex-wrap items-center gap-1 border-b border-border bg-surface px-2 py-1.5">
				{scopes.map((entry) => (
					<Button key={entry} aria-pressed={scope === entry} onClick={() => selectScope(entry)} size="sm" type="button" variant={scope === entry ? "secondary" : "ghost"}>
						{entry === "combined" ? t("files.reviewChanges") : t(`files.section.${entry}`)}
						<span className="text-caption text-passive">{sectionFiles(data, entry).length}</span>
					</Button>
				))}
				<div className="ml-auto flex items-center gap-1 text-caption text-muted-foreground">
					<span>{t("files.reviewProgress", { total: allFiles.length, viewed: viewedCount })}</span>
					<HeaderActionTooltip label={t("files.collapseAll")}>
						<Button aria-label={t("files.collapseAll")} onClick={collapseAll} size="icon-sm" type="button" variant="ghost"><ChevronsDownUp aria-hidden="true" /></Button>
					</HeaderActionTooltip>
					<HeaderActionTooltip label={t("files.expandAll")}>
						<Button aria-label={t("files.expandAll")} onClick={() => {
							setLoadedDeferredPaths(new Set(files.filter(isDeferredByDefault).map((file) => file.path)));
							setCollapsedPaths(new Set());
						}} size="icon-sm" type="button" variant="ghost"><ChevronsUpDown aria-hidden="true" /></Button>
					</HeaderActionTooltip>
				</div>
			</div>
			{firstError ? <PanelMessage action={<RetryButton onClick={retryAll} />}>{firstError.message}</PanelMessage> : null}
			{groupError ? <PanelMessage action={<RetryButton onClick={retryAll} />}>{groupError.message}</PanelMessage> : null}
			{loading && items.length === 0 ? <PanelMessage compact>{t("files.loadingDiff")}</PanelMessage> : null}
			{files.length === 0 ? <PanelMessage compact>{t("files.noFilterMatches")}</PanelMessage> : null}
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
											{isCollapsed ? <ChevronRight aria-hidden="true" /> : <ChevronDown aria-hidden="true" />}
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
										{file.editable && file.fileFingerprint ? (
											<HeaderActionTooltip label={t("files.editFile")}>
												<Button aria-label={t("files.editFile")} onClick={(event) => { event.stopPropagation(); onEditFile?.(file.path); }} size="icon-sm" type="button" variant="ghost"><Pencil aria-hidden="true" /></Button>
											</HeaderActionTooltip>
										) : null}
										<HeaderActionTooltip label={t("files.addFeedback")}>
											<Button aria-label={t("files.addFeedback")} onClick={(event) => { event.stopPropagation(); annotation.begin({ path: file.path, previousPath: file.previousPath, side: "file", scope, surface: "review", workspaceVersion: data.workspaceVersion, fileFingerprint: file.fileFingerprint }); }} size="icon-sm" type="button" variant="ghost"><MessageSquarePlus aria-hidden="true" /></Button>
										</HeaderActionTooltip>
										<HeaderActionTooltip label={renderedAvailable ? t("files.openRichPreview") : t("files.openFullFileGeneric")}>
											<Button aria-label={renderedAvailable ? t("files.openRichPreview") : t("files.openFullFileGeneric")} onClick={() => onOpenFile?.(file.path, renderedAvailable ? "rendered" : "file")} size="icon-sm" type="button" variant="ghost"><FileCode2 aria-hidden="true" /></Button>
										</HeaderActionTooltip>
										{onOpenFileInCenter ? (
											<HeaderActionTooltip label={t("files.openDiffInCenter")}>
												<Button aria-label={t("files.openDiffInCenter")} onClick={() => onOpenFileInCenter(file.path)} size="icon-sm" type="button" variant="ghost"><PanelTopOpen aria-hidden="true" /></Button>
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
						<Button onClick={() => onOpenFile?.(file.path, "file")} size="sm" type="button" variant="outline">{t("files.fileView")}</Button>
					</div>
					);
				})}
			</div>
		</div>
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
