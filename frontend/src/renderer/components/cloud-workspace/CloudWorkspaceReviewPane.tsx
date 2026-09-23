import { useQueries } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, ChevronsDownUp, ChevronsUpDown, FileCode2, GitCommitHorizontal, MessageSquarePlus } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { cloudWorkspaceReviewDiffsQueryOptions } from "../../hooks/useCloudWorkspaceReview";
import type { CloudCpClient, CloudCpWorkspaceReviewCommit, CloudCpWorkspaceReviewFileSummary, CloudCpWorkspaceReviewResponse, CloudCpWorkspaceReviewScope } from "../../lib/cloud-cp";
import { cn } from "../../lib/utils";
import type { FileAnnotationModel } from "../WorkspaceDiffView";
import { PanelMessage, RetryButton } from "../WorkspaceDiffView";
import { Button } from "../ui/button";
import { Checkbox } from "../ui/checkbox";
import { CloudDiffFile } from "./CloudDiffFile";
import type { CloudFileOpenOptions } from "./CloudWorkspaceExplorer";

const PATCH_BATCH_SIZE = 100;
type WorkingScope = "unstaged" | "staged" | "untracked";
const scopeOrder: WorkingScope[] = ["unstaged", "staged", "untracked"];
const statusLabel: Record<CloudCpWorkspaceReviewFileSummary["status"], string> = {
	unmodified: "", modified: "M", added: "A", deleted: "D", renamed: "R", copied: "C", untracked: "U",
};
const statusTone: Record<CloudCpWorkspaceReviewFileSummary["status"], string> = {
	unmodified: "text-passive", modified: "text-warning", added: "text-success", deleted: "text-error",
	renamed: "text-accent", copied: "text-accent", untracked: "text-success",
};

function chunk<T>(items: readonly T[]): T[][] {
	const result: T[][] = [];
	for (let index = 0; index < items.length; index += PATCH_BATCH_SIZE) result.push(items.slice(index, index + PATCH_BATCH_SIZE));
	return result;
}

function uniqueFiles(files: readonly CloudCpWorkspaceReviewFileSummary[]) {
	const seen = new Set<string>();
	return files.filter((file) => !seen.has(file.path) && Boolean(seen.add(file.path)));
}

function filesForScope(data: CloudCpWorkspaceReviewResponse, scope: CloudCpWorkspaceReviewScope) {
	if (scope === "combined") return uniqueFiles([...data.sections.unstaged, ...data.sections.staged, ...data.sections.untracked]);
	if (scope === "committed") return data.sections.committed;
	return data.sections[scope];
}

function initialSelection(data: CloudCpWorkspaceReviewResponse): { scope: CloudCpWorkspaceReviewScope; commitSha?: string } {
	for (const scope of scopeOrder) if (data.sections[scope].length > 0) return { scope };
	if (data.commits[0]) return { scope: "committed", commitSha: data.commits[0].sha };
	return { scope: "combined" };
}

function deferredByDefault(file: CloudCpWorkspaceReviewFileSummary) {
	const name = file.path.split("/").pop()?.toLowerCase() ?? "";
	return file.binary || file.size > 512 * 1024 || /^(package-lock\.json|pnpm-lock\.yaml|yarn\.lock|bun\.lockb?|go\.sum|cargo\.lock)$/.test(name);
}

