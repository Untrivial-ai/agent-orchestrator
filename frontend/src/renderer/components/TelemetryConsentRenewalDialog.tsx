import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useTelemetryPolicyStore } from "../stores/telemetry-policy-store";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";

/**
 * Asks again for an opt-in given while the release gate was closed, once a
 * release has opened it. Both answers carry equal weight: this is a consent
 * decision, so neither is styled as the one to pick. Closing without choosing
 * is not an answer, so it asks again on the next launch.
 */
export function TelemetryConsentRenewalDialog() {
	const view = useTelemetryPolicyStore((state) => state.view);
	const [dismissed, setDismissed] = useState(false);
	const open = !dismissed && !!view?.consentRenewalRequired && view.state === "applied" && !view.environmentVeto && view.durabilitySupported;
	if (!open) return null;
	return <TelemetryConsentRenewalDialogBody onDismiss={() => setDismissed(true)} />;
}

function TelemetryConsentRenewalDialogBody({ onDismiss }: { onDismiss: () => void }) {
	const { t } = useTranslation();
	const saving = useTelemetryPolicyStore((state) => state.saving);
	const saveError = useTelemetryPolicyStore((state) => state.saveError);
	const setEnabled = useTelemetryPolicyStore((state) => state.setEnabled);

	return (
		<Dialog open onOpenChange={(next) => !next && !saving && onDismiss()}>
			<DialogContent className={settingsDialogContentClass} data-testid="telemetry-renewal-dialog" showCloseButton={!saving}>
				<div className={settingsDialogHeaderClass}>
					<DialogTitle>{t("telemetryRenewal.title")}</DialogTitle>
					<DialogDescription>{t("telemetryRenewal.body")}</DialogDescription>
				</div>
				{saveError && (
					<div className={settingsDialogBodyClass}>
						<p role="alert" className="text-sm text-destructive">{t("settings.telemetryEvents.failed")}</p>
					</div>
				)}
				<div className={settingsDialogFooterClass}>
					<Button type="button" variant="outline" size="sm" disabled={saving} onClick={() => void setEnabled(false)}>
						{t("telemetryRenewal.keepOff")}
					</Button>
					<Button type="button" variant="outline" size="sm" disabled={saving} onClick={() => void setEnabled(true)}>
						{t("telemetryRenewal.turnOn")}
					</Button>
				</div>
			</DialogContent>
		</Dialog>
	);
}
