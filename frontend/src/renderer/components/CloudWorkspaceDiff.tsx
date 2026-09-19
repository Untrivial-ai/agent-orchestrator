import { useEffect, useMemo, useState } from "react";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { Maximize2, Minimize2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useCloudCp } from "../hooks/useCloudCp";
import type { CloudCpWorkspaceDiffFile, CloudCpWorkspaceDiffFileDetail } from "../lib/cloud-cp";
import { cn } from "../lib/utils";
import type { WorkspaceSession } from "../types/workspace";
import { PanelMessage, RetryButton } from "./WorkspaceDiffView";
import { Button } from "./ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type CloudWorkspaceDiffProps = {
	session: WorkspaceSession;
	isMaximized?: boolean;
	onOpenFile?: (path: string) => void;
	onToggleMaximized?: (next: boolean) => void;
};

const statusLabel: Record<CloudCpWorkspaceDiffFile["status"], string> = {
	unmodified: "",
	modified: "M",
	added: "A",
	deleted: "D",
	renamed: "R",
	untracked: "U",
	copied: "C",
	changed: "M",
};

const statusTone: Record<CloudCpWorkspaceDiffFile["status"], string> = {
	unmodified: "text-passive",
	modified: "text-warning",
	added: "text-success",
	deleted: "text-error",
	renamed: "text-accent",
	untracked: "text-success",
	copied: "text-accent",
	changed: "text-warning",
};

/**
 * Cloud Files tab. Cloud workspaces are remote, so they cannot use the
 * daemon-only file explorer; this deliberately consumes the control-plane
 * review endpoints shared by every cloud provider.
 */
export function CloudWorkspaceDiff({ session, isMaximized = false, onOpenFile, onToggleMaximized }: CloudWorkspaceDiffProps) {
	const { t } = useTranslation();
	const { client, ready, baseUrl } = useCloudCp();
	const cloud = session.cloud;
	const orgId = cloud?.orgId;
	const [selectedPath, setSelectedPath] = useState<string | undefined>();
	const enabled = ready && orgId !== undefined;
	const diffQuery = useQuery({
		queryKey: ["cloud-workspace-diff", baseUrl, orgId ?? "", session.id],
		enabled,
		refetchInterval: 5_000,
		queryFn: () => client.getWorkspaceDiff(orgId!, session.id),
	});
	const detailQuery = useQuery({
		queryKey: ["cloud-workspace-diff-file", baseUrl, orgId ?? "", session.id, selectedPath ?? ""],
		enabled: enabled && selectedPath !== undefined,
		queryFn: () => client.readWorkspaceDiffFile(orgId!, session.id, selectedPath!),
	});
	const files = diffQuery.data?.files ?? [];
	const summary = useMemo(
		() =>
			files.reduce(
				(total, file) => ({ additions: total.additions + file.additions, deletions: total.deletions + file.deletions }),
				{ additions: 0, deletions: 0 },
			),
		[files],
	);

	useEffect(() => {
		if (selectedPath !== undefined && !files.some((file) => file.path === selectedPath)) setSelectedPath(undefined);
	}, [files, selectedPath]);

	return (
		<section className="flex h-full min-h-0 flex-col bg-background text-foreground" aria-label={t("files.sessionFiles")}>
			<header className="flex h-10 shrink-0 items-center gap-2 border-b border-border bg-surface px-3">
				<span className="min-w-0 flex-1 truncate text-sm font-medium">{t("files.reviewChanges")}</span>
				{enabled && diffQuery.data ? (
					<span aria-label="Cloud diff summary" className="shrink-0 font-mono text-2xs text-passive">
						{files.length} {files.length === 1 ? "file" : "files"} · <span className="text-success">+{summary.additions}</span>{" "}
						<span className="text-error">-{summary.deletions}</span>
					</span>
				) : null}
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
								{isMaximized ? <Minimize2 className="size-icon-sm" /> : <Maximize2 className="size-icon-sm" />}
							</Button>
						</TooltipTrigger>
						<TooltipContent side="bottom">{isMaximized ? t("files.minimize") : t("files.maximize")}</TooltipContent>
					</Tooltip>
				) : null}
			</header>
			{diffQuery.isPending ? (
				<PanelMessage>{t("files.loading")}</PanelMessage>
			) : diffQuery.isError ? (
				<PanelMessage action={<RetryButton onClick={() => void diffQuery.refetch()} />}>{diffQuery.error.message}</PanelMessage>
			) : files.length === 0 ? (
				<PanelMessage>{t("files.noneChanged")}</PanelMessage>
			) : (
				<div className={cn("grid min-h-0 flex-1", isMaximized && "grid-cols-[minmax(18rem,30%)_1fr]")}>
					<div className="board-scrollbar min-h-0 overflow-y-auto border-r border-border">
						{files.map((file) => (
							<button
								className={cn(
									"flex w-full items-center gap-2 border-b border-border/60 px-3 py-2 text-left font-mono text-xs hover:bg-muted",
									selectedPath === file.path && "bg-muted",
								)}
								key={file.path}
								onClick={() => {
									setSelectedPath(file.path);
									if (!isMaximized) onOpenFile?.(file.path);
								}}
								type="button"
							>
								<span className={cn("w-3 shrink-0 font-semibold", statusTone[file.status])}>{statusLabel[file.status]}</span>
								<span className="min-w-0 flex-1 truncate">{file.path}</span>
								<span className="shrink-0 text-success">+{file.additions}</span>
								<span className="shrink-0 text-error">-{file.deletions}</span>
							</button>
						))}
					</div>
					{isMaximized ? <CloudDiffDetail detailQuery={detailQuery} /> : null}
				</div>
			)}
		</section>
	);
}

