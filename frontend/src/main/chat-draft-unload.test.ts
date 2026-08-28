import { describe, expect, it, vi } from "vitest";

import {
	chatDraftUnloadDialog,
	confirmUnsafeChatDraftLeave,
	shouldPreventUnsafeChatDraftClose,
} from "./chat-draft-unload";

describe("native Chat draft unload confirmation", () => {
	it("keeps the app open by default and only discards on the explicit second choice", () => {
		const stay = vi.fn((_dialog: ReturnType<typeof chatDraftUnloadDialog>) => 0);
		const leave = vi.fn((_dialog: ReturnType<typeof chatDraftUnloadDialog>) => 1);
		const risks = ["persistence-failed", "pending-attachments"] as const;
		const dialog = chatDraftUnloadDialog(risks);

		expect(confirmUnsafeChatDraftLeave(risks, stay)).toBe(false);
		expect(confirmUnsafeChatDraftLeave(risks, leave)).toBe(true);
		expect(stay).toHaveBeenCalledWith(dialog);
		expect(dialog).toMatchObject({
			defaultId: 0,
			cancelId: 0,
		});
		expect(dialog.detail).toContain("could not be saved locally");
		expect(dialog.detail).toContain("Attachments are still being saved");
		expect(dialog.detail).not.toContain("delivery recovery is still pending");
	});

	it("prevents the native window close until the user explicitly chooses to leave", () => {
		const stay = vi.fn((_dialog: ReturnType<typeof chatDraftUnloadDialog>) => 0);
		const leave = vi.fn((_dialog: ReturnType<typeof chatDraftUnloadDialog>) => 1);

		expect(shouldPreventUnsafeChatDraftClose(["pending-delivery"], false, stay)).toBe(true);
		expect(shouldPreventUnsafeChatDraftClose(["pending-delivery"], false, leave)).toBe(false);
		expect(shouldPreventUnsafeChatDraftClose([], false, stay)).toBe(false);
		expect(shouldPreventUnsafeChatDraftClose(["pending-delivery"], true, stay)).toBe(false);
		expect(stay).toHaveBeenCalledTimes(1);
		const dialog = stay.mock.calls[0]?.[0];
		expect(dialog?.detail).toContain("delivery recovery is still pending");
		expect(dialog?.detail).not.toContain("Attachments are still being saved");
		expect(dialog?.detail).not.toContain("could not be saved locally");
	});
});
