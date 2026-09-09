import { useCallback, useMemo, useRef, useState, type ReactNode } from "react";
import { parsePatchFiles, type FileDiffMetadata, type SelectedLineRange } from "@pierre/diffs";
import { FileDiff } from "@pierre/diffs/react";
import { useTranslation } from "react-i18next";
import type { DiffSelectionLine } from "../../../shared/diff-selection";
import { fetchWorkspaceFileRevision, type WorkspaceDiffScope, type WorkspaceFileDetail } from "../../hooks/useSessionWorkspaceFiles";
import { parseUnifiedDiff, type DiffRow } from "../../lib/diff-parser";
import { useUiStore } from "../../stores/ui-store";
import { DiffSelectionMenu } from "../DiffSelectionMenu";
import { FileAnnotationComposer, type FileAnnotationModel } from "../WorkspaceDiffView";

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

function selectionLines(rows: DiffRow[], range: SelectedLineRange): DiffSelectionLine[] {
	const start = Math.min(range.start, range.end);
	const end = Math.max(range.start, range.end);
	const side = range.side ?? "additions";
	return rows.flatMap((row) => {
		if (row.kind === "hunk") return [];
		const line = side === "deletions" ? row.oldNo : row.newNo;
		if (line === null || line < start || line > end) return [];
		return [{ kind: row.kind, oldNo: row.oldNo, newNo: row.newNo, text: row.text } satisfies DiffSelectionLine];
	});
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
	wrap,
}: {
	annotation: FileAnnotationModel;
	detail: WorkspaceFileDetail;
	fallback: ReactNode;
	onActiveSelectionChange: (active: boolean) => void;
	scope?: WorkspaceDiffScope;
	sessionId: string;
	split: boolean;
	wrap: boolean;
}) {
	const { t } = useTranslation();
	const resolvedTheme = useUiStore((state) => state.resolvedTheme);
	const containerRef = useRef<HTMLDivElement>(null);
	const metadata = useMemo(() => cachedMetadata(detail), [detail]);
	const rows = useMemo(() => parseUnifiedDiff(detail.diff), [detail.diff]);
	const [selection, setSelection] = useState<SelectedLineRange | null>(null);
	const [menuOpen, setMenuOpen] = useState(false);

	const selectedLines = useMemo(() => (selection ? selectionLines(rows, selection) : []), [rows, selection]);
	const selectedText = useMemo(() => selectedLines.map((line) => line.text).join("\n"), [selectedLines]);
	const menuPosition = useMemo(() => {
		const rect = containerRef.current?.getBoundingClientRect();
		return rect ? { x: Math.max(rect.left + 12, rect.right - 250), y: rect.top + 44 } : { x: 24, y: 80 };
	}, [menuOpen]);

	const loadDiffFiles = useCallback(
		async (fileDiff: FileDiffMetadata) => {
			const [before, after] = await Promise.all([
				fetchWorkspaceFileRevision({ sessionId, path: detail.path, scope, side: "before", workspaceVersion: detail.workspaceVersion }),
				fetchWorkspaceFileRevision({ sessionId, path: detail.path, scope, side: "after", workspaceVersion: detail.workspaceVersion }),
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
		[detail.path, detail.previousPath, detail.workspaceVersion, scope, sessionId, t],
	);

	if (!metadata) return <>{fallback}</>;

	return (
		<div className="ao-pierre-diff relative min-w-0" ref={containerRef}>
			<FileDiff
				disableWorkerPool={typeof Worker === "undefined"}
				fileDiff={metadata}
				options={{
					collapsedContextThreshold: 8,
					controlledSelection: true,
					diffIndicators: "classic",
					diffStyle: split ? "split" : "unified",
					enableLineSelection: true,
					expansionLineCount: 20,
					hunkSeparators: "line-info",
					lineDiffType: "word-alt",
					loadDiffFiles,
					maxLineDiffLength: 400,
					onLineNumberClick: (event) => {
						const side = event.annotationSide;
						const { row, rowIndex } = rowForLine(rows, side, event.lineNumber);
						if (!row || row.kind === "hunk") return;
						annotation.begin({
							path: detail.path,
							previousPath: detail.previousPath,
							side: side === "deletions" ? "old" : "new",
							line: event.lineNumber,
							oldLine: row.oldNo ?? undefined,
							newLine: row.newNo ?? undefined,
							lineKind: row.kind,
							lineText: row.text,
							rowIndex,
							scope,
							workspaceVersion: detail.workspaceVersion,
							fileFingerprint: detail.fileFingerprint,
						});
					},
					onLineSelectionEnd: (next) => {
						setSelection(next);
						setMenuOpen(Boolean(next));
						onActiveSelectionChange(Boolean(next));
					},
					onLineSelected: (next) => setSelection(next),
					overflow: wrap ? "wrap" : "scroll",
					theme: { dark: "github-dark", light: "github-light" },
					themeType: resolvedTheme,
					tokenizeMaxLength: 200_000,
					tokenizeMaxLineLength: 2_000,
				}}
				selectedLines={selection}
			/>
			{detail.diffTruncated ? (
				<div className="border-t border-border bg-warning/10 px-3 py-1.5 text-xs text-warning">
					{t("files.diffTruncated")}
				</div>
			) : null}
			{annotation.target?.path === detail.path ? <FileAnnotationComposer annotation={annotation} /> : null}
			<DiffSelectionMenu
				filePath={detail.path}
				fileFingerprint={detail.fileFingerprint}
				lines={selectedLines}
				onOpenChange={(open) => {
					setMenuOpen(open);
					if (!open) {
						setSelection(null);
						onActiveSelectionChange(false);
					}
				}}
				open={menuOpen && selectedLines.length > 0}
				position={menuPosition}
				selectedText={selectedText}
				sessionId={sessionId}
				scope={scope}
				workspaceVersion={detail.workspaceVersion}
			/>
		</div>
	);
}
