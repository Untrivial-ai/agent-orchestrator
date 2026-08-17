import type { SessionInterfaceTransition } from "../chat/api";
import { Alert } from "react-native";

export type TerminalInterfaceFailureRecovery = {
	actionLabel: string;
	policy: "interrupt";
	confirmationTitle: string;
	confirmationMessage: string;
	confirmationAction: string;
	confirmStyle: "destructive";
	confirm: (onConfirm: (policy: "interrupt") => void) => void;
};

const discardDraftRecovery: TerminalInterfaceFailureRecovery = {
	actionLabel: "Discard draft and switch",
	policy: "interrupt",
	confirmationTitle: "Discard draft and switch?",
	confirmationMessage:
		"Stopping now permanently discards the unsent terminal draft before switching to Chat. This cannot be undone. Completed conversation history and worktree files are preserved.",
	confirmationAction: "Discard draft and switch",
	confirmStyle: "destructive",
	confirm: (onConfirm) => {
		Alert.alert(
			"Discard draft and switch?",
			"Stopping now permanently discards the unsent terminal draft before switching to Chat. This cannot be undone. Completed conversation history and worktree files are preserved.",
			[
				{ text: "Keep draft", style: "cancel" },
				{
					text: "Discard draft and switch",
					style: "destructive",
					onPress: () => onConfirm("interrupt"),
				},
			],
		);
	},
};

const cancelDecisionRecovery: TerminalInterfaceFailureRecovery = {
	actionLabel: "Cancel request and switch",
	policy: "interrupt",
	confirmationTitle: "Cancel request and switch?",
	confirmationMessage:
		"Stopping now cancels the pending provider request and its current turn before switching to Chat. Unfinished work from that turn stops. Completed conversation history and worktree files are preserved.",
	confirmationAction: "Cancel request and switch",
	confirmStyle: "destructive",
	confirm: (onConfirm) => {
		Alert.alert(
			"Cancel request and switch?",
			"Stopping now cancels the pending provider request and its current turn before switching to Chat. Unfinished work from that turn stops. Completed conversation history and worktree files are preserved.",
			[
				{ text: "Keep request", style: "cancel" },
				{
					text: "Cancel request and switch",
					style: "destructive",
					onPress: () => onConfirm("interrupt"),
				},
			],
		);
	},
};

// Only a positively identified draft or provider request may advertise a
// destructive recovery, with copy that names exactly what interruption loses.
// Other failures must not silently become Stop.
export function terminalInterfaceFailureRecovery(
	transition?: Pick<SessionInterfaceTransition, "errorCode">,
): TerminalInterfaceFailureRecovery | undefined {
	if (transition?.errorCode === "DRAIN_DRAFT_PRESENT") return discardDraftRecovery;
	if (transition?.errorCode === "DRAIN_DECISION_PENDING") return cancelDecisionRecovery;
	return undefined;
}
