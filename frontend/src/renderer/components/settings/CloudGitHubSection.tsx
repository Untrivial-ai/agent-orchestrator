import { GitBranch } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useCloudCp } from "../../hooks/useCloudCp";
import { useCloudOrg } from "../../hooks/useCloudOrg";
import { aoBridge } from "../../lib/bridge";
import { Button } from "../ui/button";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

/** GitHub App installation and repository access for Cloud projects. */
export function CloudGitHubSection({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const { client, baseUrl } = useCloudCp();
	const { org } = useCloudOrg();
	const [connecting, setConnecting] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const previousInstallations = useRef<Map<string, string> | null>(null);
	const installations = useQuery({
		queryKey: ["cloud-github-installations", baseUrl, org?.id],
		enabled: org !== undefined,
		queryFn: async () => (await client.listGitHubInstallations(org!.id)).installations,
		refetchInterval: connecting ? 2500 : false,
	});
	const active = installations.data?.filter((installation) => installation.status === "active") ?? [];
	const connected = active.length > 0;
	const accounts = [...new Set(active.map((installation) => installation.accountLogin))].join(", ");

	useEffect(() => {
		if (!connecting || !org || !installations.data || !previousInstallations.current) return;
		const changed = installations.data.find((installation) =>
			installation.status === "active" && previousInstallations.current?.get(installation.id) !== installation.updatedAt,
		);
		if (!changed) return;
		setConnecting(false);
		previousInstallations.current = null;
		if (changed.syncStatus !== "ready") {
			void client.syncGitHubInstallation(org.id, changed.id)
				.then(() => installations.refetch())
				.catch((cause: unknown) => setError(cause instanceof Error ? cause.message : t("settings.cloudGithub.syncError")));
		}
	}, [client, connecting, installations.data, installations.refetch, org, t]);

	useEffect(() => {
		if (!connecting) return;
		const timeout = window.setTimeout(() => {
			setConnecting(false);
			previousInstallations.current = null;
			setError(t("settings.cloudGithub.timeout"));
		}, 5 * 60_000);
		return () => window.clearTimeout(timeout);
	}, [connecting, t]);

	const connect = async () => {
		if (!org || connecting) return;
		setConnecting(true);
		setError(null);
		try {
			const { installations: before } = await client.listGitHubInstallations(org.id);
			previousInstallations.current = new Map(before.map((installation) => [installation.id, installation.updatedAt]));
			const { installationUrl } = await client.startGitHubInstallation(org.id);
			await aoBridge.app.openExternal(installationUrl);
		} catch (cause) {
			setConnecting(false);
			previousInstallations.current = null;
			setError(cause instanceof Error ? cause.message : t("settings.cloudGithub.connectError"));
		}
	};

	return (
		<SettingsSection title="GitHub" sectionId="cloud-github" titleHidden={titleHidden}>
			<p className="px-3 pb-2 text-xs leading-relaxed text-muted-foreground">{t("settings.cloudGithub.description")}</p>
			<SettingsRow icon={GitBranch} label="GitHub" description={accounts || undefined}>
				<span className="mr-3 text-xs text-settings-muted">
					{installations.isPending ? t("settings.cloudAgents.loading")
						: installations.isError ? t("settings.cloudGithub.loadError")
							: connected ? t("settings.cloudAgents.valid") : t("settings.cloudGithub.notConnected")}
				</span>
				<Button type="button" variant="footer" disabled={!org || connecting} onClick={() => void connect()}>
					{connecting ? t("settings.cloudGithub.waiting")
						: connected ? t("settings.cloudGithub.manage") : t("createProject.connectGitHub")}
				</Button>
			</SettingsRow>
			{error ? <p role="alert" className="px-3 pt-2 text-xs text-error">{error}</p> : null}
		</SettingsSection>
	);
}
