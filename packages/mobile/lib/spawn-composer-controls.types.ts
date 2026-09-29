import type { VoiceMode, VoiceState } from "./voice/types";

export type SpawnComposerOption = {
	id: string;
	label: string;
};

export type SpawnComposerVoice = {
	state: VoiceState;
	mode: VoiceMode;
	onPressIn: () => void;
	onPressOut: () => void;
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
	/** Dictation into the prompt: hold to talk, double-tap for hands-free. */
	voice: SpawnComposerVoice;
	onSpawn: () => void;
	busy: boolean;
	disabled: boolean;
};
