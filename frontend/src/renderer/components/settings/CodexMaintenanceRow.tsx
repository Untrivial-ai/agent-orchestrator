import { Download, LoaderCircle, TriangleAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { apiClient, apiErrorCode, apiErrorMessage } from "../../lib/api-client";
import {
	invalidateCodexMaintenance,
	startCodexUpdate,
	useCodexMaintenanceQuery,
	type CodexInstallJob,
} from "../../hooks/useCodexMaintenanceQuery";
import { Button } from "../ui/button";

const POLL_INTERVAL_MS = 1_000;

function isActiveJob(job: CodexInstallJob | undefined): boolean {
	return job?.status === "installing" || job?.status === "verifying";
}

/**
 * Advisory row shown inside the Codex provider group: an installer-aware
 * "Update now" action that never blocks the rest of Settings from rendering.
 * The status query is cached and bounded server-side, so this only ever adds
 * one cheap GET to a Settings visit, not a repeated live probe.
 */
export function CodexMaintenanceRow() {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const maintenance = useCodexMaintenanceQuery();
	const [job, setJob] = useState<CodexInstallJob | null>(null);
	const [starting, setStarting] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const pollTimer = useRef<number | undefined>(undefined);

	useEffect(() => () => window.clearInterval(pollTimer.current), []);

	useEffect(() => {
		if (!isActiveJob(job ?? undefined)) {
			window.clearInterval(pollTimer.current);
			return;
		}
		pollTimer.current = window.setInterval(async () => {
			const { data, error: pollError } = await apiClient.GET("/api/v1/agents/{agent}/install", {
				params: { path: { agent: "codex" } },
			});
			if (pollError || !data) return;
			setJob(data);
			if (data.status === "succeeded") {
				void invalidateCodexMaintenance(queryClient);
			} else if (data.status === "failed" || data.status === "unsupported") {
				setError(data.error ?? t("settings.codexMaintenance.updateFailed"));
			}
		}, POLL_INTERVAL_MS);
		return () => window.clearInterval(pollTimer.current);
	}, [job, queryClient, t]);

	const data = maintenance.data;
	if (!data) return null;

	const active = isActiveJob(job ?? undefined);
	const updateAvailable = data.updateAvailable && !active && job?.status !== "succeeded";
	const showManualOnly = updateAvailable && !data.updateSupported;

	if (!updateAvailable && !active && job?.status !== "succeeded" && !error) return null;

	const onUpdateNow = async () => {
		if (starting || active) return;
		setStarting(true);
		setError(null);
		try {
			const started = await startCodexUpdate(data.ownership);
			setJob(started);
		} catch (caught) {
			if (apiErrorCode(caught) === "CODEX_OWNERSHIP_CHANGED") {
				setError(t("settings.codexMaintenance.ownershipChanged"));
				void invalidateCodexMaintenance(queryClient);
			} else {
				setError(apiErrorMessage(caught, t("settings.codexMaintenance.updateFailed")));
			}
		} finally {
			setStarting(false);
		}
	};

	return (
		<div className="flex items-center justify-between gap-3 border-b border-border px-4 py-3 text-xs" data-testid="codex-maintenance-row">
			<div className="min-w-0">
				{active ? (
					<p className="flex items-center gap-1.5 text-muted-foreground" role="status">
						<LoaderCircle className="size-3.5 animate-spin" aria-hidden="true" />
						{t("settings.codexMaintenance.updating")}
					</p>
				) : job?.status === "succeeded" ? (
					<p className="text-success">{t("settings.codexMaintenance.updateSucceeded")}</p>
				) : error ? (
					<p className="flex items-center gap-1.5 text-error"><TriangleAlert className="size-3.5 shrink-0" aria-hidden="true" />{error}</p>
				) : showManualOnly ? (
					<p className="text-muted-foreground">{data.manualReason ?? t("settings.codexMaintenance.manualReasonPrefix")}</p>
				) : (
					<p className="text-foreground">
						{data.installedVersion
							? t("settings.codexMaintenance.updateAvailable", { installed: data.installedVersion, latest: data.latestVersion })
							: t("settings.codexMaintenance.updateAvailableUnknownInstalled", { latest: data.latestVersion })}
					</p>
				)}
			</div>
			{updateAvailable && data.updateSupported ? (
				<Button type="button" size="sm" variant="outline" disabled={starting} onClick={() => void onUpdateNow()}>
					{starting ? <LoaderCircle className="size-3.5 animate-spin" aria-hidden="true" /> : <Download className="size-3.5" aria-hidden="true" />}
					{t("settings.codexMaintenance.updateNow")}
				</Button>
			) : null}
		</div>
	);
}
