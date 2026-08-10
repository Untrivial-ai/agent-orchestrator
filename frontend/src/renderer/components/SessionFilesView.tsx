import {
	memo,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
	type KeyboardEvent,
	type MouseEvent,
	type ReactNode,
	type RefObject,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import {
	Check,
	ChevronDown,
	ChevronRight,
	ChevronsDownUp,
	ChevronsUpDown,
	Columns2,
	Maximize2,
	MessageSquare,
	Minimize2,
	Plus,
	Rows3,
	Search,
	Send as SendIcon,
	Trash2,
} from "lucide-react";
import type { components } from "../../api/schema";
import {
	fileAnnotationKey,
	formatPendingReviewChunks,
	type FileAnnotationTarget,
} from "../../shared/file-annotations";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import {
	isChangedWorkspaceFile,
	sessionWorkspaceFilesQueryOptions,
	type WorkspaceCompareMode,
	type WorkspaceFileSummary,
} from "../hooks/useSessionWorkspaceFiles";
import { useParsedDiff } from "../hooks/useParsedDiff";
import { cn } from "../lib/utils";
import type { DiffRow, DiffRowKind, DiffSegment } from "../lib/diff-parser";
import type { DiffSelectionLine } from "../../shared/diff-selection";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "./ui/accordion";
import { subscribeWorkspaceFileChanges } from "../lib/workspace-file-events";
import { Button } from "./ui/button";
import { DiffSelectionMenu } from "./DiffSelectionMenu";
import { Input } from "./ui/input";
import {
	usePendingReviewComments,
	type PendingReviewAnnotation,
} from "./PendingReviewCommentsContext";

type WorkspaceFileDetail = components["schemas"]["WorkspaceFileResponse"] & {
	previousPath?: string;
	compareMode?: WorkspaceCompareMode;
};
type WorkspaceFileStatus = WorkspaceFileSummary["status"];

type ActiveFileAnnotationTarget = FileAnnotationTarget & { rowIndex?: number };
type FileAnnotationStatus = "idle" | "sending" | "sent" | "error";
type PendingAnnotation = PendingReviewAnnotation;
type FileAnnotationModel = {
	openTarget: ActiveFileAnnotationTarget | null;
	draft: string;
	pending: Record<string, PendingAnnotation>;
	pendingCount: number;
	status: FileAnnotationStatus;
	error: string;
	begin: (target: ActiveFileAnnotationTarget) => void;
	setDraft: (draft: string) => void;
	cancelComposer: () => void;
	stage: () => void;
	discardPending: (key: string) => void;
	submitReview: () => Promise<void>;
	keyFor: (target: ActiveFileAnnotationTarget) => string;
};

type SessionFilesViewProps = {
	sessionId: string;
	isMaximized?: boolean;
	onToggleMaximized?: (next: boolean) => void;
};

const emptyFiles: WorkspaceFileSummary[] = [];

const statusLabel: Record<WorkspaceFileStatus, string> = {
	added: "A",
	deleted: "D",
	modified: "M",
	renamed: "R",
	unmodified: "",
};

const statusTone: Record<WorkspaceFileStatus, string> = {
	added: "text-success",
	deleted: "text-error",
	modified: "text-warning",
	renamed: "text-accent",
	unmodified: "text-passive",
};

// Split (old | new) view only means something when both sides have content to
// compare. Added files have nothing on the old side; deleted files have
// nothing on the new side — splitting them just wastes half the pane on an
// empty column, so those always render unified regardless of the toggle.
function canSplitCompare(status: WorkspaceFileStatus): boolean {
	return status === "modified" || status === "renamed";
}

export function SessionFilesView({
	sessionId,
	isMaximized = false,
	onToggleMaximized,
}: SessionFilesViewProps) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [filter, setFilter] = useState("");
	const [split, setSplit] = useState(false);
	const [expandedPaths, setExpandedPaths] = useState<Set<string>>(() => new Set());
	const [openAnnotationTarget, setOpenAnnotationTarget] = useState<ActiveFileAnnotationTarget | null>(null);
	const [annotationDraft, setAnnotationDraft] = useState("");
	const { pendingAnnotations, setPendingAnnotations } = usePendingReviewComments();
	const [annotationStatus, setAnnotationStatus] = useState<FileAnnotationStatus>("idle");
	const [annotationError, setAnnotationError] = useState("");
	const annotationGenerationRef = useRef(0);
	const annotationSentTimerRef = useRef<number | null>(null);
	const rootRef = useRef<HTMLElement>(null);

	const filesQuery = useQuery(sessionWorkspaceFilesQueryOptions(sessionId, t("files.error.loadWorkspace")));
	useEffect(() => subscribeWorkspaceFileChanges(sessionId, queryClient), [queryClient, sessionId]);
	const files = filesQuery.data?.files ?? emptyFiles;
	const changedFiles = useMemo(() => files.filter(isChangedWorkspaceFile), [files]);
	const pendingCount = Object.keys(pendingAnnotations).length;

	const clearSentTimer = () => {
		if (annotationSentTimerRef.current !== null) {
			window.clearTimeout(annotationSentTimerRef.current);
			annotationSentTimerRef.current = null;
		}
	};

	useEffect(() => {
		annotationGenerationRef.current += 1;
		clearSentTimer();
		setExpandedPaths(new Set());
		setFilter("");
		setOpenAnnotationTarget(null);
		setAnnotationDraft("");
		// Pending comments clear in PendingReviewCommentsProvider when sessionId changes.
		setAnnotationStatus("idle");
		setAnnotationError("");
	}, [sessionId]);

	useEffect(
		() => () => {
			clearSentTimer();
		},
		[],
	);

	useEffect(() => {
		const root = rootRef.current;
		if (!root) return;
		const routeDiffWheel = (event: WheelEvent) => {
			if (event.ctrlKey || event.metaKey || event.shiftKey || Math.abs(event.deltaX) >= Math.abs(event.deltaY)) return;
			const target = event.target;
			if (!(target instanceof Element) || !target.closest(".session-files-diff-scrollbar")) return;
			const scrollRoot = root.querySelector<HTMLElement>("[data-files-scroll-root]");
			if (!scrollRoot) return;
			const delta =
				event.deltaMode === WheelEvent.DOM_DELTA_LINE
					? event.deltaY * 16
					: event.deltaMode === WheelEvent.DOM_DELTA_PAGE
						? event.deltaY * scrollRoot.clientHeight
						: event.deltaY;
			if (delta === 0) return;
			event.preventDefault();
			scrollRoot.scrollTop += delta;
		};
		root.addEventListener("wheel", routeDiffWheel, { capture: true, passive: false });
		return () => root.removeEventListener("wheel", routeDiffWheel, { capture: true });
	}, []);

	const beginAnnotation = (target: ActiveFileAnnotationTarget) => {
		clearSentTimer();
		if (annotationStatus === "sent") setAnnotationStatus("idle");
		const key = fileAnnotationKey(target);
		const existing = pendingAnnotations[key];
		setOpenAnnotationTarget(target);
		setAnnotationDraft(existing?.feedback ?? "");
	};
	const cancelComposer = () => {
		setOpenAnnotationTarget(null);
		setAnnotationDraft("");
	};
	const stageAnnotation = () => {
		if (!openAnnotationTarget || !annotationDraft.trim() || annotationStatus === "sending") return;
		const key = fileAnnotationKey(openAnnotationTarget);
		setPendingAnnotations((current) => ({
			...current,
			[key]: { key, target: openAnnotationTarget, feedback: annotationDraft.trim() },
		}));
		setOpenAnnotationTarget(null);
		setAnnotationDraft("");
		if (annotationStatus === "sent" || annotationStatus === "error") {
			setAnnotationStatus("idle");
			setAnnotationError("");
		}
	};
	const discardPending = (key: string) => {
		setPendingAnnotations((current) => {
			if (!(key in current)) return current;
			const next = { ...current };
			delete next[key];
			return next;
		});
		if (openAnnotationTarget && fileAnnotationKey(openAnnotationTarget) === key) {
			setOpenAnnotationTarget(null);
			setAnnotationDraft("");
		}
	};
	const submitReview = async () => {
		const pendingList = Object.values(pendingAnnotations);
		if (pendingList.length === 0 || annotationStatus === "sending") return;
		const sendGeneration = annotationGenerationRef.current;
		clearSentTimer();
		setAnnotationStatus("sending");
		setAnnotationError("");
		setOpenAnnotationTarget(null);
		setAnnotationDraft("");
		try {
			const chunks = formatPendingReviewChunks(
				pendingList.map(({ target, feedback }) => ({ target, feedback })),
			);
			for (const chunk of chunks) {
				const { error } = await apiClient.POST("/api/v1/sessions/{sessionId}/send", {
					params: { path: { sessionId } },
					body: { message: chunk.message },
				});
				if (sendGeneration !== annotationGenerationRef.current) return;
				if (error) throw new Error(apiErrorMessage(error, t("files.feedbackError")));
				// Drop only what this chunk delivered so a later failure can retry
				// without re-sending already-accepted comments.
				const deliveredKeys = new Set(chunk.comments.map((comment) => fileAnnotationKey(comment.target)));
				setPendingAnnotations((current) => {
					const next = { ...current };
					for (const key of Object.keys(next)) {
						if (deliveredKeys.has(key)) delete next[key];
					}
					return next;
				});
			}
			if (sendGeneration !== annotationGenerationRef.current) return;
			setPendingAnnotations({});
			setAnnotationStatus("sent");
			annotationSentTimerRef.current = window.setTimeout(() => {
				annotationSentTimerRef.current = null;
				if (annotationGenerationRef.current === sendGeneration) {
					setAnnotationStatus("idle");
				}
			}, 1_200);
		} catch (error) {
			if (sendGeneration !== annotationGenerationRef.current) return;
			setAnnotationStatus("error");
			setAnnotationError(apiErrorMessage(error, t("files.feedbackError")));
		}
	};
	const annotation: FileAnnotationModel = {
		openTarget: openAnnotationTarget,
		draft: annotationDraft,
		pending: pendingAnnotations,
		pendingCount,
		status: annotationStatus,
		error: annotationError,
		begin: beginAnnotation,
		setDraft: setAnnotationDraft,
		cancelComposer,
		stage: stageAnnotation,
		discardPending,
		submitReview,
		keyFor: fileAnnotationKey,
	};

	const normalizedFilter = filter.trim().toLowerCase();
	const visibleFiles = useMemo(
		() =>
			normalizedFilter
				? changedFiles.filter((file) => fileSearchText(file).includes(normalizedFilter))
				: changedFiles,
		[changedFiles, normalizedFilter],
	);
	const changedCount = changedFiles.length;
	const expandedVisibleCount = visibleFiles.filter((file) => expandedPaths.has(file.path)).length;

	const toggleVisibleFiles = () => {
		setExpandedPaths((current) => {
			const next = new Set(current);
			if (expandedVisibleCount > 0) {
				for (const file of visibleFiles) next.delete(file.path);
				return next;
			}
			for (const file of visibleFiles) next.add(file.path);
			return next;
		});
	};

	// j / k move focus between file rows (Vim-style), unless the user is typing
	// in the search box. The rows themselves handle Enter/Space to expand.
	const onFilesKeyDown = (event: KeyboardEvent<HTMLElement>) => {
		if (event.key !== "j" && event.key !== "k") return;
		const active = document.activeElement;
		if (active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement) return;
		const toggles = Array.from(rootRef.current?.querySelectorAll<HTMLButtonElement>("[data-file-toggle]") ?? []);
		if (toggles.length === 0) return;
		event.preventDefault();
		const current = toggles.findIndex((button) => button === active);
		if (current === -1) {
			toggles[0].focus();
			return;
		}
		const next = event.key === "j" ? Math.min(toggles.length - 1, current + 1) : Math.max(0, current - 1);
		toggles[next].focus();
	};

	return (
		<section
			ref={rootRef}
			onKeyDown={onFilesKeyDown}
			className="flex h-full min-h-0 flex-col bg-background text-foreground"
			aria-label={t("files.sessionFiles")}
		>
			<header className="flex h-10 shrink-0 items-center gap-0.5 border-b border-border bg-surface px-2">
				<label className="relative mr-1 min-w-0 flex-1">
					<Search className="pointer-events-none absolute left-2.5 top-1/2 size-icon-sm -translate-y-1/2 text-passive" />
					<Input
						aria-label={t("files.search")}
						className="h-8 pl-8 font-mono text-xs"
						onChange={(event) => setFilter(event.target.value)}
						placeholder={
							filesQuery.isPending
								? t("files.loading")
								: t("files.searchCountPlaceholder", { count: changedCount })
						}
						value={filter}
					/>
				</label>
				<Button
					aria-label={expandedVisibleCount > 0 ? t("files.collapseAll") : t("files.expandAll")}
					className="shrink-0"
					disabled={visibleFiles.length === 0}
					onClick={toggleVisibleFiles}
					size="icon-sm"
					type="button"
					variant="ghost"
				>
					{expandedVisibleCount > 0 ? (
						<ChevronsDownUp className="size-icon-sm" aria-hidden="true" />
					) : (
						<ChevronsUpDown className="size-icon-sm" aria-hidden="true" />
					)}
				</Button>
				<Button
					aria-label={split ? t("files.unifiedDiff") : t("files.splitDiff")}
					aria-pressed={split}
					className="shrink-0"
					onClick={() => setSplit((current) => !current)}
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
				{onToggleMaximized ? (
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
				) : null}
				{pendingCount > 0 || annotationStatus === "sending" || annotationStatus === "sent" || annotationStatus === "error" ? (
					<div className="ml-1 flex shrink-0 items-center gap-1.5 border-l border-border pl-1.5">
						{annotationStatus === "sent" ? (
							<span className="inline-flex items-center gap-1 text-caption text-success" role="status">
								<Check className="size-icon-sm" aria-hidden="true" />
								{t("files.reviewSubmitted")}
							</span>
						) : (
							<Button
								disabled={pendingCount === 0 || annotationStatus === "sending"}
								onClick={() => void submitReview()}
								size="sm"
								type="button"
							>
								<SendIcon className="size-icon-sm" aria-hidden="true" />
								{annotationStatus === "sending"
									? t("files.submittingReview")
									: t("files.submitReview", { count: pendingCount })}
							</Button>
						)}
					</div>
				) : null}
			</header>
			{annotationStatus === "error" && annotationError ? (
				<div className="shrink-0 border-b border-border bg-error/10 px-3 py-1.5 text-xs text-error" role="alert">
					{annotationError}
				</div>
			) : null}

			<div
				className="board-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain bg-background"
				data-files-scroll-root=""
			>
				<div className={cn("flex w-full flex-col px-0", !isMaximized && "mx-auto max-w-[1200px]")}>
					<ReviewFileList
						annotation={annotation}
						compareMode={filesQuery.data?.compareMode}
						error={filesQuery.error}
						expandedPaths={expandedPaths}
						files={visibleFiles}
						isLoading={filesQuery.isPending}
						onExpandedPathsChange={setExpandedPaths}
						onRetry={() => void filesQuery.refetch()}
						sessionId={sessionId}
						split={split}
						wrap={true}
					/>
				</div>
			</div>
		</section>
	);
}

