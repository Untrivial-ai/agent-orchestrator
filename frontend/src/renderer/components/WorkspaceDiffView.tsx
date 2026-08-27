import {
	memo,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
	type MouseEvent,
	type ReactNode,
	type RefObject,
} from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { Check, Plus, Send as SendIcon } from "lucide-react";
import type { FileAnnotationTarget } from "../../shared/file-annotations";
import {
	type WorkspaceCompareMode,
	type WorkspaceFileDetail,
	type WorkspaceFileSummary,
} from "../hooks/useSessionWorkspaceFiles";
import { useParsedDiff } from "../hooks/useParsedDiff";
import { useDiffHighlight } from "../hooks/useDiffHighlight";
import { cn } from "../lib/utils";
import { statusLabel, statusTone } from "../lib/workspace-file-status";
import type { DiffRow, DiffRowKind } from "../lib/diff-parser";
import type { DiffRun } from "../lib/diff-highlight";
import type { DiffSelectionLine } from "../../shared/diff-selection";
import { Button } from "./ui/button";
import { DiffSelectionMenu } from "./DiffSelectionMenu";
import { ImageDiffView } from "./ImageDiffView";
import "./chat/code-theme.css";

type WorkspaceFileStatus = WorkspaceFileSummary["status"];

export type ActiveFileAnnotationTarget = FileAnnotationTarget & { rowIndex?: number };
export type FileAnnotationStatus = "idle" | "sending" | "sent" | "error";
export type FileAnnotationModel = {
	target: ActiveFileAnnotationTarget | null;
	draft: string;
	status: FileAnnotationStatus;
	error: string;
	begin: (target: ActiveFileAnnotationTarget) => void;
	setDraft: (draft: string) => void;
	cancel: () => void;
	submit: () => Promise<void>;
};

// Split (old | new) view only means something when both sides have content to
// compare. Added files have nothing on the old side; deleted files have
// nothing on the new side — splitting them just wastes half the pane on an
// empty column, so those always render unified regardless of the toggle.
export function canSplitCompare(status: WorkspaceFileStatus): boolean {
	return status === "modified" || status === "renamed";
}


function emptyDiffMessage(compareMode: WorkspaceCompareMode | undefined, t: TFunction): string {
	return compareMode === "base" ? t("files.noChangesBase") : t("files.noChangesHead");
}


export function ReviewDiffBody({
	annotation,
	detail,
	detailLoadedAt,
	filePath,
	onActiveSelectionChange,
	sessionId,
	split,
	wrap,
}: {
	annotation: FileAnnotationModel;
	detail: WorkspaceFileDetail;
	detailLoadedAt: number;
	filePath: string;
	onActiveSelectionChange: (active: boolean) => void;
	sessionId: string;
	split: boolean;
	wrap: boolean;
}) {
	const { t } = useTranslation();
	const { rows, pending } = useParsedDiff(detail.diff);
	// An image has no readable line diff, so it renders as the images themselves
	// rather than the binary placeholder.
	if (detail.imageMediaType) {
		return (
			<ImageDiffView
				path={detail.path}
				sessionId={sessionId}
				split={split}
				status={detail.status}
				version={detailLoadedAt}
			/>
		);
	}
	if (detail.binary) {
		return <PanelMessage compact>{t("files.binaryUnavailable")}</PanelMessage>;
	}
	if (pending) {
		return <PanelMessage compact>{t("files.loadingDiff")}</PanelMessage>;
	}
	if (rows.length === 0) {
		return <PanelMessage compact>{emptyDiffMessage(detail.compareMode, t)}</PanelMessage>;
	}
	return (
		<DiffView
			annotation={annotation}
			filePath={filePath}
			onActiveSelectionChange={onActiveSelectionChange}
			path={detail.path}
			previousPath={detail.previousPath}
			rows={rows}
			sessionId={sessionId}
			split={split}
			truncated={detail.diffTruncated}
			wrap={wrap}
		/>
	);
}

const diffRowTone: Record<Exclude<DiffRowKind, "hunk">, string> = {
	add: "bg-success/10",
	del: "bg-error/10",
	context: "",
};

const diffMarkerGlyph: Record<Exclude<DiffRowKind, "hunk">, string> = {
	add: "+",
	del: "-",
	context: " ",
};

