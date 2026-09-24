import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
	ExecutionContextView,
	TaskComposerView,
	type TaskComposerAgentControl,
	type TaskComposerEffortControl,
	type TaskComposerModelCatalog,
	type TaskComposerModelControl,
} from "@aoagents/product-ui";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { RequiredAgentField } from "./CreateProjectAgentSheet";
import type { components } from "../../api/schema";
import { apiClient, apiErrorCode, apiErrorMessage } from "../lib/api-client";
import { captureRendererEvent } from "../lib/telemetry";
import {
	cacheAgentReadiness,
	ensureAgentReadiness,
	useAgentReadinessQuery,
	useEnsureAgentReadiness,
} from "../hooks/useAgentReadinessQuery";
import { type FileAttachmentPayload, useFileAttachments } from "../hooks/useFileAttachments";
import { useSettings } from "../hooks/useSettings";
import { useCloudCp } from "../hooks/useCloudCp";
import { useCloudOrg } from "../hooks/useCloudOrg";
import { useProviderConnections } from "../hooks/useProviderConnections";
import { cloudAgentInfos } from "../lib/cloud-agents";
import { isConcreteModelID, modelChoiceLabel } from "../lib/agent-model-choices";
import {
	buildRankedAgentOptions,
	DEFAULT_AGENT_PRIORITY_RANK,
	isReadyAgent,
} from "../lib/agent-select-options";
import { useSandboxProviderStore } from "../stores/sandbox-provider-store";
import { executionContextLabels, projectRepositories } from "../lib/execution-context";
import { cloudSessionsQueryKey, useCloudProjectsQuery } from "../hooks/useWorkspaceQuery";
import {
	agentModelsQueryKey,
	agentModelsQueryOptions,
	refreshAgentModels,
	revalidateAgentModels,
} from "../hooks/useAgentModelsQuery";
import { STANDALONE_WORKSPACE_ID } from "../types/workspace";
import { AgentModelCombobox } from "./settings/AgentModelCombobox";
import { useModelTuning } from "./settings/ModelTuningControls";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import {
	readTaskComposerPreferences,
	rememberTaskComposerPreference,
	type TaskComposerAgentPreference,
} from "../lib/task-composer-preferences";

type Project = components["schemas"]["Project"];
type DelegateAgent = components["schemas"]["DelegateTaskRequest"]["agent"];

type CreateTaskInput = {
	projectId: string;
	brief: string;
	agent?: DelegateAgent;
	model?: string;
	effort?: string;
	mode?: "tui";
	approvalMode?: "bypass-permissions";
	attachments?: FileAttachmentPayload[];
};

const CHAT_PREFLIGHT_CODES = new Set([
	"SESSION_MODE_UNSUPPORTED",
	"CHAT_DRIVER_UNAVAILABLE",
	"CHAT_DRIVER_INCOMPATIBLE",
	"CHAT_AUTH_REQUIRED",
]);

const READINESS_RECONCILE_CODES = new Set(["AGENT_BINARY_NOT_FOUND", "AGENT_AUTH_REQUIRED", "CHAT_AUTH_REQUIRED"]);

class TaskCreateError extends Error {
	constructor(
		message: string,
		readonly code?: string,
		readonly details?: components["schemas"]["APIError"]["details"],
	) {
		super(message);
		this.name = "TaskCreateError";
	}
}

type FallbackAction = "tui" | "bypass-permissions";

function hasErrorDetail(details: components["schemas"]["APIError"]["details"] | undefined, key: string, value: string) {
	const values = details?.[key];
	return Array.isArray(values) && values.includes(value);
}

export type TaskComposerProps = {
	projectId?: string;
	onCreated: (sessionId: string) => void;
	onDirtyChange?: (dirty: boolean) => void;
	onSubmittingChange?: (submitting: boolean) => void;
	autoFocusTitle?: boolean;
};