function ReviewFileList({
	annotation,
	compareMode,
	error,
	expandedPaths,
	files,
	isLoading,
	onExpandedPathsChange,
	onRetry,
	sessionId,
	split,
	wrap,
}: {
	annotation: FileAnnotationModel;
	compareMode?: WorkspaceCompareMode;
	error: Error | null;
	expandedPaths: Set<string>;
	files: WorkspaceFileSummary[];
	isLoading: boolean;
	onExpandedPathsChange: (next: Set<string>) => void;
	onRetry: () => void;
	sessionId: string;
	split: boolean;
	wrap: boolean;
}) {
	const { t } = useTranslation();
	if (isLoading) {
		return <PanelMessage>{t("files.loading")}</PanelMessage>;
	}
	if (error) {
		return (
			<PanelMessage action={<RetryButton onClick={onRetry} />}>{error.message || t("files.error.load")}</PanelMessage>
		);
	}
	if (files.length === 0) {
		return <PanelMessage>{emptyFilesMessage(compareMode, t)}</PanelMessage>;
	}
	return (
		<Accordion
			asChild
			onValueChange={(next: string[]) => onExpandedPathsChange(new Set(next))}
			type="multiple"
			value={Array.from(expandedPaths)}
		>
			<ul className="session-files-review-list flex flex-col gap-0.5">
				{files.map((file) => (
					<ReviewFileCard
						annotation={annotation}
						expanded={expandedPaths.has(file.path)}
						file={file}
						key={file.path}
						sessionId={sessionId}
						split={split}
						wrap={wrap}
					/>
				))}
			</ul>
		</Accordion>
	);
}

