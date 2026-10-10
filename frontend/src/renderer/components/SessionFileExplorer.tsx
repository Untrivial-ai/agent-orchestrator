import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
	Check,
	ChevronLeft,
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
	sessionWorkspaceHistoryQueryOptions,
	type FilesSource,
	useWorkspaceFileConnectionState,
	workspaceFilesRefetchInterval,
} from "../hooks/useSessionWorkspaceFiles";
import { useSessionScmSummary } from "../hooks/useSessionScmSummary";
import { subscribeWorkspaceFileChanges } from "../lib/workspace-file-events";
import { buildChangedOnlyTree, buildWorkspaceFileTree, flattenChangedFiles, type TreeNode } from "../hooks/useSessionWorkspaceTree";
import { useFileAnnotation } from "../hooks/useFileAnnotation";
import { useUiStore } from "../stores/ui-store";
import { cn } from "../lib/utils";
import type { SessionArtifact } from "../types/workspace";
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
import { ArtifactFileView } from "./ArtifactFileView";
import { markFileViewerPerformance } from "../lib/file-viewer-performance";
import { sessionUiKey } from "../lib/hosts";

const WORKSPACE_SOURCE: FilesSource = { kind: "workspace" };
const ARTIFACT_SOURCE: FilesSource = { kind: "artifact" };
const ARTIFACT_SOURCE_VALUE = "__artifacts__";
// Mirrors the browser panel's tab strip (.browser-panel__tab): no container
// box, 28px rounded tabs, filled only when active.
const viewTabClass = "inline-flex h-control-md items-center rounded-md px-2.5 text-xs font-medium transition-colors focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/50";

type SessionFileExplorerProps = {
	artifacts?: SessionArtifact[];
	sessionId: string;
	hostId?: string;
	isMaximized?: boolean;
	onOpenFile?: (path: string, options?: FileOpenOptions) => void;
	onRevealRequestConsumed?: (key: number) => void;
	onSplitChange?: (split: boolean) => void;
	onToggleMaximized?: (next: boolean) => void;
	revealRequest?: { feedback?: boolean; path: string; key: number; source?: "artifact" } | null;
	/** Called once a reveal request has opened its file, so the owner can drop it. */
	onRevealHandled?: (key: number) => void;
	split?: boolean;
};

