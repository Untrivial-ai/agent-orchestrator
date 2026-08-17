import type { components } from "../../api/schema";
import { agentLabel } from "../lib/agent-options";
import { buildRankedAgentOptions } from "../lib/agent-select-options";
import { KNOWN_REVIEWER_HARNESS_IDS } from "../lib/reviewer-harnesses";
import { AgentAvatar } from "./AgentAvatar";
import { AgentSelectMenuItem } from "./settings/AgentSelectMenuItem";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";

const REVIEWER_AGENT_PRIORITY = ["claude-code", "codex", "cursor", "opencode", "muse", "aider"] as const;
const REVIEWER_AGENT_PRIORITY_RANK = new Map<string, number>(
	REVIEWER_AGENT_PRIORITY.map((agent, index) => [agent, index]),
);

const HOST_TRUSTED_REVIEWERS = new Set(["agy", "continue", "devin", "droid", "goose", "kimchi", "kimi", "qwen", "vibe"]);
const USER_APPROVED_REVIEWERS = new Set(["auggie", "autohand", "cline", "crush", "grok"]);

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
	triggerClassName,
	// The same picker serves the project default and a one-off override for the
	// next run, so the caller names it.
	ariaLabel = "Default reviewer agent",
	// Naming what "project default" resolves to matters when nothing has run yet,
	// so the picker can say which agent that actually is.
	defaultHarness,
	defaultOptionLabel,
	defaultTriggerLabel,
	showDefaultOption = true,
	contentAlign = "start",
	disabled = false,
	authorized,
	installed,
	supported,
	excludedHarness,
}: {
	value: string;
	onChange: (value: string) => void;
	triggerClassName?: string;
	ariaLabel?: string;
	defaultHarness?: string;
	defaultOptionLabel?: string;
	defaultTriggerLabel?: string;
	showDefaultOption?: boolean;
	contentAlign?: "start" | "end";
	disabled?: boolean;
	authorized?: components["schemas"]["AgentInfo"][];
	installed?: components["schemas"]["AgentInfo"][];
	supported?: components["schemas"]["AgentInfo"][];
	excludedHarness?: string;
}) {
	// Until the daemon's catalog arrives these entries carry the whole menu, so
	// label them the way the catalog would rather than printing bare ids: without
	// this the same row reads "claude-code" now and "Claude Code" a moment later.
	const fallbackAgents: components["schemas"]["AgentInfo"][] = [...KNOWN_REVIEWER_HARNESS_IDS].map((id) => ({
		id,
		label: agentLabel(id),
	}));
	const filteredSupported = (supported ?? fallbackAgents).filter((a) => KNOWN_REVIEWER_HARNESS_IDS.has(a.id));
	const supportedAgents = filteredSupported.length > 0 ? filteredSupported : fallbackAgents;
	const options = buildRankedAgentOptions({
		supported: supportedAgents,
		installed,
		authorized,
		priorityRank: REVIEWER_AGENT_PRIORITY_RANK,
		fallbackAgents,
	});
	const selectableOptions = options.filter((agent) => agent.id !== excludedHarness);

	const menuOptions = [
		...(showDefaultOption && defaultOptionLabel ? [{ value: "__default__", label: defaultOptionLabel }] : []),
		...selectableOptions.map((agent) => ({ value: agent.id, label: agent.label, disabled: agent.disabled })),
	];
	const selectedValue = value && value !== excludedHarness ? value : "__default__";

	return (
		<SettingsOptionMenu
			aria-label={ariaLabel}
			value={selectedValue}
			options={menuOptions}
			disabled={disabled}
			menuClassName="reviews-agent-menu-surface"
			menuItemClassName="reviews-agent-menu-item"
			menuAlign={contentAlign}
			triggerClassName={triggerClassName}
			onChange={(v) => onChange(v === "__default__" ? "" : v)}
			renderTrigger={(selected) => (
				<>
					{selected && selected.value !== "__default__" ? (
						<AgentAvatar provider={selected.value} className="size-icon-lg" />
					) : defaultHarness ? (
						<AgentAvatar provider={defaultHarness} className="size-icon-lg" />
					) : null}
					<span className={contentAlign === "end" ? "min-w-0 truncate text-right" : "min-w-0 truncate"}>
						{selected && selected.value !== "__default__"
							? selected.label
							: (defaultTriggerLabel ?? defaultOptionLabel ?? defaultHarness)}
					</span>
				</>
			)}
			renderMenuItem={(option, selected) => {
				if (option.value === "__default__") {
					return <AgentSelectMenuItem label={option.label} selected={selected} />;
				}
				const agent = selectableOptions.find((entry) => entry.id === option.value);
				if (!agent) return option.label;
				return (
					<AgentSelectMenuItem
						agentId={agent.id}
						label={agent.label}
						selected={selected}
						status={agent.status}
						statusTone={agent.statusTone}
						disabled={agent.disabled}
					/>
				);
			}}
		/>
	);
}
