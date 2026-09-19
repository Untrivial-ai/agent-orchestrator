import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
	Columns2,
	Maximize2,
	Minimize2,
	Rows3,
	Search,
} from "lucide-react";
import {
	sessionWorkspaceFilesQueryOptions,
	useWorkspaceFileConnectionState,
	workspaceFilesRefetchInterval,
} from "../hooks/useSessionWorkspaceFiles";
import { subscribeWorkspaceFileChanges } from "../lib/workspace-file-events";
import { buildChangedOnlyTree, type TreeNode } from "../hooks/useSessionWorkspaceTree";
import { useFileAnnotation } from "../hooks/useFileAnnotation";
import { useUiStore } from "../stores/ui-store";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "./ui/resizable";
import { FileTree } from "./FileTree";
import { FileContentPane, type FileOpenOptions } from "./FileContentPane";
import { PanelMessage, RetryButton } from "./WorkspaceDiffView";
import { WorkspaceReviewPane } from "./diffs/WorkspaceReviewPane";

type SessionFileExplorerProps = {
	sessionId: string;
	isMaximized?: boolean;
	onOpenFile?: (path: string, options?: FileOpenOptions) => void;
	onSplitChange?: (split: boolean) => void;
	onToggleMaximized?: (next: boolean) => void;
	revealRequest?: { path: string; key: number } | null;
	split?: boolean;
};

