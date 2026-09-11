import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { CloudCpProviderConnection } from "../../lib/cloud-cp";
import { useCloudCp } from "../../hooks/useCloudCp";
import { userProviderConnectionsQueryKey } from "../../hooks/useProviderConnections";
import { Button } from "../ui/button";
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
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { cn } from "../../lib/utils";

function configString(connection: CloudCpProviderConnection | undefined, key: string, fallback = ""): string {
	const value = connection?.config[key];
	return typeof value === "string" ? value : fallback;
}

function parseParameters(raw: string, invalidLineMessage: string, emptyNameMessage: string): Record<string, string> {
	const parameters: Record<string, string> = {};
	for (const line of raw.split("\n")) {
		if (line.trim() === "") continue;
		const separator = line.indexOf("=");
		if (separator <= 0) throw new Error(`${invalidLineMessage}: ${line}`);
		const name = line.slice(0, separator).trim();
		if (name === "") throw new Error(emptyNameMessage);
		parameters[name] = line.slice(separator + 1);
	}
	return parameters;
}

function serializedParameters(connection: CloudCpProviderConnection | undefined): string {
	const value = connection?.config.parameters;
	if (value === null || typeof value !== "object" || Array.isArray(value)) return "";
	return Object.entries(value)
		.filter((entry): entry is [string, string] => typeof entry[1] === "string")
		.map(([name, parameterValue]) => `${name}=${parameterValue}`)
		.join("\n");
}

