import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { preparePresortedFileTreeInput, type GitStatus, type GitStatusEntry } from "@pierre/trees";
import { FileTree as PierreFileTree, useFileTree } from "@pierre/trees/react";
import {
	buildWorkspaceFileTree,
	sessionWorkspaceTreeQueryOptions,
	type TreeNode,
	type WorkspaceTreeEntry,
} from "../hooks/useSessionWorkspaceTree";
import { sessionWorkspaceSearchQueryOptions } from "../hooks/useSessionWorkspaceFiles";
import { markFileViewerPerformance } from "../lib/file-viewer-performance";

const ROW_HEIGHT = 30;

function entryToNode(entry: WorkspaceTreeEntry): TreeNode {
	if (entry.type === "dir") {
		return { name: entry.name, path: entry.path, type: "dir", hasChanges: entry.hasChanges, children: [] };
	}
	return { name: entry.name, path: entry.path, type: "file", status: entry.status, binary: entry.binary };
}

// Replaces the children of the directory at `dir` (root = "") wherever it
// lives in the current lazy tree, leaving every other branch untouched.
function withChildrenAt(nodes: TreeNode[], dir: string, children: TreeNode[]): TreeNode[] {
	if (dir === "") return children;
	return nodes.map((node) => {
		if (node.type !== "dir") return node;
		if (node.path === dir) return { ...node, children };
		if (dir === node.path || dir.startsWith(`${node.path}/`)) {
			return { ...node, children: withChildrenAt(node.children ?? [], dir, children) };
		}
		return node;
	});
}

function mergeRootEntries(current: TreeNode[], entries: WorkspaceTreeEntry[]): TreeNode[] {
	const currentByPath = new Map(current.map((node) => [node.path, node]));
	return entries.map((entry) => {
		const next = entryToNode(entry);
		const previous = currentByPath.get(next.path);
		return next.type === "dir" && previous?.type === "dir"
			? { ...next, children: previous.children }
			: next;
	});
}

export function FileTree({
	filterText,
	sessionId,
	hostId,
	changedOnly,
	changedOnlyData,
	selectedPath,
	onSelectPath,
	flushTop = false,
}: {
	/** Start the first row at the top edge (the Files split view's divider). */
	flushTop?: boolean;
	filterText: string;
	sessionId: string;
	hostId?: string;
	changedOnly: boolean;
	changedOnlyData: TreeNode[];
	selectedPath: string | null;
	onSelectPath: (node: TreeNode) => void;
}) {
	if (changedOnly) {
		return (
			<ChangedFileTree
				data={changedOnlyData}
				filterText={filterText}
				flushTop={flushTop}
				onSelectPath={onSelectPath}
				selectedPath={selectedPath}
				sessionId={sessionId}
			/>
		);
	}

	return (
		<WorkspaceFileTree
			filterText={filterText}
			flushTop={flushTop}
			hostId={hostId}
			onSelectPath={onSelectPath}
			selectedPath={selectedPath}
			sessionId={sessionId}
		/>
	);
}

