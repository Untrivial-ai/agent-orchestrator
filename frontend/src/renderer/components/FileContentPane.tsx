import { useCallback, useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Eye, FileCode2, GitCompareArrows, LoaderCircle, MessageSquarePlus, Pencil, Save, X } from "lucide-react";
import { Editor, type EditorFactory } from "@pierre/diffs/edit";
import { EditProvider } from "@pierre/diffs/react";
import {
	sessionWorkspaceFileQueryKey,
	sessionSourceFileQueryOptions,
	sessionSourceFileRevisionQueryOptions,
	updateSessionWorkspaceFile,
	type WorkspaceDiffScope,
	type WorkspaceFileDetail,
	type FilesSource,
} from "../hooks/useSessionWorkspaceFiles";
import { usePierreFileHighlightReady } from "../hooks/usePierreFileHighlight";
import { cn } from "../lib/utils";
import { statusLabel, statusTone } from "../lib/workspace-file-status";
import {
	canSplitCompare,
	FileAnnotationComposer,
	PanelMessage,
	ReviewDiffBody,
	RetryButton,
	type FileAnnotationModel,
} from "./WorkspaceDiffView";
import { ReadOnlyFileView } from "./ReadOnlyFileView";
import { WorkspaceEntryIcon } from "./WorkspaceEntryIcon";
import { AoDiffFile } from "./diffs/AoDiffFile";
import { AO_PIERRE_FILES_REVIEW_CSS } from "./diffs/pierreTheme";
import { Button } from "./ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { MarkdownFileView } from "./markdown/MarkdownFileView";

// Edit-mode Cancel/Save sit beside icon-sm toolbar buttons; keep them the same
// height with small text and icons so they do not dwarf the toolbar.
const EDIT_ACTION_CLASS = "h-6 gap-1 px-2 text-xs";

export type FileViewMode = "diff" | "file" | "rendered";
export type FileOpenOptions = { commitSha?: string; editing?: boolean; mode?: FileViewMode; scope?: WorkspaceDiffScope };

const DEFAULT_FILES_SOURCE: FilesSource = { kind: "workspace" };

const createReviewEditor: EditorFactory<"feedback", undefined> = (editorType, options, editStateKey) =>
	new Editor(editorType, options, editStateKey);

function canRenderMarkdown(path: string, detail: WorkspaceFileDetail): boolean {
	return !detail.deleted && !detail.binary && !detail.contentTruncated && /\.(md|markdown)$/i.test(path);
}

