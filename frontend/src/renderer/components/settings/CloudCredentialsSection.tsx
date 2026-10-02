import { KeyRound } from "lucide-react";
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { GitHubTokenField } from "../onboarding/GitHubTokenField";
import { Button } from "../ui/button";
import { useCloudGate } from "../../hooks/useCloudGate";
import { useCloudCp } from "../../hooks/useCloudCp";
import { providerConnectionsQueryKey, useProviderConnections } from "../../hooks/useProviderConnections";
import { useCloudSession } from "../../lib/cloud-session";
import { useUiStore } from "../../stores/ui-store";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

/**
 * Cloud credentials in global settings: the GitHub token for private
 * repositories, plus a pointer to the Harnesses page, where coding agents are
 * logged in for cloud. The outer component only reads the daemon settings gate
 * (a query the settings page already runs), so a local-only app renders
 * nothing and never mounts the cloud hooks.
 */
export function CloudCredentialsSection({ titleHidden }: { titleHidden?: boolean }) {
	const { cloudEnabled } = useCloudGate();
	if (!cloudEnabled) return null;
	return <CloudCredentialsSectionInner titleHidden={titleHidden} />;
}

function CloudCredentialsSectionInner({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const { status } = useCloudSession();
	const { client } = useCloudCp();
	const queryClient = useQueryClient();
	const userConnections = useProviderConnections();
	const openGlobalSettings = useUiStore((state) => state.openGlobalSettings);
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
			await queryClient.invalidateQueries({ queryKey: providerConnectionsQueryKey });
		} catch (error) {
			setGitHubPATError(error instanceof Error ? error.message : t("settings.cloudAgents.github.errorSave"));
		} finally {
			setGitHubPATBusy(false);
		}
	};
	const removeGitHubPAT = async () => {
		setGitHubPATBusy(true);
		setGitHubPATError(null);
		try {
			await client.deleteGitHubPAT();
			await queryClient.invalidateQueries({ queryKey: providerConnectionsQueryKey });
		} catch (error) {
			setGitHubPATError(error instanceof Error ? error.message : t("settings.cloudAgents.github.errorRemove"));
		} finally {
			setGitHubPATBusy(false);
		}
	};
	return (
		<SettingsSection title={t("settings.cloudAgents")} sectionId="cloud-agents" titleHidden={titleHidden}>
			<div className="flex w-full flex-col gap-1.5">
				<div className="flex items-center justify-between gap-4 px-3 pt-1">
					<p className="text-xs leading-relaxed text-muted-foreground">{t("settings.cloudAgents.description")}</p>
					<Button type="button" variant="footer" onClick={() => openGlobalSettings("harness", { harnessView: "cloud", preserveProject: true })}>
						{t("settings.cloudAgents.connect")}
					</Button>
				</div>
				<div className="mt-3 border-t border-border px-3 pt-3">
					<SettingsRow key="github-pat" icon={KeyRound} label={t("settings.cloudAgents.github.title")}>
						<span className="text-sm leading-5 text-settings-muted">{githubPATConnected ? t("settings.cloudAgents.github.connected") : t("settings.cloudAgents.github.notConnected")}</span>
					</SettingsRow>
					<GitHubTokenField
						id="settings-github-pat"
						bare
						className="mt-2"
						label={t("settings.cloudAgents.github.tokenLabel")}
						hint={t("settings.cloudAgents.github.tokenHint")}
						value={githubPAT}
						disabled={githubPATBusy}
						error={githubPATError}
						submitLabel={githubPATBusy ? t("settings.cloudAgents.github.saving") : t("settings.cloudAgents.github.save")}
						submitVariant="outline"
						submitDisabled={githubPATBusy}
						onChange={setGitHubPAT}
						onSubmit={() => void saveGitHubPAT()}
					/>
					{githubPATConnected ? (
						<div className="mt-2 flex justify-end">
							<Button type="button" variant="footer" disabled={githubPATBusy} onClick={() => void removeGitHubPAT()}>
								{t("settings.cloudAgents.github.remove")}
							</Button>
						</div>
					) : null}
				</div>
			</div>
		</SettingsSection>
	);
}
