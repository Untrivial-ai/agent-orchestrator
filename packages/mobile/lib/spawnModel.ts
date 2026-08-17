export type SpawnModelDefaults = {
	selectedAgent: string;
	projectWorkerAgent?: string;
	projectWorkerModel?: string;
	catalogDefault?: string;
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

export function resolveSpawnModel(input: SpawnModelDefaults): string {
	if (input.selectedAgent === input.projectWorkerAgent && input.projectWorkerModel) {
		return input.projectWorkerModel;
	}
	return input.catalogDefault ?? "";
}

export function modelOverride(value: string, resolvedDefault: string, touched: boolean): string | undefined {
	if (!touched) return undefined;
	const clean = value.trim();
	return clean !== resolvedDefault.trim() ? clean || undefined : undefined;
}