// isSelectionActiveIn is the shared check used both by the live selectionchange
// listener and the context-menu handler: a real (non-collapsed) selection whose
// anchor lives inside this DiffView's scroll container.
function isSelectionActiveIn(selection: Selection | null, container: HTMLElement | null): boolean {
	if (!selection || !container || selection.isCollapsed) return false;
	return selection.anchorNode !== null && container.contains(selection.anchorNode);
}

// closestDiffRowElement climbs from a Range boundary (which for a text
// selection is almost always a text node, and text nodes have no `.closest`)
// up to the nearest `[data-diff-row]` element, or null if the boundary isn't
// inside a real diff row (e.g. it landed on a hunk band or an empty
// split-view placeholder).
function closestDiffRowElement(node: Node | null): Element | null {
	if (!node) return null;
	const element = node instanceof Element ? node : node.parentElement;
	return element?.closest?.("[data-diff-row]") ?? null;
}

// toDiffSelectionLine maps a DiffRow to the shared DiffSelectionLine shape.
// Hunk rows never carry data-row-index themselves, but a multi-hunk selection
// range can still include one in the middle of the sliced rows[min..max] —
// this drops it defensively rather than emitting a bogus "hunk" line.
function toDiffSelectionLine(row: DiffRow): DiffSelectionLine | null {
	if (row.kind === "hunk") return null;
	return { kind: row.kind, oldNo: row.oldNo, newNo: row.newNo, text: row.text };
}

function isNotNull<T>(value: T | null): value is T {
	return value !== null;
}

type DiffViewMenuState = {
	open: boolean;
	position: { x: number; y: number };
	lines: DiffSelectionLine[];
	selectedText: string;
};

// SessionFilesView has no per-file scroll box — the Files panel is one
// continuous list, and an expanded file's rows scroll as part of that same
// shared container (`[data-files-scroll-root]`). Below the threshold every
// row just renders directly, unchanged from before (the vast majority of
// diffs never pay for any of this). Above it, only the rows actually
// scrolled into view are mounted, via @tanstack/react-virtual attached to
// that SHARED scroll container rather than a container of this file's own —
// `scrollMargin` is this file's row-list's offset from the top of the shared
// scrollable content, since the virtualizer needs that to know which of its
// rows fall inside the current viewport. That offset shifts whenever
// anything above this file changes height (another file expanding or
// collapsing, or gaining/losing its own virtualized rows), so it's
// re-measured via a ResizeObserver on the full file list rather than once.
// Rows wrap (`whitespace-pre-wrap`) and the panel's available width varies a
// lot — full window width when the Files view is maximized, much narrower
// when docked alongside the terminal — so a long line can be one row of
// height in the maximized view and several wrapped rows tall when docked.
// ESTIMATED_ROW_HEIGHT is only the initial guess for a row that hasn't
// rendered yet; `measureElement` corrects it to the row's real height once
// mounted, which is what keeps rows from overlapping when they wrap.
const ROW_VIRTUALIZE_THRESHOLD = 150;
const ESTIMATED_ROW_HEIGHT = 20;

function useSharedScrollRowVirtualizer(
	scrollContainerRef: RefObject<HTMLElement | null>,
	count: number,
	enabled: boolean,
) {
	const listRef = useRef<HTMLDivElement>(null);
	const [scrollElement, setScrollElement] = useState<HTMLElement | null>(null);
	const [scrollMargin, setScrollMargin] = useState(0);

	useEffect(() => {
		if (!enabled) return;
		setScrollElement(scrollContainerRef.current?.closest<HTMLElement>("[data-files-scroll-root]") ?? null);
	}, [enabled, scrollContainerRef]);

	// A regular effect, not useLayoutEffect: "Expand All" mounts many DiffView
	// instances in the same commit, and React runs every layout effect from
	// that commit synchronously, before the browser is allowed to paint
	// anything. A read (getBoundingClientRect) here forces a synchronous
	// layout — N of those back to back, all before first paint, is exactly
	// what made "Expand All" stall. A plain effect still measures almost
	// immediately (one frame later at worst), which is an imperceptible
	// one-time cost for something that self-corrects, in exchange for never
	// blocking paint on how many files just got expanded at once.
	useEffect(() => {
		if (!enabled || !scrollElement || !listRef.current) return;
		const measure = () => {
			if (!listRef.current) return;
			const listRect = listRef.current.getBoundingClientRect();
			const scrollRect = scrollElement.getBoundingClientRect();
			setScrollMargin(listRect.top - scrollRect.top + scrollElement.scrollTop);
		};
		measure();
		const resizeTarget = scrollElement.querySelector<HTMLElement>(".session-files-review-list") ?? scrollElement;
		const observer = new ResizeObserver(measure);
		observer.observe(resizeTarget);
		return () => observer.disconnect();
	}, [enabled, scrollElement]);

	const virtualizer = useVirtualizer({
		count,
		getScrollElement: () => (enabled ? scrollElement : null),
		estimateSize: () => ESTIMATED_ROW_HEIGHT,
		// Every row outside the viewport that overscan pulls in is a row that
		// still has to pay full first-time cost (render + a real DOM
		// measurement, see useSharedScrollRowVirtualizer's comment above) the
		// first time it's scrolled into range. A smaller buffer means fewer
		// never-before-seen rows get force-measured on each scroll jump,
		// trading a slightly higher chance of a one-frame blank edge during
		// very fast scrolling for less of that first-pass cost on big files.
		overscan: 8,
		scrollMargin,
	});

	return { listRef, virtualizer };
}

