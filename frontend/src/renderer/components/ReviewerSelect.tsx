import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { agentModelsQueryOptions, type AgentModelCatalog } from "../hooks/useAgentModelsQuery";
import { agentLabel } from "../lib/agent-options";
import {
	buildRankedAgentOptions,
	type AgentInfo,
	type RankedAgentOption,
	unknownAgentReadiness,
} from "../lib/agent-select-options";
import { KNOWN_REVIEWER_HARNESS_IDS } from "../lib/reviewer-harnesses";
import { cn } from "../lib/utils";
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

const HOST_TRUSTED_REVIEWERS = new Set(["agy", "continue", "devin", "droid", "goose", "kimchi", "kimi", "qwen", "vibe"]);
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
	ariaLabel = "Default reviewer agent",
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
		if (agent.id === excludedHarness) return false;
		if (showDefaultOption && defaultHarness && agent.id === defaultHarness) return false;
		return true;
	});
	const effectiveHarness = value || defaultHarness || "";
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
	const selectedModelLabel = modelOrModeLabel(triggerCatalog.data, model, mode, t("settings.models.agentDefault"));
	const triggerLabel = [value ? agentLabel(value) : (defaultTriggerLabel ?? defaultOptionLabel ?? defaultHarness), selectedModelLabel]
		.filter(Boolean)
		.join(" · ");

	return (
		<OptionMenu open={menuOpen} onOpenChange={setMenuOpen}>
			<OptionMenuTrigger
				className={cn(
					"w-auto min-w-0 max-w-full justify-between gap-2 px-2 text-left",
					contentAlign === "end" && "justify-end text-right",
					triggerClassName,
				)}
				aria-label={ariaLabel}
				disabled={disabled}
			>
				<span className="flex min-w-0 items-center gap-2">
					{effectiveHarness ? <AgentAvatar provider={effectiveHarness} className="size-icon-lg shrink-0" /> : null}
					<span className={cn("min-w-0 truncate", contentAlign === "end" && "text-right")}>{triggerLabel}</span>
				</span>
			</OptionMenuTrigger>
			<OptionMenuContent align={contentAlign === "end" ? "end" : "start"} className="reviews-agent-menu-surface w-[18rem]">
				{showDefaultOption && defaultOptionLabel ? (
					<ReviewerHarnessOption
						agent={{ id: "__default__", label: defaultOptionLabel, disabled: false, status: "", statusTone: "success" }}
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
	const selectDefault = () => onSelect(persistHarness, {});

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

	const hasChoices = hasModelChoices(catalog);
	const catalogKnown = catalogQuery.data !== undefined || catalogQuery.isFetched;

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
				<OptionMenuItem
					onSelect={selectDefault}
					active={isCurrentDefaultSelection}
				>
					<span className="flex min-w-0 items-center justify-between gap-3">
						<span>{t("settings.models.agentDefault")}</span>
						{isCurrentDefaultSelection ? <Check aria-hidden="true" className="size-4" /> : null}
					</span>
				</OptionMenuItem>
				{!catalogKnown ? (
					<OptionMenuItem disabled>{t("common.loading", { defaultValue: "Loading…" })}</OptionMenuItem>
				) : null}
				{modelOptions(catalog).map((option) => {
					const selected =
						isCurrentHarness &&
						((option.kind === "mode" && currentMode === option.value) ||
							(option.kind === "model" && currentModel === option.value));
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
	return (catalog.models ?? []).map((item) => ({
		kind: catalog.selectionMode === "mode" ? "mode" : "model",
		label: item.label,
		value: item.id,
	}));
}

function modelOrModeLabel(catalog: AgentModelCatalog | undefined, model: string, mode: string, emptyLabel: string): string {
	const value = mode || model;
	if (!value) return emptyLabel;
	const match = catalog?.models?.find((item) => item.id === value);
	return match?.label || value;
}
