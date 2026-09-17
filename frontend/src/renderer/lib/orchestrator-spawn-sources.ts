export const ORCHESTRATOR_SPAWN_SOURCES = [
	"board",
	"restore_dialog",
	"topbar",
	"sidebar",
	"project_add",
	"project_clone",
	"settings",
	"restart",
	"command_palette",
] as const;

export type OrchestratorSpawnSource = (typeof ORCHESTRATOR_SPAWN_SOURCES)[number];
