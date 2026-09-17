import { appI18n } from "../i18n/instance";
import { describe, expect, it, vi } from "vitest";

import {
	chatDraftBoundaryCopy,
	chatDraftDialogCopy,
	chatDraftDiscardWarning,
	confirmDiscardChatDraft,
	confirmDiscardChatDrafts,
	getChatDraftBoundary,
	getChatDraftBoundaries,
	setChatDraftBoundary,
	subscribeChatDraftBoundaries,
} from "./chat-draft-boundary";

describe("Chat draft destructive-boundary state", () => {
	it("isolates sessions and keeps another unsafe source when one clears", () => {
		setChatDraftBoundary("session-a", "composer", "pending-attachments");
		setChatDraftBoundary("session-a", "inline-edit", "persistence-failed");
		setChatDraftBoundary("session-b", "composer", "pending-attachments");

		expect(getChatDraftBoundary("session-a")).toBe("persistence-failed");
		setChatDraftBoundary("session-a", "inline-edit", undefined);
		expect(getChatDraftBoundary("session-a")).toBe("pending-attachments");
		expect(getChatDraftBoundary("session-b")).toBe("pending-attachments");

		setChatDraftBoundary("session-a", "composer", undefined);
		setChatDraftBoundary("session-b", "composer", undefined);
	});

	it("preserves every active risk kind for mixed boundary decisions", () => {
		setChatDraftBoundary("session-mixed", "inline-edit", "pending-attachments");
		setChatDraftBoundary("session-mixed", "composer", [
			"persistence-failed",
		]);

		expect(getChatDraftBoundaries("session-mixed")).toEqual([
			"persistence-failed",
			"pending-attachments",
		]);

		setChatDraftBoundary("session-mixed", "inline-edit", undefined);
		setChatDraftBoundary("session-mixed", "composer", undefined);
	});

	it("preserves simultaneous risks reported by the same composer", () => {
		setChatDraftBoundary("session-same-source", "composer", [
			"persistence-failed",
			"pending-attachments",
		]);

		expect(getChatDraftBoundary("session-same-source")).toBe("persistence-failed");
		expect(getChatDraftBoundaries("session-same-source")).toEqual([
			"persistence-failed",
			"pending-attachments",
		]);

		setChatDraftBoundary("session-same-source", "composer", undefined);
	});

	it("keeps one stable aggregate snapshot across equivalent updates", () => {
		setChatDraftBoundary("session-stable", "composer", [
			"pending-attachments",
			"persistence-failed",
		]);
		const first = getChatDraftBoundaries("session-stable");

		setChatDraftBoundary("session-stable", "composer", [
			"persistence-failed",
			"pending-attachments",
			"pending-attachments",
		]);
		expect(getChatDraftBoundaries("session-stable")).toBe(first);

		setChatDraftBoundary("session-stable", "composer", undefined);
	});

	it("notifies subscribers only when the effective source state changes", () => {
		const listener = vi.fn();
		const unsubscribe = subscribeChatDraftBoundaries(listener);
		setChatDraftBoundary("session-notify", "composer", "persistence-failed");
		setChatDraftBoundary("session-notify", "composer", "persistence-failed");
		setChatDraftBoundary("session-notify", "composer", undefined);
		unsubscribe();

		expect(listener).toHaveBeenCalledTimes(2);
	});

	it("asks for explicit discard using precise copy", () => {
		const confirm = vi.fn(() => false);
		expect(confirmDiscardChatDraft("pending-attachments", confirm)).toBe(false);
		expect(confirm).toHaveBeenCalledWith(expect.stringContaining(chatDraftBoundaryCopy("pending-attachments")));
		expect(confirm).toHaveBeenCalledWith(expect.stringContaining("Leave this chat anyway?"));
	});

	it("describes every active risk before a mixed discard", () => {
		const confirm = vi.fn((_message: string) => true);
		expect(
			confirmDiscardChatDrafts(["persistence-failed", "pending-attachments"], confirm),
		).toBe(true);
		const warning = confirm.mock.calls[0]?.[0];
		expect(warning).toContain(chatDraftBoundaryCopy("persistence-failed"));
		expect(warning).toContain(chatDraftBoundaryCopy("pending-attachments"));
	});

	it("builds reusable app-rendered warning copy without duplicate risks", () => {
		expect(chatDraftDiscardWarning([])).toBeUndefined();
		expect(
			chatDraftDiscardWarning(["persistence-failed", "persistence-failed"]),
		).toBe(
			`${chatDraftBoundaryCopy("persistence-failed")}\n\nLeave this chat anyway?`,
		);
	});
});

it("uses the selected locale for renderer and native leave warnings", async () => {
	await appI18n.changeLanguage("fr");
	try {
		const copy = chatDraftDialogCopy(["persistence-failed", "pending-attachments"]);
		expect(copy).toMatchObject({ title: "Brouillon de conversation non enregistré", stay: "Rester", leave: "Quitter quand même" });
		expect(copy.detail).toContain("Les pièces jointes sont en cours d’enregistrement");
		expect(chatDraftDiscardWarning(["persistence-failed"])).toContain("Quitter quand même cette conversation ?");
	} finally {
		await appI18n.changeLanguage("en");
	}
});
