import { KeyRound } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/button";
import { useCloudGate } from "../../hooks/useCloudGate";
import { useCloudOrg } from "../../hooks/useCloudOrg";
import { hasValidAgentConnection, useProviderConnections } from "../../hooks/useProviderConnections";
import { useCloudSession } from "../../lib/cloud-session";
import { useCredentialDialogStore } from "../../stores/credential-dialog-store";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

// Proper nouns; deliberately not translated.
const AGENT_LABELS: Record<string, string> = {
	"claude-code": "Claude Code",
	codex: "Codex",
	cursor: "Cursor",
};

/**
 * Cloud coding-agent credentials in global settings. The outer component only
 * reads the daemon settings gate (a query the settings page already runs), so
 * a local-only app renders nothing and never mounts the cloud hooks — the
 * inner component is what subscribes to the cloud session/org/connection
 * queries. The connect flow reuses the globally mounted CloudCredentialDialog
 * via its shared open-store.
 */
export function CloudCredentialsSection({ titleHidden }: { titleHidden?: boolean }) {
	const { cloudEnabled } = useCloudGate();
	if (!cloudEnabled) return null;
	return <CloudCredentialsSectionInner titleHidden={titleHidden} />;
}

function CloudCredentialsSectionInner({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const { status } = useCloudSession();
	const { org } = useCloudOrg();
	const connections = useProviderConnections(org?.id);
	const openCredentialDialog = useCredentialDialogStore((s) => s.openDialog);

	// Managing credentials needs the signed-in org. The Cloud settings page is
	// reachable while signed out, so say why it is empty instead of rendering a
	// blank pane.
	if (status !== "authenticated") {
		return (
			<SettingsSection title={t("settings.cloudAgents")} sectionId="cloud-agents" titleHidden={titleHidden}>
				<p className="px-3 text-xs leading-relaxed text-muted-foreground">{t("settings.cloudAgents.signIn")}</p>
			</SettingsSection>
		);
	}

	const rows = connections.data ?? [];
	return (
		<SettingsSection title={t("settings.cloudAgents")} sectionId="cloud-agents" titleHidden={titleHidden}>
			<div className="flex w-full flex-col gap-1.5">
				{rows.map((connection) => (
					<SettingsRow key={connection.id} icon={KeyRound} label={AGENT_LABELS[connection.provider] ?? connection.provider}>
						<span className="text-sm leading-5 text-settings-muted">
							{connection.validationState === "valid"
								? t("settings.cloudAgents.valid")
								: connection.validationState}
						</span>
					</SettingsRow>
				))}
				{connections.isSuccess && !hasValidAgentConnection(rows) ? (
					<p className="px-3 text-xs leading-relaxed text-muted-foreground">{t("settings.cloudAgents.empty")}</p>
				) : null}
				<div className="flex items-center justify-between gap-4 px-3 pt-1">
					<p className="text-xs leading-relaxed text-muted-foreground">{t("settings.cloudAgents.description")}</p>
					<Button type="button" variant="footer" onClick={() => openCredentialDialog()}>
						{t("settings.cloudAgents.connect")}
					</Button>
				</div>
			</div>
		</SettingsSection>
	);
}
