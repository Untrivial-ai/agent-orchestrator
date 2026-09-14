import { describe, expect, it } from "vitest";
import { conversationActionError, conversationActionUnsupported, conversationErrorCode } from "./conversationErrors";

describe("mobile conversation action errors", () => {
	it("turns protocol codes into instructions the user can act on", () => {
		expect(conversationActionError(Object.assign(new Error("conflict"), { code: "CHAT_NO_ACTIVE_TURN" })))
			.toContain("Queue it as a new message");
		expect(conversationActionError(Object.assign(new Error("busy"), { code: "CHAT_COMPACTION_BUSY" })))
			.toBe("Stop the current turn before compacting history.");
		expect(conversationActionError(Object.assign(new Error("nope"), { code: "CHAT_MCP_RELOAD_UNSUPPORTED" })))
			.toBe("This agent cannot reload its MCP servers.");
		expect(conversationActionError(Object.assign(new Error("nope"), { code: "CHAT_STEER_UNSUPPORTED" })))
			.toContain("Queue a new message");
		expect(conversationActionError(Object.assign(new Error("conflict"), { code: "CHAT_TURN_RUNNING" })))
			.toContain("Stop the current turn");
		expect(conversationActionError(Object.assign(new Error("conflict"), { code: "CHAT_REQUEST_NOT_PENDING" })))
			.toContain("already answered");
		expect(conversationActionError(Object.assign(new Error("conflict"), { code: "CHAT_QUEUE_SCOPE_CHANGED" })))
			.toBe("Queued work changed while Stop was awaiting confirmation. Review the refreshed queue and press Stop again.");
	});

	it("preserves typed refusal identities so unsupported controls can withdraw", () => {
		const error = Object.assign(new Error("conflict"), { code: "CHAT_STEER_UNSUPPORTED" });
		expect(conversationErrorCode(error)).toBe("CHAT_STEER_UNSUPPORTED");
		expect(conversationActionUnsupported("steer", conversationErrorCode(error))).toBe(true);
		expect(conversationActionUnsupported("compact", conversationErrorCode(error))).toBe(false);
	});
});
