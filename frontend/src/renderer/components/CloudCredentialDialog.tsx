import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import type { CloudCpAgentProvider } from "../lib/cloud-cp";
import { useCloudCp } from "../hooks/useCloudCp";
import { useCloudOrg } from "../hooks/useCloudOrg";
import { providerConnectionsQueryKey } from "../hooks/useProviderConnections";
import { useCredentialDialogStore } from "../stores/credential-dialog-store";
import { cn } from "../lib/utils";

// The coding-agent providers the control plane accepts, with the credential
// types each one validates (see cloud validAgentCredentialType). The first
// credential type is the default and matches the "setup token" a developer
// normally pastes.
const AGENTS = [
	{
		agent: "claude-code",
		label: "Claude Code",
		creds: [
			{ value: "oauth_token", label: "Setup token" },
			{ value: "api_key", label: "API key" },
		],
	},
	{
		agent: "codex",
		label: "Codex",
		creds: [
			{ value: "access_token", label: "Access token" },
			{ value: "api_key", label: "API key" },
		],
	},
	{
		agent: "cursor",
		label: "Cursor",
		creds: [{ value: "api_key", label: "API key" }],
	},
] as const;

type Phase = "idle" | "submitting" | "success";

// Connects a developer's local coding-agent credential (Claude Code setup
// token, Codex/Cursor key) to their cloud org so the sandbox worker can run the
// agent. Replaces the dev-only cloud/scripts/dev-connect-agent-credential.py:
// same PUT /orgs/{org}/provider-connections/agents/{agent}, in the app.
export function CloudCredentialDialog() {
	const { t } = useTranslation();
	const { client } = useCloudCp();
	const { org } = useCloudOrg();
	const queryClient = useQueryClient();
	const open = useCredentialDialogStore((s) => s.open);
	const setOpen = useCredentialDialogStore((s) => s.setOpen);

	const [agent, setAgent] = useState<CloudCpAgentProvider>(AGENTS[0].agent);
	const [credentialType, setCredentialType] = useState<string>(AGENTS[0].creds[0].value);
	const [secret, setSecret] = useState("");
	const [phase, setPhase] = useState<Phase>("idle");
	const [error, setError] = useState<string | null>(null);

	const creds = useMemo(() => AGENTS.find((a) => a.agent === agent)?.creds ?? AGENTS[0].creds, [agent]);

	// Reset the whole form each time the dialog opens so a reopen never shows a
	// stale secret or a previous error/success.
	useEffect(() => {
		if (!open) return;
		setAgent(AGENTS[0].agent);
		setCredentialType(AGENTS[0].creds[0].value);
		setSecret("");
		setPhase("idle");
		setError(null);
	}, [open]);

	const onAgentChange = (next: string) => {
		const agentValue = (AGENTS.find((a) => a.agent === next) ?? AGENTS[0]).agent;
		setAgent(agentValue);
		setCredentialType(AGENTS.find((a) => a.agent === agentValue)?.creds[0]?.value ?? "api_key");
		setError(null);
	};

	const canSubmit = phase !== "submitting" && secret.trim() !== "" && org !== undefined;

	const submit = async () => {
		if (!canSubmit || org === undefined) return;
		setPhase("submitting");
		setError(null);
		try {
			const { providerConnection } = await client.putAgentConnection(org.id, agent, {
				credentialType,
				secret: secret.trim(),
			});
			if (providerConnection.validationState !== "valid") {
				setPhase("idle");
				setError(t("cloudCredential.invalid", { state: providerConnection.validationState }));
				return;
			}
			await queryClient.invalidateQueries({ queryKey: providerConnectionsQueryKey(org.id) });
			setPhase("success");
			setSecret("");
		} catch (err) {
			setPhase("idle");
			setError(err instanceof Error ? err.message : t("cloudCredential.failed"));
		}
	};

	return (
		<Dialog open={open} onOpenChange={setOpen}>
			<DialogContent className={settingsDialogContentClass}>
				<div className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title">{t("cloudCredential.title")}</DialogTitle>
					<DialogDescription asChild>
						<div className="text-control leading-4 text-settings-muted">{t("cloudCredential.description")}</div>
					</DialogDescription>
				</div>

				{phase === "success" ? (
					<div className={settingsDialogBodyClass}>
						<p role="status" className="text-control leading-4 text-success">
							{t("cloudCredential.connected")}
						</p>
					</div>
				) : (
					<div className={cn(settingsDialogBodyClass, "flex flex-col gap-4")}>
						<div className="flex flex-col gap-1.5">
							<Label htmlFor="cloud-cred-agent">{t("cloudCredential.agentLabel")}</Label>
							<Select value={agent} onValueChange={onAgentChange}>
								<SelectTrigger id="cloud-cred-agent">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{AGENTS.map((a) => (
										<SelectItem key={a.agent} value={a.agent}>
											{a.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>

						<div className="flex flex-col gap-1.5">
							<Label htmlFor="cloud-cred-type">{t("cloudCredential.typeLabel")}</Label>
							<Select value={credentialType} onValueChange={setCredentialType} disabled={creds.length === 1}>
								<SelectTrigger id="cloud-cred-type">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{creds.map((c) => (
										<SelectItem key={c.value} value={c.value}>
											{c.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>

						<div className="flex flex-col gap-1.5">
							<Label htmlFor="cloud-cred-secret">{t("cloudCredential.tokenLabel")}</Label>
							<Input
								id="cloud-cred-secret"
								type="password"
								autoComplete="off"
								spellCheck={false}
								placeholder={t("cloudCredential.tokenPlaceholder")}
								value={secret}
								onChange={(e) => setSecret(e.target.value)}
								onKeyDown={(e) => {
									if (e.key === "Enter") void submit();
								}}
							/>
							<p className="text-caption leading-4 text-settings-muted">{t("cloudCredential.tokenHint")}</p>
						</div>

						{error ? (
							<p role="alert" className="text-caption leading-4 text-error">
								{error}
							</p>
						) : null}
					</div>
				)}

				<div className={settingsDialogFooterClass}>
					<DialogClose asChild>
						<Button type="button" variant="footer">
							{phase === "success" ? t("cloudCredential.done") : t("cloudCredential.cancel")}
						</Button>
					</DialogClose>
					{phase !== "success" ? (
						<Button type="button" variant="footer-primary" disabled={!canSubmit} onClick={() => void submit()}>
							{phase === "submitting" ? t("cloudCredential.connecting") : t("cloudCredential.connect")}
						</Button>
					) : null}
				</div>
			</DialogContent>
		</Dialog>
	);
}