export function CloudWorkspaceReviewPane({
	annotation, baseUrl, client, data, filter, onBrowseAll, onOpenFile, orgId, sessionId, split,
}: {
	annotation: FileAnnotationModel;
	baseUrl: string;
	client: CloudCpClient;
	data: CloudCpWorkspaceReviewResponse;
	filter: string;
	onBrowseAll: () => void;
	onOpenFile?: (path: string, options?: CloudFileOpenOptions) => void;
	orgId: string;
	sessionId: string;
	split: boolean;
}) {
	const { t } = useTranslation();
	const first = useMemo(() => initialSelection(data), [data]);
	const [scope, setScope] = useState<CloudCpWorkspaceReviewScope>(first.scope);
	const [commitSha, setCommitSha] = useState<string | undefined>(first.commitSha);
	const [commitBrowserOpen, setCommitBrowserOpen] = useState(false);
	const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
	const [loadedDeferred, setLoadedDeferred] = useState<Set<string>>(() => new Set());
	const selectedCommit = data.commits.find((commit) => commit.sha === commitSha);
	const allFiles = scope === "committed" && selectedCommit ? selectedCommit.files : filesForScope(data, scope);
	const normalizedFilter = filter.trim().toLowerCase();
	const files = normalizedFilter ? allFiles.filter((file) => `${file.path} ${file.previousPath ?? ""}`.toLowerCase().includes(normalizedFilter)) : allFiles;
	const selectionKey = selectedCommit ? `commit:${selectedCommit.sha}` : scope;
	const storageKey = `ao.cloud.files.viewed.${sessionId}.${selectionKey}`;
	const [viewedRecords, setViewedRecords] = useState<Record<string, string>>(() => {
		try { return JSON.parse(window.localStorage.getItem(storageKey) ?? "{}"); } catch { return {}; }
	});
	useEffect(() => {
		try { setViewedRecords(JSON.parse(window.localStorage.getItem(storageKey) ?? "{}")); } catch { setViewedRecords({}); }
	}, [storageKey]);
	useEffect(() => {
		setCollapsed(new Set(files.filter(deferredByDefault).map((file) => file.path)));
		setLoadedDeferred(new Set());
	}, [data.workspaceVersion, selectionKey]);
	useEffect(() => {
		if (scope === "committed" && selectedCommit) return;
		if (scope === "combined") return;
		if (scope !== "committed" && data.sections[scope].length > 0) return;
		setScope(first.scope); setCommitSha(first.commitSha);
	}, [data.sections, first, scope, selectedCommit]);

	const requested = files.filter((file) => !deferredByDefault(file) || loadedDeferred.has(file.path));
	const batches = chunk(requested.map((file) => file.path));
	const diffQueries = useQueries({ queries: batches.map((paths) => ({
		...cloudWorkspaceReviewDiffsQueryOptions({ client, baseUrl, orgId, sessionId, paths, scope, commitSha: selectedCommit?.sha, workspaceVersion: data.workspaceVersion, ignoreWhitespace: false }),
		staleTime: Infinity,
	})) });
	const patchByPath = useMemo(() => {
		const result = new Map<string, string>();
		for (const query of diffQueries) for (const group of query.data?.groups ?? []) for (const path of group.includedPaths) result.set(path, group.patch);
		return result;
	}, [diffQueries]);
	const deferredReasonByPath = useMemo(() => {
		const result = new Map<string, string>();
		for (const query of diffQueries) for (const group of query.data?.groups ?? []) for (const entry of group.deferred) result.set(entry.path, entry.reason);
		return result;
	}, [diffQueries]);
	const retryAll = () => diffQueries.forEach((query) => void query.refetch());
	const firstError = diffQueries.find((query) => query.error)?.error;
	const groupError = diffQueries.flatMap((query) => query.data?.groups ?? []).flatMap((group) => group.errors)[0];
	const isViewed = (file: CloudCpWorkspaceReviewFileSummary) => viewedRecords[file.path] === file.fileFingerprint;
	const toggleViewed = (file: CloudCpWorkspaceReviewFileSummary) => setViewedRecords((current) => {
		const next = { ...current };
		if (next[file.path] === file.fileFingerprint) delete next[file.path]; else next[file.path] = file.fileFingerprint;
		window.localStorage.setItem(storageKey, JSON.stringify(next));
		return next;
	});
	const selectScope = (next: CloudCpWorkspaceReviewScope) => { annotation.cancel(); setScope(next); setCommitSha(undefined); setCommitBrowserOpen(false); };
	const selectCommit = (commit: CloudCpWorkspaceReviewCommit) => { annotation.cancel(); setScope("committed"); setCommitSha(commit.sha); setCommitBrowserOpen(false); };
	const toggleCollapsed = useCallback((path: string) => setCollapsed((current) => {
		const next = new Set(current); if (next.has(path)) next.delete(path); else next.add(path); return next;
	}), []);
	const allCollapsed = files.length > 0 && files.every((file) => collapsed.has(file.path));
	const viewedCount = allFiles.filter(isViewed).length;
	const visibleScopes = scopeOrder.filter((entry) => data.sections[entry].length > 0);
	const combinedFiles = filesForScope(data, "combined");

	return <div className="flex h-full min-h-0 flex-col">
		<div className="flex shrink-0 flex-wrap items-center gap-1 border-b border-border bg-surface px-2 py-1.5">
			{combinedFiles.length > 0 ? <Button aria-pressed={scope === "combined"} onClick={() => selectScope("combined")} size="sm" variant={scope === "combined" ? "secondary" : "ghost"}>{t("files.reviewChanges")}<span className="text-caption text-passive">{combinedFiles.length}</span></Button> : null}
			{visibleScopes.map((entry) => <Button aria-pressed={scope === entry} key={entry} onClick={() => selectScope(entry)} size="sm" variant={scope === entry ? "secondary" : "ghost"}>{t(`files.section.${entry}`)}<span className="text-caption text-passive">{data.sections[entry].length}</span></Button>)}
			<Button aria-expanded={commitBrowserOpen} aria-pressed={scope === "committed"} disabled={data.commits.length === 0} onClick={() => setCommitBrowserOpen((open) => !open)} size="sm" variant={scope === "committed" ? "secondary" : "ghost"}><GitCommitHorizontal className="size-icon-sm" />{t("files.commits")}{selectedCommit ? <span className="text-caption text-passive">{selectedCommit.sha.slice(0, 7)}</span> : null}</Button>
			{!commitBrowserOpen ? <div className="ml-auto flex items-center gap-1 text-caption text-muted-foreground">
				<span>{t("files.reviewProgress", { total: allFiles.length, viewed: viewedCount })}</span>
				<Button aria-label={t(allCollapsed ? "files.expandAll" : "files.collapseAll")} onClick={() => setCollapsed(allCollapsed ? new Set() : new Set(files.map((file) => file.path)))} size="icon-sm" variant="ghost">{allCollapsed ? <ChevronsUpDown /> : <ChevronsDownUp />}</Button>
			</div> : null}
		</div>
		{commitBrowserOpen ? <CommitBrowser commits={data.commits} filter={filter} onSelect={selectCommit} selectedSha={selectedCommit?.sha} /> : <div className="board-scrollbar min-h-0 flex-1 overflow-y-auto" data-files-scroll-root>
			{firstError ? <PanelMessage action={<RetryButton onClick={retryAll} />}>{firstError.message}</PanelMessage> : null}
			{groupError ? <PanelMessage action={<RetryButton onClick={retryAll} />}>{groupError.message}</PanelMessage> : null}
			{diffQueries.some((query) => query.isPending) && patchByPath.size === 0 ? <PanelMessage compact>{t("files.loadingDiff")}</PanelMessage> : null}
			{files.length === 0 ? <PanelMessage action={allFiles.length === 0 ? <Button onClick={onBrowseAll}>{t("files.browseAll")}</Button> : undefined} compact>{allFiles.length === 0 ? t("files.noneChanged") : t("files.noFilterMatches")}</PanelMessage> : null}
			{files.map((file) => {
				const patch = patchByPath.get(file.path);
				const deferred = deferredByDefault(file) && !loadedDeferred.has(file.path);
				const reason = deferredReasonByPath.get(file.path);
				const closed = collapsed.has(file.path);
				return <section className="border-b border-border" key={`${selectionKey}:${file.path}`}>
					<header className="flex h-10 min-w-0 items-center gap-2 bg-surface px-2">
						<Button aria-label={closed ? t("files.expandFile", { file: file.path }) : t("files.collapseFile", { file: file.path })} onClick={() => toggleCollapsed(file.path)} size="icon-sm" variant="ghost">{closed ? <ChevronRight /> : <ChevronDown />}</Button>
						<span className={cn("font-mono text-xs font-semibold", statusTone[file.status])}>{statusLabel[file.status]}</span>
						<button className="min-w-0 flex-1 truncate text-left font-mono text-xs hover:underline" onClick={() => toggleCollapsed(file.path)} type="button">{file.path}</button>
						<span className="text-caption text-success">+{file.additions}</span><span className="text-caption text-error">−{file.deletions}</span>
						<Button aria-label={t("files.addFeedback")} onClick={() => annotation.begin({ path: file.path, previousPath: file.previousPath, side: "file", scope, surface: "review", workspaceVersion: data.workspaceVersion, fileFingerprint: file.fileFingerprint })} size="icon-sm" variant="ghost"><MessageSquarePlus /></Button>
						<Button aria-label={t("files.openFullFileGeneric")} onClick={() => onOpenFile?.(file.path, { commitSha: selectedCommit?.sha, mode: "file", scope })} size="icon-sm" variant="ghost"><FileCode2 /></Button>
						<Checkbox aria-label={isViewed(file) ? t("files.markUnviewed", { file: file.path }) : t("files.markViewed", { file: file.path })} checked={isViewed(file)} onCheckedChange={() => toggleViewed(file)} />
					</header>
					{!closed && patch ? <CloudDiffFile annotation={annotation} baseUrl={baseUrl} client={client} commitSha={selectedCommit?.sha} file={file} onActiveSelectionChange={() => undefined} orgId={orgId} patch={patch} scope={scope} sessionId={sessionId} split={split} workspaceVersion={data.workspaceVersion} /> : null}
					{!closed && !patch ? <div className="flex items-center gap-2 p-3 text-xs text-muted-foreground"><span className="min-w-0 flex-1">{file.binary ? t("files.binaryUnavailable") : deferred ? t("files.deferredDiff") : reason ? t("files.diffUnavailableReason", { reason }) : t("files.loadingDiff")}</span>{deferred ? <Button onClick={() => setLoadedDeferred((current) => new Set(current).add(file.path))} size="sm" variant="outline">{t("files.loadDiff")}</Button> : null}<Button onClick={() => onOpenFile?.(file.path, { commitSha: selectedCommit?.sha, mode: "file", scope })} size="sm" variant="outline">{t("files.fileView")}</Button></div> : null}
				</section>;
			})}
		</div>}
	</div>;
}

