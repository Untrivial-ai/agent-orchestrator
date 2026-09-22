import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { aoBridge } from "../lib/bridge";
import { migrationOfferQueryKey } from "../hooks/useMigrationOffer";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import type { MigrationState, MigrationStatus } from "../../main/app-state";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";

export const migrationSettingsQueryKey = ["migration-settings"] as const;

interface MigrationView {
	migration: MigrationState;
	available: boolean;
	legacyRoot: string;
}

// fetchMigrationSettings reads the persisted decision (app marker) and asks the
// daemon whether legacy data is present. Unlike useMigrationOffer it never
// short-circuits on a terminal status: Settings always shows the full state so a
// user who declined or already completed can re-run. A 501/unreachable daemon
// resolves to "not available", never an error.
async function fetchMigrationSettings(): Promise<MigrationView> {
	const migration = await aoBridge.appState.getMigration();
	const { data, error } = await apiClient.GET("/api/v1/import");
	return {
		migration,
		available: !error && (data?.available ?? false),
		legacyRoot: data?.legacyRoot ?? "",
	};
}

const STATUS_LABEL_KEY = {
	pending: "settings.migration.status.pending",
	completed: "settings.migration.status.completed",
	declined: "settings.migration.status.declined",
	failed: "settings.migration.status.failed",
} as const;

function statusClass(status: MigrationStatus): string {
	switch (status) {
		case "completed":
			return "text-success";
		case "failed":
			return "text-destructive";
		default:
			return "text-muted-foreground";
	}
}

function formatTime(iso: string | undefined, locale: string | undefined): string {
	if (!iso) return "";
	const d = new Date(iso);
	return Number.isNaN(d.getTime()) ? "" : d.toLocaleString(locale);
}

// MigrationSection is a drop-in Settings card for re-running the legacy-AO
// import. It reads the persisted migration decision + the daemon's availability,
// shows the last report/error, and exposes a Run / Re-run button that calls the
// idempotent POST /api/v1/import (safe even when completed/declined/failed).
// Issue #2205.
export function MigrationSection() {
	const { i18n, t } = useTranslation();
	const queryClient = useQueryClient();
	const query = useQuery({
		queryKey: migrationSettingsQueryKey,
		queryFn: fetchMigrationSettings,
	});

	const run = useMutation({
		mutationFn: async () => {
			const nowIso = () => new Date().toISOString();
			const { data, error } = await apiClient.POST("/api/v1/import");
			if (error) {
				const msg = apiErrorMessage(error);
				await aoBridge.appState.setMigration({ status: "failed", lastAttemptAt: nowIso(), error: msg });
				throw new Error(msg);
			}
			const report = data?.report;
			await aoBridge.appState.setMigration({
				status: "completed",
				lastAttemptAt: nowIso(),
				completedAt: nowIso(),
				report: report
					? { projectsImported: report.projectsImported, projectsSkipped: report.projectsSkipped }
					: undefined,
			});
		},
		onSettled: () => {
			void queryClient.invalidateQueries({ queryKey: migrationSettingsQueryKey });
			void queryClient.invalidateQueries({ queryKey: migrationOfferQueryKey });
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
	});

	const migration = query.data?.migration ?? { status: "pending" as MigrationStatus };
	const available = query.data?.available ?? false;
	const legacyRoot = query.data?.legacyRoot ?? "";
	const report = migration.report;
	const completed = migration.status === "completed";
	const buttonLabel = run.isPending
		? t("settings.migration.running")
		: completed
			? t("settings.migration.rerun")
			: migration.status === "failed"
				? t("settings.migration.retry")
				: t("settings.migration.run");
	const lastActivity = migration.completedAt || migration.lastAttemptAt;
	const formattedActivity = formatTime(lastActivity, i18n.resolvedLanguage);

	return (
		<Card data-testid="settings-section" data-section="migration">
			<CardHeader>
				<CardTitle className="text-control">{t("settings.migration.title")}</CardTitle>
			</CardHeader>
			<CardContent className="flex flex-col gap-4">
				<p className="text-xs leading-row text-muted-foreground">
					{t("settings.migration.description")}
				</p>

				<div className="flex flex-col gap-2 text-xs">
					<Row label={t("settings.migration.statusLabel")}>
						<span className={statusClass(migration.status)}>{t(STATUS_LABEL_KEY[migration.status])}</span>
					</Row>
					{formattedActivity && (
						<Row label={completed ? t("settings.migration.completedAt") : t("settings.migration.lastAttempt")}>
							<span className="text-foreground">{formattedActivity}</span>
						</Row>
					)}
					{report && (
						<Row label={t("settings.migration.lastReport")}>
							<span className="text-foreground">
								{t("settings.migration.report", {
									imported: report.projectsImported,
									skipped: report.projectsSkipped,
								})}
							</span>
						</Row>
					)}
					<Row label={t("settings.migration.legacyInstall")}>
						{query.isLoading ? (
							<span className="text-passive">{t("settings.migration.checking")}</span>
						) : available ? (
							<span className="font-mono text-caption text-foreground">
								{legacyRoot || t("settings.migration.found")}
							</span>
						) : (
							<span className="text-passive">{t("settings.migration.noneFound")}</span>
						)}
					</Row>
				</div>

				{migration.status === "failed" && migration.error && (
					<p className="text-xs leading-row text-destructive">
						{t("settings.migration.failedPreserved", { error: migration.error })}
					</p>
				)}
				{run.isError && (
					<p className="text-xs leading-row text-destructive">
						{run.error instanceof Error ? run.error.message : t("settings.migration.failed")}
					</p>
				)}
				{run.isSuccess && !run.isPending && (
					<p className="text-xs leading-row text-success">{t("settings.migration.complete")}</p>
				)}

				<div className="flex items-center gap-3">
					<Button
						type="button"
						variant="primary"
						onClick={() => run.mutate()}
						disabled={run.isPending || (!available && !completed)}
					>
						{run.isPending && <Loader2 className="mr-2 size-icon-base animate-spin" />}
						{buttonLabel}
					</Button>
					{!available && !query.isLoading && (
						<span className="text-xs text-passive">{t("settings.migration.nothingToImport")}</span>
					)}
				</div>
			</CardContent>
		</Card>
	);
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
	return (
		<div className="flex items-center gap-3">
			<span className="w-28 shrink-0 text-passive">{label}</span>
			<span className="min-w-0 flex-1 truncate">{children}</span>
		</div>
	);
}
