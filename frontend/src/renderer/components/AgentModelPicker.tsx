import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	agentModelsQueryKey,
	agentModelsQueryOptions,
	revalidateAgentModels,
	type AgentModelCatalog,
} from "../hooks/useAgentModelsQuery";
import { cn } from "../lib/utils";
import { AgentModelCombobox } from "./settings/AgentModelCombobox";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";

type AgentModelPickerProps = {
	agentId: string;
	agentLabel: string;
	projectId: string;
	value: string;
	mode: string;
	disabled?: boolean;
	onModelChange: (value: string) => void;
	onModeChange: (value: string) => void;
	onWarningChange: (warning: string | undefined) => void;
};

export function AgentModelPicker({
	agentId,
	agentLabel,
	projectId,
	value,
	mode,
	disabled = false,
	onModelChange,
	onModeChange,
	onWarningChange,
}: AgentModelPickerProps) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [customAgentId, setCustomAgentId] = useState<string | null>(null);
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
	const warning =
		(revalidationQuery.isError
			? revalidationQuery.error instanceof Error
				? revalidationQuery.error.message
				: t("settings.models.validateFailed")
			: undefined) ??
		catalog?.warning ??
		(query.isError ? (query.error instanceof Error ? query.error.message : t("settings.models.loadFailed")) : undefined);
	useEffect(() => {
		onWarningChange(warning);
	}, [onWarningChange, warning]);
	useEffect(() => () => onWarningChange(undefined), [onWarningChange]);

	const noOverrideLabel = agentLabel
		? t("newTask.letAgentChoose", { agent: agentLabel })
		: t("settings.models.agentDefault");
	const catalogLoading = agentId !== "" && query.isFetching && catalog === undefined;

	if (catalogLoading) {
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
				disabled={disabled}
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

	return (
		<span className="inline-flex w-full min-w-0 items-center gap-1.5">
			<input
				aria-label={t("newTask.model")}
				className={cn(
					"composer-chip composer-toolbar-option min-w-0 flex-1 text-control placeholder:text-passive disabled:cursor-not-allowed disabled:opacity-50",
					!hasCatalog && "rounded-r-md!",
				)}
				value={value}
				disabled={disabled || agentId === ""}
				onChange={(event) => onModelChange(event.target.value)}
				placeholder={query.isFetching ? t("settings.models.loading") : noOverrideLabel}
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