/**
 * Read-only cloud equivalent of the local center file pane. It intentionally
 * uses only the control-plane diff-file contract: cloud sessions must never
 * fall through to the local daemon's workspace-file endpoints.
 */
export function CloudFileContentPane({ path, session }: { path: string; session: WorkspaceSession }) {
	const { t } = useTranslation();
	const { client, ready, baseUrl } = useCloudCp();
	const cloud = session.cloud;
	const orgId = cloud?.orgId;
	const [mode, setMode] = useState<"diff" | "file">("file");
	const detailQuery = useQuery({
		queryKey: ["cloud-workspace-diff-file", baseUrl, orgId ?? "", session.id, path],
		enabled: ready && orgId !== undefined,
		queryFn: () => client.readWorkspaceDiffFile(orgId!, session.id, path),
	});

	useEffect(() => setMode("file"), [path, session.id]);

	if (detailQuery.isPending) return <PanelMessage>{t("files.loadingDiff")}</PanelMessage>;
	if (detailQuery.isError) {
		return <PanelMessage action={<RetryButton onClick={() => void detailQuery.refetch()} />}>{detailQuery.error.message}</PanelMessage>;
	}
	if (!detailQuery.data) return <PanelMessage>{t("files.error.loadFile")}</PanelMessage>;

	const detail = detailQuery.data;
	const canShowDiff = detail.status !== "unmodified";
	const content = detail.binary
		? <PanelMessage>{t("files.binaryUnavailable")}</PanelMessage>
		: detail.deleted
			? <PanelMessage>No current text content is available for this deleted file.</PanelMessage>
			: detail.contentTruncated
				? <PanelMessage>File content is too large to display.</PanelMessage>
				: <pre className="board-scrollbar min-h-0 overflow-auto whitespace-pre bg-background p-3 font-mono text-xs leading-5">{detail.content}</pre>;
	const diff = detail.binary
		? <PanelMessage>{t("files.binaryUnavailable")}</PanelMessage>
		: <pre className="board-scrollbar min-h-0 overflow-auto whitespace-pre bg-background p-3 font-mono text-xs leading-5">{detail.diff || "No text diff is available for this file."}</pre>;

	return (
		<section className="flex h-full min-h-0 flex-col bg-background text-foreground" data-testid="cloud-file-content-pane">
			<header className="flex min-h-9 shrink-0 items-center gap-2 border-b border-border bg-surface px-2 py-1">
				{canShowDiff ? (
					<div aria-label={t("files.fileDisplayMode")} className="flex items-center" role="tablist">
						<Button
							aria-selected={mode === "diff"}
							className="h-6 rounded px-2 text-2xs"
							onClick={() => setMode("diff")}
							role="tab"
							size="sm"
							type="button"
							variant={mode === "diff" ? "secondary" : "ghost"}
						>
							{t("files.diff")}
						</Button>
						<Button
							aria-selected={mode === "file"}
							className="h-6 rounded px-2 text-2xs"
							onClick={() => setMode("file")}
							role="tab"
							size="sm"
							type="button"
							variant={mode === "file" ? "secondary" : "ghost"}
						>
							{t("files.fileView")}
						</Button>
					</div>
				) : (
					<span className="min-w-0 truncate px-2 text-xs text-foreground" title={path}>{path.split("/").pop() || path}</span>
				)}
			</header>
			<div className="min-h-0 flex-1 overflow-auto">{mode === "diff" && canShowDiff ? diff : content}</div>
		</section>
	);
}

function CloudDiffDetail({ detailQuery }: { detailQuery: UseQueryResult<CloudCpWorkspaceDiffFileDetail, Error> }) {
	if (detailQuery.isPending) return <PanelMessage>Loading diff…</PanelMessage>;
	if (detailQuery.isError) return <PanelMessage action={<RetryButton onClick={() => void detailQuery.refetch()} />}>{detailQuery.error.message}</PanelMessage>;
	if (!detailQuery.data) return <PanelMessage>Select a changed file to review its patch.</PanelMessage>;
	if (detailQuery.data.binary) return <PanelMessage>Binary file; no text diff is available.</PanelMessage>;
	return (
		<pre className="board-scrollbar min-h-0 overflow-auto whitespace-pre bg-background p-3 font-mono text-xs leading-5">
			{detailQuery.data.diff || "No text diff is available for this file."}
		</pre>
	);
}