function CommitBrowser({ commits, filter, onSelect, selectedSha }: { commits: readonly CloudCpWorkspaceReviewCommit[]; filter: string; onSelect: (commit: CloudCpWorkspaceReviewCommit) => void; selectedSha?: string }) {
	const { t } = useTranslation();
	const normalized = filter.trim().toLowerCase();
	const visible = normalized ? commits.filter((commit) => `${commit.subject} ${commit.author} ${commit.sha} ${commit.files.map((file) => file.path).join(" ")}`.toLowerCase().includes(normalized)) : commits;
	return <ul aria-label={t("files.commitHistory")} className="board-scrollbar min-h-0 flex-1 overflow-y-auto">{visible.map((commit) => <li key={commit.sha}><button aria-current={selectedSha === commit.sha ? "true" : undefined} className="block w-full border-b border-border px-3 py-3 text-left hover:bg-interactive-hover" onClick={() => onSelect(commit)} type="button"><div className="flex items-center gap-2"><GitCommitHorizontal className="size-icon-sm text-passive" /><span className="min-w-0 flex-1 truncate text-xs font-medium">{commit.subject}</span><span className="font-mono text-caption text-passive">{commit.sha.slice(0, 7)}</span></div><div className="mt-1 pl-6 text-caption text-muted-foreground">{commit.author} · {t("files.count", { count: commit.files.length })}</div></button></li>)}</ul>;
}
