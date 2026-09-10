import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
	ChevronLeft,
	Columns2,
	Maximize2,
	Minimize2,
	PanelTopOpen,
	Rows3,
	Search,
} from "lucide-react";
import { cn } from "../lib/utils";
import { sessionWorkspaceFilesQueryOptions } from "../hooks/useSessionWorkspaceFiles";
import { buildChangedOnlyTree, type TreeNode } from "../hooks/useSessionWorkspaceTree";
import { useFileAnnotation } from "../hooks/useFileAnnotation";
import { useUiStore } from "../stores/ui-store";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Switch } from "./ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "./ui/resizable";
import { FileTree } from "./FileTree";
import { FileContentPane } from "./FileContentPane";

type SessionFileExplorerProps = {
	sessionId: string;
	isMaximized?: boolean;
	onOpenFile?: (path: string) => void;
	onToggleMaximized?: (next: boolean) => void;
	revealRequest?: { path: string; key: number } | null;
};

export function SessionFileExplorer({
	sessionId,
	isMaximized = false,
	onOpenFile,
	onToggleMaximized,
	revealRequest,
}: SessionFileExplorerProps) {
	const { t } = useTranslation();
	const [filter, setFilter] = useState("");
	const [split, setSplit] = useState(false);
	const [selectedPath, setSelectedPath] = useState<string | null>(null);
	const rootRef = useRef<HTMLElement>(null);
	const annotation = useFileAnnotation(sessionId);

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
		setSelectedPath(null);
		setFilter("");
	}, [sessionId]);

	useEffect(() => {
		if (revealRequest) setSelectedPath(revealRequest.path);
	}, [revealRequest]);

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

	const handleSelectPath = (node: TreeNode) => {
		setSelectedPath(node.path);
	};
	const treeSelectedPath = selectedPath;

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
					/>
					{t("files.explorer.changedOnly")}
				</label>
				<Tooltip>
					<TooltipTrigger asChild>
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
					</TooltipTrigger>
					<TooltipContent side="bottom">{split ? t("files.unifiedDiff") : t("files.splitDiff")}</TooltipContent>
				</Tooltip>
				{onToggleMaximized ? (
					<Tooltip>
						<TooltipTrigger asChild>
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
						</TooltipTrigger>
						<TooltipContent side="bottom">{isMaximized ? t("files.minimize") : t("files.maximize")}</TooltipContent>
					</Tooltip>
				) : null}
			</header>
			{isMaximized ? (
				// Maximized gives the explorer the full window — plenty of room for
				// the tree and the content side by side, like a real editor.
				<ResizablePanelGroup className="min-h-0 flex-1">
					<ResizablePanel defaultSize="26%" minSize="18%" maxSize="50%">
						<FileTree
							changedOnly={changedOnly}
							changedOnlyData={changedOnlyData}
							filterText={filter}
							onSelectPath={handleSelectPath}
							selectedPath={treeSelectedPath}
							sessionId={sessionId}
						/>
					</ResizablePanel>
					<ResizableHandle />
					<ResizablePanel defaultSize="74%" minSize="40%">
						<ContentScrollArea>
							<FileContentPane annotation={annotation} path={selectedPath} sessionId={sessionId} split={split} wrap={true} />
						</ContentScrollArea>
					</ResizablePanel>
				</ResizablePanelGroup>
			) : (
				// Docked in the 316px inspector rail there isn't room for the tree
				// and the content side by side (see git history for the version that
				// tried — file names truncated to a few characters). Show one at a
				// time instead, like a narrow-window master/detail view: the tree
				// stays mounted (not unmounted) so its scroll position and expanded
				// folders survive going back and forth.
				<div className="flex min-h-0 flex-1 flex-col">
					<div className={cn("min-h-0 flex-1", selectedPath && "hidden")}>
						<FileTree
							changedOnly={changedOnly}
							changedOnlyData={changedOnlyData}
							filterText={filter}
							onSelectPath={handleSelectPath}
							selectedPath={treeSelectedPath}
							sessionId={sessionId}
						/>
					</div>
					{selectedPath ? (
						<div className="flex min-h-0 flex-1 flex-col">
							<div className="flex h-8 shrink-0 items-center gap-1 border-b border-border bg-surface px-1">
								<Button
									aria-label={t("files.explorer.backToTree")}
									onClick={() => setSelectedPath(null)}
									size="icon-sm"
									type="button"
									variant="ghost"
								>
									<ChevronLeft className="size-icon-sm" aria-hidden="true" />
								</Button>
								<span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground">
									{selectedPath}
								</span>
								{onOpenFile ? (
									<Tooltip>
										<TooltipTrigger asChild>
											<Button
												aria-label={t("files.openInCenter", { path: selectedPath })}
												onClick={() => onOpenFile(selectedPath)}
												size="icon-sm"
												type="button"
												variant="ghost"
											>
												<PanelTopOpen className="size-icon-sm" aria-hidden="true" />
											</Button>
										</TooltipTrigger>
										<TooltipContent side="bottom">
											{t("files.openInCenter", { path: selectedPath })}
										</TooltipContent>
									</Tooltip>
								) : null}
							</div>
							<ContentScrollArea>
								<FileContentPane annotation={annotation} path={selectedPath} sessionId={sessionId} split={split} wrap={true} />
							</ContentScrollArea>
						</div>
					) : null}
				</div>
			)}
		</section>
	);
}

function ContentScrollArea({ children }: { children: ReactNode }) {
	return (
		<div
			className="board-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain bg-background"
			data-files-scroll-root=""
		>
			<div className="flex w-full flex-col px-0">{children}</div>
		</div>
	);
}
