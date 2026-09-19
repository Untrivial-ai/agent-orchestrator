import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
	ProjectGeneralSettingsView,
	ProjectSettingsFormView,
	ProjectSettingsSection,
	ProjectWorkflowSettingsView,
} from "@aoagents/product-ui";
import { Pencil } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { cloudProjectsQueryKey, useCloudProjectsQuery, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { useCloudCp } from "../hooks/useCloudCp";
import { useCloudOrg } from "../hooks/useCloudOrg";
import type {
	CloudCpAgentConfig,
	CloudCpAgentProvider,
	CloudCpProjectConfig,
	CloudCpReviewerSettings,
	CloudCpRoleSettings,
} from "../lib/cloud-cp";
import { ProductExternalLink } from "./ProductExternalLink";
import { SettingsInlineInput, SettingsRow } from "./settings/SettingsRow";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { Switch } from "./ui/switch";
import type { ProjectSettingsSaveState } from "./ProjectSettingsForm";

export type CloudProjectSettingsSection = "general" | "agents" | "workflow";

const CLOUD_AGENTS: Array<{ value: CloudCpAgentProvider; label: string }> = [
	{ value: "claude-code", label: "Claude Code" },
	{ value: "codex", label: "Codex" },
	{ value: "cursor", label: "Cursor" },
];

const permissionOptions = [
	"default",
	"accept-edits",
	"auto",
	"bypass-permissions",
] as const;

type FormState = {
	displayName: string;
	defaultBranch: string;
	sessionPrefix: string;
	worker: CloudCpRoleSettings;
	orchestrator: CloudCpRoleSettings;
	reviewer?: CloudCpReviewerSettings;
	autoReview: boolean;
};

function roleConfig(config: CloudCpAgentConfig | undefined): CloudCpAgentConfig {
	return { ...config };
}

function projectForm(config: CloudCpProjectConfig, displayName: string, defaultBranch: string): FormState {
	return {
		displayName,
		defaultBranch,
		sessionPrefix: config.sessionPrefix ?? "",
		worker: { agent: config.worker?.agent ?? "codex", agentConfig: roleConfig(config.worker?.agentConfig) },
		orchestrator: { agent: config.orchestrator?.agent ?? "codex", agentConfig: roleConfig(config.orchestrator?.agentConfig) },
		reviewer: config.reviewers?.[0],
		autoReview: config.autoReview ?? false,
	};
}

function roleSettings(agent: CloudCpAgentProvider, config: CloudCpAgentConfig): CloudCpRoleSettings {
	const next = Object.fromEntries(Object.entries(config).filter(([, value]) => value !== "")) as CloudCpAgentConfig;
	return { agent, ...(Object.keys(next).length > 0 ? { agentConfig: next } : {}) };
}

export function CloudProjectSettingsForm({
	projectId,
	section,
	onSaveState,
}: {
	projectId: string;
	section: CloudProjectSettingsSection;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const { client } = useCloudCp();
	const { org } = useCloudOrg();
	const projects = useCloudProjectsQuery();
	const project = projects.data?.find((candidate) => candidate.id === projectId);
	const [form, setForm] = useState<FormState | null>(null);
	const [validationError, setValidationError] = useState<string | undefined>();
	const [saved, setSaved] = useState(false);

	useEffect(() => {
		if (project) setForm(projectForm(project.config, project.displayName, project.defaultBranch));
	}, [project]);

	const mutation = useMutation({
		mutationFn: async () => {
			if (!project || !form || !org) throw new Error(t("settings.project.loadFailed"));
			const worker = roleSettings(form.worker.agent ?? "codex", form.worker.agentConfig ?? {});
			const orchestrator = roleSettings(form.orchestrator.agent ?? "codex", form.orchestrator.agentConfig ?? {});
			const reviewer = form.reviewer?.harness
				? [{
					harness: form.reviewer.harness,
					...(Object.keys(form.reviewer.agentConfig ?? {}).length > 0
						? { agentConfig: roleConfig(form.reviewer.agentConfig) }
						: {}),
				}]
				: undefined;
			const config: CloudCpProjectConfig = {
				...project.config,
				sessionPrefix: form.sessionPrefix.trim() || undefined,
				worker,
				orchestrator,
				reviewers: reviewer,
				autoReview: form.autoReview,
			};
			return client.updateProject(org.id, project.id, {
				displayName: form.displayName.trim(),
				defaultBranch: form.defaultBranch.trim(),
				config,
			});
		},
		onSuccess: async () => {
			setSaved(true);
			await Promise.all([
				queryClient.invalidateQueries({ queryKey: cloudProjectsQueryKey }),
				queryClient.invalidateQueries({ queryKey: workspaceQueryKey }),
			]);
		},
	});

	useEffect(() => {
		const error = validationError ?? (mutation.error instanceof Error ? mutation.error.message : undefined);
		onSaveState?.({
			phase: error ? "failed" : mutation.isPending ? "saving" : saved ? "saved" : "idle",
			error,
		});
	}, [mutation.error, mutation.isPending, onSaveState, saved, validationError]);

	useEffect(() => {
		if (!saved) return;
		const timeout = window.setTimeout(() => setSaved(false), 1800);
		return () => window.clearTimeout(timeout);
	}, [saved]);

	if (projects.isFetching && project === undefined) return <p className="text-sm text-settings-muted">{t("settings.project.loading")}</p>;
	if (!project || !form) return <p className="text-sm text-error">{t("settings.project.loadFailed")}</p>;

	return (
		<ProjectSettingsFormView
			id="project-settings-form"
			onSubmit={() => {
				setSaved(false);
				if (form.displayName.trim() === "") {
					setValidationError(t("settings.project.nameRequired"));
					return;
				}
				if (form.defaultBranch.trim() === "") {
					setValidationError(t("createProject.cloudDefaultBranchRequired"));
					return;
				}
				setValidationError(undefined);
				mutation.mutate();
			}}
		>
			{section === "general" && (
				<ProjectGeneralSettingsView
					displayName={form.displayName}
					externalLink={ProductExternalLink}
					icons={{ edit: <Pencil className="settings-inline-edit-icon" aria-hidden="true" /> }}
					onDisplayNameChange={(displayName) => setForm((current) => current ? { ...current, displayName } : current)}
					labels={{
						title: t("settings.project.identity"), name: t("settings.project.name"), id: t("settings.project.id"),
						kind: t("settings.project.kind"), path: t("settings.project.path"), repo: t("settings.project.repo"),
						workspaceRepos: t("settings.project.workspaceRepos"), workspaceReposEmpty: t("settings.project.childReposEmpty"),
						editName: t("settings.field.edit", { label: t("settings.project.name") }),
					}}
					project={{ id: project.id, kindLabel: t("createProject.kindCloud"), path: t("createProject.kindCloud"), repo: project.repositoryUrl, repoHref: project.repositoryUrl }}
				/>
			)}
			{section === "agents" && (
				<>
					<ProjectSettingsSection title={t("settings.project.agents")} titleHidden grouped>
						<CloudRoleFields
							label={t("settings.project.defaultWorker")}
							modelLabel={t("settings.models.workerModel")}
							role={form.worker}
							roleName={t("settings.models.workerRole")}
							onChange={(worker) => setForm((current) => current ? { ...current, worker } : current)}
						/>
						<CloudRoleFields
							label={t("settings.project.defaultOrchestrator")}
							modelLabel={t("settings.models.orchestratorModel")}
							role={form.orchestrator}
							roleName={t("settings.models.orchestratorRole")}
							onChange={(orchestrator) => setForm((current) => current ? { ...current, orchestrator } : current)}
						/>
					</ProjectSettingsSection>
					<ProjectSettingsSection title={t("settings.project.reviewer")} grouped>
						<CloudReviewerFields reviewer={form.reviewer} onChange={(reviewer) => setForm((current) => current ? { ...current, reviewer } : current)} />
						<SettingsRow label={t("settings.project.autoReviewToggle")}>
							<Switch aria-label={t("settings.project.autoReviewToggle")} checked={form.autoReview} onCheckedChange={(autoReview) => setForm((current) => current ? { ...current, autoReview } : current)} />
						</SettingsRow>
					</ProjectSettingsSection>
				</>
			)}
			{section === "workflow" && (
				<ProjectWorkflowSettingsView
					branch={form.defaultBranch}
					icons={{ edit: <Pencil className="settings-inline-edit-icon" aria-hidden="true" /> }}
					prefix={form.sessionPrefix}
					onBranchChange={(defaultBranch) => setForm((current) => current ? { ...current, defaultBranch } : current)}
					onPrefixChange={(sessionPrefix) => setForm((current) => current ? { ...current, sessionPrefix } : current)}
					labels={{ worktrees: t("settings.project.worktrees"), defaultBranch: t("settings.project.defaultBranch"), sessionPrefix: t("settings.project.sessionPrefix"), reviewers: t("settings.project.reviewers"), defaultReviewer: t("settings.project.defaultReviewer"), editDefaultBranch: t("settings.field.edit", { label: t("settings.project.defaultBranch") }), editSessionPrefix: t("settings.field.edit", { label: t("settings.project.sessionPrefix") }) }}
				/>
			)}
		</ProjectSettingsFormView>
	);
}

function CloudRoleFields({
	label,
	modelLabel,
	role,
	roleName,
	showAgentSelect = true,
	onChange,
}: {
	label: string;
	modelLabel: string;
	role: CloudCpRoleSettings;
	roleName: string;
	showAgentSelect?: boolean;
	onChange: (next: CloudCpRoleSettings) => void;
}) {
	const { t } = useTranslation();
	const agent = role?.agent ?? "codex";
	const config = role?.agentConfig ?? {};
	return (
		<>
			{showAgentSelect && <SettingsRow label={label}><CloudAgentSelect ariaLabel={label} value={agent} onChange={(next) => onChange({ agent: next, agentConfig: config })} /></SettingsRow>}
			<SettingsRow label={modelLabel}><SettingsInlineInput id={`${agent}-${roleName}-model`} label={modelLabel} value={config.model ?? ""} placeholder={t("settings.models.providerDefault")} onChange={(model) => onChange({ agent, agentConfig: { ...config, model } })} /></SettingsRow>
			{agent === "codex" && <SettingsRow label={t("settings.models.effort")}><SettingsInlineInput id={`${agent}-${roleName}-effort`} label={t("settings.models.effort")} value={config.effort ?? ""} placeholder={t("settings.models.providerDefault")} onChange={(effort) => onChange({ agent, agentConfig: { ...config, effort } })} /></SettingsRow>}
			<SettingsRow label={t("settings.project.roleApproval", { role: roleName })}><CloudPermissionSelect ariaLabel={t("settings.project.roleApproval", { role: roleName })} value={config.permissions ?? ""} onChange={(permissions) => onChange({ agent, agentConfig: { ...config, permissions } })} /></SettingsRow>
		</>
	);
}

function CloudReviewerFields({ reviewer, onChange }: { reviewer: FormState["reviewer"]; onChange: (next: FormState["reviewer"]) => void }) {
	const { t } = useTranslation();
	const harness = reviewer?.harness ?? "";
	const config = reviewer?.agentConfig ?? {};
	return <>
		<SettingsRow label={t("settings.project.defaultReviewer")}><SettingsOptionMenu aria-label={t("settings.project.defaultReviewer")} value={harness || "__default__"} options={[{ value: "__default__", label: t("settings.project.default") }, ...CLOUD_AGENTS]} onChange={(value) => onChange(value === "__default__" ? undefined : { harness: value as CloudCpAgentProvider, agentConfig: config })} /></SettingsRow>
		{harness && <CloudRoleFields label={t("settings.project.reviewerAgent")} modelLabel={t("settings.models.reviewerModel")} role={{ agent: harness, agentConfig: config }} roleName={t("settings.models.reviewerRole")} showAgentSelect={false} onChange={(next) => onChange({ harness: next.agent ?? harness, agentConfig: next.agentConfig })} />}
	</>;
}

function CloudAgentSelect({ ariaLabel, value, onChange }: { ariaLabel: string; value: CloudCpAgentProvider; onChange: (value: CloudCpAgentProvider) => void }) {
	return <SettingsOptionMenu aria-label={ariaLabel} value={value} options={CLOUD_AGENTS} onChange={(value) => onChange(value as CloudCpAgentProvider)} />;
}

function CloudPermissionSelect({ ariaLabel, value, onChange }: { ariaLabel: string; value: string; onChange: (value: string) => void }) {
	const { t } = useTranslation();
	return <SettingsOptionMenu aria-label={ariaLabel} value={value || "__default__"} options={[{ value: "__default__", label: `${t("settings.project.permissionAuto")} (${t("settings.project.default")})` }, ...permissionOptions.map((permission) => ({ value: permission, label: permission === "default" ? t("settings.project.permissionDefault") : permission === "accept-edits" ? t("settings.project.permissionAcceptEdits") : permission === "auto" ? t("settings.project.permissionAuto") : t("settings.project.permissionBypass") }))]} onChange={(value) => onChange(value === "__default__" ? "" : value)} />;
}
