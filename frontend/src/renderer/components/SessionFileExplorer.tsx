import { useEffect, useMemo, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
	Check,
	Columns2,
	FolderTree,
	Folders,
	GitCompareArrows,
	Maximize2,
	Minimize2,
	Rows3,
	Search,
} from "lucide-react";
import {
	sessionSourceFilesQueryOptions,
	type FilesSource,
	useWorkspaceFileConnectionState,
	workspaceFilesRefetchInterval,
} from "../hooks/useSessionWorkspaceFiles";
import { useSessionScmSummary } from "../hooks/useSessionScmSummary";
import { subscribeWorkspaceFileChanges } from "../lib/workspace-file-events";
import { buildChangedOnlyTree, type TreeNode } from "../hooks/useSessionWorkspaceTree";
import { useFileAnnotation } from "../hooks/useFileAnnotation";
import { useUiStore } from "../stores/ui-store";
import { cn } from "../lib/utils";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "./ui/resizable";
import { SettingsMenuTrigger } from "./settings/SettingsMenuTrigger";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuSub,
	DropdownMenuSubContent,
	DropdownMenuSubTrigger,
	DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { useFilesTopbarHost } from "./files-topbar-host";
import { FileTree } from "./FileTree";
import { FileContentPane, type FileOpenOptions } from "./FileContentPane";
import { PanelMessage, RetryButton } from "./WorkspaceDiffView";
import { WorkspaceReviewPane, type ReviewSourceMenu } from "./diffs/WorkspaceReviewPane";
import { formatTimeTerse } from "../lib/format-time";

