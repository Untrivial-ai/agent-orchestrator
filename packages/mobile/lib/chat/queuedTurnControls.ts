import type { ConversationQueuedTurn } from "./types";

export type QueuedTurnPresentation = {
	label: string;
	originLabel?: string;
	cancelLabel: string;
	promoteLabel?: string;
	canPromote: boolean;
};

export function queuedTurnPresentation(turn: ConversationQueuedTurn): QueuedTurnPresentation {
	const text = turn.text.trim();
	switch (turn.origin) {
		case "human": {
			const label = text || "Queued message";
			return {
				label,
				cancelLabel: `Cancel queued message: ${label}`,
				promoteLabel: `Use as next message: ${label}`,
				canPromote: true,
			};
		}
		case "automation": {
			const label = text || "Queued automation";
			return {
				label,
				originLabel: "Automation",
				cancelLabel: `Cancel queued automation: ${label}`,
				canPromote: false,
			};
		}
		case "daemon": {
			const label = text || "Queued system work";
			return {
				label,
				originLabel: "System",
				cancelLabel: `Cancel queued system work: ${label}`,
				canPromote: false,
			};
		}
		case "provider": {
			const label = text || "Queued agent work";
			return {
				label,
				originLabel: "Agent",
				cancelLabel: `Cancel queued agent work: ${label}`,
				canPromote: false,
			};
		}
		default: {
			const label = text || "Queued work";
			return {
				label,
				originLabel: "Queued",
				cancelLabel: `Cancel queued work: ${label}`,
				canPromote: false,
			};
		}
	}
}

export async function applyQueuedTurnAction(
	turnId: string,
	mutate: (turnId: string) => Promise<unknown>,
	refresh: () => Promise<void>,
): Promise<void> {
	await mutate(turnId);
	await refresh();
}
