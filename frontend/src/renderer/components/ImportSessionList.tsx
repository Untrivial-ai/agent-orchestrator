import { Check, Download, LoaderCircle, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
	type ImportableSession,
	useImportableSessions,
	useImportSession,
} from "../hooks/useImportableSessions";
import { type ImportRunProgress, useImportRunStore } from "../stores/import-run-store";
import { agentLabel } from "../lib/agent-options";
import { AgentAvatar } from "./AgentAvatar";
import { Button } from "./ui/button";

// How far back discovery looks by default, in days.
export const IMPORT_WINDOW_DAYS = 60;

export type ImportSessionListProps = {
	// projectId scopes the list to one project's history. Omitted lists every
	// conversation on the machine.
	projectId?: string;
	// active gates the query so a collapsed settings page does not scan disk.
	active?: boolean;
	// onImported fires with the new AO session id after a successful import.
	onImported?: (sessionId: string, projectId?: string) => void;
};

// ImportSessionList renders the agent conversations already on disk (Claude
// Code, Codex, any future provider) that AO can import as resumable sessions.
// The same list backs the import dialog, global settings, and a project's own
// settings, so the three surfaces can never drift apart.
export function ImportSessionList({ projectId, active = true, onImported }: ImportSessionListProps) {
	const { t } = useTranslation();
	const query = useImportableSessions(IMPORT_WINDOW_DAYS, active, projectId);
	const importMutation = useImportSession();
	// The run lives in a store, so closing this dialog leaves it going.
	const runProgress = useImportRunStore((state) => state.progress);
	const running = useImportRunStore((state) => state.running);
	const startRun = useImportRunStore((state) => state.start);
	const stopRun = useImportRunStore((state) => state.stop);
	const dismissRun = useImportRunStore((state) => state.dismiss);

	const sessions = query.data ?? [];
	const pendingId = importMutation.isPending ? importMutation.variables?.nativeSessionId : undefined;

	const handleImport = (session: ImportableSession) => {
		if (session.alreadyImported || importMutation.isPending) return;
		importMutation.mutate(
			{ provider: session.provider, nativeSessionId: session.nativeSessionId },
			{
				onSuccess: (data) => {
					const id = data?.session?.id;
					if (id) onImported?.(id, data?.session?.projectId);
				},
			},
		);
	};

	if (query.isLoading) {
		return (
			<div className="flex items-center justify-center gap-2 py-10 text-muted-foreground">
				<LoaderCircle className="size-icon-base animate-spin" aria-hidden="true" />
				<span className="text-md-sm">{t("importSession.loading")}</span>
			</div>
		);
	}

	if (query.isError) {
		return (
			<div className="flex items-start gap-3 rounded-lg border border-error/40 bg-error/5 px-3 py-3">
				<TriangleAlert aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-error" />
				<div className="min-w-0">
					<p className="text-control font-medium text-foreground">{t("importSession.errorTitle")}</p>
					<p className="mt-1 text-caption leading-4 text-muted-foreground">
						{query.error instanceof Error ? query.error.message : t("importSession.errorTitle")}
					</p>
				</div>
			</div>
		);
	}

	if (sessions.length === 0) {
		return (
			<div className="py-10 text-center">
				<p className="text-md-sm font-medium text-foreground">{t("importSession.emptyTitle")}</p>
				<p className="mt-1 text-caption leading-relaxed text-muted-foreground">
					{projectId
						? t("importSession.emptyBodyProject", { days: IMPORT_WINDOW_DAYS })
						: t("importSession.emptyBody", { days: IMPORT_WINDOW_DAYS })}
				</p>
			</div>
		);
	}

	// Grouped by folder, the way Codex and Cursor key their own history: the
	// folder is the identity a user recognizes, and there is no project to set
	// up first. Importing registers the folder itself.
	const folders = groupByFolder(sessions);
	const remaining = sessions.filter((session) => !session.alreadyImported).length;

	return (
		<>
			<ImportAllBar
				remaining={remaining}
				progress={runProgress}
				running={running}
				disabled={importMutation.isPending}
				onImportAll={() => void startRun(sessions)}
				onStop={stopRun}
				onDismiss={dismissRun}
			/>
			<div className="flex flex-col gap-4">
				{folders.map((folder) => (
					<section key={folder.path} aria-label={folder.path}>
						{projectId ? null : (
							<p
								className="mb-1.5 truncate text-caption font-medium text-muted-foreground"
								title={folder.path}
							>
								{folder.name}
							</p>
						)}
						<ul className="flex flex-col gap-2" aria-label={folder.path}>
							{folder.sessions.map((session) => (
								<ImportSessionRow
									key={`${session.provider}:${session.nativeSessionId}`}
									session={session}
									pending={pendingId === session.nativeSessionId}
									disabled={importMutation.isPending || running}
									onImport={() => handleImport(session)}
								/>
							))}
						</ul>
					</section>
				))}
			</div>
			{importMutation.isError ? (
				<p className="mt-3 text-caption leading-4 text-error" role="alert">
					{importMutation.error instanceof Error
						? importMutation.error.message
						: t("importSession.importFailed")}
				</p>
			) : null}
		</>
	);
}

