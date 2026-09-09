import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueries } from "@tanstack/react-query";
import { parsePatchFiles, type CodeViewItem, type CodeViewLineSelection, type FileDiffMetadata } from "@pierre/diffs";
import { CodeView } from "@pierre/diffs/react";
import { Check, ChevronDown, ChevronRight, ChevronsDownUp, ChevronsUpDown, Eye, FileCode2, MessageSquarePlus } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { DiffSelectionLine } from "../../../shared/diff-selection";
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
import { DiffSelectionMenu } from "../DiffSelectionMenu";
import { PanelMessage, RetryButton, FileAnnotationComposer, type FileAnnotationModel } from "../WorkspaceDiffView";
import { Button } from "../ui/button";

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
	onOpenFile,
	sessionId,
	split,
	wrap,
}: {
	annotation: FileAnnotationModel;
	data: WorkspaceFilesResponse;
	filter: string;
	onBrowseAll: () => void;
	onOpenFile?: (path: string) => void;
	sessionId: string;
	split: boolean;
	wrap: boolean;
}) {
	const { t } = useTranslation();
	const resolvedTheme = useUiStore((state) => state.resolvedTheme);
	const scopes = useMemo(() => availableScopes(data), [data]);
	const [scope, setScope] = useState<WorkspaceDiffScope>(() => scopes[0]);
	const [ignoreWhitespace, setIgnoreWhitespace] = useState(() => window.localStorage.getItem("ao.files.ignoreWhitespace") === "true");
	const [collapsedPaths, setCollapsedPaths] = useState<Set<string>>(() => new Set());
	const [loadedDeferredPaths, setLoadedDeferredPaths] = useState<Set<string>>(() => new Set());
	const [activeBatchCount, setActiveBatchCount] = useState(4);
	const [selection, setSelection] = useState<CodeViewLineSelection | null>(null);
	const [selectionLines, setSelectionLines] = useState<DiffSelectionLine[]>([]);
	const [menuOpen, setMenuOpen] = useState(false);
	const selectionGeneration = useRef(0);
	const rootRef = useRef<HTMLDivElement>(null);

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
	useEffect(() => {
		window.localStorage.setItem("ao.files.ignoreWhitespace", String(ignoreWhitespace));
	}, [ignoreWhitespace]);

	const requestedFiles = useMemo(
		() => files.filter((file) => !isDeferredByDefault(file) || loadedDeferredPaths.has(file.path)),
		[files, loadedDeferredPaths],
	);
	const batches = useMemo(() => chunked(requestedFiles.map((file) => file.path), PATCH_BATCH_SIZE), [requestedFiles]);
	const patchQueries = useQueries({
		queries: batches.map((paths, index) => ({
			...sessionWorkspaceDiffsQueryOptions({
				errorMessage: t("files.error.loadWorkspace"),
				ignoreWhitespace,
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
	const items = useMemo<CodeViewItem<undefined>[]>(
		() =>
			files.flatMap((file) => {
				if (file.binary) return [];
				const metadata = metadataByPath.get(file.path);
				if (!metadata) return [];
				return [{ id: `${scope}:${file.path}`, type: "diff", fileDiff: metadata, collapsed: collapsedPaths.has(file.path), version: 1 }];
			}),
		[collapsedPaths, files, metadataByPath, scope],
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

	const onSelectionChange = useCallback(
		(next: CodeViewLineSelection | null) => {
			setSelection(next);
			const generation = ++selectionGeneration.current;
			if (!next) {
				setMenuOpen(false);
				setSelectionLines([]);
				return;
			}
			const file = summaryById.get(next.id);
			if (!file) return;
			const side = next.range.side ?? "additions";
			void fetchWorkspaceFileRevision({ sessionId, path: file.path, scope, side: side === "deletions" ? "before" : "after", workspaceVersion: data.workspaceVersion })
				.then((revision) => {
					if (selectionGeneration.current !== generation || revision.binary || revision.truncated || !revision.exists) return;
					const start = Math.min(next.range.start, next.range.end);
					const end = Math.max(next.range.start, next.range.end);
					const contentLines = revision.content.replace(/\n$/, "").split("\n").slice(start - 1, end);
					setSelectionLines(contentLines.map((text, index) => ({
						kind: side === "deletions" ? "del" : "add",
						oldNo: side === "deletions" ? start + index : null,
						newNo: side === "additions" ? start + index : null,
						text,
					})));
					setMenuOpen(true);
				})
				.catch(() => setMenuOpen(false));
		},
		[data.workspaceVersion, scope, sessionId, summaryById],
	);

	const retryAll = () => patchQueries.forEach((query) => void query.refetch());
	const firstError = patchQueries.find((query) => query.error)?.error;
	const groupError = patchQueries.flatMap((query) => query.data?.groups ?? []).flatMap((group) => group.errors ?? [])[0];
	const loading = patchQueries.some((query) => query.isPending);
	const currentFile = selection ? summaryById.get(selection.id) : undefined;
	const viewedCount = allFiles.filter((file) => viewed.has(file.path)).length;
	const menuRect = rootRef.current?.getBoundingClientRect();

	if (allFiles.length === 0) {
		return (
			<PanelMessage action={<Button onClick={onBrowseAll}>{t("files.browseAll")}</Button>}>
				{t("files.noneChanged")}
			</PanelMessage>
		);
	}

	return (
		<div className="flex h-full min-h-0 flex-col" ref={rootRef}>
			<div className="flex shrink-0 flex-wrap items-center gap-1 border-b border-border bg-surface px-2 py-1.5">
				{scopes.map((entry) => (
					<Button key={entry} aria-pressed={scope === entry} onClick={() => setScope(entry)} size="sm" type="button" variant={scope === entry ? "secondary" : "ghost"}>
						{entry === "combined" ? t("files.reviewChanges") : t(`files.section.${entry}`)}
						<span className="text-caption text-passive">{sectionFiles(data, entry).length}</span>
					</Button>
				))}
				<div className="ml-auto flex items-center gap-1 text-caption text-muted-foreground">
					<span>{t("files.reviewProgress", { total: allFiles.length, viewed: viewedCount })}</span>
					<Button aria-label={t("files.ignoreWhitespace")} aria-pressed={ignoreWhitespace} onClick={() => setIgnoreWhitespace((value) => !value)} size="sm" type="button" variant={ignoreWhitespace ? "secondary" : "ghost"}>±</Button>
					<Button aria-label={t("files.collapseAll")} onClick={() => setCollapsedPaths(new Set(files.map((file) => file.path)))} size="icon-sm" type="button" variant="ghost"><ChevronsDownUp aria-hidden="true" /></Button>
					<Button aria-label={t("files.expandAll")} onClick={() => {
						setLoadedDeferredPaths(new Set(files.filter(isDeferredByDefault).map((file) => file.path)));
						setCollapsedPaths(new Set());
					}} size="icon-sm" type="button" variant="ghost"><ChevronsUpDown aria-hidden="true" /></Button>
				</div>
			</div>
			{annotation.target ? <FileAnnotationComposer annotation={annotation} /> : null}
			{firstError ? <PanelMessage action={<RetryButton onClick={retryAll} />}>{firstError.message}</PanelMessage> : null}
			{groupError ? <PanelMessage action={<RetryButton onClick={retryAll} />}>{groupError.message}</PanelMessage> : null}
			{loading && items.length === 0 ? <PanelMessage compact>{t("files.loadingDiff")}</PanelMessage> : null}
			{files.length === 0 ? <PanelMessage compact>{t("files.noFilterMatches")}</PanelMessage> : null}
			<div className="min-h-0 flex-1">
				{items.length > 0 ? (
					<CodeView
						className="h-full min-h-0"
						disableWorkerPool={typeof Worker === "undefined"}
						items={items}
						onSelectedLinesChange={onSelectionChange}
						options={{
							collapsedContextThreshold: 8,
							diffIndicators: "classic",
							diffStyle: split ? "split" : "unified",
							enableLineSelection: true,
							expansionLineCount: 20,
							hunkSeparators: "line-info",
							lineDiffType: "word-alt",
							loadDiffFiles,
							maxLineDiffLength: 400,
							overflow: wrap ? "wrap" : "scroll",
							stickyHeaders: true,
							theme: { dark: "github-dark", light: "github-light" },
							themeType: resolvedTheme,
							tokenizeMaxLength: 200_000,
							tokenizeMaxLineLength: 2_000,
						}}
						renderCustomHeader={(item) => {
							const file = summaryById.get(item.id);
							if (!file) return null;
							const isViewed = viewed.has(file.path);
							const isCollapsed = collapsedPaths.has(file.path);
							return (
								<div className="flex h-10 min-w-0 items-center gap-2 border-b border-border bg-surface px-2">
									<Button
										aria-label={isCollapsed ? t("files.expandFile", { file: file.path }) : t("files.collapseFile", { file: file.path })}
										onClick={() => setCollapsedPaths((current) => {
											const next = new Set(current);
											if (next.has(file.path)) next.delete(file.path);
											else next.add(file.path);
											return next;
										})}
										size="icon-sm"
										type="button"
										variant="ghost"
									>
										{isCollapsed ? <ChevronRight aria-hidden="true" /> : <ChevronDown aria-hidden="true" />}
									</Button>
									<span className={cn("font-mono text-xs font-semibold", statusTone[file.status])}>{statusLabel[file.status]}</span>
									<button className="min-w-0 flex-1 truncate text-left font-mono text-xs" onClick={() => onOpenFile?.(file.path)} type="button">{file.path}</button>
									<span className="text-caption text-success">+{file.additions}</span><span className="text-caption text-error">−{file.deletions}</span>
									<Button aria-label={t("files.addFileFeedback", { file: file.path })} onClick={() => annotation.begin({ path: file.path, previousPath: file.previousPath, side: "file", scope, workspaceVersion: data.workspaceVersion, fileFingerprint: file.fileFingerprint })} size="icon-sm" type="button" variant="ghost"><MessageSquarePlus aria-hidden="true" /></Button>
									<Button aria-label={t("files.openFullFile", { file: file.path })} onClick={() => onOpenFile?.(file.path)} size="icon-sm" type="button" variant="ghost"><FileCode2 aria-hidden="true" /></Button>
									<Button aria-label={isViewed ? t("files.markUnviewed", { file: file.path }) : t("files.markViewed", { file: file.path })} aria-pressed={isViewed} onClick={() => toggleViewed(file)} size="icon-sm" type="button" variant={isViewed ? "secondary" : "ghost"}>{isViewed ? <Check aria-hidden="true" /> : <Eye aria-hidden="true" />}</Button>
								</div>
							);
						}}
						selectedLines={selection}
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
						<Button onClick={() => onOpenFile?.(file.path)} size="sm" type="button" variant="outline">{t("files.fileView")}</Button>
					</div>
					);
				})}
			</div>
			{currentFile ? (
				<DiffSelectionMenu
					filePath={currentFile.path}
					fileFingerprint={currentFile.fileFingerprint}
					lines={selectionLines}
					onOpenChange={(open) => {
						setMenuOpen(open);
						if (!open) onSelectionChange(null);
					}}
					open={menuOpen && selectionLines.length > 0}
					position={{ x: Math.max((menuRect?.right ?? 280) - 250, 16), y: (menuRect?.top ?? 16) + 88 }}
					selectedText={selectionLines.map((line) => line.text).join("\n")}
					sessionId={sessionId}
					scope={scope}
					workspaceVersion={data.workspaceVersion}
				/>
			) : null}
		</div>
	);
}
