import { describe, expect, it } from "vitest";

import { parseChatDraftBoundaryKinds } from "./chat-draft-risk";

describe("Chat draft risk IPC payloads", () => {
	it("deduplicates and orders every known risk deterministically", () => {
		expect(
			parseChatDraftBoundaryKinds([
				"pending-attachments",
				"persistence-failed",
				"pending-attachments",
			]),
		).toEqual(["persistence-failed", "pending-attachments"]);
	});

	it.each([true, "pending-attachments", ["pending-attachments", "unknown"]])(
		"rejects an invalid payload without converting it to a safe empty state: %j",
		(payload) => {
			expect(parseChatDraftBoundaryKinds(payload)).toBeUndefined();
		},
	);
});
