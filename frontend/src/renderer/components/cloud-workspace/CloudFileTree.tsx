import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, File, Folder } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { cloudWorkspaceReviewSearchQueryOptions, cloudWorkspaceReviewTreeQueryOptions } from "../../hooks/useCloudWorkspaceReview";
import type { CloudCpClient, CloudCpWorkspaceReviewStatus } from "../../lib/cloud-cp";
import { cn } from "../../lib/utils";
import { PanelMessage, RetryButton } from "../WorkspaceDiffView";

type CloudFileTreeProps = {
	client: CloudCpClient;
	baseUrl: string;
	orgId: string;
	sessionId: string;
	filterText?: string;
	onOpenFile: (path: string) => void;
	selectedPath?: string | null;
};

const statusLabel: Partial<Record<CloudCpWorkspaceReviewStatus, string>> = {
	modified: "M", added: "A", deleted: "D", renamed: "R", copied: "C", untracked: "U",
};

export function CloudFileTree(props: CloudFileTreeProps) {
	const filter = props.filterText?.trim() ?? "";
	if (filter !== "") return <CloudFileSearchResults {...props} filterText={filter} />;
	return <CloudTreeLevel {...props} path="" depth={0} />;
}

function CloudTreeLevel(props: CloudFileTreeProps & { path: string; depth: number }) {
	const { t } = useTranslation();
	const query = useQuery(cloudWorkspaceReviewTreeQueryOptions({
		client: props.client, baseUrl: props.baseUrl, orgId: props.orgId, sessionId: props.sessionId, path: props.path || undefined,
	}));
	if (query.isPending) return props.depth === 0 ? <PanelMessage>{t("files.loading")}</PanelMessage> : null;
	if (query.isError) return props.depth === 0 ? <PanelMessage action={<RetryButton onClick={() => void query.refetch()} />}>{query.error.message}</PanelMessage> : null;
	return (
		<div className={props.depth === 0 ? "board-scrollbar min-h-0 flex-1 overflow-y-auto py-1" : undefined} role={props.depth === 0 ? "tree" : "group"}>
			{query.data?.entries.map((entry) => entry.type === "dir" ? (
				<CloudDirectory key={entry.path} {...props} depth={props.depth} entry={entry} />
			) : (
				<button
					aria-selected={props.selectedPath === entry.path}
					className={cn("flex h-7 w-full items-center gap-1.5 pr-2 text-left font-mono text-xs hover:bg-muted", props.selectedPath === entry.path && "bg-muted")}
					key={entry.path}
					onClick={() => props.onOpenFile(entry.path)}
					role="treeitem"
					style={{ paddingLeft: `${8 + props.depth * 14}px` }}
					type="button"
				>
					<File aria-hidden="true" className="size-icon-sm shrink-0 text-passive" />
					<span className="min-w-0 flex-1 truncate">{entry.name}</span>
					{entry.status && entry.status !== "unmodified" ? <span className="text-2xs text-warning">{statusLabel[entry.status]}</span> : null}
				</button>
			))}
		</div>
	);
}

function CloudDirectory({ entry, ...props }: CloudFileTreeProps & { depth: number; entry: { name: string; path: string; hasChanges?: boolean } }) {
	const [expanded, setExpanded] = useState(false);
	return (
		<div>
			<button
				aria-expanded={expanded}
				className="flex h-7 w-full items-center gap-1 pr-2 text-left font-mono text-xs hover:bg-muted"
				onClick={() => setExpanded((value) => !value)}
				role="treeitem"
				style={{ paddingLeft: `${6 + props.depth * 14}px` }}
				type="button"
			>
				{expanded ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
				<Folder aria-hidden="true" className="size-icon-sm text-passive" />
				<span className="min-w-0 flex-1 truncate">{entry.name}</span>
				{entry.hasChanges ? <span aria-hidden="true" className="size-1.5 rounded-full bg-warning" /> : null}
			</button>
			{expanded ? <CloudTreeLevel {...props} path={entry.path} depth={props.depth + 1} /> : null}
		</div>
	);
}

function CloudFileSearchResults(props: CloudFileTreeProps & { filterText: string }) {
	const { t } = useTranslation();
	const query = useQuery({
		...cloudWorkspaceReviewSearchQueryOptions({
			client: props.client, baseUrl: props.baseUrl, orgId: props.orgId, sessionId: props.sessionId,
			query: { query: props.filterText, limit: 100 },
		}),
	});
	if (query.isPending) return <PanelMessage>{t("files.loading")}</PanelMessage>;
	if (query.isError) return <PanelMessage action={<RetryButton onClick={() => void query.refetch()} />}>{query.error.message}</PanelMessage>;
	return (
		<div className="board-scrollbar min-h-0 flex-1 overflow-y-auto py-1" role="tree">
			{query.data?.results.map((file) => (
				<button className="flex h-7 w-full items-center gap-1.5 px-2 text-left font-mono text-xs hover:bg-muted" key={file.path} onClick={() => props.onOpenFile(file.path)} role="treeitem" type="button">
					<File className="size-icon-sm text-passive" /><span className="min-w-0 flex-1 truncate">{file.path}</span>
					{file.status !== "unmodified" ? <span className="text-2xs text-warning">{statusLabel[file.status]}</span> : null}
				</button>
			))}
		</div>
	);
}
