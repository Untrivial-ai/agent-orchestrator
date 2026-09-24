import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { agentModelsQueryOptions, type AgentModelCatalog } from "../hooks/useAgentModelsQuery";
import { agentLabel } from "../lib/agent-options";
import {
	buildRankedAgentOptions,
	isReadyAgent,
	type AgentInfo,
	type RankedAgentOption,
	unknownAgentReadiness,
} from "../lib/agent-select-options";
import { KNOWN_REVIEWER_HARNESS_IDS } from "../lib/reviewer-harnesses";
import { cn } from "../lib/utils";
import { useAgentManagementMenu } from "../hooks/useAgentManagementMenu";
import { AgentAvatar } from "./AgentAvatar";
import { AgentSelectMenuItem } from "./settings/AgentSelectMenuItem";
import {
	OptionMenu,
	OptionMenuContent,
	OptionMenuItem,
	OptionMenuSub,
	OptionMenuSubContent,
	OptionMenuSubTrigger,
	OptionMenuTrigger,
} from "./ui/option-menu";

const REVIEWER_AGENT_PRIORITY = ["claude-code", "codex", "cursor", "opencode", "muse", "aider"] as const;
const REVIEWER_AGENT_PRIORITY_RANK = new Map<string, number>(
	REVIEWER_AGENT_PRIORITY.map((agent, index) => [agent, index]),
);

const HOST_TRUSTED_REVIEWERS = new Set(["agy", "devin", "droid", "kimchi", "kimi"]);
const USER_APPROVED_REVIEWERS = new Set(["auggie", "autohand", "cline", "crush", "grok"]);

type ReviewerAgentConfig = components["schemas"]["AgentConfig"];

export function reviewerTrustWarning(harness: string): string | null {
	if (HOST_TRUSTED_REVIEWERS.has(harness)) {
		return "Experimental host-trusted reviewer: this agent is not OS-isolated and may retain shell, plugin, editor, and network access.";
	}
	if (USER_APPROVED_REVIEWERS.has(harness)) {
		return "Experimental user-approved reviewer: AO keeps the agent's native permission prompts enabled; review execution may pause for your approval.";
	}
	return null;
}

