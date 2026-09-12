import { describe, expect, it, vi } from "vitest";
import { chatDraftUnloadDialog, confirmUnsafeChatDraftLeave, shouldPreventUnsafeChatDraftClose } from "./chat-draft-unload";

const copy = { title: "Brouillon", message: "Non enregistré", detail: "Copiez le brouillon avant de partir.", stay: "Rester", leave: "Quitter quand même" };

describe("native Chat draft unload confirmation", () => {
	it("uses localized renderer copy and keeps the app open by default", () => {
		const stay = vi.fn(() => 0);
		const leave = vi.fn(() => 1);
		const risks = ["persistence-failed", "pending-attachments"] as const;
		expect(confirmUnsafeChatDraftLeave(risks, stay, copy)).toBe(false);
		expect(confirmUnsafeChatDraftLeave(risks, leave, copy)).toBe(true);
		expect(stay).toHaveBeenCalledWith({ type: "warning", title: copy.title, message: copy.message, detail: copy.detail, buttons: [copy.stay, copy.leave], defaultId: 0, cancelId: 0, noLink: true });
	});

	it("only blocks close for volatile state that has not been explicitly discarded", () => {
		const show = vi.fn(() => 0);
		expect(shouldPreventUnsafeChatDraftClose(["persistence-failed"], false, show, copy)).toBe(true);
		expect(shouldPreventUnsafeChatDraftClose([], false, show, copy)).toBe(false);
		expect(shouldPreventUnsafeChatDraftClose(["persistence-failed"], true, show, copy)).toBe(false);
		expect(show).toHaveBeenCalledTimes(1);
	});

	it("does not discard volatile state if localized dialog copy is missing", () => {
		const show = vi.fn();
		expect(confirmUnsafeChatDraftLeave(["persistence-failed"], show, undefined)).toBe(false);
		expect(show).not.toHaveBeenCalled();
		expect(chatDraftUnloadDialog(copy).defaultId).toBe(0);
	});
});
