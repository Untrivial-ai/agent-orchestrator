export type ComposerDeliveryIntent = "send" | "steer";

export function composerDeliveryRoute(intent: ComposerDeliveryIntent, steerEligible: boolean): "queue" | "steer" {
	return intent === "steer" && steerEligible ? "steer" : "queue";
}

export function composerDeliveryPresentation(state: { active: boolean; canSteer: boolean; hasDraft: boolean; hasAttachments: boolean; hasQueued: boolean }) {
	return {
		placeholder: state.active ? "Send after this…" : "Message this worker…",
		showQueueNote: state.active,
		showSteerAction: state.active && state.canSteer && state.hasDraft && !state.hasAttachments && !state.hasQueued,
	};
}

export function composerPrimaryAction(state: { active: boolean; hasDraft: boolean; hasAttachments: boolean }): "send" | "stop" {
	return state.active && !state.hasDraft && !state.hasAttachments ? "stop" : "send";
}
