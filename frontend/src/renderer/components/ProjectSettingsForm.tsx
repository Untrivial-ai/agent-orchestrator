import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	MAX_PROJECT_DISPLAY_NAME_LEN,
	ProjectGeneralSettingsView,
	ProjectSettingsFormView,
	ProjectSettingsSection,
	ProjectWorkflowSettingsView,
	validateProjectSettings,
} from "@aoagents/product-ui";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Info, Pencil } from "lucide-react";
import type { components } from "../../api/schema";
import { agentModelsQueryKey, agentModelsQueryOptions, refreshAgentModels, revalidateAgentModels, type AgentModelCatalog } from "../hooks/useAgentModelsQuery";
import { useAgentReadinessQuery, useEnsureAgentReadiness } from "../hooks/useAgentReadinessQuery";
import { useWorkspaceQuery, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { isConcreteModelID, modelChoiceLabel } from "../lib/agent-model-choices";
import { WORKER_DEFAULT_REVIEWERS } from "../lib/reviewer-harnesses";
import { captureOrchestratorReplacementFailure } from "../lib/orchestrator-replacement-telemetry";
import { OrchestratorSpawnError, spawnOrchestrator } from "../lib/spawn-orchestrator";
import { captureRendererEvent } from "../lib/telemetry";
import { type OrchestratorReplacementFailure, useUiStore } from "../stores/ui-store";
import { newestActiveOrchestrator } from "../types/workspace";
import { RequiredAgentField } from "./CreateProjectAgentSheet";
import { buildIntake, deriveRepoPath, deriveRepoHost, IntakeFields, intakeNeedsRule, type IntakeForm } from "./IntakeFields";
import { ProductExternalLink } from "./ProductExternalLink";
import { ReviewerSelect, reviewerTrustWarning } from "./ReviewerSelect";
import { AgentModelCombobox } from "./settings/AgentModelCombobox";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { Switch } from "./ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type Project = components["schemas"]["Project"];
type ProjectConfig = components["schemas"]["ProjectConfig"];
type TrackerIntakeConfig = components["schemas"]["TrackerIntakeConfig"];

const PERMISSION_MODE_VALUES = ["auto", "accept-edits", "bypass-permissions"] as const;
const DEFAULT_BRANCH_AUTO = "auto";

const projectQueryKey = (id: string) => ["project", id] as const;

type SettingsSaveResult = {
	savedKey: string;
	replacementError: string | null;
	replacementSessionId: string | null;
	replacementFailure: OrchestratorReplacementFailure | null;
	spawnError: unknown;
};

export type ProjectSettingsSection = "general" | "agents";
export type ProjectSettingsSaveState = {
	phase: "idle" | "pending" | "saving" | "saved" | "failed";
	error?: string;
	replacementError?: string;
};

export function ProjectSettingsForm({
	projectId,
	section = "general",
	onSaveState,
}: {
	projectId: string;
	section?: ProjectSettingsSection;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();

	const query = useQuery({
		queryKey: projectQueryKey(projectId),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			if (data?.status !== "ok") throw new Error(t("settings.project.degraded"));
			return data.project as Project;
		},
	});

	return (
		<>
			{query.isLoading ? (
				<p className="text-sm text-settings-muted">{t("settings.project.loading")}</p>
			) : query.isError || !query.data ? (
				<p className="text-sm text-error">{query.error instanceof Error ? query.error.message : t("settings.project.loadFailed")}</p>
			) : (
				<SettingsBody
					key={projectId}
					project={query.data}
					onSaved={() =>
						queryClient.invalidateQueries({ queryKey: workspaceQueryKey }).catch(() => {
							// Saving succeeds even if the cache refresh fails.
						})
					}
					projectId={projectId}
					section={section}
					onSaveState={onSaveState}
				/>
			)}
		</>
	);
}

function SettingsBody({
	project,
	projectId,
	onSaved,
	section = "general",
	onSaveState,
}: {
	project: Project;
	projectId: string;
	onSaved: () => Promise<void>;
	section?: ProjectSettingsSection;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const setOrchestratorReplacementError = useUiStore((state) => state.setOrchestratorReplacementError);
	const workspaceQuery = useWorkspaceQuery();
	const config = project.config ?? {};
	const isScratchProject = project.kind === "scratch";
	const workspace = workspaceQuery.data?.find((item) => item.id === projectId);
	const activeOrchestrator = newestActiveOrchestrator(workspace?.sessions ?? []);
	const intake: TrackerIntakeConfig = config.trackerIntake ?? {};
	const [form, setForm] = useState({
		displayName: project.name,
		defaultBranch: config.defaultBranch ?? DEFAULT_BRANCH_AUTO,
		sessionPrefix: config.sessionPrefix ?? "",
		workerAgent: config.worker?.agent ?? "",
		orchestratorAgent: config.orchestrator?.agent ?? "",
		workerModel: config.worker?.agentConfig?.model ?? config.agentConfig?.model ?? "",
		workerEffort: config.worker?.agentConfig?.effort ?? config.agentConfig?.effort ?? "",
		workerPermissions: config.worker?.agentConfig?.permissions ?? config.agentConfig?.permissions ?? "",
		orchestratorModel: config.orchestrator?.agentConfig?.model ?? config.agentConfig?.model ?? "",
		orchestratorEffort: config.orchestrator?.agentConfig?.effort ?? config.agentConfig?.effort ?? "",
		orchestratorPermissions: config.orchestrator?.agentConfig?.permissions ?? config.agentConfig?.permissions ?? "",
		workerMode: config.worker?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		orchestratorMode: config.orchestrator?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		reviewerHarness: config.reviewers?.[0]?.harness ?? "",
		reviewerModel: config.reviewers?.[0]?.agentConfig?.model ?? config.agentConfig?.model ?? "",
		reviewerMode: config.reviewers?.[0]?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		reviewerEffort: config.reviewers?.[0]?.agentConfig?.effort ?? config.agentConfig?.effort ?? "",
		reviewerPermissions: config.reviewers?.[0]?.agentConfig?.permissions ?? config.agentConfig?.permissions ?? "",
		autoReview: config.autoReview ?? false,
		intakeEnabled: intake.enabled ?? false,
		intakeRepo: intake.repo ?? "",
		intakeAssignee: intake.assignee ?? "",
	});
	const lastSavedRef = useRef(JSON.stringify(form));
	const failedKeyRef = useRef<string | null>(null);
	const lastOrchestratorRef = useRef(config.orchestrator?.agent ?? "");
	const replacementAttemptedRef = useRef(false);
	const [savedAt, setSavedAt] = useState<number | null>(null);
	const [showSaving, setShowSaving] = useState(false);
	const [replacementError, setReplacementError] = useState<string | null>(null);
	const [validationError, setValidationError] = useState<string | null>(null);
	const [tuningValidity, setTuningValidity] = useState({
		worker: true,
		orchestrator: true,
		reviewer: true,
	});
	const missingRequiredAgent = form.workerAgent === "" || form.orchestratorAgent === "";
	const agentsQuery = useAgentReadinessQuery();
	useEnsureAgentReadiness();
	useEnsureAgentReadiness({
		agentIds: [form.workerAgent, form.orchestratorAgent, form.reviewerHarness],
		enabled: form.workerAgent !== "" || form.orchestratorAgent !== "" || form.reviewerHarness !== "",
	});
	const agentCatalog = agentsQuery.data;

	const intakeForm: IntakeForm = {
		enabled: form.intakeEnabled,
		repo: form.intakeRepo,
		assignee: form.intakeAssignee,
	};
	const patchIntake = (patch: Partial<IntakeForm>) =>
		setForm((f) => ({
			...f,
			intakeEnabled: patch.enabled ?? f.intakeEnabled,
			intakeRepo: patch.repo ?? f.intakeRepo,
			intakeAssignee: patch.assignee ?? f.intakeAssignee,
		}));
	const effectiveIntakeRepo = form.intakeRepo.trim() || deriveRepoPath(project.repo);
	const intakeSetupIncomplete = !isScratchProject && intakeNeedsRule(intakeForm);
	const reviewerWarning = reviewerTrustWarning(form.reviewerHarness);
	const defaultReviewerHarness = WORKER_DEFAULT_REVIEWERS[form.workerAgent] ?? "claude-code";
	const mutation = useMutation({
		mutationFn: async (values: typeof form) => {
			const savedKey = JSON.stringify(values);
			void captureRendererEvent("ao.renderer.settings_save_requested", {
				project_id: projectId,
			});
			const displayName = values.displayName.trim();
			const { model: _legacyModel, mode: _legacyMode, effort: _legacyEffort, permissions: _legacyPermissions, ...sharedAgentConfig } = config.agentConfig ?? {};
			const existingReviewer = config.reviewers?.[0];
			const existingReviewerAgentConfig = existingReviewer?.harness === values.reviewerHarness ? existingReviewer.agentConfig : undefined;
			const next: ProjectConfig = isScratchProject
				? {
						...scratchSupportedConfig(config),
						worker: {
							...config.worker,
							agent: values.workerAgent,
							agentConfig: buildRoleAgentConfig(config.worker?.agentConfig, values.workerModel, values.workerMode, values.workerEffort, values.workerPermissions),
						},
						orchestrator: {
							...config.orchestrator,
							agent: values.orchestratorAgent,
							agentConfig: buildRoleAgentConfig(
								config.orchestrator?.agentConfig,
								values.orchestratorModel,
								values.orchestratorMode,
								values.orchestratorEffort,
								values.orchestratorPermissions,
							),
						},
						agentConfig: blankToUndefined({
							...sharedAgentConfig,
							permissions: undefined,
						}),
					}
				: {
						...config,
						defaultBranch: values.defaultBranch.trim() === DEFAULT_BRANCH_AUTO ? undefined : values.defaultBranch || undefined,
						sessionPrefix: values.sessionPrefix || undefined,
						worker: {
							...config.worker,
							agent: values.workerAgent,
							agentConfig: buildRoleAgentConfig(config.worker?.agentConfig, values.workerModel, values.workerMode, values.workerEffort, values.workerPermissions),
						},
						orchestrator: {
							...config.orchestrator,
							agent: values.orchestratorAgent,
							agentConfig: buildRoleAgentConfig(
								config.orchestrator?.agentConfig,
								values.orchestratorModel,
								values.orchestratorMode,
								values.orchestratorEffort,
								values.orchestratorPermissions,
							),
						},
						agentConfig: blankToUndefined({
							...sharedAgentConfig,
							permissions: undefined,
						}),
						reviewers: values.reviewerHarness
							? [
									{
										harness: values.reviewerHarness,
										agentConfig: buildRoleAgentConfig(
											existingReviewerAgentConfig,
											values.reviewerModel,
											values.reviewerMode,
											values.reviewerEffort,
											values.reviewerPermissions,
										),
									},
								]
							: undefined,
						trackerIntake: buildIntake(
							{
								enabled: values.intakeEnabled,
								repo: values.intakeRepo,
								assignee: values.intakeAssignee,
							},
							config.trackerIntake,
						),
						autoReview: values.autoReview,
					};
			const { error } = await apiClient.PUT("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
				body: { displayName, config: next },
			});
			if (error) throw new Error(apiErrorMessage(error));
			const replaceOrchestrator = values.orchestratorAgent !== lastOrchestratorRef.current ||
				(Boolean(activeOrchestrator && activeOrchestrator.provider !== values.orchestratorAgent) && !replacementAttemptedRef.current);
			lastOrchestratorRef.current = values.orchestratorAgent;
			if (replaceOrchestrator) {
				replacementAttemptedRef.current = true;
				try {
					const sessionId = await spawnOrchestrator(projectId, "settings", true);
					return {
						replacementError: null,
						replacementSessionId: sessionId,
						replacementFailure: null,
						spawnError: null,
						savedKey,
					} satisfies SettingsSaveResult;
				} catch (error) {
					const replacementFailure: OrchestratorReplacementFailure = {
						message: error instanceof Error ? error.message : t("settings.project.replaceOrchestratorFailed"),
						...(error instanceof OrchestratorSpawnError
							? {
									code: error.code,
									requestId: error.requestId,
									details: error.details,
								}
							: {}),
					};
					return {
						replacementError: replacementFailure.message,
						replacementSessionId: null,
						replacementFailure,
						spawnError: error,
						savedKey,
					} satisfies SettingsSaveResult;
				}
			}
			return {
				replacementError: null,
				replacementSessionId: null,
				replacementFailure: null,
				spawnError: null,
				savedKey,
			} satisfies SettingsSaveResult;
		},
		onSuccess: (result) => {
			lastSavedRef.current = result.savedKey;
			failedKeyRef.current = null;
			void captureRendererEvent("ao.renderer.settings_save_succeeded", {
				project_id: projectId,
			});
			setSavedAt(Date.now());
			setReplacementError(result.replacementError);
			setValidationError(null);
			void queryClient.invalidateQueries({ queryKey: ["project", projectId] });
			void onSaved();
			if (result.replacementFailure) {
				setOrchestratorReplacementError(projectId, result.replacementFailure);
				if (result.spawnError) captureOrchestratorReplacementFailure(result.spawnError, projectId);
			}
		},
		onError: (_error, values) => {
			failedKeyRef.current = JSON.stringify(values);
			void captureRendererEvent("ao.renderer.settings_save_failed", {
				project_id: projectId,
			});
		},
	});

	useEffect(() => {
		if (!mutation.isPending) {
			setShowSaving(false);
			return;
		}
		const timeout = window.setTimeout(() => setShowSaving(true), 200);
		return () => window.clearTimeout(timeout);
	}, [mutation.isPending]);

	useEffect(() => {
		const key = JSON.stringify(form);
		if (key === lastSavedRef.current || key === failedKeyRef.current || mutation.isPending) return;
		const timeout = window.setTimeout(() => {
			const validation = validateProjectSettings(form, {
				validateIntake: !isScratchProject,
				originalDisplayName: project.name,
			});
			if (validation === "intake_assignee_required") {
				setValidationError(null);
				return;
			}
			if (validation || !tuningValidity.worker || !tuningValidity.orchestrator || !tuningValidity.reviewer) {
				setValidationError(
					validation === "agents_required"
						? t("settings.project.agentsRequired")
						: validation === "name_required"
							? t("settings.project.nameRequired")
							: validation === "name_too_long"
								? t("settings.project.nameTooLong", {
										max: MAX_PROJECT_DISPLAY_NAME_LEN,
									})
								: t("settings.project.tuningInvalid"),
				);
				return;
			}
			setValidationError(null);
			setSavedAt(null);
			mutation.mutate(form);
		}, 650);
		return () => window.clearTimeout(timeout);
	}, [form, isScratchProject, mutation.isPending, project.name, t, tuningValidity]);

	useEffect(() => {
		const mutationError = mutation.isError ? (mutation.error instanceof Error ? mutation.error.message : t("settings.project.saveFailed")) : undefined;
		const hasUnsavedChanges = JSON.stringify(form) !== lastSavedRef.current;
		onSaveState?.({
			phase:
				validationError || mutationError
					? "failed"
					: mutation.isPending
						? showSaving
							? "saving"
							: "pending"
						: hasUnsavedChanges && !intakeSetupIncomplete
							? "pending"
							: savedAt !== null
								? "saved"
								: "idle",
			error: validationError ?? mutationError,
			replacementError: !mutation.isPending && !mutation.isError ? (replacementError ?? undefined) : undefined,
		});
	}, [
		form,
		intakeSetupIncomplete,
		mutation.error,
		mutation.isError,
		mutation.isPending,
		onSaveState,
		replacementError,
		savedAt,
		showSaving,
		t,
		validationError,
	]);

	useEffect(() => {
		if (savedAt === null) return;
		const timeout = window.setTimeout(() => setSavedAt(null), 1800);
		return () => window.clearTimeout(timeout);
	}, [savedAt]);

	return (
		<ProjectSettingsFormView
			id="project-settings-form"
			className="project-settings-form gap-5"
			onSubmit={() => {
				setSavedAt(null);
				setReplacementError(null);
				const validation = validateProjectSettings(form, {
					validateIntake: !isScratchProject,
					originalDisplayName: project.name,
				});
				if (validation === "intake_assignee_required") {
					return;
				}
				if (validation) {
					setValidationError(
						validation === "agents_required"
							? t("settings.project.agentsRequired")
							: validation === "name_required"
								? t("settings.project.nameRequired")
								: validation === "name_too_long"
									? t("settings.project.nameTooLong", {
											max: MAX_PROJECT_DISPLAY_NAME_LEN,
										})
									: t("settings.project.intakeAssigneeRequired"),
					);
					return;
				}
				if (!tuningValidity.worker || !tuningValidity.orchestrator || !tuningValidity.reviewer) {
					setValidationError(t("settings.project.tuningInvalid"));
					return;
				}
				setValidationError(null);
				mutation.mutate(form);
			}}
		>
			{section === "general" && (
				<>
					<ProjectGeneralSettingsView
						displayName={form.displayName}
						showTitle
						externalLink={ProductExternalLink}
						icons={{
							edit: <Pencil className="settings-inline-edit-icon" aria-hidden="true" />,
						}}
						onDisplayNameChange={(displayName) => setForm((f) => ({ ...f, displayName }))}
						labels={{
							title: t("settings.project.details"),
							name: t("settings.project.name"),
							id: t("settings.project.id"),
							kind: t("settings.project.kind"),
							path: t("settings.project.path"),
							repo: t("settings.project.repo"),
							workspaceRepos: t("settings.project.workspaceRepos"),
							workspaceReposEmpty: t("settings.project.childReposEmpty"),
							editName: t("settings.field.edit", {
								label: t("settings.project.name"),
							}),
						}}
						project={{
							id: project.id,
							kindLabel: projectKindLabel(project.kind, t),
							path: project.path,
							pathHref: `file://${encodeURI(project.path)}`,
							repo: project.repo,
							repoHref: project.repo ? repositoryHref(project.repo) : undefined,
							workspaceRepos: project.kind === "workspace" ? (project.workspaceRepos ?? []) : undefined,
						}}
					/>
					{!isScratchProject && (
						<>
							<ProjectWorkflowSettingsView
								branch={form.defaultBranch}
								icons={{
									edit: <Pencil className="settings-inline-edit-icon" aria-hidden="true" />,
								}}
								prefix={form.sessionPrefix}
								onBranchChange={(defaultBranch) => setForm((f) => ({ ...f, defaultBranch }))}
								onPrefixChange={(sessionPrefix) => setForm((f) => ({ ...f, sessionPrefix }))}
								labels={{
									worktrees: t("settings.project.worktrees"),
									defaultBranch: t("settings.project.defaultBranch"),
									sessionPrefix: t("settings.project.sessionPrefix"),
									reviewers: t("settings.project.reviewers"),
									defaultReviewer: t("settings.project.defaultReviewer"),
									editDefaultBranch: t("settings.field.edit", {
										label: t("settings.project.defaultBranch"),
									}),
									editSessionPrefix: t("settings.field.edit", {
										label: t("settings.project.sessionPrefix"),
									}),
								}}
							/>
							<ProjectSettingsSection title={t("settings.project.issues")} grouped>
								<IntakeFields
									variant="settings"
									form={intakeForm}
									onChange={patchIntake}
									repoPreview={{
										value: effectiveIntakeRepo,
										host: deriveRepoHost(project.repo),
									}}
								/>
							</ProjectSettingsSection>
							<ProjectSettingsSection title={t("settings.project.pullRequests")} grouped>
								<div className="settings-row-bar">
									<div className="flex shrink-0 items-center gap-1.5">
										<span className="whitespace-nowrap text-sm leading-5 text-settings-label">{t("settings.project.autoReviewToggle")}</span>
										<Tooltip>
											<TooltipTrigger asChild>
												<button
													type="button"
													className="inline-flex size-5 items-center justify-center rounded-md text-settings-muted transition-colors hover:bg-settings-menu-selected hover:text-settings-label focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none"
													aria-label={t("settings.project.autoReviewDescription")}
												>
													<Info className="size-icon-sm" aria-hidden="true" />
												</button>
											</TooltipTrigger>
											<TooltipContent className="max-w-72 leading-normal" side="top">
												{t("settings.project.autoReviewDescription")}
											</TooltipContent>
										</Tooltip>
									</div>
									<div className="flex min-w-0 flex-1 items-center justify-end">
										<Switch
											aria-label={t("settings.project.autoReviewToggle")}
											checked={form.autoReview}
											id="project-auto-review"
											onCheckedChange={(checked) => setForm((f) => ({ ...f, autoReview: checked }))}
										/>
									</div>
								</div>
							</ProjectSettingsSection>
						</>
					)}
				</>
			)}

			{section === "agents" && (
				<ProjectSettingsSection title={t("settings.project.agents")} titleHidden grouped>
					<div className="grid grid-cols-[6rem_minmax(0,0.85fr)_minmax(0,1.25fr)] gap-3 py-2 text-xs font-medium text-settings-muted">
						<span />
						<span>{t("settings.project.agent")}</span>
						<span>{t("settings.project.modelOverride")}</span>
					</div>
					<ProjectAgentRoleRow
						label={t("settings.models.workerRole")}
						agent={
							<RequiredAgentField
								id="workerAgent"
								variant="settings-control"
								value={form.workerAgent}
								placeholder={t("settings.project.selectWorker")}
								label={t("settings.project.defaultWorker")}
								agents={agentCatalog?.agents}
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
								invalid={validationError !== null && form.workerAgent === ""}
								onChange={(workerAgent) =>
									setForm((f) => ({
										...f,
										workerAgent,
										workerModel: "",
										workerMode: "",
										workerEffort: "",
									}))
								}
							/>
						}
						model={
							<AgentModelField
								role="worker"
								agentId={form.workerAgent}
								projectId={projectId}
								model={form.workerModel}
								mode={form.workerMode}
								effort={form.workerEffort}
								onModelChange={(workerModel) => setForm((f) => ({ ...f, workerModel }))}
								onModeChange={(workerMode) => setForm((f) => ({ ...f, workerMode }))}
								onEffortChange={(workerEffort) => setForm((f) => ({ ...f, workerEffort }))}
								onValidityChange={(valid) => setTuningValidity((value) => ({ ...value, worker: valid }))}
							/>
						}
					/>
					<ProjectAgentRoleRow
						label={t("settings.models.orchestratorRole")}
						agent={
							<RequiredAgentField
								id="orchestratorAgent"
								variant="settings-control"
								value={form.orchestratorAgent}
								placeholder={t("settings.project.selectOrchestrator")}
								label={t("settings.project.defaultOrchestrator")}
								agents={agentCatalog?.agents}
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
								invalid={validationError !== null && form.orchestratorAgent === ""}
								onChange={(orchestratorAgent) =>
									setForm((f) => ({
										...f,
										orchestratorAgent,
										orchestratorModel: "",
										orchestratorMode: "",
										orchestratorEffort: "",
									}))
								}
							/>
						}
						model={
							<AgentModelField
								role="orchestrator"
								agentId={form.orchestratorAgent}
								projectId={projectId}
								model={form.orchestratorModel}
								mode={form.orchestratorMode}
								effort={form.orchestratorEffort}
								onModelChange={(orchestratorModel) => setForm((f) => ({ ...f, orchestratorModel }))}
								onModeChange={(orchestratorMode) => setForm((f) => ({ ...f, orchestratorMode }))}
								onEffortChange={(orchestratorEffort) => setForm((f) => ({ ...f, orchestratorEffort }))}
								onValidityChange={(valid) =>
									setTuningValidity((value) => ({
										...value,
										orchestrator: valid,
									}))
								}
							/>
						}
					/>
					{!isScratchProject && (
						<ProjectAgentRoleRow
							label={t("settings.models.reviewerRole")}
							agent={
								<ReviewerSelect
									value={form.reviewerHarness}
									model={form.reviewerModel}
									mode={form.reviewerMode}
									projectId={projectId}
									harnessOnly
									defaultHarness={defaultReviewerHarness}
									triggerClassName="w-full"
									onChange={(reviewerHarness) =>
										setForm((f) => ({
											...f,
											reviewerHarness,
											...(reviewerHarness !== f.reviewerHarness
											? {
													reviewerModel: "",
													reviewerMode: "",
													reviewerEffort: "",
													reviewerPermissions: "",
													}
												: {}),
										}))
									}
									ariaLabel={t("settings.project.defaultReviewer")}
									agents={agentCatalog?.agents}
									disabled={agentsQuery.isFetching && agentCatalog === undefined}
								/>
							}
							model={
								<AgentModelField
									role="reviewer"
									agentId={form.reviewerHarness || defaultReviewerHarness}
									projectId={projectId}
									model={form.reviewerModel}
									mode={form.reviewerMode}
									effort={form.reviewerEffort}
									onModelChange={(reviewerModel) => setForm((f) => ({ ...f, reviewerHarness: f.reviewerHarness || defaultReviewerHarness, reviewerModel }))}
									onModeChange={(reviewerMode) => setForm((f) => ({ ...f, reviewerHarness: f.reviewerHarness || defaultReviewerHarness, reviewerMode }))}
									onEffortChange={(reviewerEffort) => setForm((f) => ({ ...f, reviewerHarness: f.reviewerHarness || defaultReviewerHarness, reviewerEffort }))}
									onValidityChange={(valid) =>
										setTuningValidity((value) => ({
											...value,
											reviewer: valid,
										}))
									}
								/>
							}
						/>
					)}
					<div className={isScratchProject ? "grid grid-cols-2 gap-3 border-t border-border/60 pt-4" : "grid grid-cols-3 gap-3 border-t border-border/60 pt-4"}>
						<div className="min-w-0 space-y-1.5">
							<span className="text-xs text-settings-muted">{t("settings.project.roleApproval", { role: t("settings.models.workerRole") })}</span>
							<PermissionModeSelect ariaLabel={t("settings.project.roleApproval", { role: t("settings.models.workerRole") })} value={form.workerPermissions} agentId={form.workerAgent} onChange={(workerPermissions) => setForm((f) => ({ ...f, workerPermissions }))} />
						</div>
						<div className="min-w-0 space-y-1.5">
							<span className="text-xs text-settings-muted">{t("settings.project.roleApproval", { role: t("settings.models.orchestratorRole") })}</span>
							<PermissionModeSelect ariaLabel={t("settings.project.roleApproval", { role: t("settings.models.orchestratorRole") })} value={form.orchestratorPermissions} agentId={form.orchestratorAgent} onChange={(orchestratorPermissions) => setForm((f) => ({ ...f, orchestratorPermissions }))} />
						</div>
						{!isScratchProject && (
							<div className="min-w-0 space-y-1.5">
								<span className="text-xs text-settings-muted">{t("settings.project.roleApproval", { role: t("settings.models.reviewerRole") })}</span>
								<PermissionModeSelect ariaLabel={t("settings.project.roleApproval", { role: t("settings.models.reviewerRole") })} value={form.reviewerPermissions} agentId={form.reviewerHarness || defaultReviewerHarness} onChange={(reviewerPermissions) => setForm((f) => ({ ...f, reviewerHarness: f.reviewerHarness || defaultReviewerHarness, reviewerPermissions }))} />
							</div>
						)}
					</div>
					{missingRequiredAgent && (
						<p className="px-3 pb-2 text-xs text-error" role="alert">
							{t("settings.project.agentsRequired")}
						</p>
					)}
					{reviewerWarning && (
						<p className="px-3 pb-2 text-xs text-warning" role="status">
							{reviewerWarning}
						</p>
					)}
				</ProjectSettingsSection>
			)}
		</ProjectSettingsFormView>
	);
}

function AgentModelField({
	role,
	agentId,
	projectId,
	model,
	mode,
	effort,
	onModelChange,
	onModeChange,
	onEffortChange,
	onValidityChange,
}: {
	role: "worker" | "orchestrator" | "reviewer";
	agentId: string;
	projectId: string;
	model: string;
	mode: string;
	effort: string;
	onModelChange: (value: string) => void;
	onModeChange: (value: string) => void;
	onEffortChange: (value: string) => void;
	onValidityChange: (valid: boolean) => void;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const query = useQuery(agentModelsQueryOptions(agentId, projectId));
	const catalog: AgentModelCatalog | undefined = query.data;
	const revalidationQuery = useQuery({
		queryKey: ["agent-model-revalidation", agentId, projectId, catalog?.validatedAt ?? ""],
		queryFn: () => revalidateAgentModels(agentId, projectId),
		enabled: agentId !== "" && catalog?.refreshRecommended === true,
		staleTime: Number.POSITIVE_INFINITY,
		retry: false,
	});
	useEffect(() => {
		if (revalidationQuery.data) {
			queryClient.setQueryData(agentModelsQueryKey(agentId, projectId), revalidationQuery.data);
		}
	}, [agentId, projectId, queryClient, revalidationQuery.data]);
	const isMode = catalog?.selectionMode === "mode";
	const label = t(`settings.models.${role}${isMode ? "Mode" : "Model"}`);
	const warning =
		(revalidationQuery.isError ? (revalidationQuery.error instanceof Error ? revalidationQuery.error.message : t("settings.models.validateFailed")) : undefined) ??
		catalog?.warning ??
		(query.isError ? (query.error instanceof Error ? query.error.message : t("settings.models.loadFailed")) : undefined);

	if (agentId !== "" && query.isFetching && catalog === undefined) {
		return (
			<div className="min-w-0">
				<span className="text-xs text-settings-muted" role="status" aria-label={t("settings.models.loading")}>
					{t("settings.models.loading")}
				</span>
			</div>
		);
	}

	if (isMode) {
		const defaultMode = catalog.models?.find((item) => item.isDefault && isConcreteModelID(item.id))?.id;
		const selectedMode = isConcreteModelID(mode) ? mode : "";
		const options = (catalog.models ?? []).filter((item) => isConcreteModelID(item.id)).map((item) => ({
			value: item.id,
			label: modelChoiceLabel(item),
		}));
		return (
			<>
				<div className="min-w-0">
					<div className="flex min-w-0 items-center gap-2">
						<SettingsOptionMenu
							aria-label={label}
							value={selectedMode || defaultMode || ""}
							options={options}
							placeholder={t("settings.models.modeNotReported")}
							action={selectedMode && !defaultMode ? { label: t("settings.models.useAgentMode"), onSelect: () => onModeChange("") } : undefined}
							triggerClassName="w-full justify-between"
							disabled={options.length === 0 && !(selectedMode && !defaultMode)}
							onChange={(value) => {
								onModeChange(value === defaultMode ? "" : value);
								onModelChange("");
							}}
						/>
					</div>
				</div>
				{warning && <p className="px-1 text-xs leading-row text-warning">{warning}</p>}
			</>
		);
	}

	const customModelEntry = catalog?.customModelEntry ?? (catalog?.allowCustom ? "direct" : "none");
	const refreshCatalog = async () => {
		const refreshed = await refreshAgentModels(agentId, projectId);
		queryClient.setQueryData(agentModelsQueryKey(agentId, projectId), refreshed);
	};
	const selectCatalogModel = (value: string) => {
		onModelChange(value);
		onModeChange("");
	};
	const selectCustomModel = (value: string) => {
		onModelChange(value);
		onModeChange("");
	};
	return (
		<>
			<div className="min-w-0">
				<div className="flex min-w-0 items-center gap-2">
					<AgentModelCombobox
						aria-label={label}
						value={model}
						models={catalog?.models ?? []}
						allowCustom={catalog?.allowCustom}
						customModelEntry={customModelEntry}
						agentLabel={agentId}
						onRefresh={refreshCatalog}
						refreshing={catalog?.refreshState === "queued" || catalog?.refreshState === "refreshing"}
						lastSuccessAt={catalog?.lastSuccessAt}
						refreshError={catalog?.refreshError}
						retryAt={catalog?.retryAt}
						disabled={(query.isFetching && !catalog) || agentId === ""}
						onChange={selectCatalogModel}
						onCustom={selectCustomModel}
						triggerClassName="w-full justify-between"
						compact={agentId === "codex"}
						tuning={{
							effort,
							onEffortChange,
							onValidityChange,
							roleLabel: t(`settings.models.${role}Role`),
						}}
					/>
				</div>
			</div>
			{warning && <p className="px-1 text-xs leading-row text-warning">{warning}</p>}
		</>
	);
}

function ProjectAgentRoleRow({ label, agent, model }: { label: string; agent: ReactNode; model: ReactNode }) {
	return (
		<div className="grid min-h-16 grid-cols-[6rem_minmax(0,0.85fr)_minmax(0,1.25fr)] items-center gap-3 py-2">
			<span className="text-sm font-medium text-settings-label">{label}</span>
			<div className="min-w-0">{agent}</div>
			<div className="min-w-0">{model}</div>
		</div>
	);
}

function PermissionModeSelect({ ariaLabel, value, agentId, onChange }: { ariaLabel: string; value: string; agentId: string; onChange: (value: string) => void }) {
	const { t } = useTranslation();
	const options: { value: string; label: string }[] = PERMISSION_MODE_VALUES.map((permission) => ({
		value: permission,
		label: permission === "accept-edits" ? t("settings.project.permissionAcceptEdits") : permission === "auto" ? t("settings.project.permissionAuto") : t("settings.project.permissionBypass"),
	}));
	if (agentId !== "codex") {
		options.unshift({
			value: "default",
			label: agentId === "claude-code" ? t("settings.project.permissionUseClaude") : t("settings.project.permissionUseAgent"),
		});
	}
	return (
		<SettingsOptionMenu
			aria-label={ariaLabel}
			value={value === "default" && agentId === "codex" ? "bypass-permissions" : value || "auto"}
			options={options}
			placeholder={t("settings.project.permissionNotReported")}
			triggerClassName="w-full justify-between"
			onChange={onChange}
		/>
	);
}

function projectKindLabel(kind: string, t: TFunction): string {
	switch (kind) {
		case "single_repo":
			return t("settings.project.kind.singleRepo");
		case "workspace":
			return t("settings.project.kind.workspace");
		case "scratch":
			return t("settings.project.kind.scratch");
		default:
			return kind || t("settings.project.kind.unknown");
	}
}

function repositoryHref(repository: string): string {
	if (/^https?:\/\//i.test(repository)) return repository;
	if (repository.startsWith("git@")) {
		const [host, path] = repository.slice(4).split(":", 2);
		return `https://${host}/${path.replace(/\.git$/, "")}`;
	}
	if (repository.startsWith("ssh://")) {
		try {
			const parsed = new URL(repository);
			return `https://${parsed.hostname}${parsed.pathname.replace(/\.git$/, "")}`;
		} catch {
			return repository;
		}
	}
	return repository;
}

function scratchSupportedConfig(config: ProjectConfig): ProjectConfig {
	const { defaultBranch: _defaultBranch, reviewers: _reviewers, autoReview: _legacyAutoReview, trackerIntake: _trackerIntake, ...supported } = config as ProjectConfig;
	return supported;
}

function blankToUndefined<T extends object>(obj: T): T | undefined {
	return Object.values(obj).some((v) => v !== undefined) ? obj : undefined;
}

function buildRoleAgentConfig(
	existing: components["schemas"]["AgentConfig"] | undefined,
	model: string,
	mode: string,
	effort: string,
	permissions: string,
): components["schemas"]["AgentConfig"] | undefined {
	const next = { ...existing };
	if (model) next.model = model;
	else delete next.model;
	if (mode) next.mode = mode;
	else delete next.mode;
	if (effort) next.effort = effort;
	else delete next.effort;
	if (permissions) next.permissions = permissions as components["schemas"]["AgentConfig"]["permissions"];
	else delete next.permissions;
	return Object.keys(next).length > 0 ? next : undefined;
}
