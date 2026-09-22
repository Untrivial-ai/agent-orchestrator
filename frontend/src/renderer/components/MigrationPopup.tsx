import * as Dialog from "@radix-ui/react-dialog";
import { useTranslation } from "react-i18next";
import { Loader2, X } from "lucide-react";
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { aoBridge } from "../lib/bridge";
import { migrationOfferQueryKey, useMigrationOffer } from "../hooks/useMigrationOffer";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { Button } from "./ui/button";
import {
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";

// MigrationPopup is the first-run legacy-AO import offer. It shows only when the
// app marker is non-terminal (pending/failed) AND the daemon reports legacy data
// available. Proceed runs the idempotent import through the daemon; Skip dismisses
// for this launch (re-prompts next launch); {t("migration.dontMigrate")} declines permanently
// (re-runnable later once the Settings entry point lands, issue #2205).
export function MigrationPopup() {
	const { t } = useTranslation();
	const offer = useMigrationOffer();
	const queryClient = useQueryClient();
	const [skipped, setSkipped] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | undefined>();

	const open = (offer.data?.show ?? false) && !skipped;
	if (!open) return null;

	const legacyRoot = offer.data?.legacyRoot || "your earlier AO";
	const nowIso = () => new Date().toISOString();

	const proceed = async () => {
		setBusy(true);
		setError(undefined);
		const { data, error: apiErr } = await apiClient.POST("/api/v1/import");
		if (apiErr) {
			const msg = apiErrorMessage(apiErr);
			setError(msg);
			await aoBridge.appState.setMigration({ status: "failed", lastAttemptAt: nowIso(), error: msg });
			setBusy(false);
			return;
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
		await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		await queryClient.invalidateQueries({ queryKey: migrationOfferQueryKey });
		setSkipped(true);
		setBusy(false);
	};

	const dontMigrate = async () => {
		await aoBridge.appState.setMigration({ status: "declined", lastAttemptAt: nowIso() });
		await queryClient.invalidateQueries({ queryKey: migrationOfferQueryKey });
	};

	return (
		<Dialog.Root
			open
			onOpenChange={(next) => {
				if (!next) setSkipped(true);
			}}
		>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in" />
				<Dialog.Content
					className={`${settingsDialogContentClass} fixed left-1/2 top-1/2 w-dialog-lg -translate-x-1/2 -translate-y-1/2 data-[state=open]:animate-modal-in`}
				>
					<button
						type="button"
						className="settings-dialog-close-button settings-close-button"
						aria-label={t("common.close")}
						disabled={busy}
						onClick={() => setSkipped(true)}
					>
						<X className="size-5" aria-hidden="true" />
					</button>
					<div className={settingsDialogHeaderClass}>
						<Dialog.Title className="settings-dialog-title">{t("migration.title")}</Dialog.Title>
						<Dialog.Description className="text-control leading-body text-muted-foreground">
							{t("migration.bodyLead")}{" "}
							<span className="font-mono text-caption text-foreground">{legacyRoot}</span>
							. {t("migration.bodyTrail")}
						</Dialog.Description>
					</div>
					<div className={settingsDialogBodyClass}>
						{error ? <p className="text-xs text-destructive">{t("migration.failed", { error })}</p> : null}
						<p className="text-caption text-muted-foreground">{t("migration.againLater")}</p>
					</div>
					<div className={`${settingsDialogFooterClass} justify-between`}>
						<Button type="button" variant="footer" className="text-destructive" onClick={dontMigrate} disabled={busy}>
							{t("migration.dontMigrate")}
						</Button>
						<div className="flex flex-wrap items-center justify-end gap-3">
							<Button type="button" variant="footer" onClick={() => setSkipped(true)} disabled={busy}>
								{t("migration.skip")}
							</Button>
							<Button type="button" variant="footer-primary" onClick={proceed} disabled={busy}>
								{busy ? <Loader2 className="size-icon-base animate-spin" aria-hidden="true" /> : null}
								{error ? t("migration.retry") : t("migration.proceed")}
							</Button>
						</div>
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}