export function TaskComposer({
	projectId,
	onCreated,
	onDirtyChange,
	onSubmittingChange,
	autoFocusTitle,
}: TaskComposerProps) {
	const { t } = useTranslation();
	const taskPlaceholder = useMemo(() => {
		const placeholders = t("newTask.taskPlaceholders" as never, { returnObjects: true }) as string[];
		return Array.isArray(placeholders)
			? (placeholders[Math.floor(Math.random() * placeholders.length)] ?? "")
			: "";
	}, [t]);
	const queryClient = useQueryClient();
	const [isPromptDirty, setIsPromptDirty] = useState(false);
	const [model, setModel] = useState("");
	const [mode, setMode] = useState("");
	const [effort, setEffort] = useState("");
	const [agent, setAgent] = useState("");
	const [agentTouched, setAgentTouched] = useState(false);
	const [modelTouched, setModelTouched] = useState(false);
	const [effortTouched, setEffortTouched] = useState(false);
	const [isSubmitting, setIsSubmitting] = useState(false);
	const [error, setError] = useState<string | undefined>();
	const [fallbackAction, setFallbackAction] = useState<FallbackAction>();
	const {
		attachments,
		error: attachmentError,
		addFiles,
		remove: removeAttachment,
		clear: clearAttachments,
		toSettledPayload,
	} = useFileAttachments();
	// Cloud vs local is decided here and nowhere else: a cloud project routes task
	// creation to the control plane (which provisions a sandbox), while a local
	// project keeps the existing daemon flow untouched.
	const { client: cloudClient } = useCloudCp();
	const { org: cloudOrg } = useCloudOrg();
	// The user's client-side sandbox-provider preference (when the control plane
	// offers more than one); omitted lets the control plane use its default.
	const selectedProvider = useSandboxProviderStore((s) => s.selectedProvider);
	const cloudProjects = useCloudProjectsQuery();
	const cloudProject = (cloudProjects.data ?? []).find((project) => project.id === projectId);
	const isCloudProject = Boolean(cloudProject);
	const isStandalone = projectId === STANDALONE_WORKSPACE_ID;
	const preferenceContext = projectId ?? "";
	const persistedPreferences = useMemo(
		() => readTaskComposerPreferences(preferenceContext),
		[preferenceContext],
	);
	const agentDrafts = useRef<Record<string, TaskComposerAgentPreference>>({
		...persistedPreferences?.agents,
	}).current;
	// A cloud project is unknown to the local daemon, so the local model catalog
	// must be queried agent-level (no project scope); otherwise the request 404s
	// and the model dropdown spins forever. Local projects keep their scope.
	const modelsProjectId = isCloudProject || isStandalone ? "" : (projectId ?? "");

	const createCloudTask = useCallback(
		async (input: CreateTaskInput): Promise<string> => {
			void captureRendererEvent("ao.renderer.task_create_requested", { project_id: input.projectId });
			if (!cloudOrg?.id) throw new Error(t("newTask.unableToStart"));
			try {
				const { session } = await cloudClient.createSession(cloudOrg.id, {
					projectId: input.projectId,
					kind: "worker",
					harness: input.agent ?? "claude-code",
					displayName: input.brief.trim().slice(0, 100) || (input.agent ?? "claude-code"),
					prompt: input.brief,
					...(selectedProvider ? { provider: selectedProvider } : {}),
				});
				// The control plane provisions the sandbox asynchronously; surface the
				// new session on the board immediately.
				void queryClient.invalidateQueries({ queryKey: cloudSessionsQueryKey });
				void captureRendererEvent("ao.renderer.task_create_succeeded", { project_id: input.projectId });
				return session.id;
			} catch (err) {
				void captureRendererEvent("ao.renderer.task_create_failed", { project_id: input.projectId });
				throw err instanceof Error ? err : new Error(t("newTask.unableToStart"));
			}
		},
		[cloudClient, cloudOrg, queryClient, selectedProvider, t],
	);

	const createLocalTask = useCallback(
		async (input: CreateTaskInput): Promise<string> => {
			void captureRendererEvent("ao.renderer.task_create_requested", { project_id: input.projectId });
			try {
				const { data, error } = await apiClient.POST("/api/v1/orchestrators/delegate", {
					body: {
						projectId: input.projectId,
						brief: input.brief,
						agent: input.agent,
						...(input.model ? { model: input.model } : {}),
						...(input.effort !== undefined ? { effort: input.effort } : {}),
						...(input.mode ? { mode: input.mode } : {}),
						...(input.approvalMode ? { approvalMode: input.approvalMode } : {}),
						...(input.attachments && input.attachments.length > 0 ? { attachments: input.attachments } : {}),
					},
				});
				if (error) {
					throw new TaskCreateError(
						apiErrorMessage(error, t("newTask.unableToStart")),
						apiErrorCode(error),
						error.details,
					);
				}
				if (!data?.workerId) throw new Error(t("newTask.noSession"));
				void captureRendererEvent("ao.renderer.task_create_succeeded", { project_id: input.projectId });
				return data.workerId;
			} catch (err) {
				void captureRendererEvent("ao.renderer.task_create_failed", { project_id: input.projectId });
				if (
					err instanceof TaskCreateError &&
					err.code &&
					READINESS_RECONCILE_CODES.has(err.code) &&
					input.agent
				) {
					try {
						const completed = await ensureAgentReadiness([input.agent], "launch");
						cacheAgentReadiness(queryClient, completed);
					} catch {
						// Preserve the launch error when opportunistic reconciliation fails.
					}
				}
				throw err instanceof Error ? err : new Error(t("newTask.unableToStart"));
			}
		},
		[queryClient, t],
	);

	const createStandaloneTask = useCallback(
		async (input: CreateTaskInput): Promise<string> => {
			void captureRendererEvent("ao.renderer.task_create_requested", { scope: "standalone" });
			const displayName = input.brief.trim().slice(0, 100) || input.agent || "Standalone agent";
			const { data, error } = await apiClient.POST("/api/v1/sessions", {
				body: {
					kind: "worker",
					harness: input.agent as components["schemas"]["SpawnSessionRequest"]["harness"],
					prompt: input.brief,
					displayName,
					model: input.model,
					...(input.effort ? { effort: input.effort } : {}),
					...(input.mode ? { mode: input.mode } : {}),
					...(input.attachments && input.attachments.length > 0 ? { attachments: input.attachments } : {}),
				},
			});
			if (error) {
				throw new TaskCreateError(apiErrorMessage(error, t("newTask.unableToStart")), apiErrorCode(error), error.details);
			}
			if (!data?.session.id) throw new Error(t("newTask.noSession"));
			void captureRendererEvent("ao.renderer.task_create_succeeded", { scope: "standalone" });
			return data.session.id;
		},
		[t],
	);

	const createTask = useCallback(
		(input: CreateTaskInput): Promise<string> =>
			isStandalone ? createStandaloneTask(input) : isCloudProject ? createCloudTask(input) : createLocalTask(input),
		[isStandalone, isCloudProject, createStandaloneTask, createCloudTask, createLocalTask],
	);

	const projectQuery = useQuery({
		// A cloud project lives in the control plane, not the local daemon, so this
		// local lookup would 404 (PROJECT_NOT_FOUND); skip it for cloud projects.
		queryKey: ["project", projectId],
		enabled: Boolean(projectId) && !isCloudProject && !isStandalone,
		queryFn: async () => {
			const { data, error: apiError } = await apiClient.GET("/api/v1/projects/{id}", {
				params: { path: { id: projectId ?? "" } },
			});
			if (apiError) throw new Error(apiErrorMessage(apiError));
			if (data?.status !== "ok") throw new Error(t("newTask.configUnavailable"));
			return data.project as Project;
		},
	});
	const agentsQuery = useAgentReadinessQuery();
	const { settings } = useSettings();
	// The composer preselects the agent and model a spawn would actually use
	// instead of parking the controls on a "default" label the user has to
	// remember. Both resolved values remain directly editable.
	// A cloud project is unknown to the local daemon, so the local projectQuery
	// above is disabled for it; its config lives in the control-plane projects
	// list (already fetched as cloudProjects). Reading the worker defaults from
	// the disabled local query is what made a cloud worker ignore
	// config.worker.agent and fall back to claude-code.
	const projectConfig = (isCloudProject ? cloudProject?.config : projectQuery.data?.config) as
		| {
				worker?: { agent?: string; agentConfig?: { model?: string; mode?: string; effort?: string } };
				agentConfig?: { model?: string; mode?: string; effort?: string };
		  }
		| undefined;
	// A cloud project's execution context should list every repo it spans (the
	// primary plus the coder dev-kit extra repos), not just the primary — so
	// multi-repo projects read as multi-repo. Narrows the untyped config safely.
	const cloudRepositories = (() => {
		if (!cloudProject) return [] as string[];
		const coder = (cloudProject.config as { coder?: { extraRepos?: Array<{ url?: string }> } } | undefined)?.coder;
		const declared = Array.isArray(coder?.extraRepos) ? coder?.extraRepos ?? [] : [];
		const extras = declared.map((repo) => repo?.url).filter((url): url is string => Boolean(url));
		return [...new Set([cloudProject.repositoryUrl, ...extras].filter(Boolean))];
	})();
	const projectWorkerAgent = projectConfig?.worker?.agent ?? "";
	const globalDefaultAgent = projectQuery.data?.agent ?? "";
	const configuredProjectAgent = projectWorkerAgent || globalDefaultAgent;
	const agentCatalog = agentsQuery.data;
	// Cloud projects only support the three control-plane agents (claude-code,
	// codex, cursor), with readiness derived from the org's provider connections.
	const cloudConnectionsQuery = useProviderConnections(isCloudProject ? cloudOrg?.id : undefined);
	const cloudAgents = useMemo(() => cloudAgentInfos(cloudConnectionsQuery.data), [cloudConnectionsQuery.data]);
	const standaloneDefaultAgent = useMemo(() => {
		if (!isStandalone || !agentCatalog) return "";
		return (
			buildRankedAgentOptions({
				agents: agentCatalog.agents,
				priorityRank: DEFAULT_AGENT_PRIORITY_RANK,
				fallbackAgents: [],
			}).find(isReadyAgent)?.id ?? ""
		);
	}, [agentCatalog, isStandalone]);
	const configuredDefaultAgent = isStandalone
		? standaloneDefaultAgent
		: configuredProjectAgent;
	const rememberedAgent = persistedPreferences?.lastAgent ?? "";
	const availableAgents = isCloudProject ? cloudAgents : agentCatalog?.agents;
	const rememberedAgentIsAvailable = Boolean(
		availableAgents?.some((candidate) => candidate.id === rememberedAgent && isReadyAgent(candidate)),
	);
	const defaultWorkerAgent = rememberedAgentIsAvailable ? rememberedAgent : configuredDefaultAgent;
	const selectedAgent = agent || defaultWorkerAgent;
	useEnsureAgentReadiness();
	useEnsureAgentReadiness({
		agentIds: selectedAgent ? [selectedAgent] : [],
		enabled: selectedAgent !== "",
		purpose: "launch",
	});
	const defaultWorkerModel =
		projectConfig?.worker?.agentConfig?.model ?? projectConfig?.agentConfig?.model ?? "";
	const defaultWorkerMode = projectConfig?.worker?.agentConfig?.mode ?? projectConfig?.agentConfig?.mode ?? "";
	const defaultWorkerEffort =
		projectConfig?.worker?.agentConfig?.effort ?? projectConfig?.agentConfig?.effort ?? "";
	const projectModelForSelectedAgent = selectedAgent === configuredProjectAgent
		? defaultWorkerModel
		: "";
	const projectModeForSelectedAgent = selectedAgent === configuredProjectAgent
		? defaultWorkerMode
		: "";
	// Shares the picker's query key, so this is the same fetch, not a second one.
	const modelCatalogQuery = useQuery(agentModelsQueryOptions(selectedAgent, modelsProjectId));
	const revalidationQuery = useQuery({
		queryKey: [
			"agent-model-revalidation",
			selectedAgent,
			modelsProjectId,
			modelCatalogQuery.data?.validatedAt ?? "",
		],
		queryFn: () => revalidateAgentModels(selectedAgent, modelsProjectId),
		enabled: selectedAgent !== "" && modelCatalogQuery.data?.refreshRecommended === true,
		staleTime: Number.POSITIVE_INFINITY,
		retry: false,
	});
	useEffect(() => {
		if (revalidationQuery.data) {
			queryClient.setQueryData(
				agentModelsQueryKey(selectedAgent, modelsProjectId),
				revalidationQuery.data,
			);
		}
	}, [modelsProjectId, queryClient, revalidationQuery.data, selectedAgent]);
	const modelWarning =
		(revalidationQuery.isError
			? revalidationQuery.error instanceof Error
				? revalidationQuery.error.message
				: t("settings.models.validateFailed")
			: undefined) ??
		modelCatalogQuery.data?.warning ??
		(modelCatalogQuery.isError
			? modelCatalogQuery.error instanceof Error
				? modelCatalogQuery.error.message
				: t("settings.models.loadFailed")
			: undefined);
	const modelCatalog: TaskComposerModelCatalog | undefined = modelCatalogQuery.data
		? {
				allowCustom: modelCatalogQuery.data.allowCustom,
				customModelEntry: modelCatalogQuery.data.customModelEntry,
				lastSuccessAt: modelCatalogQuery.data.lastSuccessAt,
				models: modelCatalogQuery.data.models,
				refreshError: modelCatalogQuery.data.refreshError,
				refreshState: modelCatalogQuery.data.refreshState,
				retryAt: modelCatalogQuery.data.retryAt,
				selectionMode: modelCatalogQuery.data.selectionMode,
			}
		: undefined;
	// An unmarked first row is not evidence of what the provider will run.
	const catalogModels = modelCatalogQuery.data?.models?.filter((item) => isConcreteModelID(item.id)) ?? [];
	const catalogDefaultOption =
		catalogModels.find((item) => item.isDefault)?.id ?? "";
	const catalogUsesModes = modelCatalogQuery.data?.selectionMode === "mode";
	const rememberedConfigForSelectedAgent = agentDrafts[selectedAgent];
	const rememberedModel = rememberedConfigForSelectedAgent?.model ?? "";
	const rememberedMode = rememberedConfigForSelectedAgent?.mode ?? "";
	const rememberedModelIsValid =
		isConcreteModelID(rememberedModel) &&
		Boolean(
			modelCatalogQuery.data &&
				(modelCatalogQuery.data.allowCustom || catalogModels.some((item) => item.id === rememberedModel)),
		);
	const rememberedModeIsValid =
		isConcreteModelID(rememberedMode) && catalogModels.some((item) => item.id === rememberedMode);
	const defaultModelForSelectedAgent =
		(rememberedModelIsValid ? rememberedModel : "") ||
		(isConcreteModelID(projectModelForSelectedAgent) ? projectModelForSelectedAgent : "") ||
		(catalogUsesModes ? "" : catalogDefaultOption);
	const defaultModeForSelectedAgent =
		(rememberedModeIsValid ? rememberedMode : "") ||
		(isConcreteModelID(projectModeForSelectedAgent) ? projectModeForSelectedAgent : "") ||
		(catalogUsesModes ? catalogDefaultOption : "");
	const selectedModel = model || (modelTouched ? (catalogUsesModes ? "" : catalogDefaultOption) : defaultModelForSelectedAgent);
	const selectedMode = mode || (modelTouched ? (catalogUsesModes ? catalogDefaultOption : "") : defaultModeForSelectedAgent);
	const selectedModelOrMode = (selectedModel || selectedMode).trim();
	const projectModelOrMode = projectModelForSelectedAgent || projectModeForSelectedAgent;
	const requestedModel = selectedModelOrMode && selectedModelOrMode !== projectModelOrMode && (
		selectedModelOrMode !== catalogDefaultOption || isConcreteModelID(projectModelOrMode)
	) ? selectedModelOrMode : undefined;
	const rememberedEffortIsExplicit = Boolean(
		rememberedConfigForSelectedAgent &&
			Object.prototype.hasOwnProperty.call(rememberedConfigForSelectedAgent, "effort"),
	);
	const defaultEffortForSelectedAgent = rememberedEffortIsExplicit
		? (rememberedConfigForSelectedAgent?.effort ?? "")
		: selectedAgent === configuredProjectAgent
			? defaultWorkerEffort
			: "";
	const { selected: effortModel } = useModelTuning({
		models: catalogModels,
		model: selectedModel,
		effort,
		onEffortChange: setEffort,
		onEffortReset: setEffort,
	});
	const effortOptions = effortModel?.efforts?.filter((option) => option && option.toLowerCase() !== "default") ?? [];
	const inheritedEffort = selectedAgent === configuredProjectAgent ? defaultWorkerEffort : "";
	const implicitEffort = inheritedEffort || effortModel?.defaultEffort || "";
	const requestedEffort = effortTouched || rememberedEffortIsExplicit
		? effort === implicitEffort ? undefined : effort
		: undefined;

	const selectedAgentLabel = agentCatalog?.agents.find((item) => item.id === selectedAgent)?.label || selectedAgent;
	const requiresTuiFallback =
		selectedAgent !== "" &&
		settings?.defaultSessionMode === "chat" &&
		!settings.chatHarnesses.includes(selectedAgent);
	const canSubmit =
		Boolean(projectId) &&
		(!isStandalone || selectedAgent !== "") &&
		(isCloudProject || isStandalone || projectQuery.data !== undefined);
	const refreshSelectedModels = useCallback(async () => {
		const refreshed = await refreshAgentModels(selectedAgent, modelsProjectId);
		queryClient.setQueryData(agentModelsQueryKey(selectedAgent, modelsProjectId), refreshed);
	}, [modelsProjectId, queryClient, selectedAgent]);
	useEffect(() => {
		if (!agentTouched) setAgent(defaultWorkerAgent);
	}, [agentTouched, defaultWorkerAgent]);
	useEffect(() => {
		if (!modelTouched) {
			setModel(defaultModelForSelectedAgent);
			setMode(defaultModeForSelectedAgent);
		}
	}, [defaultModelForSelectedAgent, defaultModeForSelectedAgent, modelTouched]);
	useEffect(() => {
		if (!effortTouched) setEffort(defaultEffortForSelectedAgent);
	}, [defaultEffortForSelectedAgent, effortTouched]);

	const isDirty = isPromptDirty || modelTouched || effortTouched || attachments.length > 0;
	const handlePromptChange = useCallback((value: string) => {
		const nextDirty = value.trim() !== "";
		setIsPromptDirty((wasDirty) => (wasDirty === nextDirty ? wasDirty : nextDirty));
	}, []);
	useEffect(() => {
		onDirtyChange?.(isDirty);
	}, [isDirty, onDirtyChange]);
	useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);

	useEffect(() => {
		onSubmittingChange?.(isSubmitting);
	}, [isSubmitting, onSubmittingChange]);
	useEffect(() => () => onSubmittingChange?.(false), [onSubmittingChange]);
	useEffect(() => () => clearAttachments(), [clearAttachments]);

	const executionContext = projectId ? (
		<ExecutionContextView
			activeAgent={selectedAgentLabel || undefined}
			activeRole="worker"
			baseBranch={projectQuery.data?.defaultBranch ?? cloudProject?.defaultBranch}
			error={!isCloudProject && !isStandalone && projectQuery.isError ? (projectQuery.error instanceof Error ? projectQuery.error.message : t("newTask.configUnavailable")) : undefined}
			labels={executionContextLabels(t)}
			loading={!isCloudProject && !isStandalone && projectQuery.isPending}
			orchestratorAgent={projectQuery.data?.config?.orchestrator?.agent ? selectedAgentLabelFor(projectQuery.data.config.orchestrator.agent, agentCatalog?.agents) : undefined}
			path={projectQuery.data?.path}
			projectName={projectQuery.data?.name ?? cloudProject?.displayName ?? projectId}
			repositories={projectQuery.data ? projectRepositories(projectQuery.data) : cloudRepositories}
			variant="compact"
			workerAgent={projectWorkerAgent ? selectedAgentLabelFor(projectWorkerAgent, agentCatalog?.agents) : undefined}
		/>
	) : undefined;

	const submitTask = async (
		brief: string,
		interfaceMode?: "tui",
		approvalMode?: "bypass-permissions",
	) => {
		if (!projectId || !canSubmit || isSubmitting) return;

		setIsSubmitting(true);
		setError(undefined);
		setFallbackAction(undefined);
		try {
			if (!isCloudProject && selectedAgent) {
				try {
					const completed = await ensureAgentReadiness([selectedAgent], "launch");
					cacheAgentReadiness(queryClient, completed);
				} catch {
					// This check lacks the selected project's cwd and environment, so it
					// is advisory. The project-aware launch path remains authoritative.
				}
			}
			const attachmentPayloads = await toSettledPayload();
			const sessionId = await createTask({
				projectId,
				brief,
				agent: selectedAgent ? (selectedAgent as CreateTaskInput["agent"]) : undefined,
				model: requestedModel,
				// Only explicit Codex picks set this; agent changes reset it, and TUI retries preserve it.
				effort: requestedEffort,
				mode: interfaceMode,
				approvalMode,
				attachments: attachmentPayloads.length > 0 ? attachmentPayloads : undefined,
			});
			if (selectedAgent) {
				const preference: TaskComposerAgentPreference = {
					model: selectedModel ? requestedModel ?? "" : "",
					mode: selectedMode ? requestedModel ?? "" : "",
					...(requestedEffort !== undefined ? { effort: requestedEffort } : {}),
				};
				agentDrafts[selectedAgent] = preference;
				rememberTaskComposerPreference(preferenceContext, selectedAgent, preference);
			}
			onCreated(sessionId);
		} catch (err) {
			const canBypassApprovals =
				err instanceof TaskCreateError &&
				err.code === "SESSION_MODE_UNSUPPORTED" &&
				hasErrorDetail(err.details, "missingCapabilities", "approvals") &&
				hasErrorDetail(err.details, "allowedApprovalModes", "bypass-permissions");
			setFallbackAction(
				canBypassApprovals
					? "bypass-permissions"
					: interfaceMode !== "tui" &&
							err instanceof TaskCreateError &&
							Boolean(err.code && CHAT_PREFLIGHT_CODES.has(err.code))
						? "tui"
						: undefined,
			);
			setError(err instanceof Error ? err.message : t("newTask.unableToStart"));
		} finally {
			setIsSubmitting(false);
		}
	};

	return (
		<TaskComposerView
			autoFocusPrompt={autoFocusTitle}
			canSubmit={canSubmit}
			context={executionContext}
			onPromptChange={handlePromptChange}
			labels={{
				addFile: t("newTask.addFile"),
				effort: t("settings.models.effort"),
				fallbackAction: fallbackAction === "bypass-permissions"
					? t("newTask.startWithoutApprovals", { defaultValue: "Start without approvals" })
					: t("newTask.createAsTui"),
				removeFile: (name) => t("newTask.removeFile", { name }),
				runsWith: t("newTask.runsWith"),
				start: t("newTask.start"),
				starting: t("newTask.starting"),
				task: t("newTask.task"),
				taskPlaceholder,
			}}
			agent={{
				label: t("newTask.agent"),
				placeholder: t("newTask.selectAgent"),
				value: selectedAgent,
				agents: isCloudProject ? cloudAgents : agentCatalog?.agents,
				disabled:
					isSubmitting || (!isCloudProject && agentsQuery.isFetching && agentCatalog === undefined),
				onChange: (value) => {
					if (selectedAgent) {
						agentDrafts[selectedAgent] = {
							model: selectedModel ? requestedModel ?? "" : "",
							mode: selectedMode ? requestedModel ?? "" : "",
					...(requestedEffort !== undefined ? { effort: requestedEffort } : {}),
						};
					}
					setAgent(value);
					setAgentTouched(true);
					setModel("");
					setMode("");
					setEffort("");
					setModelTouched(false);
					setEffortTouched(false);
				},
			}}
			model={{
				agentId: selectedAgent,
				agentLabel: selectedAgentLabel,
				projectId: isStandalone ? "" : (projectId ?? ""),
				disabled: isSubmitting,
				value: selectedModel,
				mode: selectedMode,
				catalog: modelCatalog,
				fetching: modelCatalogQuery.isFetching,
				loading:
					selectedAgent !== "" &&
					modelCatalogQuery.isFetching &&
					modelCatalogQuery.data === undefined,
				onModelChange: (value) => {
					setModel(value);
					setMode("");
					setModelTouched(true);
					// Effort levels are per-model, so a level the newly chosen model
					// does not advertise has to be dropped rather than carried over.
					const nextEfforts =
						modelCatalog?.models?.find((item) => item.id === value)?.efforts ?? [];
					setEffort((current) => (current !== "" && !nextEfforts.includes(current) ? "" : current));
				},
				onModeChange: (value) => {
					setMode(value);
					setModel("");
					setModelTouched(true);
					// A mode replaces the model entirely, so no model vouches for a
					// previously chosen level any more.
					setEffort("");
				},
			}}
			effort={{
				disabled: isSubmitting,
				options: effortOptions,
				value: effort,
				onChange: (value) => {
					setEffort(value);
					setEffortTouched(true);
				},
			}}
			attachments={{
				items: attachments.map(({ id, name, dataUrl }) => ({ id, name, previewUrl: dataUrl })),
				error: attachmentError,
				onAddFiles: (files) => void addFiles(files),
				onRemove: removeAttachment,
			}}
			submission={{
				showFallbackAction: fallbackAction !== undefined,
				error,
				isSubmitting,
				modelWarning,
				onFallbackAction: (brief) =>
					void (fallbackAction === "bypass-permissions"
						? submitTask(brief, undefined, "bypass-permissions")
						: submitTask(brief, "tui")),
				onSubmit: (brief) => void submitTask(brief, requiresTuiFallback ? "tui" : undefined),
			}}
			renderAgentControl={(control) => <DesktopAgentControl {...control} manageAgents={!isCloudProject} />}
			renderEffortControl={(control) => <TaskEffortPicker {...control} defaultEffort={effortModel?.defaultEffort} />}
			renderModelControl={(control) => <TaskModelPicker {...control} onRefresh={refreshSelectedModels}
				showFollowAgentAction={Boolean(catalogDefaultOption || !isConcreteModelID(projectModelOrMode))} />}
			showEffort={!requiresTuiFallback && effortOptions.length > 0}
		/>
	);
}

