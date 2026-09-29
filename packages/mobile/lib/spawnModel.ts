export type SpawnModelDefaults = {
	selectedAgent: string;
	projectWorkerAgent?: string;
	projectWorkerModel?: string;
};

export type SpawnModelSource = { projectId: string | null; agentId: string };

export type SpawnAgentDefaults = {
	projectWorkerAgent?: string;
	projectAgent?: string;
	availableAgents: readonly string[];
};

export function resolveSpawnAgent(input: SpawnAgentDefaults): string {
	for (const candidate of [input.projectWorkerAgent, input.projectAgent]) {
		if (candidate && input.availableAgents.includes(candidate)) return candidate;
	}
	return input.availableAgents[0] ?? "";
}

export function spawnModelSourceChanged(current: SpawnModelSource, next: SpawnModelSource): boolean {
	return current.projectId !== next.projectId || current.agentId !== next.agentId;
}

/**
 * The model the project pins for the agent about to be spawned, or "" for none.
 *
 * Only the project's own configuration counts. The catalog's `isDefault` entry is
 * deliberately **not** consulted: it is a claim about which model the provider
 * will choose, and it is not always true. Codex advertises `gpt-6-astra` as its
 * default while a fresh thread comes up on `gpt-5.6-sol`, so the spawn sheet
 * named Astra and the chat named Sol. Nothing on this side can know what a
 * provider will really pick, so nothing here pretends to.
 */
export function resolveSpawnModel(input: SpawnModelDefaults): string {
	if (input.selectedAgent !== input.projectWorkerAgent) return "";
	return (input.projectWorkerModel ?? "").trim();
}

/**
 * What to send as the model.
 *
 * A picked value is sent even when the project pins the same one. The old rule
 * dropped it as redundant, so choosing the model the sheet had *shown* as the
 * default sent no model at all: the session recorded nothing, and the chat had no
 * way to know what was asked for, so it displayed whatever the thread happened to
 * be running. Sending the pick is what makes "you started this with Astra" true
 * in both places.
 *
 * `touched` is false for the untouched "Automatic" state, which sends nothing and
 * lets the provider choose — the one case where no model is the honest answer.
 */
export function modelOverride(value: string, touched: boolean): string | undefined {
	if (!touched) return undefined;
	const clean = value.trim();
	return clean && clean !== "__auto__" ? clean : undefined;
}