function WorkspaceFileTree({
	filterText,
	hostId,
	sessionId,
	selectedPath,
	onSelectPath,
	flushTop,
}: {
	filterText: string;
	hostId?: string;
	sessionId: string;
	selectedPath: string | null;
	onSelectPath: (node: TreeNode) => void;
	flushTop: boolean;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const loadedDirsRef = useRef<Set<string>>(new Set());
	const [lazyData, setLazyData] = useState<TreeNode[]>([]);
	const normalizedFilter = filterText.trim();

	const rootQuery = useQuery({
		...sessionWorkspaceTreeQueryOptions(sessionId, "", "Unable to load workspace tree", hostId),
		enabled: normalizedFilter.length === 0,
	});
	const searchQuery = useQuery({
		...sessionWorkspaceSearchQueryOptions(sessionId, normalizedFilter, t("files.error.searchWorkspace"), hostId),
		enabled: normalizedFilter.length > 0,
	});

	useEffect(() => {
		setLazyData([]);
		loadedDirsRef.current = new Set();
	}, [sessionId, hostId]);

	useEffect(() => {
		if (!rootQuery.data) return;
		loadedDirsRef.current.add("");
		setLazyData((current) => mergeRootEntries(current, rootQuery.data.entries));
	}, [rootQuery.data]);

	const loadChildren = useCallback(
		async (dir: string) => {
			if (loadedDirsRef.current.has(dir)) return;
			loadedDirsRef.current.add(dir);
			try {
				const result = await queryClient.fetchQuery(
					sessionWorkspaceTreeQueryOptions(sessionId, dir, t("files.error.loadWorkspaceTree"), hostId),
				);
				setLazyData((current) => withChildrenAt(current, dir, result.entries.map(entryToNode)));
			} catch {
				// Allow the next expand attempt to retry instead of leaving the
				// folder permanently stuck as "loaded but empty".
				loadedDirsRef.current.delete(dir);
			}
		},
		[queryClient, sessionId, hostId, t],
	);

	const searchData = buildWorkspaceFileTree(searchQuery.data?.results ?? []);
	const data = normalizedFilter ? searchData : lazyData;
	const isPending = normalizedFilter ? searchQuery.isPending : rootQuery.isPending;
	const activeError = normalizedFilter ? searchQuery.error : rootQuery.error;
	const isEmpty = data.length === 0 && !isPending && !activeError;

	return (
		<div className="flex h-full min-h-0 min-w-0 flex-col bg-background px-2">
			{isPending ? (
				<p className="p-3 text-xs text-muted-foreground">{t("files.loading")}</p>
			) : null}
			{activeError ? (
				<p className="p-3 text-xs text-error">{activeError.message || t("files.error.loadWorkspaceTree")}</p>
			) : null}
			{isEmpty ? <p className="p-3 text-xs text-muted-foreground">{t("files.explorer.empty")}</p> : null}
			{!isPending && !activeError && !isEmpty ? (
				<PierreTreeSurface
					data={data}
					expandAll={normalizedFilter.length > 0}
					flushTop={flushTop}
					id={`workspace-files-${sessionId}`}
					onDirectoryExpanded={normalizedFilter ? undefined : loadChildren}
					onSelectPath={onSelectPath}
					selectedPath={selectedPath}
				/>
			) : null}
		</div>
	);
}

function flattenChangedFiles(nodes: TreeNode[], files: TreeNode[] = []): TreeNode[] {
	for (const node of nodes) {
		if (node.type === "file") files.push(node);
		else flattenChangedFiles(node.children ?? [], files);
	}
	return files;
}

function toPierreGitStatus(status: TreeNode["status"]): GitStatus | null {
	switch (status) {
		case "added":
		case "deleted":
		case "modified":
		case "renamed":
			return status;
		default:
			return null;
	}
}

function flattenTreeNodes(nodes: TreeNode[], entries: TreeNode[] = []): TreeNode[] {
	for (const node of nodes) {
		entries.push(node);
		if (node.type === "dir") flattenTreeNodes(node.children ?? [], entries);
	}
	return entries;
}

function pierrePath(entry: TreeNode): string {
	return entry.type === "dir" ? `${entry.path}/` : entry.path;
}

function PierreTreeSurface({
	data,
	expandAll,
	flushTop,
	id,
	onDirectoryExpanded,
	onSelectPath,
	selectedPath,
}: {
	data: TreeNode[];
	expandAll: boolean;
	flushTop: boolean;
	id: string;
	onDirectoryExpanded?: (path: string) => void;
	onSelectPath: (node: TreeNode) => void;
	selectedPath: string | null;
}) {
	const { t } = useTranslation();
	const entries = useMemo(() => flattenTreeNodes(data), [data]);
	const entriesByPath = useMemo(() => new Map(entries.map((entry) => [entry.path, entry])), [entries]);
	const entriesByPathRef = useRef(entriesByPath);
	entriesByPathRef.current = entriesByPath;
	const onSelectPathRef = useRef(onSelectPath);
	onSelectPathRef.current = onSelectPath;
	const onDirectoryExpandedRef = useRef(onDirectoryExpanded);
	onDirectoryExpandedRef.current = onDirectoryExpanded;
	const paths = useMemo(() => entries.map(pierrePath), [entries]);
	const preparedInput = useMemo(() => preparePresortedFileTreeInput(paths), [paths]);
	const gitStatus = useMemo(
		() => entries.flatMap<GitStatusEntry>((entry) => {
			if (entry.type !== "file") return [];
			const status = toPierreGitStatus(entry.status);
			return status ? [{ path: entry.path, status }] : [];
		}),
		[entries],
	);
	const syncingSelection = useRef(false);
	const { model } = useFileTree({
		preparedInput,
		flattenEmptyDirectories: false,
		initialExpansion: expandAll ? "open" : "closed",
		initialSelectedPaths: selectedPath && entriesByPath.get(selectedPath)?.type === "file" ? [selectedPath] : [],
		itemHeight: ROW_HEIGHT,
		overscan: 8,
		gitStatus,
		onSelectionChange: (selectedPaths) => {
			if (syncingSelection.current) return;
			const path = selectedPaths.at(-1);
			const entry = path ? entriesByPathRef.current.get(path) : undefined;
			if (entry?.type === "file") onSelectPathRef.current(entry);
		},
	});

	useLayoutEffect(() => {
		const expandedPaths = expandAll
			? entries.filter((entry) => entry.type === "dir").map(pierrePath)
			: model.getVisibleRows(0, model.getVisibleCount())
				.filter((row) => row.kind === "directory" && row.isExpanded)
				.map((row) => row.path);
		model.resetPaths({ preparedInput, initialExpandedPaths: expandedPaths });
		model.setGitStatus(gitStatus);
	}, [entries, expandAll, gitStatus, model, preparedInput]);

	useEffect(() => {
		const loadExpandedDirectories = () => {
			const load = onDirectoryExpandedRef.current;
			if (!load) return;
			for (const entry of entriesByPathRef.current.values()) {
				const item = model.getItem(pierrePath(entry));
				if (entry.type === "dir" && item && "isExpanded" in item && item.isExpanded()) {
					load(entry.path);
				}
			}
		};
		loadExpandedDirectories();
		return model.subscribe(loadExpandedDirectories);
	}, [model]);

	useLayoutEffect(() => {
		if (!selectedPath || entriesByPath.get(selectedPath)?.type !== "file") return;
		if (model.getSelectedPaths().length === 1 && model.getSelectedPaths()[0] === selectedPath) return;
		syncingSelection.current = true;
		model.getItem(selectedPath)?.select();
		syncingSelection.current = false;
	}, [entriesByPath, model, selectedPath]);
	useLayoutEffect(() => markFileViewerPerformance("tree-painted"), [model, preparedInput]);

	return (
		<PierreFileTree
			aria-label={t("files.explorer.tree")}
			className="min-h-0 flex-1"
			data-testid="workspace-file-tree"
			id={id}
			model={model}
			style={{
				"--trees-bg-override": "transparent",
				"--trees-fg-override": "var(--foreground)",
				"--trees-selected-bg-override": "var(--interactive-active)",
				"--trees-padding-inline-override": "0px",
				"--trees-font-family-override": "inherit",
				"--trees-font-size-override": "var(--font-size-base)",
				paddingTop: flushTop ? 0 : 4,
			} as React.CSSProperties}
		/>
	);
}

/**
 * The changed-files view is a warm, path-first model. Pierre Trees owns the
 * canonical path index and virtualized rows; manifest refreshes update that
 * stable model instead of rebuilding a React node for every visible file.
 */
function ChangedFileTree({
	data,
	filterText,
	flushTop,
	onSelectPath,
	selectedPath,
	sessionId,
}: {
	data: TreeNode[];
	filterText: string;
	flushTop: boolean;
	onSelectPath: (node: TreeNode) => void;
	selectedPath: string | null;
	sessionId: string;
}) {
	const { t } = useTranslation();
	const onSelectPathRef = useRef(onSelectPath);
	onSelectPathRef.current = onSelectPath;

	const files = useMemo(() => {
		const normalizedFilter = filterText.trim().toLocaleLowerCase();
		const allFiles = flattenChangedFiles(data);
		return normalizedFilter
			? allFiles.filter((file) => file.path.toLocaleLowerCase().includes(normalizedFilter))
			: allFiles;
	}, [data, filterText]);
	const filesByPath = useMemo(() => new Map(files.map((file) => [file.path, file])), [files]);
	const filesByPathRef = useRef(filesByPath);
	filesByPathRef.current = filesByPath;
	const paths = useMemo(() => files.map((file) => file.path), [files]);
	const preparedInput = useMemo(() => preparePresortedFileTreeInput(paths), [paths]);
	const gitStatus = useMemo(
		() => files.flatMap<GitStatusEntry>((file) => {
			const status = toPierreGitStatus(file.status);
			return status ? [{ path: file.path, status }] : [];
		}),
		[files],
	);
	const syncingSelection = useRef(false);
	const { model } = useFileTree({
		preparedInput,
		flattenEmptyDirectories: false,
		initialExpansion: "closed",
		initialSelectedPaths: selectedPath && filesByPath.has(selectedPath) ? [selectedPath] : [],
		itemHeight: ROW_HEIGHT,
		overscan: 8,
		gitStatus,
		onSelectionChange: (selectedPaths) => {
			if (syncingSelection.current) return;
			const path = selectedPaths.at(-1);
			const file = path ? filesByPathRef.current.get(path) : undefined;
			if (file) onSelectPathRef.current(file);
		},
	});

	useLayoutEffect(() => {
		const expandedPaths = model
			.getVisibleRows(0, model.getVisibleCount())
			.filter((row) => row.kind === "directory" && row.isExpanded)
			.map((row) => row.path);
		model.resetPaths({ preparedInput, initialExpandedPaths: expandedPaths });
		model.setGitStatus(gitStatus);
	}, [gitStatus, model, preparedInput]);

	useLayoutEffect(() => {
		if (!selectedPath || !filesByPath.has(selectedPath)) return;
		if (model.getSelectedPaths().length === 1 && model.getSelectedPaths()[0] === selectedPath) return;
		syncingSelection.current = true;
		model.getItem(selectedPath)?.select();
		syncingSelection.current = false;
	}, [filesByPath, model, selectedPath]);
	useLayoutEffect(() => markFileViewerPerformance("tree-painted"), [model, preparedInput]);

	if (files.length === 0) {
		return (
			<div className="flex h-full min-h-0 min-w-0 flex-col bg-background px-2">
				<p className="p-3 text-xs text-muted-foreground">{t("files.explorer.empty")}</p>
			</div>
		);
	}

	return (
		<div className="flex h-full min-h-0 min-w-0 flex-col bg-background px-2">
			<PierreFileTree
				aria-label={t("files.explorer.tree")}
				className="min-h-0 flex-1"
				data-testid="changed-file-tree"
				id={`changed-files-${sessionId}`}
				model={model}
				style={{
					"--trees-bg-override": "transparent",
					"--trees-fg-override": "var(--foreground)",
					"--trees-selected-bg-override": "var(--interactive-active)",
					"--trees-padding-inline-override": "0px",
					"--trees-font-family-override": "inherit",
					"--trees-font-size-override": "var(--font-size-base)",
					paddingTop: flushTop ? 0 : 4,
				} as React.CSSProperties}
			/>
		</div>
	);
}
