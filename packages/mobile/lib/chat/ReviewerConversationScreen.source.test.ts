import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("../../app/reviewer/[reviewId].tsx", import.meta.url), "utf8");

describe("reviewer conversation parity", () => {
	it("only exposes reviewer-owned send, request and interruption actions", () => {
		expect(source).toContain("<ReviewerComposer");
		expect(source).toContain("onSend={conversation.send}");
		expect(source).toContain("onInterrupt={conversation.interrupt}");
		expect(source).toContain("onDecide={conversation.resolveApproval}");
		expect(source).toContain("onResolveInput={conversation.resolveInput}");
		expect(source).not.toContain("<ChatComposer");
		expect(source).not.toContain("conversation.chooseSettings");
		expect(source).not.toContain("conversation.steer");
		expect(source).not.toContain("conversation.promoteQueuedTurn");
	});

	it("stages reviewer attachments against the worker session", () => {
		expect(source).toContain("useMobileConversation(config, sessionId || reviewId");
		expect(source).toContain("{ reviewId, eventSessionId: sessionId }");
	});

	it("does not offer ordinary-session rollback for a reviewer id", () => {
		expect(source).not.toContain("onRollback=");
	});
});