export function SessionFileExplorer({
	sessionId,
	isMaximized = false,
	onOpenFile,
	onSplitChange,
	onToggleMaximized,
	revealRequest,
	split: controlledSplit,
}: SessionFileExplorerProps) {
	const { t } = useTranslation();
	const [filter, setFilter] = useState("");
	const [internalSplit, setInternalSplit] = useState(() => window.localStorage.getItem("ao.files.diffStyle") === "split");
	const split = controlledSplit ?? internalSplit;
	const [selectedPath, setSelectedPath] = useState<string | null>(null);
	const annotation = useFileAnnotation(sessionId);
	const queryClient = useQueryClient();
	const connectionState = useWorkspaceFileConnectionState(sessionId);

	const changedOnly = useUiStore((state) => state.inspectorSessions[sessionId]?.filesChangedOnly ?? true);
	const setFilesChangedOnly = useUiStore((state) => state.setFilesChangedOnly);

	const filesQuery = useQuery({
		...sessionWorkspaceFilesQueryOptions(sessionId, t("files.error.loadWorkspace")),
		refetchInterval: workspaceFilesRefetchInterval(connectionState),
	});
	const changedOnlyData = useMemo(
		() => (filesQuery.data ? buildChangedOnlyTree(filesQuery.data.files) : []),
		[filesQuery.data],
	);
	const hasChanges = filesQuery.data?.files.some((file) => file.status !== "unmodified") ?? false;
	const showChanges = changedOnly && (!filesQuery.data || hasChanges);

	useEffect(() => {
		setSelectedPath(null);
		setFilter("");
	}, [sessionId]);

	useEffect(() => subscribeWorkspaceFileChanges(sessionId, queryClient), [queryClient, sessionId]);
	useEffect(() => {
		window.localStorage.setItem("ao.files.diffStyle", split ? "split" : "unified");
	}, [split]);
	useEffect(() => {
		if (!revealRequest) return;
		setFilesChangedOnly(sessionId, false);
		setSelectedPath(revealRequest.path);
		if (!isMaximized) onOpenFile?.(revealRequest.path, { mode: "file" });
	}, [isMaximized, onOpenFile, revealRequest, sessionId, setFilesChangedOnly]);

	const handleSelectPath = (node: TreeNode) => {
		setSelectedPath(node.path);
		if (!isMaximized) onOpenFile?.(node.path, { mode: "file" });
	};
	const handleViewChange = (next: boolean) => {
		setSelectedPath(null);
		setFilesChangedOnly(sessionId, next);
	};
	const treeSelectedPath = selectedPath;

	return (
		<section className="flex h-full min-h-0 flex-col bg-background text-foreground" aria-label={t("files.sessionFiles")}>
			<header className="flex h-10 shrink-0 items-center gap-0.5 border-b border-border bg-surface px-2">
				<label className="relative mr-1 min-w-0 flex-1">
					<Search className="pointer-events-none absolute left-2.5 top-1/2 size-icon-sm -translate-y-1/2 text-passive" />
					<Input
						aria-label={t("files.explorer.filter")}
						className="h-8 pl-8 font-mono text-xs"
						onChange={(event) => setFilter(event.target.value)}
						placeholder={t("files.explorer.filterPlaceholder")}
						value={filter}
					/>
				</label>
				{hasChanges ? (
					<div
						aria-label={t("files.viewMode")}
						className="flex shrink-0 items-center rounded-md border border-border bg-muted/30 p-0.5"
						role="tablist"
					>
						<Button
							aria-selected={showChanges}
							className="h-6 rounded px-2 text-2xs"
							onClick={() => handleViewChange(true)}
							role="tab"
							size="sm"
							type="button"
							variant={showChanges ? "secondary" : "ghost"}
						>
							{t("files.reviewChanges")}
						</Button>
						<Button
							aria-selected={!showChanges}
							className="h-6 rounded px-2 text-2xs"
							onClick={() => handleViewChange(false)}
							role="tab"
							size="sm"
							type="button"
							variant={!showChanges ? "secondary" : "ghost"}
						>
							{t("files.allFiles")}
						</Button>
					</div>
				) : null}
				{showChanges ? (
					<Tooltip>
						<TooltipTrigger asChild>
							<Button
								aria-label={split ? t("files.unifiedDiff") : t("files.splitDiff")}
								aria-pressed={split}
								className="shrink-0"
								onClick={() => {
									const next = !split;
									if (controlledSplit === undefined) setInternalSplit(next);
									onSplitChange?.(next);
								}}
								size="icon-sm"
								type="button"
								variant="ghost"
							>
								{split ? (
									<Columns2 className="size-icon-sm" aria-hidden="true" />
								) : (
									<Rows3 className="size-icon-sm" aria-hidden="true" />
								)}
							</Button>
						</TooltipTrigger>
						<TooltipContent side="bottom">{split ? t("files.unifiedDiff") : t("files.splitDiff")}</TooltipContent>
					</Tooltip>
				) : null}
				{onToggleMaximized ? (
					<Tooltip>
						<TooltipTrigger asChild>
							<Button
								aria-label={isMaximized ? t("files.minimize") : t("files.maximize")}
								className="shrink-0"
								onClick={() => onToggleMaximized(!isMaximized)}
								size="icon-sm"
								type="button"
								variant="ghost"
							>
								{isMaximized ? (
									<Minimize2 className="size-icon-sm" aria-hidden="true" />
								) : (
									<Maximize2 className="size-icon-sm" aria-hidden="true" />
								)}
							</Button>
						</TooltipTrigger>
						<TooltipContent side="bottom">{isMaximized ? t("files.minimize") : t("files.maximize")}</TooltipContent>
					</Tooltip>
				) : null}
			</header>
			{showChanges ? (
				filesQuery.isPending ? (
					<PanelMessage>{t("files.loading")}</PanelMessage>
				) : filesQuery.isError ? (
					<PanelMessage action={<RetryButton onClick={() => void filesQuery.refetch()} />}>
						{filesQuery.error.message || t("files.error.loadWorkspace")}
					</PanelMessage>
				) : filesQuery.data ? (
					<WorkspaceReviewPane
						annotation={annotation}
						data={filesQuery.data}
						filter={filter}
						onBrowseAll={() => handleViewChange(false)}
						onOpenFile={onOpenFile}
						sessionId={sessionId}
						split={split}
					/>
				) : null
			) : isMaximized ? (
				// Maximized gives the explorer the full window — plenty of room for
				// the tree and the content side by side, like a real editor.
				<ResizablePanelGroup className="min-h-0 flex-1">
					<ResizablePanel defaultSize="26%" minSize="18%" maxSize="50%">
						<FileTree
							changedOnly={false}
							changedOnlyData={changedOnlyData}
							filterText={filter}
							onSelectPath={handleSelectPath}
							selectedPath={treeSelectedPath}
							sessionId={sessionId}
						/>
					</ResizablePanel>
					<ResizableHandle />
					<ResizablePanel defaultSize="74%" minSize="40%">
						<ContentScrollArea>
							<FileContentPane annotation={annotation} path={selectedPath} sessionId={sessionId} split={split} />
						</ContentScrollArea>
					</ResizablePanel>
				</ResizablePanelGroup>
			) : (
				// The right rail remains a persistent navigator. File contents open
				// in center tabs so expanding folders and scrolling the tree survive.
				<FileTree
					changedOnly={false}
					changedOnlyData={changedOnlyData}
					filterText={filter}
					onSelectPath={handleSelectPath}
					selectedPath={treeSelectedPath}
					sessionId={sessionId}
				/>
			)}
		</section>
	);
}

function ContentScrollArea({ children }: { children: ReactNode }) {
	return (
		<div
			className="board-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain bg-background"
			data-files-scroll-root=""
		>
			<div className="flex w-full flex-col px-0">{children}</div>
		</div>
	);
}
