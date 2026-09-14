import { useEffect, useMemo, useState } from "react";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { AnimatePresence, motion } from "motion/react";
import { parsePatchFiles } from "@pierre/diffs";
import { CodeView } from "@pierre/diffs/react";
import { useTranslation } from "react-i18next";
import { useCloudCp } from "../hooks/useCloudCp";
import type { CloudCpWorkspaceDiffFile, CloudCpWorkspaceDiffFileDetail } from "../lib/cloud-cp";
import { cn } from "../lib/utils";
import type { WorkspaceSession } from "../types/workspace";
import { PanelMessage, RetryButton } from "./WorkspaceDiffView";
import { AO_PIERRE_SURFACE_CSS } from "./diffs/pierreTheme";

type CloudWorkspaceDiffProps = {
	session: WorkspaceSession;
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
 * Cloud Files tab for Docker sandboxes. Cloud workspaces are remote, so they
 * cannot use the daemon-only file explorer; this deliberately consumes the
 * control-plane review endpoints instead. NodeOps and Coder never mount it.
 */
export function CloudWorkspaceDiff({ session }: CloudWorkspaceDiffProps) {
	const { t } = useTranslation();
	const { client, ready, baseUrl } = useCloudCp();
	const cloud = session.cloud;
	const docker = cloud?.sandboxProvider === "docker";
	const orgId = cloud?.orgId;
	const [selectedPath, setSelectedPath] = useState<string | undefined>();
	const [view, setView] = useState<"files" | "diff">("files");
	const [category, setCategory] = useState<"uncommitted" | "unpushed" | "pushed">("uncommitted");
	const enabled = ready && docker && orgId !== undefined;
	const diffQuery = useQuery({
		queryKey: ["cloud-workspace-diff", baseUrl, orgId ?? "", session.id],
		enabled,
		refetchInterval: 5_000,
		queryFn: () => client.getWorkspaceDiff(orgId!, session.id),
	});
	const detailQuery = useQuery({
		queryKey: ["cloud-workspace-diff-file", baseUrl, orgId ?? "", session.id, category, selectedPath ?? ""],
		enabled: enabled && selectedPath !== undefined,
		queryFn: () => client.readWorkspaceDiffFile(orgId!, session.id, selectedPath!, category),
	});
	const categories = diffQuery.data?.categories ?? { uncommitted: { files: diffQuery.data?.files ?? [] } };
	const files = categories[category]?.files ?? [];
	const summary = useMemo(
		() =>
			files.reduce(
				(total, file) => ({ additions: total.additions + file.additions, deletions: total.deletions + file.deletions }),
				{ additions: 0, deletions: 0 },
			),
		[files],
	);

	useEffect(() => {
		if (selectedPath !== undefined && !files.some((file) => file.path === selectedPath)) {
			setSelectedPath(undefined);
			setView("files");
		}
	}, [files, selectedPath]);

	if (!docker) {
		return <PanelMessage>{t("files.noneChanged")}</PanelMessage>;
	}

	return (
		<section className="flex h-full min-h-0 flex-col bg-background text-foreground" aria-label={t("files.sessionFiles")}>
			<header className="flex h-10 shrink-0 items-center gap-2 border-b border-border bg-surface px-3">
				<div className="flex min-w-0 flex-1 items-center gap-1" role="tablist" aria-label={t("files.reviewChanges")}>
					<button
						aria-selected={view === "files"}
						className={cn("rounded px-1.5 py-1 text-sm font-medium", view === "files" ? "bg-muted text-foreground" : "text-muted-foreground hover:text-foreground")}
						onClick={() => setView("files")}
						role="tab"
						type="button"
					>
						{t("files.file")}
					</button>
					<button
						aria-selected={view === "diff"}
						className={cn("rounded px-1.5 py-1 text-sm font-medium", view === "diff" ? "bg-muted text-foreground" : "text-muted-foreground hover:text-foreground")}
						disabled={selectedPath === undefined}
						onClick={() => setView("diff")}
						role="tab"
						type="button"
					>
						{t("files.diff")}
					</button>
				</div>
				<span aria-label={t("files.reviewChanges")} className="shrink-0 font-mono text-2xs text-passive">
					{files.length} {files.length === 1 ? "file" : "files"} · <span className="text-success">+{summary.additions}</span>{" "}
					<span className="text-error">-{summary.deletions}</span>
				</span>
			</header>
			<div className="flex shrink-0 gap-1 border-b border-border px-2 py-1.5" aria-label={t("files.reviewChanges")}>
				{(["uncommitted", "unpushed", "pushed"] as const).map((value) => (
					<button className={cn("rounded px-2 py-1 text-2xs", category === value ? "bg-muted font-medium" : "text-muted-foreground")} key={value} onClick={() => { setCategory(value); setSelectedPath(undefined); setView("files"); }} type="button">
						{value === "uncommitted" ? "Uncommitted" : value === "unpushed" ? "Unpushed" : "Pushed"} {categories[value]?.files.length ?? 0}
					</button>
				))}
			</div>
			{diffQuery.isPending ? (
				<PanelMessage>{t("files.loading")}</PanelMessage>
			) : diffQuery.isError ? (
				<PanelMessage action={<RetryButton onClick={() => void diffQuery.refetch()} />}>{diffQuery.error.message}</PanelMessage>
			) : files.length === 0 ? (
				<PanelMessage>{t("files.noneChanged")}</PanelMessage>
			) : (
				<AnimatePresence initial={false} mode="wait">
					{view === "files" ? (
					<motion.div animate={{ opacity: 1, x: 0 }} className="board-scrollbar min-h-0 flex-1 overflow-y-auto" exit={{ opacity: 0, x: -12 }} initial={{ opacity: 0, x: 12 }} key="files" role="tabpanel" transition={{ duration: 0.16 }}>
						{files.map((file) => (
							<button
								className={cn(
									"flex w-full items-center gap-2 border-b border-border/60 px-3 py-2 text-left font-mono text-xs hover:bg-muted",
									selectedPath === file.path && "bg-muted",
								)}
								key={file.path}
								onClick={() => {
									setSelectedPath(file.path);
									setView("diff");
								}}
								type="button"
							>
								<span className={cn("w-3 shrink-0 font-semibold", statusTone[file.status])}>{statusLabel[file.status]}</span>
								<span className="min-w-0 flex-1 truncate">{file.path}</span>
								<span className="shrink-0 text-success">+{file.additions}</span>
								<span className="shrink-0 text-error">-{file.deletions}</span>
							</button>
						))}
					</motion.div>
					) : <motion.div animate={{ opacity: 1, x: 0 }} className="min-h-0 flex-1" exit={{ opacity: 0, x: 12 }} initial={{ opacity: 0, x: -12 }} key="diff" transition={{ duration: 0.16 }}><CloudDiffDetail detailQuery={detailQuery} /></motion.div>}
				</AnimatePresence>
			)}
		</section>
	);
}

function CloudDiffDetail({ detailQuery }: { detailQuery: UseQueryResult<CloudCpWorkspaceDiffFileDetail, Error> }) {
	const { t } = useTranslation();
	if (detailQuery.isPending) return <PanelMessage>{t("files.loadingDiff")}</PanelMessage>;
	if (detailQuery.isError) return <PanelMessage action={<RetryButton onClick={() => void detailQuery.refetch()} />}>{detailQuery.error.message}</PanelMessage>;
	if (!detailQuery.data) return <PanelMessage>{t("files.explorer.selectFile")}</PanelMessage>;
	if (detailQuery.data.binary) return <PanelMessage>{t("files.binaryUnavailable")}</PanelMessage>;
	const diff = detailQuery.data.diff;
	const metadata = parsePatchFiles(diff, `cloud:${detailQuery.data.path}`, true).flatMap((entry) => entry.files);
	if (metadata.length === 0) return <PanelMessage>{t("files.deferredDiff")}</PanelMessage>;
	return <CodeView className="ao-pierre-surface board-scrollbar h-full overflow-y-auto" disableWorkerPool={typeof Worker === "undefined"} items={metadata.map((file) => ({ id: file.name, type: "diff" as const, fileDiff: file }))} options={{ collapsedContextThreshold: 8, diffIndicators: "classic", diffStyle: "unified", expansionLineCount: 20, loadDiffFiles: async (file) => {
		const oldFile = detailQuery.data.status === "added" || detailQuery.data.status === "untracked" ? null : { name: file.prevName ?? file.name, contents: detailQuery.data.baseContent, cacheKey: `${detailQuery.data.path}:base` };
		const newFile = detailQuery.data.deleted ? null : { name: file.name, contents: detailQuery.data.content, cacheKey: `${detailQuery.data.path}:current` };
		if (file.type === "rename-pure") return { oldFile: null, newFile };
		return { oldFile, newFile };
	}, overflow: "wrap", stickyHeaders: true, theme: { dark: "github-dark", light: "github-light" }, unsafeCSS: AO_PIERRE_SURFACE_CSS }} />;
}