function DiffView({
	annotation,
	filePath,
	onActiveSelectionChange,
	path,
	previousPath,
	rows,
	sessionId,
	split,
	truncated,
	wrap,
}: {
	annotation: FileAnnotationModel;
	filePath: string;
	onActiveSelectionChange: (active: boolean) => void;
	path: string;
	previousPath?: string;
	rows: DiffRow[];
	sessionId: string;
	split: boolean;
	truncated?: boolean;
	wrap: boolean;
}) {
	const { t } = useTranslation();
	const containerRef = useRef<HTMLDivElement>(null);
	const [hasSelection, setHasSelection] = useState(false);
	const [menuState, setMenuState] = useState<DiffViewMenuState | null>(null);
	const shouldVirtualize = !split && rows.length > ROW_VIRTUALIZE_THRESHOLD;
	const { listRef, virtualizer } = useSharedScrollRowVirtualizer(containerRef, rows.length, shouldVirtualize);
	const highlight = useDiffHighlight(rows, path, previousPath);
	const runs = useMemo(
		() => rows.map((row, index) => (row.kind === "del" ? highlight.oldSide[index] : highlight.newSide[index])),
		[rows, highlight],
	);

	useEffect(() => {
		const onSelectionChange = () => {
			setHasSelection(isSelectionActiveIn(window.getSelection(), containerRef.current));
		};
		document.addEventListener("selectionchange", onSelectionChange);
		return () => document.removeEventListener("selectionchange", onSelectionChange);
	}, []);

	const menuOpen = menuState?.open ?? false;
	useEffect(() => {
		onActiveSelectionChange(hasSelection || menuOpen);
	}, [hasSelection, menuOpen, onActiveSelectionChange]);

	const onContextMenu = useCallback(
		(event: MouseEvent<HTMLDivElement>) => {
			const container = containerRef.current;
			const selection = window.getSelection();
			if (!isSelectionActiveIn(selection, container) || !selection) return;

			// Only past this point do we know we're overriding a real selection —
			// the native context menu must stay untouched for a collapsed selection
			// or one outside this container.
			event.preventDefault();

			const range = selection.getRangeAt(0);
			const startRow = closestDiffRowElement(range.startContainer);
			const endRow = closestDiffRowElement(range.endContainer);
			if (!startRow || !endRow) return;

			const startIndex = Number(startRow.getAttribute("data-row-index"));
			const endIndex = Number(endRow.getAttribute("data-row-index"));
			if (!Number.isFinite(startIndex) || !Number.isFinite(endIndex)) return;

			const min = Math.min(startIndex, endIndex);
			const max = Math.max(startIndex, endIndex);
			const lines = rows
				.slice(min, max + 1)
				.map(toDiffSelectionLine)
				.filter(isNotNull);

			setMenuState({
				open: true,
				position: { x: event.clientX, y: event.clientY },
				lines,
				selectedText: selection.toString(),
			});
		},
		[rows],
	);

	return (
		<div>
			{truncated ? (
				<div className="shrink-0 border-b border-border bg-warning/10 px-3 py-1.5 text-xs text-warning">
					{t("files.diffTruncated")}
				</div>
			) : null}
			<div
				className="diff-code session-files-diff-scrollbar overflow-x-auto overflow-y-visible bg-terminal font-mono text-xs leading-row text-terminal-foreground"
				onContextMenu={onContextMenu}
				ref={containerRef}
			>
				{split ? (
					<SplitDiff
						annotation={annotation}
						newRuns={highlight.newSide}
						oldRuns={highlight.oldSide}
						path={path}
						previousPath={previousPath}
						rows={rows}
						t={t}
					/>
				) : shouldVirtualize ? (
					<div
						className={cn("relative", !wrap && "min-w-max")}
						ref={listRef}
						style={{ height: virtualizer.getTotalSize() }}
					>
						{virtualizer.getVirtualItems().map((virtualRow) => {
							const row = rows[virtualRow.index];
							return (
								<div
									data-index={virtualRow.index}
									key={virtualRow.index}
									ref={virtualizer.measureElement}
									style={{
										position: "absolute",
										top: 0,
										left: 0,
										width: "100%",
										transform: `translateY(${virtualRow.start - virtualizer.options.scrollMargin}px)`,
									}}
								>
									<DiffRowContent
										annotation={annotation}
										index={virtualRow.index}
										path={path}
										previousPath={previousPath}
										row={row}
										runs={runs[virtualRow.index]}
										t={t}
										wrap={wrap}
									/>
								</div>
							);
						})}
					</div>
				) : (
					<div className={cn(!wrap && "min-w-max")}>
						{rows.map((row, index) => (
							<DiffRowContent
								annotation={annotation}
								index={index}
								key={index}
								path={path}
								previousPath={previousPath}
								row={row}
								runs={runs[index]}
								t={t}
								wrap={wrap}
							/>
						))}
					</div>
				)}
			</div>
			<DiffSelectionMenu
				filePath={filePath}
				lines={menuState?.lines ?? []}
				onOpenChange={(open) => setMenuState((current) => (current ? { ...current, open } : current))}
				open={menuOpen}
				position={menuState?.position ?? { x: 0, y: 0 }}
				selectedText={menuState?.selectedText ?? ""}
				sessionId={sessionId}
			/>
		</div>
	);
}

