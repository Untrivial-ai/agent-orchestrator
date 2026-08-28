import type { MessageBoxSyncOptions } from "electron";
import {
	CHAT_DRAFT_BOUNDARY_COPY,
	type ChatDraftBoundaryKind,
} from "../shared/chat-draft-risk";

export function chatDraftUnloadDialog(
	risks: readonly ChatDraftBoundaryKind[],
): MessageBoxSyncOptions {
	const detail = [...new Set(risks)]
		.map((risk) => CHAT_DRAFT_BOUNDARY_COPY[risk])
		.join("\n\n");
	return {
		type: "warning",
		title: "Unsaved Chat draft",
		message: "This Chat draft is not safely saved yet.",
		detail,
		buttons: ["Stay", "Leave anyway"],
		defaultId: 0,
		cancelId: 0,
		noLink: true,
	};
}

export function confirmUnsafeChatDraftLeave(
	risks: readonly ChatDraftBoundaryKind[],
	showMessageBoxSync: (options: MessageBoxSyncOptions) => number,
): boolean {
	return risks.length === 0 || showMessageBoxSync(chatDraftUnloadDialog(risks)) === 1;
}

export function shouldPreventUnsafeChatDraftClose(
	risks: readonly ChatDraftBoundaryKind[],
	alreadyConfirmed: boolean,
	showMessageBoxSync: (options: MessageBoxSyncOptions) => number,
): boolean {
	if (risks.length === 0 || alreadyConfirmed) return false;
	return !confirmUnsafeChatDraftLeave(risks, showMessageBoxSync);
}
