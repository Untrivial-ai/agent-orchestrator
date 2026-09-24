import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
	ChevronLeft,
	Columns2,
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
import { buildChangedOnlyTree, buildWorkspaceFileTree, type TreeNode } from "../hooks/useSessionWorkspaceTree";
import { useFileAnnotation } from "../hooks/useFileAnnotation";
import { useUiStore } from "../stores/ui-store";
import type { SessionArtifact } from "../types/workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "./ui/resizable";
import { FileTree } from "./FileTree";
import { FileContentPane, type FileOpenOptions } from "./FileContentPane";
import { PanelMessage, RetryButton } from "./WorkspaceDiffView";
import { WorkspaceReviewPane } from "./diffs/WorkspaceReviewPane";
import { ArtifactFileView } from "./ArtifactFileView";

const WORKSPACE_SOURCE: FilesSource = { kind: "workspace" };
const ARTIFACT_SOURCE: FilesSource = { kind: "artifact" };
const ARTIFACT_SOURCE_VALUE = "__artifacts__";

type SessionFileExplorerProps = {
	artifacts?: SessionArtifact[];
	sessionId: string;
	isMaximized?: boolean;
	onOpenFile?: (path: string, options?: FileOpenOptions) => void;
	onRevealRequestConsumed?: (key: number) => void;
	onSplitChange?: (split: boolean) => void;
	onToggleMaximized?: (next: boolean) => void;
	revealRequest?: { feedback?: boolean; path: string; key: number; source?: "artifact" } | null;
	split?: boolean;
};

