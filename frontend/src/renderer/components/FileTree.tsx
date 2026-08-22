import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Tree, type NodeApi, type NodeRendererProps, type TreeApi } from "react-arborist";
import { File, Folder, FolderOpen } from "lucide-react";
import { cn } from "../lib/utils";
import { statusLabel, statusTone } from "../lib/workspace-file-status";
import {
	sessionWorkspaceTreeQueryOptions,
	type TreeNode,
	type WorkspaceTreeEntry,
} from "../hooks/useSessionWorkspaceTree";

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

function useContainerSize(): [RefObject<HTMLDivElement | null>, { width: number; height: number }] {
	const ref = useRef<HTMLDivElement>(null);
	const [size, setSize] = useState({ width: 0, height: 0 });
	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		const observer = new ResizeObserver(([entry]) => {
			if (!entry) return;
			const { width, height } = entry.contentRect;
			setSize({ width, height });
		});
		observer.observe(el);
		return () => observer.disconnect();
	}, []);
	return [ref, size];
}

export function FileTree({
	filterText,
	sessionId,
	changedOnly,
	changedOnlyData,
	selectedPath,
	onSelectPath,
}: {
	filterText: string;
	sessionId: string;
	changedOnly: boolean;
	changedOnlyData: TreeNode[];
	selectedPath: string | null;
	onSelectPath: (node: TreeNode) => void;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const treeApiRef = useRef<TreeApi<TreeNode> | null>(null);
	const loadedDirsRef = useRef<Set<string>>(new Set());
	const [lazyData, setLazyData] = useState<TreeNode[]>([]);
	const [containerRef, size] = useContainerSize();

	const rootQuery = useQuery({ ...sessionWorkspaceTreeQueryOptions(sessionId, ""), enabled: !changedOnly });

	useEffect(() => {
		setLazyData([]);
		loadedDirsRef.current = new Set();
	}, [sessionId]);

	useEffect(() => {
		if (changedOnly || !rootQuery.data) return;
		loadedDirsRef.current.add("");
		setLazyData(rootQuery.data.entries.map(entryToNode));
	}, [changedOnly, rootQuery.data]);

	const loadChildren = useCallback(
		async (dir: string) => {
			if (loadedDirsRef.current.has(dir)) return;
			loadedDirsRef.current.add(dir);
			try {
				const result = await queryClient.fetchQuery(
					sessionWorkspaceTreeQueryOptions(sessionId, dir, t("files.error.loadWorkspaceTree")),
				);
				setLazyData((current) => withChildrenAt(current, dir, result.entries.map(entryToNode)));
			} catch {
				// Allow the next expand attempt to retry instead of leaving the
				// folder permanently stuck as "loaded but empty".
				loadedDirsRef.current.delete(dir);
			}
		},
		[queryClient, sessionId, t],
	);

	const handleToggle = useCallback(
		(id: string) => {
			if (changedOnly) return;
			const node = treeApiRef.current?.get(id);
			if (node?.isOpen && node.data.type === "dir") void loadChildren(node.data.path);
		},
		[changedOnly, loadChildren],
	);

	const handleActivate = useCallback(
		(node: NodeApi<TreeNode>) => {
			if (node.data.type === "file") onSelectPath(node.data);
		},
		[onSelectPath],
	);

	const data = changedOnly ? changedOnlyData : lazyData;
	const isEmpty = data.length === 0 && (changedOnly || (rootQuery.isFetched && !rootQuery.isError));

	return (
		<div className="flex h-full min-h-0 flex-col bg-background" ref={containerRef}>
			{rootQuery.isPending && !changedOnly ? (
				<p className="p-3 text-xs text-muted-foreground">{t("files.loading")}</p>
			) : null}
			{rootQuery.isError && !changedOnly ? (
				<p className="p-3 text-xs text-error">{rootQuery.error.message || t("files.error.loadWorkspaceTree")}</p>
			) : null}
			{isEmpty ? <p className="p-3 text-xs text-muted-foreground">{t("files.explorer.empty")}</p> : null}
			{size.width > 0 && size.height > 0 ? (
				<Tree<TreeNode>
					data={data}
					ref={treeApiRef}
					idAccessor="path"
					onToggle={handleToggle}
					onActivate={handleActivate}
					openByDefault={false}
					selection={selectedPath ?? undefined}
					disableDrag
					disableDrop
					disableEdit
					disableMultiSelection
					searchTerm={filterText}
					rowHeight={26}
					indent={14}
					width={size.width}
					height={size.height}
					aria-label={t("files.explorer.tree")}
				>
					{FileTreeRow}
				</Tree>
			) : null}
		</div>
	);
}

function FileTreeRow({ node, style, dragHandle }: NodeRendererProps<TreeNode>) {
	const { t } = useTranslation();
	const entry = node.data;
	const isDir = entry.type === "dir";
	return (
		<div
			className={cn(
				"flex cursor-pointer items-center gap-1.5 truncate rounded-sm px-1.5 text-xs",
				node.isSelected ? "bg-interactive-active text-foreground" : "hover:bg-interactive-hover/60",
			)}
			onClick={() => (isDir ? node.toggle() : node.activate())}
			ref={dragHandle}
			style={style}
		>
			{isDir ? (
				node.isOpen ? (
					<FolderOpen className="size-icon-sm shrink-0 text-passive" aria-hidden="true" />
				) : (
					<Folder className="size-icon-sm shrink-0 text-passive" aria-hidden="true" />
				)
			) : (
				<File className="size-icon-sm shrink-0 text-passive" aria-hidden="true" />
			)}
			<span className="min-w-0 flex-1 truncate font-mono">{entry.name}</span>
			{isDir && entry.hasChanges ? (
				<span aria-hidden="true" className="size-1.5 shrink-0 rounded-full bg-warning" />
			) : null}
			{!isDir && entry.status && entry.status !== "unmodified" ? (
				<span
					className={cn("shrink-0 font-mono text-caption font-medium", statusTone[entry.status])}
					title={t(`files.status.${entry.status}`)}
				>
					{statusLabel[entry.status]}
				</span>
			) : null}
		</div>
	);
}
