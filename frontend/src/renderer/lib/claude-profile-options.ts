import type { ClaudeProfile } from "../hooks/useClaudeProfiles";
import type { AgentInfo } from "./agent-select-options";

export function profileSelectionKey(agent: string, configDir?: string): string {
	return agent === "claude-code" && configDir !== undefined
		? JSON.stringify([agent, configDir]) : agent;
}

export function expandClaudeProfiles<T extends AgentInfo>(
	agents: T[],
	profiles: ClaudeProfile[],
	selectedConfigDir?: string,
): Array<T & { harness: string; claudeConfigDir?: string }> {
	return agents.flatMap((agent) => {
		if (agent.id !== "claude-code") return [{ ...agent, harness: agent.id }];
		const available = profiles.some((profile) => profile.configDir === "")
			? [...profiles] : [{ name: "Default", configDir: "" }, ...profiles];
		if (selectedConfigDir !== undefined && !available.some((profile) => profile.configDir === selectedConfigDir)) {
			available.push({ name: selectedConfigDir || "Default", configDir: selectedConfigDir });
		}
		return [
			{ ...agent, label: `${agent.label} — Inherit profile`, harness: agent.id },
			...available.map((profile) => ({
				...agent,
				id: profileSelectionKey(agent.id, profile.configDir),
				harness: agent.id,
				claudeConfigDir: profile.configDir,
				label: `Claude Code — ${profile.name}`,
				// The daemon's default account does not establish this profile's auth.
				authentication: { state: "unknown" as const, freshness: "stale" as const },
				effectiveReadiness: "unknown" as const,
			})),
		];
	});
}
