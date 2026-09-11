import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { appI18n } from "../../i18n";
import type { components } from "../../../api/schema";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import { Button } from "../ui/button";

type InstallJob = components["schemas"]["InstallJob"];
export const codexUpdateQueryKey = ["codex-update"] as const;

async function checkCodexUpdate(refresh = false) {
	const { data, error } = await apiClient.GET("/api/v1/agents/codex/update", { params: { query: { refresh } } });
	if (error || !data) throw new Error(apiErrorMessage(error, appI18n.t("settings.codexUpdate.checkFailed")));
	return data;
}

export function CodexUpdatePanel({ job, onJob }: { job?: InstallJob; onJob: (job: InstallJob) => void }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const advisory = useQuery({ queryKey: codexUpdateQueryKey, queryFn: () => checkCodexUpdate(), staleTime: 60_000, retry: false });
	const [pending, setPending] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const updateJob = job?.method?.startsWith("update:") ? job : undefined;
	const active = job?.status === "queued" || job?.status === "installing" || job?.status === "verifying";
	const current = advisory.data;

	async function refresh() {
		setPending(true);
		setError(null);
		try { queryClient.setQueryData(codexUpdateQueryKey, await checkCodexUpdate(true)); }
		catch (cause) { setError(cause instanceof Error ? cause.message : t("settings.codexUpdate.checkFailed")); }
		finally { setPending(false); }
	}

	async function update() {
		if (!current?.token || pending || active) return;
		setPending(true);
		setError(null);
		try {
			const { data, error: failure } = await apiClient.POST("/api/v1/agents/codex/update", { body: { token: current.token } });
			if (failure || !data) throw new Error(apiErrorMessage(failure, t("settings.codexUpdate.startFailed")));
			onJob(data);
		} catch (cause) { setError(cause instanceof Error ? cause.message : t("settings.codexUpdate.startFailed")); }
		finally { setPending(false); }
	}

	return (
		<div className="basis-full space-y-2 pl-10 text-xs text-settings-muted">
			{current ? <p className="tabular-nums">{t("settings.codexUpdate.version", { version: current.version || t("settings.codexUpdate.unknown"), source: current.source })}{current.availableVersion ? ` · ${t("settings.codexUpdate.available", { version: current.availableVersion, source: current.versionSource })}` : ""}{current.stale ? ` · ${t("settings.codexUpdate.stale")}` : ""}</p> : null}
			{current?.warning ? <p className="text-pretty">{current.warning}</p> : null}
			{error ? <p role="alert" className="text-error">{error}</p> : null}
			{advisory.error ? <p aria-live="polite">{advisory.error.message}</p> : null}
			{current?.updateAvailable ? <p className="text-pretty">{t("settings.codexUpdate.shared")}</p> : null}
			{(current?.runningSessions ?? 0) > 0 ? <p className="text-pretty">{t("settings.codexUpdate.sessions", { count: current?.runningSessions, closeAction: t("inspector.review.killSession"), stopAction: t("inspector.review.cancel") })}</p> : null}
			<div className="flex flex-wrap gap-2">
				{current?.canUpdate ? <Button size="sm" disabled={pending || active || current.runningSessions > 0} onClick={() => void update()}>{t("settings.codexUpdate.update")}</Button> : null}
				<Button size="sm" variant="outline" disabled={pending || active || advisory.isFetching} onClick={() => void refresh()}>{pending || advisory.isFetching ? t("settings.codexUpdate.checking") : t("settings.codexUpdate.check")}</Button>
			</div>
			{updateJob ? <div role="status" aria-live="polite"><p>{updateJob.status === "queued" ? t("settings.codexUpdate.queued") : updateJob.status === "installing" ? t("settings.codexUpdate.running") : updateJob.status === "verifying" ? t("settings.codexUpdate.verifying") : updateJob.status === "succeeded" ? t("settings.codexUpdate.success") : t("settings.codexUpdate.attention")}</p>{updateJob.error ? <p className="text-error">{updateJob.error}</p> : null}</div> : null}
			{current?.path || updateJob ? <details>
				<summary className="cursor-pointer">{t("settings.codexUpdate.diagnostics")}</summary>
				<p className="mt-2 break-all">{t("settings.codexUpdate.selected", { path: current?.path || t("settings.codexUpdate.unavailable") })}</p>
				<p className="break-all">{t("settings.codexUpdate.resolved", { path: current?.realPath || t("settings.codexUpdate.unavailable") })}</p>
				{updateJob?.command ? <p className="break-all">{t("settings.codexUpdate.command", { command: updateJob.command })}</p> : null}
				{updateJob?.output ? <pre className="mt-2 max-h-40 overflow-auto whitespace-pre-wrap break-words">{updateJob.output}</pre> : null}
			</details> : null}
		</div>
	);
}
