import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
	sessionWorkspaceFileQueryOptions,
	sessionWorkspaceFileRevisionQueryOptions,
	type WorkspaceDiffScope,
	type WorkspaceFileDetail,
} from "../hooks/useSessionWorkspaceFiles";
import {
	canSplitCompare,
	PanelMessage,
	ReviewDiffBody,
	RetryButton,
	type FileAnnotationModel,
} from "./WorkspaceDiffView";
import { ReadOnlyFileView } from "./ReadOnlyFileView";
import { AoDiffFile } from "./diffs/AoDiffFile";
import { Button } from "./ui/button";
import { MarkdownFileView } from "./markdown/MarkdownFileView";

type FileViewMode = "diff" | "file" | "rendered";

function canRenderMarkdown(path: string, detail: WorkspaceFileDetail): boolean {
	return !detail.deleted && !detail.binary && !detail.contentTruncated && /\.(md|markdown)$/i.test(path);
}

export function FileContentPane({
	annotation,
	path,
	sessionId,
	split,
	wrap,
	scope = "combined",
}: {
	annotation: FileAnnotationModel;
	path: string | null;
	sessionId: string;
	split: boolean;
	wrap: boolean;
	scope?: WorkspaceDiffScope;
}) {
	const { t } = useTranslation();
	const [mode, setMode] = useState<FileViewMode>("diff");
	// Mirrors WorkspaceDiffView's own guard: a background refetch mid-selection
	// would re-render the pane out from under an active text selection or its
	// context menu.
	const [selectionOrMenuActive, setSelectionOrMenuActive] = useState(false);
	const query = useQuery({
		...sessionWorkspaceFileQueryOptions(sessionId, path ?? "", t("files.error.loadWorkspaceFile"), scope),
		enabled: Boolean(path) && !selectionOrMenuActive,
	});
	useEffect(() => setMode("diff"), [path, scope]);
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
	if (detail.status !== "unmodified") {
		const fallback = (
			<ReviewDiffBody
				annotation={annotation}
				detail={detail}
				detailLoadedAt={query.dataUpdatedAt}
				emptyFallback={
					!detail.binary && !detail.contentTruncated && !detail.deleted && detail.content ? (
						<ReadOnlyFileView detail={detail} sessionId={sessionId} />
					) : undefined
				}
				filePath={path}
				onActiveSelectionChange={setSelectionOrMenuActive}
				sessionId={sessionId}
				split={split && canSplitCompare(detail.status)}
				wrap={wrap}
			/>
		);
		const renderedAvailable = canRenderMarkdown(path, detail);
		return (
			<div className="min-w-0">
				<div className="sticky top-0 z-20 flex h-9 items-center gap-1 border-b border-border bg-surface px-2">
					<Button aria-pressed={mode === "diff"} onClick={() => setMode("diff")} size="sm" type="button" variant={mode === "diff" ? "secondary" : "ghost"}>
						{t("files.diff")}
					</Button>
					<Button aria-pressed={mode === "file"} onClick={() => setMode("file")} size="sm" type="button" variant={mode === "file" ? "secondary" : "ghost"}>
						{t("files.fileView")}
					</Button>
					{renderedAvailable ? (
						<Button aria-pressed={mode === "rendered"} onClick={() => setMode("rendered")} size="sm" type="button" variant={mode === "rendered" ? "secondary" : "ghost"}>
							{t("files.rendered")}
						</Button>
					) : null}
				</div>
				{mode === "diff" ? (
					<AoDiffFile
						annotation={annotation}
						detail={detail}
						fallback={fallback}
						onActiveSelectionChange={setSelectionOrMenuActive}
						scope={scope}
						sessionId={sessionId}
						split={split && canSplitCompare(detail.status)}
						wrap={wrap}
					/>
				) : mode === "rendered" && renderedAvailable ? (
					<MarkdownFileView content={detail.content} filePath={path} sessionId={sessionId} truncated={detail.contentTruncated} version={query.dataUpdatedAt} />
				) : (
					<CompleteFileView detail={detail} scope={scope} sessionId={sessionId} />
				)}
			</div>
		);
	}
	return <ReadOnlyFileView detail={detail} sessionId={sessionId} />;
}

function CompleteFileView({ detail, scope, sessionId }: { detail: WorkspaceFileDetail; scope: WorkspaceDiffScope; sessionId: string }) {
	const { t } = useTranslation();
	const revision = useQuery({
		...sessionWorkspaceFileRevisionQueryOptions({ path: detail.path, scope, sessionId, side: detail.deleted ? "before" : "after", workspaceVersion: detail.workspaceVersion }),
		enabled: detail.deleted || detail.contentTruncated,
	});
	if (revision.isPending && revision.isFetching) return <PanelMessage>{t("files.loading")}</PanelMessage>;
	if (revision.error) return <PanelMessage>{revision.error.message}</PanelMessage>;
	if (revision.data) {
		if (!revision.data.exists) return <PanelMessage>{t("files.error.loadFile")}</PanelMessage>;
		return (
			<ReadOnlyFileView
				detail={{
					...detail,
					binary: revision.data.binary,
					content: revision.data.content,
					contentTruncated: revision.data.truncated,
					deleted: false,
					size: revision.data.size,
				}}
				sessionId={sessionId}
				side={detail.deleted ? "before" : "after"}
			/>
		);
	}
	return <ReadOnlyFileView detail={detail} sessionId={sessionId} />;
}