export function SessionFileExplorer({
	artifacts = [],
	sessionId,
	hostId,
	isMaximized = false,
	onOpenFile,
	onRevealRequestConsumed,
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
	const [selectedArtifactPath, setSelectedArtifactPath] = useState<string | null>(null);
	const [sourceNotice, setSourceNotice] = useState("");
	const [reviewMenu, setReviewMenu] = useState<ReviewSourceMenu | null>(null);
	// Keyed by PR URL so a commit picked in one PR never applies to another.
	const [selectedPRCommit, setSelectedPRCommit] = useState<{ url: string; sha: string } | null>(null);
	const filesTopbarHost = useFilesTopbarHost();
	const [treeOpen, setTreeOpen] = useState(true);
	// A PR has no continuous review, so its Changes tab previews one changed file
	// and its Files tab is the changed-files tree: the same switch as Workspace's.
	const [prView, setPRView] = useState<"changes" | "files">("files");
	const changesTabRef = useRef<HTMLButtonElement>(null);
	const uiKey = sessionUiKey(sessionId, hostId);
	const scmQuery = useSessionScmSummary(sessionId, true, undefined, false, hostId);
	const prSummaries = scmQuery.data?.prs ?? [];
	const queryClient = useQueryClient();
	const connectionState = useWorkspaceFileConnectionState(sessionId, hostId);
	useLayoutEffect(() => markFileViewerPerformance("files-shell-painted"), []);

	const changedOnly = useUiStore((state) => state.inspectorSessions[uiKey]?.filesChangedOnly ?? true);
	const source = useUiStore((state) => state.inspectorSessions[uiKey]?.filesSource ?? WORKSPACE_SOURCE);
	const setFilesChangedOnly = useUiStore((state) => state.setFilesChangedOnly);
	const setFilesSource = useUiStore((state) => state.setFilesSource);
	const annotation = useFileAnnotation(sessionId, {
		hostId,
		source: source.kind === "pull_request" ? `${source.label} (${source.url})` : "Workspace",
	});
	const snapshot = source.kind === "pull_request" ? prSummaries.find((pr) => pr.url === source.url)?.headSha ?? "" : "";
	const querySource = useMemo<FilesSource>(
		() => source.kind === "pull_request" ? { ...source, snapshot } : source,
		[source, snapshot],
	);
	const artifactTree = useMemo(
		() => buildWorkspaceFileTree(artifacts.map((artifact) => ({ binary: false, path: artifact.path, status: "unmodified" as const }))),
		[artifacts],
	);
	const selectedArtifact = artifacts.find((artifact) => artifact.path === selectedArtifactPath);
	const artifactFeedbackRequestKey =
		revealRequest?.source === "artifact" && revealRequest.feedback && revealRequest.path === selectedArtifact?.path
			? revealRequest.key
			: undefined;

	const filesQuery = useQuery({
		...sessionSourceFilesQueryOptions(sessionId, querySource, t("files.error.loadWorkspace"), hostId),
		enabled: source.kind !== "artifact",
		refetchInterval: (query) => workspaceFilesRefetchInterval(connectionState, Boolean(query.state.data?.degraded)),
	});
	useLayoutEffect(() => {
		if (filesQuery.data) markFileViewerPerformance("manifest-ready");
	}, [filesQuery.data?.workspaceVersion]);
	// Commit history and ahead/behind are not part of the latency-sensitive
	// manifest. Load the legacy enrichment only after the first useful Changes
	// snapshot is already available, so it can never gate opening Files.
	const historyQuery = useQuery({
		...sessionWorkspaceHistoryQueryOptions(sessionId, t("files.error.loadWorkspace"), hostId),
		enabled: source.kind === "workspace" && Boolean(filesQuery.data),
	});
	const filesData = useMemo(
		() => source.kind === "workspace" && filesQuery.data
			? {
				...filesQuery.data,
				...(historyQuery.data ? {
					ahead: historyQuery.data.ahead,
					behind: historyQuery.data.behind,
					commits: historyQuery.data.commits,
					commitsTruncated: historyQuery.data.commitsTruncated,
				} : {}),
			}
			: filesQuery.data,
		[filesQuery.data, historyQuery.data, source.kind],
	);
	// A PR's own commits (the Workspace's live in its Changes review). Picking one
	// narrows the tree and the preview to that commit.
	const prCommits = source.kind === "pull_request" ? filesData?.commits ?? [] : [];
	const prCommit = source.kind === "pull_request" && selectedPRCommit?.url === source.url
		? prCommits.find((commit) => commit.sha === selectedPRCommit.sha)
		: undefined;
	const sourceFiles = prCommit?.files ?? filesData?.files;
	const changedOnlyData = useMemo(
		() => (sourceFiles ? buildChangedOnlyTree(sourceFiles) : []),
		[sourceFiles],
	);
	const hasChanges = filesData?.files.some((file) => file.status !== "unmodified") ?? false;
	const showChanges = source.kind === "workspace" && changedOnly && (!filesData || hasChanges);
	const splitView = !showChanges && isMaximized && source.kind === "workspace";
	// The PR's changed files in tree order. Like a clean workspace, a PR (or
	// picked commit) with none has no Changes view, only the empty tree.
	const prFiles = useMemo(
		() => (source.kind === "pull_request" ? flattenChangedFiles(changedOnlyData) : []),
		[changedOnlyData, source.kind],
	);
	const showPRChanges = prFiles.length > 0 && prView === "changes";
	const sourceKey = source.kind === "pull_request" ? source.url : source.kind;
	const sourceUnavailable = source.kind === "pull_request"
		&& (filesQuery.isError || Boolean(scmQuery.data && !prSummaries.some((pr) => pr.url === source.url)));

	useEffect(() => {
		setSelectedPath(null);
		setSelectedArtifactPath(null);
		setFilter("");
		setSourceNotice("");
		setSelectedPRCommit(null);
		setPRView("files");
	}, [uiKey]);
	// Any source change (the picker, the Workspace button, the unavailable-PR
	// fallback, or the docked/maximized twin sharing the source) reopens a PR
	// on its tree.
	useEffect(() => setPRView("files"), [sourceKey]);

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
		if (revealRequest.source === "artifact") {
			setFilesSource(uiKey, ARTIFACT_SOURCE);
			setSelectedArtifactPath(revealRequest.path);
			return;
		}
		setFilesSource(uiKey, WORKSPACE_SOURCE);
		setFilesChangedOnly(uiKey, false);
		setSelectedPath(revealRequest.path);
		// Maximized, the file is shown in this view's own preview; the request
		// stays pending so un-maximizing still opens it in the centre.
		if (isMaximized) return;
		onOpenFile?.(revealRequest.path, { mode: "file" });
		onRevealHandled?.(revealRequest.key);
	}, [isMaximized, onOpenFile, onRevealHandled, revealRequest, uiKey, setFilesChangedOnly, setFilesSource]);

	const handleSelectPath = (node: TreeNode) => {
		setPreviewRequest(null);
		setSelectedPath(node.path);
		if (source.kind === "pull_request") {
			setPRView("changes");
			// The tree hides behind the preview; keep focus on the switch that
			// brings it back instead of letting it drop to the page.
			changesTabRef.current?.focus();
		} else if (!isMaximized) onOpenFile?.(node.path, { mode: "file" });
	};
	const handleSelectArtifact = (node: TreeNode) => {
		setSelectedArtifactPath(node.path);
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
	// A PR previews the picked file while it is still in this PR or commit;
	// otherwise the tree's first file (that matches the filter), which the tree
	// highlights, so both tabs agree on the current file.
	const normalizedFilter = filter.trim().toLocaleLowerCase();
	const previewPath = source.kind === "pull_request"
		? (prFiles.find((file) => file.path === selectedPath)
			?? prFiles.find((file) => file.path.toLocaleLowerCase().includes(normalizedFilter))
			?? prFiles[0])?.path ?? null
		: selectedPath;
	const selectedPreviousPath = sourceFiles?.find((file) => file.path === previewPath)?.previousPath;
	const sourceValue = source.kind === "pull_request" ? source.url : source.kind === "artifact" ? ARTIFACT_SOURCE_VALUE : "workspace";
	// Artifacts remain viewable by clicking one from the Summary panel (which
	// switches the Files source programmatically to ARTIFACT_SOURCE), but are
	// not offered as a manually selectable Files source alongside Workspace/PR.
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

	// Workspace and PR share one Changes/Files switch, shown only when there are
	// changes to switch to.
	const hasViewTabs = prFiles.length > 0 || (hasChanges && source.kind === "workspace");
	const changesTabActive = source.kind === "pull_request" ? showPRChanges : showChanges;
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
			<header className={floatingHeader ? "absolute right-0 top-0 z-10 flex items-center gap-1 pr-1 pt-1" : "flex shrink-0 items-center gap-1 pb-3 pl-3 pr-1 pt-2"}>
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
				{/* Unified/split applies wherever a diff shows: the Changes review, a
				    PR's Changes preview, and the maximized preview beside the tree. */}
				{showChanges || splitView || showPRChanges ? (
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
									aria-selected={changesTabActive}
									ref={changesTabRef}
									className={cn(viewTabClass, "w-control-md justify-center px-0", changesTabActive ? "bg-interactive-active text-foreground" : "text-muted-foreground hover:bg-interactive-hover hover:text-foreground")}
									onClick={() => (source.kind === "pull_request" ? setPRView("changes") : handleViewChange(true))}
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
									aria-selected={!changesTabActive}
									className={cn(viewTabClass, "w-control-md justify-center px-0", !changesTabActive ? "bg-interactive-active text-foreground" : "text-muted-foreground hover:bg-interactive-hover hover:text-foreground")}
									onClick={() => (source.kind === "pull_request" ? setPRView("files") : !showChanges && splitView ? setTreeOpen((open) => !open) : handleViewChange(false))}
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
			{source.kind === "artifact" ? (
				<ArtifactFilesPanel
					artifact={selectedArtifact}
					artifactTree={artifactTree}
					feedbackRequestKey={artifactFeedbackRequestKey}
					filter={filter}
					hostId={hostId}
					isMaximized={isMaximized}
					onBack={() => setSelectedArtifactPath(null)}
					onFeedbackRequestConsumed={onRevealRequestConsumed}
					onSelect={handleSelectArtifact}
					selectedPath={selectedArtifactPath}
					sessionId={sessionId}
				/>
			) : showChanges ? (
				filesQuery.isPending ? (
					<PanelMessage>{t("files.loading")}</PanelMessage>
				) : filesQuery.isError ? (
					<PanelMessage action={<RetryButton onClick={() => void filesQuery.refetch()} />}>
						{filesQuery.error.message || t("files.error.loadWorkspace")}
					</PanelMessage>
				) : filesData ? (
					<WorkspaceReviewPane
						annotation={annotation}
						data={filesData}
						hostId={hostId}
						filter={filter}
						onBrowseAll={() => source.kind === "workspace" && handleViewChange(false)}
						canOpenInCenter={!isMaximized && Boolean(onOpenFile)}
						onOpenFile={isMaximized ? openInMaximizedPreview : onOpenFile}
						onSourceMenuChange={setReviewMenu}
						sessionId={sessionId}
						split={split}
					/>
				) : null
			) : source.kind === "pull_request" ? (
				// The tree stays mounted (hidden) under the preview, so its expanded
				// folders and scroll position survive a pick and Files returns to them.
				<div className="relative min-h-0 flex-1">
					<div className={cn("absolute inset-0 flex flex-col", showPRChanges && "invisible")} data-testid="pr-files-tree">
						<FileTree
							activateOnClick
							hostId={hostId}
							changedOnly
							changedOnlyData={changedOnlyData}
							filterText={filter}
							onSelectPath={handleSelectPath}
							selectedPath={previewPath}
							sessionId={sessionId}
						/>
					</div>
					{showPRChanges ? (
						<div className="absolute inset-0 flex flex-col">
							<ContentScrollArea>
								<FileContentPane annotation={annotation} commitSha={prCommit?.sha} hostId={hostId} path={previewPath} previousPath={selectedPreviousPath} sessionId={sessionId} source={querySource} split={split} toolbar="compact" />
							</ContentScrollArea>
						</div>
					) : null}
				</div>
			) : splitView ? (
				// Maximized Workspace: preview on the left, the full tree on the right
				// (collapsible from the header).
				<ResizablePanelGroup className="min-h-0 flex-1 border-t border-border">
					<ResizablePanel defaultSize="74%" minSize="40%">
						<ContentScrollArea>
							<FileContentPane annotation={annotation} commitSha={previewRequest?.commitSha} hostId={hostId} initialEditing={previewRequest?.editing ?? false} initialMode={previewRequest?.mode} initialRequestKey={previewRequest?.key ?? 0} path={previewPath} previousPath={selectedPreviousPath} scope={previewRequest?.scope} sessionId={sessionId} source={querySource} split={split} toolbar="compact" />
						</ContentScrollArea>
					</ResizablePanel>
					{treeOpen ? (
						<>
							<ResizableHandle />
							<ResizablePanel defaultSize="26%" minSize="18%" maxSize="50%">
								<FileTree
									hostId={hostId}
									changedOnly={false}
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

function ArtifactFilesPanel({
	artifact,
	artifactTree,
	feedbackRequestKey,
	filter,
	hostId,
	isMaximized,
	onBack,
	onFeedbackRequestConsumed,
	onSelect,
	selectedPath,
	sessionId,
}: {
	artifact?: SessionArtifact;
	artifactTree: TreeNode[];
	feedbackRequestKey?: number;
	filter: string;
	hostId?: string;
	isMaximized: boolean;
	onBack: () => void;
	onFeedbackRequestConsumed?: (key: number) => void;
	onSelect: (node: TreeNode) => void;
	selectedPath: string | null;
	sessionId: string;
}) {
	const { t } = useTranslation();
	if (isMaximized) {
		return (
			<ResizablePanelGroup className="min-h-0 flex-1">
				<ResizablePanel defaultSize="26%" minSize="18%" maxSize="50%">
					<FileTree
						changedOnly
						changedOnlyData={artifactTree}
						filterText={filter}
						onSelectPath={onSelect}
						selectedPath={selectedPath}
						sessionId={sessionId}
					/>
				</ResizablePanel>
				<ResizableHandle />
				<ResizablePanel defaultSize="74%" minSize="40%">
					{artifact ? (
						<ArtifactFileView
							artifactName={artifact.name}
							feedbackRequestKey={feedbackRequestKey}
							hostId={hostId}
							onFeedbackRequestConsumed={onFeedbackRequestConsumed}
							path={artifact.path}
							rawUrl={artifact.rawUrl}
							sessionId={sessionId}
						/>
					) : (
						<PanelMessage>{t("files.explorer.selectFile")}</PanelMessage>
					)}
				</ResizablePanel>
			</ResizablePanelGroup>
		);
	}
	if (artifact) {
		return (
			<div className="flex min-h-0 flex-1 flex-col">
				<div className="flex h-8 shrink-0 items-center gap-1 border-b border-border bg-surface px-1">
					<Button
						aria-label={t("files.explorer.backToTree")}
						onClick={onBack}
						size="icon-sm"
						type="button"
						variant="ghost"
					>
						<ChevronLeft className="size-icon-sm" aria-hidden="true" />
					</Button>
					<span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground">{artifact.path}</span>
				</div>
				<ArtifactFileView
					artifactName={artifact.name}
					feedbackRequestKey={feedbackRequestKey}
					hostId={hostId}
					onFeedbackRequestConsumed={onFeedbackRequestConsumed}
					path={artifact.path}
					rawUrl={artifact.rawUrl}
					sessionId={sessionId}
				/>
			</div>
		);
	}
	return (
		<FileTree
			changedOnly
			changedOnlyData={artifactTree}
			filterText={filter}
			onSelectPath={onSelect}
			selectedPath={selectedPath}
			sessionId={sessionId}
		/>
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
