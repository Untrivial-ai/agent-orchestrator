import { describe, expect, it } from "vitest";
import { activityHierarchy, activityNodesRunning, activityStartsExpanded, canRollbackTurn, conversationMarkers, countActivityNodes, groupConversationByTurn, latestFirstConversationGroups, readableConversationItems } from "./timelineModel";
import * as timelineModel from "./timelineModel";
import type { ConversationActivity, ConversationSnapshot } from "./types";

function snapshot(): ConversationSnapshot {
	return {
		conversationId: "c", sessionId: "s", harness: "codex", mode: "chat", controller: { state: "ready" },
		latestSequence: 4, oldestSequence: 1, hasMoreBefore: false, settings: {}, capabilities: ["rollback"],
		turns: [
			{ id: "t1", state: "completed", providerTurnId: "p1", requestedAt: "2026-08-05T00:00:00Z" },
			{ id: "t2", state: "completed", providerTurnId: "p2", requestedAt: "2026-08-05T00:00:01Z" },
		],
		items: [
			{ kind: "message", id: "u1", turnId: "t1", sequence: 1, revision: 1, role: "user", origin: "human", text: "First task", streaming: false, createdAt: "" },
			{ kind: "message", id: "u2", turnId: "t2", sequence: 2, revision: 1, role: "user", origin: "human", text: "Queued task", streaming: false, createdAt: "" },
			{ kind: "message", id: "a1", turnId: "t1", sequence: 3, revision: 1, role: "assistant", origin: "provider", text: "First answer", streaming: false, createdAt: "" },
			{ kind: "message", id: "a2", turnId: "t2", sequence: 4, revision: 1, role: "assistant", origin: "provider", text: "Queued answer", streaming: false, createdAt: "" },
		],
	};
}

describe("mobile Chat timeline model", () => {
	it("keeps the empty state outside the inverted conversation surface", () => {
		const value = snapshot();
		value.items = [];
		value.turns = [];
		const buildPlan = (
			timelineModel as unknown as {
				conversationTimelineRenderPlan?: (snapshot: ConversationSnapshot) => {
					kind: "empty" | "list";
					inverted: boolean;
					groups: unknown[];
				};
			}
		).conversationTimelineRenderPlan;

		expect(buildPlan?.(value)).toEqual({ kind: "empty", inverted: false, groups: [] });
	});

	it("puts the latest exchange first in the virtualized render plan", () => {
		expect(latestFirstConversationGroups(snapshot()).map((group) => group.items.map((item) => item.id))).toEqual([
			["u2", "a2"],
			["u1", "a1"],
		]);
	});

	it("keeps queued questions with their own answers instead of strict-sequence interleaving", () => {
		const groups = groupConversationByTurn(snapshot());
		expect(groups.map((group) => group.items.map((item) => item.id))).toEqual([["u1", "a1"], ["u2", "a2"]]);
		expect(conversationMarkers(snapshot())).toMatchObject([
			{ sequence: 1, title: "First task", detail: "First answer" },
			{ sequence: 2, title: "Queued task", detail: "Queued answer" },
		]);
	});

	it("keys a loaded turn by durable identity rather than its current page boundary", () => {
		const value = snapshot();
		value.items = value.items.filter((item) => item.id !== "u1");
		expect(groupConversationByTurn(value).find((group) => group.turnId === "t1")?.key).toBe("turn-t1");
		value.items.unshift(snapshot().items[0]);
		expect(groupConversationByTurn(value).find((group) => group.turnId === "t1")?.key).toBe("turn-t1");
	});

	it("filters usage, reasoning and duplicate plan rows without hiding unknown work", () => {
		const value = snapshot();
		value.turns[0].plan = { steps: [{ text: "Do it", status: "pending" }] };
		value.items.push(
			activity("usage", 5, "t1"), activity("reasoning", 6, "t1"), activity("plan", 7, "t1"), activity("system", 8, "t1"),
		);
		expect(readableConversationItems(value).slice(-1)[0]).toMatchObject({ activityKind: "system" });
	});

	it("gates rollback on the daemon capability, accepted history and idle state", () => {
		const value = snapshot();
		expect(canRollbackTurn(value, value.turns[0])).toBe(true);
		value.turns[1].state = "running";
		expect(canRollbackTurn(value, value.turns[0])).toBe(false);
		value.turns[1].state = "completed";
		value.capabilities = [];
		expect(canRollbackTurn(value, value.turns[0])).toBe(false);
	});

	it("opens failed and live-output activities but keeps settled mechanics collapsed", () => {
		const running = activity("command", 1, "t1");
		running.status = "running";
		running.detail = { output: "tick" };
		expect(activityStartsExpanded(running)).toBe(true);
		running.status = "completed";
		expect(activityStartsExpanded(running)).toBe(false);
		running.status = "failed";
		expect(activityStartsExpanded(running)).toBe(true);
		running.status = "cancelled";
		expect(activityStartsExpanded(running)).toBe(false);
	});

	it("builds nested provider work without hiding or looping malformed events", () => {
		const parent = activity("command", 1, "t1");
		parent.providerItemId = "parent";
		const child = activity("command", 2, "t1");
		child.providerItemId = "child";
		child.detail = { parentProviderItemId: "parent" };
		const orphan = activity("command", 3, "t1");
		orphan.detail = { parentProviderItemId: "missing" };
		const roots = activityHierarchy([parent, child, orphan]);
		expect(roots.map((node) => node.activity.id)).toEqual([parent.id, orphan.id]);
		expect(roots[0].children[0].activity.id).toBe(child.id);
		expect(countActivityNodes(roots)).toBe(3);
		child.status = "running";
		expect(activityNodesRunning(roots)).toBe(true);

		parent.detail = { parentProviderItemId: "child" };
		expect(activityHierarchy([parent, child])).toHaveLength(2);
		expect(countActivityNodes(activityHierarchy([parent, child]))).toBe(2);
	});
});

function activity(kind: ConversationActivity["activityKind"], sequence: number, turnId?: string): ConversationActivity {
	return { kind: "activity", id: `${kind}-${sequence}`, turnId, sequence, revision: 1, activityKind: kind, status: "completed", summary: kind, createdAt: "" };
}
