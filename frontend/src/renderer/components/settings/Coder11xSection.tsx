import { KeyRound } from "lucide-react";
import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { SearchablePicker } from "../SearchablePicker";
import { useCloudGate } from "../../hooks/useCloudGate";
import { useCloudCp } from "../../hooks/useCloudCp";
import { useCloudOrg } from "../../hooks/useCloudOrg";
import { useCoderTemplates } from "../../hooks/useCoderTemplates";
import { orgCoderConfigQueryKey, useOrgCoderConfig } from "../../hooks/useOrgCoderConfig";
import { useCloudSession } from "../../lib/cloud-session";
import {
	onboardingFieldErrorClass,
	onboardingFieldHintClass,
	onboardingFormLabelClass,
} from "../../lib/onboarding-ui";
import { SettingsSection } from "./SettingsSection";

/**
 * Bring-your-own-Coder configuration for 11x: the connection the control plane
 * uses to drive the org's own Coder deployment (URL/IP, API token, owner,
 * default template, agent name). Only shown to @11x.ai users (gated by the
 * settings catalog). Mirrors CloudCredentialsSection's shape: the outer
 * component reads only the daemon cloud gate (a query the settings page already
 * runs), so a local-only app renders nothing and never mounts the cloud hooks.
 */
export function Coder11xSection({ titleHidden }: { titleHidden?: boolean }) {
	const { cloudEnabled } = useCloudGate();
	if (!cloudEnabled) return null;
	return <Coder11xSectionInner titleHidden={titleHidden} />;
}

