import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { CloudCpClientEvent } from "../../lib/cloud-cp";
import { CloudCpError } from "../../lib/cloud-cp/errors";
import type { WorkspaceSession } from "../../types/workspace";
import { appendCloudEvents, CloudSessionChatSurface, loadCloudChatEvents, toSnapshot } from "./CloudSessionChatSurface";

const cloudMocks = vi.hoisted(() => ({
	listChatEvents: vi.fn(),
	sendSessionMessage: vi.fn(),
	cancelTurn: vi.fn(),
	steerTurn: vi.fn(),
	listChatModels: vi.fn(),
	resumeSession: vi.fn(),
	chatProps: vi.fn(),
}));
vi.mock("../../hooks/useCloudCp", () => ({
	useCloudCp: () => ({ ready: true, client: cloudMocks }),
}));
vi.mock("./ChatWorkspace", () => ({
	ChatWorkspace: (props: unknown) => {
		cloudMocks.chatProps(props);
		return <div data-testid="cloud-chat" />;
	},
}));

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
	it("wakes a paused worker before loading model choices", async () => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [], hasMore: false, nextAfter: 0 });
		cloudMocks.listChatModels.mockReset()
			.mockRejectedValueOnce(new CloudCpError("The session worker is not connected.", { status: 409, code: "WORKER_UNAVAILABLE" }))
			.mockResolvedValue({ models: [{ id: "codex-test", displayName: "Codex Test", default: true }] });
		cloudMocks.resumeSession.mockReset().mockResolvedValue({});
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		await waitFor(() => expect(cloudMocks.resumeSession).toHaveBeenCalledWith("org-1", session.id, { signal: expect.any(AbortSignal) }));
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].models).toEqual([
			{ id: "codex-test", displayName: "Codex Test", default: true },
		]));
	});

	it("shows provider models and sends the selected model and effort with the next turn", async () => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [], hasMore: false, nextAfter: 0 });
		cloudMocks.listChatModels.mockResolvedValue({ models: [{ id: "codex-test", displayName: "Codex Test", default: true, efforts: ["low", "high"] }] });
		cloudMocks.sendSessionMessage.mockResolvedValue({ event: {} });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].models).toEqual([
			{ id: "codex-test", displayName: "Codex Test", default: true, efforts: ["low", "high"] },
		]));
		cloudMocks.chatProps.mock.lastCall?.[0].onChooseSettings({ model: "codex-test", reasoningEffort: "high" });
		await cloudMocks.chatProps.mock.lastCall?.[0].onSend("hello", [], "message-2");
		expect(cloudMocks.sendSessionMessage).toHaveBeenCalledWith("org-1", session.id, {
			text: "hello", model: "codex-test", reasoningEffort: "high",
		}, { idempotencyKey: "message-2" });
	});

	it("does not carry one Cloud session's model selection into another session", async () => {
		cloudMocks.listChatEvents.mockResolvedValue({ events: [], hasMore: false, nextAfter: 0 });
		cloudMocks.listChatModels.mockResolvedValue({ models: [{ id: "codex-test", displayName: "Codex Test", default: true }] });
		cloudMocks.sendSessionMessage.mockResolvedValue({ event: {} });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		const view = render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		cloudMocks.chatProps.mock.lastCall?.[0].onChooseSettings({ model: "codex-test" });
		view.rerender(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, id: "session-2", cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		await cloudMocks.chatProps.mock.lastCall?.[0].onSend("next", [], "message-3");
		expect(cloudMocks.sendSessionMessage).toHaveBeenLastCalledWith("org-1", "session-2", { text: "next" }, { idempotencyKey: "message-3" });
	});

	it("surfaces send errors and sends cancellation to the active turn", async () => {
		cloudMocks.listChatEvents.mockReset().mockResolvedValue({
			events: [
				{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "Run", turnId: "turn-1" }, createdAt: session.updatedAt },
				{ sessionId: session.id, sequence: 2, type: "chat.turn_started", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			],
			hasMore: false,
			nextAfter: 2,
		});
		cloudMocks.sendSessionMessage.mockReset().mockRejectedValue(new Error("send failed"));
		cloudMocks.cancelTurn.mockReset().mockRejectedValue(new Error("cancel failed"));
		cloudMocks.chatProps.mockClear();
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudSessionChatSurface session={{ ...session, cloud: { orgId: "org-1" } }} />
			</QueryClientProvider>,
		);
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].onInterrupt).toBeTypeOf("function"));
		const props = cloudMocks.chatProps.mock.lastCall?.[0];
		await expect(props.onSend("hello", [], "message-1")).rejects.toThrow("send failed");
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].commandError).toBe("send failed"));
		cloudMocks.chatProps.mock.lastCall?.[0].onInterrupt();
		await waitFor(() => expect(cloudMocks.cancelTurn).toHaveBeenCalledWith("org-1", session.id, "turn-1"));
		await waitFor(() => expect(cloudMocks.chatProps.mock.lastCall?.[0].commandError).toBe("cancel failed"));
	});

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

	it("continues a long history from its last sequence", async () => {
		const event = (sequence: number): CloudCpClientEvent => ({
			sessionId: session.id, sequence, type: "chat.assistant_delta",
			payload: { text: String(sequence) }, createdAt: session.updatedAt,
		});
		const listChatEvents = vi.fn().mockResolvedValue({ events: [event(500), event(501)], hasMore: false, nextAfter: 501 });
		const events = await loadCloudChatEvents({ listChatEvents }, "org-1", session.id, [event(1), event(500)]);
		expect(listChatEvents).toHaveBeenCalledWith("org-1", session.id, { after: 500, limit: 500 });
		expect(events.map((item) => item.sequence)).toEqual([1, 500, 501]);
	});

	it("projects completed and interrupted turns without leaving output streaming", () => {
		const events: CloudCpClientEvent[] = [
			{ sessionId: session.id, sequence: 1, type: "chat.user_message", payload: { text: "First", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 2, type: "chat.turn_started", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 3, type: "chat.assistant_delta", payload: { text: "Done", turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 4, type: "chat.turn_completed", payload: { turnId: "turn-1" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 5, type: "chat.user_message", payload: { text: "Second", turnId: "turn-2" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 6, type: "chat.turn_started", payload: { turnId: "turn-2" }, createdAt: session.updatedAt },
			{ sessionId: session.id, sequence: 7, type: "chat.turn_interrupted", payload: { turnId: "turn-2" }, createdAt: session.updatedAt },
		];
		const snapshot = toSnapshot(session, events);
		expect(snapshot.turns.map((turn) => turn.state)).toEqual(["completed", "interrupted"]);
		expect(snapshot.items).toContainEqual(expect.objectContaining({ role: "assistant", text: "Done", streaming: false }));
		expect(snapshot.controller.state).toBe("ready");
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