export function ReviewerSelect({
	value,
	onChange,
	onConfigChange,
	model = "",
	mode = "",
	projectId,
	triggerClassName,
	ariaLabel,
	defaultHarness,
	defaultOptionLabel,
	defaultTriggerLabel,
	showDefaultOption = true,
	contentAlign = "start",
	disabled = false,
	agents,
	excludedHarness,
}: {
	value: string;
	onChange: (value: string) => void;
	onConfigChange?: (harness: string, config: ReviewerAgentConfig) => void;
	model?: string;
	mode?: string;
	projectId?: string;
	triggerClassName?: string;
	ariaLabel?: string;
	defaultHarness?: string;
	defaultOptionLabel?: string;
	defaultTriggerLabel?: string;
	showDefaultOption?: boolean;
	contentAlign?: "start" | "end";
	disabled?: boolean;
	agents?: components["schemas"]["AgentReadinessSnapshot"][];
	excludedHarness?: string;
}) {
	const { t } = useTranslation();
	const reviewerAriaLabel = ariaLabel ?? t("settings.project.defaultReviewer");
	const queryClient = useQueryClient();
	const [menuOpen, setMenuOpen] = useState(false);
	// Until the daemon's catalog arrives these entries carry the whole menu, so
	// label them the way the catalog would rather than printing bare ids: without
	// this the same row reads "claude-code" now and "Claude Code" a moment later.
	const fallbackAgents: AgentInfo[] = [...KNOWN_REVIEWER_HARNESS_IDS].map(
		(id) => unknownAgentReadiness(id, agentLabel(id)),
	);
	const filteredSupported = (agents ?? fallbackAgents).filter((a) => KNOWN_REVIEWER_HARNESS_IDS.has(a.id));
	const supportedAgents = filteredSupported.length > 0 ? filteredSupported : fallbackAgents;
	const options = buildRankedAgentOptions({
		agents: supportedAgents,
		priorityRank: REVIEWER_AGENT_PRIORITY_RANK,
		fallbackAgents,
	});
	const selectableOptions = options.filter((agent) => {
		if (agents !== undefined && !isReadyAgent(agent)) return false;
		if (agent.id === excludedHarness) return false;
		if (showDefaultOption && defaultHarness && agent.id === defaultHarness) return false;
		return true;
	});
	const catalogDefaultLabel = options.find((agent) => agent.id === defaultHarness)?.label;
	const defaultHarnessLabel = catalogDefaultLabel && catalogDefaultLabel !== defaultHarness ? catalogDefaultLabel : agentLabel(defaultHarness ?? "");
	const effectiveHarness = value || defaultHarness || "";
	const needsSetup = agents !== undefined && Boolean(effectiveHarness && !options.some((agent) => agent.id === effectiveHarness && isReadyAgent(agent)));
	const management = useAgentManagementMenu(needsSetup ? effectiveHarness : undefined);
	const menuProjectID = projectId ?? "";
	const triggerCatalog = useQuery(agentModelsQueryOptions(effectiveHarness, menuProjectID));

	useEffect(() => {
		if (!menuOpen) return;
		const harnesses = new Set<string>();
		if (defaultHarness) harnesses.add(defaultHarness);
		for (const agent of selectableOptions) {
			harnesses.add(agent.id);
		}
		for (const harness of harnesses) {
			if (!harness) continue;
			void queryClient.prefetchQuery(agentModelsQueryOptions(harness, menuProjectID));
		}
	}, [defaultHarness, menuOpen, menuProjectID, queryClient, selectableOptions]);
	const selectedModelLabel = modelOrModeLabel(triggerCatalog.data, model, mode, t("settings.models.modelNotReported"));
	const triggerLabel = [value ? agentLabel(value) : (defaultHarness ? defaultHarnessLabel : (defaultTriggerLabel ?? defaultOptionLabel)), selectedModelLabel]
		.filter(Boolean)
		.join(" · ");

	return (
		<OptionMenu open={menuOpen} onOpenChange={setMenuOpen}>
			<OptionMenuTrigger
				ref={management.triggerRef}
				className={cn(
					"w-auto min-w-0 max-w-full justify-between gap-2 px-2 text-left",
					contentAlign === "end" && "justify-end text-right",
					triggerClassName,
				)}
				aria-label={reviewerAriaLabel}
				disabled={disabled}
			>
				<span className="flex min-w-0 items-center gap-2">
					{effectiveHarness ? <AgentAvatar provider={effectiveHarness} className="size-icon-lg shrink-0" /> : null}
					<span className={cn("min-w-0 truncate", contentAlign === "end" && "text-right")}>{triggerLabel}</span>
					{needsSetup && <span className="text-xs text-muted-foreground">{t("agentSelector.needsSetup")}</span>}
				</span>
			</OptionMenuTrigger>
			<OptionMenuContent onCloseAutoFocus={management.onCloseAutoFocus} align={contentAlign === "end" ? "end" : "start"} className="reviews-agent-menu-surface w-[18rem]">
				{showDefaultOption && (defaultHarness || defaultOptionLabel) ? (
					<ReviewerHarnessOption
						agent={{ id: "__default__", label: defaultHarness ? defaultHarnessLabel : (defaultOptionLabel ?? ""), disabled: false, status: "", statusTone: "success" }}
						currentHarness={value}
						currentModel={model}
						currentMode={mode}
						onSelect={(nextHarness, nextConfig) => {
							setMenuOpen(false);
							onChange(nextHarness);
							onConfigChange?.(nextHarness, nextConfig);
						}}
						projectId={menuProjectID}
						resolvedHarness={defaultHarness}
						persistHarness=""
						closeMenu={() => setMenuOpen(false)}
					/>
				) : null}
				{selectableOptions.map((agent) => (
					<ReviewerHarnessOption
						key={agent.id}
						agent={agent}
						currentHarness={value}
						currentModel={model}
						currentMode={mode}
						onSelect={(nextHarness, nextConfig) => {
							setMenuOpen(false);
							onChange(nextHarness);
							onConfigChange?.(nextHarness, nextConfig);
						}}
						projectId={menuProjectID}
						resolvedHarness={agent.id}
						persistHarness={agent.id}
						closeMenu={() => setMenuOpen(false)}
					/>
				))}
				{selectableOptions.length === 0 && !(showDefaultOption && (defaultHarness || defaultOptionLabel)) && <p className="px-3 py-2 text-xs text-muted-foreground">{t("agentSelector.noneReady")}</p>}
				<OptionMenuItem className="mt-1 border-t border-border" onSelect={management.requestManagement}>{t("agentSelector.manage")}</OptionMenuItem>
			</OptionMenuContent>
		</OptionMenu>
	);
}

