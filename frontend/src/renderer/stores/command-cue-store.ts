import { create } from "zustand";

export type CommandCueState = "starting" | "running" | "exited" | "stopped" | "failed";

export type CommandCueCard = {
	projectId: string;
	sessionId?: string;
	handleId: string;
	name: string;
	command: string;
	state: CommandCueState;
	error?: string;
	inputEnabled: boolean;
};

type CommandCueStore = {
	cards: Record<string, CommandCueCard>;
	register: (card: Omit<CommandCueCard, "inputEnabled">) => void;
	setState: (handleId: string, state: CommandCueState, error?: string) => void;
	enableInput: (handleId: string) => void;
};

export const useCommandCueStore = create<CommandCueStore>((set) => ({
	cards: {},
	register: (card) => set((current) => ({
		cards: { ...current.cards, [card.handleId]: { ...card, inputEnabled: false } },
	})),
	setState: (handleId, state, error) => set((current) => {
		const card = current.cards[handleId];
		if (!card) return current;
		return { cards: { ...current.cards, [handleId]: { ...card, state, error } } };
	}),
	enableInput: (handleId) => set((current) => {
		const card = current.cards[handleId];
		if (!card) return current;
		return { cards: { ...current.cards, [handleId]: { ...card, inputEnabled: true } } };
	}),
}));

export function commandCueInputEnabled(handleId: string): boolean | undefined {
	return useCommandCueStore.getState().cards[handleId]?.inputEnabled;
}

export function resetCommandCueStore() {
	useCommandCueStore.setState({ cards: {} });
}
