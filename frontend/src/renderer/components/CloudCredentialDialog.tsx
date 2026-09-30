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
import type { CloudCpAgentProvider } from "../lib/cloud-cp";
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

// The coding-agent providers supported by the cloud connection flow, with the
// credential values each one accepts. Browser login deliberately has no secret
// field: the desktop app performs the login locally.
const AGENTS = [
	{
		agent: "claude-code",
		label: "Claude Code",
		titleKey: "cloudCredential.titleClaudeCode",
		creds: [
			{ value: "oauth_token", labelKey: "cloudCredential.claudeSetupToken" },
			{ value: "api_key", labelKey: "cloudCredential.anthropicApiKey" },
			{ value: BROWSER_LOGIN, labelKey: "cloudCredential.loginWithAnthropic" },
		],
	},
	{
		agent: "codex",
		label: "Codex",
		titleKey: "cloudCredential.titleCodex",
		creds: [
			{ value: "api_key", labelKey: "cloudCredential.openaiApiKey" },
			{ value: BROWSER_LOGIN, labelKey: "cloudCredential.loginWithChatGPT" },
		],
	},
	{
		agent: "cursor",
		label: "Cursor",
		titleKey: "cloudCredential.titleCursor",
		creds: [{ value: "api_key", labelKey: "cloudCredential.cursorApiKey" }],
	},
	{
		agent: "opencode",
		label: "OpenCode",
		titleKey: "cloudCredential.titleOpenCode",
		// OpenCode accepts keys for several providers. The selected value is sent
		// as the credential type so the worker can use the matching key.
		creds: [
			{ value: "opencode_api_key", labelKey: "cloudCredential.opencodeApiKey" },
			{ value: "anthropic_api_key", labelKey: "cloudCredential.anthropicApiKey" },
			{ value: "openai_api_key", labelKey: "cloudCredential.openaiApiKey" },
			{ value: "openrouter_api_key", labelKey: "cloudCredential.openrouterApiKey" },
		],
	},
] as const;

type Phase = "idle" | "submitting" | "success";

