import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
	TaskComposerView,
	type TaskComposerAgentControl,
	type TaskComposerModelCatalog,
	type TaskComposerModelControl,
} from "@aoagents/product-ui";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { RequiredAgentField } from "./CreateProjectAgentSheet";
import type { components } from "../../api/schema";
import { apiClient, apiErrorCode, apiErrorMessage } from "../lib/api-client";
import { captureRendererEvent } from "../lib/telemetry";
import { agentsQueryKey, agentsQueryOptions, refreshAgentsIfStale } from "../hooks/useAgentsQuery";
import { type FileAttachmentPayload, useFileAttachments } from "../hooks/useFileAttachments";
import { useSettings } from "../hooks/useSettings";
import { useCloudCp } from "../hooks/useCloudCp";
import { useCloudOrg } from "../hooks/useCloudOrg";
import { useSandboxProviderStore } from "../stores/sandbox-provider-store";
import { cloudSessionsQueryKey, useCloudProjectsQuery } from "../hooks/useWorkspaceQuery";
import {
	agentModelsQueryKey,
	agentModelsQueryOptions,
	revalidateAgentModels,
} from "../hooks/useAgentModelsQuery";
import { cn } from "../lib/utils";
import { AgentModelCombobox } from "./settings/AgentModelCombobox";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";

type Project = components["schemas"]["Project"];
type DelegateAgent = components["schemas"]["DelegateTaskRequest"]["agent"];