function ReviewerHarnessOption({
	agent,
	currentHarness,
	currentModel,
	currentMode,
	onSelect,
	projectId,
	resolvedHarness,
	persistHarness,
	closeMenu,
}: {
	agent: Pick<RankedAgentOption, "id" | "label" | "status" | "statusTone" | "disabled">;
	currentHarness: string;
	currentModel: string;
	currentMode: string;
	onSelect: (harness: string, config: ReviewerAgentConfig) => void;
	projectId: string;
	resolvedHarness?: string;
	persistHarness: string;
	closeMenu: () => void;
}) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const catalogQuery = useQuery({
		...agentModelsQueryOptions(resolvedHarness ?? "", projectId),
		enabled: false,
	});
	const catalog = catalogQuery.data;
	const effectiveCurrentHarness =
		currentHarness || (persistHarness === "" ? (resolvedHarness ?? "") : "");
	const effectivePersistHarness = persistHarness || resolvedHarness || "";
	const isCurrentHarness = effectiveCurrentHarness !== "" && effectiveCurrentHarness === effectivePersistHarness;
	const isCurrentDefaultSelection = isCurrentHarness && currentModel === "" && currentMode === "";
	const hasChoices = hasModelChoices(catalog);
	const catalogKnown = catalogQuery.data !== undefined || catalogQuery.isFetched;
	const defaultModel = catalog?.models?.find((item) => item.isDefault && item.id.toLowerCase() !== "default")?.id;
	const selectDefault = () => onSelect(persistHarness, defaultModel
		? catalog?.selectionMode === "mode" ? { mode: defaultModel } : { model: defaultModel }
		: {});

	if (!resolvedHarness) {
		return (
			<OptionMenuItem onSelect={() => onSelect("", {})} active={currentHarness === "" && currentModel === "" && currentMode === ""}>
				<span className="flex min-w-0 items-center justify-between gap-3">
					<span>{agent.label}</span>
					{currentHarness === "" && currentModel === "" && currentMode === "" ? <Check aria-hidden="true" className="size-4" /> : null}
				</span>
			</OptionMenuItem>
		);
	}

	if (catalogKnown && !hasChoices) {
		return (
			<>
				<OptionMenuItem
					onSelect={selectDefault}
					active={isCurrentDefaultSelection}
					className="reviews-agent-menu-item"
					disabled={agent.disabled}
				>
					<AgentSelectMenuItem
						agentId={resolvedHarness}
						label={agent.label}
						selected={isCurrentHarness}
						status={agent.status}
						statusTone={agent.statusTone}
						disabled={agent.disabled}
					/>
				</OptionMenuItem>
			</>
		);
	}

	return (
		<OptionMenuSub open={open} onOpenChange={setOpen}>
			<OptionMenuSubTrigger
				disabled={agent.disabled}
				aria-label={agent.status ? `${agent.label}${agent.status}` : agent.label}
				onClick={(event) => {
					if (!isCurrentHarness) {
						event.preventDefault();
						closeMenu();
						selectDefault();
					}
				}}
			>
				<AgentSelectMenuItem
					agentId={resolvedHarness}
					label={agent.label}
					selected={isCurrentHarness}
					status={agent.status}
					statusTone={agent.statusTone}
					disabled={agent.disabled}
				/>
			</OptionMenuSubTrigger>
			<OptionMenuSubContent className="w-[15rem]">
				{!catalogKnown ? (
					<OptionMenuItem disabled>{t("common.loading", { defaultValue: "Loading…" })}</OptionMenuItem>
				) : null}
				{modelOptions(catalog).map((option) => {
					const selected =
						isCurrentHarness &&
						((option.kind === "mode" && currentMode === option.value) ||
							(option.kind === "model" && currentModel === option.value) ||
							(isCurrentDefaultSelection && option.value === defaultModel));
					return (
						<OptionMenuItem
							key={`${option.kind}:${option.value}`}
							onSelect={() => onSelect(persistHarness, option.kind === "mode" ? { mode: option.value } : { model: option.value })}
							active={selected}
						>
							<span className="flex min-w-0 items-center justify-between gap-3">
								<span className="min-w-0 truncate">{option.label}</span>
								{selected ? <Check aria-hidden="true" className="size-4" /> : null}
							</span>
						</OptionMenuItem>
					);
				})}
			</OptionMenuSubContent>
		</OptionMenuSub>
	);
}

function hasModelChoices(catalog?: AgentModelCatalog): boolean {
	return modelOptions(catalog).length > 0;
}

function modelOptions(catalog?: AgentModelCatalog): Array<{ kind: "model" | "mode"; label: string; value: string }> {
	if (!catalog) return [];
	if (catalog.selectionMode !== "catalog" && catalog.selectionMode !== "mode" && catalog.selectionMode !== "text") return [];
	return (catalog.models ?? []).filter((item) => item.id && item.id.toLowerCase() !== "default").map((item) => ({
		kind: catalog.selectionMode === "mode" ? "mode" : "model",
		label: /^default(?:\s*\([^)]*\))?$/i.test(item.label.trim()) ? item.id : item.label,
		value: item.id,
	}));
}

function modelOrModeLabel(catalog: AgentModelCatalog | undefined, model: string, mode: string, emptyLabel: string): string {
	const configured = mode || model;
	const value = configured.toLowerCase() === "default" ? "" : configured;
	const effective = value || catalog?.models?.find((item) => item.isDefault && item.id.toLowerCase() !== "default")?.id || "";
	if (!effective) return emptyLabel;
	return modelOptions(catalog).find((item) => item.value === effective)?.label || effective;
}
