import { describe, expect, it } from "vitest";
import { emptySideChatDraft, parseSideChatDraft } from "./sideChatDraft";

describe("launch-scoped side draft", () => {
	it("restores text, files, and multiple references", () => {
		const draft = { ...emptySideChatDraft(), text: "What are these?", attachments: [{ id: "file", path: "notes.txt", name: "notes.txt", mimeType: "text/plain", bytes: 3 }],
			references: [{ id: "one", conversationId: "main", messageId: "message-1", revision: 2, text: "sun", role: "assistant" as const },
				{ id: "two", conversationId: "side", messageId: "message-2", revision: 1, text: "moon", role: "user" as const }] };
		expect(parseSideChatDraft(JSON.stringify(draft))).toEqual(draft);
	});

	it("migrates a plain-text draft from the previous side UI", () => {
		expect(parseSideChatDraft("unfinished question")).toEqual({ ...emptySideChatDraft(), text: "unfinished question" });
	});
});