function ImportSessionRow({
	session,
	pending,
	disabled,
	onImport,
}: {
	session: ImportableSession;
	pending: boolean;
	disabled: boolean;
	onImport: () => void;
}) {
	const { t } = useTranslation();
	const recency = relativeDay(session.lastActivity);
	const recencyLabel = recency ? t(recency.key, { count: recency.count }) : "";
	const meta = [agentLabel(session.provider), recencyLabel].filter(Boolean).join(" · ");

	return (
		<li
			className="flex items-center gap-3 rounded-lg border border-border bg-surface-raised/40 px-3 py-2.5"
			data-testid="importable-session"
		>
			<AgentAvatar className="size-icon-lg shrink-0" decorative provider={session.provider} />
			<div className="min-w-0 flex-1">
				<p className="truncate text-control font-medium text-foreground" title={session.title}>
					{session.title || session.nativeSessionId}
				</p>
				{session.branch ? (
					<p className="truncate text-caption text-muted-foreground" title={session.branch}>
						{session.branch}
					</p>
				) : null}
				<p className="mt-0.5 text-micro text-muted-foreground">
					{meta}
					{session.messageCount > 0 ? ` · ${t("importSession.messages", { count: session.messageCount })}` : ""}
				</p>
			</div>
			{session.alreadyImported ? (
				<span className="flex shrink-0 items-center gap-1 text-caption text-muted-foreground">
					<Check className="size-icon-sm" aria-hidden="true" />
					{t("importSession.imported")}
				</span>
			) : (
				<Button
					className="shrink-0"
					disabled={disabled}
					onClick={onImport}
					type="button"
					variant="outline"
				>
					{pending ? (
						<LoaderCircle className="size-icon-sm animate-spin" aria-hidden="true" />
					) : (
						<Download className="size-icon-sm" aria-hidden="true" />
					)}
					{t("importSession.import")}
				</Button>
			)}
		</li>
	);
}

// relativeDay returns the i18n key (and count) for a coarse recency label.
// Precise timestamps do not matter for a "which conversation is this" list.
type RecencyKey = "importSession.today" | "importSession.yesterday" | "importSession.daysAgo";

function relativeDay(iso: string): { key: RecencyKey; count: number } | null {
	const then = new Date(iso).getTime();
	if (Number.isNaN(then)) return null;
	const days = Math.floor((Date.now() - then) / (24 * 60 * 60 * 1000));
	if (days <= 0) return { key: "importSession.today", count: 0 };
	if (days === 1) return { key: "importSession.yesterday", count: 1 };
	return { key: "importSession.daysAgo", count: days };
}

// ImportAllBar is the one-click path: someone arriving with a hundred threads
// should not have to press Import a hundred times. It doubles as the run's
// progress and its result, so the outcome is reported where the action was.
function ImportAllBar({
	remaining,
	progress,
	running,
	disabled,
	onImportAll,
	onStop,
	onDismiss,
}: {
	remaining: number;
	progress: ImportRunProgress | null;
	running: boolean;
	disabled: boolean;
	onImportAll: () => void;
	onStop: () => void;
	onDismiss: () => void;
}) {
	const { t } = useTranslation();

	if (progress && !running) {
		return (
			<div className="mb-3 flex items-center justify-between gap-3 rounded-lg border border-border bg-surface-raised/40 px-3 py-2">
				<p className="text-caption text-foreground">
					{t("importSession.importedCount", { count: progress.imported })}
					{progress.failed > 0 ? ` · ${t("importSession.failedCount", { count: progress.failed })}` : ""}
				</p>
				<Button onClick={onDismiss} type="button" variant="ghost">
					{t("importSession.done")}
				</Button>
			</div>
		);
	}

	if (running && progress) {
		return (
			<div className="mb-3 flex items-center justify-between gap-3 rounded-lg border border-border bg-surface-raised/40 px-3 py-2">
				<span className="flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
					<LoaderCircle className="size-icon-sm shrink-0 animate-spin" aria-hidden="true" />
					<span className="truncate">
						{t("importSession.importingProgress", { done: progress.done, total: progress.total })}
						{" · "}
						{t("importSession.keepsRunning")}
					</span>
				</span>
				<Button onClick={onStop} type="button" variant="outline">
					{t("importSession.stop")}
				</Button>
			</div>
		);
	}

	if (remaining === 0) return null;

	return (
		<div className="mb-3 flex items-center justify-between gap-3">
			<p className="text-caption text-muted-foreground">
				{t("importSession.available", { count: remaining })}
			</p>
			<Button
				data-testid="import-all"
				disabled={disabled}
				onClick={onImportAll}
				type="button"
				variant="outline"
			>
				<Download className="size-icon-sm" aria-hidden="true" />
				{t("importSession.importAll")}
			</Button>
		</div>
	);
}

type ImportFolder = {
	path: string;
	name: string;
	sessions: ImportableSession[];
};

// groupByFolder buckets conversations by the directory they ran in, keeping the
// recency order the daemon returned: folders appear in order of their most
// recent conversation, and rows stay newest-first inside each folder.
function groupByFolder(sessions: ImportableSession[]): ImportFolder[] {
	const byPath = new Map<string, ImportFolder>();
	for (const session of sessions) {
		const path = session.cwd || "";
		let folder = byPath.get(path);
		if (!folder) {
			folder = { path, name: folderName(path), sessions: [] };
			byPath.set(path, folder);
		}
		folder.sessions.push(session);
	}
	return [...byPath.values()];
}

// folderName is the last path segment, which is what a user recognizes. The
// full path stays available as a tooltip for the ambiguous cases.
function folderName(path: string): string {
	const trimmed = path.replace(/[\\/]+$/, "");
	if (!trimmed) return path;
	const segments = trimmed.split(/[\\/]/);
	return segments[segments.length - 1] || trimmed;
}