function selectedAgentLabelFor(agent: string, catalog?: Array<{ id: string; label: string }>): string {
	return catalog?.find((item) => item.id === agent)?.label ?? agent;
}

function TaskEffortPicker({ disabled, label, onChange, options, value, defaultEffort }: TaskComposerEffortControl & { defaultEffort?: string }) {
	const { t } = useTranslation();
	const explicitEffort = value.toLowerCase() === "default" ? "" : value;
	const reportedDefault = defaultEffort && options.includes(defaultEffort) ? defaultEffort : "";
	const effectiveEffort = explicitEffort || reportedDefault;
	const visibleLabel = effectiveEffort ? formatEffortLabel(effectiveEffort) : t("settings.models.effortNotReported");

	return (
		<SettingsOptionMenu
			aria-label={label}
			disabled={disabled}
			value={effectiveEffort}
			options={options.map((option) => ({ value: option, label: formatEffortLabel(option) }))}
			action={explicitEffort && !reportedDefault ? { label: t("settings.models.useAgentEffort"), onSelect: () => onChange("") } : undefined}
			triggerClassName="composer-chip composer-toolbar-option w-full justify-between"
			menuAlign="end"
			renderTrigger={() => (
				<span className="min-w-0 truncate text-control text-foreground" title={visibleLabel}>
					{visibleLabel}
				</span>
			)}
			onChange={onChange}
		/>
	);
}