type CreateTaskInput = {
	projectId: string;
	brief: string;
	agent?: DelegateAgent;
	model?: string;
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
	const [agent, setAgent] = useState("");
	const [agentTouched, setAgentTouched] = useState(false);
	const [modelTouched, setModelTouched] = useState(false);
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
	const isCloudProject =
		Boolean(projectId) && (cloudProjects.data ?? []).some((project) => project.id === projectId);
	// A cloud project is unknown to the local daemon, so the local model catalog
	// must be queried agent-level (no project scope); otherwise the request 404s
	// and the model dropdown spins forever. Local projects keep their scope.
	const modelsProjectId = isCloudProject ? "" : (projectId ?? "");

	const createCloudTask = useCallback(
		async (input: CreateTaskInput): Promise<string> => {
			void captureRendererEvent("ao.renderer.task_create_requested", { project_id: input.projectId });
			if (!cloudOrg?.id) throw new Error(t("newTask.unableToStart"));
			try {
				const { session } = await cloudClient.createSession(cloudOrg.id, {
					projectId: input.projectId,
					kind: "worker",
					harness: input.agent ?? "claude-code",
					displayName: input.brief.trim().slice(0, 80) || (input.agent ?? "claude-code"),
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
						model: input.model,
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
				void queryClient.invalidateQueries({ queryKey: agentsQueryKey });
				throw err instanceof Error ? err : new Error(t("newTask.unableToStart"));
			}
		},
		[queryClient, t],
	);

	const createTask = useCallback(
		(input: CreateTaskInput): Promise<string> =>
			isCloudProject ? createCloudTask(input) : createLocalTask(input),
		[isCloudProject, createCloudTask, createLocalTask],
	);

	const projectQuery = useQuery({
		// A cloud project lives in the control plane, not the local daemon, so this
		// local lookup would 404 (PROJECT_NOT_FOUND); skip it for cloud projects.
		queryKey: ["project", projectId],
		enabled: Boolean(projectId) && !isCloudProject,
		queryFn: async () => {
			const { data, error: apiError } = await apiClient.GET("/api/v1/projects/{id}", {
				params: { path: { id: projectId ?? "" } },
			});
			if (apiError) throw new Error(apiErrorMessage(apiError));
			if (data?.status !== "ok") throw new Error(t("newTask.configUnavailable"));
			return data.project as Project;
		},
	});
	const agentsQuery = useQuery(agentsQueryOptions);
	const { settings } = useSettings();
	// Freshen the inventory on open so a just-installed or just-authenticated agent
	// is present without the user asking for it.
	useEffect(() => {
		void refreshAgentsIfStale().then((next) => {
			if (next) queryClient.setQueryData(agentsQueryKey, next);
		});
	}, [queryClient]);
	// The composer preselects the agent and model a spawn would actually use
	// instead of parking the controls on a "default" label the user has to
	// remember. Both resolved values remain directly editable.
	const projectWorkerAgent = projectQuery.data?.config?.worker?.agent ?? "";
	const globalDefaultAgent = projectQuery.data?.agent ?? "";
	const defaultWorkerAgent = projectWorkerAgent || globalDefaultAgent;
	const selectedAgent = agent || defaultWorkerAgent;
	const defaultWorkerModel =
		projectQuery.data?.config?.worker?.agentConfig?.model ?? projectQuery.data?.config?.agentConfig?.model ?? "";
	const defaultWorkerMode =
		projectQuery.data?.config?.worker?.agentConfig?.mode ?? projectQuery.data?.config?.agentConfig?.mode ?? "";
	const projectModelForSelectedAgent = selectedAgent === defaultWorkerAgent ? defaultWorkerModel : "";
	const projectModeForSelectedAgent = selectedAgent === defaultWorkerAgent ? defaultWorkerMode : "";
	const agentCatalog = agentsQuery.data;

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
				models: modelCatalogQuery.data.models,
				selectionMode: modelCatalogQuery.data.selectionMode,
			}
		: undefined;
	const catalogDefaultOption = modelCatalogQuery.data?.models?.find((item) => item.isDefault)?.id ?? "";
	const catalogUsesModes = modelCatalogQuery.data?.selectionMode === "mode";
	const defaultModelForSelectedAgent =
		projectModelForSelectedAgent || (catalogUsesModes ? "" : catalogDefaultOption);
	const defaultModeForSelectedAgent = projectModeForSelectedAgent || (catalogUsesModes ? catalogDefaultOption : "");

	const selectedAgentLabel =
		agentCatalog?.supported?.find((item) => item.id === selectedAgent)?.label || selectedAgent;
	const requiresTuiFallback =
		selectedAgent !== "" &&
		settings?.defaultSessionMode === "chat" &&
		!settings.chatHarnesses.includes(selectedAgent);

	useEffect(() => {
		if (!agentTouched) setAgent(defaultWorkerAgent);
	}, [agentTouched, defaultWorkerAgent]);
	useEffect(() => {
		if (!modelTouched) {
			setModel(defaultModelForSelectedAgent);
			setMode(defaultModeForSelectedAgent);
		}
	}, [defaultModelForSelectedAgent, defaultModeForSelectedAgent, modelTouched]);

	const isDirty = isPromptDirty || modelTouched || attachments.length > 0;
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

	const submitTask = async (
		brief: string,
		interfaceMode?: "tui",
		approvalMode?: "bypass-permissions",
	) => {
		if (!projectId || isSubmitting) return;

		const cleanModel = model.trim();
		const cleanMode = mode.trim();
		const requestedModel =
			modelTouched && (cleanModel !== defaultModelForSelectedAgent || cleanMode !== defaultModeForSelectedAgent)
				? cleanModel || cleanMode || undefined
				: undefined;

		setIsSubmitting(true);
		setError(undefined);
		setFallbackAction(undefined);
		try {
			const attachmentPayloads = await toSettledPayload();
			const sessionId = await createTask({
				projectId,
				brief,
				// The visible selection is authoritative: it is either the user's pick
				// or the resolved default, so spawning names it explicitly.
				agent: selectedAgent ? (selectedAgent as CreateTaskInput["agent"]) : undefined,
				model: requestedModel,
				mode: interfaceMode,
				approvalMode,
				attachments: attachmentPayloads.length > 0 ? attachmentPayloads : undefined,
			});
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
			canSubmit={Boolean(projectId)}
			onPromptChange={handlePromptChange}
			labels={{
				addFile: t("newTask.addFile"),
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
				authorized: agentCatalog?.authorized,
				installed: agentCatalog?.installed,
				supported: agentCatalog?.supported,
				disabled: isSubmitting || (agentsQuery.isFetching && agentCatalog === undefined),
				onChange: (value) => {
					setAgent(value);
					setAgentTouched(true);
					setModel("");
					setMode("");
					setModelTouched(false);
				},
			}}
			model={{
				agentId: selectedAgent,
				agentLabel: selectedAgentLabel,
				projectId: projectId ?? "",
				disabled: isSubmitting,
				value: model,
				mode,
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
				},
				onModeChange: (value) => {
					setMode(value);
					setModel("");
					setModelTouched(true);
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
			renderAgentControl={(control) => <DesktopAgentControl {...control} />}
			renderModelControl={(control) => <TaskModelPicker {...control} />}
		/>
	);
}

function DesktopAgentControl(control: TaskComposerAgentControl) {
	return (
		<RequiredAgentField
			{...control}
			variant="chip"
			triggerClassName="composer-toolbar-option w-full justify-between"
		/>
	);
}

function TaskModelPicker({
	id,
	agentId,
	agentLabel,
	catalog,
	disabled,
	fetching,
	loading,
	value,
	mode,
	onModelChange,
	onModeChange,
}: TaskComposerModelControl) {
	const { t } = useTranslation();
	const [customAgentId, setCustomAgentId] = useState<string | null>(null);

	// Says what happens with no override, rather than labelling it "Agent default".
	const noOverrideLabel = agentLabel
		? t("newTask.letAgentChoose", { agent: agentLabel })
		: t("settings.models.agentDefault");

	if (loading) {
		return (
			<span
				className="composer-chip composer-toolbar-option w-full cursor-not-allowed justify-start opacity-50"
				role="status"
				aria-label={t("settings.models.loading")}
				aria-busy="true"
			>
				<Loader2 className="size-icon-sm shrink-0 animate-spin text-settings-muted" aria-hidden="true" />
				<span className="truncate text-settings-muted">{t("settings.models.loading")}</span>
			</span>
		);
	}

	if (catalog?.selectionMode === "mode") {
		const options = [
			{ value: "__default__", label: noOverrideLabel },
			...(catalog.models ?? []).map((item) => ({ value: item.id, label: item.label })),
		];
		const visibleModeLabel = mode ? (options.find((option) => option.value === mode)?.label ?? mode) : noOverrideLabel;
		return (
			<SettingsOptionMenu
				aria-label={t("newTask.model")}
				disabled={disabled}
				value={mode || "__default__"}
				options={options}
				triggerClassName="composer-chip composer-toolbar-option w-full justify-between"
				menuAlign="start"
				renderTrigger={() => (
					<span className="min-w-0 truncate text-control text-foreground" title={visibleModeLabel}>
						{visibleModeLabel}
					</span>
				)}
				onChange={(nextMode) => onModeChange(nextMode === "__default__" ? "" : nextMode)}
			/>
		);
	}

	const hasCatalog = catalog?.selectionMode === "catalog" && (catalog.models?.length ?? 0) > 0;
	const modelIsInCatalog = catalog?.models?.some((item) => item.id === value) ?? false;
	const displayModels = (catalog?.models ?? []).map((item) =>
		item.id === "auto" ? { ...item, label: t("settings.models.autoRouteLabel") } : item,
	);
	const showCustomInput = hasCatalog && (customAgentId === agentId || (value !== "" && !modelIsInCatalog));
	const selectCatalogModel = (nextModel: string) => {
		setCustomAgentId(null);
		onModelChange(nextModel);
	};
	const selectCustomModel = (nextModel: string) => {
		setCustomAgentId(agentId);
		onModelChange(nextModel);
	};

	if (hasCatalog && !showCustomInput) {
		return (
			<AgentModelCombobox
				key={agentId}
				aria-label={t("newTask.model")}
				value={value}
				models={displayModels}
				allowCustom={catalog.allowCustom}
				disabled={disabled}
				emptyLabel={noOverrideLabel}
				onChange={selectCatalogModel}
				onCustom={selectCustomModel}
				compact
				recentScope={agentId}
				triggerClassName="composer-chip composer-toolbar-option w-full justify-between"
				menuAlign="start"
				renderTrigger={(label) => {
					const visibleLabel = value ? label : noOverrideLabel;
					return (
						<span className="min-w-0 truncate text-control text-foreground" title={visibleLabel}>
							{visibleLabel}
						</span>
					);
				}}
			/>
		);
	}

	// Free-text agents keep an input inside the same stable model track.
	return (
		<span className="inline-flex w-full min-w-0 items-center gap-1.5">
			<input
				id={id}
				aria-label={t("newTask.model")}
				className={cn(
					"composer-chip composer-toolbar-option min-w-0 flex-1 text-control placeholder:text-passive disabled:cursor-not-allowed disabled:opacity-50",
					!hasCatalog && "rounded-r-md!",
				)}
				value={value}
				disabled={disabled || agentId === ""}
				onChange={(event) => onModelChange(event.target.value)}
				placeholder={fetching ? t("settings.models.loading") : noOverrideLabel}
			/>
			{hasCatalog && (
				<AgentModelCombobox
					key={agentId}
					aria-label={t("settings.models.optionsAria", { label: t("newTask.model") })}
					value={value}
					models={displayModels}
					allowCustom={catalog.allowCustom}
					disabled={disabled}
					emptyLabel={noOverrideLabel}
					onChange={selectCatalogModel}
					onCustom={selectCustomModel}
					compact
					recentScope={agentId}
					triggerLabel={t("settings.models.browse")}
					triggerClassName="shrink-0"
				/>
			)}
		</span>
	);
}