function Coder11xSectionInner({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const { status } = useCloudSession();
	const { client } = useCloudCp();
	const { org } = useCloudOrg();
	const queryClient = useQueryClient();
	const config = useOrgCoderConfig();
	const orgId = org?.id ?? "";

	const [baseUrl, setBaseUrl] = useState("");
	const [token, setToken] = useState("");
	const [owner, setOwner] = useState("");
	const [templateId, setTemplateId] = useState("");
	const [agentName, setAgentName] = useState("");
	const [endpointServiceName, setEndpointServiceName] = useState("");
	const [region, setRegion] = useState("");
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [saved, setSaved] = useState(false);
	const [confirmingRemove, setConfirmingRemove] = useState(false);

	// Hydrate the editable non-secret fields from the loaded config. The token is
	// never returned by the control plane, so its field always starts empty.
	const loaded = config.data;
	useEffect(() => {
		if (!loaded) return;
		setBaseUrl(loaded.baseUrl ?? "");
		setOwner(loaded.owner ?? "");
		setTemplateId(loaded.defaultTemplateId ?? "");
		setAgentName(loaded.agentName ?? "");
		setEndpointServiceName(loaded.endpointServiceName ?? "");
		setRegion(loaded.region ?? "");
	}, [loaded]);

	// A connection must be saved before the control plane can reach the org's Coder
	// to list its templates, so only then do we enable the live template dropdown.
	const tokenStored = Boolean(loaded?.tokenSet);
	const { templates, isLoading: templatesLoading } = useCoderTemplates(orgId === "" ? undefined : orgId, tokenStored);
	// The coder-templates query is keyed by this prefix (see useCoderTemplates); a
	// prefix match re-runs it after the connection changes.
	const coderTemplatesQueryKey = ["cloud-coder-templates"] as const;

	// Saving the connection needs the signed-in org. This page is reachable while
	// signed out, so say why it is empty instead of rendering a blank pane.
	if (status !== "authenticated") {
		return (
			<SettingsSection title={t("settings.coder11x.title")} sectionId="coder11x" titleHidden={titleHidden}>
				<p className="px-3 text-xs leading-relaxed text-muted-foreground">{t("settings.coder11x.signIn")}</p>
			</SettingsSection>
		);
	}

	const canSave = !busy && orgId !== "" && baseUrl.trim() !== "" && owner.trim() !== "" && templateId.trim() !== "" && (tokenStored || token.trim() !== "");
	const save = async () => {
		if (!canSave) return;
		setBusy(true);
		setError(null);
		setSaved(false);
		try {
			await client.putOrgCoderConfig(orgId, {
				baseUrl: baseUrl.trim(),
				owner: owner.trim(),
				defaultTemplateId: templateId.trim(),
				agentName: agentName.trim() === "" ? undefined : agentName.trim(),
				endpointServiceName: endpointServiceName.trim() === "" ? undefined : endpointServiceName.trim(),
				region: region.trim() === "" ? undefined : region.trim(),
				token: token.trim() === "" ? undefined : token.trim(),
			});
			setToken("");
			setSaved(true);
			// The connection now points at a (possibly new) Coder, so its template list
			// may have changed — refresh both the config and the templates.
			await Promise.all([
				queryClient.invalidateQueries({ queryKey: orgCoderConfigQueryKey }),
				queryClient.invalidateQueries({ queryKey: coderTemplatesQueryKey }),
			]);
		} catch (caught) {
			setError(caught instanceof Error ? caught.message : t("settings.coder11x.errorSave"));
		} finally {
			setBusy(false);
		}
	};

	// Removing the connection reverts the org's cloud sessions to the deployment
	// default. A two-step confirm (see MobileDevicesSection) guards the destructive
	// click without a modal.
	const remove = async () => {
		if (orgId === "") return;
		setBusy(true);
		setError(null);
		setSaved(false);
		try {
			await client.deleteOrgCoderConfig(orgId);
			setConfirmingRemove(false);
			setToken("");
			await Promise.all([
				queryClient.invalidateQueries({ queryKey: orgCoderConfigQueryKey }),
				queryClient.invalidateQueries({ queryKey: coderTemplatesQueryKey }),
			]);
		} catch (caught) {
			setError(caught instanceof Error ? caught.message : t("settings.coder11x.errorRemove"));
		} finally {
			setBusy(false);
		}
	};

	return (
		<SettingsSection title={t("settings.coder11x.title")} sectionId="coder11x" titleHidden={titleHidden}>
			<div className="flex w-full flex-col gap-4 px-3 py-1">
				<p className="text-xs leading-relaxed text-muted-foreground">{t("settings.coder11x.description")}</p>

				<div className="flex flex-col gap-1.5">
					<Label htmlFor="coder11x-base-url" className={onboardingFormLabelClass}>{t("settings.coder11x.urlLabel")}</Label>
					<p className={onboardingFieldHintClass}>{t("settings.coder11x.urlHint")}</p>
					<Input
						id="coder11x-base-url"
						type="text"
						autoComplete="off"
						spellCheck={false}
						className="font-mono text-[13px]"
						placeholder={t("settings.coder11x.urlPlaceholder")}
						disabled={busy}
						value={baseUrl}
						onChange={(event) => setBaseUrl(event.target.value)}
					/>
				</div>

				<div className="flex flex-col gap-1.5">
					<Label htmlFor="coder11x-token" className={onboardingFormLabelClass}>{t("settings.coder11x.tokenLabel")}</Label>
					<p className={onboardingFieldHintClass}>{t("settings.coder11x.tokenHint")}</p>
					<div className="relative">
						<span className="pointer-events-none absolute inset-y-0 left-3 flex w-4 items-center justify-center text-muted-foreground">
							<KeyRound className="size-4" aria-hidden="true" />
						</span>
						<Input
							id="coder11x-token"
							type="password"
							autoComplete="off"
							spellCheck={false}
							className="pl-10 font-mono text-[13px]"
							placeholder={tokenStored ? t("settings.coder11x.tokenStored") : t("settings.coder11x.tokenPlaceholder")}
							disabled={busy}
							value={token}
							onChange={(event) => setToken(event.target.value)}
						/>
					</div>
				</div>

				<div className="flex flex-col gap-1.5">
					<Label htmlFor="coder11x-owner" className={onboardingFormLabelClass}>{t("settings.coder11x.ownerLabel")}</Label>
					<Input
						id="coder11x-owner"
						type="text"
						autoComplete="off"
						spellCheck={false}
						className="text-[13px]"
						placeholder={t("settings.coder11x.ownerPlaceholder")}
						disabled={busy}
						value={owner}
						onChange={(event) => setOwner(event.target.value)}
					/>
				</div>

				<div className="flex flex-col gap-1.5">
					<Label htmlFor="coder11x-template" className={onboardingFormLabelClass}>{t("settings.coder11x.templateLabel")}</Label>
					<p className={onboardingFieldHintClass}>{t("settings.coder11x.templateHint")}</p>
					{tokenStored && templates.length > 0 ? (
						// Live template list from the org's own Coder (powered by the same
						// per-org endpoint the project-creation picker uses).
						<SearchablePicker
							ariaLabel={t("settings.coder11x.templateLabel")}
							placeholder={t("settings.coder11x.templateSelect")}
							searchPlaceholder={t("settings.coder11x.templateSearch")}
							value={templateId}
							onChange={setTemplateId}
							disabled={busy}
							options={templates.map((tpl) => ({
								value: tpl.id,
								label: tpl.displayName || tpl.name,
								description: tpl.description,
							}))}
						/>
					) : (
						// Fall back to a plain UUID input when no connection is saved yet, or
						// the list is still loading, empty, or unreachable.
						<>
							<Input
								id="coder11x-template"
								type="text"
								autoComplete="off"
								spellCheck={false}
								className="font-mono text-[13px]"
								placeholder={t("settings.coder11x.templatePlaceholder")}
								disabled={busy}
								value={templateId}
								onChange={(event) => setTemplateId(event.target.value)}
							/>
							{tokenStored && !templatesLoading ? (
								<p className={onboardingFieldHintClass}>{t("settings.coder11x.templateManualHint")}</p>
							) : null}
						</>
					)}
				</div>

				<div className="flex flex-col gap-1.5">
					<Label htmlFor="coder11x-agent" className={onboardingFormLabelClass}>{t("settings.coder11x.agentLabel")}</Label>
					<p className={onboardingFieldHintClass}>{t("settings.coder11x.agentHint")}</p>
					<Input
						id="coder11x-agent"
						type="text"
						autoComplete="off"
						spellCheck={false}
						className="text-[13px]"
						placeholder={t("settings.coder11x.agentPlaceholder")}
						disabled={busy}
						value={agentName}
						onChange={(event) => setAgentName(event.target.value)}
					/>
				</div>

				<div className="flex flex-col gap-1.5">
					<Label htmlFor="coder11x-endpoint-service" className={onboardingFormLabelClass}>{t("settings.coder11x.endpointServiceLabel")}</Label>
					<Input
						id="coder11x-endpoint-service"
						type="text"
						autoComplete="off"
						spellCheck={false}
						className="font-mono text-[13px]"
						placeholder={t("settings.coder11x.endpointServicePlaceholder")}
						disabled={busy}
						value={endpointServiceName}
						onChange={(event) => setEndpointServiceName(event.target.value)}
					/>
				</div>

				<div className="flex flex-col gap-1.5">
					<Label htmlFor="coder11x-region" className={onboardingFormLabelClass}>{t("settings.coder11x.regionLabel")}</Label>
					<p className={onboardingFieldHintClass}>{t("settings.coder11x.regionHint")}</p>
					<Input
						id="coder11x-region"
						type="text"
						autoComplete="off"
						spellCheck={false}
						className="font-mono text-[13px]"
						placeholder={t("settings.coder11x.regionPlaceholder")}
						disabled={busy}
						value={region}
						onChange={(event) => setRegion(event.target.value)}
					/>
				</div>

				{error ? <p className={onboardingFieldErrorClass} role="alert">{error}</p> : null}

				<div className="flex items-center justify-between gap-3">
					<div className="flex items-center gap-2">
						{tokenStored ? (
							confirmingRemove ? (
								<>
									<Button type="button" variant="ghost" disabled={busy} onClick={() => setConfirmingRemove(false)}>
										{t("settings.coder11x.removeCancel")}
									</Button>
									<Button type="button" variant="footer" className="text-error" disabled={busy} onClick={() => void remove()}>
										{busy ? t("settings.coder11x.removing") : t("settings.coder11x.removeConfirm")}
									</Button>
								</>
							) : (
								<Button type="button" variant="footer" disabled={busy} onClick={() => setConfirmingRemove(true)}>
									{t("settings.coder11x.remove")}
								</Button>
							)
						) : null}
					</div>
					<div className="flex items-center gap-3">
						{saved && !busy ? <span className="text-xs text-settings-muted">{t("settings.coder11x.saved")}</span> : null}
						<Button type="button" variant="outline" disabled={!canSave} onClick={() => void save()}>
							{busy ? t("settings.coder11x.saving") : t("settings.coder11x.save")}
						</Button>
					</div>
				</div>
			</div>
		</SettingsSection>
	);
}
