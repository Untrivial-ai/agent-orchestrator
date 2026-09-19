import { parsePatchFiles, type DiffLineAnnotation, type FileDiffMetadata } from "@pierre/diffs";
import { FileDiff } from "@pierre/diffs/react";
import { useCallback, useEffect, useMemo, useRef, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { CloudCpClient, CloudCpWorkspaceReviewFileSummary, CloudCpWorkspaceReviewScope } from "../../lib/cloud-cp";
import { parseUnifiedDiff } from "../../lib/diff-parser";
import { useUiStore } from "../../stores/ui-store";
import { FileAnnotationComposer, LineFeedbackButtonControl, type FileAnnotationModel } from "../WorkspaceDiffView";
import { AO_PIERRE_SURFACE_CSS } from "../diffs/pierreTheme";

function metadataForPath(patch: string, path: string): FileDiffMetadata | null {
	if (!patch) return null;
	try {
		const files = parsePatchFiles(patch, path, true).flatMap((entry) => entry.files);
		return files.find((file) => file.name === path || file.prevName === path) ?? files[0] ?? null;
	} catch {
		return null;
	}
}

export function CloudDiffFile({
	annotation, client, commitSha, fallback, file, onActiveSelectionChange, orgId, patch, scope,
	sessionId, split, workspaceVersion,
}: {
	annotation: FileAnnotationModel;
	baseUrl: string;
	client: CloudCpClient;
	commitSha?: string;
	fallback?: ReactNode;
	file: CloudCpWorkspaceReviewFileSummary;
	onActiveSelectionChange: (active: boolean) => void;
	orgId: string;
	patch: string;
	scope: CloudCpWorkspaceReviewScope;
	sessionId: string;
	split: boolean;
	workspaceVersion: string;
}) {
	const { t } = useTranslation();
	const resolvedTheme = useUiStore((state) => state.resolvedTheme);
	const containerRef = useRef<HTMLDivElement>(null);
	const metadata = useMemo(() => metadataForPath(patch, file.path), [file.path, patch]);
	const rows = useMemo(() => parseUnifiedDiff(patch), [patch]);
	const activeTarget = annotation.target?.surface !== "focused" && annotation.target?.path === file.path && annotation.target.side !== "file" ? annotation.target : null;
	const annotations: DiffLineAnnotation<"feedback">[] | undefined = activeTarget?.line != null
		? [{ lineNumber: activeTarget.line, side: activeTarget.side === "old" ? "deletions" : "additions", metadata: "feedback" }]
		: undefined;
	useEffect(() => {
		const onSelectionChange = () => {
			const selection = window.getSelection();
			onActiveSelectionChange(Boolean(selection && !selection.isCollapsed && selection.anchorNode && containerRef.current?.contains(selection.anchorNode)));
		};
		document.addEventListener("selectionchange", onSelectionChange);
		return () => { document.removeEventListener("selectionchange", onSelectionChange); onActiveSelectionChange(false); };
	}, [onActiveSelectionChange]);
	const loadDiffFiles = useCallback(async (diff: FileDiffMetadata) => {
		const base = { path: file.path, scope, commitSha, workspaceVersion };
		const [before, after] = await Promise.all([
			client.getWorkspaceReviewRevision(orgId, sessionId, { ...base, side: "before" }),
			client.getWorkspaceReviewRevision(orgId, sessionId, { ...base, side: "after" }),
		]);
		if (before.binary || after.binary || before.truncated || after.truncated) throw new Error(t("files.error.loadFile"));
		const newFile = { name: file.path, contents: after.content, cacheKey: after.revision ?? workspaceVersion };
		if (diff.type === "rename-pure") return { oldFile: null, newFile };
		return { oldFile: { name: file.previousPath ?? file.path, contents: before.content, cacheKey: before.revision ?? workspaceVersion }, newFile };
	}, [client, commitSha, file.path, file.previousPath, orgId, scope, sessionId, t, workspaceVersion]);
	const beginLineAnnotation = useCallback((side: "deletions" | "additions", line: number) => {
		const rowIndex = rows.findIndex((row) => (side === "deletions" ? row.oldNo : row.newNo) === line);
		const row = rowIndex >= 0 ? rows[rowIndex] : undefined;
		if (!row || row.kind === "hunk") return;
		annotation.begin({
			path: file.path, previousPath: file.previousPath, side: side === "deletions" ? "old" : "new", line,
			oldLine: row.oldNo ?? undefined, newLine: row.newNo ?? undefined, lineKind: row.kind, lineText: row.text,
			rowIndex, scope, surface: "review", workspaceVersion, fileFingerprint: file.fileFingerprint,
		});
	}, [annotation, file.fileFingerprint, file.path, file.previousPath, rows, scope, workspaceVersion]);

	if (!metadata) return <>{fallback}</>;
	return <div className="ao-pierre-surface relative min-w-0 select-text" ref={containerRef}>
		<FileDiff
			disableWorkerPool={typeof Worker === "undefined"}
			fileDiff={metadata}
			lineAnnotations={annotations}
			options={{
				collapsedContextThreshold: 8, diffIndicators: "classic", diffStyle: split ? "split" : "unified",
				enableGutterUtility: true, expansionLineCount: 20, hunkSeparators: "line-info", lineDiffType: "word-alt",
				lineHoverHighlight: "line", loadDiffFiles, maxLineDiffLength: 400, overflow: "wrap",
				theme: { dark: "github-dark", light: "github-light" }, themeType: resolvedTheme,
				tokenizeMaxLength: 200_000, tokenizeMaxLineLength: 2_000, unsafeCSS: AO_PIERRE_SURFACE_CSS,
			}}
			renderAnnotation={() => <FileAnnotationComposer annotation={annotation} />}
			renderGutterUtility={(getHoveredLine) => <LineFeedbackButtonControl gutter label={t("files.addFeedback")} onClick={() => {
				const line = getHoveredLine();
				if (line) beginLineAnnotation(line.side, line.lineNumber);
			}} />}
		/>
	</div>;
}
