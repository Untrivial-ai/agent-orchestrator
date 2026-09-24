import { describe, expect, it } from "vitest";
import type { CloudCpClientEvent } from "../../lib/cloud-cp";
import type { WorkspaceSession } from "../../types/workspace";
import { appendCloudEvents, toSnapshot } from "./CloudSessionChatSurface";

const session = {
	id: "session-1",
	workspaceId: "project-1",
	workspaceName: "project",
	title: "Cloud session",
	provider: "codex",
	kind: "worker",
	mode: "chat",
	status: "working",
	updatedAt: "2026-09-22T00:00:00Z",
	prs: [],
} satisfies WorkspaceSession;

describe("CloudSessionChatSurface", () => {
	it("appends only newer events without duplicating replayed pages", () => {
		const event = (sequence: number): CloudCpClientEvent => ({
			sessionId: session.id,
			sequence,
			type: "chat.assistant_delta",
			payload: { text: String(sequence) },
			createdAt: session.updatedAt,
		});
		const existing = [event(1), event(500)];
		const merged = appendCloudEvents(existing, [event(500), event(501)]);
		expect(merged.map((item) => item.sequence)).toEqual([1, 500, 501]);
		expect(merged.at(-1)?.sequence).toBe(501);
	});

	it("projects a durable steer as activity on the active turn", () => {
		const events: CloudCpClientEvent[] = [
			{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "Build it", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 2, type: "chat.turn_started", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 3, type: "chat.turn_steered", payload: { turnId: "turn-1", text: "Prefer tests first", clientMessageId: "message-1" }, createdAt: session.updatedAt },
		];

		const snapshot = toSnapshot(session, events);

		expect(snapshot.items).toContainEqual(expect.objectContaining({
			kind: "activity",
			turnId: "turn-1",
			summary: "Prefer tests first",
			detail: { event: "steer", text: "Prefer tests first", origin: "human", clientMessageId: "message-1" },
		}));
	});
});