export function SessionFileExplorer({
	artifacts = [],
	sessionId,
	isMaximized = false,
	onOpenFile,
	onRevealRequestConsumed,
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
	const [selectedArtifactPath, setSelectedArtifactPath] = useState<string | null>(null);
	const [sourceNotice, setSourceNotice] = useState("");
	const scmQuery = useSessionScmSummary(sessionId);
	const queryClient = useQueryClient();
	const connectionState = useWorkspaceFileConnectionState(sessionId);

	const changedOnly = useUiStore((state) => state.inspectorSessions[sessionId]?.filesChangedOnly ?? true);
	const source = useUiStore((state) => state.inspectorSessions[sessionId]?.filesSource ?? WORKSPACE_SOURCE);
	const setFilesChangedOnly = useUiStore((state) => state.setFilesChangedOnly);
	const setFilesSource = useUiStore((state) => state.setFilesSource);
	const annotation = useFileAnnotation(sessionId, {
		source: source.kind === "pull_request" ? `${source.label} (${source.url})` : "Workspace",
	});
	const snapshot = source.kind === "pull_request" ? scmQuery.data?.find((pr) => pr.url === source.url)?.headSha ?? "" : "";
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
		...sessionSourceFilesQueryOptions(sessionId, querySource, t("files.error.loadWorkspace")),
		enabled: source.kind !== "artifact",
		refetchInterval: (query) => workspaceFilesRefetchInterval(connectionState, Boolean(query.state.data?.degraded)),
	});
	const changedOnlyData = useMemo(
		() => (filesQuery.data ? buildChangedOnlyTree(filesQuery.data.files) : []),
		[filesQuery.data],
	);
	const hasChanges = filesQuery.data?.files.some((file) => file.status !== "unmodified") ?? false;
	const showChanges = source.kind === "workspace" && changedOnly && (!filesQuery.data || hasChanges);
	const sourceUnavailable = source.kind === "pull_request"
		&& (filesQuery.isError || Boolean(scmQuery.data && !scmQuery.data.some((pr) => pr.url === source.url)));

	useEffect(() => {
		setSelectedPath(null);
		setSelectedArtifactPath(null);
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
		if (revealRequest.source === "artifact") {
			setFilesSource(sessionId, ARTIFACT_SOURCE);
			setSelectedArtifactPath(revealRequest.path);
			return;
		}
		setFilesSource(sessionId, WORKSPACE_SOURCE);
		setFilesChangedOnly(sessionId, false);
		setSelectedPath(revealRequest.path);
		if (!isMaximized) onOpenFile?.(revealRequest.path, { mode: "file" });
	}, [isMaximized, onOpenFile, revealRequest, sessionId, setFilesChangedOnly, setFilesSource]);

	const handleSelectPath = (node: TreeNode) => {
		setSelectedPath(node.path);
		if (!isMaximized && source.kind === "workspace") onOpenFile?.(node.path, { mode: "file" });
	};
	const handleSelectArtifact = (node: TreeNode) => {
		setSelectedArtifactPath(node.path);
	};
	const handleViewChange = (next: boolean) => {
		setSelectedPath(null);
		setFilesChangedOnly(sessionId, next);
	};
	const treeSelectedPath = selectedPath;
	const selectedPreviousPath = filesQuery.data?.files.find((file) => file.path === selectedPath)?.previousPath;
	const sourceValue = source.kind === "pull_request" ? source.url : source.kind === "artifact" ? ARTIFACT_SOURCE_VALUE : "workspace";
	const selectSource = (value: string) => {
		setSourceNotice("");
		if (value === "workspace") {
			setFilesSource(sessionId, WORKSPACE_SOURCE);
			return;
		}
		if (value === ARTIFACT_SOURCE_VALUE) {
			setFilesSource(sessionId, ARTIFACT_SOURCE);
			return;
		}
		const pr = scmQuery.data?.find((candidate) => candidate.url === value);
		if (pr) setFilesSource(sessionId, { kind: "pull_request", number: pr.number, url: pr.url, label: `PR #${pr.number} · ${pr.sourceBranch || pr.title}` });
	};

	return (
		<section className="flex h-full min-h-0 flex-col bg-background text-foreground" aria-label={t("files.sessionFiles")}>
			<header className="flex min-h-10 shrink-0 flex-wrap items-center gap-0.5 border-b border-border bg-surface px-2 py-1">
				<Select onValueChange={selectSource} value={sourceValue}>
					<SelectTrigger aria-label={t("files.explorer.source")} className="h-8 max-w-56 min-w-32 text-xs">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="workspace">{t("files.explorer.workspaceSource")}</SelectItem>
						{scmQuery.data?.map((pr) => (
							<SelectItem key={pr.url} value={pr.url}>{`PR #${pr.number} · ${pr.sourceBranch || pr.title}`}</SelectItem>
						))}
						{artifacts.length > 0 ? (
							<SelectItem value={ARTIFACT_SOURCE_VALUE}>
								{artifacts.length > 1 ? t("inspector.artifacts", { count: artifacts.length }) : t("inspector.artifact")}
							</SelectItem>
						) : null}
					</SelectContent>
				</Select>
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
			<div className="shrink-0 border-b border-border px-3 py-1 text-2xs text-muted-foreground">
				{source.kind === "pull_request"
					? source.label
					: source.kind === "artifact"
						? artifacts.length > 1
							? t("inspector.artifacts", { count: artifacts.length })
							: t("inspector.artifact")
						: t("files.explorer.workspaceSource")}
				{sourceNotice ? ` — ${sourceNotice}` : ""}
			</div>
			{source.kind === "artifact" ? (
				<ArtifactFilesPanel
					artifact={selectedArtifact}
					artifactTree={artifactTree}
					feedbackRequestKey={artifactFeedbackRequestKey}
					filter={filter}
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
				) : filesQuery.data ? (
					<WorkspaceReviewPane
						annotation={annotation}
						data={filesQuery.data}
						filter={filter}
						onBrowseAll={() => source.kind === "workspace" && handleViewChange(false)}
						onOpenFile={onOpenFile}
						sessionId={sessionId}
						split={split}
					/>
				) : null
			) : isMaximized || source.kind === "pull_request" ? (
				// Maximized gives the explorer the full window — plenty of room for
				// the tree and the content side by side, like a real editor.
				<ResizablePanelGroup className="min-h-0 flex-1">
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
					<ResizableHandle />
					<ResizablePanel defaultSize="74%" minSize="40%">
						<ContentScrollArea>
							<FileContentPane annotation={annotation} path={selectedPath} previousPath={selectedPreviousPath} sessionId={sessionId} source={querySource} split={split} />
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

function ArtifactFilesPanel({
	artifact,
	artifactTree,
	feedbackRequestKey,
	filter,
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
							onFeedbackRequestConsumed={onFeedbackRequestConsumed}
							path={artifact.path}
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
					onFeedbackRequestConsumed={onFeedbackRequestConsumed}
					path={artifact.path}
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
			className="board-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain bg-background"
			data-files-scroll-root=""
		>
			<div className="flex w-full flex-col px-0">{children}</div>
		</div>
	);
}