function ReviewFileCard({
	annotation,
	expanded,
	file,
	sessionId,
	split,
	wrap,
}: {
	annotation: FileAnnotationModel;
	expanded: boolean;
	file: WorkspaceFileSummary;
	sessionId: string;
	split: boolean;
	wrap: boolean;
}) {
	const { t } = useTranslation();
	// While the user has an active text selection (or the context menu it opens)
	// in this file's diff, a background refetch would re-render the diff body
	// out from under them and blow away the browser's native selection.
	const [selectionOrMenuActive, setSelectionOrMenuActive] = useState(false);
	const detailQuery = useQuery({
		queryKey: ["session-workspace-file", sessionId, file.path],
		enabled: expanded && !selectionOrMenuActive,
		queryFn: () => loadWorkspaceFile(sessionId, file.path, t),
	});
	const fileTarget: ActiveFileAnnotationTarget = {
		path: file.path,
		previousPath: file.previousPath,
		side: "file",
	};
	const fileKey = annotation.keyFor(fileTarget);
	const filePending = annotation.pending[fileKey];
	const fileComposerOpen = isOpenAnnotation(annotation.openTarget, file.path, "file");

	return (
		<AccordionItem asChild value={file.path}>
			<li className="session-files-review-row overflow-hidden bg-transparent">
				<AccordionTrigger
					aria-label={t(expanded ? "files.collapseFile" : "files.expandFile", { file: fileLabel(file) })}
					className="flex min-w-0 flex-1 items-center gap-1.5 px-2.5 py-1 text-left"
					data-file-toggle=""
					headerClassName="min-h-9 hover:bg-interactive-hover/50 data-[state=open]:bg-interactive-active/35"
					trailing={
						<FileFeedbackButton
							active={fileComposerOpen}
							file={file}
							onClick={() => annotation.begin(fileTarget)}
							pending={Boolean(filePending)}
						/>
					}
				>
					{expanded ? (
						<ChevronDown className="size-icon-sm shrink-0 text-passive" aria-hidden="true" />
					) : (
						<ChevronRight className="size-icon-sm shrink-0 text-passive" aria-hidden="true" />
					)}
					<StatusMark status={file.status} />
					<FilePathLabel file={file} />
					<ChangeBadges additions={file.additions} deletions={file.deletions} />
				</AccordionTrigger>
				{fileComposerOpen ? <FileAnnotationComposer annotation={annotation} /> : null}
				{!fileComposerOpen && filePending ? (
					<PendingAnnotationCard annotation={annotation} pending={filePending} />
				) : null}
				<AccordionContent className="border-t border-border/60 bg-background/40">
					{detailQuery.isPending ? <PanelMessage compact>{t("files.loadingDiff")}</PanelMessage> : null}
					{!detailQuery.isPending && detailQuery.error ? (
						<PanelMessage compact action={<RetryButton onClick={() => void detailQuery.refetch()} />}>
							{detailQuery.error.message || t("files.error.loadFile")}
						</PanelMessage>
					) : null}
					{!detailQuery.isPending && !detailQuery.error && detailQuery.data ? (
						<ReviewDiffBody
							annotation={annotation}
							detail={detailQuery.data}
							filePath={file.path}
							onActiveSelectionChange={setSelectionOrMenuActive}
							sessionId={sessionId}
							split={split && canSplitCompare(file.status)}
							wrap={wrap}
						/>
					) : null}
				</AccordionContent>
			</li>
		</AccordionItem>
	);
}

