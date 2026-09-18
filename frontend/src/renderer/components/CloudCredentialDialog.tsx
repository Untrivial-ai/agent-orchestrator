import { useEffect, useMemo, useState } from "react";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { AgentAvatar } from "./AgentAvatar";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import type { CloudCpAgentProvider, CloudCpOrganization } from "../lib/cloud-cp";
import {
	centeredOnboardingDialogClass,
	onboardingFieldErrorClass,
	onboardingFieldHintClass,
	onboardingFooterActionsEndClass,
	onboardingFormLabelClass,
} from "../lib/onboarding-ui";
import { useCloudCp } from "../hooks/useCloudCp";
import { useCloudOrg } from "../hooks/useCloudOrg";
import { providerConnectionsQueryKey } from "../hooks/useProviderConnections";
import { useCredentialDialogStore } from "../stores/credential-dialog-store";
import { cn } from "../lib/utils";
import { aoBridge } from "../lib/bridge";

const BROWSER_LOGIN = "browser_login";

// The coding-agent providers the control plane accepts, with the credential
// types each one validates (see cloud validAgentCredentialType). Codex's
// ChatGPT-subscription path deliberately has no secret field: the desktop app
// performs the browser login locally, then securely sends Codex's native auth
// document to the control plane. A session token is never pasted or displayed.
const AGENTS = [
	{
		agent: "claude-code",
		label: "Claude Code",
		creds: [
			{ value: "oauth_token", label: "Setup token" },
			{ value: "api_key", label: "API key" },
			{ value: BROWSER_LOGIN, label: "Log in with Anthropic" },
		],
	},
	{
		agent: "codex",
		label: "Codex",
		creds: [
			{ value: "api_key", label: "API key" },
			{ value: BROWSER_LOGIN, label: "Log in with ChatGPT" },
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
	const { client, baseUrl, ready } = useCloudCp();
	const { org, refetch: refetchOrg } = useCloudOrg();
	const queryClient = useQueryClient();
	const open = useCredentialDialogStore((s) => s.open);
	const setOpen = useCredentialDialogStore((s) => s.setOpen);

	const [agent, setAgent] = useState<CloudCpAgentProvider>(AGENTS[0].agent);
	const [credentialType, setCredentialType] = useState<string>(AGENTS[0].creds[0].value);
	const [secret, setSecret] = useState("");
	const [phase, setPhase] = useState<Phase>("idle");
	const [error, setError] = useState<string | null>(null);

	const creds = useMemo(() => AGENTS.find((a) => a.agent === agent)?.creds ?? AGENTS[0].creds, [agent]);
	const agentOptions = useMemo(() => AGENTS.map((entry) => ({ value: entry.agent, label: entry.label })), []);
	const credentialOptions = useMemo(
		() => creds.map((entry) => ({ value: entry.value, label: entry.label })),
		[creds],
	);
	const selectedAgent = AGENTS.find((entry) => entry.agent === agent);
	const selectedCredential = creds.find((entry) => entry.value === credentialType);
	const needsSecret = credentialType !== BROWSER_LOGIN;

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
		setSecret("");
		setError(null);
	};

	// The org query can still be settling (or may have failed transiently) while
	// the user is filling in the form. Do not leave Connect permanently disabled;
	// submit retries the get-or-create request before writing the credential.
	const canSubmit = phase !== "submitting" && needsSecret && secret.trim() !== "" && ready;
	const busy = phase === "submitting";

	const submit = async () => {
		if (!canSubmit) return;
		setPhase("submitting");
		setError(null);
		try {
			let targetOrg: CloudCpOrganization | undefined = org;
			if (targetOrg === undefined) {
				const result = await refetchOrg();
				targetOrg = result.data;
				if (targetOrg === undefined) {
					setPhase("idle");
					setError(t("cloudCredential.failed"));
					return;
				}
			}
			const { providerConnection } = await client.putAgentConnection(targetOrg.id, agent, {
				credentialType,
				secret: secret.trim(),
			});
			if (providerConnection.validationState !== "valid") {
				setPhase("idle");
				setError(t("cloudCredential.invalid", { state: providerConnection.validationState }));
				return;
			}
			await queryClient.invalidateQueries({ queryKey: providerConnectionsQueryKey(targetOrg.id) });
			setPhase("success");
			setSecret("");
		} catch (err) {
			setPhase("idle");
			setError(err instanceof Error ? err.message : t("cloudCredential.failed"));
		}
	};

	const loginWithBrowser = async () => {
		if (org === undefined || phase === "submitting") return;
		setPhase("submitting");
		setError(null);
		try {
			await aoBridge.cloud.connectProviderAuth({ baseUrl, orgId: org.id, provider: agent });
			await queryClient.invalidateQueries({ queryKey: providerConnectionsQueryKey(org.id) });
			setPhase("success");
		} catch (err) {
			setPhase("idle");
			setError(err instanceof Error ? err.message : t("cloudCredential.failed"));
		}
	};

	return (
		<Dialog open={open} onOpenChange={setOpen}>
			<DialogContent className={centeredOnboardingDialogClass} showCloseButton={false}>
				<DialogClose asChild>
					<button
						type="button"
						className="settings-dialog-close-button settings-close-button"
						aria-label={t("common.close")}
						disabled={busy}
					>
						<X className="size-icon-base" aria-hidden="true" />
					</button>
				</DialogClose>

				<DialogTitle className="px-4 pr-12 pt-3 text-balance text-[18px] font-semibold text-[var(--color-text-import-title)]">{t("cloudCredential.title")}</DialogTitle>
				<DialogDescription className="px-4 pr-12 pt-1 text-pretty text-[13px] leading-5 text-muted-foreground">
					{t("cloudCredential.description")}
				</DialogDescription>

				{phase === "success" ? (
					<div className="min-h-0 overflow-y-auto px-4 pb-1 pt-4">
						<p role="status" className="text-control leading-4 text-success">
							{t("cloudCredential.connected")}
						</p>
					</div>
				) : (
					<div className="flex min-h-0 flex-col gap-4 overflow-y-auto px-4 pb-1 pt-4">
						<div className="space-y-2">
							<Label htmlFor="cloud-cred-agent" className={onboardingFormLabelClass}>
								{t("cloudCredential.agentLabel")}
							</Label>
							<SettingsOptionMenu
								aria-label={t("cloudCredential.agentLabel")}
								value={agent}
								options={agentOptions}
								disabled={busy}
								menuAlign="start"
								onChange={onAgentChange}
								triggerClassName="composer-chip composer-toolbar-option h-control-form w-full justify-between"
								renderTrigger={() => (
									<span className="flex min-w-0 items-center gap-2">
										<AgentAvatar provider={agent} className="size-icon-base" decorative />
										<span className="min-w-0 truncate text-control text-foreground" title={selectedAgent?.label}>
											{selectedAgent?.label}
										</span>
									</span>
								)}
							/>
						</div>

						<div className="space-y-2">
							<Label htmlFor="cloud-cred-type" className={onboardingFormLabelClass}>
								{t("cloudCredential.typeLabel")}
							</Label>
							<SettingsOptionMenu
								aria-label={t("cloudCredential.typeLabel")}
								value={credentialType}
								options={credentialOptions}
								disabled={busy || creds.length === 1}
								menuAlign="start"
								onChange={(val) => {
									setCredentialType(val);
									setSecret("");
									setError(null);
								}}
								triggerClassName="composer-chip composer-toolbar-option h-control-form w-full justify-between"
								renderTrigger={() => (
									<span className="min-w-0 truncate text-control text-foreground" title={selectedCredential?.label}>
										{selectedCredential?.label}
									</span>
								)}
							/>
						</div>

						{needsSecret ? (
							<div className="space-y-2">
							<Label htmlFor="cloud-cred-secret" className={onboardingFormLabelClass}>
								{t("cloudCredential.tokenLabel")}
							</Label>
							<Input
								id="cloud-cred-secret"
								type="password"
								autoComplete="off"
								spellCheck={false}
								className="text-[13px]"
								placeholder={t("cloudCredential.tokenPlaceholder")}
								disabled={busy}
								value={secret}
								onChange={(e) => setSecret(e.target.value)}
								onKeyDown={(e) => {
									if (e.key === "Enter") void submit();
								}}
							/>
							<p className={onboardingFieldHintClass}>{t("cloudCredential.tokenHint")}</p>
						</div>
						) : (
							<p className={onboardingFieldHintClass}>
								{agent === "claude-code"
									? t("cloudCredential.anthropicLoginDescription")
									: t("cloudCredential.chatgptLoginDescription")}
							</p>
						)}

						{error ? (
							<p role="alert" className={onboardingFieldErrorClass}>
								{error}
							</p>
						) : null}
					</div>
				)}

				<div className={cn(onboardingFooterActionsEndClass, "px-4 pb-4")}>
					{phase === "submitting" && !needsSecret ? (
						<Button type="button" variant="outline" onClick={() => void aoBridge.cloud.cancelProviderAuth()}>
							{t("cloudCredential.cancel")}
						</Button>
					) : (
						<DialogClose asChild>
							<Button type="button" variant="outline" disabled={busy}>
								{phase === "success" ? t("cloudCredential.done") : t("cloudCredential.cancel")}
							</Button>
						</DialogClose>
					)}
					{phase !== "success" && needsSecret ? (
						<Button type="button" variant="primary" disabled={!canSubmit} onClick={() => void submit()}>
							{phase === "submitting" ? t("cloudCredential.connecting") : t("cloudCredential.connect")}
						</Button>
					) : null}
					{phase !== "success" && !needsSecret ? (
						<Button type="button" variant="primary" disabled={org === undefined || phase === "submitting"} onClick={() => void loginWithBrowser()}>
							{phase === "submitting" ? t("cloudCredential.connecting") : (agent === "claude-code" ? t("cloudCredential.loginWithAnthropic") : t("cloudCredential.loginWithChatGPT"))}
						</Button>
					) : null}
				</div>
			</DialogContent>
		</Dialog>
	);
}
