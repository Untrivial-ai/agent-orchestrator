import { Alert } from "react-native";
import type { ConversationQueuedTurn } from "./types";

/**
 * Bind mobile Stop to the complete ordered durable queue the user reviewed.
 * The copied IDs remain stable while the native alert is open; a daemon conflict
 * makes the caller refresh and invoke this again with the new snapshot.
 */
export function confirmConversationStop(
	queuedTurns: readonly ConversationQueuedTurn[] | undefined,
	onConfirm: (queuedTurnIds: string[]) => void,
): void {
	if (queuedTurns === undefined) {
		Alert.alert(
			"Stop unavailable",
			"This AO daemon does not report the complete queued work needed for a safe Stop. Update or restart AO, then try again.",
		);
		return;
	}
	const queuedTurnIds = queuedTurns.map((turn) => turn.turnId);
	if (queuedTurnIds.length === 0) {
		onConfirm([]);
		return;
	}
	const count = queuedTurnIds.length;
	Alert.alert(
		`Stop turn and cancel ${count} queued ${count === 1 ? "message" : "messages"}?`,
		count === 2
			? "The active turn and both queued messages will be stopped. This cannot be undone."
			: `The active turn and ${count === 1 ? "the queued message" : `all ${count} queued messages`} will be stopped. This cannot be undone.`,
		[
			{ text: "Keep working", style: "cancel" },
			{
				text: "Stop all",
				style: "destructive",
				onPress: () => onConfirm(queuedTurnIds),
			},
		],
	);
}
