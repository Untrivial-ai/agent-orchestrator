import { useCallback, useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { LoaderCircle, MessageSquarePlus, Pencil, Save, X } from "lucide-react";
import { Editor, type EditorFactory } from "@pierre/diffs/edit";
import { EditProvider } from "@pierre/diffs/react";
import {
	sessionWorkspaceFileQueryKey,
	sessionWorkspaceFileQueryOptions,
	sessionWorkspaceFileRevisionQueryOptions,
	updateSessionWorkspaceFile,
	type WorkspaceDiffScope,
	type WorkspaceFileDetail,
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
import { AoDiffFile } from "./diffs/AoDiffFile";
import { Button } from "./ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { MarkdownFileView } from "./markdown/MarkdownFileView";

export type FileViewMode = "diff" | "file" | "rendered";
export type FileOpenOptions = { commitSha?: string; editing?: boolean; mode?: FileViewMode; scope?: WorkspaceDiffScope };

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
	sessionId,
	split,
	scope = "combined",
}: {
	annotation: FileAnnotationModel;
	initialEditing?: boolean;
	initialMode?: FileViewMode;
	initialRequestKey?: number;
	commitSha?: string;
	onDirtyChange?: (dirty: boolean) => void;
	path: string | null;
	sessionId: string;
	split: boolean;
	scope?: WorkspaceDiffScope;
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
		...sessionWorkspaceFileQueryOptions(sessionId, path ?? "", t("files.error.loadWorkspaceFile"), scope, commitSha),
		enabled: Boolean(path) && !selectionOrMenuActive,
	});
	const hasUnsavedChanges = Boolean(editing && query.data && draft !== query.data.content);
	useEffect(() => {
		setMode(initialMode);
		setEditing(initialEditing);
		setDraft("");
		setSaveError("");
	}, [commitSha, initialEditing, initialMode, initialRequestKey, path, scope]);
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
	const tabs = (
		<div className="sticky top-0 z-20 flex min-h-9 items-center gap-2 border-b border-border bg-surface px-2 py-1">
			{statusLabel[detail.status] ? (
				<span className={cn("shrink-0 font-mono text-2xs font-semibold", statusTone[detail.status])}>
					{statusLabel[detail.status]}
				</span>
			) : null}
			{hasDisplayModeChoice ? <div aria-label={t("files.fileDisplayMode")} className="flex items-center" role="tablist">
				{detail.status !== "unmodified" ? (
					<Button aria-selected={effectiveMode === "diff"} className="h-6 rounded px-2 text-2xs" disabled={editing} onClick={() => setMode("diff")} role="tab" size="sm" type="button" variant={effectiveMode === "diff" ? "secondary" : "ghost"}>
						{t("files.diff")}
					</Button>
				) : null}
				<Button aria-selected={effectiveMode === "file"} className="h-6 rounded px-2 text-2xs" disabled={editing} onClick={() => setMode("file")} role="tab" size="sm" type="button" variant={effectiveMode === "file" ? "secondary" : "ghost"}>
					{t("files.fileView")}{unsavedIndicator}
				</Button>
				{renderedAvailable ? (
					<Button aria-selected={effectiveMode === "rendered"} className="h-6 rounded px-2 text-2xs" disabled={editing} onClick={() => setMode("rendered")} role="tab" size="sm" type="button" variant={effectiveMode === "rendered" ? "secondary" : "ghost"}>
						{t("files.rendered")}
					</Button>
				) : null}
			</div> : (
				<span className="flex min-w-0 items-center gap-1.5 px-2 text-xs text-foreground" title={path}>
					<span className="truncate">{fileName}</span>{unsavedIndicator}
				</span>
			)}
			{editing ? (
				<div className="ml-auto flex items-center gap-1">
					<Button aria-label={t("files.cancelEditing")} disabled={saving} onClick={cancelEditing} size="sm" type="button" variant="ghost"><X aria-hidden="true" />{t("files.cancelEditing")}</Button>
					<Button aria-label={t("files.saveFile")} disabled={saving || !hasUnsavedChanges} onClick={() => void saveEditing()} size="sm" type="button" variant="primary">{saving ? <LoaderCircle aria-hidden="true" className="animate-spin" /> : <Save aria-hidden="true" />}{t("files.saveFile")}</Button>
				</div>
			) : (
				<>
					{editable ? (
						<Tooltip>
							<TooltipTrigger asChild><Button aria-label={t("files.editFile")} className="ml-auto" onClick={beginEditing} size="icon-sm" type="button" variant="ghost"><Pencil aria-hidden="true" /></Button></TooltipTrigger>
							<TooltipContent side="bottom">{t("files.editFile")}</TooltipContent>
						</Tooltip>
					) : <span className="ml-auto" />}
			<Tooltip>
				<TooltipTrigger asChild>
					<Button
						aria-label={t("files.addFeedback")}
						onClick={() => annotation.begin({ path: detail.path, previousPath: detail.previousPath, side: "file", scope, surface: "focused", workspaceVersion: detail.workspaceVersion, fileFingerprint: detail.fileFingerprint })}
						size="icon-sm"
						type="button"
						variant="ghost"
					>
						<MessageSquarePlus aria-hidden="true" />
					</Button>
				</TooltipTrigger>
				<TooltipContent side="bottom">{t("files.addFeedback")}</TooltipContent>
			</Tooltip>
				</>
			)}
			{wholeFileAnnotationActive ? (
				<div className="absolute right-2 top-full z-50 w-[min(32rem,calc(100%-1rem))] overflow-hidden rounded-md border border-border bg-surface shadow-xl">
					<FileAnnotationComposer annotation={annotation} />
				</div>
			) : null}
		</div>
	);

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
				{tabs}
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
			{tabs}
			<EditProvider createEditor={createReviewEditor}>
			{effectiveMode === "rendered" && renderedAvailable ? (
				<MarkdownFileView content={detail.content} filePath={path} sessionId={sessionId} truncated={detail.contentTruncated} version={query.dataUpdatedAt} />
			) : fileView}
			</EditProvider>
			{saveError ? <p className="border-t border-error/40 bg-error/10 px-3 py-2 text-xs text-error" role="alert">{saveError}</p> : null}
		</div>
	);
}

function CompleteFileView({ annotation, commitSha, detail, editing, onEditChange, scope, sessionId }: { annotation: FileAnnotationModel; commitSha?: string; detail: WorkspaceFileDetail; editing: boolean; onEditChange: (content: string) => void; scope: WorkspaceDiffScope; sessionId: string }) {
	const { t } = useTranslation();
	const revision = useQuery({
		...sessionWorkspaceFileRevisionQueryOptions({ commitSha, path: detail.path, scope, sessionId, side: detail.deleted ? "before" : "after", workspaceVersion: detail.workspaceVersion }),
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
