import { KeyRound } from "lucide-react";
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { useCloudGate } from "../../hooks/useCloudGate";
import { useCloudCp } from "../../hooks/useCloudCp";
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
	github: "GitHub",
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
	const { client } = useCloudCp();
	const queryClient = useQueryClient();
	const connections = useProviderConnections(org?.id);
	const userConnections = useQuery({
		queryKey: ["cloud-user-provider-connections"],
		enabled: status === "authenticated",
		queryFn: async () => (await client.listUserProviderConnections()).providerConnections,
	});
	const openCredentialDialog = useCredentialDialogStore((s) => s.openDialog);
	const [githubPAT, setGitHubPAT] = useState("");
	const [githubPATBusy, setGitHubPATBusy] = useState(false);
	const [githubPATError, setGitHubPATError] = useState<string | null>(null);

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
	const githubPATConnected = (userConnections.data ?? []).some(
		(connection) => connection.provider === "github" && connection.label === "default" && connection.validationState === "valid",
	);
	const saveGitHubPAT = async () => {
		if (githubPAT.trim() === "") return;
		setGitHubPATBusy(true);
		setGitHubPATError(null);
		try {
			await client.putGitHubPAT({ secret: githubPAT.trim() });
			setGitHubPAT("");
			await queryClient.invalidateQueries({ queryKey: ["cloud-user-provider-connections"] });
		} catch (error) {
			setGitHubPATError(error instanceof Error ? error.message : "Could not save the GitHub token.");
		} finally {
			setGitHubPATBusy(false);
		}
	};
	const removeGitHubPAT = async () => {
		setGitHubPATBusy(true);
		setGitHubPATError(null);
		try {
			await client.deleteGitHubPAT();
			await queryClient.invalidateQueries({ queryKey: ["cloud-user-provider-connections"] });
		} catch (error) {
			setGitHubPATError(error instanceof Error ? error.message : "Could not remove the GitHub token.");
		} finally {
			setGitHubPATBusy(false);
		}
	};
	return (
		<SettingsSection title={t("settings.cloudAgents")} sectionId="cloud-agents" titleHidden={titleHidden}>
			<div className="flex w-full flex-col gap-1.5">
				{rows.filter((connection) => connection.provider !== "github").map((connection) => (
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
				<div className="mt-3 border-t border-border px-3 pt-3">
					<SettingsRow key="github-pat" icon={KeyRound} label="GitHub private repositories">
						<span className="text-sm leading-5 text-settings-muted">{githubPATConnected ? "Connected" : "Not connected"}</span>
					</SettingsRow>
					<p className="mt-1 text-xs leading-relaxed text-muted-foreground">{t("settings.githubPat.description")}</p>
					<div className="mt-2 flex items-center gap-2">
						<Input type="password" autoComplete="off" spellCheck={false} value={githubPAT} onChange={(event) => setGitHubPAT(event.target.value)} placeholder="github_pat_…" />
						<Button type="button" variant="footer" disabled={githubPATBusy || githubPAT.trim() === ""} onClick={() => void saveGitHubPAT()}>
							{githubPATBusy ? "Saving…" : "Save token"}
						</Button>
						{githubPATConnected ? <Button type="button" variant="footer" disabled={githubPATBusy} onClick={() => void removeGitHubPAT()}>{t("settings.githubPat.remove")}</Button> : null}
					</div>
					{githubPATError ? <p role="alert" className="mt-2 text-xs text-error">{githubPATError}</p> : null}
				</div>
			</div>
		</SettingsSection>
	);
}
