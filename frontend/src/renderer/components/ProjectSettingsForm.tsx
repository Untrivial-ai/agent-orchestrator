import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
	ProjectAgentsSettingsView,
	ProjectGeneralSettingsView,
	ProjectSettingsFormView,
	ProjectSettingsSection,
	ProjectWorkflowSettingsView,
	validateProjectSettings,
} from "@aoagents/product-ui";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { useEffect, useState } from "react";
import { Info, Pencil } from "lucide-react";
import type { components } from "../../api/schema";
import {
	agentModelsQueryKey,
	agentModelsQueryOptions,
	refreshAgentModels,
	revalidateAgentModels,
	type AgentModelCatalog,
} from "../hooks/useAgentModelsQuery";
import { useAgentReadinessQuery, useEnsureAgentReadiness } from "../hooks/useAgentReadinessQuery";
import { useWorkspaceQuery, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { captureOrchestratorReplacementFailure } from "../lib/orchestrator-replacement-telemetry";
import { OrchestratorSpawnError, spawnOrchestrator } from "../lib/spawn-orchestrator";
import { captureRendererEvent } from "../lib/telemetry";
import { type OrchestratorReplacementFailure, useUiStore } from "../stores/ui-store";
import { newestActiveOrchestrator } from "../types/workspace";
import { RequiredAgentField } from "./CreateProjectAgentSheet";
import { buildIntake, deriveRepoPath, deriveRepoHost, IntakeFields, type IntakeForm } from "./IntakeFields";
import { ProductExternalLink } from "./ProductExternalLink";
import { ReviewerSelect, reviewerTrustWarning } from "./ReviewerSelect";
import { AgentModelCombobox } from "./settings/AgentModelCombobox";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { SettingsRow } from "./settings/SettingsRow";
import { Switch } from "./ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type Project = components["schemas"]["Project"];
type ProjectConfig = components["schemas"]["ProjectConfig"];
type TrackerIntakeConfig = components["schemas"]["TrackerIntakeConfig"];

const PERMISSION_MODE_VALUES = ["default", "accept-edits", "auto", "bypass-permissions"] as const;
const DEFAULT_BRANCH_AUTO = "auto";

const projectQueryKey = (id: string) => ["project", id] as const;

type SettingsSaveResult = {
	replacementError: string | null;
	replacementSessionId: string | null;
	replacementFailure: OrchestratorReplacementFailure | null;
	spawnError: unknown;
};

export type ProjectSettingsSection = "general" | "agents" | "workflow" | "intake";
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
				<p className="text-sm text-error">
					{query.error instanceof Error ? query.error.message : t("settings.project.loadFailed")}
				</p>
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
	const navigate = useNavigate();
	const closeSettings = useUiStore((state) => state.closeSettings);
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
		orchestratorModel: config.orchestrator?.agentConfig?.model ?? config.agentConfig?.model ?? "",
		workerMode: config.worker?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		orchestratorMode: config.orchestrator?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		permissions: config.agentConfig?.permissions ?? "",
		reviewerHarness: config.reviewers?.[0]?.harness ?? "",
		reviewerModel: config.reviewers?.[0]?.agentConfig?.model ?? "",
		reviewerMode: config.reviewers?.[0]?.agentConfig?.mode ?? "",
		autoReview: config.autoReview ?? false,
		intakeEnabled: intake.enabled ?? false,
		intakeRepo: intake.repo ?? "",
		intakeAssignee: intake.assignee ?? "",
	});
	const [savedAt, setSavedAt] = useState<number | null>(null);
	const [showSaving, setShowSaving] = useState(false);
	const [replacementError, setReplacementError] = useState<string | null>(null);
	const [validationError, setValidationError] = useState<string | null>(null);
	const initialOrchestratorAgent = config.orchestrator?.agent ?? "";
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
	const reviewerWarning = reviewerTrustWarning(form.reviewerHarness);
	const mutation = useMutation({
		mutationFn: async () => {
			void captureRendererEvent("ao.renderer.settings_save_requested", { project_id: projectId });
			const displayName = form.displayName.trim();
			const {
				model: _legacyModel,
				mode: _legacyMode,
				...sharedAgentConfig
			} = config.agentConfig ?? {};
			const existingReviewer = config.reviewers?.[0];
			const existingReviewerAgentConfig =
				existingReviewer?.harness === form.reviewerHarness ? existingReviewer.agentConfig : undefined;
			const next: ProjectConfig = isScratchProject
				? {
						...scratchSupportedConfig(config),
						worker: {
							...config.worker,
							agent: form.workerAgent,
							agentConfig: buildRoleAgentConfig(config.worker?.agentConfig, form.workerModel, form.workerMode),
						},
						orchestrator: {
							...config.orchestrator,
							agent: form.orchestratorAgent,
							agentConfig: buildRoleAgentConfig(
								config.orchestrator?.agentConfig,
								form.orchestratorModel,
								form.orchestratorMode,
							),
						},
						agentConfig: blankToUndefined({
							...sharedAgentConfig,
							permissions: form.permissions || undefined,
						}),
					}
				: {
						...config,
						defaultBranch:
							form.defaultBranch.trim() === DEFAULT_BRANCH_AUTO
								? undefined
								: form.defaultBranch || undefined,
						sessionPrefix: form.sessionPrefix || undefined,
						worker: {
							...config.worker,
							agent: form.workerAgent,
							agentConfig: buildRoleAgentConfig(config.worker?.agentConfig, form.workerModel, form.workerMode),
						},
						orchestrator: {
							...config.orchestrator,
							agent: form.orchestratorAgent,
							agentConfig: buildRoleAgentConfig(
								config.orchestrator?.agentConfig,
								form.orchestratorModel,
								form.orchestratorMode,
							),
						},
						agentConfig: blankToUndefined({
							...sharedAgentConfig,
							permissions: form.permissions || undefined,
						}),
						reviewers: form.reviewerHarness
							? [
									{
										harness: form.reviewerHarness,
										agentConfig: buildRoleAgentConfig(existingReviewerAgentConfig, form.reviewerModel, form.reviewerMode),
									},
								]
							: undefined,
						trackerIntake: buildIntake(intakeForm),
						autoReview: form.autoReview,
					};
			const { error } = await apiClient.PUT("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
				body: { displayName, config: next },
			});
			if (error) throw new Error(apiErrorMessage(error));
			if (
				form.orchestratorAgent !== initialOrchestratorAgent ||
				(activeOrchestrator && activeOrchestrator.provider !== form.orchestratorAgent)
			) {
				try {
					const sessionId = await spawnOrchestrator(projectId, "settings", true);
					return {
						replacementError: null,
						replacementSessionId: sessionId,
						replacementFailure: null,
						spawnError: null,
					} satisfies SettingsSaveResult;
				} catch (error) {
					const replacementFailure: OrchestratorReplacementFailure = {
						message:
							error instanceof Error ? error.message : t("settings.project.replaceOrchestratorFailed"),
						...(error instanceof OrchestratorSpawnError
							? { code: error.code, requestId: error.requestId }
							: {}),
					};
					return {
						replacementError: replacementFailure.message,
						replacementSessionId: null,
						replacementFailure,
						spawnError: error,
					} satisfies SettingsSaveResult;
				}
			}
			return {
				replacementError: null,
				replacementSessionId: null,
				replacementFailure: null,
				spawnError: null,
			} satisfies SettingsSaveResult;
		},
		onSuccess: async (result) => {
			void captureRendererEvent("ao.renderer.settings_save_succeeded", { project_id: projectId });
			setSavedAt(Date.now());
			setReplacementError(result.replacementError);
			setValidationError(null);
			void queryClient.invalidateQueries({ queryKey: ["project", projectId] });
			const workspaceRefresh = onSaved();

			if (result.replacementSessionId) {
				await workspaceRefresh;
				closeSettings();
				void navigate({
					to: "/projects/$projectId/sessions/$sessionId",
					params: { projectId, sessionId: result.replacementSessionId },
				});
				return;
			}

			if (result.replacementFailure) {
				closeSettings();
				setOrchestratorReplacementError(projectId, result.replacementFailure);
				if (result.spawnError) {
					captureOrchestratorReplacementFailure(result.spawnError, projectId);
				}
			}
		},
		onError: () => {
			void captureRendererEvent("ao.renderer.settings_save_failed", { project_id: projectId });
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
		const mutationError = mutation.isError
			? mutation.error instanceof Error
				? mutation.error.message
				: t("settings.project.saveFailed")
			: undefined;
		onSaveState?.({
			phase: validationError || mutationError
				? "failed"
				: mutation.isPending
					? showSaving
						? "saving"
						: "pending"
					: savedAt !== null
						? "saved"
						: "idle",
			error: validationError ?? mutationError,
			replacementError: !mutation.isPending && !mutation.isError ? replacementError ?? undefined : undefined,
		});
	}, [
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
			onSubmit={() => {
				setSavedAt(null);
				setReplacementError(null);
				const validation = validateProjectSettings(form, { validateIntake: !isScratchProject });
				if (validation) {
					setValidationError(
						validation === "agents_required"
							? t("settings.project.agentsRequired")
							: validation === "name_required"
								? t("settings.project.nameRequired")
								: t("settings.project.intakeAssigneeRequired"),
					);
					return;
				}
				setValidationError(null);
				mutation.mutate();
			}}
		>
			{section === "general" && (
				<>
					<ProjectGeneralSettingsView
						displayName={form.displayName}
						externalLink={ProductExternalLink}
						icons={{
							edit: <Pencil className="settings-inline-edit-icon" aria-hidden="true" />,
						}}
						onDisplayNameChange={(displayName) => setForm((f) => ({ ...f, displayName }))}
						labels={{
							title: t("settings.project.identity"),
							name: t("settings.project.name"),
							id: t("settings.project.id"),
							kind: t("settings.project.kind"),
							path: t("settings.project.path"),
							repo: t("settings.project.repo"),
							workspaceRepos: t("settings.project.workspaceRepos"),
							workspaceReposEmpty: t("settings.project.childReposEmpty"),
							editName: t("settings.field.edit", { label: t("settings.project.name") }),
						}}
						project={{
							id: project.id,
							kindLabel: projectKindLabel(project.kind, t),
							path: project.path,
							pathHref: `file://${encodeURI(project.path)}`,
							repo: project.repo,
							repoHref: project.repo ? repositoryHref(project.repo) : undefined,
							workspaceRepos: project.kind === "workspace" ? project.workspaceRepos ?? [] : undefined,
						}}
					/>
				</>
			)}

			{section === "agents" && (
				<>
					<ProjectAgentsSettingsView
						title={t("settings.project.agents")}
						workerArea={
							<RequiredAgentField
								id="workerAgent"
								variant="settings-row"
								value={form.workerAgent}
								placeholder={t("settings.project.selectWorker")}
								label={t("settings.project.defaultWorker")}
								agents={agentCatalog?.agents}
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
								invalid={validationError !== null && form.workerAgent === ""}
								onChange={(v) =>
									setForm((f) => ({ ...f, workerAgent: v, workerModel: "", workerMode: "" }))
								}
							/>
						}
						workerModelArea={
							<AgentModelField
								role="worker"
								agentId={form.workerAgent}
								projectId={projectId}
								model={form.workerModel}
								mode={form.workerMode}
								onModelChange={(workerModel) => setForm((f) => ({ ...f, workerModel }))}
								onModeChange={(workerMode) => setForm((f) => ({ ...f, workerMode }))}
							/>
						}
						orchestratorArea={
							<RequiredAgentField
								id="orchestratorAgent"
								variant="settings-row"
								value={form.orchestratorAgent}
								placeholder={t("settings.project.selectOrchestrator")}
								label={t("settings.project.defaultOrchestrator")}
								agents={agentCatalog?.agents}
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
								invalid={validationError !== null && form.orchestratorAgent === ""}
								onChange={(v) =>
									setForm((f) => ({
										...f,
										orchestratorAgent: v,
										orchestratorModel: "",
										orchestratorMode: "",
									}))
								}
							/>
						}
						orchestratorModelArea={
							<AgentModelField
								role="orchestrator"
								agentId={form.orchestratorAgent}
								projectId={projectId}
								model={form.orchestratorModel}
								mode={form.orchestratorMode}
								onModelChange={(orchestratorModel) => setForm((f) => ({ ...f, orchestratorModel }))}
								onModeChange={(orchestratorMode) => setForm((f) => ({ ...f, orchestratorMode }))}
							/>
						}
						permissions={{
							control: (
								<PermissionModeSelect
									value={form.permissions}
									onChange={(v) => setForm((f) => ({ ...f, permissions: v }))}
								/>
							),
							label: t("settings.project.permissionMode"),
						}}
						missingRequiredMessage={
							missingRequiredAgent ? t("settings.project.agentsRequired") : null
						}
					/>
				{!isScratchProject && (
					<ProjectSettingsSection title={t("settings.project.reviewer")} grouped>
						<SettingsRow label={t("settings.project.defaultReviewer")}>
							<ReviewerSelect
								value={form.reviewerHarness}
								onChange={(v) =>
									setForm((f) => ({
										...f,
										reviewerHarness: v,
										...(v !== f.reviewerHarness ? { reviewerModel: "", reviewerMode: "" } : {}),
									}))
								}
								onConfigChange={(_harness, agentConfig) =>
									setForm((f) => ({
										...f,
										reviewerModel: agentConfig.model ?? "",
										reviewerMode: agentConfig.mode ?? "",
									}))
								}
								model={form.reviewerModel}
								mode={form.reviewerMode}
								projectId={projectId}
								ariaLabel={t("settings.project.defaultReviewer")}
								agents={agentCatalog?.agents}
								defaultOptionLabel={t("settings.project.default")}
								defaultTriggerLabel={t("settings.project.default")}
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
							/>
						</SettingsRow>
						{reviewerWarning && (
							<p className="px-1 text-xs leading-row text-warning" role="status">
								{reviewerWarning}
							</p>
						)}
						<div className="settings-row-bar">
							<div className="flex shrink-0 items-center gap-1.5">
								<span className="whitespace-nowrap text-sm leading-5 text-settings-label">
									{t("settings.project.autoReviewToggle")}
								</span>
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
				)}
				</>
			)}

			{section === "workflow" && (
				<>
					{!isScratchProject ? (
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
						</>
					) : (
						<p className="px-1 text-xs text-settings-muted">{t("settings.project.workflow")}</p>
					)}
				</>
			)}

			{section === "intake" && (
				<>
					{!isScratchProject ? (
						<ProjectSettingsSection title={t("settings.project.trackerIntake")} grouped>
							<IntakeFields
								variant="settings"
								form={intakeForm}
								onChange={patchIntake}
								repoPreview={{ value: effectiveIntakeRepo, host: deriveRepoHost(project.repo) }}
							/>
						</ProjectSettingsSection>
					) : (
						<p className="px-1 text-xs text-settings-muted">{t("settings.project.trackerIntake")}</p>
					)}
				</>
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
	onModelChange,
	onModeChange,
}: {
	role: "worker" | "orchestrator";
	agentId: string;
	projectId: string;
	model: string;
	mode: string;
	onModelChange: (value: string) => void;
	onModeChange: (value: string) => void;
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
		(revalidationQuery.isError
			? revalidationQuery.error instanceof Error
				? revalidationQuery.error.message
				: t("settings.models.validateFailed")
			: undefined) ??
		catalog?.warning ??
		(query.isError ? (query.error instanceof Error ? query.error.message : t("settings.models.loadFailed")) : undefined);

	if (agentId !== "" && query.isFetching && catalog === undefined) {
		return (
			<SettingsRow label={label}>
				<span className="text-xs text-settings-muted" role="status" aria-label={t("settings.models.loading")}>
					{t("settings.models.loading")}
				</span>
			</SettingsRow>
		);
	}

	if (isMode) {
		const options = [
			{ value: "__default__", label: t("settings.models.agentDefault") },
			...(catalog.models ?? []).map((item) => ({ value: item.id, label: item.label })),
		];
		return (
			<>
				<SettingsRow label={label}>
					<div className="flex min-w-0 items-center gap-2">
						<SettingsOptionMenu
							aria-label={label}
							value={mode || "__default__"}
							options={options}
							triggerClassName="justify-end"
							onChange={(value) => {
								onModeChange(value === "__default__" ? "" : value);
								onModelChange("");
							}}
						/>
					</div>
				</SettingsRow>
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
			<SettingsRow label={label}>
				<div className="flex min-w-0 items-center gap-2">
					<AgentModelCombobox
						aria-label={label}
						value={model}
						models={catalog?.models ?? []}
						allowCustom={catalog?.allowCustom}
						customModelEntry={customModelEntry}
						agentLabel={agentId}
						onRefresh={refreshCatalog}
						disabled={query.isFetching || agentId === ""}
						onChange={selectCatalogModel}
						onCustom={selectCustomModel}
						triggerClassName="justify-end"
					/>
				</div>
			</SettingsRow>
			{warning && <p className="px-1 text-xs leading-row text-warning">{warning}</p>}
		</>
	);
}

function PermissionModeSelect({ value, onChange }: { value: string; onChange: (value: string) => void }) {
	const { t } = useTranslation();
	const options = [
		{ value: "__default__", label: `${t("settings.project.permissionAuto")} (${t("settings.project.default")})` },
		...PERMISSION_MODE_VALUES.map((value) => ({
			value,
			label:
				value === "default"
					? t("settings.project.permissionDefault")
					: value === "accept-edits"
						? t("settings.project.permissionAcceptEdits")
						: value === "auto"
							? t("settings.project.permissionAuto")
							: t("settings.project.permissionBypass"),
		})),
	];

	return (
		<SettingsOptionMenu
			aria-label={t("settings.project.permissionMode")}
			value={value || "__default__"}
			options={options}
			onChange={(v) => onChange(v === "__default__" ? "" : v)}
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
	const {
		defaultBranch: _defaultBranch,
		reviewers: _reviewers,
		autoReview: _legacyAutoReview,
		trackerIntake: _trackerIntake,
		...supported
	} = config as ProjectConfig;
	return supported;
}

function blankToUndefined<T extends object>(obj: T): T | undefined {
	return Object.values(obj).some((v) => v !== undefined) ? obj : undefined;
}

function buildRoleAgentConfig(
	existing: components["schemas"]["AgentConfig"] | undefined,
	model: string,
	mode: string,
): components["schemas"]["AgentConfig"] | undefined {
	const next = { ...existing };
	if (model) next.model = model;
	else delete next.model;
	if (mode) next.mode = mode;
	else delete next.mode;
	return Object.keys(next).length > 0 ? next : undefined;
}