function FileFeedbackButton({
	active,
	file,
	onClick,
	pending,
}: {
	active: boolean;
	file: WorkspaceFileSummary;
	onClick: () => void;
	pending: boolean;
}) {
	const { t } = useTranslation();
	if (active) return <span className="size-7 shrink-0" aria-hidden="true" />;
	return (
		<Button
			aria-label={
				pending
					? t("files.editPendingFileFeedback", { file: file.path })
					: t("files.addFileFeedback", { file: file.path })
			}
			className={cn(
				"size-7 shrink-0 transition-opacity focus-visible:opacity-100",
				pending ? "opacity-100 text-accent" : "opacity-0 group-hover/row:opacity-100",
			)}
			onClick={onClick}
			size={null}
			type="button"
			variant="ghost"
		>
			{pending ? (
				<MessageSquare className="size-icon-sm" aria-hidden="true" />
			) : (
				<Plus className="size-icon-sm" aria-hidden="true" />
			)}
		</Button>
	);
}
function FilePathLabel({ file }: { file: WorkspaceFileSummary }) {
	if (!file.previousPath) {
		return <span className="min-w-0 flex-1 truncate font-mono text-xs font-medium text-foreground">{file.path}</span>;
	}
	return (
		<span className="min-w-0 flex-1 truncate font-mono text-xs font-medium text-foreground">
			<span className="text-passive line-through decoration-border">{file.previousPath}</span>
			<span className="px-1 text-passive">-&gt;</span>
			<span>{file.path}</span>
		</span>
	);
}

