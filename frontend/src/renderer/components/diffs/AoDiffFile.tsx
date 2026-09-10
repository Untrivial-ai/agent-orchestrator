import { useCallback, useEffect, useMemo, useRef, type ReactNode } from "react";
import { parsePatchFiles, type DiffLineAnnotation, type FileDiffMetadata } from "@pierre/diffs";
import { FileDiff } from "@pierre/diffs/react";
import { useTranslation } from "react-i18next";
import { fetchWorkspaceFileRevision, type WorkspaceDiffScope, type WorkspaceFileDetail } from "../../hooks/useSessionWorkspaceFiles";
import { parseUnifiedDiff, type DiffRow } from "../../lib/diff-parser";
import { useUiStore } from "../../stores/ui-store";
import { FileAnnotationComposer, LineFeedbackButtonControl, type FileAnnotationModel } from "../WorkspaceDiffView";
import { AO_PIERRE_SURFACE_CSS } from "./pierreTheme";
import { usePersistentGutterUtility } from "./usePersistentGutterUtility";

const metadataCache = new Map<string, FileDiffMetadata>();
const MAX_METADATA_CACHE_ENTRIES = 100;

function cachedMetadata(detail: WorkspaceFileDetail): FileDiffMetadata | null {
	if (!detail.diff) return null;
	const key = `${detail.fileFingerprint}:${detail.diff}`;
	const existing = metadataCache.get(key);
	if (existing) return existing;
	try {
		const parsed = parsePatchFiles(detail.diff, detail.fileFingerprint || detail.path, true);
		const metadata = parsed.flatMap((patch) => patch.files)[0] ?? null;
		if (!metadata) return null;
		metadataCache.set(key, metadata);
		if (metadataCache.size > MAX_METADATA_CACHE_ENTRIES) {
			const oldest = metadataCache.keys().next().value;
			if (oldest) metadataCache.delete(oldest);
		}
		return metadata;
	} catch {
		return null;
	}
}

function rowForLine(rows: DiffRow[], side: "deletions" | "additions", lineNumber: number) {
	const rowIndex = rows.findIndex((row) => (side === "deletions" ? row.oldNo : row.newNo) === lineNumber);
	return { row: rowIndex >= 0 ? rows[rowIndex] : undefined, rowIndex };
}

export function AoDiffFile({
	annotation,
	detail,
	fallback,
	onActiveSelectionChange,
	scope = "combined",
	sessionId,
	split,
	commitSha,
}: {
	annotation: FileAnnotationModel;
	detail: WorkspaceFileDetail;
	fallback: ReactNode;
	onActiveSelectionChange: (active: boolean) => void;
	scope?: WorkspaceDiffScope;
	sessionId: string;
	split: boolean;
	commitSha?: string;
}) {
	const { t } = useTranslation();
	const resolvedTheme = useUiStore((state) => state.resolvedTheme);
	const containerRef = useRef<HTMLDivElement>(null);
	const gutterHover = usePersistentGutterUtility(containerRef);
	const metadata = useMemo(() => cachedMetadata(detail), [detail]);
	const rows = useMemo(() => parseUnifiedDiff(detail.diff), [detail.diff]);
	const activeTarget = annotation.target?.surface !== "review" && annotation.target?.path === detail.path && annotation.target.side !== "file" ? annotation.target : null;
	const lineAnnotations: DiffLineAnnotation<"feedback">[] | undefined = activeTarget?.line != null
		? [{ lineNumber: activeTarget.line, side: activeTarget.side === "old" ? "deletions" : "additions", metadata: "feedback" }]
		: undefined;

	useEffect(() => {
		const onSelectionChange = () => {
			const selection = window.getSelection();
			onActiveSelectionChange(Boolean(selection && !selection.isCollapsed && selection.anchorNode && containerRef.current?.contains(selection.anchorNode)));
		};
		document.addEventListener("selectionchange", onSelectionChange);
		return () => {
			document.removeEventListener("selectionchange", onSelectionChange);
			onActiveSelectionChange(false);
		};
	}, [onActiveSelectionChange]);

	const loadDiffFiles = useCallback(
		async (fileDiff: FileDiffMetadata) => {
			const [before, after] = await Promise.all([
				fetchWorkspaceFileRevision({ commitSha, sessionId, path: detail.path, scope, side: "before", workspaceVersion: detail.workspaceVersion }),
				fetchWorkspaceFileRevision({ commitSha, sessionId, path: detail.path, scope, side: "after", workspaceVersion: detail.workspaceVersion }),
			]);
			if (before.binary || after.binary || before.truncated || after.truncated) {
				throw new Error(t("files.explorer.tooLarge", { size: Math.max(before.size, after.size) }));
			}
			const newFile = { name: detail.path, contents: after.content, cacheKey: after.revision };
			if (fileDiff.type === "rename-pure") return { oldFile: null, newFile };
			return {
				oldFile: { name: detail.previousPath || detail.path, contents: before.content, cacheKey: before.revision },
				newFile,
			};
		},
		[commitSha, detail.path, detail.previousPath, detail.workspaceVersion, scope, sessionId, t],
	);
	const beginLineAnnotation = useCallback((side: "deletions" | "additions", lineNumber: number) => {
		const { row, rowIndex } = rowForLine(rows, side, lineNumber);
		if (!row || row.kind === "hunk") return;
		annotation.begin({
			path: detail.path,
			previousPath: detail.previousPath,
			side: side === "deletions" ? "old" : "new",
			line: lineNumber,
			oldLine: row.oldNo ?? undefined,
			newLine: row.newNo ?? undefined,
			lineKind: row.kind,
			lineText: row.text,
			rowIndex,
			scope,
			surface: "focused",
			workspaceVersion: detail.workspaceVersion,
			fileFingerprint: detail.fileFingerprint,
		});
	}, [annotation, detail.fileFingerprint, detail.path, detail.previousPath, detail.workspaceVersion, rows, scope]);

	if (!metadata) return <>{fallback}</>;

	return (
		<div
			className="ao-pierre-surface relative min-w-0 select-text"
			onPointerLeave={gutterHover.onPointerLeave}
			onPointerMove={gutterHover.onPointerMove}
			ref={containerRef}
		>
			<FileDiff
				disableWorkerPool={typeof Worker === "undefined"}
				fileDiff={metadata}
				lineAnnotations={lineAnnotations}
				options={{
					collapsedContextThreshold: 8,
					diffIndicators: "classic",
					diffStyle: split ? "split" : "unified",
					enableGutterUtility: true,
					expansionLineCount: 20,
					hunkSeparators: "line-info",
					lineDiffType: "word-alt",
					loadDiffFiles,
					maxLineDiffLength: 400,
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
				renderGutterUtility={(getHoveredLine) => (
					<LineFeedbackButtonControl
						gutter
						label={t("files.addFeedback")}
						onClick={() => {
							const line = getHoveredLine();
							if (line) beginLineAnnotation(line.side, line.lineNumber);
						}}
					/>
				)}
			/>
			{detail.diffTruncated ? (
				<div className="border-t border-border bg-warning/10 px-3 py-1.5 text-xs text-warning">
					{t("files.diffTruncated")}
				</div>
			) : null}
		</div>
	);
}