const WORKSPACE_SOURCE: FilesSource = { kind: "workspace" };
// Mirrors the browser panel's tab strip (.browser-panel__tab): no container
// box, 28px rounded tabs, filled only when active.
const viewTabClass = "inline-flex h-control-md items-center rounded-md px-2.5 text-xs font-medium transition-colors focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/50";

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
	// Maximized, the review pane's edit/preview actions open the file in this
	// view's own preview (the center pane is hidden behind the overlay).
	const [previewRequest, setPreviewRequest] = useState<(FileOpenOptions & { key: number }) | null>(null);
	const [sourceNotice, setSourceNotice] = useState("");
	const [reviewMenu, setReviewMenu] = useState<ReviewSourceMenu | null>(null);
	const filesTopbarHost = useFilesTopbarHost();
	const [treeOpen, setTreeOpen] = useState(true);
	const scmQuery = useSessionScmSummary(sessionId);
	const queryClient = useQueryClient();
	const connectionState = useWorkspaceFileConnectionState(sessionId);

	const changedOnly = useUiStore((state) => state.inspectorSessions[sessionId]?.filesChangedOnly ?? true);
	const source = useUiStore((state) => state.inspectorSessions[sessionId]?.filesSource ?? WORKSPACE_SOURCE);
	const setFilesChangedOnly = useUiStore((state) => state.setFilesChangedOnly);
	const setFilesSource = useUiStore((state) => state.setFilesSource);
	const annotation = useFileAnnotation(sessionId, { source: source.kind === "workspace" ? "Workspace" : `${source.label} (${source.url})` });
	const snapshot = source.kind === "pull_request" ? scmQuery.data?.find((pr) => pr.url === source.url)?.headSha ?? "" : "";
	const querySource = useMemo<FilesSource>(
		() => source.kind === "pull_request" ? { ...source, snapshot } : source,
		[source, snapshot],
	);

	const filesQuery = useQuery({
		...sessionSourceFilesQueryOptions(sessionId, querySource, t("files.error.loadWorkspace")),
		refetchInterval: workspaceFilesRefetchInterval(connectionState),
	});
	const changedOnlyData = useMemo(
		() => (filesQuery.data ? buildChangedOnlyTree(filesQuery.data.files) : []),
		[filesQuery.data],
	);
	const hasChanges = filesQuery.data?.files.some((file) => file.status !== "unmodified") ?? false;
	const showChanges = source.kind === "workspace" && changedOnly && (!filesQuery.data || hasChanges);
	const splitView = !showChanges && (isMaximized || source.kind === "pull_request");
	const sourceUnavailable = source.kind === "pull_request"
		&& (filesQuery.isError || Boolean(scmQuery.data && !scmQuery.data.some((pr) => pr.url === source.url)));

	useEffect(() => {
		setSelectedPath(null);
		setFilter("");
		setSourceNotice("");
	}, [sessionId]);

	useEffect(() => {
		if (!sourceUnavailable) return;
		setFilesSource(sessionId, WORKSPACE_SOURCE);
		setSelectedPath(null);
		setSourceNotice(t("files.explorer.sourceUnavailable"));
	}, [sessionId, setFilesSource, sourceUnavailable, t]);

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
		setPreviewRequest(null);
		setSelectedPath(node.path);
		if (!isMaximized && source.kind === "workspace") onOpenFile?.(node.path, { mode: "file" });
	};
	const handleViewChange = (next: boolean) => {
		setPreviewRequest(null);
		setSelectedPath(null);
		setFilesChangedOnly(sessionId, next);
	};
	const openInMaximizedPreview = (path: string, options?: FileOpenOptions) => {
		setPreviewRequest((current) => ({ ...options, key: (current?.key ?? 0) + 1 }));
		setSelectedPath(path);
		setFilesChangedOnly(sessionId, false);
	};
	const treeSelectedPath = selectedPath;
	const selectedPreviousPath = filesQuery.data?.files.find((file) => file.path === selectedPath)?.previousPath;
	const sourceValue = source.kind === "workspace" ? "workspace" : source.url;
	const sourceOptions: { value: string; label: string }[] = [
		{ value: "workspace", label: t("files.explorer.workspaceSource") },
		...(scmQuery.data ?? []).map((pr) => ({ value: pr.url, label: `PR #${pr.number} · ${pr.sourceBranch || pr.title}` })),
	];
	// With no review scopes or commits (nothing changed) and no PR to switch to,
	// the picker's only entry is the already-selected Workspace, so it is hidden
	// until there is something to choose.
	const showSourcePicker = reviewMenu !== null || source.kind !== "workspace" || sourceOptions.length > 1;
	const currentSourceLabel = sourceOptions.find((option) => option.value === sourceValue)?.label;
	const selectSource = (value: string) => {
		setSourceNotice("");
		setPreviewRequest(null);
		setSelectedPath(null);
		if (value === "workspace") {
			setFilesSource(sessionId, WORKSPACE_SOURCE);
			return;
		}
		const pr = scmQuery.data?.find((candidate) => candidate.url === value);
		if (pr) setFilesSource(sessionId, { kind: "pull_request", number: pr.number, url: pr.url, label: `PR #${pr.number} · ${pr.sourceBranch || pr.title}` });
	};

	// The Changes view only exists for the workspace; for a PR the switch would
	// do nothing, so it is not shown.
	const hasViewTabs = hasChanges && source.kind === "workspace";
	// In the preview + tree split the (already active) All-files tab doubles as
	// the tree toggle, so the header doesn't carry two file-tree buttons.
	const allFilesTabLabel = !showChanges && splitView ? (treeOpen ? t("files.hideFileTree") : t("files.showFileTree")) : t("files.allFiles");

	const filterField = (
		<label className={cn("relative min-w-0", filesTopbarHost ? "block w-full" : "w-64 shrink")}>
			<Search className="pointer-events-none absolute left-2.5 top-1/2 size-icon-sm -translate-y-1/2 text-passive" />
			<Input
				aria-label={t("files.explorer.filter")}
				className="inspector-field-input pl-8"
				onChange={(event) => setFilter(event.target.value)}
				placeholder={t("files.explorer.filterPlaceholder")}
				value={filter}
			/>
		</label>
	);

	return (
		<section className="flex h-full min-h-0 flex-col bg-background text-foreground" aria-label={t("files.sessionFiles")}>
			{/* In the tree + preview split the header gets a hairline divider with
			    only a sliver of space above it, so content never touches the line.
			    The trailing actions sit 4px from the right edge with 4px gaps, the same
			    as the pinned top-bar buttons above them and the review rows below. */}
			<header className={cn("flex min-h-10 shrink-0 items-center gap-1 pl-3 pr-1 pt-1", splitView ? "border-b border-border pb-1" : showChanges ? "pb-3" : "pb-1")}>
				{/* One dropdown for "what am I reviewing", laid out like a VCS review
				    picker: working scopes at the top, then Commits › and Branch ›
				    flyouts (Branch = Workspace or a PR). */}
				{/* -ml-2 cancels the trigger's own 8px inline padding so its label
				    starts on the same 12px gutter as the context row's text below. */}
				{showSourcePicker ? (
				<DropdownMenu>
					<DropdownMenuTrigger asChild>
						<SettingsMenuTrigger
							aria-label={t("files.explorer.source")}
							className="-ml-2 h-control-md min-w-0 max-w-72 shrink text-xs"
							title={currentSourceLabel}
						>
							<span className="min-w-0 truncate">{currentSourceLabel}</span>
							{reviewMenu ? <span className="shrink-0 text-caption text-passive">{reviewMenu.label}</span> : null}
						</SettingsMenuTrigger>
					</DropdownMenuTrigger>
					<DropdownMenuContent align="start" className="w-max max-w-72">
						{reviewMenu?.scopes.map((scope) => (
							<DropdownMenuItem className="gap-1.5" key={scope.key} onSelect={scope.select}>
								<span className="min-w-0 truncate">{scope.label}</span>
								<span className="ml-auto flex size-4 shrink-0 items-center justify-center">
									{scope.selected ? <Check aria-hidden="true" className="text-logo-accent" /> : null}
								</span>
							</DropdownMenuItem>
						))}
						{reviewMenu && reviewMenu.scopes.length > 0 ? <DropdownMenuSeparator /> : null}
						{reviewMenu && reviewMenu.commits.length > 0 ? (
							<DropdownMenuSub>
								<DropdownMenuSubTrigger className={cn(reviewMenu.commits.some((commit) => commit.selected) && "text-foreground")}>
									{t("files.commits")}
								</DropdownMenuSubTrigger>
								<DropdownMenuSubContent className="w-max max-w-[min(28rem,calc(100vw_-_2rem))]">
									<div className="board-scrollbar flex max-h-72 flex-col gap-px overflow-y-auto pr-0.5">
										{reviewMenu.commits.map((commit) => (
											<DropdownMenuItem className="gap-2" key={commit.sha} onSelect={commit.select}>
												<span className="min-w-0 flex-1 truncate">{commit.subject}</span>
												<span className="shrink-0 text-caption text-passive">{formatTimeTerse(commit.timestamp)}</span>
												<span className="flex size-4 shrink-0 items-center justify-center">
													{commit.selected ? <Check aria-hidden="true" className="text-logo-accent" /> : null}
												</span>
											</DropdownMenuItem>
										))}
									</div>
								</DropdownMenuSubContent>
							</DropdownMenuSub>
						) : null}
						<DropdownMenuSub>
							<DropdownMenuSubTrigger>{t("files.branch")}</DropdownMenuSubTrigger>
							<DropdownMenuSubContent className="w-max max-w-[min(28rem,calc(100vw_-_2rem))]">
								{sourceOptions.map((option) => (
									<DropdownMenuItem className="gap-2" key={option.value} onSelect={() => selectSource(option.value)}>
										<span className="min-w-0 flex-1 truncate">{option.label}</span>
										<span className="flex size-4 shrink-0 items-center justify-center">
											{option.value === sourceValue ? <Check aria-hidden="true" className="text-logo-accent" /> : null}
										</span>
									</DropdownMenuItem>
								))}
							</DropdownMenuSubContent>
						</DropdownMenuSub>
					</DropdownMenuContent>
				</DropdownMenu>
				) : null}
				{/* Inline (maximized) the filter sits centred between the picker and the actions. */}
				{filesTopbarHost ? null : <span aria-hidden="true" className="flex-1" />}
				{filesTopbarHost ? createPortal(filterField, filesTopbarHost) : filterField}
				<span aria-hidden="true" className="flex-1" />
				{/* Unified/split applies wherever a diff shows: the Changes review and
				    the preview beside the tree. */}
				{showChanges || splitView ? (
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
									<Columns2 className="size-icon-base" aria-hidden="true" />
								) : (
									<Rows3 className="size-icon-base" aria-hidden="true" />
								)}
							</Button>
						</TooltipTrigger>
						<TooltipContent side="bottom">{split ? t("files.unifiedDiff") : t("files.splitDiff")}</TooltipContent>
					</Tooltip>
				) : null}
				{hasViewTabs ? (
					<div aria-label={t("files.viewMode")} className="flex shrink-0 items-center gap-1" role="tablist">
						<Tooltip>
							<TooltipTrigger asChild>
								<button
									aria-label={t("files.reviewChanges")}
									aria-selected={showChanges}
									className={cn(viewTabClass, "w-control-md justify-center px-0", showChanges ? "bg-interactive-active text-foreground" : "text-muted-foreground hover:bg-interactive-hover hover:text-foreground")}
									onClick={() => handleViewChange(true)}
									role="tab"
									type="button"
								>
									<GitCompareArrows aria-hidden="true" className="size-icon-base" />
								</button>
							</TooltipTrigger>
							<TooltipContent side="bottom">{t("files.reviewChanges")}</TooltipContent>
						</Tooltip>
						<Tooltip>
							<TooltipTrigger asChild>
								<button
									aria-label={allFilesTabLabel}
									aria-selected={!showChanges}
									className={cn(viewTabClass, "w-control-md justify-center px-0", !showChanges ? "bg-interactive-active text-foreground" : "text-muted-foreground hover:bg-interactive-hover hover:text-foreground")}
									onClick={() => (!showChanges && splitView ? setTreeOpen((open) => !open) : handleViewChange(false))}
									role="tab"
									type="button"
								>
									<Folders aria-hidden="true" className="size-icon-base" />
								</button>
							</TooltipTrigger>
							<TooltipContent side="bottom">{allFilesTabLabel}</TooltipContent>
						</Tooltip>
					</div>
				) : null}
				{splitView && !hasViewTabs ? (
					<Tooltip>
						<TooltipTrigger asChild>
							<Button
								aria-label={treeOpen ? t("files.hideFileTree") : t("files.showFileTree")}
								aria-pressed={treeOpen}
								className="shrink-0"
								onClick={() => setTreeOpen((open) => !open)}
								size="icon-sm"
								type="button"
								variant="ghost"
							>
								<FolderTree className="size-icon-base" aria-hidden="true" />
							</Button>
						</TooltipTrigger>
						<TooltipContent side="bottom">{treeOpen ? t("files.hideFileTree") : t("files.showFileTree")}</TooltipContent>
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
									<Minimize2 className="size-icon-base" aria-hidden="true" />
								) : (
									<Maximize2 className="size-icon-base" aria-hidden="true" />
								)}
							</Button>
						</TooltipTrigger>
						<TooltipContent side="bottom">{isMaximized ? t("files.minimize") : t("files.maximize")}</TooltipContent>
					</Tooltip>
				) : null}
			</header>
			{/* The source name already shows in the picker; this row only appears
			    to explain an automatic fall back to Workspace. */}
			{sourceNotice ? (
				<p className="shrink-0 border-b border-border px-3 py-1 text-2xs text-muted-foreground" role="status">
					{sourceNotice}
				</p>
			) : null}
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
						onBrowseAll={() => source.kind === "workspace" && handleViewChange(false)}
						onOpenFile={isMaximized ? openInMaximizedPreview : onOpenFile}
						onSourceMenuChange={setReviewMenu}
						sessionId={sessionId}
						split={split}
					/>
				) : null
			) : isMaximized || source.kind === "pull_request" ? (
				// Preview on the left, tree on the right (collapsible from the header),
				// like an editor's changed-files rail.
				<ResizablePanelGroup className="min-h-0 flex-1">
					<ResizablePanel defaultSize="74%" minSize="40%">
						<ContentScrollArea>
							<FileContentPane annotation={annotation} commitSha={previewRequest?.commitSha} initialEditing={previewRequest?.editing ?? false} initialMode={previewRequest?.mode} initialRequestKey={previewRequest?.key ?? 0} path={selectedPath} previousPath={selectedPreviousPath} scope={previewRequest?.scope} sessionId={sessionId} source={querySource} split={split} toolbar="compact" />
						</ContentScrollArea>
					</ResizablePanel>
					{treeOpen ? (
						<>
							<ResizableHandle />
							<ResizablePanel defaultSize="26%" minSize="18%" maxSize="50%">
								<FileTree
									changedOnly={source.kind === "pull_request"}
									changedOnlyData={changedOnlyData}
									filterText={filter}
									onSelectPath={handleSelectPath}
									selectedPath={treeSelectedPath}
									sessionId={sessionId}
								/>
							</ResizablePanel>
						</>
					) : null}
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
			className="board-scrollbar h-full min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain bg-background"
			data-files-scroll-root=""
		>
			<div className="flex w-full flex-col px-0">{children}</div>
		</div>
	);
}