function fileLabel(file: WorkspaceFileSummary): string {
	return file.previousPath ? `${file.previousPath} -> ${file.path}` : file.path;
}

function fileSearchText(file: WorkspaceFileSummary): string {
	return fileLabel(file).toLowerCase();
}

function emptyFilesMessage(compareMode: WorkspaceCompareMode | undefined, t: TFunction): string {
	if (compareMode === "head_fallback") return t("files.noChangesHead");
	if (compareMode === "base") return t("files.noChangesBase");
	return t("files.noneChanged");
}

function emptyDiffMessage(compareMode: WorkspaceCompareMode | undefined, t: TFunction): string {
	return compareMode === "base" ? t("files.noChangesBase") : t("files.noChangesHead");
}

async function loadWorkspaceFile(sessionId: string, path: string, t: TFunction) {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/file", {
		params: { path: { sessionId }, query: { path } },
	});
	if (error) throw new Error(apiErrorMessage(error, t("files.error.loadWorkspaceFile")));
	if (!data) throw new Error(t("files.error.emptyResponse"));
	return data;
}

function ReviewDiffBody({
	annotation,
	detail,
	filePath,
	onActiveSelectionChange,
	sessionId,
	split,
	wrap,
}: {
	annotation: FileAnnotationModel;
	detail: WorkspaceFileDetail;
	filePath: string;
	onActiveSelectionChange: (active: boolean) => void;
	sessionId: string;
	split: boolean;
	wrap: boolean;
}) {
	const { t } = useTranslation();
	const { rows, pending } = useParsedDiff(detail.diff);
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
				className="session-files-diff-scrollbar overflow-x-auto overflow-y-visible bg-terminal font-mono text-xs leading-row text-terminal-foreground"
				onContextMenu={onContextMenu}
				ref={containerRef}
			>
				{split ? (
					<SplitDiff annotation={annotation} path={path} previousPath={previousPath} rows={rows} t={t} />
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
	t: TFunction;
	wrap: boolean;
};

// One unified-view diff row, shared between the plain (non-virtualized) and
// virtualized render paths so they can't drift apart from each other.
function DiffRowContentInner({ annotation, index, path, previousPath, row, t, wrap }: DiffRowContentProps) {
	if (row.kind === "hunk") return <HunkBand row={row} />;
	const target = lineAnnotationTarget(path, previousPath, row, index);
	const key = annotation.keyFor(target);
	const pending = annotation.pending[key];
	const composerOpen = isAnnotationRow(annotation.openTarget, path, index);
	return (
		<div>
			<div
				className={cn(
					"group/line relative flex",
					diffRowTone[row.kind],
					pending && !composerOpen && "ring-1 ring-inset ring-accent/40",
				)}
				data-diff-row=""
				data-kind={row.kind}
				data-new-no={row.newNo ?? ""}
				data-old-no={row.oldNo ?? ""}
				data-row-index={index}
			>
				<LineFeedbackButton
					active={composerOpen}
					onClick={() => annotation.begin(target)}
					pending={Boolean(pending)}
					t={t}
					target={target}
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
					{row.segments ? <DiffLineSegments add={row.kind === "add"} segments={row.segments} /> : row.text || " "}
				</span>
			</div>
			{composerOpen ? <FileAnnotationComposer annotation={annotation} /> : null}
			{!composerOpen && pending ? <PendingAnnotationCard annotation={annotation} pending={pending} /> : null}
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
		prev.wrap !== next.wrap
	) {
		return false;
	}
	const prevTarget = lineAnnotationTarget(prev.path, prev.previousPath, prev.row, prev.index);
	const nextTarget = lineAnnotationTarget(next.path, next.previousPath, next.row, next.index);
	const prevKey = prev.annotation.keyFor(prevTarget);
	const nextKey = next.annotation.keyFor(nextTarget);
	const prevPending = prev.annotation.pending[prevKey];
	const nextPending = next.annotation.pending[nextKey];
	if (prevPending !== nextPending) return false;
	const prevActive = isAnnotationRow(prev.annotation.openTarget, prev.path, prev.index);
	const nextActive = isAnnotationRow(next.annotation.openTarget, next.path, next.index);
	if (prevActive || nextActive) return false;
	// Draft/status changes only matter for the open composer row (handled above).
	return true;
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
	path,
	previousPath,
	rows,
	t,
}: {
	annotation: FileAnnotationModel;
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
					<SplitPairBlock
						annotation={annotation}
						key={`sp${index}`}
						path={path}
						previousPath={previousPath}
						splitRow={splitRow}
						t={t}
					/>
				),
			)}
		</div>
	);
}

