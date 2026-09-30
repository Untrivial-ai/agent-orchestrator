import { useMutation } from "@tanstack/react-query";
import { MAX_PROJECT_DISPLAY_NAME_LEN, ProjectSettingsFormView, ProjectSettingsInputRow, ProjectSettingsRow, ProjectSettingsSection, ProjectSettingsValueRow } from "@aoagents/product-ui";
import { Pencil } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { ProjectSettingsSaveState, ProjectSettingsSection as SettingsSection } from "./ProjectSettingsForm";
import { deriveRepoHost, deriveRepoPath, IntakeFields, intakeNeedsRule } from "./IntakeFields";
import { ProductExternalLink } from "./ProductExternalLink";
import { AgentModelField, ProjectAgentRoleHeader, ProjectAgentRoleRow, ProjectAutoReviewToggle } from "./settings/ProjectAgentRoleControls";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { Button } from "./ui/button";

export type ProjectSettingsDraft = {
	displayName: string;
	defaultBranch: string;
	sessionPrefix: string;
	workerAgent: string;
	orchestratorAgent: string;
	reviewerHarness: string;
	workerModel: string;
	orchestratorModel: string;
	reviewerModel: string;
	workerMode: string;
	orchestratorMode: string;
	reviewerMode: string;
	workerEffort: string;
	orchestratorEffort: string;
	reviewerEffort: string;
	workerPermissions: string;
	orchestratorPermissions: string;
	reviewerPermissions: string;
	autoReview: boolean;
	intakeEnabled: boolean;
	intakeRepo: string;
	intakeAssignee: string;
};

const roles = ["worker", "orchestrator", "reviewer"] as const;
export type ProjectSettingsRole = typeof roles[number];
export type ProjectAgentPickerProps = {
	role: ProjectSettingsRole;
	draft: ProjectSettingsDraft;
	value: string;
	invalid: boolean;
	onChange: (agent: string) => void;
};
type Capabilities = {
	workflow: boolean;
	sessionPrefix: boolean;
	intake: boolean;
	reviewer: boolean;
	requiredAgents: boolean;
	nameLimit?: number;
	requiredBranch?: boolean;
	/** Cloud defaults and tuning belong to its runtime, rather than the local catalog. */
	runtimeDefaults?: boolean;
};
type RoleFields = {
	agent: "workerAgent" | "orchestratorAgent" | "reviewerHarness";
	model: "workerModel" | "orchestratorModel" | "reviewerModel";
	mode: "workerMode" | "orchestratorMode" | "reviewerMode";
	effort: "workerEffort" | "orchestratorEffort" | "reviewerEffort";
	permissions: "workerPermissions" | "orchestratorPermissions" | "reviewerPermissions";
};
const roleFields: Record<ProjectSettingsRole, RoleFields> = {
	worker: { agent: "workerAgent", model: "workerModel", mode: "workerMode", effort: "workerEffort", permissions: "workerPermissions" },
	orchestrator: { agent: "orchestratorAgent", model: "orchestratorModel", mode: "orchestratorMode", effort: "orchestratorEffort", permissions: "orchestratorPermissions" },
	reviewer: { agent: "reviewerHarness", model: "reviewerModel", mode: "reviewerMode", effort: "reviewerEffort", permissions: "reviewerPermissions" },
};

