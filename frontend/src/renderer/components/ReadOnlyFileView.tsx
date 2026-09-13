import { useId, useRef } from "react";
import { useTranslation } from "react-i18next";
import { type FileContents, type LineAnnotation } from "@pierre/diffs";
import { File } from "@pierre/diffs/react";
import { getApiBaseUrl } from "../lib/api-client";
import type { WorkspaceDiffScope, WorkspaceFileDetail } from "../hooks/useSessionWorkspaceFiles";
import { useUiStore } from "../stores/ui-store";
import { FileAnnotationComposer, LineFeedbackButtonControl, PanelMessage, type FileAnnotationModel } from "./WorkspaceDiffView";
import { AO_PIERRE_SURFACE_CSS } from "./diffs/pierreTheme";
import { usePersistentGutterUtility } from "./diffs/usePersistentGutterUtility";

function formatBytes(bytes: number): string {
	if (bytes < 1024) return `${bytes} B`;
	if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
	return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function workspaceRawImageUrl(sessionId: string, path: string, side: "before" | "after"): string {
	const query = new URLSearchParams({ path, side });
	return `${getApiBaseUrl()}/api/v1/sessions/${encodeURIComponent(sessionId)}/workspace/file/blob?${query}`;
}

// Renders an untouched (unmodified) workspace file: an agent didn't write
// this one, so there's no diff to show, just its current content. Binary and
// oversized files short-circuit before any tokenization is attempted.
export function ReadOnlyFileView({
	annotation,
	detail,
	editing = false,
	onEditChange,
	scope = "combined",
	sessionId,
	side = "after",
}: {
	annotation: FileAnnotationModel;
	detail: WorkspaceFileDetail;
	editing?: boolean;
	onEditChange?: (content: string) => void;
	scope?: WorkspaceDiffScope;
	sessionId: string;
	side?: "before" | "after";
}) {
	const { t } = useTranslation();
	const resolvedTheme = useUiStore((state) => state.resolvedTheme);
	const containerRef = useRef<HTMLDivElement>(null);
	const editorInstanceId = useId();
	const gutterHover = usePersistentGutterUtility(containerRef);
	if (detail.binary) {
		if (detail.imageMediaType) {
			return (
				<div className="grid place-items-center p-3">
					<img
						alt={detail.path}
						className="max-h-[70vh] max-w-full object-contain"
						src={workspaceRawImageUrl(sessionId, detail.path, side)}
					/>
				</div>
			);
		}
		return <PanelMessage>{t("files.binaryUnavailable")}</PanelMessage>;
	}
	if (detail.contentTruncated) {
		return <PanelMessage>{t("files.explorer.tooLarge", { size: formatBytes(detail.size) })}</PanelMessage>;
	}
	const activeLine = annotation.target?.surface !== "review" && annotation.target?.path === detail.path && annotation.target.side === "file"
		? annotation.target.line
		: undefined;
	const lineAnnotations: LineAnnotation<"feedback">[] | undefined = activeLine != null
		? [{ lineNumber: activeLine, metadata: "feedback" }]
		: undefined;
	const file: FileContents = {
		name: detail.path,
		contents: detail.content,
		cacheKey: detail.fileFingerprint ?? detail.workspaceVersion ?? `${detail.path}:${detail.size}`,
	};
	const beginLineAnnotation = (line: number) => {
		annotation.begin({
			path: detail.path,
			previousPath: detail.previousPath,
			side: "file",
			line,
			newLine: side === "after" ? line : undefined,
			oldLine: side === "before" ? line : undefined,
			lineKind: "context",
			lineText: detail.content.replace(/\n$/, "").split("\n")[line - 1] ?? "",
			scope,
			surface: "focused",
			workspaceVersion: detail.workspaceVersion,
			fileFingerprint: detail.fileFingerprint,
		});
	};
	return (
		<div
			className="ao-pierre-surface min-w-0 select-text"
			data-editing={editing || undefined}
			onPointerLeave={gutterHover.onPointerLeave}
			onPointerMove={gutterHover.onPointerMove}
			ref={containerRef}
		>
			<File<"feedback">
				disableWorkerPool={typeof Worker === "undefined"}
				edit={editing}
				editStateKey={`${sessionId}:${detail.path}:file:${editorInstanceId}`}
				editorOptions={{
					onAttach: (editor) => requestAnimationFrame(() => editor.focus({ lineNumber: "first-visible" })),
					ownsVerticalViewport: true,
				}}
				file={file}
				lineAnnotations={lineAnnotations}
				options={{
					disableFileHeader: true,
					enableGutterUtility: true,
					lineHoverHighlight: "line",
					onPostRender: gutterHover.restoreAfterRender,
					overflow: "wrap",
					theme: { dark: "github-dark", light: "github-light" },
					themeType: resolvedTheme,
					tokenizeMaxLength: 200_000,
					tokenizeMaxLineLength: 2_000,
					unsafeCSS: AO_PIERRE_SURFACE_CSS,
				}}
				renderAnnotation={() => <FileAnnotationComposer annotation={annotation} />}
				onEditChange={(event) => onEditChange?.(event.file.contents)}
				onEditComplete={() => "reject"}
				renderGutterUtility={(getHoveredLine) => (
					<LineFeedbackButtonControl gutter label={t("files.addFeedback")} onClick={() => {
						const line = getHoveredLine();
						if (line) beginLineAnnotation(line.lineNumber);
					}} />
				)}
			/>
		</div>
	);
}