export function CoderConnectionDialog({
	open,
	onOpenChange,
	connection,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	connection?: CloudCpProviderConnection;
}) {
	const { t } = useTranslation();
	const { client } = useCloudCp();
	const queryClient = useQueryClient();
	const [baseUrl, setBaseUrl] = useState("");
	const [apiToken, setApiToken] = useState("");
	const [templateId, setTemplateId] = useState("");
	const [agentName, setAgentName] = useState("");
	const [durableRoot, setDurableRoot] = useState("/workspace");
	const [parameters, setParameters] = useState("");
	const [submitting, setSubmitting] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [connected, setConnected] = useState(false);

	useEffect(() => {
		if (!open) return;
		setBaseUrl(configString(connection, "baseUrl"));
		setApiToken("");
		setTemplateId(configString(connection, "templateId"));
		setAgentName(configString(connection, "agentName"));
		setDurableRoot(configString(connection, "durableRoot", "/workspace"));
		setParameters(serializedParameters(connection));
		setSubmitting(false);
		setError(null);
		setConnected(false);
	}, [open, connection]);

	const canSubmit =
		!submitting &&
		baseUrl.trim() !== "" &&
		apiToken.trim() !== "" &&
		templateId.trim() !== "" &&
		durableRoot.trim() !== "";

	const submit = async () => {
		if (!canSubmit) return;
		setSubmitting(true);
		setError(null);
		try {
			await client.putUserCoderConnection({
				baseUrl: baseUrl.trim(),
				apiToken: apiToken.trim(),
				templateId: templateId.trim(),
				agentName: agentName.trim() || undefined,
				parameters: parseParameters(
					parameters,
					t("settings.coder.parameterInvalid"),
					t("settings.coder.parameterNameEmpty"),
				),
				durableRoot: durableRoot.trim(),
			});
			setApiToken("");
			await queryClient.invalidateQueries({
				queryKey: userProviderConnectionsQueryKey,
			});
			setConnected(true);
		} catch (submitError) {
			setError(submitError instanceof Error ? submitError.message : t("settings.coder.connectFailed"));
		} finally {
			setSubmitting(false);
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className={cn(settingsDialogContentClass, "w-[min(560px,calc(100vw-24px))]")}>
				<div className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title">{t("settings.coder.connect")}</DialogTitle>
					<DialogDescription className="text-control leading-4 text-settings-muted">
						{t("settings.coder.dialogDescription")}
					</DialogDescription>
				</div>

				{connected ? (
					<div className={settingsDialogBodyClass}>
						<p role="status" className="text-control leading-5 text-success">
							{t("settings.coder.connectedMessage")}
						</p>
					</div>
				) : (
					<div className={cn(settingsDialogBodyClass, "grid grid-cols-2 gap-x-3 gap-y-4")}>
						<div className="col-span-2 flex flex-col gap-1.5">
							<Label htmlFor="coder-base-url">{t("settings.coder.url")}</Label>
							<Input
								id="coder-base-url"
								type="url"
								placeholder={t("settings.coder.urlPlaceholder")}
								value={baseUrl}
								onChange={(event) => setBaseUrl(event.target.value)}
							/>
							<p className="text-caption leading-4 text-settings-muted">
								{t("settings.coder.urlHint")}
							</p>
						</div>
						<div className="col-span-2 flex flex-col gap-1.5">
							<Label htmlFor="coder-token">{t("settings.coder.token")}</Label>
							<Input
								id="coder-token"
								type="password"
								autoComplete="off"
								spellCheck={false}
								value={apiToken}
								onChange={(event) => setApiToken(event.target.value)}
							/>
							<p className="text-caption leading-4 text-settings-muted">
								{t("settings.coder.tokenHint")}
							</p>
						</div>
						<div className="col-span-2 flex flex-col gap-1.5">
							<Label htmlFor="coder-template-id">{t("settings.coder.template")}</Label>
							<Input
								id="coder-template-id"
								spellCheck={false}
								placeholder={t("settings.coder.templatePlaceholder")}
								value={templateId}
								onChange={(event) => setTemplateId(event.target.value)}
							/>
						</div>
						<div className="flex flex-col gap-1.5">
							<Label htmlFor="coder-agent-name">
								{t("settings.coder.agent")} <span className="text-settings-muted">{t("settings.coder.optional")}</span>
							</Label>
							<Input
								id="coder-agent-name"
								spellCheck={false}
								placeholder={t("settings.coder.autoDetect")}
								value={agentName}
								onChange={(event) => setAgentName(event.target.value)}
							/>
						</div>
						<div className="flex flex-col gap-1.5">
							<Label htmlFor="coder-durable-root">{t("settings.coder.durableRoot")}</Label>
							<Input
								id="coder-durable-root"
								spellCheck={false}
								placeholder={t("settings.coder.durableRootPlaceholder")}
								value={durableRoot}
								onChange={(event) => setDurableRoot(event.target.value)}
							/>
						</div>
						<div className="col-span-2 flex flex-col gap-1.5">
							<Label htmlFor="coder-parameters">
								{t("settings.coder.parameters")} <span className="text-settings-muted">{t("settings.coder.optional")}</span>
							</Label>
							<textarea
								id="coder-parameters"
								className="min-h-20 rounded-md border border-input bg-transparent px-3 py-2 font-mono text-sm outline-none focus-visible:ring-1 focus-visible:ring-ring"
								spellCheck={false}
								placeholder={t("settings.coder.parametersPlaceholder")}
								value={parameters}
								onChange={(event) => setParameters(event.target.value)}
							/>
							<p className="text-caption leading-4 text-settings-muted">{t("settings.coder.parametersHint")}</p>
						</div>
						{error ? (
							<p role="alert" className="col-span-2 text-caption leading-4 text-error">
								{error}
							</p>
						) : null}
					</div>
				)}

				<div className={settingsDialogFooterClass}>
					<DialogClose asChild>
						<Button type="button" variant="footer">
							{connected ? t("settings.coder.done") : t("settings.coder.cancel")}
						</Button>
					</DialogClose>
					{!connected ? (
						<Button type="button" variant="footer-primary" disabled={!canSubmit} onClick={() => void submit()}>
							{submitting
								? t("settings.coder.validating")
								: connection
									? t("settings.coder.reconnect")
									: t("settings.coder.connect")}
						</Button>
					) : null}
				</div>
			</DialogContent>
		</Dialog>
	);
}
