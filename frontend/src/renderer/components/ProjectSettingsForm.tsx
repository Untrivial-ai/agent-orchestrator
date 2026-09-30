import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useRef } from "react";
import type { components } from "../../api/schema";
import { useAgentReadinessQuery, useEnsureAgentReadiness } from "../hooks/useAgentReadinessQuery";
import { useWorkspaceQuery, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { useSettings } from "../hooks/useSettings";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { WORKER_DEFAULT_REVIEWERS } from "../lib/reviewer-harnesses";
import { captureOrchestratorReplacementFailure } from "../lib/orchestrator-replacement-telemetry";
import { OrchestratorSpawnError, spawnOrchestrator } from "../lib/spawn-orchestrator";
import { captureRendererEvent } from "../lib/telemetry";
import { type OrchestratorReplacementFailure, useUiStore } from "../stores/ui-store";
import { newestActiveOrchestrator } from "../types/workspace";
import { RequiredAgentField } from "./CreateProjectAgentSheet";
import { buildIntake } from "./IntakeFields";
import { ReviewerSelect, reviewerTrustWarning } from "./ReviewerSelect";
import { ProjectSettingsEditor, type ProjectAgentPickerProps, type ProjectSettingsDraft } from "./ProjectSettingsEditor";
import { CloudProjectSettingsAdapter } from "./CloudProjectSettingsForm";

type Project = components["schemas"]["Project"];
type ProjectConfig = components["schemas"]["ProjectConfig"];
type TrackerIntakeConfig = components["schemas"]["TrackerIntakeConfig"];

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
	dirty?: boolean;
	requestPending?: boolean;
	error?: string;
	replacementError?: string;
	retry?: () => void;
};

type ProjectSettingsFormProps = {
	projectId: string;
	cloudOrgId?: string;
	section?: ProjectSettingsSection;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
};

export function ProjectSettingsForm(props: ProjectSettingsFormProps) {
	return props.cloudOrgId !== undefined
		? <CloudProjectSettingsAdapter {...props} cloudOrgId={props.cloudOrgId} />
		: <LocalProjectSettingsAdapter {...props} />;
}

function LocalProjectSettingsAdapter({
	projectId,
	section = "general",
	onSaveState,
}: ProjectSettingsFormProps) {
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

function SettingsBody({ project, projectId, onSaved, section = "general", onSaveState }: {
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
	const { settings } = useSettings();
	const intakeVisible = !isScratchProject && !!settings?.trackerIntakeEnabled;
	const workspace = workspaceQuery.data?.find((item) => item.id === projectId);
	const activeOrchestrator = newestActiveOrchestrator(workspace?.sessions ?? []);
	const intake: TrackerIntakeConfig = config.trackerIntake ?? {};
	const initialValues: ProjectSettingsDraft = {
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
	};
	const lastOrchestratorRef = useRef(config.orchestrator?.agent ?? "");
	const replacementAttemptedRef = useRef(false);
	const agentsQuery = useAgentReadinessQuery();
	useEnsureAgentReadiness();
	const persist = async (values: ProjectSettingsDraft): Promise<SettingsSaveResult> => {
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
	};
	const save = async (values: ProjectSettingsDraft) => {
		try {
			const result = await persist(values);
			void captureRendererEvent("ao.renderer.settings_save_succeeded", { project_id: projectId });
			void queryClient.invalidateQueries({ queryKey: projectQueryKey(projectId) });
			void onSaved();
			if (result.replacementFailure) {
				setOrchestratorReplacementError(projectId, result.replacementFailure);
				if (result.spawnError) captureOrchestratorReplacementFailure(result.spawnError, projectId);
			}
			return { replacementError: result.replacementError };
		} catch (error) {
			void captureRendererEvent("ao.renderer.settings_save_failed", { project_id: projectId });
			throw error;
		}
	};
	return <ProjectSettingsEditor initialValues={initialValues} section={section}
		capabilities={{ workflow: !isScratchProject, sessionPrefix: !isScratchProject, intake: intakeVisible, reviewer: !isScratchProject, requiredAgents: true }}
		details={[{ label: t("settings.project.path"), value: project.path, href: `file://${encodeURI(project.path)}` }, { label: t("settings.project.repo"), value: project.repo || "—", href: project.repo ? repositoryHref(project.repo) : undefined }]}
		workspaceRepos={project.kind === "workspace" ? project.workspaceRepos ?? [] : undefined} repository={project.repo}
		modelScope={() => projectId} defaultReviewer={(draft) => WORKER_DEFAULT_REVIEWERS[draft.workerAgent] ?? "claude-code"}
		reviewerWarning={reviewerTrustWarning} save={save} saveUnchanged onSaveState={onSaveState}
		renderAgent={(props) => <LocalAgentPicker {...props} projectId={projectId} agentsQuery={agentsQuery} />} />;
}

function LocalAgentPicker({ role, draft, value, invalid, onChange, projectId, agentsQuery }: ProjectAgentPickerProps & { projectId: string; agentsQuery: ReturnType<typeof useAgentReadinessQuery> }) {
	const { t } = useTranslation();
	useEnsureAgentReadiness({ agentIds: [draft.workerAgent, draft.orchestratorAgent, draft.reviewerHarness], enabled: role === "worker" && Boolean(draft.workerAgent || draft.orchestratorAgent || draft.reviewerHarness) });
	const disabled = agentsQuery.isFetching && agentsQuery.data === undefined;
	return role === "reviewer"
		? <ReviewerSelect value={value} model={draft.reviewerModel} mode={draft.reviewerMode} projectId={projectId} harnessOnly defaultHarness={WORKER_DEFAULT_REVIEWERS[draft.workerAgent] ?? "claude-code"} triggerClassName="w-full" onChange={onChange} ariaLabel={t("settings.project.defaultReviewer")} agents={agentsQuery.data?.agents} disabled={disabled} />
		: <RequiredAgentField id={`${role}Agent`} variant="settings-control" value={value} placeholder={t(role === "worker" ? "settings.project.selectWorker" : "settings.project.selectOrchestrator")} label={t(role === "worker" ? "settings.project.defaultWorker" : "settings.project.defaultOrchestrator")} agents={agentsQuery.data?.agents} disabled={disabled} invalid={invalid} onChange={onChange} />;
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
