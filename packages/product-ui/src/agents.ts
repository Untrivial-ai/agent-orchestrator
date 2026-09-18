import type { AgentProfile } from "./agent-capabilities";

export const AGENT_OPTIONS = ["opencode"] as const;

export type AgentId = (typeof AGENT_OPTIONS)[number];
export type AgentOption = AgentId;

export type AgentIdentity = Pick<AgentProfile, "id" | "label"> & {
	logoKey?: string;
	initial: string;
};

export const AGENT_LABELS: Record<AgentId, string> = {
	opencode: "OpenCode",
};

export const AGENT_IDENTITIES: ReadonlyMap<AgentId, AgentIdentity> = new Map(
	AGENT_OPTIONS.map((id) => [
		id,
		{
			id,
			label: AGENT_LABELS[id],
			logoKey: id,
			initial: AGENT_LABELS[id].charAt(0).toUpperCase(),
		},
	]),
);

export function getAgentIdentity(provider: string): AgentIdentity {
	const identity = findAgentIdentity(provider);
	if (identity) {
		return identity;
	}
	return {
		id: provider,
		label: provider || "Unknown agent",
		initial: provider.charAt(0).toUpperCase() || "?",
	};
}

export function agentLabel(provider: string): string {
	return provider in AGENT_LABELS ? AGENT_LABELS[provider as AgentId] : provider;
}

function findAgentIdentity(provider: string): AgentIdentity | undefined {
	for (const id of AGENT_OPTIONS) {
		if (id === provider) return AGENT_IDENTITIES.get(id);
	}
	return undefined;
}
