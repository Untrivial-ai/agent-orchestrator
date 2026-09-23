import type { AgentProfile } from "./agent-capabilities";

export const AGENT_OPTIONS = [
	"claude-code",
	"codex",
	"aider",
	"opencode",
	"grok",
	"droid",
	"amp",
	"agy",
	"crush",
	"cursor",
	"qwen",
	"copilot",
	"goose",
	"auggie",
	"continue",
	"devin",
	"cline",
	"kimi",
	"muse",
	"kiro",
	"kilocode",
	"vibe",
	"pi",
	"kimchi",
	"prime-agent",
	"autohand",
	"omp",
] as const;

export type AgentId = (typeof AGENT_OPTIONS)[number];
export type AgentOption = AgentId;

export type AgentIdentity = Pick<AgentProfile, "id" | "label"> & {
	logoKey?: string;
	initial: string;
};

export const AGENT_LABELS: Record<AgentId, string> = {
	"claude-code": "Claude Code",
	codex: "Codex",
	aider: "Aider",
	opencode: "OpenCode",
	grok: "Grok",
	droid: "Droid",
	amp: "Amp",
	agy: "AGY",
	crush: "Crush",
	cursor: "Cursor",
	qwen: "Qwen",
	copilot: "GitHub Copilot",
	goose: "Goose",
	auggie: "Auggie",
	continue: "Continue",
	devin: "Devin",
	cline: "Cline",
	kimi: "Kimi",
	muse: "Muse",
	kiro: "Kiro",
	kilocode: "Kilo Code",
	vibe: "Vibe",
	pi: "Pi",
	kimchi: "Kimchi",
	"prime-agent": "Prime Agent",
	autohand: "Autohand",
	omp: "OMP",
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

const identityAliases: Readonly<Record<string, AgentIdentity>> = {
	claude: {
		id: "claude",
		label: "Claude",
		logoKey: "claude",
		initial: "C",
	},
};

export function getAgentIdentity(provider: string): AgentIdentity {
	const identity = identityAliases[provider] ?? findAgentIdentity(provider);
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
