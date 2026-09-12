import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { Card, CardContent } from "../ui/card";
import { Button } from "../ui/button";
import { StatusBadge } from "./StatusBadge";

type RunView = components["schemas"]["ControllersRunView"];

type RunEntryProps = {
	run: RunView;
	isLatest?: boolean;
	onStart?: () => void;
	onCancel?: () => void;
	onViewSession?: (sessionId: string) => void;
	startPending?: boolean;
	cancelPending?: boolean;
};

function formatDateTime(iso: string | null | undefined): string {
	if (!iso) return "—";
	return new Date(iso).toLocaleString();
}

export function RunEntry({ run, isLatest, onStart, onCancel, onViewSession, startPending, cancelPending }: RunEntryProps) {
	const { t } = useTranslation();

	return (
		<Card size="sm" data-testid={`run-entry-${run.id}`}>
			<CardContent className="flex flex-col gap-2 text-xs">
				<div className="flex items-center justify-between">
					<span className="font-medium">
						{t("workflow.run.attempt")} #{run.attempt}
					</span>
					<StatusBadge status={run.status} entity="run" />
				</div>

				<div className="grid grid-cols-2 gap-x-3 gap-y-1 text-muted-foreground">
					<span>{t("workflow.run.session")}</span>
					<span className="truncate font-mono text-[10px]">{run.sessionId || "—"}</span>

					<span>{t("workflow.run.role")}</span>
					<span className="truncate">{run.agentRoleId || "—"}</span>

					<span>{t("workflow.run.provider")}</span>
					<span className="truncate">{run.providerDisplayName || run.providerId || "—"}</span>

					<span>{t("workflow.run.model")}</span>
					<span className="truncate">{run.providerModelName || run.providerModelId || "—"}</span>

					<span>{t("workflow.run.executor")}</span>
					<span className="truncate">{run.executorType || "—"}</span>

					<span>{t("workflow.run.createdAt")}</span>
					<span>{formatDateTime(run.createdAt)}</span>

					<span>{t("workflow.run.startedAt")}</span>
					<span>{formatDateTime(run.startedAt)}</span>

					<span>{t("workflow.run.finishedAt")}</span>
					<span>{formatDateTime(run.finishedAt)}</span>
				</div>

				{run.resultSummary && (
					<div>
						<p className="text-muted-foreground">{t("workflow.run.result")}</p>
						<p className="mt-0.5 line-clamp-2">{run.resultSummary}</p>
					</div>
				)}

				{run.errorMessage && (
					<div>
						<p className="text-destructive">{t("workflow.run.error")}</p>
						<p className="mt-0.5 line-clamp-2 text-destructive">{run.errorMessage}</p>
					</div>
				)}

				{(run.previousRunId || run.retryMode) && (
					<div className="flex gap-2 border-t pt-1">
						{run.previousRunId && (
							<span className="text-muted-foreground">
								{t("workflow.run.previousRun")}: <span className="font-mono text-[10px]">{run.previousRunId}</span>
							</span>
						)}
						{run.retryMode && (
							<span className="text-muted-foreground">
								{t("workflow.run.retryMode")}: {run.retryMode}
							</span>
						)}
					</div>
				)}

				{isLatest && (
					<div className="flex gap-2 border-t pt-2">
						{run.status === "pending" && onStart && (
							<Button size="sm" onClick={onStart} disabled={startPending} data-testid={`start-run-${run.id}`}>
								{t("workflow.run.start")}
							</Button>
						)}
						{run.status === "pending" && onCancel && (
							<Button size="sm" variant="outline" onClick={onCancel} disabled={cancelPending} data-testid={`cancel-run-${run.id}`}>
								{t("workflow.run.cancel")}
							</Button>
						)}
						{run.status === "running" && run.sessionId && onViewSession && (
							<Button size="sm" variant="outline" onClick={() => onViewSession(run.sessionId!)} data-testid={`view-session-${run.id}`}>
								{t("workflow.run.viewSession")}
							</Button>
						)}
						{run.status === "running" && onCancel && (
							<Button size="sm" variant="outline" onClick={onCancel} disabled={cancelPending} data-testid={`cancel-run-${run.id}`}>
								{t("workflow.run.cancel")}
							</Button>
						)}
					</div>
				)}
			</CardContent>
		</Card>
	);
}