function SplitPairBlock({
	annotation,
	path,
	previousPath,
	splitRow,
	t,
}: {
	annotation: FileAnnotationModel;
	path: string;
	previousPath?: string;
	splitRow: Extract<SplitRow, { kind: "pair" }>;
	t: TFunction;
}) {
	const leftOpen =
		splitRow.leftIndex !== null && isAnnotationRow(annotation.openTarget, path, splitRow.leftIndex);
	const rightOpen =
		splitRow.rightIndex !== null && isAnnotationRow(annotation.openTarget, path, splitRow.rightIndex);
	const leftPending =
		splitRow.left && splitRow.leftIndex !== null
			? annotation.pending[
					annotation.keyFor(lineAnnotationTarget(path, previousPath, splitRow.left, splitRow.leftIndex, "old"))
				]
			: undefined;
	const rightPending =
		splitRow.right && splitRow.rightIndex !== null
			? annotation.pending[
					annotation.keyFor(lineAnnotationTarget(path, previousPath, splitRow.right, splitRow.rightIndex, "new"))
				]
			: undefined;
	const pendingCards = [leftPending, rightPending].filter((item): item is PendingAnnotation => {
		if (!item) return false;
		const openForThis =
			(leftPending?.key === item.key && leftOpen) || (rightPending?.key === item.key && rightOpen);
		return !openForThis;
	});

	return (
		<div>
			<div className="grid grid-cols-2 divide-x divide-border/40">
				<SplitSide
					annotation={annotation}
					path={path}
					pending={Boolean(leftPending)}
					previousPath={previousPath}
					row={splitRow.left}
					rowIndex={splitRow.leftIndex}
					side="old"
					t={t}
				/>
				<SplitSide
					annotation={annotation}
					path={path}
					pending={Boolean(rightPending)}
					previousPath={previousPath}
					row={splitRow.right}
					rowIndex={splitRow.rightIndex}
					side="new"
					t={t}
				/>
			</div>
			{leftOpen || rightOpen ? <FileAnnotationComposer annotation={annotation} /> : null}
			{pendingCards.map((pending) => (
				<PendingAnnotationCard annotation={annotation} key={pending.key} pending={pending} />
			))}
		</div>
	);
}

