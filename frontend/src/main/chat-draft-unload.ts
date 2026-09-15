import type { MessageBoxSyncOptions } from "electron";
import {
	type ChatDraftDialogCopy,
	type ChatDraftBoundaryKind,
} from "../shared/chat-draft-risk";

export function chatDraftUnloadDialog(
	copy: ChatDraftDialogCopy,
): MessageBoxSyncOptions {
	return {
		type: "warning",
		title: copy.title,
		message: copy.message,
		detail: copy.detail,
		buttons: [copy.stay, copy.leave],
		defaultId: 0,
		cancelId: 0,
		noLink: true,
	};
}

export function confirmUnsafeChatDraftLeave(
	risks: readonly ChatDraftBoundaryKind[],
	showMessageBoxSync: (options: MessageBoxSyncOptions) => number,
	copy: ChatDraftDialogCopy | undefined,
): boolean {
	return risks.length === 0 || Boolean(copy && showMessageBoxSync(chatDraftUnloadDialog(copy)) === 1);
}

export function shouldPreventUnsafeChatDraftClose(
	risks: readonly ChatDraftBoundaryKind[],
	alreadyConfirmed: boolean,
	showMessageBoxSync: (options: MessageBoxSyncOptions) => number,
	copy: ChatDraftDialogCopy | undefined,
): boolean {
	if (risks.length === 0 || alreadyConfirmed) return false;
	return !confirmUnsafeChatDraftLeave(risks, showMessageBoxSync, copy);
}