function formatEffortLabel(value: string): string {
	return value === "xhigh" ? "Extra high" : value.charAt(0).toUpperCase() + value.slice(1);
}

function DesktopAgentControl({ manageAgents, ...control }: TaskComposerAgentControl & { manageAgents: boolean }) {
	return (
		<RequiredAgentField
			{...control}
			manageAgents={manageAgents}
			variant="chip"
			triggerClassName="composer-toolbar-option w-full justify-between"
		/>
	);
}

function TaskModelPicker({
	agentId,
	agentLabel,
	catalog,
	disabled,
	loading,
	value,
	mode,
	onModelChange,
	onModeChange,
	onRefresh,
	showFollowAgentAction,
}: TaskComposerModelControl & { onRefresh: () => Promise<void>; showFollowAgentAction: boolean }) {
	const { t } = useTranslation();

	// No agent selected: there is nothing loading and no model to choose yet, so
	// show a clear "select an agent" placeholder, never a spinner. This returns
	// before the loading check so a no-agent state can never render one.
	if (agentId === "") {
		return (
			<span
				className="composer-chip composer-toolbar-option w-full cursor-not-allowed justify-start opacity-50"
				aria-disabled="true"
				aria-label={t("newTask.model")}
			>
				<span className="truncate text-settings-muted">{t("newTask.selectAgent")}</span>
			</span>
		);
	}

	if (loading) {
		return (
			<span
				className="composer-chip composer-toolbar-option w-full cursor-not-allowed justify-start opacity-50"
				aria-label={t("newTask.model")}
			>
				<span
					className="inline-flex min-w-0 items-center gap-1.5"
					role="status"
					aria-label={t("settings.models.loading")}
					aria-busy="true"
				>
					<Loader2 className="size-icon-sm shrink-0 animate-spin text-settings-muted" aria-hidden="true" />
					<span className="truncate text-settings-muted">{t("settings.models.loading")}</span>
				</span>
			</span>
		);
	}

	if (catalog?.selectionMode === "mode") {
		const options = (catalog.models ?? []).filter((item) => isConcreteModelID(item.id)).map((item) => ({
			value: item.id,
			label: modelChoiceLabel(item),
		}));
		const explicitMode = isConcreteModelID(mode) ? mode : "";
		const defaultMode = catalog.models?.find((item) => item.isDefault && isConcreteModelID(item.id))?.id || "";
		const effectiveMode = explicitMode || defaultMode;
		const visibleModeLabel = options.find((option) => option.value === effectiveMode)?.label ?? t("settings.models.modeNotReported");
		return (
			<SettingsOptionMenu
				aria-label={t("newTask.model")}
				disabled={disabled || options.length === 0}
				value={effectiveMode}
				options={options}
				action={explicitMode && !defaultMode && showFollowAgentAction
					? { label: t("settings.models.useAgentMode"), onSelect: () => onModeChange("") }
					: undefined}
				triggerClassName="composer-chip composer-toolbar-option w-full justify-between"
				menuAlign="start"
				renderTrigger={() => (
					<span className="min-w-0 truncate text-control text-foreground" title={visibleModeLabel}>
						{visibleModeLabel}
					</span>
				)}
				onChange={(value) => onModeChange(value === defaultMode ? "" : value)}
			/>
		);
	}

	const customModelEntry = catalog?.customModelEntry ?? (catalog?.allowCustom ? "direct" : "none");
	const displayModels = (catalog?.models ?? []).map((item) =>
		item.id === "auto" ? { ...item, label: t("settings.models.autoRouteLabel") } : item,
	);
	const selectCatalogModel = (nextModel: string) => {
		onModelChange(nextModel);
	};
	const selectCustomModel = (nextModel: string) => {
		onModelChange(nextModel);
	};

	return (
		<AgentModelCombobox
			key={agentId}
			aria-label={t("newTask.model")}
			value={value}
			models={displayModels}
			allowCustom={catalog?.allowCustom}
			customModelEntry={customModelEntry}
			agentLabel={agentLabel}
			onRefresh={onRefresh}
			refreshing={catalog?.refreshState === "queued" || catalog?.refreshState === "refreshing"}
			lastSuccessAt={catalog?.lastSuccessAt}
			refreshError={catalog?.refreshError}
			retryAt={catalog?.retryAt}
			disabled={disabled || agentId === ""}
			showFollowAgentAction={showFollowAgentAction}
			onChange={selectCatalogModel}
			onCustom={selectCustomModel}
			compact
			recentScope={agentId}
			triggerClassName="composer-chip composer-toolbar-option w-full justify-between"
			menuAlign="start"
			renderTrigger={(label) => <span className="min-w-0 truncate text-control text-foreground" title={label}>{label}</span>}
		/>
	);
}
