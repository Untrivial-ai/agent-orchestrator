export type SpawnComposerOption = {
	id: string;
	label: string;
};

export type SpawnComposerControlsProps = {
	projects: readonly SpawnComposerOption[];
	projectId: string | null;
	onSelectProject: (projectId: string) => void;
	agents: readonly SpawnComposerOption[];
	harness: string;
	onSelectHarness: (harness: string) => void;
	models: readonly SpawnComposerOption[];
	modelSelection: string;
	modelLabel: string;
	onSelectModel: (model: string) => void;
	onAttach: () => void;
	onSpawn: () => void;
	busy: boolean;
	disabled: boolean;
};