// Both persistence adapters render this page. Drafts, validation, autosave and
// common controls live here so a UI change applies to local and Cloud projects.
export function ProjectSettingsEditor({ initialValues, section, capabilities, details, workspaceRepos, repository, renderAgent, defaultReviewer = () => "", modelScope, reviewerWarning, autoReviewDescription, save, onSaveState, saveUnchanged = false }: {
	initialValues: ProjectSettingsDraft;
	section: SettingsSection;
	capabilities: Capabilities;
	details: Array<{ label: string; value: string; href?: string }>;
	workspaceRepos?: Array<{ name: string; relativePath: string; repo?: string }>;
	repository?: string;
	renderAgent: (props: ProjectAgentPickerProps) => ReactNode;
	defaultReviewer?: (draft: ProjectSettingsDraft) => string;
	modelScope: (agent: string) => string;
	reviewerWarning?: (agent: string) => string | null;
	autoReviewDescription?: string;
	save: (values: ProjectSettingsDraft) => Promise<{ values?: ProjectSettingsDraft; replacementError?: string | null }>;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
	saveUnchanged?: boolean;
}) {
	const { t } = useTranslation();
	const [draft, setDraft] = useState(initialValues);
	const saved = useRef(JSON.stringify(initialValues));
	const failedKey = useRef<string | undefined>(undefined);
	const [error, setError] = useState<string>();
	const [replacementError, setReplacementError] = useState<string>();
	const [didSave, setDidSave] = useState(false);
	const [showSaving, setShowSaving] = useState(false);
	const [validity, setValidity] = useState({ worker: true, orchestrator: true, reviewer: true });
	const mutation = useMutation({
		mutationFn: (values: ProjectSettingsDraft) => save(values),
		onSuccess: (result, values) => {
			const normalized = result.values ?? values;
			saved.current = JSON.stringify(normalized);
			failedKey.current = undefined;
			setDraft((current) => JSON.stringify(current) === JSON.stringify(values) ? normalized : current);
			setReplacementError(result.replacementError ?? undefined);
			setDidSave(true);
		},
		onError: (cause, values) => {
			failedKey.current = JSON.stringify(values);
			setError(cause instanceof Error ? cause.message : t("settings.project.saveFailed"));
		},
	});
	const dirty = JSON.stringify(draft) !== saved.current;
	const intake = { enabled: draft.intakeEnabled, repo: draft.intakeRepo, assignee: draft.intakeAssignee };
	const intakeIncomplete = capabilities.intake && intakeNeedsRule(intake);
	const visibleRoles = capabilities.reviewer ? roles : roles.slice(0, 2);
	const nameLimit = capabilities.nameLimit ?? MAX_PROJECT_DISPLAY_NAME_LEN;
	const validate = () => {
		if (capabilities.requiredAgents && (!draft.workerAgent || !draft.orchestratorAgent)) return t("settings.project.agentsRequired");
		if (!draft.displayName.trim()) return t("settings.project.nameRequired");
		if ([...draft.displayName.trim()].length > nameLimit && draft.displayName.trim() !== initialValues.displayName.trim()) return t("settings.project.nameTooLong", { max: nameLimit });
		if (capabilities.requiredBranch && (!draft.defaultBranch.trim() || [...draft.defaultBranch.trim()].length > 255)) return t("settings.cloudProject.identityValidation");
		if (visibleRoles.some((role) => !validity[role])) return t("settings.project.tuningInvalid");
		return undefined;
	};
	const submit = () => {
		if (mutation.isPending || intakeIncomplete || (!dirty && !saveUnchanged)) return;
		const problem = validate();
		setError(problem);
		if (problem) return;
		setReplacementError(undefined);
		setDidSave(false);
		mutation.mutate(draft);
	};
	useEffect(() => {
		if (!dirty || mutation.isPending || JSON.stringify(draft) === failedKey.current) return;
		const timeout = window.setTimeout(submit, 650);
		return () => window.clearTimeout(timeout);
	}, [draft, dirty, mutation.isPending, validity]);
	useEffect(() => {
		if (!mutation.isPending) { setShowSaving(false); return; }
		const timeout = window.setTimeout(() => setShowSaving(true), 200);
		return () => window.clearTimeout(timeout);
	}, [mutation.isPending]);
	useEffect(() => {
		onSaveState?.({ phase: error ? "failed" : mutation.isPending ? showSaving ? "saving" : "pending" : dirty && !intakeIncomplete ? "pending" : didSave ? "saved" : "idle", dirty: dirty && !intakeIncomplete, requestPending: mutation.isPending, error, replacementError });
	}, [dirty, didSave, error, intakeIncomplete, mutation.isPending, onSaveState, replacementError, showSaving]);
	useEffect(() => {
		if (!didSave) return;
		const timeout = window.setTimeout(() => setDidSave(false), 1800);
		return () => window.clearTimeout(timeout);
	}, [didSave]);
	const patch = (values: Partial<ProjectSettingsDraft>) => setDraft((current) => ({ ...current, ...values }));
	const warning = reviewerWarning?.(draft.reviewerHarness);
	return <ProjectSettingsFormView id="project-settings-form" className="project-settings-form gap-5" onSubmit={submit}>
		<fieldset disabled={mutation.isPending} className="flex min-w-0 flex-col gap-5">
			{section === "general" ? <>
				<ProjectSettingsSection title={t("settings.project.details")} grouped>
					<ProjectSettingsInputRow id="projectName" label={t("settings.project.name")} editLabel={t("settings.field.edit", { label: t("settings.project.name") })} editIcon={<Pencil className="settings-inline-edit-icon" aria-hidden="true" />} value={draft.displayName} onChange={(displayName) => patch({ displayName })} />
					{details.map((detail) => <ProjectSettingsValueRow key={detail.label} {...detail} externalLink={ProductExternalLink} />)}
				</ProjectSettingsSection>
				{workspaceRepos && <ProjectSettingsSection title={t("settings.project.workspaceRepos")} grouped>
					{workspaceRepos.length ? workspaceRepos.map((repo) => <ProjectSettingsRow key={repo.name} label={repo.name}><span className="settings-row-value">{repo.relativePath}{repo.repo ? ` · ${repo.repo}` : ""}</span></ProjectSettingsRow>) : <p className="px-1 text-xs text-settings-muted">{t("settings.project.childReposEmpty")}</p>}
				</ProjectSettingsSection>}
				{capabilities.workflow && <>
					<ProjectSettingsSection title={t("settings.project.worktrees")} grouped>
						<ProjectSettingsInputRow id="defaultBranch" label={t("settings.project.defaultBranch")} editLabel={t("settings.field.edit", { label: t("settings.project.defaultBranch") })} editIcon={<Pencil className="settings-inline-edit-icon" aria-hidden="true" />} value={draft.defaultBranch} placeholder={capabilities.requiredBranch ? undefined : "auto"} onChange={(defaultBranch) => patch({ defaultBranch })} />
						{capabilities.sessionPrefix && <ProjectSettingsInputRow id="sessionPrefix" label={t("settings.project.sessionPrefix")} editLabel={t("settings.field.edit", { label: t("settings.project.sessionPrefix") })} editIcon={<Pencil className="settings-inline-edit-icon" aria-hidden="true" />} value={draft.sessionPrefix} placeholder="ao" onChange={(sessionPrefix) => patch({ sessionPrefix })} />}
					</ProjectSettingsSection>
					{capabilities.intake && <ProjectSettingsSection title={t("settings.project.issues")} grouped><IntakeFields variant="settings" form={intake} onChange={(values) => patch({ intakeEnabled: values.enabled ?? draft.intakeEnabled, intakeRepo: values.repo ?? draft.intakeRepo, intakeAssignee: values.assignee ?? draft.intakeAssignee })} repoPreview={{ value: draft.intakeRepo.trim() || deriveRepoPath(repository ?? ""), host: deriveRepoHost(repository ?? "") }} /></ProjectSettingsSection>}
					<ProjectSettingsSection title={t("settings.project.pullRequests")} grouped><ProjectAutoReviewToggle description={autoReviewDescription} checked={draft.autoReview} onCheckedChange={(autoReview) => patch({ autoReview })} /></ProjectSettingsSection>
				</>}
			</> : <ProjectSettingsSection title={t("settings.project.agents")} titleHidden grouped>
				<ProjectAgentRoleHeader />
				{visibleRoles.map((role) => {
					const fields = roleFields[role];
					const selectedAgent = draft[fields.agent];
					const agent = selectedAgent || (role === "reviewer" ? defaultReviewer(draft) : "");
					const updateConfig = (key: RoleFields["model"] | RoleFields["mode"] | RoleFields["effort"] | RoleFields["permissions"], value: string) => patch({ [key]: value, ...(role === "reviewer" && !selectedAgent && agent ? { reviewerHarness: agent } : {}) });
					return <ProjectAgentRoleRow key={role} label={t(`settings.models.${role}Role`)}
						agent={renderAgent({ role, draft, value: selectedAgent, invalid: error !== undefined && !selectedAgent, onChange: (value) => setDraft((current) => ({ ...current, [fields.agent]: value, ...(value !== current[fields.agent] ? { [fields.model]: "", [fields.mode]: "", [fields.effort]: "", ...(role === "reviewer" || capabilities.runtimeDefaults ? { [fields.permissions]: "" } : {}) } : {}) })) })}
						model={<div className="space-y-1.5"><AgentModelField role={role} agentId={agent} projectId={modelScope(agent)} model={draft[fields.model]} mode={draft[fields.mode]} effort={draft[fields.effort]}
							allowCustomFallback={capabilities.runtimeDefaults} followCatalogDefaults={!capabilities.runtimeDefaults}
							supportedEfforts={capabilities.runtimeDefaults ? agent === "codex" ? ["low", "medium", "high", "xhigh", "max"] : agent === "claude-code" ? ["low", "medium", "high", "max"] : undefined : undefined}
							emptyLabel={capabilities.runtimeDefaults ? t(agent === "" ? "settings.cloudProject.sessionModel" : "settings.cloudProject.agentDefault") : undefined}
							independentMode={capabilities.runtimeDefaults && agent === "cursor"}
							onModelChange={(value) => updateConfig(fields.model, value)} onModeChange={(value) => updateConfig(fields.mode, value)} onEffortChange={(value) => updateConfig(fields.effort, value)} onValidityChange={(valid) => setValidity((current) => current[role] === valid ? current : { ...current, [role]: valid })} />
							{capabilities.runtimeDefaults && agent === "cursor" && <SettingsOptionMenu aria-label={t(`settings.models.${role}Mode`)} value={draft[fields.mode]} triggerClassName="w-full justify-between" options={[{ value: "", label: t("settings.cloudProject.agentDefault") }, { value: "plan", label: t("settings.cloudProject.plan") }, { value: "ask", label: t("settings.cloudProject.ask") }]} onChange={(value) => updateConfig(fields.mode, value)} />}
						</div>} />;
				})}
				<div className={capabilities.reviewer ? "grid grid-cols-3 gap-3 border-t border-border/60 pt-4" : "grid grid-cols-2 gap-3 border-t border-border/60 pt-4"}>
					{visibleRoles.map((role) => {
						const fields = roleFields[role];
						const agent = draft[fields.agent] || (role === "reviewer" ? defaultReviewer(draft) : "");
						return <ProjectRolePermissions key={role} role={role} agent={agent} value={draft[fields.permissions]} runtimeDefaults={capabilities.runtimeDefaults} onChange={(value) => patch({ [fields.permissions]: value, ...(role === "reviewer" && !draft.reviewerHarness && agent ? { reviewerHarness: agent } : {}) })} />;
					})}
				</div>
				{capabilities.requiredAgents && (!draft.workerAgent || !draft.orchestratorAgent) && <p className="px-3 pb-2 text-xs text-error" role="alert">{t("settings.project.agentsRequired")}</p>}
				{warning && <p className="px-3 pb-2 text-xs text-warning" role="status">{warning}</p>}
			</ProjectSettingsSection>}
		</fieldset>
		{error && !onSaveState && <p role="alert" className="text-sm text-error">{error}</p>}
		{!onSaveState && <div className="flex justify-end"><Button type="submit" disabled={!dirty || mutation.isPending}>{mutation.isPending ? t("settings.project.saving") : t("files.saveFile")}</Button></div>}
	</ProjectSettingsFormView>;
}

