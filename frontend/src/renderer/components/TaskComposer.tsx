import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
	TaskComposerView,
	type TaskComposerAgentControl,
	type TaskComposerModelCatalog,
	type TaskComposerModelControl,
} from "@aoagents/product-ui";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { RequiredAgentField } from "./CreateProjectAgentSheet";
import type { components } from "../../api/schema";
import { apiClient, apiErrorCode, apiErrorMessage } from "../lib/api-client";
import { captureRendererEvent } from "../lib/telemetry";
import { agentsQueryKey, agentsQueryOptions, refreshAgentsIfStale } from "../hooks/useAgentsQuery";
import { type FileAttachmentPayload, useFileAttachments } from "../hooks/useFileAttachments";
import { useSettings } from "../hooks/useSettings";
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
	) {
		super(message);
		this.name = "TaskCreateError";
	}
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
	const queryClient = useQueryClient();
	const [prompt, setPrompt] = useState("");
	const [model, setModel] = useState("");
	const [mode, setMode] = useState("");
	const [agent, setAgent] = useState("");
	const [agentTouched, setAgentTouched] = useState(false);
	const [modelTouched, setModelTouched] = useState(false);
	const [isSubmitting, setIsSubmitting] = useState(false);
	const [error, setError] = useState<string | undefined>();
	const [canCreateAsTUI, setCanCreateAsTUI] = useState(false);
	const {
		attachments,
		error: attachmentError,
		addFiles,
		remove: removeAttachment,
		clear: clearAttachments,
		toSettledPayload,
	} = useFileAttachments();
	const createTask = useCallback(
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
						...(input.attachments && input.attachments.length > 0 ? { attachments: input.attachments } : {}),
					},
				});
				if (error) {
					throw new TaskCreateError(
						apiErrorMessage(error, t("newTask.unableToStart")),
						apiErrorCode(error),
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

	const projectQuery = useQuery({
		queryKey: ["project", projectId],
		enabled: Boolean(projectId),
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
	const modelCatalogQuery = useQuery(agentModelsQueryOptions(selectedAgent, projectId ?? ""));
	const revalidationQuery = useQuery({
		queryKey: [
			"agent-model-revalidation",
			selectedAgent,
			projectId ?? "",
			modelCatalogQuery.data?.validatedAt ?? "",
		],
		queryFn: () => revalidateAgentModels(selectedAgent, projectId ?? ""),
		enabled: selectedAgent !== "" && modelCatalogQuery.data?.refreshRecommended === true,
		staleTime: Number.POSITIVE_INFINITY,
		retry: false,
	});
	useEffect(() => {
		if (revalidationQuery.data) {
			queryClient.setQueryData(
				agentModelsQueryKey(selectedAgent, projectId ?? ""),
				revalidationQuery.data,
			);
		}
	}, [projectId, queryClient, revalidationQuery.data, selectedAgent]);
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

	const isDirty = prompt.trim() !== "" || modelTouched || attachments.length > 0;
	useEffect(() => {
		onDirtyChange?.(isDirty);
	}, [isDirty, onDirtyChange]);
	useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);

	useEffect(() => {
		onSubmittingChange?.(isSubmitting);
	}, [isSubmitting, onSubmittingChange]);
	useEffect(() => () => onSubmittingChange?.(false), [onSubmittingChange]);
	useEffect(() => () => clearAttachments(), [clearAttachments]);

	const submitTask = async (interfaceMode?: "tui") => {
		if (!projectId || isSubmitting) return;

		const cleanModel = model.trim();
		const cleanMode = mode.trim();
		const requestedModel =
			modelTouched && (cleanModel !== defaultModelForSelectedAgent || cleanMode !== defaultModeForSelectedAgent)
				? cleanModel || cleanMode || undefined
				: undefined;

		setIsSubmitting(true);
		setError(undefined);
		setCanCreateAsTUI(false);
		try {
			const attachmentPayloads = await toSettledPayload();
			const sessionId = await createTask({
				projectId,
				brief: prompt,
				// The visible selection is authoritative: it is either the user's pick
				// or the resolved default, so spawning names it explicitly.
				agent: selectedAgent ? (selectedAgent as CreateTaskInput["agent"]) : undefined,
				model: requestedModel,
				mode: interfaceMode,
				attachments: attachmentPayloads.length > 0 ? attachmentPayloads : undefined,
			});
			onCreated(sessionId);
		} catch (err) {
			setCanCreateAsTUI(
				interfaceMode !== "tui" &&
					err instanceof TaskCreateError &&
					Boolean(err.code && CHAT_PREFLIGHT_CODES.has(err.code)),
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
			prompt={prompt}
			onPromptChange={setPrompt}
			labels={{
				addFile: t("newTask.addFile"),
				createAsTui: t("newTask.createAsTui"),
				removeFile: (name) => t("newTask.removeFile", { name }),
				runsWith: t("newTask.runsWith"),
				start: t("newTask.start"),
				starting: t("newTask.starting"),
				task: t("newTask.task"),
				taskPlaceholder: t("newTask.taskPlaceholder"),
			}}
			agent={{
				label: t("newTask.agent"),
				placeholder: t("newTask.selectAgent"),
				value: selectedAgent,
				authorized: agentCatalog?.authorized,
				installed: agentCatalog?.installed,
				supported: agentCatalog?.supported,
				disabled: agentsQuery.isFetching && agentCatalog === undefined,
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
				value: model,
				mode,
				catalog: modelCatalog,
				fetching: modelCatalogQuery.isFetching,
				loading:
					selectedAgent !== "" &&
					modelCatalogQuery.isFetching &&
					modelCatalogQuery.data === undefined,
				refreshing: false,
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
				canCreateAsTui: canCreateAsTUI,
				error,
				isSubmitting,
				modelWarning,
				onSubmit: () => void submitTask(requiresTuiFallback ? "tui" : undefined),
				onSubmitAsTui: () => void submitTask("tui"),
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
				disabled={agentId === ""}
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
