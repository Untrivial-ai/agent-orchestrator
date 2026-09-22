export const TASK_COMPOSER_PREFERENCES_STORAGE_KEY = "ao.taskComposer.preferences.v1";

export type TaskComposerAgentPreference = {
	model: string;
	mode: string;
	effort?: string;
};

export type TaskComposerContextPreferences = {
	lastAgent: string;
	agents: Record<string, TaskComposerAgentPreference>;
};

type TaskComposerPreferences = Record<string, TaskComposerContextPreferences>;

function storage(): Storage | undefined {
	if (typeof window === "undefined") return undefined;
	try {
		return window.localStorage;
	} catch {
		return undefined;
	}
}

function readAll(): TaskComposerPreferences {
	try {
		const parsed: unknown = JSON.parse(storage()?.getItem(TASK_COMPOSER_PREFERENCES_STORAGE_KEY) ?? "{}");
		if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
		const preferences: TaskComposerPreferences = {};
		for (const [context, value] of Object.entries(parsed)) {
			if (!value || typeof value !== "object" || Array.isArray(value)) continue;
			const candidate = value as { lastAgent?: unknown; agents?: unknown };
			if (
				typeof candidate.lastAgent !== "string" ||
				!candidate.agents ||
				typeof candidate.agents !== "object" ||
				Array.isArray(candidate.agents)
			) {
				continue;
			}
			const agents: Record<string, TaskComposerAgentPreference> = {};
			for (const [agent, config] of Object.entries(candidate.agents)) {
				if (!config || typeof config !== "object" || Array.isArray(config)) continue;
				const fields = config as { model?: unknown; mode?: unknown; effort?: unknown };
				if (typeof fields.model !== "string" || typeof fields.mode !== "string") continue;
				if (fields.effort !== undefined && typeof fields.effort !== "string") continue;
				agents[agent] = {
					model: fields.model,
					mode: fields.mode,
					...(typeof fields.effort === "string" ? { effort: fields.effort } : {}),
				};
			}
			preferences[context] = { lastAgent: candidate.lastAgent, agents };
		}
		return preferences;
	} catch {
		return {};
	}
}

export function readTaskComposerPreferences(context: string): TaskComposerContextPreferences | undefined {
	return readAll()[context];
}

export function rememberTaskComposerPreference(
	context: string,
	agent: string,
	config: TaskComposerAgentPreference,
): void {
	const preferences = readAll();
	const current = preferences[context];
	preferences[context] = {
		lastAgent: agent,
		agents: { ...current?.agents, [agent]: config },
	};
	try {
		storage()?.setItem(TASK_COMPOSER_PREFERENCES_STORAGE_KEY, JSON.stringify(preferences));
	} catch {
		// Remembering composer choices is optional; spawning must still succeed.
	}
}
