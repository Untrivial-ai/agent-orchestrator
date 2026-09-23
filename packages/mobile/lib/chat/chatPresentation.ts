import type { Feather } from "../icons";
import type { ConversationTurn } from "./types";

export type ChatTone = "accent" | "attention" | "danger" | "muted" | "success" | "working";

export type ChatPresentation = {
	icon: keyof typeof Feather.glyphMap;
	label: string;
	tone: ChatTone;
};

export function conversationMarkerPresentation(state?: ConversationTurn["state"]): ChatPresentation {
	switch (state) {
		case "running": return { icon: "activity", label: "Working", tone: "working" };
		case "completed": return { icon: "check-circle", label: "Done", tone: "success" };
		case "failed": return { icon: "alert-circle", label: "Failed", tone: "danger" };
		case "interrupted": return { icon: "minus-circle", label: "Stopped", tone: "muted" };
		case "queued": return { icon: "clock", label: "Queued", tone: "muted" };
		default: return { icon: "message-circle", label: "Update", tone: "accent" };
	}
}

export function requestPresentation(kind: "approval" | "input", pending: boolean): Omit<ChatPresentation, "label"> & { title: string; badge: string } {
	if (!pending) return { icon: "check-circle", title: kind === "approval" ? "Approval resolved" : "Input resolved", badge: "Resolved", tone: "muted" };
	return kind === "approval"
		? { icon: "shield", title: "Approval needed", badge: "Needs approval", tone: "attention" }
		: { icon: "message-circle", title: "Agent needs input", badge: "Needs input", tone: "accent" };
}

export function actionControlWidth(label: string, primary = false): number {
	return Math.min(148, Math.max(primary ? 76 : 58, label.length * 7 + 24));
}
