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
import { sessionUiKey } from "../lib/hosts";

const WORKSPACE_SOURCE: FilesSource = { kind: "workspace" };
// Mirrors the browser panel's tab strip (.browser-panel__tab): no container
// box, 28px rounded tabs, filled only when active.
const viewTabClass = "inline-flex h-control-md items-center rounded-md px-2.5 text-xs font-medium transition-colors focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/50";

type SessionFileExplorerProps = {
	sessionId: string;
	hostId?: string;
	isMaximized?: boolean;
	onOpenFile?: (path: string, options?: FileOpenOptions) => void;
	onSplitChange?: (split: boolean) => void;
	onToggleMaximized?: (next: boolean) => void;
	revealRequest?: { path: string; key: number } | null;
	/** Called once a reveal request has opened its file, so the owner can drop it. */
	onRevealHandled?: (key: number) => void;
	split?: boolean;
};

export function SessionFileExplorer({
	sessionId,
	hostId,
	isMaximized = false,
	onOpenFile,
	onSplitChange,
	onToggleMaximized,
	revealRequest,
	onRevealHandled,
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
	// Keyed by PR URL so a commit picked in one PR never applies to another.
	const [selectedPRCommit, setSelectedPRCommit] = useState<{ url: string; sha: string } | null>(null);
	const filesTopbarHost = useFilesTopbarHost();
	const [treeOpen, setTreeOpen] = useState(true);
	const uiKey = sessionUiKey(sessionId, hostId);
	const scmQuery = useSessionScmSummary(sessionId, true, undefined, false, hostId);
	const prSummaries = scmQuery.data?.prs ?? [];
	const queryClient = useQueryClient();
	const connectionState = useWorkspaceFileConnectionState(sessionId, hostId);

	const changedOnly = useUiStore((state) => state.inspectorSessions[uiKey]?.filesChangedOnly ?? true);
	const source = useUiStore((state) => state.inspectorSessions[uiKey]?.filesSource ?? WORKSPACE_SOURCE);
	const setFilesChangedOnly = useUiStore((state) => state.setFilesChangedOnly);
	const setFilesSource = useUiStore((state) => state.setFilesSource);
	const annotation = useFileAnnotation(sessionId, { hostId, source: source.kind === "workspace" ? "Workspace" : `${source.label} (${source.url})` });
	const snapshot = source.kind === "pull_request" ? prSummaries.find((pr) => pr.url === source.url)?.headSha ?? "" : "";
	const querySource = useMemo<FilesSource>(
		() => source.kind === "pull_request" ? { ...source, snapshot } : source,
		[source, snapshot],
	);

	const filesQuery = useQuery({
		...sessionSourceFilesQueryOptions(sessionId, querySource, t("files.error.loadWorkspace"), hostId),
		refetchInterval: (query) => workspaceFilesRefetchInterval(connectionState, Boolean(query.state.data?.degraded)),
	});
	// A PR's own commits (the Workspace's live in its Changes review). Picking one
	// narrows the tree and the preview to that commit.
	const prCommits = source.kind === "pull_request" ? filesQuery.data?.commits ?? [] : [];
	const prCommit = source.kind === "pull_request" && selectedPRCommit?.url === source.url
		? prCommits.find((commit) => commit.sha === selectedPRCommit.sha)
		: undefined;
	const sourceFiles = prCommit?.files ?? filesQuery.data?.files;
	const changedOnlyData = useMemo(
		() => (sourceFiles ? buildChangedOnlyTree(sourceFiles) : []),
		[sourceFiles],
	);
	const hasChanges = filesQuery.data?.files.some((file) => file.status !== "unmodified") ?? false;
	const showChanges = source.kind === "workspace" && changedOnly && (!filesQuery.data || hasChanges);
	const splitView = !showChanges && (isMaximized || source.kind === "pull_request");
	const sourceUnavailable = source.kind === "pull_request"
		&& (filesQuery.isError || Boolean(scmQuery.data && !prSummaries.some((pr) => pr.url === source.url)));

	useEffect(() => {
		setSelectedPath(null);
		setFilter("");
		setSourceNotice("");
		setSelectedPRCommit(null);
	}, [uiKey]);

	useEffect(() => {
		if (!sourceUnavailable) return;
		setFilesSource(uiKey, WORKSPACE_SOURCE);
		setSelectedPath(null);
		setSourceNotice(t("files.explorer.sourceUnavailable"));
	}, [uiKey, setFilesSource, sourceUnavailable, t]);

	useEffect(() => subscribeWorkspaceFileChanges(sessionId, queryClient, hostId), [queryClient, sessionId, hostId]);
	useEffect(() => {
		window.localStorage.setItem("ao.files.diffStyle", split ? "split" : "unified");
	}, [split]);
	useEffect(() => {
		if (!revealRequest) return;
		setFilesChangedOnly(uiKey, false);
		setSelectedPath(revealRequest.path);
		// Maximized, the file is shown in this view's own preview; the request
		// stays pending so un-maximizing still opens it in the centre.
		if (isMaximized) return;
		onOpenFile?.(revealRequest.path, { mode: "file" });
		onRevealHandled?.(revealRequest.key);
	}, [isMaximized, onOpenFile, onRevealHandled, revealRequest, uiKey, setFilesChangedOnly]);

	const handleSelectPath = (node: TreeNode) => {
		setPreviewRequest(null);
		setSelectedPath(node.path);
		if (!isMaximized && source.kind === "workspace") onOpenFile?.(node.path, { mode: "file" });
	};
	const handleViewChange = (next: boolean) => {
		setPreviewRequest(null);
		setSelectedPath(null);
		setFilesChangedOnly(uiKey, next);
	};
	const openInMaximizedPreview = (path: string, options?: FileOpenOptions) => {
		setPreviewRequest((current) => ({ ...options, key: (current?.key ?? 0) + 1 }));
		setSelectedPath(path);
		setFilesChangedOnly(uiKey, false);
	};
	const treeSelectedPath = selectedPath;
	const selectedPreviousPath = sourceFiles?.find((file) => file.path === selectedPath)?.previousPath;
	const sourceValue = source.kind === "workspace" ? "workspace" : source.url;
	const sourceOptions: { value: string; label: string }[] = [
		{ value: "workspace", label: t("files.explorer.workspaceSource") },
		...prSummaries.map((pr) => ({ value: pr.url, label: `PR #${pr.number} · ${pr.sourceBranch || pr.title}` })),
	];
	const selectPRCommit = (sha: string | null) => {
		setPreviewRequest(null);
		setSelectedPath(null);
		setSelectedPRCommit(sha && source.kind === "pull_request" ? { url: source.url, sha } : null);
	};
	// A PR's menu mirrors the Workspace review's: Changes (the whole PR) on top,
	// then its commits. Staged/unstaged scopes are local-only, so a PR has none.
	const prSourceMenu: ReviewSourceMenu | null = prCommits.length > 0
		? {
			label: prCommit ? prCommit.sha.slice(0, 7) : t("files.reviewChanges"),
			scopes: [{ key: "combined", label: t("files.reviewChanges"), selected: !prCommit, select: () => selectPRCommit(null) }],
			commits: prCommits.map((commit) => ({ sha: commit.sha, subject: commit.subject, timestamp: commit.timestamp, selected: commit.sha === prCommit?.sha, select: () => selectPRCommit(commit.sha) })),
		}
		: null;
	const sourceMenu = source.kind === "pull_request" ? prSourceMenu : reviewMenu;
	// Branch › lists only PRs (Workspace is not a branch; it gets its own button
	// before the picker), so it appears only when there is a PR to switch to.
	const showBranchMenu = sourceOptions.length > 1;
	// The picker is hidden until it has something to choose: no review scopes or
	// commits (nothing changed) and no PR listed, e.g. while an active PR's
	// summary is still loading — the Workspace button stays the way back.
	const showSourcePicker = sourceMenu !== null || showBranchMenu;
	const currentSourceLabel = sourceOptions.find((option) => option.value === sourceValue)?.label;
	const selectSource = (value: string) => {
		setSourceNotice("");
		setPreviewRequest(null);
		setSelectedPath(null);
		setSelectedPRCommit(null);
		if (value === "workspace") {
			setFilesSource(uiKey, WORKSPACE_SOURCE);
			return;
		}
		const pr = prSummaries.find((candidate) => candidate.url === value);
		if (pr) setFilesSource(uiKey, { kind: "pull_request", number: pr.number, url: pr.url, label: `PR #${pr.number} · ${pr.sourceBranch || pr.title}` });
	};

	// The Changes view only exists for the workspace; for a PR the switch would
	// do nothing, so it is not shown.
	const hasViewTabs = hasChanges && source.kind === "workspace";
	// Docked with nothing to pick or review, the header would hold only the
	// maximize button (the filter lives in the top bar), so it floats over the
	// tree's top-right corner instead of reserving an empty row above it.
	const floatingHeader = !showSourcePicker && Boolean(filesTopbarHost) && !showChanges && !splitView && !hasViewTabs;
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
		<section className="relative flex h-full min-h-0 flex-col bg-background text-foreground" aria-label={t("files.sessionFiles")}>
			{/* The header keeps one geometry in every view (Changes, Files, the
			    preview + tree split), so switching views never moves its controls;
			    the split draws its divider on the content below instead.
			    The trailing actions sit 4px from the right edge with 4px gaps, the same
			    as the pinned top-bar buttons above them and the review rows below. */}
			<header className={floatingHeader ? "absolute right-0 top-0 z-10 flex items-center gap-1 pr-1 pt-1" : "flex shrink-0 items-center gap-1 pb-3 pl-3 pr-1 pt-1"}>
				{/* One dropdown for "what am I reviewing", laid out like a VCS review
				    picker: working scopes at the top, then Commits › and Branch ›
				    flyouts (Branch = Workspace or a PR). */}
				{/* -ml-2 cancels the trigger's own 8px inline padding so its label
				    starts on the same 12px gutter as the context row's text below. */}
				{/* While a PR is shown, Workspace sits before the picker as the way back;
				    on the workspace itself the trigger already names it. It keys off the
				    source alone, so it stays reachable while the PR list loads or fails. */}
				{source.kind === "pull_request" ? (
					<Button className="-ml-2 h-control-md shrink-0 text-xs" onClick={() => selectSource("workspace")} size="sm" type="button" variant="ghost">
						{t("files.explorer.workspaceSource")}
					</Button>
				) : null}
				{showSourcePicker ? (
				<DropdownMenu>
					<DropdownMenuTrigger asChild>
						<SettingsMenuTrigger
							aria-label={t("files.explorer.source")}
							className={cn(source.kind !== "pull_request" && "-ml-2", "h-control-md min-w-0 max-w-72 shrink text-xs")}
							title={currentSourceLabel}
						>
							<span className="min-w-0 truncate">{currentSourceLabel}</span>
							{sourceMenu ? <span className="shrink-0 text-caption text-passive">{sourceMenu.label}</span> : null}
						</SettingsMenuTrigger>
					</DropdownMenuTrigger>
					<DropdownMenuContent align="start" className="w-max max-w-72">
						{sourceMenu?.scopes.map((scope) => (
							<DropdownMenuItem className="gap-1.5" key={scope.key} onSelect={scope.select}>
								<span className="min-w-0 truncate">{scope.label}</span>
								<span className="ml-auto flex size-4 shrink-0 items-center justify-center">
									{scope.selected ? <Check aria-hidden="true" className="text-logo-accent" /> : null}
								</span>
							</DropdownMenuItem>
						))}
						{sourceMenu && sourceMenu.scopes.length > 0 && (sourceMenu.commits.length > 0 || showBranchMenu) ? <DropdownMenuSeparator /> : null}
						{sourceMenu && sourceMenu.commits.length > 0 ? (
							<DropdownMenuSub>
								<DropdownMenuSubTrigger className={cn(sourceMenu.commits.some((commit) => commit.selected) && "text-foreground")}>
									{t("files.commits")}
								</DropdownMenuSubTrigger>
								<DropdownMenuSubContent className="w-max max-w-[min(28rem,calc(100vw_-_2rem))]">
									<div className="board-scrollbar flex max-h-72 flex-col gap-px overflow-y-auto pr-0.5">
										{sourceMenu.commits.map((commit) => (
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
						{showBranchMenu ? <DropdownMenuSub>
							<DropdownMenuSubTrigger>{t("files.branch")}</DropdownMenuSubTrigger>
							<DropdownMenuSubContent className="w-max max-w-[min(28rem,calc(100vw_-_2rem))]">
								{sourceOptions.filter((option) => option.value !== "workspace").map((option) => (
									<DropdownMenuItem className="gap-2" key={option.value} onSelect={() => selectSource(option.value)}>
										<span className="min-w-0 flex-1 truncate">{option.label}</span>
										<span className="flex size-4 shrink-0 items-center justify-center">
											{option.value === sourceValue ? <Check aria-hidden="true" className="text-logo-accent" /> : null}
										</span>
									</DropdownMenuItem>
								))}
							</DropdownMenuSubContent>
						</DropdownMenuSub> : null}
					</DropdownMenuContent>
				</DropdownMenu>
				) : null}
				{/* The inspector top bar or the maximized overlay's titlebar hosts the
				    filter; without a host it sits centred between the picker and the actions. */}
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
						hostId={hostId}
						data={filesQuery.data}
						filter={filter}
						onBrowseAll={() => source.kind === "workspace" && handleViewChange(false)}
						canOpenInCenter={!isMaximized && Boolean(onOpenFile)}
						onOpenFile={isMaximized ? openInMaximizedPreview : onOpenFile}
						onSourceMenuChange={setReviewMenu}
						sessionId={sessionId}
						split={split}
					/>
				) : null
			) : splitView ? (
				// Preview on the left, tree on the right (collapsible from the header),
				// like an editor's changed-files rail.
				<ResizablePanelGroup className="min-h-0 flex-1 border-t border-border">
					<ResizablePanel defaultSize="74%" minSize="40%">
						<ContentScrollArea>
							<FileContentPane annotation={annotation} commitSha={prCommit?.sha ?? previewRequest?.commitSha} hostId={hostId} initialEditing={previewRequest?.editing ?? false} initialMode={previewRequest?.mode} initialRequestKey={previewRequest?.key ?? 0} path={selectedPath} previousPath={selectedPreviousPath} scope={previewRequest?.scope} sessionId={sessionId} source={querySource} split={split} toolbar="compact" />
						</ContentScrollArea>
					</ResizablePanel>
					{treeOpen ? (
						<>
							<ResizableHandle />
							<ResizablePanel defaultSize="26%" minSize="18%" maxSize="50%">
								<FileTree
									hostId={hostId}
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
					hostId={hostId}
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
