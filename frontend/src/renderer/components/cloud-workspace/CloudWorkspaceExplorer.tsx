import { useQuery } from "@tanstack/react-query";
import { Columns2, Maximize2, Minimize2, Rows3, Search } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useCloudCp } from "../../hooks/useCloudCp";
import { cloudWorkspaceReviewSummaryQueryOptions, useCloudWorkspaceReviewEvents } from "../../hooks/useCloudWorkspaceReview";
import type { CloudCpWorkspaceReviewFileSummary, CloudCpWorkspaceReviewScope } from "../../lib/cloud-cp";
import type { WorkspaceSession } from "../../types/workspace";
import { PanelMessage, RetryButton, type FileAnnotationModel } from "../WorkspaceDiffView";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { CloudFileTree } from "./CloudFileTree";
import { CloudWorkspaceReviewPane } from "./CloudWorkspaceReviewPane";

export type CloudFileOpenOptions = {
	scope?: CloudCpWorkspaceReviewScope;
	commitSha?: string;
	mode?: "diff" | "file" | "rendered";
	editing?: boolean;
};

type CloudWorkspaceExplorerProps = {
	annotation?: FileAnnotationModel;
	session: WorkspaceSession;
	isMaximized?: boolean;
	onOpenFile?: (path: string, options?: CloudFileOpenOptions) => void;
	onSplitChange?: (split: boolean) => void;
	onToggleMaximized?: (next: boolean) => void;
	split?: boolean;
};

const noAnnotation: FileAnnotationModel = { target: null, draft: "", status: "idle", error: "", begin: () => undefined, setDraft: () => undefined, cancel: () => undefined, submit: async () => undefined };

export function CloudWorkspaceExplorer({ annotation = noAnnotation, session, isMaximized = false, onOpenFile, onSplitChange, onToggleMaximized, split: controlledSplit }: CloudWorkspaceExplorerProps) {
	const { t } = useTranslation();
	const { client, ready, baseUrl } = useCloudCp();
	const orgId = session.cloud?.orgId;
	const enabled = ready && orgId !== undefined;
	const [showChanges, setShowChanges] = useState(true);
	const [filter, setFilter] = useState("");
	const [internalSplit, setInternalSplit] = useState(() => window.localStorage.getItem("ao.cloud.files.diffStyle") === "split");
	const split = controlledSplit ?? internalSplit;
	const [selectedPath, setSelectedPath] = useState<string | null>(null);
	const summaryQuery = useQuery({
		...cloudWorkspaceReviewSummaryQueryOptions({ client, baseUrl, orgId: orgId ?? "", sessionId: session.id, enabled, visible: true }),
	});
	useCloudWorkspaceReviewEvents({ baseUrl, orgId, sessionId: session.id, enabled });
	useEffect(() => { window.localStorage.setItem("ao.cloud.files.diffStyle", split ? "split" : "unified"); }, [split]);
	useEffect(() => { setFilter(""); setSelectedPath(null); setShowChanges(true); }, [session.id]);
	const changedFiles = useMemo(() => {
		const data = summaryQuery.data;
		if (!data) return [];
		const seen = new Set<string>();
		const result: Array<{ file: CloudCpWorkspaceReviewFileSummary; scope: CloudCpWorkspaceReviewScope }> = [];
		for (const [scope, files] of [["unstaged", data.sections.unstaged], ["staged", data.sections.staged], ["untracked", data.sections.untracked], ["committed", data.sections.committed]] as const) {
			for (const file of files) if (!seen.has(file.path)) { seen.add(file.path); result.push({ file, scope }); }
		}
		return result;
	}, [summaryQuery.data]);
	const open = (path: string, options?: CloudFileOpenOptions) => { setSelectedPath(path); onOpenFile?.(path, options); };

	return (
		<section aria-label={t("files.sessionFiles")} className="flex h-full min-h-0 flex-col bg-background text-foreground">
			<header className="flex h-10 shrink-0 items-center gap-0.5 border-b border-border bg-surface px-2">
				<label className="relative mr-1 min-w-0 flex-1"><Search className="pointer-events-none absolute left-2.5 top-1/2 size-icon-sm -translate-y-1/2 text-passive" /><Input aria-label={t("files.explorer.filter")} className="h-8 pl-8 font-mono text-xs" onChange={(event) => setFilter(event.target.value)} placeholder={t("files.explorer.filterPlaceholder")} value={filter} /></label>
				{changedFiles.length > 0 ? <div aria-label={t("files.viewMode")} className="flex items-center rounded-md border border-border bg-muted/30 p-0.5" role="tablist">
					<Button aria-selected={showChanges} className="h-6 px-2 text-2xs" onClick={() => setShowChanges(true)} role="tab" size="sm" variant={showChanges ? "secondary" : "ghost"}>{t("files.reviewChanges")}</Button>
					<Button aria-selected={!showChanges} className="h-6 px-2 text-2xs" onClick={() => setShowChanges(false)} role="tab" size="sm" variant={!showChanges ? "secondary" : "ghost"}>{t("files.allFiles")}</Button>
				</div> : null}
				{showChanges ? <Tooltip><TooltipTrigger asChild><Button aria-label={split ? t("files.unifiedDiff") : t("files.splitDiff")} aria-pressed={split} onClick={() => { const next = !split; if (controlledSplit === undefined) setInternalSplit(next); onSplitChange?.(next); }} size="icon-sm" variant="ghost">{split ? <Columns2 className="size-icon-sm" /> : <Rows3 className="size-icon-sm" />}</Button></TooltipTrigger><TooltipContent>{split ? t("files.unifiedDiff") : t("files.splitDiff")}</TooltipContent></Tooltip> : null}
				{onToggleMaximized ? <Tooltip><TooltipTrigger asChild><Button aria-label={isMaximized ? t("files.minimize") : t("files.maximize")} onClick={() => onToggleMaximized(!isMaximized)} size="icon-sm" variant="ghost">{isMaximized ? <Minimize2 className="size-icon-sm" /> : <Maximize2 className="size-icon-sm" />}</Button></TooltipTrigger><TooltipContent>{isMaximized ? t("files.minimize") : t("files.maximize")}</TooltipContent></Tooltip> : null}
			</header>
			{!enabled || summaryQuery.isPending ? <PanelMessage>{t("files.loading")}</PanelMessage> : summaryQuery.isError ? <PanelMessage action={<RetryButton onClick={() => void summaryQuery.refetch()} />}>{summaryQuery.error.message}</PanelMessage> : showChanges ? (
				<CloudWorkspaceReviewPane annotation={annotation} baseUrl={baseUrl} client={client} data={summaryQuery.data} filter={filter} onBrowseAll={() => setShowChanges(false)} onOpenFile={open} orgId={orgId!} sessionId={session.id} split={split} />
			) : <CloudFileTree baseUrl={baseUrl} client={client} filterText={filter} onOpenFile={(path) => open(path, { mode: "file" })} orgId={orgId!} selectedPath={selectedPath} sessionId={session.id} />}
		</section>
	);
}