const HunkBand = memo(function HunkBand({ row }: { row: DiffRow }) {
	return (
		<div className="flex select-none items-baseline gap-2 bg-surface-faint px-2.5 py-0.75 text-passive">
			<span className="shrink-0 text-passive/70">{row.text}</span>
			{row.section ? <span className="min-w-0 truncate text-passive/90">{row.section}</span> : null}
		</div>
	);
});

type DiffRowContentProps = {
	annotation: FileAnnotationModel;
	index: number;
	path: string;
	previousPath?: string;
	row: DiffRow;
	runs: DiffRun[];
	t: TFunction;
	wrap: boolean;
};

// One unified-view diff row, shared between the plain (non-virtualized) and
// virtualized render paths so they can't drift apart from each other.
function DiffRowContentInner({ annotation, index, path, previousPath, row, runs, t, wrap }: DiffRowContentProps) {
	if (row.kind === "hunk") return <HunkBand row={row} />;
	return (
		<div>
			<div
				className={cn("group/line relative flex", diffRowTone[row.kind])}
				data-diff-row=""
				data-kind={row.kind}
				data-new-no={row.newNo ?? ""}
				data-old-no={row.oldNo ?? ""}
				data-row-index={index}
			>
				<LineFeedbackButton
					active={isAnnotationRow(annotation.target, path, index)}
					onClick={() => annotation.begin(lineAnnotationTarget(path, previousPath, row, index))}
					t={t}
					target={lineAnnotationTarget(path, previousPath, row, index)}
				/>
				<span className="w-9 shrink-0 select-none border-r border-border/50 bg-terminal px-1.5 text-right text-passive/70 tabular-nums">
					{row.newNo ?? row.oldNo ?? ""}
				</span>
				<span
					className={cn(
						"w-4 shrink-0 select-none text-center",
						row.kind === "add" && "text-success",
						row.kind === "del" && "text-error",
					)}
				>
					{diffMarkerGlyph[row.kind]}
				</span>
				<span className={cn("pr-3", wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre")}>
					{renderDiffRuns(runs, row.kind === "add")}
				</span>
			</div>
			{isAnnotationRow(annotation.target, path, index) ? <FileAnnotationComposer annotation={annotation} /> : null}
		</div>
	);
}

// `annotation` is a fresh object on every SessionFilesView render (its draft
// text, send status, etc. all live there), so a plain memo would never skip
// a re-render — every row would see a "changed" prop on every keystroke
// anywhere in the panel, on top of every scroll tick. Only a row that IS or
// WAS the active annotation target actually needs to re-render when
// `annotation` changes; every other row only cares about `row`/`index`/
// `path`/`previousPath`/`wrap`, which are stable across scroll-driven
// re-renders. This is what actually lets scrolling skip re-running the
// i18next calls, cn() calls, and nested Button for rows that are already
// mounted and unchanged.
const DiffRowContent = memo(DiffRowContentInner, (prev, next) => {
	if (
		prev.row !== next.row ||
		prev.index !== next.index ||
		prev.path !== next.path ||
		prev.previousPath !== next.previousPath ||
		prev.runs !== next.runs ||
		prev.wrap !== next.wrap
	) {
		return false;
	}
	const prevActive = isAnnotationRow(prev.annotation.target, prev.path, prev.index);
	const nextActive = isAnnotationRow(next.annotation.target, next.path, next.index);
	return !prevActive && !nextActive;
});

type SplitRow =
	| { kind: "hunk"; row: DiffRow }
	| { kind: "pair"; left: DiffRow | null; leftIndex: number | null; right: DiffRow | null; rightIndex: number | null };

// toSplitRows aligns the unified rows into left (old) / right (new) pairs: each
// run of deletions lines up index-for-index with the additions that follow it,
// context appears on both sides, and hunk headers span the full width. Each
// side also carries its original index into the flat `rows` array (leftIndex /
// rightIndex) so SplitSide can stamp the same data-row-index the unified view
// uses, without SplitSide re-deriving it via `rows.indexOf`.
function toSplitRows(rows: DiffRow[]): SplitRow[] {
	const out: SplitRow[] = [];
	let dels: Array<{ row: DiffRow; index: number }> = [];
	let adds: Array<{ row: DiffRow; index: number }> = [];
	const flush = () => {
		const count = Math.max(dels.length, adds.length);
		for (let i = 0; i < count; i += 1) {
			out.push({
				kind: "pair",
				left: dels[i]?.row ?? null,
				leftIndex: dels[i]?.index ?? null,
				right: adds[i]?.row ?? null,
				rightIndex: adds[i]?.index ?? null,
			});
		}
		dels = [];
		adds = [];
	};
	rows.forEach((row, index) => {
		if (row.kind === "del") {
			dels.push({ row, index });
			return;
		}
		if (row.kind === "add") {
			adds.push({ row, index });
			return;
		}
		flush();
		if (row.kind === "hunk") out.push({ kind: "hunk", row });
		else out.push({ kind: "pair", left: row, leftIndex: index, right: row, rightIndex: index });
	});
	flush();
	return out;
}

function SplitDiff({
	annotation,
	newRuns,
	oldRuns,
	path,
	previousPath,
	rows,
	t,
}: {
	annotation: FileAnnotationModel;
	newRuns: DiffRun[][];
	oldRuns: DiffRun[][];
	path: string;
	previousPath?: string;
	rows: DiffRow[];
	t: TFunction;
}) {
	const splitRows = useMemo(() => toSplitRows(rows), [rows]);
	return (
		<div>
			{splitRows.map((splitRow, index) =>
				splitRow.kind === "hunk" ? (
					<HunkBand key={`sh${index}`} row={splitRow.row} />
				) : (
					<div key={`sp${index}`}>
						<div className="grid grid-cols-2 divide-x divide-border/40">
							<SplitSide
								annotation={annotation}
								path={path}
								previousPath={previousPath}
								row={splitRow.left}
								rowIndex={splitRow.leftIndex}
								runs={splitRow.leftIndex === null ? null : oldRuns[splitRow.leftIndex]}
								side="old"
								t={t}
							/>
							<SplitSide
								annotation={annotation}
								path={path}
								previousPath={previousPath}
								row={splitRow.right}
								rowIndex={splitRow.rightIndex}
								runs={splitRow.rightIndex === null ? null : newRuns[splitRow.rightIndex]}
								side="new"
								t={t}
							/>
						</div>
						{(splitRow.leftIndex !== null && isAnnotationRow(annotation.target, path, splitRow.leftIndex)) ||
						(splitRow.rightIndex !== null && isAnnotationRow(annotation.target, path, splitRow.rightIndex)) ? (
							<FileAnnotationComposer annotation={annotation} />
						) : null}
					</div>
				),
			)}
		</div>
	);
}

function SplitSide({
	annotation,
	path,
	previousPath,
	row,
	rowIndex,
	runs,
	side,
	t,
}: {
	annotation: FileAnnotationModel;
	path: string;
	previousPath?: string;
	row: DiffRow | null;
	rowIndex: number | null;
	runs: DiffRun[] | null;
	side: "old" | "new";
	t: TFunction;
}) {
	if (!row || rowIndex === null || !runs) return <div className="bg-surface-faint/20" aria-hidden="true" />;
	const lineNo = side === "old" ? row.oldNo : row.newNo;
	const tone = row.kind === "hunk" ? "" : diffRowTone[row.kind];
	const target = lineNo == null ? null : lineAnnotationTarget(path, previousPath, row, rowIndex, side);
	return (
		<div
			className={cn("group/line relative flex min-w-0", tone)}
			data-diff-row=""
			data-kind={row.kind}
			data-new-no={row.newNo ?? ""}
			data-old-no={row.oldNo ?? ""}
			data-row-index={rowIndex}
		>
			{target ? (
				<LineFeedbackButton
					active={isAnnotationRow(annotation.target, path, rowIndex)}
					onClick={() => annotation.begin(target)}
					t={t}
					target={target}
				/>
			) : null}
			<span className="w-9 shrink-0 select-none border-r border-border/50 bg-terminal px-1.5 text-right text-passive/70 tabular-nums">
				{lineNo ?? ""}
			</span>
			<span className="min-w-0 whitespace-pre-wrap break-all px-1.5">{renderDiffRuns(runs, row.kind === "add")}</span>
		</div>
	);
}

function lineAnnotationTarget(
	path: string,
	previousPath: string | undefined,
	row: DiffRow,
	rowIndex: number,
	side: "old" | "new" = row.kind === "del" ? "old" : "new",
): ActiveFileAnnotationTarget {
	return {
		path,
		previousPath,
		side,
		line: side === "old" ? (row.oldNo ?? undefined) : (row.newNo ?? undefined),
		oldLine: row.oldNo ?? undefined,
		newLine: row.newNo ?? undefined,
		lineKind: row.kind === "hunk" ? undefined : row.kind,
		lineText: row.text,
		rowIndex,
	};
}

function isAnnotationRow(target: ActiveFileAnnotationTarget | null, path: string, rowIndex: number): boolean {
	return target?.path === path && target.side !== "file" && target.rowIndex === rowIndex;
}

// `t` comes from the caller (which already holds one useTranslation()
// subscription) rather than calling useTranslation() here: this button is
// invisible until hover on every diff row, and a virtualized file can mount
// dozens of them per scroll tick — a separate i18n context subscription per
// row added up on large files.
function LineFeedbackButton({
	active,
	onClick,
	t,
	target,
}: {
	active: boolean;
	onClick: () => void;
	t: TFunction;
	target: ActiveFileAnnotationTarget;
}) {
	if (active) return null;
	const side = t(target.side === "old" ? "files.oldSide" : "files.newSide");
	const label = t("files.addLineFeedback", { file: target.path, line: target.line, side });
	return (
		<Button
			aria-label={label}
			className="absolute inset-y-0 left-6 z-20 my-auto size-6 rounded-sm border-primary/70 opacity-0 shadow-md shadow-black/30 transition-opacity active:translate-y-0 active:scale-100 focus-visible:opacity-100 group-hover/line:opacity-100"
			onClick={onClick}
			size={null}
			type="button"
			variant="primary"
		>
			<Plus className="size-4" aria-hidden="true" />
		</Button>
	);
}

export function FileAnnotationComposer({ annotation }: { annotation: FileAnnotationModel }) {
	const { t } = useTranslation();
	const target = annotation.target;
	if (!target) return null;
	const side = target.side === "file" ? "" : t(target.side === "old" ? "files.oldSide" : "files.newSide");
	const targetLabel =
		target.side === "file"
			? t("files.fileFeedbackTarget", { file: target.path })
			: t("files.lineFeedbackTarget", { file: target.path, line: target.line, side });
	const submit = () => void annotation.submit();

	return (
		<form
			className="border-y border-border/70 bg-surface px-3 py-2 font-sans"
			onSubmit={(event) => {
				event.preventDefault();
				submit();
			}}
		>
			<div className="mb-1.5 flex items-center justify-between gap-2">
				<span className="min-w-0 truncate font-mono text-caption text-passive">{targetLabel}</span>
				{annotation.status === "sent" ? (
					<span className="inline-flex items-center gap-1 text-caption text-success" role="status">
						<Check className="size-icon-sm" aria-hidden="true" />
						{t("files.feedbackSent")}
					</span>
				) : null}
			</div>
			<textarea
				aria-label={t("files.feedbackLabel", { target: targetLabel })}
				autoFocus
				className="min-h-20 w-full resize-y rounded-md border border-input bg-background px-2.5 py-2 text-sm text-foreground outline-none placeholder:text-passive focus-visible:outline-none disabled:opacity-60"
				disabled={annotation.status === "sending" || annotation.status === "sent"}
				onChange={(event) => annotation.setDraft(event.target.value)}
				onKeyDown={(event) => {
					if (event.key === "Escape") {
						event.preventDefault();
						annotation.cancel();
					} else if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
						event.preventDefault();
						submit();
					}
				}}
				placeholder={t("files.feedbackPlaceholder")}
				value={annotation.draft}
			/>
			{annotation.status === "error" ? (
				<p className="mt-1.5 text-xs text-error" role="alert">
					{annotation.error}
				</p>
			) : null}
			<div className="mt-2 flex items-center justify-end gap-1.5">
				<span className="mr-auto text-caption text-passive">{t("files.feedbackShortcut")}</span>
				<Button
					disabled={annotation.status === "sending" || annotation.status === "sent"}
					onClick={annotation.cancel}
					size="sm"
					type="button"
					variant="ghost"
				>
					{t("files.cancelFeedback")}
				</Button>
				<Button
					disabled={!annotation.draft.trim() || annotation.status === "sending" || annotation.status === "sent"}
					size="sm"
					type="submit"
				>
					<SendIcon className="size-icon-sm" aria-hidden="true" />
					{annotation.status === "sending" ? t("files.sendingFeedback") : t("files.sendFeedback")}
				</Button>
			</div>
		</form>
	);
}

function renderDiffRuns(runs: DiffRun[], add: boolean): ReactNode {
	return runs.map((run, index) =>
		run.changed ? (
			<span className={cn(run.className, "rounded-sm", add ? "bg-success/35" : "bg-error/35")} key={index}>
				{run.text}
			</span>
		) : run.className ? (
			<span className={run.className} key={index}>
				{run.text}
			</span>
		) : (
			run.text
		),
	);
}

export function ChangeBadges({ additions, deletions }: { additions: number; deletions: number }) {
	return (
		<span className="flex shrink-0 items-center gap-1 font-mono text-caption font-medium">
			{additions > 0 ? <span className="px-0.5 text-success">+{additions}</span> : null}
			{deletions > 0 ? <span className="px-0.5 text-error">-{deletions}</span> : null}
		</span>
	);
}

export function PanelMessage({ action, children, compact = false }: { action?: ReactNode; children: ReactNode; compact?: boolean }) {
	return (
		<div
			className={cn(
				"grid place-items-center text-center text-xs text-muted-foreground",
				compact ? "min-h-16 p-3" : "min-h-[180px] p-6",
			)}
		>
			<div className="flex max-w-sm flex-col items-center gap-3">
				<p>{children}</p>
				{action ?? null}
			</div>
		</div>
	);
}

export function RetryButton({ onClick }: { onClick: () => void }) {
	const { t } = useTranslation();
	return (
		<Button onClick={onClick} size="sm" type="button" variant="outline">
			{t("files.retry")}
		</Button>
	);
}

export function StatusMark({ status }: { status: WorkspaceFileStatus }) {
	const { t } = useTranslation();
	const label = statusLabel[status];
	return (
		<span
			className={cn(
				"inline-flex w-5 shrink-0 items-center justify-center font-mono text-caption font-medium",
				statusTone[status],
			)}
			title={t(`files.status.${status}`)}
		>
			{label}
		</span>
	);
}
