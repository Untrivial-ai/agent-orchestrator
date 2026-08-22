import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Columns2, Maximize2, Minimize2, Rows3, Search } from "lucide-react";
import { formatFileAnnotationMessage } from "../../shared/file-annotations";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { sessionWorkspaceFilesQueryOptions } from "../hooks/useSessionWorkspaceFiles";
import { buildChangedOnlyTree } from "../hooks/useSessionWorkspaceTree";
import { useUiStore } from "../stores/ui-store";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Switch } from "./ui/switch";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "./ui/resizable";
import { FileTree } from "./FileTree";
import { FileContentPane } from "./FileContentPane";
import type {
	ActiveFileAnnotationTarget,
	FileAnnotationModel,
	FileAnnotationStatus,
} from "./WorkspaceDiffView";

type SessionFileExplorerProps = {
	sessionId: string;
	isMaximized?: boolean;
	onToggleMaximized?: (next: boolean) => void;
};

export function SessionFileExplorer({ sessionId, isMaximized = false, onToggleMaximized }: SessionFileExplorerProps) {
	const { t } = useTranslation();
	const [filter, setFilter] = useState("");
	const [split, setSplit] = useState(false);
	const [selectedPath, setSelectedPath] = useState<string | null>(null);
	const [annotationTarget, setAnnotationTarget] = useState<ActiveFileAnnotationTarget | null>(null);
	const [annotationDraft, setAnnotationDraft] = useState("");
	const [annotationStatus, setAnnotationStatus] = useState<FileAnnotationStatus>("idle");
	const [annotationError, setAnnotationError] = useState("");
	const annotationGenerationRef = useRef(0);
	const annotationSentTimerRef = useRef<number | null>(null);
	const rootRef = useRef<HTMLElement>(null);

	const changedOnly = useUiStore((state) => Boolean(state.inspectorSessions[sessionId]?.filesChangedOnly));
	const setFilesChangedOnly = useUiStore((state) => state.setFilesChangedOnly);

	const filesQuery = useQuery({
		...sessionWorkspaceFilesQueryOptions(sessionId, t("files.error.loadWorkspace")),
		enabled: changedOnly,
	});
	const changedOnlyData = useMemo(
		() => (filesQuery.data ? buildChangedOnlyTree(filesQuery.data.files) : []),
		[filesQuery.data],
	);

	useEffect(() => {
		annotationGenerationRef.current += 1;
		setSelectedPath(null);
		setFilter("");
		setAnnotationTarget(null);
		setAnnotationDraft("");
		setAnnotationStatus("idle");
		setAnnotationError("");
	}, [sessionId]);

	useEffect(
		() => () => {
			if (annotationSentTimerRef.current !== null) window.clearTimeout(annotationSentTimerRef.current);
		},
		[],
	);

	// Routes vertical wheel scroll landing on the diff's own horizontal
	// scrollbar back up to the shared scroll root, so scrolling down over a
	// long unwrapped line still scrolls the file instead of doing nothing.
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
		annotationGenerationRef.current += 1;
		if (annotationSentTimerRef.current !== null) window.clearTimeout(annotationSentTimerRef.current);
		annotationSentTimerRef.current = null;
		setAnnotationTarget(target);
		setAnnotationDraft("");
		setAnnotationStatus("idle");
		setAnnotationError("");
	};
	const cancelAnnotation = () => {
		annotationGenerationRef.current += 1;
		setAnnotationTarget(null);
		setAnnotationDraft("");
		setAnnotationStatus("idle");
		setAnnotationError("");
	};
	const submitAnnotation = async () => {
		if (!annotationTarget || !annotationDraft.trim() || annotationStatus === "sending") return;
		const sendGeneration = annotationGenerationRef.current;
		const sendTarget = annotationTarget;
		const sendFeedback = annotationDraft;
		setAnnotationStatus("sending");
		setAnnotationError("");
		try {
			const { error } = await apiClient.POST("/api/v1/sessions/{sessionId}/send", {
				params: { path: { sessionId } },
				body: { message: formatFileAnnotationMessage(sendTarget, sendFeedback) },
			});
			if (sendGeneration !== annotationGenerationRef.current) return;
			if (error) throw new Error(apiErrorMessage(error, t("files.feedbackError")));
			setAnnotationStatus("sent");
			annotationSentTimerRef.current = window.setTimeout(() => {
				annotationSentTimerRef.current = null;
				cancelAnnotation();
			}, 1_200);
		} catch (error) {
			if (sendGeneration !== annotationGenerationRef.current) return;
			setAnnotationStatus("error");
			setAnnotationError(apiErrorMessage(error, t("files.feedbackError")));
		}
	};
	const annotation: FileAnnotationModel = {
		target: annotationTarget,
		draft: annotationDraft,
		status: annotationStatus,
		error: annotationError,
		begin: beginAnnotation,
		setDraft: setAnnotationDraft,
		cancel: cancelAnnotation,
		submit: submitAnnotation,
	};

	return (
		<section
			ref={rootRef}
			className="flex h-full min-h-0 flex-col bg-background text-foreground"
			aria-label={t("files.sessionFiles")}
		>
			<header className="flex h-10 shrink-0 items-center gap-0.5 border-b border-border bg-surface px-2">
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
				<label className="flex shrink-0 items-center gap-1.5 px-1.5 text-2xs text-muted-foreground">
					<Switch
						aria-label={t("files.explorer.changedOnly")}
						checked={changedOnly}
						onCheckedChange={(next) => setFilesChangedOnly(sessionId, next)}
						size="sm"
					/>
					{t("files.explorer.changedOnly")}
				</label>
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
			</header>
			<ResizablePanelGroup className="min-h-0 flex-1">
				<ResizablePanel defaultSize={30} minSize={15} maxSize={60}>
					<FileTree
						changedOnly={changedOnly}
						changedOnlyData={changedOnlyData}
						filterText={filter}
						onSelectPath={(node) => setSelectedPath(node.path)}
						selectedPath={selectedPath}
						sessionId={sessionId}
					/>
				</ResizablePanel>
				<ResizableHandle />
				<ResizablePanel defaultSize={70} minSize={40}>
					<div
						className="board-scrollbar h-full min-h-0 overflow-x-hidden overflow-y-auto overscroll-contain bg-background"
						data-files-scroll-root=""
					>
						<div className="flex w-full flex-col px-0">
							<FileContentPane annotation={annotation} path={selectedPath} sessionId={sessionId} split={split} wrap={true} />
						</div>
					</div>
				</ResizablePanel>
			</ResizablePanelGroup>
		</section>
	);
}
