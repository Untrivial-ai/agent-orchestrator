import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, type ReactNode } from "react";
import { Info } from "lucide-react";
import { Switch } from "../ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { useTranslation } from "react-i18next";
import { agentModelsQueryKey, agentModelsQueryOptions, refreshAgentModels, revalidateAgentModels, type AgentModelCatalog } from "../../hooks/useAgentModelsQuery";
import { isConcreteModelID, modelChoiceLabel } from "../../lib/agent-model-choices";
import { AgentModelCombobox } from "./AgentModelCombobox";
import { SettingsOptionMenu } from "./SettingsOptionMenu";

export function AgentModelField({
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
	allowCustomFallback = false,
	supportedEfforts,
	followCatalogDefaults = true,
	emptyLabel,
	independentMode = false,
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
	allowCustomFallback?: boolean;
	/** Cloud accepts custom model IDs and validates effort at the harness boundary. */
	supportedEfforts?: readonly string[];
	/** Local catalog defaults may differ from the Cloud worker runtime. */
	followCatalogDefaults?: boolean;
	emptyLabel?: string;
	/** Cloud Cursor can select both a model and a launch mode. */
	independentMode?: boolean;
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
	const isMode = !independentMode && catalog?.selectionMode === "mode";
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
		const defaultMode = followCatalogDefaults ? catalog.models?.find((item) => item.isDefault && isConcreteModelID(item.id))?.id : "agent";
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

	const models = supportedEfforts || !followCatalogDefaults
		? (catalog?.models ?? []).map((item) => ({
			...item,
			...(supportedEfforts ? { efforts: item.efforts ? item.efforts.filter((value) => supportedEfforts.includes(value)) : [...supportedEfforts] } : {}),
			...(!followCatalogDefaults ? { isDefault: false, defaultEffort: undefined } : {}),
		}))
		: catalog?.models ?? [];
	if (supportedEfforts && isConcreteModelID(model) && !models.some((item) => item.id === model)) {
		models.push({ id: model, label: model, efforts: [...supportedEfforts] });
	}
	const customModelEntry = catalog?.customModelEntry ?? (catalog?.allowCustom || allowCustomFallback ? "direct" : "none");
	const refreshCatalog = async () => {
		const refreshed = await refreshAgentModels(agentId, projectId);
		queryClient.setQueryData(agentModelsQueryKey(agentId, projectId), refreshed);
	};
	const selectCatalogModel = (value: string) => {
		onModelChange(value);
		if (!independentMode) onModeChange("");
	};
	const selectCustomModel = (value: string) => {
		onModelChange(value);
		if (!independentMode) onModeChange("");
	};
	return (
		<>
			<div className="min-w-0">
				<div className="min-w-0">
					<AgentModelCombobox
						aria-label={label}
						value={model}
						models={models}
						emptyLabel={emptyLabel}
						allowCustom={catalog?.allowCustom}
						customModelEntry={customModelEntry}
						agentLabel={agentId}
						onRefresh={refreshCatalog}
						refreshing={catalog?.refreshState === "queued" || catalog?.refreshState === "refreshing"}
						refreshError={catalog?.refreshError}
						retryAt={catalog?.retryAt}
						disabled={(query.isFetching && !catalog) || agentId === ""}
						onChange={selectCatalogModel}
						onCustom={selectCustomModel}
						triggerClassName="w-full justify-between"
						compact={agentId === "codex"}
						tuning={{
							effort,
							effortsWithoutModel: supportedEfforts,
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

export function ProjectAgentRoleRow({ label, agent, model }: { label: string; agent: ReactNode; model: ReactNode }) {
	return (
		<div className="grid min-h-16 grid-cols-[6rem_minmax(0,0.85fr)_minmax(0,1.25fr)] items-center gap-3 py-2">
			<span className="text-sm font-medium text-settings-label">{label}</span>
			<div className="min-w-0">{agent}</div>
			<div className="min-w-0">{model}</div>
		</div>
	);
}

export function ProjectAgentRoleHeader() {
	const { t } = useTranslation();
	return (
		<div className="grid grid-cols-[6rem_minmax(0,0.85fr)_minmax(0,1.25fr)] gap-3 py-2 text-xs font-medium text-settings-muted">
			<span />
			<span>{t("settings.project.agent")}</span>
			<span>{t("settings.project.modelOverride")}</span>
		</div>
	);
}

export function ProjectAutoReviewToggle({ checked, onCheckedChange, description }: { checked: boolean; onCheckedChange: (checked: boolean) => void; description?: string }) {
	const { t } = useTranslation();
	return (
		<div className="settings-row-bar">
			<div className="flex shrink-0 items-center gap-1.5">
				<span className="whitespace-nowrap text-sm leading-5 text-settings-label">{t("settings.project.autoReviewToggle")}</span>
				<Tooltip>
					<TooltipTrigger asChild>
						<button
							type="button"
							className="inline-flex size-5 items-center justify-center rounded-md text-settings-muted transition-colors hover:bg-settings-menu-selected hover:text-settings-label focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none"
							aria-label={description ?? t("settings.project.autoReviewDescription")}
						>
							<Info className="size-icon-sm" aria-hidden="true" />
						</button>
					</TooltipTrigger>
					<TooltipContent className="max-w-72 leading-normal" side="top">
						{description ?? t("settings.project.autoReviewDescription")}
					</TooltipContent>
				</Tooltip>
			</div>
			<div className="flex min-w-0 flex-1 items-center justify-end">
				<Switch
					aria-label={t("settings.project.autoReviewToggle")}
					checked={checked}
					id="project-auto-review"
					onCheckedChange={onCheckedChange}
				/>
			</div>
		</div>
	);
}
