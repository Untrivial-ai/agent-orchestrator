import type { CloudCpAgentProvider, CloudCpProject, CloudCpProjectAgentConfig, CloudCpProjectSettingsRequest } from "./cloud-cp";
import { CLOUD_AGENT_PROVIDERS } from "./cloud-agents";

export type CloudProjectRoleDraft = {
	agent: CloudCpAgentProvider | "";
	agentConfig: Required<CloudCpProjectAgentConfig>;
};

export type CloudProjectSettingsDraft = {
	displayName: string;
	defaultBranch: string;
	worker: CloudProjectRoleDraft;
	orchestrator: CloudProjectRoleDraft;
	reviewer: CloudProjectRoleDraft;
	autoReview: boolean;
};

export function emptyCloudAgentConfig(): Required<CloudCpProjectAgentConfig> {
	return { model: "", mode: "", effort: "", permissions: "" };
}

function cloudAgent(value: unknown): CloudCpAgentProvider | "" {
	return CLOUD_AGENT_PROVIDERS.find((agent) => agent === value) ?? "";
}

export function cloudProjectSettingsDraft(project: CloudCpProject): CloudProjectSettingsDraft {
	const config = project.config;
	const role = (name: "worker" | "orchestrator"): CloudProjectRoleDraft => ({
		agent: cloudAgent(config[name]?.agent ?? config[`${name}Agent`]),
		agentConfig: { ...emptyCloudAgentConfig(), ...config[name]?.agentConfig },
	});
	return {
		displayName: project.displayName,
		defaultBranch: project.defaultBranch,
		worker: role("worker"),
		orchestrator: role("orchestrator"),
		reviewer: {
			agent: cloudAgent(config.reviewers?.[0]?.harness),
			agentConfig: { ...emptyCloudAgentConfig(), ...config.reviewers?.[0]?.agentConfig },
		},
		autoReview: config.autoReview ?? true,
	};
}

// Send only changed fields. The control plane preserves every omitted setting.
export function cloudProjectSettingsPatch(before: CloudProjectSettingsDraft, after: CloudProjectSettingsDraft): CloudCpProjectSettingsRequest {
	const patch: CloudCpProjectSettingsRequest = {};
	if (before.displayName !== after.displayName) patch.displayName = after.displayName.trim();
	if (before.defaultBranch !== after.defaultBranch) patch.defaultBranch = after.defaultBranch.trim();
	const config: NonNullable<CloudCpProjectSettingsRequest["config"]> = {};
	for (const role of ["worker", "orchestrator"] as const) {
		if (JSON.stringify(before[role]) !== JSON.stringify(after[role])) {
			config[role] = after[role].agent === "" ? null : { agent: after[role].agent, agentConfig: after[role].agentConfig };
		}
	}
	if (JSON.stringify(before.reviewer) !== JSON.stringify(after.reviewer)) {
		config.reviewers = after.reviewer.agent === "" ? [] : [{ harness: after.reviewer.agent, agentConfig: after.reviewer.agentConfig }];
	}
	if (before.autoReview !== after.autoReview) config.autoReview = after.autoReview;
	if (Object.keys(config).length > 0) patch.config = config;
	return patch;
}