function ProjectRolePermissions({ role, agent, value, runtimeDefaults, onChange }: { role: ProjectSettingsRole; agent: string; value: string; runtimeDefaults?: boolean; onChange: (value: string) => void }) {
	const { t } = useTranslation();
	const label = t("settings.project.roleApproval", { role: t(`settings.models.${role}Role`) });
	const values = runtimeDefaults ? ["", "default", "auto", ...(agent === "opencode" ? [] : ["accept-edits"]), "bypass-permissions"] : [...(agent === "codex" ? [] : ["default"]), "auto", "accept-edits", "bypass-permissions"];
	const options = values.map((permission) => ({ value: permission, label: permission === "" ? t("settings.cloudProject.sessionPolicy") : permission === "default" ? t(runtimeDefaults ? "settings.cloudProject.agentDefault" : agent === "claude-code" ? "settings.project.permissionUseClaude" : "settings.project.permissionUseAgent") : t(permission === "accept-edits" ? "settings.project.permissionAcceptEdits" : permission === "auto" ? "settings.project.permissionAuto" : "settings.project.permissionBypass") }));
	return <div className="min-w-0 space-y-1.5"><span className="text-xs text-settings-muted">{label}</span><SettingsOptionMenu aria-label={label} value={runtimeDefaults ? value : value === "default" && agent === "codex" ? "bypass-permissions" : value || "auto"} options={options} disabled={runtimeDefaults && !agent} placeholder={t("settings.project.permissionNotReported")} triggerClassName="w-full justify-between" onChange={onChange} /></div>;
}
