import type { components } from "../../api/schema";
import { buildRankedAgentOptions } from "../lib/agent-select-options";
import { KNOWN_REVIEWER_HARNESS_IDS } from "../lib/reviewer-harnesses";
import { AgentAvatar } from "./AgentAvatar";
import { AgentSelectMenuItem } from "./settings/AgentSelectMenuItem";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";

const REVIEWER_AGENT_PRIORITY = ["claude-code", "codex", "cursor", "opencode", "muse", "aider"] as const;
const REVIEWER_AGENT_PRIORITY_RANK = new Map<string, number>(
	REVIEWER_AGENT_PRIORITY.map((agent, index) => [agent, index]),
);

const HOST_TRUSTED_REVIEWERS = new Set(["agy", "devin", "droid", "kimchi", "kimi", "qwen"]);
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
	disabled = false,
	authorized,
	installed,
	supported,
}: {
	value: string;
	onChange: (value: string) => void;
	triggerClassName?: string;
	ariaLabel?: string;
	defaultHarness?: string;
	defaultOptionLabel: string;
	defaultTriggerLabel?: string;
	disabled?: boolean;
	authorized?: components["schemas"]["AgentInfo"][];
	installed?: components["schemas"]["AgentInfo"][];
	supported?: components["schemas"]["AgentInfo"][];
}) {
	const fallbackAgents: components["schemas"]["AgentInfo"][] = [...KNOWN_REVIEWER_HARNESS_IDS].map((id) => ({
		id,
		label: id,
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

	// The trigger shows the agent that will actually run, since "project default"
	// names the setting rather than answering the question. The menu keeps the
	// longer wording, where there is room for it.
	const menuOptions = [
		{ value: "__default__", label: defaultOptionLabel },
		...options.map((agent) => ({ value: agent.id, label: agent.label, disabled: agent.disabled })),
	];
	const selectedValue = value || "__default__";

	return (
		<SettingsOptionMenu
			aria-label={ariaLabel}
			value={selectedValue}
			options={menuOptions}
			disabled={disabled}
			menuClassName="reviews-agent-menu-surface"
			menuItemClassName="reviews-agent-menu-item"
			menuAlign="start"
			triggerClassName={triggerClassName}
			onChange={(v) => onChange(v === "__default__" ? "" : v)}
			renderTrigger={(selected) => (
				<>
					{selected && selected.value !== "__default__" ? (
						<AgentAvatar provider={selected.value} className="size-icon-lg" />
					) : defaultHarness ? (
						<AgentAvatar provider={defaultHarness} className="size-icon-lg" />
					) : null}
					<span className="min-w-0 truncate">
						{selected && selected.value !== "__default__" ? selected.label : (defaultTriggerLabel ?? defaultOptionLabel)}
					</span>
				</>
			)}
			renderMenuItem={(option, selected) => {
				if (option.value === "__default__") {
					return <AgentSelectMenuItem label={option.label} selected={selected} />;
				}
				const agent = options.find((entry) => entry.id === option.value);
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
