import { create } from "zustand";

export type CommandCueState = "starting" | "running" | "exited" | "stopped" | "failed" | "closed";

export type CommandCueCard = {
	projectId: string;
	sessionId?: string;
	handleId: string;
	invokedAt: number;
	invocationOrder: number;
	name: string;
	command: string;
	state: CommandCueState;
	error?: string;
	output?: string;
	inputEnabled: boolean;
};

type CommandCueStore = {
	cards: Record<string, CommandCueCard>;
	register: (card: Omit<CommandCueCard, "inputEnabled" | "invokedAt" | "invocationOrder"> & Partial<Pick<CommandCueCard, "invokedAt" | "invocationOrder">>) => void;
	setState: (handleId: string, state: CommandCueState, error?: string, output?: string) => void;
	close: (handleId: string) => void;
	enableInput: (handleId: string) => void;
};

let nextInvocationOrder = 0;

export function commandCueInvocation() {
	return { invokedAt: Date.now(), invocationOrder: ++nextInvocationOrder };
}

export const useCommandCueStore = create<CommandCueStore>((set) => ({
	cards: {},
	register: (card) => set((current) => ({
		cards: { ...current.cards, [card.handleId]: { ...commandCueInvocation(), ...card, inputEnabled: false } },
	})),
	setState: (handleId, state, error, output) => set((current) => {
		const card = current.cards[handleId];
		if (!card || card.state === "closed") return current;
		return { cards: { ...current.cards, [handleId]: { ...card, state, error, output: output ?? card.output } } };
	}),
	close: (handleId) => set((current) => {
		const card = current.cards[handleId];
		if (!card) return current;
		return { cards: { ...current.cards, [handleId]: { ...card, state: "closed", inputEnabled: false } } };
	}),
	enableInput: (handleId) => set((current) => {
		const card = current.cards[handleId];
		if (!card || card.state === "closed") return current;
		return { cards: { ...current.cards, [handleId]: { ...card, inputEnabled: true } } };
	}),
}));

export function commandCueInputEnabled(handleId: string): boolean | undefined {
	return useCommandCueStore.getState().cards[handleId]?.inputEnabled;
}

export function resetCommandCueStore() {
	nextInvocationOrder = 0;
	useCommandCueStore.setState({ cards: {} });
}