// Connects a developer's local coding-agent credential (Claude Code setup
// token, Codex/Cursor key) to their cloud org so the sandbox worker can run the
// agent. Replaces the dev-only cloud/scripts/dev-connect-agent-credential.py:
// same PUT /orgs/{org}/provider-connections/agents/{agent}, in the app.
export function CloudCredentialDialog() {
	const { t } = useTranslation();
	const { client, baseUrl } = useCloudCp();
	const { org } = useCloudOrg();
	const queryClient = useQueryClient();
	const open = useCredentialDialogStore((s) => s.open);
	const setOpen = useCredentialDialogStore((s) => s.setOpen);
	const targetAgent = useCredentialDialogStore((s) => s.targetAgent);
	const targetCredentialType = useCredentialDialogStore((s) => s.targetCredentialType);

	const [agent, setAgent] = useState<CloudCpAgentProvider>(AGENTS[0].agent);
	const [credentialType, setCredentialType] = useState<string>(AGENTS[0].creds[0].value);
	const [secret, setSecret] = useState("");
	const [phase, setPhase] = useState<Phase>("idle");
	const [error, setError] = useState<string | null>(null);

	const creds = useMemo(() => AGENTS.find((a) => a.agent === agent)?.creds ?? AGENTS[0].creds, [agent]);
	const agentOptions = useMemo(() => AGENTS.map((entry) => ({ value: entry.agent, label: entry.label })), []);
	const credentialOptions = useMemo(
		() => creds.map((entry) => ({ value: entry.value, label: t(entry.labelKey) })),
		[creds, t],
	);
	const selectedAgent = AGENTS.find((entry) => entry.agent === agent);
	const selectedCredential = creds.find((entry) => entry.value === credentialType);
	const selectedCredentialLabel = selectedCredential ? t(selectedCredential.labelKey) : "";
	const needsSecret = credentialType !== BROWSER_LOGIN;
	const showCredentialType = creds.length > 1;
	// When opened from a specific harness row (targetAgent) the agent is fixed to
	// that harness. The generic settings entry keeps the agent picker.
	const agentLocked = Boolean(targetAgent && AGENTS.some((a) => a.agent === targetAgent));
	const openCodeProviders: Record<string, string> = {
		opencode_api_key: "OpenCode",
		anthropic_api_key: "Anthropic",
		openai_api_key: "OpenAI",
		openrouter_api_key: "OpenRouter",
	};
	const keyProvider = agent === "claude-code" ? "Anthropic"
		: agent === "codex" ? "OpenAI"
			: agent === "cursor" ? "Cursor" : openCodeProviders[credentialType] ?? "OpenCode";
	const secretCopy = (() => {
		if (agent === "claude-code" && credentialType === "oauth_token") {
			return {
				label: t("cloudCredential.claudeSetupToken"),
				placeholder: t("cloudCredential.setupTokenPlaceholder"),
				hint: t("cloudCredential.setupTokenHint"),
			};
		}
		if (agent === "claude-code" && credentialType === "api_key") {
			return {
				label: t("cloudCredential.anthropicApiKey"),
				placeholder: t("cloudCredential.anthropicApiKeyPlaceholder"),
				hint: t("cloudCredential.anthropicApiKeyHint"),
			};
		}
		if (agent === "codex") {
			return {
				label: t("cloudCredential.openaiApiKey"),
				placeholder: t("cloudCredential.openaiApiKeyPlaceholder"),
				hint: t("cloudCredential.openaiApiKeyHint"),
			};
		}
		if (agent === "cursor") {
			return {
				label: t("cloudCredential.cursorApiKey"),
				placeholder: t("cloudCredential.cursorApiKeyPlaceholder"),
				hint: t("cloudCredential.cursorApiKeyHint"),
			};
		}
		return {
			label: selectedCredentialLabel,
			placeholder: t("cloudCredential.providerApiKeyPlaceholder", { provider: keyProvider }),
			hint: t("cloudCredential.providerApiKeyHint", { provider: keyProvider }),
		};
	})();
	const title = agentLocked ? t(selectedAgent?.titleKey ?? "cloudCredential.title") : t("cloudCredential.title");
	const description = needsSecret
		? credentialType === "oauth_token"
			? t("cloudCredential.setupTokenDescription")
			: t("cloudCredential.apiKeyDescription", { provider: keyProvider, agent: selectedAgent?.label })
		: agent === "claude-code"
			? t("cloudCredential.anthropicLoginDescription")
			: t("cloudCredential.chatgptLoginDescription");

	// Reset the whole form each time the dialog opens so a reopen never shows a
	// stale secret or a previous error/success. Pre-select the harness the user
	// opened it from, when scoped.
	useEffect(() => {
		if (!open) return;
		const initial = AGENTS.find((a) => a.agent === targetAgent) ?? AGENTS[0];
		const requestedType = initial.agent === "codex" && (targetCredentialType === "auth_json" || targetCredentialType === "access_token")
			? BROWSER_LOGIN : targetCredentialType;
		setAgent(initial.agent);
		setCredentialType(initial.creds.find((entry) => entry.value === requestedType)?.value ?? initial.creds[0].value);
		setSecret("");
		setPhase("idle");
		setError(null);
	}, [open, targetAgent, targetCredentialType]);

	const onAgentChange = (next: string) => {
		const agentValue = (AGENTS.find((a) => a.agent === next) ?? AGENTS[0]).agent;
		setAgent(agentValue);
		setCredentialType(AGENTS.find((a) => a.agent === agentValue)?.creds[0]?.value ?? "api_key");
		setSecret("");
		setError(null);
	};

	const canSubmit = phase !== "submitting" && needsSecret && secret.trim() !== "" && org !== undefined;
	const busy = phase === "submitting";

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

				<DialogTitle className="px-4 pr-12 pt-3 text-balance text-[18px] font-semibold text-[var(--color-text-import-title)]">{title}</DialogTitle>
				<DialogDescription className="px-4 pr-12 pt-1 text-pretty text-[13px] leading-5 text-muted-foreground">
					{description}
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
							{agentLocked ? (
								<div className="composer-chip composer-toolbar-option flex h-control-form w-full items-center gap-2 px-3">
									<AgentAvatar provider={agent} className="size-icon-base" decorative />
									<span className="min-w-0 truncate text-control text-foreground" title={selectedAgent?.label}>
										{selectedAgent?.label}
									</span>
								</div>
							) : (
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
							)}
						</div>

						{showCredentialType ? <div className="space-y-2">
							<Label htmlFor="cloud-cred-type" className={onboardingFormLabelClass}>
								{t("cloudCredential.methodLabel")}
							</Label>
							<SettingsOptionMenu
								aria-label={t("cloudCredential.methodLabel")}
								value={credentialType}
								options={credentialOptions}
								disabled={busy}
								menuAlign="start"
								onChange={(val) => {
									setCredentialType(val);
									setSecret("");
									setError(null);
								}}
								triggerClassName="composer-chip composer-toolbar-option h-control-form w-full justify-between"
								renderTrigger={() => (
									<span className="min-w-0 truncate text-control text-foreground" title={selectedCredentialLabel}>
										{selectedCredentialLabel}
									</span>
								)}
							/>
						</div> : null}

						{needsSecret ? (
							<div className="space-y-2">
							<Label htmlFor="cloud-cred-secret" className={onboardingFormLabelClass}>
								{secretCopy.label}
							</Label>
							<Input
								id="cloud-cred-secret"
								type="password"
								autoComplete="off"
								spellCheck={false}
								className="text-[13px]"
								placeholder={secretCopy.placeholder}
								disabled={busy}
								value={secret}
								onChange={(e) => setSecret(e.target.value)}
								onKeyDown={(e) => {
									if (e.key === "Enter") void submit();
								}}
							/>
							<p className={onboardingFieldHintClass}>{secretCopy.hint}</p>
							<p className={onboardingFieldHintClass}>{t("cloudCredential.storageHint")}</p>
						</div>
						) : null}

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