export function FileContentPane({
	annotation,
	initialEditing = false,
	initialMode = "diff",
	initialRequestKey = 0,
	commitSha,
	onDirtyChange,
	path,
	previousPath,
	sessionId,
	split,
	scope = "combined",
	source = DEFAULT_FILES_SOURCE,
	toolbar = "tabs",
}: {
	annotation: FileAnnotationModel;
	initialEditing?: boolean;
	initialMode?: FileViewMode;
	initialRequestKey?: number;
	commitSha?: string;
	onDirtyChange?: (dirty: boolean) => void;
	path: string | null;
	previousPath?: string;
	sessionId: string;
	split: boolean;
	scope?: WorkspaceDiffScope;
	source?: FilesSource;
	/**
	 * "tabs" (default, center file tabs): status letter + text mode tabs.
	 * "compact" (Files panel split view only): file breadcrumb on the left, icon
	 * mode switches on the right, no status letter (the tree already shows it).
	 */
	toolbar?: "tabs" | "compact";
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [mode, setMode] = useState<FileViewMode>(initialMode);
	const [editing, setEditing] = useState(false);
	const [draft, setDraft] = useState("");
	const [saving, setSaving] = useState(false);
	const [saveError, setSaveError] = useState("");
	const sourceHighlightReady = usePierreFileHighlightReady(path);
	// A background refetch mid-selection would re-render the pane out from under
	// an active native text selection.
	const [selectionOrMenuActive, setSelectionOrMenuActive] = useState(false);
	const query = useQuery({
		...sessionSourceFileQueryOptions(sessionId, source, path ?? "", t("files.error.loadWorkspaceFile"), scope, commitSha, previousPath),
		enabled: Boolean(path) && !selectionOrMenuActive,
	});
	const hasUnsavedChanges = Boolean(editing && query.data && draft !== query.data.content);
	useEffect(() => {
		setMode(initialMode);
		setEditing(initialEditing);
		setDraft("");
		setSaveError("");
	}, [commitSha, initialEditing, initialMode, initialRequestKey, path, scope, source]);
	useEffect(() => {
		if (initialEditing && query.data) setDraft(query.data.content);
	}, [initialEditing, initialRequestKey, path, query.data]);
	useEffect(() => {
		onDirtyChange?.(hasUnsavedChanges);
	}, [hasUnsavedChanges, onDirtyChange]);
	useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);
	const saveEditing = useCallback(async () => {
		const detail = query.data;
		if (!path || !detail?.fileFingerprint || saving) return;
		setSaving(true);
		setSaveError("");
		try {
			const saved = await updateSessionWorkspaceFile({
				content: draft,
				expectedFileFingerprint: detail.fileFingerprint,
				path,
				sessionId,
			});
			queryClient.setQueryData(sessionWorkspaceFileQueryKey(sessionId, path, scope, commitSha), saved);
			await queryClient.invalidateQueries({
				predicate: ({ queryKey }) => [
					"session-workspace-files",
					"session-workspace-tree",
					"session-workspace-search",
					"session-workspace-file-revision",
					"session-workspace-diffs",
				].includes(String(queryKey[0])),
			});
			setEditing(false);
			setDraft("");
		} catch (error) {
			setSaveError(error instanceof Error ? error.message : t("files.saveError"));
		} finally {
			setSaving(false);
		}
	}, [commitSha, draft, path, query.data, queryClient, saving, scope, sessionId, t]);
	useEffect(() => {
		if (!editing) return;
		const onSaveShortcut = (event: KeyboardEvent) => {
			if (
				event.key.toLowerCase() !== "s"
				|| (!event.metaKey && !event.ctrlKey)
				|| event.altKey
				|| event.shiftKey
			) return;
			event.preventDefault();
			if (!saving && hasUnsavedChanges) void saveEditing();
		};
		window.addEventListener("keydown", onSaveShortcut, true);
		return () => window.removeEventListener("keydown", onSaveShortcut, true);
	}, [editing, hasUnsavedChanges, saveEditing, saving]);
	const refetch = query.refetch;

	if (!path) {
		return <PanelMessage>{t("files.explorer.selectFile")}</PanelMessage>;
	}
	if (query.isPending) {
		return <PanelMessage>{t("files.loadingDiff")}</PanelMessage>;
	}
	if (query.error) {
		return (
			<PanelMessage action={<RetryButton onClick={() => void refetch()} />}>
				{query.error.message || t("files.error.loadFile")}
			</PanelMessage>
		);
	}
	if (!query.data) {
		return (
			<PanelMessage action={<RetryButton onClick={() => void refetch()} />}>
				{t("files.error.loadFile")}
			</PanelMessage>
		);
	}

	const detail = query.data;
	const renderedAvailable = canRenderMarkdown(path, detail);
	const hasDisplayModeChoice = detail.status !== "unmodified" || renderedAvailable;
	const fileName = path.split("/").pop() || path;
	const editable = detail.editable && Boolean(detail.fileFingerprint);
	const effectiveMode =
		(detail.status === "unmodified" && mode === "diff") || (mode === "rendered" && !renderedAvailable)
			? "file"
			: mode;
	const fileView = sourceHighlightReady ? (
		<CompleteFileView
			annotation={annotation}
			detail={detail}
			editing={editing && effectiveMode === "file"}
			onEditChange={setDraft}
			scope={scope}
			sessionId={sessionId}
			commitSha={commitSha}
			source={source}
		/>
	) : <PanelMessage>{t("files.loading")}</PanelMessage>;
	const beginEditing = () => {
		setMode("file");
		annotation.cancel();
		setDraft(detail.content);
		setSaveError("");
		setEditing(true);
	};
	const cancelEditing = () => {
		setEditing(false);
		setDraft("");
		setSaveError("");
	};
	const unsavedIndicator = hasUnsavedChanges && !onDirtyChange ? (
		<span
			aria-hidden="true"
			className="size-2 shrink-0 rounded-full bg-foreground"
			data-testid="unsaved-file-indicator"
		/>
	) : null;
	const wholeFileAnnotationActive = annotation.target?.surface !== "review"
		&& annotation.target?.path === detail.path
		&& annotation.target.side === "file"
		&& annotation.target.line == null;
	const compactModeButton = (mode: FileViewMode, label: string, icon: React.ReactNode) => (
		<Tooltip key={mode}>
			<TooltipTrigger asChild>
				<Button
					aria-label={label}
					aria-selected={effectiveMode === mode}
					className={cn("text-muted-foreground hover:text-foreground", effectiveMode === mode && "bg-interactive-active text-foreground")}
					disabled={editing}
					onClick={() => setMode(mode)}
					role="tab"
					size="icon-sm"
					type="button"
					variant="ghost"
				>
					{icon}
				</Button>
			</TooltipTrigger>
			<TooltipContent side="bottom">{label}</TooltipContent>
		</Tooltip>
	);
	const pathSegments = detail.path.split("/");
	const compactTabs = (
		<div className="sticky top-0 z-20 flex min-h-9 items-center gap-2 border-b border-border bg-background px-3 py-1">
			<nav aria-label={t("files.filePath")} className="flex min-w-0 flex-1 items-center gap-1.5" title={detail.path}>
				<WorkspaceEntryIcon className="size-icon-base" kind="file" name={fileName} />
				<span className="flex min-w-0 items-center text-xs">
					{pathSegments.slice(0, -1).map((segment, index) => (
						<span className="flex min-w-0 shrink items-center text-muted-foreground" key={`${index}:${segment}`}>
							<span className="truncate">{segment}</span>
							<span aria-hidden="true" className="px-1 text-passive">/</span>
						</span>
					))}
					<span className="shrink-0 truncate text-foreground">{fileName}</span>
				</span>
				{unsavedIndicator}
				{detail.status !== "unmodified" ? (
					<span className="ml-1.5 flex shrink-0 items-center gap-1.5 font-mono text-xs tabular-nums">
						<span className="text-success">+{detail.additions}</span>
						<span className="text-error">−{detail.deletions}</span>
					</span>
				) : null}
			</nav>
			{/* Mode switches and file actions share one row and one gap. */}
			<div className="flex shrink-0 items-center gap-0.5">
				{hasDisplayModeChoice ? (
					<div aria-label={t("files.fileDisplayMode")} className="flex shrink-0 items-center gap-0.5" role="tablist">
						{detail.status !== "unmodified" ? compactModeButton("diff", t("files.diff"), <GitCompareArrows aria-hidden="true" className="size-icon-sm" />) : null}
						{compactModeButton("file", t("files.fileView"), <FileCode2 aria-hidden="true" className="size-icon-sm" />)}
						{renderedAvailable ? compactModeButton("rendered", t("files.rendered"), <Eye aria-hidden="true" className="size-icon-sm" />) : null}
					</div>
				) : null}
				{editing ? (
					<div className="flex shrink-0 items-center gap-1">
						<Button aria-label={t("files.cancelEditing")} className={EDIT_ACTION_CLASS} disabled={saving} onClick={cancelEditing} size="sm" type="button" variant="ghost"><X aria-hidden="true" className="size-icon-sm" />{t("files.cancelEditing")}</Button>
						<Button aria-label={t("files.saveFile")} className={EDIT_ACTION_CLASS} disabled={saving || !hasUnsavedChanges} onClick={() => void saveEditing()} size="sm" type="button" variant="primary">{saving ? <LoaderCircle aria-hidden="true" className="size-icon-sm animate-spin" /> : <Save aria-hidden="true" className="size-icon-sm" />}{t("files.saveFile")}</Button>
					</div>
				) : (
					<div className="flex shrink-0 items-center gap-0.5">
						{editable ? (
							<Tooltip>
								<TooltipTrigger asChild><Button aria-label={t("files.editFile")} className="text-muted-foreground hover:text-foreground" onClick={beginEditing} size="icon-sm" type="button" variant="ghost"><Pencil aria-hidden="true" className="size-icon-sm" /></Button></TooltipTrigger>
								<TooltipContent side="bottom">{t("files.editFile")}</TooltipContent>
							</Tooltip>
						) : null}
						<Tooltip>
							<TooltipTrigger asChild>
								<Button
									aria-label={t("files.addFeedback")}
									className="text-muted-foreground hover:text-foreground"
									onClick={() => annotation.begin({ path: detail.path, previousPath: detail.previousPath, side: "file", scope, surface: "focused", workspaceVersion: detail.workspaceVersion, fileFingerprint: detail.fileFingerprint })}
									size="icon-sm"
									type="button"
									variant="ghost"
								>
									<MessageSquarePlus aria-hidden="true" className="size-icon-sm" />
								</Button>
							</TooltipTrigger>
							<TooltipContent side="bottom">{t("files.addFeedback")}</TooltipContent>
						</Tooltip>
					</div>
				)}
			</div>
			{wholeFileAnnotationActive ? (
				<div className="absolute right-2 top-full z-50 w-[min(32rem,calc(100%-1rem))] overflow-hidden rounded-md border border-border bg-surface shadow-xl">
					<FileAnnotationComposer annotation={annotation} />
				</div>
			) : null}
		</div>
	);
	// Both the Files panel and a centre file tab use the compact icon toolbar.
	const toolbarNode = compactTabs;

	if (detail.status !== "unmodified") {
		const fallback = (
			<ReviewDiffBody
				annotation={annotation}
				detail={detail}
				detailLoadedAt={query.dataUpdatedAt}
				emptyFallback={
					!detail.binary && !detail.contentTruncated && !detail.deleted && detail.content ? (
						<ReadOnlyFileView annotation={annotation} detail={detail} scope={scope} sessionId={sessionId} />
					) : undefined
				}
				filePath={path}
				onActiveSelectionChange={setSelectionOrMenuActive}
				sessionId={sessionId}
				split={split && canSplitCompare(detail.status)}
				wrap
			/>
		);
		return (
			<div className="relative min-w-0">
				{toolbarNode}
				<EditProvider createEditor={createReviewEditor}>
				{effectiveMode === "diff" ? (
					<AoDiffFile
						annotation={annotation}
						detail={detail}
						fallback={fallback}
						onActiveSelectionChange={setSelectionOrMenuActive}
						scope={scope}
						sessionId={sessionId}
						split={split && canSplitCompare(detail.status)}
						commitSha={commitSha}
						source={source}
						// Files panel preview: the compact toolbar already shows the name
						// and counts, and its filler gutters sit on the canvas.
						extraCSS={AO_PIERRE_FILES_REVIEW_CSS}
						hideFileHeader
					/>
				) : effectiveMode === "rendered" && renderedAvailable ? (
					<MarkdownFileView content={detail.content} filePath={path} sessionId={sessionId} truncated={detail.contentTruncated} version={query.dataUpdatedAt} />
				) : (
					fileView
				)}
				</EditProvider>
				{saveError ? <p className="border-t border-error/40 bg-error/10 px-3 py-2 text-xs text-error" role="alert">{saveError}</p> : null}
			</div>
		);
	}
	return (
		<div className="relative min-w-0">
			{toolbarNode}
			<EditProvider createEditor={createReviewEditor}>
			{effectiveMode === "rendered" && renderedAvailable ? (
				<MarkdownFileView content={detail.content} filePath={path} sessionId={sessionId} truncated={detail.contentTruncated} version={query.dataUpdatedAt} />
			) : fileView}
			</EditProvider>
			{saveError ? <p className="border-t border-error/40 bg-error/10 px-3 py-2 text-xs text-error" role="alert">{saveError}</p> : null}
		</div>
	);
}