function SplitSide({
	annotation,
	path,
	pending,
	previousPath,
	row,
	rowIndex,
	side,
	t,
}: {
	annotation: FileAnnotationModel;
	path: string;
	pending: boolean;
	previousPath?: string;
	row: DiffRow | null;
	rowIndex: number | null;
	side: "old" | "new";
	t: TFunction;
}) {
	if (!row || rowIndex === null) return <div className="bg-surface-faint/20" aria-hidden="true" />;
	const lineNo = side === "old" ? row.oldNo : row.newNo;
	const tone = row.kind === "hunk" ? "" : diffRowTone[row.kind];
	const target = lineNo == null ? null : lineAnnotationTarget(path, previousPath, row, rowIndex, side);
	const composerOpen = isAnnotationRow(annotation.openTarget, path, rowIndex);
	return (
		<div
			className={cn(
				"group/line relative flex min-w-0",
				tone,
				pending && !composerOpen && "ring-1 ring-inset ring-accent/40",
			)}
			data-diff-row=""
			data-kind={row.kind}
			data-new-no={row.newNo ?? ""}
			data-old-no={row.oldNo ?? ""}
			data-row-index={rowIndex}
		>
			{target ? (
				<LineFeedbackButton
					active={composerOpen}
					onClick={() => annotation.begin(target)}
					pending={pending}
					t={t}
					target={target}
				/>
			) : null}
			<span className="w-9 shrink-0 select-none border-r border-border/50 bg-terminal px-1.5 text-right text-passive/70 tabular-nums">
				{lineNo ?? ""}
			</span>
			<span className="min-w-0 whitespace-pre-wrap break-all px-1.5">
				{row.segments ? <DiffLineSegments add={row.kind === "add"} segments={row.segments} /> : row.text || " "}
			</span>
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

function isOpenAnnotation(
	target: ActiveFileAnnotationTarget | null,
	path: string,
	side: ActiveFileAnnotationTarget["side"],
): boolean {
	return target?.path === path && target.side === side;
}

// `t` comes from the caller (which already holds one useTranslation()
// subscription) rather than calling useTranslation() here: this button is
// invisible until hover on every diff row, and a virtualized file can mount
// dozens of them per scroll tick — a separate i18n context subscription per
// row added up on large files.
function LineFeedbackButton({
	active,
	onClick,
	pending,
	t,
	target,
}: {
	active: boolean;
	onClick: () => void;
	pending: boolean;
	t: TFunction;
	target: ActiveFileAnnotationTarget;
}) {
	if (active) return null;
	const side = t(target.side === "old" ? "files.oldSide" : "files.newSide");
	const label = pending
		? t("files.editPendingLineFeedback", { file: target.path, line: target.line, side })
		: t("files.addLineFeedback", { file: target.path, line: target.line, side });
	return (
		<Button
			aria-label={label}
			className={cn(
				"absolute inset-y-0 left-6 z-20 my-auto size-6 rounded-sm border-primary/70 shadow-md shadow-black/30 transition-opacity active:translate-y-0 active:scale-100 focus-visible:opacity-100",
				pending ? "opacity-100" : "opacity-0 group-hover/line:opacity-100",
			)}
			onClick={onClick}
			size={null}
			type="button"
			variant="primary"
		>
			{pending ? <MessageSquare className="size-4" aria-hidden="true" /> : <Plus className="size-4" aria-hidden="true" />}
		</Button>
	);
}

function PendingAnnotationCard({
	annotation,
	pending,
}: {
	annotation: FileAnnotationModel;
	pending: PendingAnnotation;
}) {
	const { t } = useTranslation();
	const target = pending.target;
	const side = target.side === "file" ? "" : t(target.side === "old" ? "files.oldSide" : "files.newSide");
	const targetLabel =
		target.side === "file"
			? t("files.fileFeedbackTarget", { file: target.path })
			: t("files.lineFeedbackTarget", { file: target.path, line: target.line, side });

	return (
		<div className="border-y border-accent/30 bg-accent/5 px-3 py-2 font-sans">
			<div className="mb-1 flex items-center justify-between gap-2">
				<span className="inline-flex min-w-0 items-center gap-1.5 truncate text-caption text-accent">
					<MessageSquare className="size-icon-sm shrink-0" aria-hidden="true" />
					<span className="truncate font-mono">{targetLabel}</span>
					<span className="shrink-0 text-passive">{t("files.pendingComment")}</span>
				</span>
				<div className="flex shrink-0 items-center gap-1">
					<Button
						disabled={annotation.status === "sending"}
						onClick={() => annotation.begin(pending.target)}
						size="sm"
						type="button"
						variant="ghost"
					>
						{t("files.editPendingComment")}
					</Button>
					<Button
						aria-label={t("files.discardPendingComment")}
						disabled={annotation.status === "sending"}
						onClick={() => annotation.discardPending(pending.key)}
						size="sm"
						type="button"
						variant="ghost"
					>
						<Trash2 className="size-icon-sm" aria-hidden="true" />
						{t("files.discardPendingComment")}
					</Button>
				</div>
			</div>
			<p className="whitespace-pre-wrap text-sm text-foreground">{pending.feedback}</p>
		</div>
	);
}

function FileAnnotationComposer({ annotation }: { annotation: FileAnnotationModel }) {
	const { t } = useTranslation();
	const target = annotation.openTarget;
	if (!target) return null;
	const side = target.side === "file" ? "" : t(target.side === "old" ? "files.oldSide" : "files.newSide");
	const targetLabel =
		target.side === "file"
			? t("files.fileFeedbackTarget", { file: target.path })
			: t("files.lineFeedbackTarget", { file: target.path, line: target.line, side });
	const pendingKey = annotation.keyFor(target);
	const editingPending = Boolean(annotation.pending[pendingKey]);
	const draftEmpty = !annotation.draft.trim();
	const removeInsteadOfStage = editingPending && draftEmpty;
	const commitComposer = () => {
		if (removeInsteadOfStage) {
			annotation.discardPending(pendingKey);
			return;
		}
		annotation.stage();
	};

	return (
		<form
			className="border-y border-border/70 bg-surface px-3 py-2 font-sans"
			onSubmit={(event) => {
				event.preventDefault();
				commitComposer();
			}}
		>
			<div className="mb-1.5 flex items-center justify-between gap-2">
				<span className="min-w-0 truncate font-mono text-caption text-passive">{targetLabel}</span>
				{editingPending ? (
					<span className="shrink-0 text-caption text-accent">{t("files.editingPendingComment")}</span>
				) : null}
			</div>
			<textarea
				aria-label={t("files.feedbackLabel", { target: targetLabel })}
				autoFocus
				className="min-h-20 w-full resize-y rounded-md border border-input bg-background px-2.5 py-2 text-sm text-foreground outline-none placeholder:text-passive focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/30 disabled:opacity-60"
				disabled={annotation.status === "sending"}
				onChange={(event) => annotation.setDraft(event.target.value)}
				onKeyDown={(event) => {
					if (event.key === "Escape") {
						event.preventDefault();
						annotation.cancelComposer();
					} else if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
						event.preventDefault();
						commitComposer();
					}
				}}
				placeholder={t("files.feedbackPlaceholder")}
				value={annotation.draft}
			/>
			<div className="mt-2 flex items-center justify-end gap-1.5">
				<span className="mr-auto text-caption text-passive">{t("files.stageCommentShortcut")}</span>
				{editingPending ? (
					<Button
						aria-label={t("files.discardPendingComment")}
						disabled={annotation.status === "sending"}
						onClick={() => annotation.discardPending(pendingKey)}
						size="sm"
						type="button"
						variant="ghost"
					>
						<Trash2 className="size-icon-sm" aria-hidden="true" />
						{t("files.discardPendingComment")}
					</Button>
				) : null}
				<Button
					disabled={annotation.status === "sending"}
					onClick={annotation.cancelComposer}
					size="sm"
					type="button"
					variant="ghost"
				>
					{t("files.cancelFeedback")}
				</Button>
				<Button
					disabled={(!editingPending && draftEmpty) || annotation.status === "sending"}
					size="sm"
					type="submit"
				>
					{removeInsteadOfStage ? (
						<Trash2 className="size-icon-sm" aria-hidden="true" />
					) : (
						<MessageSquare className="size-icon-sm" aria-hidden="true" />
					)}
					{removeInsteadOfStage
						? t("files.removePendingComment")
						: editingPending
							? t("files.updatePendingComment")
							: t("files.addPendingComment")}
				</Button>
			</div>
		</form>
	);
}

function DiffLineSegments({ add, segments }: { add: boolean; segments: DiffSegment[] }) {
	return (
		<>
			{segments.map((segment, index) =>
				segment.changed ? (
					<span className={cn("rounded-sm", add ? "bg-success/35" : "bg-error/35")} key={index}>
						{segment.text}
					</span>
				) : (
					<span key={index}>{segment.text}</span>
				),
			)}
		</>
	);
}

function ChangeBadges({ additions, deletions }: { additions: number; deletions: number }) {
	return (
		<span className="flex shrink-0 items-center gap-1 font-mono text-caption font-medium">
			{additions > 0 ? <span className="px-0.5 text-success">+{additions}</span> : null}
			{deletions > 0 ? <span className="px-0.5 text-error">-{deletions}</span> : null}
		</span>
	);
}

function PanelMessage({ action, children, compact = false }: { action?: ReactNode; children: ReactNode; compact?: boolean }) {
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

function RetryButton({ onClick }: { onClick: () => void }) {
	const { t } = useTranslation();
	return (
		<Button onClick={onClick} size="sm" type="button" variant="outline">
			{t("files.retry")}
		</Button>
	);
}

function StatusMark({ status }: { status: WorkspaceFileStatus }) {
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
