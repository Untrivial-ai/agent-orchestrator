import { Check, Download, LoaderCircle, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
	useImportableSessions,
	type ImportableSession,
} from "../hooks/useImportableSessions";
import {
	type ImportRunProgress,
	useImportRunStore,
} from "../stores/import-run-store";
import { agentLabel } from "../lib/agent-options";
import { AgentAvatar } from "./AgentAvatar";
import { Button } from "./ui/button";

export type ImportSessionListProps = { projectId: string; active?: boolean };
export function ImportSessionList({
	projectId,
	active = true,
}: ImportSessionListProps) {
	const { t } = useTranslation();
	const run = useImportRunStore((state) => state.runs[projectId]);
	const query = useImportableSessions(projectId, active && !run?.running);
	const start = useImportRunStore((state) => state.start);
	const stop = useImportRunStore((state) => state.stop);
	const dismiss = useImportRunStore((state) => state.dismiss);
	const sessions = query.data ?? [];
	return (
		<>
			<ImportAllBar
				remaining={sessions.filter((s) => !s.alreadyImported).length}
				progress={run?.progress ?? null}
				elapsedMs={run?.elapsedMs}
				running={run?.running ?? false}
				disabled={query.isFetching}
				onImportAll={() => void start(projectId, sessions)}
				onStop={() => stop(projectId)}
				onDismiss={() => dismiss(projectId)}
			/>
			{!run?.running && (
				<Button
					className="mb-3"
					variant="ghost"
					disabled={query.isFetching}
					onClick={() => void query.refetch()}
				>
					{t("settings.project.refresh")}
				</Button>
			)}
			{run?.stopped && (
				<p role="status" className="mb-3 text-caption text-muted-foreground">
					{t("importSession.stopped", {
						count: run.progress.total - run.progress.done,
					})}
				</p>
			)}
			{!!run?.errors.length && (
				<ul role="alert" className="mb-3 space-y-2 text-caption text-error">
					{run.errors.map((error, i) => (
						<li key={i}>
							<strong>{error.title}:</strong> {error.message}
						</li>
					))}
				</ul>
			)}
			{query.isLoading && (
				<div
					role="status"
					className="flex items-center justify-center gap-2 py-10 text-muted-foreground"
				>
					<LoaderCircle
						className="size-icon-base animate-spin"
						aria-hidden="true"
					/>
					{t("importSession.loading")}
				</div>
			)}
			{query.isError && (
				<div
					role="alert"
					className="rounded-lg border border-error/40 px-3 py-3"
				>
					<TriangleAlert aria-hidden="true" className="size-5 text-error" />
					<p>{query.error.message}</p>
					<Button
						onClick={() => void query.refetch()}
						disabled={query.isFetching}
					>
						{t("files.retry")}
					</Button>
				</div>
			)}
			{!query.isLoading && !query.isError && sessions.length === 0 && (
				<div className="py-10 text-center">
					<p>{t("importSession.emptyTitle")}</p>
					<p className="mt-1 text-caption text-muted-foreground">
						{t("importSession.emptyBodyProject")}
					</p>
				</div>
			)}
			<ul className="flex flex-col gap-2" aria-label={t("importSession.title")}>
				{sessions.map((session) => (
					<ImportSessionRow
						key={`${session.provider}:${session.nativeSessionId}`}
						session={session}
						pending={
							run?.currentId ===
							`${session.provider}:${session.nativeSessionId}`
						}
						disabled={!!run?.running || query.isFetching}
						onImport={() => void start(projectId, [session])}
					/>
				))}
			</ul>
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
	const meta = [agentLabel(session.provider), recencyLabel]
		.filter(Boolean)
		.join(" · ");

	return (
		<li
			className="flex items-center gap-3 rounded-lg border border-border bg-surface-raised/40 px-3 py-2.5"
			data-testid="importable-session"
		>
			<AgentAvatar
				className="size-icon-lg shrink-0"
				decorative
				provider={session.provider}
			/>
			<div className="min-w-0 flex-1">
				<p
					className="truncate text-control font-medium text-foreground"
					title={session.title}
				>
					{session.title || session.nativeSessionId}
				</p>
				{session.branch ? (
					<p
						className="truncate text-caption text-muted-foreground"
						title={session.branch}
					>
						{session.branch}
					</p>
				) : null}
				<p className="mt-0.5 text-micro text-muted-foreground">
					{meta}
					{` · ${t("importSession.tokens", { count: session.tokenCount })}`}
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
						<LoaderCircle
							className="size-icon-sm animate-spin"
							aria-hidden="true"
						/>
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
type RecencyKey =
	| "importSession.today"
	| "importSession.yesterday"
	| "importSession.daysAgo";

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
	elapsedMs,
	progress,
	running,
	disabled,
	onImportAll,
	onStop,
	onDismiss,
}: {
	remaining: number;
	elapsedMs?: number;
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
					{elapsedMs !== undefined ? ` · ${(elapsedMs / 1000).toFixed(2)} s` : ""}
					{progress.failed > 0
						? ` · ${t("importSession.failedCount", { count: progress.failed })}`
						: ""}
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
					<LoaderCircle
						className="size-icon-sm shrink-0 animate-spin"
						aria-hidden="true"
					/>
					<span className="truncate">
						{t("importSession.importingProgress", {
							done: progress.done,
							total: progress.total,
						})}
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