function CompleteFileView({ annotation, commitSha, detail, editing, onEditChange, scope, sessionId, source }: { annotation: FileAnnotationModel; commitSha?: string; detail: WorkspaceFileDetail; editing: boolean; onEditChange: (content: string) => void; scope: WorkspaceDiffScope; sessionId: string; source: FilesSource }) {
	const { t } = useTranslation();
	const revision = useQuery({
		...sessionSourceFileRevisionQueryOptions({ commitSha, path: detail.path, scope, sessionId, side: detail.deleted ? "before" : "after", source, workspaceVersion: detail.workspaceVersion }),
		enabled: detail.deleted || detail.contentTruncated,
	});
	if (revision.isPending && revision.isFetching) return <PanelMessage>{t("files.loading")}</PanelMessage>;
	if (revision.error) return <PanelMessage>{revision.error.message}</PanelMessage>;
	if (revision.data) {
		if (!revision.data.exists) return <PanelMessage>{t("files.error.loadFile")}</PanelMessage>;
		return (
			<ReadOnlyFileView
				annotation={annotation}
				detail={{
					...detail,
					binary: revision.data.binary,
					content: revision.data.content,
					contentTruncated: revision.data.truncated,
					deleted: false,
					size: revision.data.size,
				}}
				editing={editing}
				onEditChange={onEditChange}
				sessionId={sessionId}
				side={detail.deleted ? "before" : "after"}
				scope={scope}
			/>
		);
	}
	return <ReadOnlyFileView annotation={annotation} detail={detail} editing={editing} onEditChange={onEditChange} scope={scope} sessionId={sessionId} />;
}
