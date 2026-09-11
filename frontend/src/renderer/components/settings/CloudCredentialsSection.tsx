import { useState } from "react";
import { KeyRound, Server } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/button";
import { useCloudGate } from "../../hooks/useCloudGate";
import { useCloudCp } from "../../hooks/useCloudCp";
import { useCloudOrg } from "../../hooks/useCloudOrg";
import {
	hasValidAgentConnection,
	useProviderConnections,
	useUserProviderConnections,
	userProviderConnectionsQueryKey,
} from "../../hooks/useProviderConnections";
import { useCloudSession } from "../../lib/cloud-session";
import { useCredentialDialogStore } from "../../stores/credential-dialog-store";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";
import { CoderConnectionDialog } from "./CoderConnectionDialog";

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
	const userConnections = useUserProviderConnections(status === "authenticated");
	const queryClient = useQueryClient();
	const { client } = useCloudCp();
	const openCredentialDialog = useCredentialDialogStore((s) => s.openDialog);
	const [coderDialogOpen, setCoderDialogOpen] = useState(false);
	const [coderError, setCoderError] = useState<string | null>(null);

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

	const rows = (connections.data ?? []).filter((connection) => connection.provider in AGENT_LABELS);
	const coderConnection = (userConnections.data ?? []).find(
		(connection) => connection.provider === "coder" && connection.label === "default",
	);
	const disconnectCoder = async () => {
		setCoderError(null);
		try {
			await client.deleteUserCoderConnection();
			await queryClient.invalidateQueries({
				queryKey: userProviderConnectionsQueryKey,
			});
		} catch (error) {
			setCoderError(error instanceof Error ? error.message : t("settings.coder.disconnectFailed"));
		}
	};
	return (
		<>
			<SettingsSection title={t("settings.cloudAgents")} sectionId="cloud-agents" titleHidden={titleHidden}>
				<div className="flex w-full flex-col gap-1.5">
					{rows.map((connection) => (
						<SettingsRow
							key={connection.id}
							icon={KeyRound}
							label={AGENT_LABELS[connection.provider] ?? connection.provider}
						>
							<span className="text-sm leading-5 text-settings-muted">
								{connection.validationState === "valid" ? t("settings.cloudAgents.valid") : connection.validationState}
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

			<SettingsSection title={t("settings.coder.section")} sectionId="cloud-coder">
				<div className="flex w-full flex-col gap-1.5">
					{coderConnection ? (
						<SettingsRow icon={Server} label="Coder">
							<div className="flex items-center gap-3">
								<span className="max-w-52 truncate text-sm leading-5 text-settings-muted">
									{typeof coderConnection.config.baseUrl === "string"
										? coderConnection.config.baseUrl
										: t("settings.coder.connected")}
								</span>
								<Button type="button" variant="footer" onClick={() => setCoderDialogOpen(true)}>
									{t("settings.coder.reconnectShort")}
								</Button>
								<Button type="button" variant="footer" onClick={() => void disconnectCoder()}>
									{t("settings.coder.disconnect")}
								</Button>
							</div>
						</SettingsRow>
					) : (
						<div className="flex items-center justify-between gap-4 px-3 py-1">
							<div>
								<p className="text-sm font-medium text-foreground">{t("settings.coder.heading")}</p>
								<p className="mt-1 text-xs leading-relaxed text-muted-foreground">
									{t("settings.coder.description")}
								</p>
							</div>
							<Button type="button" variant="footer-primary" onClick={() => setCoderDialogOpen(true)}>
								{t("settings.coder.connect")}
							</Button>
						</div>
					)}
					{coderError ? (
						<p role="alert" className="px-3 text-xs leading-4 text-error">
							{coderError}
						</p>
					) : null}
				</div>
			</SettingsSection>
			<CoderConnectionDialog open={coderDialogOpen} onOpenChange={setCoderDialogOpen} connection={coderConnection} />
		</>
	);
}
